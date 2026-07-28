package infra

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProxyModel struct {
	ID                  uint       `gorm:"primaryKey;autoIncrement"`
	ProxyServerID       uint       `gorm:"not null;column:proxy_server_id"`
	Pool                string     `gorm:"type:varchar(16);not null"`
	URL                 string     `gorm:"type:varchar(1024);not null;column:url"`
	URLHash             string     `gorm:"type:char(64);not null;column:url_hash"`
	URLHost             string     `gorm:"type:varchar(255);not null;column:url_host"`
	ExpireAt            *time.Time `gorm:"column:expire_at"`
	IPVersion           string     `gorm:"type:varchar(8);not null;column:ip_version"`
	OutboundIP          string     `gorm:"type:varchar(64);not null;column:outbound_ip"`
	Country             string     `gorm:"type:varchar(64);not null"`
	LatencyMs           int        `gorm:"not null;column:latency_ms"`
	Status              string     `gorm:"type:varchar(32);not null"`
	Errors              int        `gorm:"not null"`
	LastSafeError       string     `gorm:"type:varchar(500);not null;column:last_safe_error"`
	CheckOperatorUserID uint       `gorm:"not null;default:0;column:check_operator_user_id"`
	CheckRequestID      string     `gorm:"type:varchar(64);not null;default:'';column:check_request_id"`
	CheckPath           string     `gorm:"type:varchar(255);not null;default:'';column:check_path"`
	CheckGeneration     uint64     `gorm:"not null;default:1;column:check_generation"`
	LastCheckedAt       *time.Time `gorm:"column:last_checked_at"`
	LastUsedAt          *time.Time `gorm:"column:last_used_at"`
	LastAssignedAt      *time.Time `gorm:"column:last_assigned_at"`
	CreatedAt           time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt           time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ProxyModel) TableName() string {
	return "proxies"
}

type ProxyServerModel struct {
	ID                   uint       `gorm:"primaryKey;autoIncrement"`
	ServerIP             string     `gorm:"type:varchar(255);not null;column:server_ip"`
	Name                 string     `gorm:"type:varchar(255);not null"`
	SourceType           string     `gorm:"type:varchar(16);not null;column:source_type"`
	CapacityWeight       uint       `gorm:"not null;column:capacity_weight"`
	AdminStatus          string     `gorm:"type:varchar(16);not null;column:admin_status"`
	HealthStatus         string     `gorm:"type:varchar(16);not null;column:health_status"`
	HealthFailures       uint       `gorm:"not null;column:health_failures"`
	HealthGeneration     uint64     `gorm:"not null;column:health_generation"`
	LastHealthError      string     `gorm:"type:varchar(500);not null;column:last_health_error"`
	LastHealthCheckedAt  *time.Time `gorm:"column:last_health_checked_at"`
	NextHealthCheckAt    time.Time  `gorm:"not null;column:next_health_check_at"`
	InventoryStatus      string     `gorm:"type:varchar(16);not null;column:inventory_status"`
	LastFailoverLoggedAt *time.Time `gorm:"column:last_failover_logged_at"`
	CreatedAt            time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt            time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ProxyServerModel) TableName() string {
	return "proxy_servers"
}

type ProxyBindingModel struct {
	ID         uint       `gorm:"primaryKey;autoIncrement"`
	BindKey    string     `gorm:"type:varchar(255);not null;column:bind_key"`
	ProxyID    uint       `gorm:"not null;column:proxy_id"`
	IPVersion  string     `gorm:"type:varchar(8);not null;column:ip_version"`
	ExpireAt   time.Time  `gorm:"not null;column:expire_at"`
	CreatedAt  time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
}

func (ProxyBindingModel) TableName() string {
	return "proxy_bindings"
}

type ProxyRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
	systemLogs    *governanceinfra.SystemLogRepo
}

const (
	transactionRetryAttempts       = 8
	proxyServerFailoverLogInterval = 5 * time.Minute
)

var errRetryProxyAcquire = errors.New("retry proxy acquire")

func NewProxyRepo(db *gorm.DB) *ProxyRepo {
	return &ProxyRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		systemLogs:    governanceinfra.NewSystemLogRepo(db),
	}
}

func (r *ProxyRepo) Create(ctx context.Context, proxy *domain.Proxy) error {
	return r.createInTx(ctx, r.db.WithContext(ctx), proxy)
}

func (r *ProxyRepo) CreateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.createInTx(ctx, tx, proxy); err != nil {
			return err
		}
		return r.createOperationLogInTx(ctx, tx, log, fmt.Sprintf("%d", proxy.ID), "")
	})
}

func (r *ProxyRepo) CreateBatchWithLog(ctx context.Context, proxies []*domain.Proxy, log *governancedomain.OperationLog) ([]domain.Proxy, int, error) {
	if len(proxies) == 0 {
		return nil, 0, domain.ErrInvalidProxyFilter
	}

	created := make([]domain.Proxy, 0, len(proxies))
	duplicates := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		serverIDs := make(map[string]uint)
		for _, proxy := range proxies {
			if proxy == nil {
				continue
			}
			serverIP := proxyURLHost(proxy.URL)
			serverID, ok := serverIDs[serverIP]
			if !ok {
				var err error
				serverID, err = ensureProxyServer(ctx, tx, serverIP)
				if err != nil {
					return err
				}
				serverIDs[serverIP] = serverID
			}
			proxy.ProxyServerID = serverID
			if err := r.createInTx(ctx, tx, proxy); err != nil {
				if errors.Is(err, domain.ErrDuplicateProxy) {
					duplicates++
					continue
				}
				return err
			}
			created = append(created, *proxy)
		}
		summary := fmt.Sprintf("Proxy imported. Created: %d. Duplicated: %d.", len(created), duplicates)
		return r.createOperationLogInTx(ctx, tx, log, "batch", summary)
	})
	if err != nil {
		return nil, 0, err
	}
	return created, duplicates, nil
}

func (r *ProxyRepo) createInTx(ctx context.Context, tx *gorm.DB, proxy *domain.Proxy) error {
	model := proxyModel(proxy)
	if model.ProxyServerID == 0 {
		serverID, err := ensureProxyServer(ctx, tx, model.URLHost)
		if err != nil {
			return err
		}
		model.ProxyServerID = serverID
		proxy.ProxyServerID = serverID
	}
	if err := tx.WithContext(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrDuplicateProxy
		}
		return fmt.Errorf("create proxy: %w", err)
	}
	*proxy = proxyFromModel(*model)
	return nil
}

func (r *ProxyRepo) FindByID(ctx context.Context, id uint) (*domain.Proxy, error) {
	var model ProxyModel
	err := r.db.WithContext(ctx).First(&model, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find proxy: %w", err)
	}
	proxy := proxyFromModel(model)
	return &proxy, nil
}

func (r *ProxyRepo) List(ctx context.Context, filter proxyapp.ProxyListFilter, offset, limit int) ([]domain.Proxy, error) {
	var models []ProxyModel
	db := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	if err := db.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	items := make([]domain.Proxy, len(models))
	for i, model := range models {
		items[i] = proxyFromModel(model)
	}
	return items, nil
}

func (r *ProxyRepo) Count(ctx context.Context, filter proxyapp.ProxyListFilter) (int64, error) {
	var total int64
	db := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	if err := db.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count proxies: %w", err)
	}
	return total, nil
}

func (r *ProxyRepo) CountDisableCandidates(ctx context.Context, filter proxyapp.ProxyListFilter) (int64, error) {
	var total int64
	db := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter).
		Where("status <> ?", string(domain.ProxyStatusDisabled))
	if err := db.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count disable proxy candidates: %w", err)
	}
	return total, nil
}

