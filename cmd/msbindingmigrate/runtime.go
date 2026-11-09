package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type runtime struct {
	db        *gorm.DB
	proxies   *proxyapp.ProxyUseCase
	inspector *asynq.Inspector
	close     func()
	dir       string
	routeMu   sync.Mutex
	routes    map[string]chan struct{}
}

func openRuntime(ctx context.Context, dir string) (*runtime, error) {
	if os.Getenv("MYSQL_DSN") == "" {
		return nil, errors.New("MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("MYSQL_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("database initialization failed")
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(24)
	sqlDB.SetMaxIdleConns(8)
	r := &runtime{db: db, dir: dir, routes: map[string]chan struct{}{}, close: func() { _ = sqlDB.Close() }}
	ok := false
	defer func() {
		if !ok {
			r.close()
		}
	}()
	var count int64
	if db.WithContext(ctx).Table("users").Where("id=1 AND status='active' AND role IN ('admin','super_admin')").Count(&count).Error != nil || count != 1 {
		return nil, errors.New("operator 1 must be an active administrator")
	}
	settings, err := settingsinfra.NewRepository(db).List(ctx)
	if err != nil {
		return nil, errors.New("system settings could not be read")
	}
	runtimeconfig.Replace(settings)
	domains, allowed, err := coreinfra.NewResourceRepo(db).ListBindingDomains(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, domain := range allowed {
		if domain == "aishop6.com" {
			found = true
		}
	}
	if !found {
		return nil, errors.New("aishop6.com must allow new system bindings")
	}
	msacl.SetAuxiliaryDomainPolicy(domains, []string{"aishop6.com"})
	client, err := minio.New(os.Getenv("MINIO_ENDPOINT"), &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY"), ""), Secure: strings.EqualFold(os.Getenv("MINIO_USE_SSL"), "true")})
	if err != nil {
		return nil, errors.New("object store initialization failed")
	}
	reader := mailinfra.NewMSACLMailboxReaderWithContentWindow(db, governanceinfra.NewMinIOFileStore(client, os.Getenv("MINIO_BUCKET")), 24*time.Hour)
	msacl.SetMailboxReader(reader)
	msacl.SetRecoveryLeaseStore(mailinfra.NewMicrosoftBindingRecoveryLeaseStore(db))
	redis := asynq.RedisClientOpt{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD"), PoolSize: 4}
	r.inspector = asynq.NewInspector(redis)
	queue := asynq.NewClient(redis)
	r.close = func() { _ = r.inspector.Close(); _ = queue.Close(); _ = sqlDB.Close() }
	if err := r.checkQueues(); err != nil {
		return nil, err
	}
	module, err := proxyapi.NewProxyModule(db, queue)
	if err != nil {
		return nil, err
	}
	r.proxies = module.ProxyUseCase
	ok = true
	return r, nil
}

func (r *runtime) checkQueues() error {
	for _, name := range []string{platform.QueueBackgroundValidation, platform.QueueBackgroundProjectHistory, platform.QueueBackgroundAlias} {
		q, err := r.inspector.GetQueueInfo(name)
		if err != nil || !q.Paused || q.Active != 0 {
			return errors.New("microsoft maintenance queues must remain paused and idle")
		}
	}
	return nil
}

// A route stays exclusive for the entire account; no IP change midway through proof verification.
func (r *runtime) lockRoute(ctx context.Context, route string) (func(), error) {
	r.routeMu.Lock()
	ch := r.routes[route]
	if ch == nil {
		ch = make(chan struct{}, 1)
		r.routes[route] = ch
	}
	r.routeMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func releaseRecoveryLeases(ctx context.Context, id uint) {
	if release := msacl.RecoveryLeaseReleaser(ctx); release != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := release(cleanup); err != nil {
			slog.Warn("committed recovery lease cleanup deferred", "resource_id", id)
		}
	}
}
