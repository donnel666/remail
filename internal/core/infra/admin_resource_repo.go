package infra

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdminResourceRepo struct {
	db           *gorm.DB
	facetsCache  *platform.TTLCache[string, coreapp.AdminMicrosoftFacets]
	facetsFlight singleflight.Group
}

const adminMicrosoftFacetsCacheTTL = 5 * time.Minute

type AdminResourceCommandReceiptModel struct {
	OperatorUserID     uint      `gorm:"primaryKey;column:operator_user_id"`
	IdempotencyKey     string    `gorm:"primaryKey;type:varchar(128);column:idempotency_key"`
	Operation          string    `gorm:"type:varchar(64);not null;column:operation"`
	Subject            string    `gorm:"type:varchar(255);not null;column:subject"`
	RequestFingerprint string    `gorm:"type:char(64);not null;column:request_fingerprint"`
	ReservationToken   string    `gorm:"type:char(36);not null;column:reservation_token"`
	Status             string    `gorm:"type:varchar(16);not null;column:status"`
	ResultJSON         []byte    `gorm:"type:json;column:result_json"`
	CreatedAt          time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (AdminResourceCommandReceiptModel) TableName() string {
	return "admin_resource_command_receipts"
}

func NewAdminResourceRepo(db *gorm.DB) *AdminResourceRepo {
	return &AdminResourceRepo{
		db:          db,
		facetsCache: platform.NewTTLCache[string, coreapp.AdminMicrosoftFacets](),
	}
}

func (r *AdminResourceRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *AdminResourceRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return domain.ErrInvalidResourceCommand
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(platform.WithGormTx(ctx, tx.WithContext(ctx)))
	}
	invalidation := &microsoftFacetsInvalidationState{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(platform.WithGormTx(ctx, tx), microsoftFacetsInvalidationKey{}, invalidation)
		return fn(txCtx)
	})
	if err == nil && invalidation.dirty {
		invalidateMicrosoftFacets()
	}
	return err
}

func (r *AdminResourceRepo) ReserveAdminCommand(ctx context.Context, receipt coreapp.AdminResourceCommandReceipt) ([]byte, bool, error) {
	if r == nil || r.db == nil || receipt.OperatorUserID == 0 || strings.TrimSpace(receipt.IdempotencyKey) == "" ||
		len(receipt.IdempotencyKey) > 128 || strings.TrimSpace(receipt.Operation) == "" ||
		strings.TrimSpace(receipt.Subject) == "" || strings.TrimSpace(receipt.RequestFingerprint) == "" {
		return nil, false, domain.ErrInvalidResourceCommand
	}
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return nil, false, domain.ErrResourceDependency
	}
	reservationToken := platform.NewUUIDV7String()
	candidate := &AdminResourceCommandReceiptModel{
		OperatorUserID: receipt.OperatorUserID, IdempotencyKey: strings.TrimSpace(receipt.IdempotencyKey),
		Operation: strings.TrimSpace(receipt.Operation), Subject: strings.TrimSpace(receipt.Subject),
		RequestFingerprint: strings.TrimSpace(receipt.RequestFingerprint), ReservationToken: reservationToken,
		Status: "processing",
	}
	db := r.dbFor(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
		return nil, false, fmt.Errorf("reserve administrator resource command: %w", err)
	}
	var stored AdminResourceCommandReceiptModel
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("operator_user_id = ? AND idempotency_key = ?", receipt.OperatorUserID, candidate.IdempotencyKey).
		First(&stored).Error; err != nil {
		return nil, false, fmt.Errorf("lock administrator resource command receipt: %w", err)
	}
	if stored.Operation != candidate.Operation || stored.Subject != candidate.Subject || stored.RequestFingerprint != candidate.RequestFingerprint {
		return nil, false, domain.ErrResourceIdempotencyConflict
	}
	if stored.ReservationToken == reservationToken {
		if stored.Status != "processing" || len(stored.ResultJSON) != 0 {
			return nil, false, domain.ErrResourceDependency
		}
		return nil, false, nil
	}
	if stored.Status != "succeeded" || len(stored.ResultJSON) == 0 {
		return nil, false, domain.ErrResourceDependency
	}
	result := append([]byte(nil), stored.ResultJSON...)
	return result, true, nil
}

