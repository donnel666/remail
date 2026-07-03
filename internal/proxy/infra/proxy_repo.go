package infra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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
	ID            uint       `gorm:"primaryKey;autoIncrement"`
	Pool          string     `gorm:"type:varchar(16);not null"`
	URL           string     `gorm:"type:varchar(1024);not null;column:url"`
	URLHash       string     `gorm:"type:char(64);not null;column:url_hash"`
	URLHost       string     `gorm:"type:varchar(255);not null;column:url_host"`
	ExpireAt      time.Time  `gorm:"not null;column:expire_at"`
	IPVersion     string     `gorm:"type:varchar(8);not null;column:ip_version"`
	OutboundIP    string     `gorm:"type:varchar(64);not null;column:outbound_ip"`
	Country       string     `gorm:"type:varchar(64);not null"`
	LatencyMs     int        `gorm:"not null;column:latency_ms"`
	Status        string     `gorm:"type:varchar(32);not null"`
	Errors        int        `gorm:"not null"`
	LastSafeError string     `gorm:"type:varchar(500);not null;column:last_safe_error"`
	LastCheckedAt *time.Time `gorm:"column:last_checked_at"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at"`
	CreatedAt     time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt     time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ProxyModel) TableName() string {
	return "proxies"
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
}

const transactionRetryAttempts = 3

