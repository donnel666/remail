package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx))
	})
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
	if cached, ok := r.facetsCache.Get(cacheKey); ok {
		return cloneAdminMicrosoftFacets(&cached), nil
	}

	value, err, _ := r.facetsFlight.Do(cacheKey, func() (any, error) {
		if cached, ok := r.facetsCache.Get(cacheKey); ok {
			return cached, nil
		}
		var rows []adminMicrosoftFacetRow
		if err := r.adminMicrosoftFacetQuery(ctx, filter, now).
			Select(adminMicrosoftFacetSelect, now, now.Add(7*24*time.Hour)).
			Group("mr.status, mr.for_sale, mr.long_lived, mr.graph_available, token_health, mr.email_domain").
			Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("admin microsoft facets: %w", err)
		}
		facets := adminMicrosoftFacetsFromRows(rows, filter)
		r.facetsCache.Set(cacheKey, *cloneAdminMicrosoftFacets(facets), runtimeconfig.Duration(
			"admin_resource_facets_cache_ttl_seconds", adminMicrosoftFacetsCacheTTL, time.Second, 1,
		))
		return *facets, nil
	})
	if err != nil {
		return nil, err
	}
	facets, ok := value.(coreapp.AdminMicrosoftFacets)
	if !ok {
		return nil, fmt.Errorf("admin microsoft facets: unexpected cache value")
	}
	return cloneAdminMicrosoftFacets(&facets), nil
}

type adminMicrosoftFacetRow struct {
	Status         string
	ForSale        bool
	LongLived      bool
	GraphAvailable bool
	TokenHealth    string
	EmailDomain    string
	Count          int64
}

const adminTokenHealthExpression = `CASE
    WHEN mr.client_id = '' OR mr.refresh_token = '' THEN 'missing'
    WHEN mr.rt_expire_at IS NOT NULL AND mr.rt_expire_at <= ? THEN 'expired'
    WHEN mr.rt_expire_at IS NOT NULL AND mr.rt_expire_at <= ? THEN 'expiring'
    ELSE 'valid'
END`

const adminMicrosoftFacetSelect = `
mr.status AS status,
mr.for_sale AS for_sale,
mr.long_lived AS long_lived,
mr.graph_available AS graph_available,
` + adminTokenHealthExpression + ` AS token_health,
mr.email_domain AS email_domain,
COUNT(*) AS count`

func adminMicrosoftFacetsFromRows(rows []adminMicrosoftFacetRow, filter coreapp.AdminMicrosoftListFilter) *coreapp.AdminMicrosoftFacets {
	facets := &coreapp.AdminMicrosoftFacets{}
	suffixes := make(map[string]int64)
	for _, row := range rows {
		if adminMicrosoftFacetRowMatches(row, filter, "") {
			facets.Matched += row.Count
		}
		if adminMicrosoftFacetRowMatches(row, filter, "status") {
			addAdminStatusFacet(&facets.Status, domain.MicrosoftResourceStatus(row.Status), row.Count)
		}
		if adminMicrosoftFacetRowMatches(row, filter, "for_sale") {
			addAdminBooleanFacet(&facets.ForSale, row.ForSale, row.Count)
		}
		if adminMicrosoftFacetRowMatches(row, filter, "long_lived") {
			addAdminBooleanFacet(&facets.LongLived, row.LongLived, row.Count)
		}
		if adminMicrosoftFacetRowMatches(row, filter, "graph_available") {
			addAdminBooleanFacet(&facets.GraphAvailable, row.GraphAvailable, row.Count)
		}
		if adminMicrosoftFacetRowMatches(row, filter, "token_health") {
			addAdminTokenFacet(&facets.TokenHealth, row.TokenHealth, row.Count)
		}
		if row.EmailDomain != "" && adminMicrosoftFacetRowMatches(row, filter, "suffix") {
			key := "@" + strings.TrimPrefix(strings.ToLower(row.EmailDomain), "@")
			suffixes[key] += row.Count
		}
	}
	facets.Status.All = facets.Status.Pending + facets.Status.Validating + facets.Status.Identifying + facets.Status.Normal + facets.Status.Abnormal + facets.Status.Disabled
	facets.Suffixes = make([]coreapp.AdminKeyFacet, 0, len(suffixes))
	for key, count := range suffixes {
		facets.Suffixes = append(facets.Suffixes, coreapp.AdminKeyFacet{Key: key, Count: count})
	}
	sort.Slice(facets.Suffixes, func(i, j int) bool {
		if facets.Suffixes[i].Count == facets.Suffixes[j].Count {
			return facets.Suffixes[i].Key < facets.Suffixes[j].Key
		}
		return facets.Suffixes[i].Count > facets.Suffixes[j].Count
	})
	return facets
}