func (r *ProxyRepo) Stats(ctx context.Context, filter proxyapp.ProxyListFilter) (*proxyapp.ProxyStats, error) {
	base := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count proxy stats: %w", err)
	}
	facetFilter := filter
	facetFilter.Country = ""
	countries, err := r.groupProxyCounts(ctx, facetFilter, "country")
	if err != nil {
		return nil, err
	}
	facetFilter = filter
	facetFilter.Status = ""
	statuses, err := r.groupProxyCounts(ctx, facetFilter, "status")
	if err != nil {
		return nil, err
	}
	facetFilter = filter
	facetFilter.Pool = ""
	pools, err := r.groupProxyCounts(ctx, facetFilter, "pool")
	if err != nil {
		return nil, err
	}
	facetFilter = filter
	facetFilter.IPVersion = ""
	facetFilter.IPv6 = nil
	ipVersions, err := r.groupProxyCounts(ctx, facetFilter, "ip_version")
	if err != nil {
		return nil, err
	}
	return &proxyapp.ProxyStats{
		Total:      total,
		Countries:  countries,
		Statuses:   statuses,
		Pools:      pools,
		IPVersions: ipVersions,
	}, nil
}

func (r *ProxyRepo) groupProxyCounts(ctx context.Context, filter proxyapp.ProxyListFilter, column string) ([]proxyapp.ProxyCount, error) {
	var rows []struct {
		Key   string `gorm:"column:key"`
		Count int64  `gorm:"column:count"`
	}
	db := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	if err := db.
		Select(column + " AS `key`, COUNT(*) AS `count`").
		Group(column).
		Order(column + " ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("group proxy counts by %s: %w", column, err)
	}
	items := make([]proxyapp.ProxyCount, len(rows))
	for i, row := range rows {
		items[i] = proxyapp.ProxyCount{Key: row.Key, Count: row.Count}
	}
	return items, nil
}

func (r *ProxyRepo) ListBindings(ctx context.Context, filter proxyapp.ProxyBindingListFilter, offset, limit int) ([]domain.Binding, error) {
	var models []ProxyBindingModel
	db := applyProxyBindingListFilter(r.db.WithContext(ctx).Model(&ProxyBindingModel{}), filter)
	if err := db.Order("expire_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list proxy bindings: %w", err)
	}
	items := make([]domain.Binding, len(models))
	for i, model := range models {
		items[i] = bindingFromModel(model)
	}
	return items, nil
}

func (r *ProxyRepo) CountBindings(ctx context.Context, filter proxyapp.ProxyBindingListFilter) (int64, error) {
	var total int64
	db := applyProxyBindingListFilter(r.db.WithContext(ctx).Model(&ProxyBindingModel{}), filter)
	if err := db.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count proxy bindings: %w", err)
	}
	return total, nil
}

// ListAdminResourceProxyBindings returns the current stored proxy-binding
// facts for resource email addresses. It intentionally selects the parsed host
// instead of the raw proxy URL so a cross-context administrator read cannot
// expose proxy credentials.
func (r *ProxyRepo) ListAdminResourceProxyBindings(ctx context.Context, keys []string) ([]proxyapp.AdminResourceProxyBinding, error) {
	keys = normalizeProxyBindingKeys(keys)
	if len(keys) == 0 {
		return []proxyapp.AdminResourceProxyBinding{}, nil
	}
	var rows []struct {
		BindKey    string    `gorm:"column:bind_key"`
		ProxyID    uint      `gorm:"column:proxy_id"`
		Host       string    `gorm:"column:host"`
		OutboundIP string    `gorm:"column:outbound_ip"`
		Country    string    `gorm:"column:country"`
		IPVersion  string    `gorm:"column:ip_version"`
		Status     string    `gorm:"column:status"`
		ExpireAt   time.Time `gorm:"column:expire_at"`
	}
	if err := r.db.WithContext(ctx).Table("proxy_bindings AS binding").
		Select(`binding.bind_key, binding.proxy_id, proxy.url_host AS host,
			proxy.outbound_ip, proxy.country, binding.ip_version, proxy.status,
			binding.expire_at`).
		Joins("JOIN proxies AS proxy ON proxy.id = binding.proxy_id").
		Where("binding.bind_key IN ?", keys).
		Order("binding.bind_key ASC, binding.expire_at DESC, binding.id DESC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list admin resource proxy bindings: %w", err)
	}
	items := make([]proxyapp.AdminResourceProxyBinding, len(rows))
	for i := range rows {
		items[i] = proxyapp.AdminResourceProxyBinding{
			BindKey:    rows[i].BindKey,
			ProxyID:    rows[i].ProxyID,
			Host:       rows[i].Host,
			OutboundIP: rows[i].OutboundIP,
			Country:    rows[i].Country,
			IPVersion:  rows[i].IPVersion,
			Status:     rows[i].Status,
			ExpireAt:   rows[i].ExpireAt,
		}
	}
	return items, nil
}

func (r *ProxyRepo) Update(ctx context.Context, proxy *domain.Proxy) error {
	return r.updateInTx(ctx, r.db.WithContext(ctx), proxy)
}

func (r *ProxyRepo) UpdateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.updateInTx(ctx, tx, proxy); err != nil {
			return err
		}
		return r.createOperationLogInTx(ctx, tx, log, fmt.Sprintf("%d", proxy.ID), "")
	})
}

func (r *ProxyRepo) UpdateWithLogAndBumpCheckGeneration(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current ProxyModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, check_generation").First(&current, "id = ?", proxy.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProxyNotFound
			}
			return fmt.Errorf("lock proxy check generation: %w", err)
		}
		proxy.CheckGeneration = current.CheckGeneration + 1
		if err := r.updateInTx(ctx, tx, proxy); err != nil {
			return err
		}
		return r.createOperationLogInTx(ctx, tx, log, fmt.Sprintf("%d", proxy.ID), "")
	})
}

func (r *ProxyRepo) updateInTx(ctx context.Context, tx *gorm.DB, proxy *domain.Proxy) error {
	model := proxyModel(proxy)
	serverID, err := ensureProxyServer(ctx, tx, model.URLHost)
	if err != nil {
		return err
	}
	model.ProxyServerID = serverID
	proxy.ProxyServerID = serverID
	result := tx.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("id = ?", proxy.ID).
		Updates(map[string]any{
			"proxy_server_id":        model.ProxyServerID,
			"url":                    model.URL,
			"url_hash":               model.URLHash,
			"url_host":               model.URLHost,
			"expire_at":              model.ExpireAt,
			"ip_version":             model.IPVersion,
			"outbound_ip":            model.OutboundIP,
			"country":                model.Country,
			"latency_ms":             model.LatencyMs,
			"status":                 model.Status,
			"errors":                 model.Errors,
			"last_safe_error":        model.LastSafeError,
			"check_operator_user_id": model.CheckOperatorUserID,
			"check_request_id":       model.CheckRequestID,
			"check_path":             model.CheckPath,
			"check_generation":       model.CheckGeneration,
			"last_checked_at":        model.LastCheckedAt,
			"last_used_at":           model.LastUsedAt,
			"last_assigned_at":       model.LastAssignedAt,
		})
	if isDuplicateKeyError(result.Error) {
		return domain.ErrDuplicateProxy
	}
	if result.Error != nil {
		return fmt.Errorf("update proxy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrProxyNotFound
	}
	return nil
}

func (r *ProxyRepo) DeleteBatch(ctx context.Context, ids []uint) ([]uint, error) {
	return r.deleteBatchWithTxLog(ctx, ids, nil)
}

func (r *ProxyRepo) DeleteBatchWithLog(ctx context.Context, ids []uint, log *governancedomain.OperationLog) ([]uint, error) {
	return r.deleteBatchWithTxLog(ctx, ids, log)
}

func (r *ProxyRepo) deleteBatchWithTxLog(ctx context.Context, ids []uint, log *governancedomain.OperationLog) ([]uint, error) {
	if len(ids) == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	var deletedIDs []uint
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ProxyModel{}).
			Where("id IN ?", ids).
			Pluck("id", &deletedIDs).Error; err != nil {
			return fmt.Errorf("find proxies for delete: %w", err)
		}
		if len(deletedIDs) == 0 {
			summary := "Proxy deleted. Count: 0."
			return r.createOperationLogInTx(ctx, tx, log, "batch", summary)
		}
		if err := tx.Delete(&ProxyModel{}, deletedIDs).Error; err != nil {
			return fmt.Errorf("delete proxies: %w", err)
		}
		summary := fmt.Sprintf("Proxy deleted. Count: %d.", len(deletedIDs))
		return r.createOperationLogInTx(ctx, tx, log, "batch", summary)
	})
	if err != nil {
		return nil, err
	}
	return deletedIDs, nil
}