func NewProxyRepo(db *gorm.DB) *ProxyRepo {
	return &ProxyRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
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
		for _, proxy := range proxies {
			if proxy == nil {
				continue
			}
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

func (r *ProxyRepo) Stats(ctx context.Context, filter proxyapp.ProxyListFilter) (*proxyapp.ProxyStats, error) {
	base := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count proxy stats: %w", err)
	}
	countries, err := r.groupProxyCounts(ctx, filter, "country")
	if err != nil {
		return nil, err
	}
	statuses, err := r.groupProxyCounts(ctx, filter, "status")
	if err != nil {
		return nil, err
	}
	pools, err := r.groupProxyCounts(ctx, filter, "pool")
	if err != nil {
		return nil, err
	}
	ipVersions, err := r.groupProxyCounts(ctx, filter, "ip_version")
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

func (r *ProxyRepo) ListIDs(ctx context.Context, filter proxyapp.ProxyListFilter, afterID uint, limit int) ([]uint, error) {
	var ids []uint
	db := applyProxyListFilter(r.db.WithContext(ctx).Model(&ProxyModel{}), filter)
	if afterID > 0 {
		db = db.Where("id > ?", afterID)
	}
	if limit > 0 {
		db = db.Limit(limit)
	}
	if err := db.Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list proxy ids: %w", err)
	}
	return ids, nil
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

func (r *ProxyRepo) updateInTx(ctx context.Context, tx *gorm.DB, proxy *domain.Proxy) error {
	model := proxyModel(proxy)
	result := tx.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("id = ?", proxy.ID).
		Updates(map[string]any{
			"expire_at":       model.ExpireAt,
			"ip_version":      model.IPVersion,
			"outbound_ip":     model.OutboundIP,
			"country":         model.Country,
			"latency_ms":      model.LatencyMs,
			"status":          model.Status,
			"errors":          model.Errors,
			"last_safe_error": model.LastSafeError,
			"last_checked_at": model.LastCheckedAt,
			"last_used_at":    model.LastUsedAt,
		})
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
		Where("expire_at <= ? AND status <> ?", now, string(domain.ProxyStatusExpired)).
		Updates(map[string]any{
			"status":          string(domain.ProxyStatusExpired),
			"last_safe_error": "Proxy has expired.",
			"last_checked_at": now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("mark expired proxies: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *ProxyRepo) UpdateCheckResult(ctx context.Context, id uint, result domain.CheckResult, success bool) (*domain.Proxy, error) {
	return r.updateCheckResultWithTxLog(ctx, id, result, success, nil)
}

func (r *ProxyRepo) UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error) {
	return r.updateCheckResultWithTxLog(ctx, id, result, success, log)
}

func (r *ProxyRepo) updateCheckResultWithTxLog(ctx context.Context, id uint, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error) {
	var updated domain.Proxy
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		proxy, err := updateCheckResultInTx(ctx, tx, id, result, success)
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

func updateCheckResultInTx(ctx context.Context, tx *gorm.DB, id uint, result domain.CheckResult, success bool) (domain.Proxy, error) {
	var model ProxyModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Proxy{}, domain.ErrProxyNotFound
		}
		return domain.Proxy{}, fmt.Errorf("lock proxy for check: %w", err)
	}
	proxy := proxyFromModel(model)
	if success {
		if !domain.CanTransitionProxyStatus(proxy.Status, domain.ProxyStatusNormal) {
			if err := proxy.MarkChecking(); err != nil {
				return domain.Proxy{}, err
			}
		}
		if err := proxy.ApplyCheckSuccess(result); err != nil {
			return domain.Proxy{}, err
		}
	} else {
		if result.LastSafeError == "Proxy has expired." || proxy.IsExpired(result.CheckedAt) {
			if err := proxy.MarkExpired(result.CheckedAt); err != nil {
				return domain.Proxy{}, err
			}
		} else if err := proxy.ApplyCheckFailure(result); err != nil {
			return domain.Proxy{}, err
		}
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
	err := withTransactionRetry(func() error {
		selected = nil
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if bound, err := findBoundResourceProxy(ctx, tx, key, ipVersion, now); err != nil {
				return err
			} else if bound != nil {
				selected = bound
				return nil
			}

			proxy, err := selectResourceProxy(ctx, tx, ipVersion, now)
			if err != nil {
				return err
			}
			bindingExpireAt := now.Add(bindingTTL)
			if proxy.ExpireAt.Before(bindingExpireAt) {
				bindingExpireAt = proxy.ExpireAt
			}
			covered, err := coverInvalidBinding(ctx, tx, key, proxy, bindingExpireAt, now)
			if err != nil {
				return err
			}
			if covered {
				proxy.LastUsedAt = &now
				selected = proxy
				return nil
			}
			binding := &ProxyBindingModel{
				BindKey:   key,
				ProxyID:   proxy.ID,
				IPVersion: string(proxy.IPVersion),
				ExpireAt:  bindingExpireAt,
			}
			if err := tx.Create(binding).Error; err != nil {
				if isDuplicateKeyError(err) {
					bound, findErr := findBoundResourceProxy(ctx, tx, key, ipVersion, now)
					if findErr != nil {
						return findErr
					}
					if bound != nil {
						selected = bound
						return nil
					}
					covered, coverErr := coverInvalidBinding(ctx, tx, key, proxy, bindingExpireAt, now)
					if coverErr != nil {
						return coverErr
					}
					if covered {
						proxy.LastUsedAt = &now
						selected = proxy
						return nil
					}
				}
				return fmt.Errorf("create proxy binding: %w", err)
			}
			if err := touchProxyUsed(ctx, tx, proxy.ID, now); err != nil {
				return err
			}
			proxy.LastUsedAt = &now
			selected = proxy
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, domain.ErrProxyUnavailable
	}
	return selected, nil
}

func (r *ProxyRepo) AcquireSystemProxy(ctx context.Context, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	if ipVersion == "" {
		ipVersion = domain.ProxyIPAuto
	}
	var selected *domain.Proxy
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := selectSystemProxy(ctx, tx, ipVersion, now)
		if err != nil {
			return err
		}
		if err := touchProxyUsed(ctx, tx, model.ID, now); err != nil {
			return err
		}
		proxy := proxyFromModel(*model)
		proxy.LastUsedAt = &now
		selected = &proxy
		return nil
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, domain.ErrProxyUnavailable
	}
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
		if err := proxy.ReportFailure(safeError, retryable); err != nil {
			return err
		}
		if err := tx.Model(&ProxyModel{}).
			Where("id = ?", proxyID).
			Updates(map[string]any{
				"status":          string(proxy.Status),
				"errors":          proxy.Errors,
				"last_safe_error": proxy.LastSafeError,
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

func findBoundResourceProxy(ctx context.Context, tx *gorm.DB, key string, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	var binding ProxyBindingModel
	query := tx.WithContext(ctx).
		Table("proxy_bindings AS b").
		Select("b.*").
		Joins("JOIN proxies AS p ON p.id = b.proxy_id").
		Where("b.bind_key = ? AND b.expire_at > ? AND p.pool = ? AND p.status = ? AND p.expire_at > ?",
			key,
			now,
			string(domain.ProxyPoolResource),
			string(domain.ProxyStatusNormal),
			now,
		)
	if ipVersion == domain.ProxyIPAuto {
		query = query.Where("b.ip_version IN ?", []string{string(domain.ProxyIPv4), string(domain.ProxyIPv6)})
	} else {
		query = query.Where("b.ip_version = ?", string(ipVersion))
	}
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("b.last_used_at DESC, b.id DESC").
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find bound proxy: %w", err)
	}

	var model ProxyModel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", binding.ProxyID).Error; err != nil {
		return nil, fmt.Errorf("find bound proxy model: %w", err)
	}
	if err := tx.Model(&ProxyBindingModel{}).Where("id = ?", binding.ID).Update("last_used_at", now).Error; err != nil {
		return nil, fmt.Errorf("touch proxy binding: %w", err)
	}
	if err := touchProxyUsed(ctx, tx, model.ID, now); err != nil {
		return nil, err
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
		Where("b.bind_key = ? AND b.ip_version = ?", key, string(proxy.IPVersion)).
		Where("(b.expire_at <= ? OR p.id IS NULL OR p.pool <> ? OR p.status <> ? OR p.expire_at <= ?)",
			now,
			string(domain.ProxyPoolResource),
			string(domain.ProxyStatusNormal),
			now,
		).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock coverable proxy binding: %w", err)
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
	if err := touchProxyUsed(ctx, tx, proxy.ID, now); err != nil {
		return false, err
	}
	return true, nil
}

func selectResourceProxy(ctx context.Context, tx *gorm.DB, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error) {
	var model ProxyModel
	sql, args := buildSelectResourceProxySQL(ipVersion, now)
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

func buildSelectResourceProxySQL(ipVersion domain.ProxyIPVersion, now time.Time) (string, []any) {
	sql := `
SELECT p.*
FROM proxies AS p FORCE INDEX (idx_proxies_select_health)
LEFT JOIN (
    SELECT proxy_id, COUNT(*) AS active_bindings
    FROM proxy_bindings
    WHERE expire_at > ?
    GROUP BY proxy_id
) AS b ON b.proxy_id = p.id
WHERE p.pool = ? AND p.status = ? AND p.expire_at > ?`
	args := []any{now, string(domain.ProxyPoolResource), string(domain.ProxyStatusNormal), now}
	if ipVersion == domain.ProxyIPAuto {
		sql += " AND p.ip_version IN (?, ?)"
		args = append(args, string(domain.ProxyIPv4), string(domain.ProxyIPv6))
	} else {
		sql += " AND p.ip_version = ?"
		args = append(args, string(ipVersion))
	}
	sql += `
ORDER BY p.errors ASC,
         COALESCE(b.active_bindings, 0) ASC,
         CASE WHEN p.latency_ms > 0 THEN p.latency_ms ELSE 2147483647 END ASC,
         COALESCE(p.last_used_at, '1970-01-01') ASC,
         p.id ASC
LIMIT 1
FOR UPDATE`
	return sql, args
}

func selectSystemProxy(ctx context.Context, tx *gorm.DB, ipVersion domain.ProxyIPVersion, now time.Time) (*ProxyModel, error) {
	var model ProxyModel
	sql, args := buildSelectSystemProxySQL(ipVersion, now)
	err := tx.WithContext(ctx).Raw(sql, args...).Scan(&model).Error
	if err != nil {
		return nil, fmt.Errorf("select system proxy: %w", err)
	}
	if model.ID == 0 {
		return nil, domain.ErrProxyUnavailable
	}
	return &model, nil
}

func buildSelectSystemProxySQL(ipVersion domain.ProxyIPVersion, now time.Time) (string, []any) {
	sql := `
SELECT *
FROM proxies FORCE INDEX (idx_proxies_select_health)
WHERE pool = ? AND status = ? AND expire_at > ?`
	args := []any{string(domain.ProxyPoolSystem), string(domain.ProxyStatusNormal), now}
	if ipVersion == domain.ProxyIPAuto {
		sql += " AND ip_version IN (?, ?)"
		args = append(args, string(domain.ProxyIPv4), string(domain.ProxyIPv6))
	} else {
		sql += " AND ip_version = ?"
		args = append(args, string(ipVersion))
	}
	sql += `
ORDER BY errors ASC,
         COALESCE(last_used_at, '1970-01-01') ASC,
         CASE WHEN latency_ms > 0 THEN latency_ms ELSE 2147483647 END ASC,
         id ASC
LIMIT 1
FOR UPDATE`
	return sql, args
}

func touchProxyUsed(ctx context.Context, tx *gorm.DB, proxyID uint, usedAt time.Time) error {
	if err := tx.WithContext(ctx).
		Model(&ProxyModel{}).
		Where("id = ?", proxyID).
		Update("last_used_at", usedAt).Error; err != nil {
		return fmt.Errorf("touch proxy used: %w", err)
	}
	return nil
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
		like := escapeLikePrefix(proxySearchTerm(search))
		normalizedURL, err := domain.NormalizeProxyURL(search)
		if err == nil {
			db = db.Where(
				"(url_hash = ? OR url_host LIKE ? ESCAPE '!' OR outbound_ip LIKE ? ESCAPE '!' OR country LIKE ? ESCAPE '!')",
				proxyURLHash(normalizedURL),
				like,
				like,
				like,
			)
		} else {
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
	return &ProxyModel{
		ID:            proxy.ID,
		Pool:          string(proxy.Pool),
		URL:           proxy.URL,
		URLHash:       proxyURLHash(proxy.URL),
		URLHost:       proxyURLHost(proxy.URL),
		ExpireAt:      proxy.ExpireAt,
		IPVersion:     string(proxy.IPVersion),
		OutboundIP:    proxy.OutboundIP,
		Country:       domain.NormalizeCountry(proxy.Country),
		LatencyMs:     proxy.LatencyMs,
		Status:        string(proxy.Status),
		Errors:        proxy.Errors,
		LastSafeError: proxy.LastSafeError,
		LastCheckedAt: proxy.LastCheckedAt,
		LastUsedAt:    proxy.LastUsedAt,
		CreatedAt:     proxy.CreatedAt,
		UpdatedAt:     proxy.UpdatedAt,
	}
}

func proxyFromModel(model ProxyModel) domain.Proxy {
	return domain.Proxy{
		ID:            model.ID,
		Pool:          domain.ProxyPool(model.Pool),
		URL:           model.URL,
		ExpireAt:      model.ExpireAt,
		IPVersion:     domain.ProxyIPVersion(model.IPVersion),
		OutboundIP:    model.OutboundIP,
		Country:       model.Country,
		LatencyMs:     model.LatencyMs,
		Status:        domain.ProxyStatus(model.Status),
		Errors:        model.Errors,
		LastSafeError: model.LastSafeError,
		LastCheckedAt: model.LastCheckedAt,
		LastUsedAt:    model.LastUsedAt,
		CreatedAt:     model.CreatedAt,
		UpdatedAt:     model.UpdatedAt,
	}
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

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
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
	return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
}

func proxySearchTerm(value string) string {
	trimmed := strings.TrimSpace(value)
	if parsed, err := url.Parse(trimmed); err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
		return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	}
	return strings.ToLower(trimmed)
}

func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return replacer.Replace(strings.TrimSpace(value)) + "%"
}

func withTransactionRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < transactionRetryAttempts; attempt++ {
		err = fn()
		if err == nil || !isRetryableTransactionError(err) {
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