func adminMicrosoftFacetRowMatches(row adminMicrosoftFacetRow, filter coreapp.AdminMicrosoftListFilter, ignore string) bool {
	if ignore != "status" && ((filter.Status != "" && row.Status != string(filter.Status)) || (filter.Status == "" && row.Status == string(domain.MicrosoftStatusDeleted))) {
		return false
	}
	if ignore != "suffix" && filter.Suffix != "" && row.EmailDomain != strings.TrimPrefix(strings.ToLower(filter.Suffix), "@") {
		return false
	}
	if ignore != "for_sale" && filter.ForSale != nil && row.ForSale != *filter.ForSale {
		return false
	}
	if ignore != "long_lived" && filter.LongLived != nil && row.LongLived != *filter.LongLived {
		return false
	}
	if ignore != "graph_available" && filter.GraphAvailable != nil && row.GraphAvailable != *filter.GraphAvailable {
		return false
	}
	return ignore == "token_health" || filter.TokenHealth == "" || row.TokenHealth == filter.TokenHealth
}

func addAdminStatusFacet(facets *coreapp.AdminFacetCounts, status domain.MicrosoftResourceStatus, count int64) {
	switch status {
	case domain.MicrosoftStatusPending:
		facets.Pending += count
	case domain.MicrosoftStatusValidating:
		facets.Validating += count
	case domain.MicrosoftStatusIdentifying:
		facets.Identifying += count
	case domain.MicrosoftStatusNormal:
		facets.Normal += count
	case domain.MicrosoftStatusAbnormal:
		facets.Abnormal += count
	case domain.MicrosoftStatusDisabled:
		facets.Disabled += count
	case domain.MicrosoftStatusDeleted:
		facets.Deleted += count
	}
}

func addAdminBooleanFacet(facets *coreapp.AdminBooleanFacets, value bool, count int64) {
	facets.All += count
	if value {
		facets.Yes += count
	} else {
		facets.No += count
	}
}

func addAdminTokenFacet(facets *coreapp.AdminTokenHealthFacets, health string, count int64) {
	facets.All += count
	switch health {
	case "valid":
		facets.Valid += count
	case "expiring":
		facets.Expiring += count
	case "expired":
		facets.Expired += count
	case "missing":
		facets.Missing += count
	}
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
		escaped := escapeAdminLike(filter.Search)
		pattern := "%" + strings.ToLower(escaped) + "%"
		conditions := []string{
			"LOWER(mr.email_address) LIKE ? ESCAPE '\\\\'",
			"EXISTS (SELECT 1 FROM explicit_aliases ea WHERE ea.resource_id = " + idColumn + " AND LOWER(ea.email) LIKE ? ESCAPE '\\\\')",
			"EXISTS (SELECT 1 FROM dot_aliases da WHERE da.resource_id = " + idColumn + " AND LOWER(da.email) LIKE ? ESCAPE '\\\\')",
			"EXISTS (SELECT 1 FROM plus_aliases pa WHERE pa.resource_id = " + idColumn + " AND LOWER(pa.email) LIKE ? ESCAPE '\\\\')",
		}
		args := []any{pattern, pattern, pattern, pattern}
		if id, err := strconv.ParseUint(filter.Search, 10, 64); err == nil && id > 0 {
			conditions = append(conditions, idColumn+" = ?")
			args = append(args, id)
		}
		if len(filter.OwnerIDs) > 0 {
			conditions = append(conditions, ownerColumn+" IN ?")
			args = append(args, filter.OwnerIDs)
		}
		query = query.Where("("+strings.Join(conditions, " OR ")+")", args...)
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

func applyAdminTokenHealth(query *gorm.DB, health string, now time.Time) *gorm.DB {
	switch health {
	case "missing":
		return query.Where("mr.client_id = '' OR mr.refresh_token = ''")
	case "expired":
		return query.Where("mr.client_id <> '' AND mr.refresh_token <> '' AND mr.rt_expire_at IS NOT NULL AND mr.rt_expire_at <= ?", now)
	case "expiring":
		return query.Where("mr.client_id <> '' AND mr.refresh_token <> '' AND mr.rt_expire_at > ? AND mr.rt_expire_at <= ?", now, now.Add(7*24*time.Hour))
	case "valid":
		return query.Where("mr.client_id <> '' AND mr.refresh_token <> '' AND (mr.rt_expire_at IS NULL OR mr.rt_expire_at > ?)", now.Add(7*24*time.Hour))
	default:
		return query
	}
}

func adminMicrosoftFacetsCacheKey(filter coreapp.AdminMicrosoftListFilter) string {
	ownerIDs := make([]string, len(filter.OwnerIDs))
	for i, ownerID := range filter.OwnerIDs {
		ownerIDs[i] = strconv.FormatUint(uint64(ownerID), 10)
	}
	return fmt.Sprintf("search=%q|suffix=%q|status=%q|sale=%s|long=%s|graph=%s|token=%q|from=%s|to=%s|owners=%s",
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