func (r *ProxyRepo) DeleteByFilter(ctx context.Context, filter proxyapp.ProxyListFilter) (int64, error) {
	return r.deleteByFilterWithTxLog(ctx, filter, nil)
}

func (r *ProxyRepo) DeleteByFilterWithLog(ctx context.Context, filter proxyapp.ProxyListFilter, log *governancedomain.OperationLog) (int64, error) {
	return r.deleteByFilterWithTxLog(ctx, filter, log)
}

func (r *ProxyRepo) DisableByFilterWithLog(ctx context.Context, filter proxyapp.ProxyListFilter, log *governancedomain.OperationLog) (int64, error) {
	var disabled int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := applyProxyListFilter(tx.Model(&ProxyModel{}), filter).
			Where("status <> ?", string(domain.ProxyStatusDisabled)).
			Updates(map[string]any{
				"status":          string(domain.ProxyStatusDisabled),
				"last_safe_error": "Proxy disabled by administrator.",
				"updated_at":      time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("disable proxies by filter: %w", result.Error)
		}
		disabled = result.RowsAffected
		summary := fmt.Sprintf("Proxy disabled. Count: %d.", disabled)
		return r.createOperationLogInTx(ctx, tx, log, "filter", summary)
	})
	if err != nil {
		return 0, err
	}
	return disabled, nil
}

func (r *ProxyRepo) MarkPendingBatchWithLog(ctx context.Context, ids []uint, log *governancedomain.OperationLog) (int, int, error) {
	if len(ids) == 0 {
		return 0, 0, domain.ErrInvalidProxyFilter
	}
	var matched, updated int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ProxyModel{}).Where("id IN ?", ids).Count(&matched).Error; err != nil {
			return fmt.Errorf("count proxies for check: %w", err)
		}
		updates := proxyCheckMetadataUpdates(log)
		updates["status"] = string(domain.ProxyStatusPending)
		updates["errors"] = 0
		updates["last_safe_error"] = ""
		updates["check_generation"] = gorm.Expr("check_generation + 1")
		updates["updated_at"] = time.Now().UTC()
		result := tx.Model(&ProxyModel{}).Where("id IN ?", ids).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark proxies pending: %w", result.Error)
		}
		updated = result.RowsAffected
		return r.createOperationLogInTx(ctx, tx, log, "batch", fmt.Sprintf("Proxy batch check queued. Count: %d.", updated))
	})
	return int(matched), int(updated), err
}

func (r *ProxyRepo) MarkPendingByFilterWithLog(ctx context.Context, filter proxyapp.ProxyListFilter, log *governancedomain.OperationLog) (int64, int64, error) {
	var matched, updated int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := applyProxyListFilter(tx.Model(&ProxyModel{}), filter).Count(&matched).Error; err != nil {
			return fmt.Errorf("count proxies for check: %w", err)
		}
		if matched == 0 {
			return nil
		}
		updates := proxyCheckMetadataUpdates(log)
		updates["status"] = string(domain.ProxyStatusPending)
		updates["errors"] = 0
		updates["last_safe_error"] = ""
		updates["check_generation"] = gorm.Expr("check_generation + 1")
		updates["updated_at"] = time.Now().UTC()
		result := applyProxyListFilter(tx.Model(&ProxyModel{}), filter).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark proxies pending by filter: %w", result.Error)
		}
		updated = result.RowsAffected
		return r.createOperationLogInTx(ctx, tx, log, "filter", fmt.Sprintf("Proxy batch check queued. Count: %d.", updated))
	})
	return matched, updated, err
}

func (r *ProxyRepo) deleteByFilterWithTxLog(ctx context.Context, filter proxyapp.ProxyListFilter, log *governancedomain.OperationLog) (int64, error) {
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := applyProxyListFilter(tx.Model(&ProxyModel{}), filter).
			Where("1 = 1").
			Delete(&ProxyModel{})
		if result.Error != nil {
			return fmt.Errorf("delete proxies by filter: %w", result.Error)
		}
		deleted = result.RowsAffected
		summary := fmt.Sprintf("Proxy deleted. Count: %d.", deleted)
		return r.createOperationLogInTx(ctx, tx, log, "filter", summary)
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func (r *ProxyRepo) MarkExpiredBefore(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("expire_at IS NOT NULL AND expire_at <= ?", now).
		Where("status IN ?", []string{string(domain.ProxyStatusNormal), string(domain.ProxyStatusAbnormal)}).
		Updates(map[string]any{
			"status":          string(domain.ProxyStatusExpired),
			"last_safe_error": "Proxy has expired.",
			"updated_at":      now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("mark expired proxies: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *ProxyRepo) ListPendingProxyChecks(ctx context.Context, limit int) ([]proxyapp.ProxyCheckTask, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []ProxyModel
	if err := r.db.WithContext(ctx).
		Select("id, check_generation").
		Where("status = ?", string(domain.ProxyStatusPending)).
		Order("updated_at ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending proxy checks: %w", err)
	}
	tasks := make([]proxyapp.ProxyCheckTask, 0, len(models))
	for i := range models {
		tasks = append(tasks, proxyapp.ProxyCheckTask{
			ProxyID:         models[i].ID,
			CheckGeneration: models[i].CheckGeneration,
		})
	}
	return tasks, nil
}

func (r *ProxyRepo) ActivateProxyCheck(ctx context.Context, id uint, generation uint64) (bool, error) {
	if id == 0 || generation == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).Model(&ProxyModel{}).
		Where("id = ? AND status = ? AND check_generation = ?", id, string(domain.ProxyStatusPending), generation).
		Updates(map[string]any{
			"status":     string(domain.ProxyStatusChecking),
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("activate proxy check: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ProxyRepo) ReleaseProxyCheckInfrastructureFailure(ctx context.Context, id uint, generation uint64, safeError string) (bool, error) {
	if id == 0 || generation == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).Model(&ProxyModel{}).
		Where("id = ? AND status = ? AND check_generation = ?", id, string(domain.ProxyStatusChecking), generation).
		Updates(map[string]any{
			"status":           string(domain.ProxyStatusPending),
			"check_generation": gorm.Expr("check_generation + 1"),
			"last_safe_error":  domain.SafeProxyError(safeError),
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("release proxy check infrastructure failure: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ProxyRepo) UpdateCheckResult(ctx context.Context, id uint, result domain.CheckResult, success bool) (*domain.Proxy, error) {
	return r.updateCheckResultWithTxLog(ctx, id, 0, result, success, nil)
}

func (r *ProxyRepo) UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error) {
	return r.updateCheckResultWithTxLog(ctx, id, 0, result, success, log)
}

func (r *ProxyRepo) UpdateCheckResultForGenerationWithLog(ctx context.Context, id uint, generation uint64, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error) {
	return r.updateCheckResultWithTxLog(ctx, id, generation, result, success, log)
}

func (r *ProxyRepo) updateCheckResultWithTxLog(ctx context.Context, id uint, generation uint64, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error) {
	var updated domain.Proxy
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		proxy, err := updateCheckResultInTx(ctx, tx, id, generation, result, success)
		if err != nil {
			return err
		}
		updated = proxy
		return r.createOperationLogInTx(ctx, tx, log, fmt.Sprintf("%d", id), "")
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func updateCheckResultInTx(ctx context.Context, tx *gorm.DB, id uint, generation uint64, result domain.CheckResult, success bool) (domain.Proxy, error) {
	var model ProxyModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Proxy{}, domain.ErrProxyNotFound
		}
		return domain.Proxy{}, fmt.Errorf("lock proxy for check: %w", err)
	}
	proxy := proxyFromModel(model)
	if proxy.Status != domain.ProxyStatusChecking {
		return domain.Proxy{}, domain.ErrInvalidProxyStatus
	} else if generation != 0 && proxy.CheckGeneration != generation {
		return domain.Proxy{}, domain.ErrInvalidProxyStatus
	} else if success {
		if err := proxy.ApplyCheckSuccess(result); err != nil {
			return domain.Proxy{}, err
		}
	} else if err := proxy.ApplyCheckFailure(result); err != nil {
		return domain.Proxy{}, err
	}
	if err := tx.WithContext(ctx).Model(&ProxyModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"ip_version":      string(proxy.IPVersion),
			"outbound_ip":     proxy.OutboundIP,
			"country":         proxy.Country,
			"latency_ms":      proxy.LatencyMs,
			"status":          string(proxy.Status),
			"errors":          proxy.Errors,
			"last_safe_error": proxy.LastSafeError,
			"last_checked_at": proxy.LastCheckedAt,
			"last_used_at":    proxy.LastUsedAt,
			"updated_at":      time.Now().UTC(),
		}).Error; err != nil {
		return domain.Proxy{}, fmt.Errorf("update proxy check result: %w", err)
	}
	return proxy, nil
}

func (r *ProxyRepo) AcquireResourceProxy(ctx context.Context, key string, ipVersion domain.ProxyIPVersion, now time.Time, bindingTTL time.Duration) (*domain.Proxy, error) {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return nil, domain.ErrProxyBindingInvalid
	}
	if ipVersion == "" {
		ipVersion = domain.ProxyIPAuto
	}
	var selected *domain.Proxy
	var failover *proxyServerFailover
	err := withTransactionRetry(func() error {
		selected = nil
		failover = nil
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if bound, err := findBoundResourceProxy(ctx, tx, key, ipVersion, now); err != nil {
				return err
			} else if bound != nil {
				selected = bound
				return nil
			}

			servers, err := listEligibleProxyServers(ctx, tx, domain.ProxyPoolResource, ipVersion, now)
			if err != nil {
				return err
			}
			var proxy *domain.Proxy
			orderedServers := orderProxyServers(key, servers)
			preferredFailure := ""
			// ponytail: fallback is O(eligible servers); batch availability summaries
			// only if profiling shows hundreds of misses on the common path.
			for index, server := range orderedServers {
				eligible, err := lockEligibleProxyServer(ctx, tx, server.ID)
				if err != nil {
					return err
				}
				if !eligible {
					if index == 0 {
						preferredFailure = "preferred server became ineligible"
					}
					continue
				}
				proxy, err = selectResourceProxy(ctx, tx, server.ID, ipVersion, now)
				if err == nil {
					if index > 0 {
						failover = newProxyServerFailover(orderedServers[0].ID, server.ID, preferredFailure)
					}
					break
				}
				if !errors.Is(err, domain.ErrProxyUnavailable) {
					return err
				}
				if index == 0 {
					preferredFailure = "preferred server had no selectable or lockable resource route"
				}
			}
			if proxy == nil {
				return errRetryProxyAcquire
			}
			bindingExpireAt := now.Add(bindingTTL)
			if !proxy.ExpireAt.IsZero() && proxy.ExpireAt.Before(bindingExpireAt) {
				bindingExpireAt = proxy.ExpireAt
			}
			covered, err := coverInvalidBinding(ctx, tx, key, proxy, bindingExpireAt, now)
			if err != nil {
				return err
			}
			if covered {
				if err := touchProxyAssigned(ctx, tx, proxy.ID, now); err != nil {
					return err
				}
				proxy.LastAssignedAt = &now
				proxy.LastUsedAt = &now
				selected = proxy
				return nil
			}
			binding := &ProxyBindingModel{
				BindKey:    key,
				ProxyID:    proxy.ID,
				IPVersion:  string(proxy.IPVersion),
				ExpireAt:   bindingExpireAt,
				LastUsedAt: &now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "bind_key"},
					{Name: "ip_version"},
				},
				DoUpdates: clause.Assignments(map[string]any{
					"last_used_at": now,
				}),
			}).Create(binding).Error; err != nil {
				return fmt.Errorf("create proxy binding: %w", err)
			}
			bound, err := findExactBoundResourceProxy(ctx, tx, key, proxy.IPVersion, now)
			if err != nil {
				return err
			}
			if bound == nil {
				return errRetryProxyAcquire
			}
			if err := touchProxyAssigned(ctx, tx, bound.ID, now); err != nil {
				return err
			}
			bound.LastAssignedAt = &now
			selected = bound
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, errRetryProxyAcquire) {
			return nil, domain.ErrProxyUnavailable
		}
		return nil, err
	}
	if selected == nil {
		return nil, domain.ErrProxyUnavailable
	}
	if failover != nil {
		_ = r.recordProxyServerFailover(ctx, *failover, now)
	}
	// last_used_at is telemetry and remains best effort after commit.
	_ = touchProxyUsed(ctx, r.db, selected.ID, now)
	return selected, nil
}