func (r *AdminResourceRepo) CompleteAdminCommand(ctx context.Context, operatorUserID uint, idempotencyKey string, resultJSON []byte) error {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if r == nil || r.db == nil || operatorUserID == 0 || idempotencyKey == "" || len(idempotencyKey) > 128 || len(resultJSON) == 0 {
		return domain.ErrInvalidResourceCommand
	}
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return domain.ErrResourceDependency
	}
	result := r.dbFor(ctx).Model(&AdminResourceCommandReceiptModel{}).
		Where("operator_user_id = ? AND idempotency_key = ? AND status = ?", operatorUserID, idempotencyKey, "processing").
		Updates(map[string]any{"status": "succeeded", "result_json": string(resultJSON)})
	if result.Error != nil {
		return fmt.Errorf("complete administrator resource command receipt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return domain.ErrResourceDependency
	}
	return nil
}

func (r *AdminResourceRepo) LockAdminMicrosoft(ctx context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error) {
	if resourceID == 0 {
		return nil, nil, domain.ErrResourceNotFound
	}
	db := r.dbFor(ctx)
	var root EmailResourceModel
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, string(domain.ResourceTypeMicrosoft)).
		First(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, domain.ErrResourceNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock admin microsoft resource root: %w", err)
	}
	var resource MicrosoftResourceModel
	err = db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", resourceID).
		First(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, domain.ErrResourceNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock admin microsoft resource: %w", err)
	}
	return root.toDomain(), resource.toDomain(), nil
}

func (r *AdminResourceRepo) MaxMicrosoftResourceID(ctx context.Context) (uint, error) {
	var maxID uint
	err := r.dbFor(ctx).Raw(`
SELECT COALESCE(MAX(er.id), 0)
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id
WHERE er.type = ? AND mr.status <> ?`, domain.ResourceTypeMicrosoft, domain.MicrosoftStatusDeleted).Scan(&maxID).Error
	if err != nil {
		return 0, fmt.Errorf("find maximum microsoft resource id: %w", err)
	}
	return maxID, nil
}

