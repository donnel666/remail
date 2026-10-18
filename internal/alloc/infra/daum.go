package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DaumAllocationModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo            string     `gorm:"type:varchar(64);not null;column:order_no"`
	GuardType          string     `gorm:"type:varchar(32);not null;column:guard_type"`
	ProjectID          uint       `gorm:"not null;column:project_id"`
	ProductID          uint       `gorm:"not null;column:product_id"`
	ResourceID         uint       `gorm:"not null;column:resource_id"`
	OwnerUserID        uint       `gorm:"not null;column:owner_user_id"`
	SupplyScope        string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	ExplicitAliasID    *uint      `gorm:"column:explicit_alias_id"`
	Mailbox            string     `gorm:"type:varchar(16);not null;column:mailbox"`
	ServiceMode        string     `gorm:"type:varchar(32);not null;column:service_mode"`
	Email              string     `gorm:"type:varchar(320);not null;column:email"`
	Status             string     `gorm:"type:varchar(32);not null;column:status"`
	CostPointsSnapshot string     `gorm:"type:decimal(18,6);not null;column:cost_points_snapshot"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt         *time.Time `gorm:"column:released_at"`
}

func (DaumAllocationModel) TableName() string { return "daum_allocations" }

func daumAllocationFromDomain(allocation *domain.DaumAllocation) *DaumAllocationModel {
	return &DaumAllocationModel{
		ID: allocation.ID, OrderNo: allocation.OrderNo, GuardType: string(domain.AllocationTypeDaum),
		ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
		OwnerUserID: allocation.OwnerUserID,
		SupplyScope: string(domain.NormalizeSupplyScope(allocation.SupplyScope)), Mailbox: allocation.Mailbox, ExplicitAliasID: allocation.ExplicitAliasID,
		ServiceMode: allocation.ServiceMode, Email: strings.ToLower(strings.TrimSpace(allocation.Email)),
		Status: string(allocation.Status), CostPointsSnapshot: allocation.CostPointsSnapshot,
		CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
	}
}

func (m DaumAllocationModel) toDomain() domain.DaumAllocation {
	return domain.DaumAllocation{
		ID: m.ID, OrderNo: m.OrderNo, ProjectID: m.ProjectID, ProductID: m.ProductID,
		ResourceID: m.ResourceID, SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		OwnerUserID: m.OwnerUserID,
		Mailbox:     m.Mailbox, ExplicitAliasID: m.ExplicitAliasID, ServiceMode: m.ServiceMode, Email: m.Email,
		Status: domain.AllocationStatus(m.Status), CostPointsSnapshot: m.CostPointsSnapshot,
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

func (m DaumAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type: domain.AllocationTypeDaum, ID: m.ID, OrderNo: m.OrderNo,
		ProjectID: m.ProjectID, ProductID: m.ProductID, ResourceID: m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     m.Mailbox, Email: m.Email, Status: domain.AllocationStatus(m.Status),
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

// Each row is a deliverable address; health, ownership and connection identity remain on the root.
const daumAddressEmailSQL = "CASE WHEN suffix.suffix = 'daum.net' THEN dr.email_address ELSE alias.email_address END"
const daumCandidateColumns = "dr.id AS resource_id, dr.owner_user_id, alias.id AS explicit_alias_id, CASE WHEN suffix.suffix = 'daum.net' THEN 'main' ELSE 'alias' END AS mailbox, " + daumAddressEmailSQL + " AS email, dr.last_allocated_at"

func daumAddressQuery(db *gorm.DB) *gorm.DB {
	return db.Table("daum_resources AS dr").
		Joins("JOIN (SELECT 'daum.net' AS suffix UNION ALL SELECT 'hanmail.net' UNION ALL SELECT 'hanmail2.net') AS suffix ON TRUE").
		Joins("LEFT JOIN daum_explicit_aliases AS alias ON alias.resource_id = dr.id AND alias.email_domain = suffix.suffix AND alias.status = 'normal'").
		Where("dr.resource_type = 'daum' AND (suffix.suffix = 'daum.net' OR alias.id IS NOT NULL)")
}

func daumSourceCandidateQuery(db *gorm.DB, projectID, buyerUserID uint, scope domain.SupplyScope) *gorm.DB {
	query := daumAddressQuery(db).Where("dr.status = 'normal' AND dr.app_password <> ''").
		Where("NOT EXISTS (SELECT 1 FROM daum_allocations history WHERE history.resource_id = dr.id AND history.project_id = ? AND history.email = "+daumAddressEmailSQL+")", projectID).
		Where("NOT EXISTS (SELECT 1 FROM project_daum_suffix_blacklists blacklist WHERE blacklist.project_id = ? AND blacklist.suffix = suffix.suffix)", projectID)
	owner := db.Table("email_resources AS er").Select("1").Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("er.id = dr.id AND er.type = 'daum' AND er.owner_user_id = dr.owner_user_id")
	if scope == domain.SupplyScopeOwned {
		query = query.Where("dr.for_sale = FALSE AND suffix.suffix = 'daum.net'")
		owner = owner.Where("er.owner_user_id = ?", buyerUserID)
	} else {
		query = query.Where("dr.for_sale = TRUE")
		owner = owner.Where("owner.status = 'active' AND owner.role IN ('supplier','admin','super_admin')")
	}
	return query.Where("EXISTS (?)", owner)
}

func (r *Repo) ListDaumSourceCandidates(ctx context.Context, projectID, buyerUserID uint, scope domain.SupplyScope, buckets []uint16, limit int, emailSuffix string) ([]allocapp.DaumCandidate, error) {
	if projectID == 0 || buyerUserID == 0 || len(buckets) == 0 || len(buckets) > allocapp.MicrosoftExpansionBuckets || limit <= 0 || (emailSuffix != "" && !coredomain.IsDaumEmailSuffix(emailSuffix)) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	for _, bucket := range buckets {
		if bucket >= allocapp.DaumBucketCount {
			return nil, domain.ErrInvalidAllocationRequest
		}
	}
	query := daumSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope)
	if emailSuffix != "" {
		query = query.Where("suffix.suffix = ?", emailSuffix)
	}
	query = query.Where("dr.alloc_bucket IN ?", buckets)
	var rows []allocapp.DaumCandidate
	if err := query.Select(daumCandidateColumns).Order("dr.last_allocated_at ASC, dr.quality_score DESC, dr.id ASC, suffix.suffix ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Daum allocation candidates: %w", err)
	}
	return rows, nil
}

func (r *Repo) LockDaumCandidate(ctx context.Context, candidate allocapp.DaumCandidate, projectID, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*allocapp.DaumCandidate, error) {
	if candidate.ResourceID == 0 || candidate.Email == "" || projectID == 0 || buyerUserID == 0 || (emailSuffix != "" && !coredomain.IsDaumEmailSuffix(emailSuffix)) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	// The caller holds email_resources first; lock the provider child before rechecking address availability.
	var resource struct{ ID uint }
	lock := r.dbFor(ctx).Table("daum_resources").Select("id").Where("id = ? AND resource_type = 'daum'", candidate.ResourceID)
	if r.dbFor(ctx).Name() == "mysql" {
		lock = lock.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
	if err := lock.Scan(&resource).Error; err != nil {
		return nil, fmt.Errorf("lock Daum resource: %w", err)
	}
	if resource.ID == 0 {
		return nil, nil
	}
	query := daumSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope).Where("dr.id = ? AND "+daumAddressEmailSQL+" = ?", candidate.ResourceID, candidate.Email)
	if emailSuffix != "" {
		query = query.Where("suffix.suffix = ?", emailSuffix)
	}
	var row allocapp.DaumCandidate
	if r.dbFor(ctx).Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Select(daumCandidateColumns).Limit(1).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("recheck Daum allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) DaumAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if strings.TrimSpace(orderNo) == "" {
		return false, domain.ErrInvalidAllocationRequest
	}
	var result struct {
		ID    uint
		Ready bool
	}
	query := r.dbFor(ctx).Table("daum_allocations AS allocation").Select(`allocation.id, (allocation.status = 'allocated' AND EXISTS (
 SELECT 1 FROM daum_resources dr JOIN email_resources er ON er.id = dr.id AND er.type = 'daum' AND er.owner_user_id = dr.owner_user_id
 WHERE dr.id = allocation.resource_id AND dr.resource_type = 'daum' AND dr.status = 'normal' AND dr.app_password <> ''
 AND ((allocation.mailbox = 'main' AND dr.email_address = allocation.email)
 OR (allocation.mailbox = 'alias' AND EXISTS (SELECT 1 FROM daum_explicit_aliases alias WHERE alias.id = allocation.explicit_alias_id AND alias.resource_id = dr.id AND alias.email_address = allocation.email AND alias.status = 'normal'))))) AS ready`).
		Where("allocation.order_no = ? AND allocation.guard_type = 'daum'", strings.TrimSpace(orderNo))
	if allocationID != 0 {
		query = query.Where("allocation.id = ?", allocationID)
	}
	if err := query.Limit(1).Scan(&result).Error; err != nil {
		return false, fmt.Errorf("check Daum allocation readiness: %w", err)
	}
	return (result.ID == 0 && allocationID == 0) || result.Ready, nil
}

func (r *Repo) CreateDaumAllocation(ctx context.Context, allocation *domain.DaumAllocation) error {
	if allocation == nil || allocation.ProjectID == 0 || allocation.ProductID == 0 || allocation.ResourceID == 0 || allocation.OwnerUserID == 0 || strings.TrimSpace(allocation.OrderNo) == "" || strings.TrimSpace(allocation.Email) == "" ||
		(allocation.Mailbox != "main" && allocation.Mailbox != "alias") || (allocation.Mailbox == "alias") != (allocation.ExplicitAliasID != nil) ||
		!domain.IsValidServiceMode(domain.ServiceMode(allocation.ServiceMode)) || !domain.IsValidSupplyScope(allocation.SupplyScope) {
		return domain.ErrInvalidAllocationRequest
	}
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	model := daumAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create Daum allocation: %w", err)
	}
	// The shared snapshot assigns explicit alias income to the platform.
	if err := r.snapshotAllocationSupplier(ctx, allocation.OrderNo, allocation.ResourceID, allocation.Mailbox == "alias"); err != nil {
		return err
	}
	*allocation = model.toDomain()
	return nil
}

func (r *Repo) TouchDaumAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error {
	return r.dbFor(ctx).Table("daum_resources").Where("id = ? AND resource_type = 'daum'", resourceID).Update("last_allocated_at", allocatedAt).Error
}

func (r *Repo) LockDaumHistoricalResource(ctx context.Context, resourceID uint, email string) (*allocapp.DaumCandidate, error) {
	var row allocapp.DaumCandidate
	err := daumAddressQuery(r.dbFor(ctx)).Joins("JOIN email_resources er ON er.id = dr.id AND er.type = 'daum' AND er.owner_user_id = dr.owner_user_id").
		Select(daumCandidateColumns).Where("dr.id = ? AND "+daumAddressEmailSQL+" = ? AND dr.status IN ('identifying','normal')", resourceID, email).Take(&row).Error
	if err != nil {
		return nil, fmt.Errorf("load Daum historical address: %w", err)
	}
	return &row, nil
}

func (r *Repo) HasDaumProjectHistory(ctx context.Context, resourceID, projectID uint, email string) (bool, error) {
	var count int64
	err := r.dbFor(ctx).Model(&DaumAllocationModel{}).Where("resource_id = ? AND project_id = ? AND email = ? AND guard_type = 'daum'", resourceID, projectID, email).Count(&count).Error
	return count > 0, err
}

func (r *Repo) ListPrivateDaumInventoryTotals(ctx context.Context, projectID, buyerUserID uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	var rows []allocapp.PrivateProductInventoryTotal
	err := daumSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, domain.SupplyScopeOwned).
		Joins("JOIN project_products pp ON pp.project_id = ? AND pp.type = 'daum' AND pp.status = 'enabled'", projectID).
		Joins("JOIN projects p ON p.id = pp.project_id AND p.status = 'listed'").
		Select("pp.id AS product_id, suffix.suffix AS suffix, COUNT(*) AS available").Group("pp.id, suffix.suffix").Order("pp.id, suffix.suffix").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list private Daum inventory totals: %w", err)
	}
	return rows, nil
}

func (r *Repo) daumSuffixInventory(ctx context.Context, projectID, buyerUserID uint, scope domain.SupplyScope) (map[string]int64, error) {
	var rows []struct {
		Suffix string
		Count  int64
	}
	if err := daumSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope).Select("suffix.suffix AS suffix, COUNT(*) AS count").Group("suffix.suffix").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Daum suffix inventory: %w", err)
	}
	result := map[string]int64{"daum.net": 0, "hanmail.net": 0, "hanmail2.net": 0}
	for _, row := range rows {
		result[row.Suffix] += row.Count
	}
	return result, nil
}