func (r *ProxyRepo) AcquireSystemProxy(ctx context.Context, ipVersion domain.ProxyIPVersion, now time.Time, selection proxyapp.ProxyServerSelection) (*domain.Proxy, error) {
	if ipVersion == "" {
		ipVersion = domain.ProxyIPAuto
	}
	var selected *domain.Proxy
	var failover *proxyServerFailover
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		servers, err := listEligibleProxyServers(ctx, tx, domain.ProxyPoolSystem, ipVersion, now)
		if err != nil {
			return err
		}
		avoidServerIDs := proxyServerIDSet(selection.AvoidProxyServerIDs)
		orderedServers := orderProxyServers(selection.Seed, servers)
		preferredFailure := ""
		for index, server := range orderedServers {
			if _, avoid := avoidServerIDs[server.ID]; avoid {
				if index == 0 {
					preferredFailure = "preferred server was excluded after a request failure"
				}
				continue
			}
			eligible, err := lockEligibleProxyServer(ctx, tx, server.ID)
			if err != nil {
				return err
			}
			if !eligible {
				if index == 0 {
					preferredFailure = "preferred server became ineligible"
				}
				continue
			}
			model, err := selectSystemProxy(ctx, tx, server.ID, ipVersion, now)
			if errors.Is(err, domain.ErrProxyUnavailable) {
				if index == 0 {
					preferredFailure = "preferred server had no selectable or lockable system route"
				}
				continue
			}
			if err != nil {
				return err
			}
			proxy := proxyFromModel(*model)
			proxy.LastUsedAt = &now
			selected = &proxy
			if index > 0 {
				failover = newProxyServerFailover(orderedServers[0].ID, server.ID, preferredFailure)
			}
			return nil
		}
		return domain.ErrProxyUnavailable
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, domain.ErrProxyUnavailable
	}
	if failover != nil {
		_ = r.recordProxyServerFailover(ctx, *failover, now)
	}
	_ = touchProxyUsed(ctx, r.db, selected.ID, now)
	return selected, nil
}

func (r *ProxyRepo) ReportSuccess(ctx context.Context, proxyID uint, usedAt time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model ProxyModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", proxyID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProxyNotFound
			}
			return fmt.Errorf("lock proxy success report: %w", err)
		}
		proxy := proxyFromModel(model)
		proxy.ReportSuccess(usedAt)
		if err := tx.Model(&ProxyModel{}).
			Where("id = ?", proxyID).
			Updates(map[string]any{
				"errors":          proxy.Errors,
				"last_safe_error": proxy.LastSafeError,
				"last_used_at":    proxy.LastUsedAt,
			}).Error; err != nil {
			return fmt.Errorf("report proxy success: %w", err)
		}
		return nil
	})
}