func (r *AdminResourceRepo) FindNextMicrosoft(ctx context.Context, afterID, maxID uint) (*domain.MicrosoftResource, error) {
	if maxID == 0 || afterID >= maxID {
		return nil, nil
	}
	var resource MicrosoftResourceModel
	result := r.dbFor(ctx).
		Table("microsoft_resources AS mr").
		Select("mr.id, mr.status, mr.email_address, mr.client_id, mr.refresh_token, mr.credential_revision").
		Joins("JOIN email_resources AS er ON er.id = mr.id AND er.type = ?", domain.ResourceTypeMicrosoft).
		Where("mr.id > ? AND mr.id <= ? AND mr.status <> ?", afterID, maxID, domain.MicrosoftStatusDeleted).
		Order("mr.id ASC").
		Limit(1).
		Scan(&resource)
	if result.Error != nil {
		return nil, fmt.Errorf("find next microsoft resource: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return resource.toDomain(), nil
}

func (r *AdminResourceRepo) SaveAdminMicrosoft(ctx context.Context, root *domain.EmailResource, resource *domain.MicrosoftResource, expectedVersion uint64) error {
	if root == nil || resource == nil || root.ID == 0 || resource.ID != root.ID || expectedVersion == 0 {
		return domain.ErrInvalidResourceCommand
	}
	db := r.dbFor(ctx)
	now := time.Now().UTC()
	rootUpdate := db.Model(&EmailResourceModel{}).
		Where("id = ? AND type = ? AND version = ?", root.ID, string(domain.ResourceTypeMicrosoft), expectedVersion).
		Updates(map[string]any{
			"owner_user_id": root.OwnerUserID,
			"version":       expectedVersion + 1,
			"updated_at":    now,
		})
	if rootUpdate.Error != nil {
		return fmt.Errorf("save admin microsoft resource root: %w", rootUpdate.Error)
	}
	if rootUpdate.RowsAffected == 0 {
		return domain.ErrResourceVersionConflict
	}
	resourceUpdate := db.Model(&MicrosoftResourceModel{}).
		Where("id = ?", resource.ID).
		Updates(map[string]any{
			"email_address":           strings.ToLower(strings.TrimSpace(resource.EmailAddress)),
			"email_domain":            microsoftEmailDomain(resource.EmailAddress),
			"password":                resource.Password,
			"client_id":               resource.ClientID,
			"refresh_token":           resource.RefreshToken,
			"credential_revision":     resource.CredentialRevision,
			"credential_updated_at":   resource.CredentialUpdatedAt,
			"long_lived":              resource.LongLived,
			"graph_available":         resource.GraphAvailable,
			"rt_expire_at":            resource.RTExpireAt,
			"token_last_refreshed_at": resource.TokenLastRefreshedAt,
			"token_last_request_id":   resource.TokenLastRequestID,
			"for_sale":                resource.ForSale,
			"status":                  string(resource.Status),
			"validation_generation":   resource.ValidationGeneration,
			"validation_failures":     resource.ValidationFailures,
			"quality_score":           resource.QualityScore,
			"last_safe_error":         resource.LastSafeError,
			"last_allocated_at":       resource.LastAllocatedAt,
			"updated_at":              now,
		})
	if resourceUpdate.Error != nil {
		if isDuplicateKeyError(resourceUpdate.Error) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("save admin microsoft resource: %w", resourceUpdate.Error)
	}
	if resourceUpdate.RowsAffected == 0 {
		return domain.ErrResourceNotFound
	}
	root.Version = expectedVersion + 1
	root.UpdatedAt = now
	resource.UpdatedAt = now
	markMicrosoftFacetsDirty(ctx)
	return nil
}

type adminMicrosoftRow struct {
	ID                     uint
	OwnerUserID            uint
	Version                uint64
	EmailAddress           string
	EmailDomain            string
	Status                 string
	ForSale                bool
	LongLived              bool
	GraphAvailable         bool
	QualityScore           int
	RefreshTokenConfigured bool
	PasswordConfigured     bool
	ClientIDConfigured     bool
	CredentialRevision     uint64
	CredentialUpdatedAt    time.Time
	RTExpireAt             *time.Time
	TokenLastRefreshedAt   *time.Time
	TokenLastRequestID     string
	LastAllocatedAt        *time.Time
	LastSafeError          string
	ExplicitAliasCount     int64
	DotAliasCount          int64
	PlusAliasCount         int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

const adminMicrosoftListSelect = `
er.id AS id,
er.owner_user_id AS owner_user_id,
er.version AS version,
mr.email_address AS email_address,
mr.email_domain AS email_domain,
mr.status AS status,
mr.for_sale AS for_sale,
mr.long_lived AS long_lived,
mr.graph_available AS graph_available,
mr.quality_score AS quality_score,
(mr.refresh_token <> '') AS refresh_token_configured,
(mr.password <> '') AS password_configured,
(mr.client_id <> '') AS client_id_configured,
mr.credential_revision AS credential_revision,
mr.credential_updated_at AS credential_updated_at,
mr.rt_expire_at AS rt_expire_at,
mr.token_last_refreshed_at AS token_last_refreshed_at,
mr.token_last_request_id AS token_last_request_id,
mr.last_allocated_at AS last_allocated_at,
mr.last_safe_error AS last_safe_error,
er.created_at AS created_at,
er.updated_at AS updated_at`

const adminMicrosoftDetailSelect = adminMicrosoftListSelect + `,
(SELECT COUNT(*) FROM explicit_aliases ea WHERE ea.resource_id = er.id) AS explicit_alias_count,
(SELECT COUNT(*) FROM dot_aliases da WHERE da.resource_id = er.id) AS dot_alias_count,
(SELECT COUNT(*) FROM plus_aliases pa WHERE pa.resource_id = er.id) AS plus_alias_count`

func (r *AdminResourceRepo) ListAdminMicrosoft(ctx context.Context, filter coreapp.AdminMicrosoftListFilter, offset, limit int, afterID uint, now time.Time) ([]coreapp.AdminMicrosoftRecord, error) {
	query := r.adminMicrosoftFilterQuery(ctx, filter, now, "").Select(adminMicrosoftListSelect)
	if afterID > 0 {
		query = query.Where("er.id < ?", afterID)
	} else {
		query = query.Offset(offset)
	}
	var rows []adminMicrosoftRow
	if err := query.Order("er.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list admin microsoft resources: %w", err)
	}
	items := make([]coreapp.AdminMicrosoftRecord, len(rows))
	for i := range rows {
		items[i] = adminMicrosoftRecord(rows[i])
	}
	return items, nil
}

func (r *AdminResourceRepo) CountAdminMicrosoft(ctx context.Context, filter coreapp.AdminMicrosoftListFilter, now time.Time) (int64, error) {
	var total int64
	if err := r.adminMicrosoftFilterQuery(ctx, filter, now, "").Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count admin microsoft resources: %w", err)
	}
	return total, nil
}

func (r *AdminResourceRepo) FindAdminMicrosoft(ctx context.Context, resourceID uint) (*coreapp.AdminMicrosoftRecord, error) {
	if resourceID == 0 {
		return nil, nil
	}
	var row adminMicrosoftRow
	err := r.dbFor(ctx).
		Table("email_resources AS er").
		Joins("JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'").
		Select(adminMicrosoftDetailSelect).
		Where("er.id = ?", resourceID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find admin microsoft resource: %w", err)
	}
	record := adminMicrosoftRecord(row)
	return &record, nil
}

func (r *AdminResourceRepo) AdminMicrosoftFacets(ctx context.Context, filter coreapp.AdminMicrosoftListFilter, now time.Time) (*coreapp.AdminMicrosoftFacets, error) {
	cacheKey := adminMicrosoftFacetsCacheKey(filter)
	for attempt := 0; ; attempt++ {
		if cached, ok := r.facetsCache.Get(cacheKey); ok {
			return cloneAdminMicrosoftFacets(&cached), nil
		}

		resultCh := r.facetsFlight.DoChan(cacheKey, func() (any, error) {
			if cached, ok := r.facetsCache.Get(cacheKey); ok {
				return cached, nil
			}
			queryCtx, cancel := context.WithTimeout(ctx, microsoftFacetsQueryTimeout)
			defer cancel()

			var selects facetAggregateSelect
			add := func(alias string, ignore string, extra string, extraArgs ...any) {
				predicate, args := adminMicrosoftFacetPredicate(filter, ignore, now)
				if extra != "" {
					predicate += " AND " + extra
					args = append(args, extraArgs...)
				}
				selects.add(alias, predicate, args...)
			}
			add("matched_count", "", "")
			add("status_pending_count", "status", "mr.status = ?", string(domain.MicrosoftStatusPending))
			add("status_validating_count", "status", "mr.status = ?", string(domain.MicrosoftStatusValidating))
			add("status_identifying_count", "status", "mr.status = ?", string(domain.MicrosoftStatusIdentifying))
			add("status_normal_count", "status", "mr.status = ?", string(domain.MicrosoftStatusNormal))
			add("status_abnormal_count", "status", "mr.status = ?", string(domain.MicrosoftStatusAbnormal))
			add("status_disabled_count", "status", "mr.status = ?", string(domain.MicrosoftStatusDisabled))
			add("status_deleted_count", "status", "mr.status = ?", string(domain.MicrosoftStatusDeleted))
			add("for_sale_all_count", "for_sale", "")
			add("for_sale_yes_count", "for_sale", "mr.for_sale = TRUE")
			add("for_sale_no_count", "for_sale", "mr.for_sale = FALSE")
			add("long_lived_all_count", "long_lived", "")
			add("long_lived_yes_count", "long_lived", "mr.long_lived = TRUE")
			add("long_lived_no_count", "long_lived", "mr.long_lived = FALSE")
			add("graph_available_all_count", "graph_available", "")
			add("graph_available_yes_count", "graph_available", "mr.graph_available = TRUE")
			add("graph_available_no_count", "graph_available", "mr.graph_available = FALSE")
			add("token_health_all_count", "token_health", "")
			for _, health := range []string{"valid", "expiring", "expired", "missing"} {
				predicate, args := adminTokenHealthPredicate(health, now)
				add("token_health_"+health+"_count", "token_health", predicate, args...)
			}
			selectSQL, selectArgs := selects.query()

			var counts adminMicrosoftFacetCountsRow
			if err := r.adminMicrosoftFacetQuery(queryCtx, filter, now).
				Select(selectSQL, selectArgs...).
				Scan(&counts).Error; err != nil {
				return nil, fmt.Errorf("admin microsoft facets: %w", err)
			}

			suffixPredicate, suffixArgs := adminMicrosoftFacetPredicate(filter, "suffix", now)
			var suffixRows []microsoftResourceSuffixFacetRow
			if err := r.adminMicrosoftFacetQuery(queryCtx, filter, now).
				Select("mr.email_domain AS facet_key, COUNT(*) AS count").
				Where(suffixPredicate, suffixArgs...).
				Where("mr.email_domain <> ''").
				Group("mr.email_domain").
				Order("count DESC, facet_key ASC").
				Limit(microsoftFacetsSuffixLimit).
				Scan(&suffixRows).Error; err != nil {
				return nil, fmt.Errorf("admin microsoft suffix facets: %w", err)
			}

			facets := adminMicrosoftFacetsFromCounts(counts, suffixRows)
			r.facetsCache.Set(cacheKey, *cloneAdminMicrosoftFacets(facets), runtimeconfig.Duration(
				"admin_resource_facets_cache_ttl_seconds", adminMicrosoftFacetsCacheTTL, time.Second, 1,
			))
			return *facets, nil
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-resultCh:
			if result.Err != nil {
				if attempt == 0 && ctx.Err() == nil && errors.Is(result.Err, context.Canceled) {
					continue
				}
				return nil, result.Err
			}
			facets, ok := result.Val.(coreapp.AdminMicrosoftFacets)
			if !ok {
				return nil, fmt.Errorf("admin microsoft facets: unexpected cache value")
			}
			return cloneAdminMicrosoftFacets(&facets), nil
		}
	}
}

type adminMicrosoftFacetCountsRow struct {
	Matched             int64 `gorm:"column:matched_count"`
	StatusPending       int64 `gorm:"column:status_pending_count"`
	StatusValidating    int64 `gorm:"column:status_validating_count"`
	StatusIdentifying   int64 `gorm:"column:status_identifying_count"`
	StatusNormal        int64 `gorm:"column:status_normal_count"`
	StatusAbnormal      int64 `gorm:"column:status_abnormal_count"`
	StatusDisabled      int64 `gorm:"column:status_disabled_count"`
	StatusDeleted       int64 `gorm:"column:status_deleted_count"`
	ForSaleAll          int64 `gorm:"column:for_sale_all_count"`
	ForSaleYes          int64 `gorm:"column:for_sale_yes_count"`
	ForSaleNo           int64 `gorm:"column:for_sale_no_count"`
	LongLivedAll        int64 `gorm:"column:long_lived_all_count"`
	LongLivedYes        int64 `gorm:"column:long_lived_yes_count"`
	LongLivedNo         int64 `gorm:"column:long_lived_no_count"`
	GraphAvailableAll   int64 `gorm:"column:graph_available_all_count"`
	GraphAvailableYes   int64 `gorm:"column:graph_available_yes_count"`
	GraphAvailableNo    int64 `gorm:"column:graph_available_no_count"`
	TokenHealthAll      int64 `gorm:"column:token_health_all_count"`
	TokenHealthValid    int64 `gorm:"column:token_health_valid_count"`
	TokenHealthExpiring int64 `gorm:"column:token_health_expiring_count"`
	TokenHealthExpired  int64 `gorm:"column:token_health_expired_count"`
	TokenHealthMissing  int64 `gorm:"column:token_health_missing_count"`
}

func adminMicrosoftFacetsFromCounts(counts adminMicrosoftFacetCountsRow, suffixRows []microsoftResourceSuffixFacetRow) *coreapp.AdminMicrosoftFacets {
	status := coreapp.AdminFacetCounts{
		Pending: counts.StatusPending, Validating: counts.StatusValidating,
		Identifying: counts.StatusIdentifying, Normal: counts.StatusNormal,
		Abnormal: counts.StatusAbnormal, Disabled: counts.StatusDisabled, Deleted: counts.StatusDeleted,
	}
	status.All = status.Pending + status.Validating + status.Identifying + status.Normal + status.Abnormal + status.Disabled
	facets := &coreapp.AdminMicrosoftFacets{
		Matched: counts.Matched,
		Status:  status,
		ForSale: coreapp.AdminBooleanFacets{All: counts.ForSaleAll, Yes: counts.ForSaleYes, No: counts.ForSaleNo},
		LongLived: coreapp.AdminBooleanFacets{
			All: counts.LongLivedAll, Yes: counts.LongLivedYes, No: counts.LongLivedNo,
		},
		GraphAvailable: coreapp.AdminBooleanFacets{
			All: counts.GraphAvailableAll, Yes: counts.GraphAvailableYes, No: counts.GraphAvailableNo,
		},
		TokenHealth: coreapp.AdminTokenHealthFacets{
			All: counts.TokenHealthAll, Valid: counts.TokenHealthValid, Expiring: counts.TokenHealthExpiring,
			Expired: counts.TokenHealthExpired, Missing: counts.TokenHealthMissing,
		},
		Suffixes: make([]coreapp.AdminKeyFacet, len(suffixRows)),
	}
	for i, row := range suffixRows {
		facets.Suffixes[i] = coreapp.AdminKeyFacet{Key: "@" + strings.TrimPrefix(strings.ToLower(row.Key), "@"), Count: row.Count}
	}
	return facets
}

func adminMicrosoftFacetPredicate(filter coreapp.AdminMicrosoftListFilter, ignore string, now time.Time) (string, []any) {
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if ignore != "status" {
		if filter.Status != "" {
			conditions = append(conditions, "mr.status = ?")
			args = append(args, string(filter.Status))
		} else {
			conditions = append(conditions, "mr.status <> ?")
			args = append(args, string(domain.MicrosoftStatusDeleted))
		}
	}
	if ignore != "suffix" && filter.Suffix != "" {
		conditions = append(conditions, "mr.email_domain = ?")
		args = append(args, strings.TrimPrefix(strings.ToLower(filter.Suffix), "@"))
	}
	if ignore != "for_sale" && filter.ForSale != nil {
		conditions = append(conditions, "mr.for_sale = ?")
		args = append(args, *filter.ForSale)
	}
	if ignore != "long_lived" && filter.LongLived != nil {
		conditions = append(conditions, "mr.long_lived = ?")
		args = append(args, *filter.LongLived)
	}
	if ignore != "graph_available" && filter.GraphAvailable != nil {
		conditions = append(conditions, "mr.graph_available = ?")
		args = append(args, *filter.GraphAvailable)
	}
	if ignore != "token_health" && filter.TokenHealth != "" {
		condition, conditionArgs := adminTokenHealthPredicate(filter.TokenHealth, now)
		if condition != "" {
			conditions = append(conditions, condition)
			args = append(args, conditionArgs...)
		}
	}
	return facetPredicateSQL(conditions), args
}

func (r *AdminResourceRepo) adminMicrosoftFilterQuery(ctx context.Context, filter coreapp.AdminMicrosoftListFilter, now time.Time, ignore string) *gorm.DB {
	return applyAdminMicrosoftFilters(r.dbFor(ctx).
		Table("email_resources AS er").
		Joins("JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'"),
		filter, now, ignore, "er.id", "er.owner_user_id", "er.created_at")
}

func (r *AdminResourceRepo) adminMicrosoftFacetQuery(ctx context.Context, filter coreapp.AdminMicrosoftListFilter, now time.Time) *gorm.DB {
	query := r.dbFor(ctx).Table("microsoft_resources AS mr")
	if adminMicrosoftFacetNeedsRoot(filter) {
		query = query.Joins("JOIN email_resources AS er ON er.id = mr.id AND er.type = 'microsoft'")
	}
	base := filter
	base.Status = ""
	base.Suffix = ""
	base.ForSale = nil
	base.LongLived = nil
	base.GraphAvailable = nil
	base.TokenHealth = ""
	return applyAdminMicrosoftFilters(query, base, now, "status", "mr.id", "er.owner_user_id", "er.created_at")
}

// Keep every facet filter that references email_resources in this predicate.
func adminMicrosoftFacetNeedsRoot(filter coreapp.AdminMicrosoftListFilter) bool {
	return filter.CreatedFrom != nil || filter.CreatedTo != nil || len(filter.OwnerIDs) > 0
}

func applyAdminMicrosoftFilters(query *gorm.DB, filter coreapp.AdminMicrosoftListFilter, now time.Time, ignore, idColumn, ownerColumn, createdAtColumn string) *gorm.DB {
	if ignore != "status" {
		if filter.Status != "" {
			query = query.Where("mr.status = ?", string(filter.Status))
		} else {
			query = query.Where("mr.status <> ?", string(domain.MicrosoftStatusDeleted))
		}
	}
	if filter.Search != "" {
		query = applyAdminMicrosoftSearch(query, filter, idColumn, ownerColumn)
	} else if len(filter.OwnerIDs) > 0 {
		query = query.Where(ownerColumn+" IN ?", filter.OwnerIDs)
	}
	if ignore != "suffix" && filter.Suffix != "" {
		query = query.Where("mr.email_domain = ?", strings.TrimPrefix(strings.ToLower(filter.Suffix), "@"))
	}
	if ignore != "for_sale" && filter.ForSale != nil {
		query = query.Where("mr.for_sale = ?", *filter.ForSale)
	}
	if ignore != "long_lived" && filter.LongLived != nil {
		query = query.Where("mr.long_lived = ?", *filter.LongLived)
	}
	if ignore != "graph_available" && filter.GraphAvailable != nil {
		query = query.Where("mr.graph_available = ?", *filter.GraphAvailable)
	}
	if ignore != "token_health" && filter.TokenHealth != "" {
		query = applyAdminTokenHealth(query, filter.TokenHealth, now)
	}
	if filter.CreatedFrom != nil {
		query = query.Where(createdAtColumn+" >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where(createdAtColumn+" < ?", *filter.CreatedTo)
	}
	return query
}

func applyAdminMicrosoftSearch(query *gorm.DB, filter coreapp.AdminMicrosoftListFilter, idColumn, ownerColumn string) *gorm.DB {
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	if id, err := strconv.ParseUint(search, 10, 64); err == nil && id > 0 {
		conditions := []string{idColumn + " = ?"}
		args := []any{id}
		if len(filter.OwnerIDs) > 0 {
			conditions = append(conditions, ownerColumn+" IN ?")
			args = append(args, filter.OwnerIDs)
		}
		return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
	}
	if adminMicrosoftSearchUsesCandidates(search) {
		parts := []string{
			"SELECT id AS resource_id FROM microsoft_resources WHERE email_address = ?",
			"SELECT resource_id FROM explicit_aliases WHERE email = ?",
			"SELECT resource_id FROM dot_aliases WHERE email = ?",
			"SELECT resource_id FROM plus_aliases WHERE email = ?",
		}
		args := []any{search, search, search, search}
		if len(filter.OwnerIDs) > 0 {
			return query.
				Joins("LEFT JOIN ("+strings.Join(parts, " UNION ")+") AS admin_resource_search ON admin_resource_search.resource_id = "+idColumn, args...).
				Where("(admin_resource_search.resource_id IS NOT NULL OR "+ownerColumn+" IN ?)", filter.OwnerIDs)
		}
		return query.Joins("JOIN ("+strings.Join(parts, " UNION ")+") AS admin_resource_search ON admin_resource_search.resource_id = "+idColumn, args...)
	}

	pattern := escapeAdminLike(search) + "%"
	domainPattern := escapeAdminLike(strings.TrimPrefix(search, "@")) + "%"
	conditions := []string{
		"mr.email_address LIKE ? ESCAPE '\\\\'",
		"mr.email_domain LIKE ? ESCAPE '\\\\'",
		"EXISTS (SELECT 1 FROM explicit_aliases ea WHERE ea.resource_id = " + idColumn + " AND ea.email LIKE ? ESCAPE '\\\\')",
		"EXISTS (SELECT 1 FROM dot_aliases da WHERE da.resource_id = " + idColumn + " AND da.email LIKE ? ESCAPE '\\\\')",
		"EXISTS (SELECT 1 FROM plus_aliases pa WHERE pa.resource_id = " + idColumn + " AND pa.email LIKE ? ESCAPE '\\\\')",
	}
	args := []any{pattern, domainPattern, pattern, pattern, pattern}
	if len(filter.OwnerIDs) > 0 {
		conditions = append(conditions, ownerColumn+" IN ?")
		args = append(args, filter.OwnerIDs)
	}
	// ponytail: indexed prefixes are the compatibility ceiling; add a search index if arbitrary substrings become mandatory.
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func adminMicrosoftSearchUsesCandidates(search string) bool {
	search = strings.TrimSpace(search)
	address, err := mail.ParseAddress(search)
	return err == nil && strings.EqualFold(address.Address, search)
}

func applyAdminTokenHealth(query *gorm.DB, health string, now time.Time) *gorm.DB {
	predicate, args := adminTokenHealthPredicate(health, now)
	if predicate == "" {
		return query
	}
	return query.Where(predicate, args...)
}

func adminTokenHealthPredicate(health string, now time.Time) (string, []any) {
	switch health {
	case "missing":
		return "(mr.client_id = '' OR mr.refresh_token = '')", nil
	case "expired":
		return "mr.client_id <> '' AND mr.refresh_token <> '' AND mr.rt_expire_at IS NOT NULL AND mr.rt_expire_at <= ?", []any{now}
	case "expiring":
		return "mr.client_id <> '' AND mr.refresh_token <> '' AND mr.rt_expire_at > ? AND mr.rt_expire_at <= ?", []any{now, now.Add(7 * 24 * time.Hour)}
	case "valid":
		return "mr.client_id <> '' AND mr.refresh_token <> '' AND (mr.rt_expire_at IS NULL OR mr.rt_expire_at > ?)", []any{now.Add(7 * 24 * time.Hour)}
	default:
		return "", nil
	}
}

func adminMicrosoftFacetsCacheKey(filter coreapp.AdminMicrosoftListFilter) string {
	ownerIDs := make([]string, len(filter.OwnerIDs))
	for i, ownerID := range filter.OwnerIDs {
		ownerIDs[i] = strconv.FormatUint(uint64(ownerID), 10)
	}
	return fmt.Sprintf("generation=%d|search=%q|suffix=%q|status=%q|sale=%s|long=%s|graph=%s|token=%q|from=%s|to=%s|owners=%s",
		currentMicrosoftFacetsGeneration(),
		strings.ToLower(strings.TrimSpace(filter.Search)), strings.ToLower(strings.TrimSpace(filter.Suffix)), filter.Status,
		resourceBoolPtrKey(filter.ForSale), resourceBoolPtrKey(filter.LongLived), resourceBoolPtrKey(filter.GraphAvailable), filter.TokenHealth,
		resourceTimePtrKey(filter.CreatedFrom), resourceTimePtrKey(filter.CreatedTo), strings.Join(ownerIDs, ","))
}

func cloneAdminMicrosoftFacets(facets *coreapp.AdminMicrosoftFacets) *coreapp.AdminMicrosoftFacets {
	if facets == nil {
		return nil
	}
	clone := *facets
	clone.Suffixes = append([]coreapp.AdminKeyFacet(nil), facets.Suffixes...)
	return &clone
}

func escapeAdminLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func adminMicrosoftRecord(row adminMicrosoftRow) coreapp.AdminMicrosoftRecord {
	record := coreapp.AdminMicrosoftRecord{
		ID:                     row.ID,
		OwnerUserID:            row.OwnerUserID,
		Version:                row.Version,
		EmailAddress:           row.EmailAddress,
		EmailDomain:            row.EmailDomain,
		Status:                 domain.MicrosoftResourceStatus(row.Status),
		ForSale:                row.ForSale,
		LongLived:              row.LongLived,
		GraphAvailable:         row.GraphAvailable,
		QualityScore:           row.QualityScore,
		RefreshTokenConfigured: row.RefreshTokenConfigured,
		PasswordConfigured:     row.PasswordConfigured,
		ClientIDConfigured:     row.ClientIDConfigured,
		CredentialRevision:     row.CredentialRevision,
		CredentialUpdatedAt:    row.CredentialUpdatedAt,
		RTExpireAt:             row.RTExpireAt,
		TokenLastRefreshedAt:   row.TokenLastRefreshedAt,
		TokenLastRequestID:     row.TokenLastRequestID,
		LastAllocatedAt:        row.LastAllocatedAt,
		LastSafeError:          row.LastSafeError,
		ExplicitAliasCount:     row.ExplicitAliasCount,
		DotAliasCount:          row.DotAliasCount,
		PlusAliasCount:         row.PlusAliasCount,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
	}
	return record
}

func (r *AdminResourceRepo) ListAdminMicrosoftAliases(ctx context.Context, resourceID uint, kind string, offset, limit int) ([]coreapp.AdminMicrosoftAliasItem, int64, error) {
	var exists int64
	if err := r.dbFor(ctx).Table("microsoft_resources").Where("id = ?", resourceID).Count(&exists).Error; err != nil {
		return nil, 0, fmt.Errorf("find admin microsoft aliases resource: %w", err)
	}
	if exists == 0 {
		return nil, 0, domain.ErrResourceNotFound
	}
	type aliasRow struct {
		ID        uint64
		Kind      string
		Email     string
		CreatedAt time.Time
	}
	var rows []aliasRow
	var total int64
	if kind == "explicit" {
		if err := r.dbFor(ctx).Table("explicit_aliases").Where("resource_id = ?", resourceID).Count(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("count admin explicit aliases: %w", err)
		}
		if err := r.dbFor(ctx).Raw(`
SELECT (CAST(id AS UNSIGNED) * 4 + 1) AS id, 'explicit' AS kind, email, created_at
FROM explicit_aliases
WHERE resource_id = ?
ORDER BY explicit_aliases.created_at DESC, explicit_aliases.id DESC
LIMIT ? OFFSET ?`, resourceID, limit, offset).Scan(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("list admin explicit aliases: %w", err)
		}
	} else {
		if err := r.dbFor(ctx).Raw(`
SELECT
    (SELECT COUNT(*) FROM dot_aliases WHERE resource_id = ?) +
    (SELECT COUNT(*) FROM plus_aliases WHERE resource_id = ?) AS total`, resourceID, resourceID).Scan(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("count admin other aliases: %w", err)
		}
		if err := r.dbFor(ctx).Raw(`
SELECT * FROM (
    SELECT (CAST(id AS UNSIGNED) * 4 + 2) AS id, 'dot' AS kind, email, created_at
    FROM dot_aliases WHERE resource_id = ?
    UNION ALL
    SELECT (CAST(id AS UNSIGNED) * 4 + 3) AS id, 'plus' AS kind, email, created_at
    FROM plus_aliases WHERE resource_id = ?
) aliases
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?`, resourceID, resourceID, limit, offset).Scan(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("list admin other aliases: %w", err)
		}
	}
	items := make([]coreapp.AdminMicrosoftAliasItem, len(rows))
	for i := range rows {
		items[i] = coreapp.AdminMicrosoftAliasItem{ID: rows[i].ID, Kind: rows[i].Kind, EmailAddress: rows[i].Email, CreatedAt: rows[i].CreatedAt}
	}
	return items, total, nil
}
