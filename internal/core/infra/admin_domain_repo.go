package infra

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type adminDomainRow struct {
	ID              uint
	OwnerUserID     uint
	Version         uint64
	Domain          string
	DomainTLD       string
	MailServerID    uint
	Purpose         string
	Status          string
	MailboxCount    int64
	LastSafeError   string
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const adminDomainListSelect = `
er.id AS id,
er.owner_user_id AS owner_user_id,
er.version AS version,
dr.domain AS domain,
dr.domain_tld AS domain_tld,
dr.mail_server_id AS mail_server_id,
dr.purpose AS purpose,
dr.status AS status,
(SELECT COUNT(*) FROM generated_mailboxes gm WHERE gm.resource_id = er.id AND gm.owner_user_id = er.owner_user_id AND gm.status <> 'retired') AS mailbox_count,
dr.last_safe_error AS last_safe_error,
dr.last_allocated_at AS last_allocated_at,
er.created_at AS created_at,
GREATEST(er.updated_at, dr.updated_at) AS updated_at`

func (r *AdminResourceRepo) ListAdminDomains(ctx context.Context, filter coreapp.AdminDomainListFilter, offset, limit int, afterID uint) ([]coreapp.AdminDomainRecord, int64, error) {
	base := r.adminDomainFilterQuery(ctx, filter, false, false, false)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count administrator domains: %w", err)
	}
	query := base.Session(&gorm.Session{}).Select(adminDomainListSelect).Order("er.id DESC")
	if afterID > 0 {
		query = query.Where("er.id < ?", afterID)
	} else {
		query = query.Offset(offset)
	}
	var rows []adminDomainRow
	if err := query.Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list administrator domains: %w", err)
	}
	records := make([]coreapp.AdminDomainRecord, len(rows))
	for i := range rows {
		records[i] = adminDomainRecord(rows[i])
	}
	return records, total, nil
}

func (r *AdminResourceRepo) FindAdminDomain(ctx context.Context, resourceID uint) (*coreapp.AdminDomainRecord, error) {
	if resourceID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	var row adminDomainRow
	result := r.dbFor(ctx).
		Table("email_resources AS er").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Select(adminDomainListSelect).
		Where("er.id = ?", resourceID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, domain.ErrResourceNotFound
	}
	if result.Error != nil {
		return nil, fmt.Errorf("find administrator domain: %w", result.Error)
	}
	record := adminDomainRecord(row)
	return &record, nil
}

func (r *AdminResourceRepo) AdminDomainFacets(ctx context.Context, filter coreapp.AdminDomainListFilter) (*coreapp.AdminDomainFacets, error) {
	result := &coreapp.AdminDomainFacets{}
	type countRow struct {
		Key   string
		Count int64
	}

	var statusRows []countRow
	if err := r.adminDomainFilterQuery(ctx, filter, true, false, true).
		Select("dr.status AS `key`, COUNT(*) AS `count`").
		Group("dr.status").
		Scan(&statusRows).Error; err != nil {
		return nil, fmt.Errorf("count administrator domain status facets: %w", err)
	}
	for _, row := range statusRows {
		switch domain.MailDomainStatus(row.Key) {
		case domain.DomainStatusPending:
			result.Status.Pending = row.Count
			result.Status.All += row.Count
		case domain.DomainStatusValidating:
			result.Status.Validating = row.Count
			result.Status.All += row.Count
		case domain.DomainStatusNormal:
			result.Status.Normal = row.Count
			result.Status.All += row.Count
		case domain.DomainStatusAbnormal:
			result.Status.Abnormal = row.Count
			result.Status.All += row.Count
		case domain.DomainStatusDisabled:
			result.Status.Disabled = row.Count
			result.Status.All += row.Count
		case domain.DomainStatusDeleted:
			result.Status.Deleted = row.Count
		}
	}

	var purposeRows []countRow
	if err := r.adminDomainFilterQuery(ctx, filter, false, true, true).
		Select("dr.purpose AS `key`, COUNT(*) AS `count`").
		Group("dr.purpose").
		Scan(&purposeRows).Error; err != nil {
		return nil, fmt.Errorf("count administrator domain purpose facets: %w", err)
	}
	for _, row := range purposeRows {
		result.Purpose.All += row.Count
		switch domain.ResourcePurpose(row.Key) {
		case domain.PurposeNotSale:
			result.Purpose.NotSale = row.Count
		case domain.PurposeSale:
			result.Purpose.Sale = row.Count
		case domain.PurposeBinding:
			result.Purpose.Binding = row.Count
		}
	}

	var tldRows []countRow
	if err := r.adminDomainFilterQuery(ctx, filter, false, false, true).
		Select("dr.domain_tld AS `key`, COUNT(*) AS `count`").
		Group("dr.domain_tld").
		Order("count DESC, `key` ASC").
		Scan(&tldRows).Error; err != nil {
		return nil, fmt.Errorf("count administrator domain TLD facets: %w", err)
	}
	result.TLDs = make([]coreapp.AdminKeyFacet, 0, len(tldRows))
	for _, row := range tldRows {
		if row.Key != "" {
			result.TLDs = append(result.TLDs, coreapp.AdminKeyFacet{Key: row.Key, Count: row.Count})
		}
	}
	return result, nil
}