func (r *ProxyRepo) ReportFailure(ctx context.Context, proxyID uint, safeError string, retryable bool) (*domain.Proxy, error) {
	var updated domain.Proxy
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model ProxyModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", proxyID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrProxyNotFound
			}
			return fmt.Errorf("lock proxy failure report: %w", err)
		}
		proxy := proxyFromModel(model)
		wasPending := proxy.Status == domain.ProxyStatusPending
		if err := proxy.ReportFailure(safeError, retryable); err != nil {
			return err
		}
		if !wasPending && proxy.Status == domain.ProxyStatusPending {
			proxy.CheckGeneration++
			proxy.CheckOperatorUserID = 0
			proxy.CheckRequestID = ""
			proxy.CheckPath = ""
		}
		if err := tx.Model(&ProxyModel{}).
			Where("id = ?", proxyID).
			Updates(map[string]any{
				"status":                 string(proxy.Status),
				"errors":                 proxy.Errors,
				"last_safe_error":        proxy.LastSafeError,
				"check_generation":       proxy.CheckGeneration,
				"check_operator_user_id": proxy.CheckOperatorUserID,
				"check_request_id":       proxy.CheckRequestID,
				"check_path":             proxy.CheckPath,
			}).Error; err != nil {
			return fmt.Errorf("report proxy failure: %w", err)
		}
		updated = proxy
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (r *ProxyRepo) ListDueProxyServerChecks(ctx context.Context, now time.Time, limit int) ([]proxyapp.ProxyServerCheckTask, error) {
	if limit <= 0 {
		limit = 100
	}
	var servers []ProxyServerModel
	if err := r.db.WithContext(ctx).
		Select("id, health_generation").
		Where("admin_status IN ? AND next_health_check_at <= ?", []string{string(domain.ProxyServerAdminOnline), string(domain.ProxyServerAdminDraining)}, now).
		Where("EXISTS (SELECT 1 FROM proxies AS p WHERE p.proxy_server_id = proxy_servers.id AND p.status <> ?)", string(domain.ProxyStatusDisabled)).
		Order("next_health_check_at ASC, id ASC").
		Limit(limit).
		Find(&servers).Error; err != nil {
		return nil, fmt.Errorf("list due proxy server checks: %w", err)
	}
	tasks := make([]proxyapp.ProxyServerCheckTask, len(servers))
	for i := range servers {
		tasks[i] = proxyapp.ProxyServerCheckTask{ProxyServerID: servers[i].ID, HealthGeneration: servers[i].HealthGeneration}
	}
	return tasks, nil
}

func (r *ProxyRepo) MarkProxyServerCheckScheduled(ctx context.Context, task proxyapp.ProxyServerCheckTask, nextCheckAt time.Time) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&ProxyServerModel{}).
		Where("id = ? AND health_generation = ?", task.ProxyServerID, task.HealthGeneration).
		Update("next_health_check_at", nextCheckAt)
	if result.Error != nil {
		return false, fmt.Errorf("mark proxy server check scheduled: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ProxyRepo) FindProxyServerCheckTarget(ctx context.Context, task proxyapp.ProxyServerCheckTask, now time.Time) (*proxyapp.ProxyServerCheckTarget, error) {
	var server ProxyServerModel
	err := r.db.WithContext(ctx).
		Where("id = ? AND health_generation = ? AND admin_status IN ?", task.ProxyServerID, task.HealthGeneration, []string{string(domain.ProxyServerAdminOnline), string(domain.ProxyServerAdminDraining)}).
		First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find proxy server check target: %w", err)
	}
	var urls []string
	if err := r.db.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("proxy_server_id = ? AND status <> ?", server.ID, string(domain.ProxyStatusDisabled)).
		Order(clause.Expr{SQL: "CASE WHEN status = 'normal' AND (expire_at IS NULL OR expire_at > ?) THEN 0 ELSE 1 END ASC", Vars: []any{now}, WithoutParentheses: true}).
		Order("last_checked_at DESC, id ASC").
		Limit(proxyServerCheckTargetLimit).
		Pluck("url", &urls).Error; err != nil {
		return nil, fmt.Errorf("list proxy server check endpoints: %w", err)
	}
	if len(urls) == 0 {
		return nil, nil
	}
	return &proxyapp.ProxyServerCheckTarget{Server: proxyServerFromModel(server), ProxyURLs: urls}, nil
}

func (r *ProxyRepo) CompleteProxyServerCheck(
	ctx context.Context,
	task proxyapp.ProxyServerCheckTask,
	reachable bool,
	safeError string,
	now, nextCheckAt time.Time,
	failureThreshold, inventoryThresholdPercent int,
) (*proxyapp.ProxyServerCheckUpdate, error) {
	var update *proxyapp.ProxyServerCheckUpdate
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var server ProxyServerModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND health_generation = ?", task.ProxyServerID, task.HealthGeneration).
			First(&server).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock proxy server check result: %w", err)
		}
		var inventory struct {
			Active      int64 `gorm:"column:active"`
			Unavailable int64 `gorm:"column:unavailable"`
		}
		if err := tx.Model(&ProxyModel{}).
			Select("COUNT(*) AS active, COALESCE(SUM(status <> ?), 0) AS unavailable", string(domain.ProxyStatusNormal)).
			Where("proxy_server_id = ? AND status <> ? AND (expire_at IS NULL OR expire_at > ?)", server.ID, string(domain.ProxyStatusDisabled), now).
			Scan(&inventory).Error; err != nil {
			return fmt.Errorf("count proxy server inventory: %w", err)
		}
		if failureThreshold < 1 {
			failureThreshold = 1
		}
		if inventoryThresholdPercent < 1 {
			inventoryThresholdPercent = 1
		} else if inventoryThresholdPercent > 100 {
			inventoryThresholdPercent = 100
		}
		previousHealth := domain.ProxyServerHealthStatus(server.HealthStatus)
		previousInventory := domain.ProxyServerInventoryStatus(server.InventoryStatus)
		if reachable {
			server.HealthFailures = 0
			server.HealthStatus = string(domain.ProxyServerHealthy)
			server.LastHealthError = ""
		} else {
			server.HealthFailures++
			server.LastHealthError = domain.SafeProxyError(safeError)
			if server.HealthFailures >= uint(failureThreshold) {
				server.HealthStatus = string(domain.ProxyServerUnhealthy)
			}
		}
		server.InventoryStatus = string(domain.ProxyServerInventoryHealthy)
		if inventory.Active > 0 && inventory.Unavailable*100 >= inventory.Active*int64(inventoryThresholdPercent) {
			server.InventoryStatus = string(domain.ProxyServerInventoryDegraded)
		}
		server.LastHealthCheckedAt = &now
		server.NextHealthCheckAt = nextCheckAt
		server.HealthGeneration++
		if err := tx.Model(&ProxyServerModel{}).Where("id = ?", server.ID).Updates(map[string]any{
			"health_status":          server.HealthStatus,
			"health_failures":        server.HealthFailures,
			"health_generation":      server.HealthGeneration,
			"last_health_error":      server.LastHealthError,
			"last_health_checked_at": server.LastHealthCheckedAt,
			"next_health_check_at":   server.NextHealthCheckAt,
			"inventory_status":       server.InventoryStatus,
		}).Error; err != nil {
			return fmt.Errorf("complete proxy server check: %w", err)
		}
		update = &proxyapp.ProxyServerCheckUpdate{
			ProxyServerID:           server.ID,
			PreviousHealthStatus:    previousHealth,
			HealthStatus:            domain.ProxyServerHealthStatus(server.HealthStatus),
			PreviousInventoryStatus: previousInventory,
			InventoryStatus:         domain.ProxyServerInventoryStatus(server.InventoryStatus),
			HealthFailures:          server.HealthFailures,
			ActiveProxies:           inventory.Active,
			UnavailableProxies:      inventory.Unavailable,
		}
		return r.writeProxyServerCheckTransitionLogs(ctx, tx, update, safeError)
	})
	if err != nil {
		return nil, err
	}
	return update, nil
}

func findBoundResourceProxy(ctx context.Context, tx *gorm.DB, key string, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	var binding ProxyBindingModel
	query := tx.WithContext(ctx).
		Table("proxy_bindings AS b").
		Select("b.*").
		Joins("JOIN proxies AS p ON p.id = b.proxy_id").
		Joins("JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("b.bind_key = ? AND b.expire_at > ? AND p.pool = ? AND p.status = ? AND (p.expire_at IS NULL OR p.expire_at > ?) AND s.health_status = ? AND s.admin_status IN ?",
			key,
			now,
			string(domain.ProxyPoolResource),
			string(domain.ProxyStatusNormal),
			now,
			string(domain.ProxyServerHealthy),
			[]string{string(domain.ProxyServerAdminOnline), string(domain.ProxyServerAdminDraining)},
		)
	if ipVersion == domain.ProxyIPAuto {
		query = query.Where("b.ip_version IN ?", []string{string(domain.ProxyIPv4), string(domain.ProxyIPv6)})
	} else {
		query = query.Where("b.ip_version = ?", string(ipVersion))
	}
	err := query.
		Order("b.last_used_at DESC, b.id DESC").
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find bound proxy: %w", err)
	}
	var model ProxyModel
	if err := tx.WithContext(ctx).First(&model, "id = ?", binding.ProxyID).Error; err != nil {
		return nil, fmt.Errorf("find bound proxy model: %w", err)
	}
	if !binding.ExpireAt.After(now) || !bindingMatchesIPVersion(binding, ipVersion) || !usableResourceProxyModel(model, now) {
		return nil, nil
	}
	result := tx.Model(&ProxyBindingModel{}).
		Where("id = ? AND expire_at > ?", binding.ID, now).
		Update("last_used_at", now)
	if result.Error != nil {
		return nil, fmt.Errorf("touch proxy binding: %w", result.Error)
	}
	proxy := proxyFromModel(model)
	proxy.LastUsedAt = &now
	return &proxy, nil
}

