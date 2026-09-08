package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProtoAllocationModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo            string     `gorm:"type:varchar(64);not null;column:order_no"`
	GuardType          string     `gorm:"type:varchar(32);not null;column:guard_type"`
	ProjectID          uint       `gorm:"not null;column:project_id"`
	ProductID          uint       `gorm:"not null;column:product_id"`
	ResourceID         uint       `gorm:"not null;column:resource_id"`
	OwnerUserID        uint       `gorm:"not null;column:owner_user_id"`
	SupplyScope        string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	Mailbox            string     `gorm:"type:varchar(16);not null;column:mailbox"`
	ServiceMode        string     `gorm:"type:varchar(32);not null;column:service_mode"`
	Email              string     `gorm:"type:varchar(320);not null;column:email"`
	Status             string     `gorm:"type:varchar(32);not null;column:status"`
	CostPointsSnapshot string     `gorm:"type:decimal(18,6);not null;column:cost_points_snapshot"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt         *time.Time `gorm:"column:released_at"`
}

func (ProtoAllocationModel) TableName() string { return "proto_allocations" }

func protoAllocationFromDomain(allocation *domain.ProtoAllocation) *ProtoAllocationModel {
	return &ProtoAllocationModel{
		ID: allocation.ID, OrderNo: allocation.OrderNo, GuardType: string(domain.AllocationTypeProto),
		ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
		OwnerUserID: allocation.OwnerUserID,
		SupplyScope: string(domain.NormalizeSupplyScope(allocation.SupplyScope)), Mailbox: "main",
		ServiceMode: allocation.ServiceMode, Email: strings.ToLower(strings.TrimSpace(allocation.Email)),
		Status: string(allocation.Status), CostPointsSnapshot: allocation.CostPointsSnapshot,
		CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
	}
}

func (m ProtoAllocationModel) toDomain() domain.ProtoAllocation {
	return domain.ProtoAllocation{
		ID: m.ID, OrderNo: m.OrderNo, ProjectID: m.ProjectID, ProductID: m.ProductID,
		ResourceID: m.ResourceID, SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		OwnerUserID: m.OwnerUserID,
		Mailbox:     m.Mailbox, ServiceMode: m.ServiceMode, Email: m.Email,
		Status: domain.AllocationStatus(m.Status), CostPointsSnapshot: m.CostPointsSnapshot,
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

func (m ProtoAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type: domain.AllocationTypeProto, ID: m.ID, OrderNo: m.OrderNo,
		ProjectID: m.ProjectID, ProductID: m.ProductID, ResourceID: m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     m.Mailbox, Email: m.Email, Status: domain.AllocationStatus(m.Status),
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

func (r *Repo) ProtoAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if strings.TrimSpace(orderNo) == "" {
		return false, domain.ErrInvalidAllocationRequest
	}
	var result struct {
		ID    uint
		Ready bool
	}
	query := r.dbFor(ctx).Table("proto_allocations AS pa").Select(`pa.id,
        (pa.status = 'allocated' AND EXISTS (
            SELECT 1 FROM proto_resources AS pr
            JOIN email_resources AS er ON er.id = pr.id AND er.type = 'proto' AND er.owner_user_id = pr.owner_user_id
            JOIN proto_sessions AS session ON session.resource_id = pr.id AND session.credential_revision = pr.credential_revision
            WHERE pr.id = pa.resource_id AND pr.resource_type = 'proto' AND pr.status = 'normal' AND pr.email_address = pa.email
        )) AS ready`).Where("pa.order_no = ? AND pa.guard_type = 'proto'", strings.TrimSpace(orderNo))
	if allocationID != 0 {
		query = query.Where("pa.id = ?", allocationID)
	}
	if err := query.Limit(1).Scan(&result).Error; err != nil {
		return false, fmt.Errorf("check Proto allocation readiness: %w", err)
	}
	return (result.ID == 0 && allocationID == 0) || result.Ready, nil
}

func (r *Repo) ListProtoSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int) ([]allocapp.ProtoCandidate, error) {
	if projectID == 0 || buyerUserID == 0 || limit <= 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	query := protoSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope)
	if bucket != nil {
		query = query.Where("pr.alloc_bucket = ?", *bucket)
	}
	var rows []allocapp.ProtoCandidate
	if err := query.Select("pr.id AS resource_id, pr.owner_user_id AS owner_user_id, pr.email_address AS email, pr.last_allocated_at AS last_allocated_at").
		Order("pr.last_allocated_at ASC, pr.quality_score DESC, pr.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Proto allocation candidates: %w", err)
	}
	return rows, nil
}

func protoSourceCandidateQuery(db *gorm.DB, projectID uint, buyerUserID uint, scope domain.SupplyScope) *gorm.DB {
	query := db.Table("proto_resources AS pr").
		Where("pr.resource_type = ? AND pr.status = ?", string(domain.AllocationTypeProto), "normal").
		Where("EXISTS (SELECT 1 FROM proto_sessions session WHERE session.resource_id = pr.id AND session.credential_revision = pr.credential_revision)").
		Where("NOT EXISTS (SELECT 1 FROM proto_allocations history WHERE history.resource_id = pr.id AND history.project_id = ?)", projectID)
	// Like Microsoft, keep owner checks outside the candidate's locking query.
	owner := db.Table("email_resources AS er").Select("1").
		Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("er.id = pr.id AND er.type = ? AND er.owner_user_id = pr.owner_user_id", string(domain.AllocationTypeProto))
	if scope == domain.SupplyScopeOwned {
		query = query.Where("pr.for_sale = FALSE")
		owner = owner.Where("er.owner_user_id = ?", buyerUserID)
	} else {
		query = query.Where("pr.for_sale = TRUE")
		owner = owner.Where("owner.status = 'active' AND owner.role IN ('supplier', 'admin', 'super_admin')")
	}
	return query.Where("EXISTS (?)", owner)
}

func (r *Repo) LockProtoCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope) (*allocapp.ProtoCandidate, error) {
	if resourceID == 0 || projectID == 0 || buyerUserID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	query := protoSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope).
		Where("pr.id = ?", resourceID).
		Select("pr.id AS resource_id, pr.owner_user_id AS owner_user_id, pr.email_address AS email, pr.last_allocated_at AS last_allocated_at").Limit(1)
	if r.dbFor(ctx).Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
	var row allocapp.ProtoCandidate
	if err := query.Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock Proto allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) CreateProtoAllocation(ctx context.Context, allocation *domain.ProtoAllocation) error {
	if allocation == nil || allocation.ProjectID == 0 || allocation.ProductID == 0 || allocation.ResourceID == 0 ||
		allocation.OwnerUserID == 0 || strings.TrimSpace(allocation.OrderNo) == "" || strings.TrimSpace(allocation.Email) == "" || allocation.Mailbox != "" && allocation.Mailbox != "main" ||
		!domain.IsValidServiceMode(domain.ServiceMode(allocation.ServiceMode)) || !domain.IsValidSupplyScope(allocation.SupplyScope) {
		return domain.ErrInvalidAllocationRequest
	}
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	if allocation.Mailbox == "" {
		allocation.Mailbox = "main"
	}
	model := protoAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create Proto allocation: %w", err)
	}
	*allocation = model.toDomain()
	return nil
}

func (r *Repo) TouchProtoAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error {
	if err := r.dbFor(ctx).Table("proto_resources").Where("id = ? AND resource_type = ?", resourceID, string(domain.AllocationTypeProto)).
		Update("last_allocated_at", allocatedAt).Error; err != nil {
		return fmt.Errorf("touch Proto resource allocated: %w", err)
	}
	return nil
}

func (r *Repo) LockProtoHistoricalResource(ctx context.Context, resourceID uint) (*allocapp.ProtoCandidate, error) {
	var resource allocapp.ProtoCandidate
	err := r.dbFor(ctx).Table("proto_resources pr").
		Joins("JOIN email_resources er ON er.id = pr.id AND er.type = 'proto'").
		Select("pr.id AS resource_id, er.owner_user_id, pr.email_address AS email").
		Where("pr.id = ? AND pr.resource_type = 'proto' AND pr.status IN ('identifying', 'normal')", resourceID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource).Error
	if err != nil {
		return nil, fmt.Errorf("lock Proto historical resource: %w", err)
	}
	return &resource, nil
}

func (r *Repo) HasProtoProjectHistory(ctx context.Context, resourceID, projectID uint) (bool, error) {
	var count int64
	err := r.dbFor(ctx).Model(&ProtoAllocationModel{}).
		Where("resource_id = ? AND project_id = ? AND guard_type = 'proto'", resourceID, projectID).Count(&count).Error
	return count > 0, err
}

func (r *Repo) ListPrivateProtoInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]allocapp.PrivateSingletonInventoryTotal, error) {
	var rows []allocapp.PrivateSingletonInventoryTotal
	if err := r.dbFor(ctx).Raw(`
SELECT pp.id AS product_id, pp.type AS product_type, COUNT(pr.id) AS available
FROM project_products pp
JOIN projects p ON p.id = pp.project_id AND p.status = 'listed'
JOIN proto_resources pr ON pr.status = 'normal' AND pr.resource_type = 'proto'
JOIN email_resources er ON er.id = pr.id AND er.type = 'proto'
WHERE pp.project_id = ?
  AND pp.type = 'proto'
  AND pp.status = 'enabled'
  AND er.owner_user_id = ?
  AND pr.owner_user_id = er.owner_user_id
  AND pr.for_sale = FALSE
  AND EXISTS (
      SELECT 1 FROM proto_sessions session
      WHERE session.resource_id = pr.id AND session.credential_revision = pr.credential_revision
  )
  AND NOT EXISTS (
      SELECT 1 FROM proto_allocations history
      WHERE history.resource_id = pr.id AND history.project_id = pp.project_id
  )
GROUP BY pp.id, pp.type
ORDER BY pp.id`, projectID, buyerUserID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list private Proto inventory totals: %w", err)
	}
	return rows, nil
}
