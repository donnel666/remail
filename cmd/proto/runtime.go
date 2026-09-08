package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/proto/domain"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type commandRuntime struct {
	db      *gorm.DB
	service *protoinfra.Service
	fetch   func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error)
	close   func()
}

func openRuntime(ctx context.Context, opts options) (*commandRuntime, error) {
	if opts.ResourceID == 0 {
		return nil, nil
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		return nil, safeError("MYSQL_DSN is required for database resource selection")
	}
	// Never echo a driver/SQL error containing credentials. CLI reports safe categories.
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, safeError("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, safeError("database pool is unavailable")
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, safeError("database connection failed")
	}
	rt := &commandRuntime{db: db, service: protoinfra.NewService(db), close: func() { _ = sqlDB.Close() }}
	if !opts.Apply || opts.Mode == "login" {
		return rt, nil
	}
	settings, err := settingsinfra.NewRepository(db).List(ctx)
	if err != nil {
		rt.close()
		return nil, safeError("runtime settings are unavailable")
	}
	runtimeconfig.Replace(settings)
	redisDB := 0
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		redisDB, err = strconv.Atoi(raw)
		if err != nil || redisDB < 0 {
			rt.close()
			return nil, safeError("REDIS_DB must be non-negative")
		}
	}
	address := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if address == "" {
		address = "127.0.0.1:6379"
	}
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: address, Password: os.Getenv("REDIS_PASSWORD"), DB: redisDB, PoolSize: 4})
	rt.close = func() { _ = queue.Close(); _ = sqlDB.Close() }
	rt.service.Queue = queue
	rt.service.SessionSecret = os.Getenv("SESSION_SECRET")
	rt.service.Protocol = proton.NewPKLClient()
	rt.service.OperationLogs = governanceinfra.NewOperationLogRepo(db)
	rt.service.SystemLogs = governanceinfra.NewSystemLogRepo(db)
	proxies, err := proxyapi.NewProxyModule(db, queue)
	if err != nil {
		rt.close()
		return nil, safeError("production proxy service is unavailable")
	}
	rt.service.Proxies = proxies.ProxyUseCase
	rt.fetch = rt.service.FetchMailbox
	if opts.Mode == "fetch" && rt.service.SessionSecret == "" {
		rt.close()
		return nil, safeError("SESSION_SECRET is required to load the encrypted Proto session")
	}
	return rt, nil
}

func (rt *commandRuntime) requireOperator(ctx context.Context, id uint) error {
	var user struct {
		Role   iamdomain.Role
		Status iamdomain.UserStatus
	}
	if id == 0 {
		return safeError("an enabled administrator operator is required")
	}
	err := rt.db.WithContext(ctx).Table("users").Select("role", "status").Where("id = ?", id).Take(&user).Error
	if err != nil || !user.Role.HasAdminAccess() || !user.Status.IsActive() {
		return safeError("operator must be an enabled administrator")
	}
	return nil
}

func (rt *commandRuntime) loginCredential(ctx context.Context, resource protoinfra.Resource) (domain.ImportLine, error) {
	if resource.Status == domain.StatusDeleted {
		return domain.ImportLine{}, domain.ErrResourceMissing
	}
	var row struct{ Password string }
	err := rt.db.WithContext(ctx).Table("proto_resources").Select("password").Where("id = ? AND resource_type = 'proto' AND version = ? AND credential_revision = ?", resource.ID, resource.Version, resource.CredentialRevision).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ImportLine{}, domain.ErrInvalidClaim
	}
	if err != nil {
		return domain.ImportLine{}, err
	}
	if row.Password == "" {
		return domain.ImportLine{}, safeError("resource login password is not configured")
	}
	return domain.ImportLine{Email: resource.EmailAddress, Password: row.Password}, nil
}

func loginProxy(ctx context.Context, opts options, rt *commandRuntime) (string, error) {
	if opts.Direct {
		return "", nil
	}
	if opts.ProxyEnv != "" {
		value, err := proxydomain.NormalizeProxyURL(os.Getenv(opts.ProxyEnv))
		if err != nil {
			return "", safeError("selected proxy environment variable must contain a valid proxy URL")
		}
		return value, nil
	}
	if rt == nil || rt.db == nil || opts.ResourceID == 0 {
		return "", safeError("login requires an existing proxy binding, -direct, or -proxy-env")
	}
	var proxy struct{ URL string }
	now := time.Now().UTC()
	// Same eligibility as the production sticky binding, but no Acquire, lease
	// touch, expiry marking, health report, failover, or proxy rotation.
	err := rt.db.WithContext(ctx).Table("proxy_bindings AS b").Select("p.url").
		Joins("JOIN proxies AS p ON p.id = b.proxy_id").
		Joins("JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("b.bind_key = ? AND b.expire_at > ? AND b.ip_version IN ('ipv4', 'ipv6')", fmt.Sprintf("proto:%d", opts.ResourceID), now).
		Where("p.pool = 'resource' AND p.status = 'normal' AND (p.expire_at IS NULL OR p.expire_at > ?)", now).
		Where("s.health_status = 'healthy' AND s.admin_status IN ('online', 'draining')").
		Order("b.last_used_at DESC, b.id DESC").Take(&proxy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", safeError("no valid existing Proto proxy binding; choose explicit -direct or -proxy-env")
	}
	if err != nil {
		return "", safeError("existing Proto proxy binding could not be read")
	}
	value, err := proxydomain.NormalizeProxyURL(proxy.URL)
	if err != nil {
		return "", safeError("existing Proto proxy binding has an invalid URL")
	}
	return value, nil
}

func (rt *commandRuntime) sessionMetadata(ctx context.Context, resource protoinfra.Resource) (bool, bool, error) {
	var row struct{ CredentialRevision uint64 }
	err := rt.db.WithContext(ctx).Table("proto_sessions").Select("credential_revision").Where("resource_id = ?", resource.ID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, row.CredentialRevision == resource.CredentialRevision, nil
}