func findExactBoundResourceProxy(ctx context.Context, tx *gorm.DB, key string, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	var binding ProxyBindingModel
	err := tx.WithContext(ctx).
		Where("bind_key = ? AND ip_version = ?", key, string(ipVersion)).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find exact bound proxy: %w", err)
	}
	model, usable, err := findUsableResourceProxyModel(ctx, tx, binding.ProxyID, now)
	if err != nil {
		return nil, err
	}
	if !binding.ExpireAt.After(now) || !usable {
		return nil, nil
	}
	result := tx.Model(&ProxyBindingModel{}).
		Where("id = ? AND expire_at > ?", binding.ID, now).
		Update("last_used_at", now)
	if result.Error != nil {
		return nil, fmt.Errorf("touch exact proxy binding: %w", result.Error)
	}
	proxy := proxyFromModel(model)
	proxy.LastUsedAt = &now
	return &proxy, nil
}

func coverInvalidBinding(ctx context.Context, tx *gorm.DB, key string, proxy *domain.Proxy, expireAt time.Time, now time.Time) (bool, error) {
	var binding ProxyBindingModel
	err := tx.WithContext(ctx).
		Table("proxy_bindings AS b").
		Select("b.*").
		Joins("LEFT JOIN proxies AS p ON p.id = b.proxy_id").
		Joins("LEFT JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("b.bind_key = ? AND b.ip_version = ?", key, string(proxy.IPVersion)).
		Where("(b.expire_at <= ? OR p.id IS NULL OR p.pool <> ? OR p.status <> ? OR (p.expire_at IS NOT NULL AND p.expire_at <= ?) OR s.id IS NULL OR s.health_status <> ? OR s.admin_status NOT IN ?)",
			now,
			string(domain.ProxyPoolResource),
			string(domain.ProxyStatusNormal),
			now,
			string(domain.ProxyServerHealthy),
			[]string{string(domain.ProxyServerAdminOnline), string(domain.ProxyServerAdminDraining)},
		).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock coverable proxy binding: %w", err)
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "id = ?", binding.ID).Error; err != nil {
		return false, fmt.Errorf("lock coverable proxy binding: %w", err)
	}
	if binding.IPVersion != string(proxy.IPVersion) {
		return false, nil
	}
	if binding.ExpireAt.After(now) {
		_, usable, err := findUsableResourceProxyModel(ctx, tx, binding.ProxyID, now)
		if err != nil {
			return false, err
		}
		if usable {
			return false, nil
		}
	}
	if err := tx.WithContext(ctx).
		Model(&ProxyBindingModel{}).
		Where("id = ?", binding.ID).
		Updates(map[string]any{
			"proxy_id":     proxy.ID,
			"expire_at":    expireAt,
			"created_at":   now,
			"last_used_at": now,
		}).Error; err != nil {
		return false, fmt.Errorf("cover proxy binding: %w", err)
	}
	return true, nil
}

type proxyServerCandidate struct {
	ID             uint   `gorm:"column:id"`
	ServerIP       string `gorm:"column:server_ip"`
	CapacityWeight uint   `gorm:"column:capacity_weight"`
}

type proxyServerFailover struct {
	FromServerID uint
	ToServerID   uint
	Reason       string
}

func newProxyServerFailover(fromServerID, toServerID uint, reason string) *proxyServerFailover {
	if strings.TrimSpace(reason) == "" {
		reason = "preferred server was unavailable"
	}
	return &proxyServerFailover{FromServerID: fromServerID, ToServerID: toServerID, Reason: reason}
}

func listEligibleProxyServers(
	ctx context.Context,
	tx *gorm.DB,
	pool domain.ProxyPool,
	ipVersion domain.ProxyIPVersion,
	now time.Time,
) ([]proxyServerCandidate, error) {
	var servers []proxyServerCandidate
	index := "idx_proxies_system_server_select"
	if pool == domain.ProxyPoolResource {
		index = "idx_proxies_resource_server_select"
	}
	query := tx.WithContext(ctx).
		Table("proxy_servers AS s").
		Select("s.id, s.server_ip, s.capacity_weight").
		Where("s.admin_status = ? AND s.health_status = ?", string(domain.ProxyServerAdminOnline), string(domain.ProxyServerHealthy))
	proxyStatuses := []string{
		string(domain.ProxyStatusNormal),
		string(domain.ProxyStatusPending),
		string(domain.ProxyStatusChecking),
		string(domain.ProxyStatusAbnormal),
	}
	existsSQL := `EXISTS (
		SELECT 1 FROM proxies AS p FORCE INDEX (` + index + `)
		WHERE p.proxy_server_id = s.id
		  AND p.pool = ?
		  AND p.status IN ?
		  AND (p.expire_at IS NULL OR p.expire_at > ?)`
	args := []any{string(pool), proxyStatuses, now}
	if ipVersion == domain.ProxyIPAuto {
		existsSQL += " AND p.ip_version IN ?"
		args = append(args, []string{string(domain.ProxyIPv4), string(domain.ProxyIPv6)})
	} else {
		existsSQL += " AND p.ip_version = ?"
		args = append(args, string(ipVersion))
	}
	query = query.Where(existsSQL+")", args...)
	if err := query.Scan(&servers).Error; err != nil {
		return nil, fmt.Errorf("list eligible proxy servers: %w", err)
	}
	return servers, nil
}

func lockEligibleProxyServer(ctx context.Context, tx *gorm.DB, serverID uint) (bool, error) {
	var server ProxyServerModel
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id").
		Where("id = ? AND admin_status = ? AND health_status = ?", serverID, string(domain.ProxyServerAdminOnline), string(domain.ProxyServerHealthy)).
		First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recheck proxy server eligibility: %w", err)
	}
	return true, nil
}

func orderProxyServers(seed string, servers []proxyServerCandidate) []proxyServerCandidate {
	type rankedServer struct {
		proxyServerCandidate
		score float64
	}
	ranked := make([]rankedServer, 0, len(servers))
	for _, server := range servers {
		weight := server.CapacityWeight
		if weight == 0 {
			weight = 1
		}
		sum := sha256.Sum256([]byte(seed + "\x00" + server.ServerIP))
		// Use the top 53 bits so conversion to float64 stays deterministic.
		u := (float64(binary.BigEndian.Uint64(sum[:8])>>11) + 1) / (float64(uint64(1)<<53) + 1)
		ranked = append(ranked, rankedServer{
			proxyServerCandidate: server,
			score:                -math.Log(u) / float64(weight),
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].ID < ranked[j].ID
		}
		return ranked[i].score < ranked[j].score
	})
	ordered := make([]proxyServerCandidate, len(ranked))
	for i := range ranked {
		ordered[i] = ranked[i].proxyServerCandidate
	}
	return ordered
}