func (r *AdminResourceRepo) ListAdminDomainIDs(ctx context.Context, filter coreapp.AdminDomainListFilter, limit int) ([]uint, error) {
	var ids []uint
	if err := r.adminDomainFilterQuery(ctx, filter, false, false, false).
		Select("er.id").
		Order("er.id ASC").
		Limit(limit).
		Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list administrator domain ids: %w", err)
	}
	return ids, nil
}

// MaxAdminDomainID captures the highest matching domain id, used as an async
// bulk batch's frozen high-water mark so rows inserted mid-batch are excluded.
func (r *AdminResourceRepo) MaxAdminDomainID(ctx context.Context, filter coreapp.AdminDomainListFilter) (uint, error) {
	var maxID uint
	if err := r.adminDomainFilterQuery(ctx, filter, false, false, false).
		Select("COALESCE(MAX(er.id), 0)").
		Row().Scan(&maxID); err != nil {
		return 0, fmt.Errorf("capture admin domain bulk high-water mark: %w", err)
	}
	return maxID, nil
}

// ListAdminDomainBulkPageIDs returns up to limit matching domain ids on the
// (afterID, throughID] cursor window, ascending, for one async batch page.
func (r *AdminResourceRepo) ListAdminDomainBulkPageIDs(ctx context.Context, filter coreapp.AdminDomainListFilter, afterID, throughID uint, limit int) ([]uint, error) {
	query := r.adminDomainFilterQuery(ctx, filter, false, false, false).
		Select("er.id").
		Where("er.id > ?", afterID)
	if throughID > 0 {
		query = query.Where("er.id <= ?", throughID)
	}
	var ids []uint
	if err := query.Order("er.id ASC").Limit(limit).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list admin domain bulk page ids: %w", err)
	}
	return ids, nil
}

func (r *AdminResourceRepo) adminDomainFilterQuery(ctx context.Context, filter coreapp.AdminDomainListFilter, ignoreStatus, ignorePurpose, ignoreTLD bool) *gorm.DB {
	query := r.dbFor(ctx).
		Table("email_resources AS er").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain))
	if !ignoreStatus {
		if filter.Status != "" {
			query = query.Where("dr.status = ?", string(filter.Status))
		} else {
			query = query.Where("dr.status <> ?", string(domain.DomainStatusDeleted))
		}
	}
	if !ignorePurpose && filter.Purpose != "" {
		query = query.Where("dr.purpose = ?", string(filter.Purpose))
	}
	if !ignoreTLD && filter.TLD != "" {
		query = query.Where("dr.domain_tld = ?", filter.TLD)
	}
	if filter.OwnerID != 0 {
		query = query.Where("er.owner_user_id = ?", filter.OwnerID)
	}
	if filter.MailServerID != 0 {
		query = query.Where("dr.mail_server_id = ?", filter.MailServerID)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("er.created_at < ?", *filter.CreatedTo)
	}
	if filter.Search != "" {
		escaped := strings.ToLower(escapeAdminLike(filter.Search))
		conditions := []string{"LOWER(dr.domain) LIKE ? ESCAPE '\\\\'"}
		args := []any{"%" + escaped + "%"}
		if id, err := strconv.ParseUint(filter.Search, 10, 64); err == nil && id > 0 {
			conditions = append(conditions, "er.id = ?", "er.owner_user_id = ?")
			args = append(args, id, id)
		}
		if len(filter.OwnerIDs) > 0 {
			conditions = append(conditions, "er.owner_user_id IN ?")
			args = append(args, filter.OwnerIDs)
		}
		query = query.Where("("+strings.Join(conditions, " OR ")+")", args...)
	}
	return query
}