func proxyServerIDSet(values []uint) map[uint]struct{} {
	result := make(map[uint]struct{}, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}

func selectResourceProxy(ctx context.Context, tx *gorm.DB, serverID uint, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	var model ProxyModel
	sql, args := buildSelectResourceProxySQL(serverID, ipVersion, now)
	err := tx.WithContext(ctx).Raw(sql, args...).Scan(&model).Error
	if err != nil {
		return nil, fmt.Errorf("select resource proxy: %w", err)
	}
	if model.ID == 0 {
		return nil, domain.ErrProxyUnavailable
	}
	proxy := proxyFromModel(model)
	return &proxy, nil
}

func bindingMatchesIPVersion(binding ProxyBindingModel, ipVersion domain.ProxyIPVersion) bool {
	return ipVersion == domain.ProxyIPAuto || binding.IPVersion == string(ipVersion)
}

func usableResourceProxyModel(model ProxyModel, now time.Time) bool {
	if model.ID == 0 {
		return false
	}
	if model.Pool != string(domain.ProxyPoolResource) || model.Status != string(domain.ProxyStatusNormal) {
		return false
	}
	return model.ExpireAt == nil || model.ExpireAt.After(now)
}

func findUsableResourceProxyModel(ctx context.Context, tx *gorm.DB, proxyID uint, now time.Time) (ProxyModel, bool, error) {
	var model ProxyModel
	err := tx.WithContext(ctx).
		Table("proxies AS p").
		Select("p.*").
		Joins("JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("p.id = ? AND p.pool = ? AND p.status = ? AND (p.expire_at IS NULL OR p.expire_at > ?)", proxyID, string(domain.ProxyPoolResource), string(domain.ProxyStatusNormal), now).
		Where("s.health_status = ? AND s.admin_status IN ?", string(domain.ProxyServerHealthy), []string{string(domain.ProxyServerAdminOnline), string(domain.ProxyServerAdminDraining)}).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProxyModel{}, false, nil
	}
	if err != nil {
		return ProxyModel{}, false, fmt.Errorf("find usable resource proxy: %w", err)
	}
	return model, true, nil
}

func buildSelectResourceProxySQL(serverID uint, ipVersion domain.ProxyIPVersion, now time.Time) (string, []any) {
	sql := `
SELECT p.*
FROM proxies AS p FORCE INDEX (idx_proxies_resource_server_select)
WHERE p.proxy_server_id = ? AND p.pool = ? AND p.status = ? AND (p.expire_at IS NULL OR p.expire_at > ?)`
	args := []any{serverID, string(domain.ProxyPoolResource), string(domain.ProxyStatusNormal), now}
	if ipVersion == domain.ProxyIPAuto {
		sql += " AND p.ip_version IN (?, ?)"
		args = append(args, string(domain.ProxyIPv4), string(domain.ProxyIPv6))
	} else {
		sql += " AND p.ip_version = ?"
		args = append(args, string(ipVersion))
	}
	sql += `
	ORDER BY p.errors ASC,
	         p.last_assigned_at ASC,
	         p.latency_sort_ms ASC,
	         p.id ASC
	LIMIT 1
	FOR UPDATE SKIP LOCKED`
	return sql, args
}

func selectSystemProxy(ctx context.Context, tx *gorm.DB, serverID uint, ipVersion domain.ProxyIPVersion, now time.Time) (*ProxyModel, error) {
	var model ProxyModel
	sql, args := buildSelectSystemProxySQL(serverID, ipVersion, now)
	err := tx.WithContext(ctx).Raw(sql, args...).Scan(&model).Error
	if err != nil {
		return nil, fmt.Errorf("select system proxy: %w", err)
	}
	if model.ID == 0 {
		return nil, domain.ErrProxyUnavailable
	}
	return &model, nil
}

func buildSelectSystemProxySQL(serverID uint, ipVersion domain.ProxyIPVersion, now time.Time) (string, []any) {
	sql := `
SELECT *
FROM proxies FORCE INDEX (idx_proxies_system_server_select)
WHERE proxy_server_id = ? AND pool = ? AND status = ? AND (expire_at IS NULL OR expire_at > ?)`
	args := []any{serverID, string(domain.ProxyPoolSystem), string(domain.ProxyStatusNormal), now}
	if ipVersion == domain.ProxyIPAuto {
		sql += " AND ip_version IN (?, ?)"
		args = append(args, string(domain.ProxyIPv4), string(domain.ProxyIPv6))
	} else {
		sql += " AND ip_version = ?"
		args = append(args, string(ipVersion))
	}
	sql += `
ORDER BY errors ASC,
         last_used_at ASC,
         latency_sort_ms ASC,
         id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`
	return sql, args
}

func touchProxyAssigned(ctx context.Context, tx *gorm.DB, proxyID uint, assignedAt time.Time) error {
	if err := tx.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("id = ?", proxyID).
		Update("last_assigned_at", assignedAt).Error; err != nil {
		return fmt.Errorf("touch proxy assigned: %w", err)
	}
	return nil
}

func touchProxyUsed(ctx context.Context, db *gorm.DB, proxyID uint, usedAt time.Time) error {
	if err := db.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", proxyID, usedAt).
		Update("last_used_at", usedAt).Error; err != nil {
		return fmt.Errorf("touch proxy used: %w", err)
	}
	return nil
}

func (r *ProxyRepo) recordProxyServerFailover(ctx context.Context, failover proxyServerFailover, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&ProxyServerModel{}).
			Where("id = ? AND (last_failover_logged_at IS NULL OR last_failover_logged_at <= ?)", failover.FromServerID, now.Add(-proxyServerFailoverLogInterval)).
			Update("last_failover_logged_at", now)
		if result.Error != nil {
			return fmt.Errorf("claim proxy server failover log: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return r.writeSystemLogInTx(ctx, tx, &governancedomain.SystemLog{
			Level:     "warning",
			Module:    "proxy",
			EventType: "proxy.server_failover",
			BizType:   "proxy_server",
			BizID:     fmt.Sprintf("%d", failover.FromServerID),
			Message:   "Proxy server traffic failed over to another server.",
			Detail:    fmt.Sprintf("from_server_id=%d to_server_id=%d reason=%s", failover.FromServerID, failover.ToServerID, failover.Reason),
		})
	})
}

func (r *ProxyRepo) writeProxyServerCheckTransitionLogs(
	ctx context.Context,
	tx *gorm.DB,
	update *proxyapp.ProxyServerCheckUpdate,
	safeError string,
) error {
	serverID := fmt.Sprintf("%d", update.ProxyServerID)
	if update.PreviousHealthStatus != update.HealthStatus {
		log := &governancedomain.SystemLog{
			Module:  "proxy",
			BizType: "proxy_server",
			BizID:   serverID,
		}
		if update.HealthStatus == domain.ProxyServerUnhealthy {
			log.Level = "error"
			log.EventType = "proxy.server_unhealthy"
			log.Message = "Proxy server became unhealthy; traffic will fail over to another server."
			log.Detail = safeError
		} else {
			log.Level = "info"
			log.EventType = "proxy.server_recovered"
			log.Message = "Proxy server recovered and is eligible for new traffic."
			log.Detail = "Proxy server transport probe succeeded."
		}
		if err := r.writeSystemLogInTx(ctx, tx, log); err != nil {
			return err
		}
	}
	if update.PreviousInventoryStatus != update.InventoryStatus {
		log := &governancedomain.SystemLog{
			Module:  "proxy",
			BizType: "proxy_server",
			BizID:   serverID,
			Detail:  fmt.Sprintf("active=%d unavailable=%d", update.ActiveProxies, update.UnavailableProxies),
		}
		if update.InventoryStatus == domain.ProxyServerInventoryDegraded {
			log.Level = "warning"
			log.EventType = "proxy.server_inventory_degraded"
			log.Message = "Proxy server route inventory is degraded."
		} else {
			log.Level = "info"
			log.EventType = "proxy.server_inventory_recovered"
			log.Message = "Proxy server route inventory recovered."
		}
		if err := r.writeSystemLogInTx(ctx, tx, log); err != nil {
			return err
		}
	}
	return nil
}

func (r *ProxyRepo) writeSystemLogInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.SystemLog) error {
	if r.systemLogs == nil {
		return fmt.Errorf("system log repository is unavailable")
	}
	return r.systemLogs.CreateInTx(ctx, tx, log)
}

func (r *ProxyRepo) createOperationLogInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.OperationLog, resourceID string, summary string) error {
	if log == nil || r.operationLogs == nil {
		return nil
	}
	next := *log
	if strings.TrimSpace(next.ResourceID) == "" {
		next.ResourceID = resourceID
	}
	if strings.TrimSpace(summary) != "" {
		next.SafeSummary = summary
	}
	if err := r.operationLogs.CreateInTx(ctx, tx, &next); err != nil {
		return fmt.Errorf("create operation log: %w", err)
	}
	return nil
}

func proxyCheckMetadataUpdates(log *governancedomain.OperationLog) map[string]any {
	if log == nil {
		return map[string]any{}
	}
	return map[string]any{
		"check_operator_user_id": log.OperatorUserID,
		"check_request_id":       log.RequestID,
		"check_path":             log.Path,
	}
}

func applyProxyListFilter(db *gorm.DB, filter proxyapp.ProxyListFilter) *gorm.DB {
	if filter.Pool != "" {
		db = db.Where("pool = ?", string(filter.Pool))
	}
	if filter.IPVersion != "" && filter.IPVersion != domain.ProxyIPAuto {
		db = db.Where("ip_version = ?", string(filter.IPVersion))
	}
	if filter.IPv6 != nil {
		if *filter.IPv6 {
			db = db.Where("ip_version = ?", string(domain.ProxyIPv6))
		} else {
			db = db.Where("ip_version <> ?", string(domain.ProxyIPv6))
		}
	}
	if filter.Status != "" {
		db = db.Where("status = ?", string(filter.Status))
	}
	if strings.TrimSpace(filter.Country) != "" {
		db = db.Where("country = ?", domain.NormalizeCountry(filter.Country))
	}
	if filter.CreatedFrom != nil {
		db = db.Where("created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		db = db.Where("created_at <= ?", *filter.CreatedTo)
	}
	search := strings.TrimSpace(filter.Search)
	if search != "" {
		normalizedURL, err := domain.NormalizeProxyURL(search)
		if err == nil {
			db = db.Where("url_hash = ?", proxyURLHash(normalizedURL))
		} else {
			like := escapeLikePrefix(proxySearchTerm(search))
			db = db.Where(
				"(url_host LIKE ? ESCAPE '!' OR outbound_ip LIKE ? ESCAPE '!' OR country LIKE ? ESCAPE '!')",
				like,
				like,
				like,
			)
		}
	}
	return db
}

func applyProxyBindingListFilter(db *gorm.DB, filter proxyapp.ProxyBindingListFilter) *gorm.DB {
	if strings.TrimSpace(filter.Key) != "" {
		db = db.Where("bind_key = ?", strings.TrimSpace(strings.ToLower(filter.Key)))
	}
	if filter.ProxyID != 0 {
		db = db.Where("proxy_id = ?", filter.ProxyID)
	}
	if filter.IPVersion != "" && filter.IPVersion != domain.ProxyIPAuto {
		db = db.Where("ip_version = ?", string(filter.IPVersion))
	}
	return db
}

func proxyModel(proxy *domain.Proxy) *ProxyModel {
	checkGeneration := proxy.CheckGeneration
	if checkGeneration == 0 {
		checkGeneration = 1
	}
	return &ProxyModel{
		ID:                  proxy.ID,
		ProxyServerID:       proxy.ProxyServerID,
		Pool:                string(proxy.Pool),
		URL:                 proxy.URL,
		URLHash:             proxyURLHash(proxy.URL),
		URLHost:             proxyURLHost(proxy.URL),
		ExpireAt:            optionalTimePtr(proxy.ExpireAt),
		IPVersion:           string(proxy.IPVersion),
		OutboundIP:          proxy.OutboundIP,
		Country:             domain.NormalizeCountry(proxy.Country),
		LatencyMs:           proxy.LatencyMs,
		Status:              string(proxy.Status),
		Errors:              proxy.Errors,
		LastSafeError:       proxy.LastSafeError,
		CheckOperatorUserID: proxy.CheckOperatorUserID,
		CheckRequestID:      proxy.CheckRequestID,
		CheckPath:           proxy.CheckPath,
		CheckGeneration:     checkGeneration,
		LastCheckedAt:       proxy.LastCheckedAt,
		LastUsedAt:          proxy.LastUsedAt,
		LastAssignedAt:      proxy.LastAssignedAt,
		CreatedAt:           proxy.CreatedAt,
		UpdatedAt:           proxy.UpdatedAt,
	}
}

func proxyFromModel(model ProxyModel) domain.Proxy {
	var expireAt time.Time
	if model.ExpireAt != nil {
		expireAt = *model.ExpireAt
	}
	return domain.Proxy{
		ID:                  model.ID,
		ProxyServerID:       model.ProxyServerID,
		Pool:                domain.ProxyPool(model.Pool),
		URL:                 model.URL,
		ExpireAt:            expireAt,
		IPVersion:           domain.ProxyIPVersion(model.IPVersion),
		OutboundIP:          model.OutboundIP,
		Country:             model.Country,
		LatencyMs:           model.LatencyMs,
		Status:              domain.ProxyStatus(model.Status),
		Errors:              model.Errors,
		LastSafeError:       model.LastSafeError,
		CheckOperatorUserID: model.CheckOperatorUserID,
		CheckRequestID:      model.CheckRequestID,
		CheckPath:           model.CheckPath,
		CheckGeneration:     model.CheckGeneration,
		LastCheckedAt:       model.LastCheckedAt,
		LastUsedAt:          model.LastUsedAt,
		LastAssignedAt:      model.LastAssignedAt,
		CreatedAt:           model.CreatedAt,
		UpdatedAt:           model.UpdatedAt,
	}
}

func proxyServerFromModel(model ProxyServerModel) domain.ProxyServer {
	return domain.ProxyServer{
		ID:                  model.ID,
		ServerIP:            model.ServerIP,
		Name:                model.Name,
		SourceType:          model.SourceType,
		CapacityWeight:      model.CapacityWeight,
		AdminStatus:         domain.ProxyServerAdminStatus(model.AdminStatus),
		HealthStatus:        domain.ProxyServerHealthStatus(model.HealthStatus),
		HealthFailures:      model.HealthFailures,
		HealthGeneration:    model.HealthGeneration,
		LastHealthError:     model.LastHealthError,
		LastHealthCheckedAt: model.LastHealthCheckedAt,
		NextHealthCheckAt:   model.NextHealthCheckAt,
		InventoryStatus:     domain.ProxyServerInventoryStatus(model.InventoryStatus),
		CreatedAt:           model.CreatedAt,
		UpdatedAt:           model.UpdatedAt,
	}
}

func optionalTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func bindingFromModel(model ProxyBindingModel) domain.Binding {
	return domain.Binding{
		ID:         model.ID,
		Key:        model.BindKey,
		ProxyID:    model.ProxyID,
		IPVersion:  domain.ProxyIPVersion(model.IPVersion),
		ExpireAt:   model.ExpireAt,
		CreatedAt:  model.CreatedAt,
		LastUsedAt: model.LastUsedAt,
	}
}

func proxyURLHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func proxyURLHost(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return domain.NormalizeProxyServerIP(parsed.Hostname())
}

func ensureProxyServer(ctx context.Context, tx *gorm.DB, serverIP string) (uint, error) {
	serverIP = domain.NormalizeProxyServerIP(serverIP)
	if serverIP == "" {
		return 0, domain.ErrInvalidProxyURL
	}
	server := ProxyServerModel{
		ServerIP:          serverIP,
		Name:              serverIP,
		SourceType:        "vendor",
		CapacityWeight:    1,
		AdminStatus:       string(domain.ProxyServerAdminOnline),
		HealthStatus:      string(domain.ProxyServerHealthy),
		HealthGeneration:  1,
		InventoryStatus:   string(domain.ProxyServerInventoryHealthy),
		NextHealthCheckAt: time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "server_ip"}},
		DoNothing: true,
	}).Create(&server).Error; err != nil {
		return 0, fmt.Errorf("ensure proxy server: %w", err)
	}
	if server.ID != 0 {
		return server.ID, nil
	}
	if err := tx.WithContext(ctx).Select("id").First(&server, "server_ip = ?", serverIP).Error; err != nil {
		return 0, fmt.Errorf("find proxy server: %w", err)
	}
	return server.ID, nil
}

func proxySearchTerm(value string) string {
	trimmed := strings.TrimSpace(value)
	if parsed, err := url.Parse(trimmed); err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
		return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	}
	return strings.ToLower(trimmed)
}

func normalizeProxyBindingKeys(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	keys := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		keys = append(keys, value)
	}
	return keys
}

func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return replacer.Replace(strings.TrimSpace(value)) + "%"
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func withTransactionRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < transactionRetryAttempts; attempt++ {
		err = fn()
		if err == nil || (!errors.Is(err, errRetryProxyAcquire) && !isRetryableTransactionError(err)) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

func isRetryableTransactionError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1205 || mysqlErr.Number == 1213
}

var _ proxyapp.ProxyRepository = (*ProxyRepo)(nil)