func (r *AdminResourceRepo) LockAdminDomain(ctx context.Context, resourceID uint) (*domain.EmailResource, *domain.MailDomainResource, error) {
	if resourceID == 0 {
		return nil, nil, domain.ErrResourceNotFound
	}
	db := r.dbFor(ctx)
	var root EmailResourceModel
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", resourceID, string(domain.ResourceTypeDomain)).
		First(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, domain.ErrResourceNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock administrator domain root: %w", err)
	}
	var resource DomainResourceModel
	err = db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, domain.ErrResourceNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock administrator domain: %w", err)
	}
	return root.toDomain(), resource.toDomain(), nil
}

func (r *AdminResourceRepo) LockAdminDomainMailServer(ctx context.Context, mailServerID uint) (*domain.MailServer, error) {
	if mailServerID == 0 {
		return nil, domain.ErrMailServerNotFound
	}
	var model MailServerModel
	err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, mailServerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrMailServerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock administrator domain mail server: %w", err)
	}
	return model.toDomain(), nil
}

func (r *AdminResourceRepo) CreateAdminDomain(ctx context.Context, root *domain.EmailResource, resource *domain.MailDomainResource) error {
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return domain.ErrResourceDependency
	}
	return createDomainTx(tx.WithContext(ctx), root, resource)
}

func (r *AdminResourceRepo) SaveAdminDomain(ctx context.Context, root *domain.EmailResource, resource *domain.MailDomainResource, expectedVersion uint64, previousOwnerID uint) error {
	if root == nil || resource == nil || root.ID == 0 || resource.ID != root.ID || expectedVersion == 0 || previousOwnerID == 0 {
		return domain.ErrInvalidResourceCommand
	}
	db := r.dbFor(ctx)
	now := time.Now().UTC()
	rootUpdate := db.Model(&EmailResourceModel{}).
		Where("id = ? AND type = ? AND version = ?", root.ID, string(domain.ResourceTypeDomain), expectedVersion).
		Updates(map[string]any{"owner_user_id": root.OwnerUserID, "version": expectedVersion + 1, "updated_at": now})
	if rootUpdate.Error != nil {
		return fmt.Errorf("save administrator domain root: %w", rootUpdate.Error)
	}
	if rootUpdate.RowsAffected == 0 {
		return domain.ErrResourceVersionConflict
	}
	resourceUpdate := db.Model(&DomainResourceModel{}).
		Where("id = ?", resource.ID).
		Updates(map[string]any{
			"owner_user_id": root.OwnerUserID, "domain": resource.Domain, "domain_tld": domain.TLD(resource.Domain),
			"mail_server_id": resource.MailServerID, "purpose": string(resource.Purpose), "status": string(resource.Status),
			"validation_generation": resource.ValidationGeneration, "validation_failures": resource.ValidationFailures,
			"mailbox_daily_limit": normalizeDailyLimit(resource.MailboxDailyLimit, domain.DefaultMailboxDailyLimit),
			"last_safe_error":     resource.LastSafeError, "last_allocated_at": resource.LastAllocatedAt, "updated_at": now,
		})
	if resourceUpdate.Error != nil {
		if isDuplicateKeyError(resourceUpdate.Error) {
			return domain.ErrDuplicateDomain
		}
		return fmt.Errorf("save administrator domain: %w", resourceUpdate.Error)
	}
	if resourceUpdate.RowsAffected == 0 {
		return domain.ErrResourceNotFound
	}
	if previousOwnerID != root.OwnerUserID {
		if err := db.Model(&GeneratedMailboxModel{}).
			Where("resource_id = ? AND owner_user_id = ?", root.ID, previousOwnerID).
			Updates(map[string]any{"owner_user_id": root.OwnerUserID}).Error; err != nil {
			return fmt.Errorf("transfer administrator domain mailboxes: %w", err)
		}
	}
	root.Version = expectedVersion + 1
	root.UpdatedAt = now
	resource.UpdatedAt = now
	return nil
}

func adminDomainRecord(row adminDomainRow) coreapp.AdminDomainRecord {
	return coreapp.AdminDomainRecord{
		ID: row.ID, OwnerUserID: row.OwnerUserID, Version: row.Version, Domain: row.Domain, DomainTLD: row.DomainTLD,
		MailServerID: row.MailServerID, Purpose: domain.ResourcePurpose(row.Purpose), Status: domain.MailDomainStatus(row.Status),
		MailboxCount: row.MailboxCount, LastSafeError: row.LastSafeError, LastAllocatedAt: row.LastAllocatedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
