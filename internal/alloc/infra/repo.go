package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	gmailLocalSource                              = "local"
	microsoftNotUnderBlockingMaintenanceCondition = `ms.token_refresh_status NOT IN ('pending', 'processing')
			AND NOT EXISTS (
				SELECT 1
				FROM mailmatch_resource_fetch_states maintenance_fetch
				WHERE maintenance_fetch.email_resource_id = ms.id
				  AND maintenance_fetch.operation_kind = 'resource_history'
				  AND maintenance_fetch.status IN ('pending', 'processing')
			)`
	microsoftProjectUnmatchedCondition = `NOT EXISTS (
			SELECT 1 FROM microsoft_resource_project_matches legacy_match
			WHERE legacy_match.resource_id = ms.id AND legacy_match.project_id = ?
		)`
	microsoftUnusedMainCondition = `(
				NOT EXISTS (
					SELECT 1
					FROM microsoft_allocations history_main
					WHERE history_main.resource_id = ms.id
					  AND history_main.project_id = ?
					  AND history_main.mailbox = 'main'
				)
				AND NOT EXISTS (
					SELECT 1
					FROM microsoft_allocations active_main
					WHERE active_main.active_kind = 1
					  AND active_main.project_id = ?
					  AND active_main.active_entity_id = ms.id
				)
			)`
	microsoftReusableExplicitAliasConditionTemplate = `EXISTS (
				SELECT 1
				FROM explicit_aliases ea
				WHERE ea.resource_id = ms.id
				  AND ea.status = 'normal'%s
				  AND NOT EXISTS (
					SELECT 1 FROM microsoft_allocations history_alias
					WHERE history_alias.explicit_alias_id = ea.id
					  AND history_alias.project_id = ?
					  AND history_alias.mailbox = 'alias'
				  )
				  AND NOT EXISTS (
					SELECT 1 FROM microsoft_allocations ma
					WHERE ma.active_kind = 2
					  AND ma.project_id = ?
					  AND ma.active_entity_id = ea.id
				  )
			)`
)

func reusableExplicitAliasCondition(projectID uint, emailSuffix string) (string, []any) {
	filter := ""
	args := make([]any, 0, 2)
	if suffix := normalizeCandidateSuffix(emailSuffix); suffix != "" {
		filter = " AND ea.email_domain = ?"
		args = append(args, suffix)
	}
	args = append(args, projectID, projectID)
	return fmt.Sprintf(microsoftReusableExplicitAliasConditionTemplate, filter), args
}

func microsoftMainCandidateCondition(projectID uint, emailSuffix string) (string, []any) {
	suffix := normalizeCandidateSuffix(emailSuffix)
	aliasCondition, aliasArgs := reusableExplicitAliasCondition(projectID, suffix)
	if suffix == "" {
		return "(" + microsoftUnusedMainCondition + " OR " + aliasCondition + ")",
			append([]any{projectID, projectID}, aliasArgs...)
	}
	return "((ms.email_domain = ? AND " + microsoftUnusedMainCondition + ") OR " + aliasCondition + ")",
		append([]any{suffix, projectID, projectID}, aliasArgs...)
}

func microsoftProjectSuffixAllowedCondition(projectID uint, domainColumn string) (string, []any) {
	return `NOT EXISTS (
        SELECT 1
        FROM project_microsoft_suffix_blacklists blocked
        WHERE blocked.project_id = ?
          AND blocked.suffix = ` + microsoftCanonicalDomainExpression(domainColumn) + `
    )`, []any{projectID}
}

func microsoftCanonicalDomainExpression(domainColumn string) string {
	return "LOWER(TRIM(TRAILING '.' FROM " + domainColumn + "))"
}

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		// Candidate rechecks must see commits made while waiting for a resource
		// root; READ COMMITTED also avoids RR gap locks on missing history rows.
		txOptions := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
		if r.db.Name() != "mysql" {
			txOptions = nil
		}
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(platform.WithGormTx(ctx, tx))
		}, txOptions)
		if err == nil || !isDeadlockError(err) {
			return err
		}
		platform.RecordMySQLTransactionEvent("alloc", mysqlRetryEvent(err))
		if !isWholeTransactionRollbackError(err) {
			return err
		}
		if attempt == 1 {
			platform.RecordMySQLTransactionEvent("alloc", "retry_exhausted")
			return err
		}
		platform.RecordMySQLTransactionEvent("alloc", "retry")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(deadlockBackoff(attempt)):
		}
	}
	return err
}

func (r *Repo) HasParentTx(ctx context.Context) bool {
	_, ok := platform.GormTxFromContext(ctx)
	return ok
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

type OrderGuardModel struct {
	OrderNo   string    `gorm:"primaryKey;column:order_no"`
	Type      string    `gorm:"type:varchar(32);not null"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (OrderGuardModel) TableName() string { return "allocation_order_guards" }

type MicrosoftAllocationModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo         string     `gorm:"type:varchar(64);not null;column:order_no"`
	ProjectID       uint       `gorm:"not null;column:project_id"`
	ProductID       uint       `gorm:"not null;column:product_id"`
	ResourceID      uint       `gorm:"not null;column:resource_id"`
	SupplyScope     string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	Mailbox         string     `gorm:"type:varchar(32);not null"`
	ExplicitAliasID *uint      `gorm:"column:explicit_alias_id"`
	DotAliasID      *uint      `gorm:"column:dot_alias_id"`
	PlusAliasID     *uint      `gorm:"column:plus_alias_id"`
	Email           string     `gorm:"type:varchar(255);not null"`
	Status          string     `gorm:"type:varchar(32);not null;default:'allocated'"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt      *time.Time `gorm:"column:released_at"`
}

func (MicrosoftAllocationModel) TableName() string { return "microsoft_allocations" }

func microsoftAllocationFromDomain(allocation *domain.MicrosoftAllocation) *MicrosoftAllocationModel {
	return &MicrosoftAllocationModel{
		ID:              allocation.ID,
		OrderNo:         allocation.OrderNo,
		ProjectID:       allocation.ProjectID,
		ProductID:       allocation.ProductID,
		ResourceID:      allocation.ResourceID,
		SupplyScope:     string(domain.NormalizeSupplyScope(allocation.SupplyScope)),
		Mailbox:         string(allocation.Mailbox),
		ExplicitAliasID: allocation.ExplicitAliasID,
		DotAliasID:      allocation.DotAliasID,
		PlusAliasID:     allocation.PlusAliasID,
		Email:           strings.ToLower(strings.TrimSpace(allocation.Email)),
		Status:          string(allocation.Status),
		CreatedAt:       allocation.CreatedAt,
		ReleasedAt:      allocation.ReleasedAt,
	}
}

func (m MicrosoftAllocationModel) toDomain() domain.MicrosoftAllocation {
	return domain.MicrosoftAllocation{
		ID:              m.ID,
		OrderNo:         m.OrderNo,
		ProjectID:       m.ProjectID,
		ProductID:       m.ProductID,
		ResourceID:      m.ResourceID,
		SupplyScope:     domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:         domain.MicrosoftMailbox(m.Mailbox),
		ExplicitAliasID: m.ExplicitAliasID,
		DotAliasID:      m.DotAliasID,
		PlusAliasID:     m.PlusAliasID,
		Email:           m.Email,
		Status:          domain.AllocationStatus(m.Status),
		CreatedAt:       m.CreatedAt,
		ReleasedAt:      m.ReleasedAt,
	}
}

func (m MicrosoftAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type:        domain.AllocationTypeMicrosoft,
		ID:          m.ID,
		OrderNo:     m.OrderNo,
		ProjectID:   m.ProjectID,
		ProductID:   m.ProductID,
		ResourceID:  m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     m.Mailbox,
		Email:       m.Email,
		Status:      domain.AllocationStatus(m.Status),
		CreatedAt:   m.CreatedAt,
		ReleasedAt:  m.ReleasedAt,
	}
}

type GmailAllocationModel struct {
	ID                 uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo            string     `gorm:"type:varchar(64);not null;column:order_no"`
	GuardType          string     `gorm:"type:varchar(32);not null;column:guard_type"`
	ProjectID          uint       `gorm:"column:project_id"`
	ProductID          uint       `gorm:"column:product_id"`
	Source             string     `gorm:"type:varchar(64);not null;column:source"`
	SourceRef          string     `gorm:"type:varchar(128);not null;column:source_ref"`
	ProviderCursor     uint64     `gorm:"not null;column:provider_cursor"`
	ProviderSpamCursor uint64     `gorm:"not null;column:provider_spam_cursor"`
	ServiceMode        string     `gorm:"type:varchar(32);not null;column:service_mode"`
	ResourceID         uint       `gorm:"column:resource_id"`
	SupplyScope        string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	Mailbox            string     `gorm:"type:varchar(16);not null;column:mailbox"`
	Email              string     `gorm:"type:varchar(320);not null;column:email"`
	Status             string     `gorm:"type:varchar(32);not null;column:status"`
	CostPointsSnapshot string     `gorm:"type:decimal(18,6);not null;column:cost_points_snapshot"`
	CreatedAt          time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt         *time.Time `gorm:"column:released_at"`
}

func (GmailAllocationModel) TableName() string { return "gmail_allocations" }

func gmailAllocationFromDomain(allocation *domain.GmailAllocation) *GmailAllocationModel {
	return &GmailAllocationModel{
		ID: allocation.ID, OrderNo: allocation.OrderNo, GuardType: string(domain.AllocationTypeGmail),
		ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, Source: gmailLocalSource,
		ServiceMode: string(allocation.ServiceMode), ResourceID: allocation.ResourceID,
		SupplyScope: string(domain.NormalizeSupplyScope(allocation.SupplyScope)), Mailbox: string(allocation.Mailbox),
		Email: strings.ToLower(strings.TrimSpace(allocation.Email)), Status: string(allocation.Status),
		CostPointsSnapshot: allocation.CostPointsSnapshot, CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
	}
}

func (m GmailAllocationModel) toDomain() domain.GmailAllocation {
	return domain.GmailAllocation{
		ID: m.ID, OrderNo: m.OrderNo, ProjectID: m.ProjectID, ProductID: m.ProductID, ResourceID: m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)), Mailbox: domain.GmailMailbox(m.Mailbox),
		ServiceMode: domain.GmailServiceMode(m.ServiceMode), Email: m.Email, Status: domain.AllocationStatus(m.Status),
		CostPointsSnapshot: m.CostPointsSnapshot, CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

func (m GmailAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type: domain.AllocationTypeGmail, ID: m.ID, OrderNo: m.OrderNo,
		ProjectID: m.ProjectID, ProductID: m.ProductID, ResourceID: m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     m.Mailbox, Email: m.Email, Status: domain.AllocationStatus(m.Status),
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

type ICloudAllocationModel struct {
	ID          uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo     string     `gorm:"type:varchar(64);not null;column:order_no"`
	ProjectID   uint       `gorm:"not null;column:project_id"`
	ProductID   uint       `gorm:"not null;column:product_id"`
	ResourceID  uint       `gorm:"not null;column:resource_id"`
	AliasID     uint       `gorm:"not null;column:alias_id"`
	SupplyScope string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	Email       string     `gorm:"type:varchar(320);not null"`
	Status      string     `gorm:"type:varchar(32);not null;default:'allocated'"`
	CreatedAt   time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt  *time.Time `gorm:"column:released_at"`
}

func (ICloudAllocationModel) TableName() string { return "icloud_allocations" }

func icloudAllocationFromDomain(allocation *domain.ICloudAllocation) *ICloudAllocationModel {
	return &ICloudAllocationModel{
		ID: allocation.ID, OrderNo: allocation.OrderNo, ProjectID: allocation.ProjectID,
		ProductID: allocation.ProductID, ResourceID: allocation.ResourceID, AliasID: allocation.AliasID,
		SupplyScope: string(domain.NormalizeSupplyScope(allocation.SupplyScope)),
		Email:       strings.ToLower(strings.TrimSpace(allocation.Email)), Status: string(allocation.Status),
		CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
	}
}

func (m ICloudAllocationModel) toDomain() domain.ICloudAllocation {
	return domain.ICloudAllocation{
		ID: m.ID, OrderNo: m.OrderNo, ProjectID: m.ProjectID, ProductID: m.ProductID,
		ResourceID: m.ResourceID, AliasID: m.AliasID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Email:       m.Email, Status: domain.AllocationStatus(m.Status), CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

func (m ICloudAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type: domain.AllocationTypeICloud, ID: m.ID, OrderNo: m.OrderNo,
		ProjectID: m.ProjectID, ProductID: m.ProductID, ResourceID: m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     "alias", Email: m.Email, Status: domain.AllocationStatus(m.Status),
		CreatedAt: m.CreatedAt, ReleasedAt: m.ReleasedAt,
	}
}

type DomainAllocationModel struct {
	ID          uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo     string     `gorm:"type:varchar(64);not null;column:order_no"`
	ProjectID   uint       `gorm:"not null;column:project_id"`
	ProductID   uint       `gorm:"not null;column:product_id"`
	ResourceID  uint       `gorm:"not null;column:resource_id"`
	SupplyScope string     `gorm:"type:varchar(16);not null;column:supply_scope"`
	MailboxID   uint       `gorm:"not null;column:mailbox_id"`
	Email       string     `gorm:"type:varchar(255);not null"`
	Status      string     `gorm:"type:varchar(32);not null;default:'allocated'"`
	CreatedAt   time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt  *time.Time `gorm:"column:released_at"`
}

func (DomainAllocationModel) TableName() string { return "domain_allocations" }

func domainAllocationFromDomain(allocation *domain.GeneratedMailboxAllocation) *DomainAllocationModel {
	return &DomainAllocationModel{
		ID:          allocation.ID,
		OrderNo:     allocation.OrderNo,
		ProjectID:   allocation.ProjectID,
		ProductID:   allocation.ProductID,
		ResourceID:  allocation.ResourceID,
		SupplyScope: string(domain.NormalizeSupplyScope(allocation.SupplyScope)),
		MailboxID:   allocation.MailboxID,
		Email:       strings.ToLower(strings.TrimSpace(allocation.Email)),
		Status:      string(allocation.Status),
		CreatedAt:   allocation.CreatedAt,
		ReleasedAt:  allocation.ReleasedAt,
	}
}

func (m DomainAllocationModel) toDomain() domain.GeneratedMailboxAllocation {
	return domain.GeneratedMailboxAllocation{
		ID:          m.ID,
		OrderNo:     m.OrderNo,
		ProjectID:   m.ProjectID,
		ProductID:   m.ProductID,
		ResourceID:  m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		MailboxID:   m.MailboxID,
		Email:       m.Email,
		Status:      domain.AllocationStatus(m.Status),
		CreatedAt:   m.CreatedAt,
		ReleasedAt:  m.ReleasedAt,
	}
}

func (m DomainAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type:        domain.AllocationTypeDomain,
		ID:          m.ID,
		OrderNo:     m.OrderNo,
		ProjectID:   m.ProjectID,
		ProductID:   m.ProductID,
		ResourceID:  m.ResourceID,
		SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(m.SupplyScope)),
		Mailbox:     "domain",
		Email:       m.Email,
		Status:      domain.AllocationStatus(m.Status),
		CreatedAt:   m.CreatedAt,
		ReleasedAt:  m.ReleasedAt,
	}
}

type DotAliasModel struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	ResourceID uint      `gorm:"column:resource_id"`
	Email      string    `gorm:"type:varchar(255);not null"`
	Status     string    `gorm:"type:varchar(32);not null"`
	CreatedAt  time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (DotAliasModel) TableName() string { return "dot_aliases" }

type PlusAliasModel struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	ResourceID uint      `gorm:"column:resource_id"`
	Email      string    `gorm:"type:varchar(255);not null"`
	Status     string    `gorm:"type:varchar(32);not null"`
	CreatedAt  time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (PlusAliasModel) TableName() string { return "plus_aliases" }

type GeneratedMailboxModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID      uint       `gorm:"column:resource_id"`
	OwnerUserID     uint       `gorm:"column:owner_user_id"`
	Email           string     `gorm:"type:varchar(255);not null"`
	Status          string     `gorm:"type:varchar(32);not null"`
	AllocBucket     uint16     `gorm:"column:alloc_bucket"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
}

func (GeneratedMailboxModel) TableName() string { return "generated_mailboxes" }

type DailyUsageModel struct {
	UsageDate    time.Time `gorm:"primaryKey;column:usage_date"`
	ResourceType string    `gorm:"primaryKey;type:varchar(32);column:resource_type"`
	ResourceID   uint      `gorm:"primaryKey;column:resource_id"`
	UsageKind    string    `gorm:"primaryKey;type:varchar(32);column:usage_kind"`
	UsedCount    int       `gorm:"not null;default:0;column:used_count"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt    time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (DailyUsageModel) TableName() string { return "allocation_daily_usages" }

func (r *Repo) FindExistingAllocation(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	var guard OrderGuardModel
	if err := r.dbFor(ctx).Where("order_no = ?", orderNo).First(&guard).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find allocation guard: %w", err)
	}
	return r.findByGuard(ctx, guard)
}

func (r *Repo) CreateOrderGuard(ctx context.Context, orderNo string, allocationType domain.AllocationType) error {
	model := OrderGuardModel{OrderNo: orderNo, Type: string(allocationType)}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		return fmt.Errorf("create allocation guard: %w", err)
	}
	return nil
}

func (r *Repo) LoadProductConfig(ctx context.Context, productID uint, buyerUserID uint, fulfillExistingOrder bool) (*allocapp.ProductAllocationConfig, error) {
	type row struct {
		ProjectID             uint
		ProductID             uint
		Type                  string
		CodeEnabled           bool
		PurchaseEnabled       bool
		CodeSupplierPrice     string
		PurchaseSupplierPrice string
		MainWeight            int
		DotWeight             int
		PlusWeight            int
	}
	var item row
	err := r.dbFor(ctx).Raw(`
SELECT
    pp.project_id AS project_id,
    pp.id AS product_id,
    pp.type AS type,
	pp.code_enabled AS code_enabled,
	pp.purchase_enabled AS purchase_enabled,
	pp.code_supplier_price AS code_supplier_price,
	pp.purchase_supplier_price AS purchase_supplier_price,
    pp.main_weight AS main_weight,
    pp.dot_weight AS dot_weight,
    pp.plus_weight AS plus_weight
FROM project_products pp
JOIN projects p ON p.id = pp.project_id
WHERE pp.id = ?
  AND (? = TRUE OR pp.status = 'enabled')
  AND p.status = 'listed'
  AND (
      p.access_type = 'public'
      OR EXISTS (
          SELECT 1 FROM project_accesses pa
          WHERE pa.project_id = p.id AND pa.user_id = ?
      )
  )
LIMIT 1`, productID, fulfillExistingOrder, buyerUserID).Scan(&item).Error
	if err != nil {
		return nil, fmt.Errorf("load product config: %w", err)
	}
	if item.ProductID == 0 {
		return nil, nil
	}
	productType := coredomain.ProductType(item.Type)
	if !coredomain.IsValidProductType(productType) {
		return nil, domain.ErrProjectNotAllocatable
	}
	return &allocapp.ProductAllocationConfig{
		ProjectID:             item.ProjectID,
		ProductID:             item.ProductID,
		ProductType:           productType,
		CodeEnabled:           item.CodeEnabled,
		PurchaseEnabled:       item.PurchaseEnabled,
		CodeSupplierPrice:     item.CodeSupplierPrice,
		PurchaseSupplierPrice: item.PurchaseSupplierPrice,
		MainWeight:            item.MainWeight,
		DotWeight:             item.DotWeight,
		PlusWeight:            item.PlusWeight,
	}, nil
}

func (r *Repo) ListGmailSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.GmailMailbox, after *allocapp.GmailCandidate, limit int) ([]allocapp.GmailCandidate, error) {
	if projectID == 0 || buyerUserID == 0 || limit <= 0 || !domain.IsValidGmailMailbox(mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	query := gmailSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope, mailbox)
	if after != nil {
		if after.LastAllocatedAt == nil {
			query = query.Where("(gr.last_allocated_at IS NULL AND gr.id > ?) OR gr.last_allocated_at IS NOT NULL", after.ResourceID)
		} else {
			query = query.Where("gr.last_allocated_at IS NOT NULL AND (gr.last_allocated_at > ? OR (gr.last_allocated_at = ? AND gr.id > ?))", after.LastAllocatedAt, after.LastAllocatedAt, after.ResourceID)
		}
	}
	var rows []allocapp.GmailCandidate
	if err := query.Select("gr.id AS resource_id, gr.email AS email, gr.last_allocated_at AS last_allocated_at").
		Order("gr.last_allocated_at ASC, gr.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Gmail allocation candidates: %w", err)
	}
	return rows, nil
}

func gmailSourceCandidateQuery(db *gorm.DB, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.GmailMailbox) *gorm.DB {
	query := db.Table("gmail_resources AS gr").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", string(domain.AllocationTypeGmail)).
		Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("gr.status IN (?, ?)", "normal", "available")
	switch mailbox {
	case domain.GmailMailboxMain:
		query = query.
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations active WHERE active.source = ? AND active.resource_id = gr.id AND active.project_id = ? AND active.mailbox = ? AND active.status = ?)", gmailLocalSource, projectID, domain.GmailMailboxMain, domain.AllocationStatusAllocated).
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations history WHERE history.source = ? AND history.resource_id = gr.id AND history.project_id = ? AND history.mailbox = ?)", gmailLocalSource, projectID, domain.GmailMailboxMain)
	case domain.GmailMailboxDot:
		query = query.Where(`
(SELECT COUNT(*)
 FROM gmail_allocations history
 WHERE history.source = ?
   AND history.resource_id = gr.id
   AND history.project_id = ?
   AND history.mailbox = ?) < `+gmailDotCandidateCapacityExpression(db, "gr"),
			gmailLocalSource, projectID, domain.GmailMailboxDot)
	}
	if scope == domain.SupplyScopeOwned {
		return query.Where("gr.for_sale = FALSE AND er.owner_user_id = ?", buyerUserID)
	}
	return query.Where("gr.for_sale = TRUE AND owner.status = 'active' AND owner.role IN ('supplier', 'admin', 'super_admin')")
}

func (r *Repo) ListMicrosoftSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, bucket *uint16, limit int, emailSuffix string) ([]allocapp.MicrosoftCandidate, error) {
	suffix := normalizeCandidateSuffix(emailSuffix)
	if mailbox == domain.MicrosoftMailboxMain {
		type candidateRow struct {
			ResourceID      uint       `gorm:"column:resource_id"`
			EmailAddress    string     `gorm:"column:email_address"`
			QualityScore    int        `gorm:"column:quality_score"`
			LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
		}
		base := func() ([]string, []any) {
			where := []string{
				"ms.status = 'normal'",
				microsoftNotUnderBlockingMaintenanceCondition,
				microsoftProjectUnmatchedCondition,
			}
			args := []any{projectID}
			switch scope {
			case domain.SupplyScopeOwned:
				where = append(where, "ms.for_sale = FALSE", "er.owner_user_id = ?")
				args = append(args, buyerUserID)
			default:
				where = append(where, "ms.for_sale = TRUE", "u.status = 'active'", "u.role IN ('supplier', 'admin', 'super_admin')")
			}
			return where, args
		}
		list := func(from string, distinct bool, where []string, args []any) ([]candidateRow, error) {
			selectSQL := "SELECT "
			if distinct {
				selectSQL += "DISTINCT "
			}
			query := selectSQL + `ms.id AS resource_id,
       ms.email_address AS email_address,
       ms.quality_score AS quality_score,
       ms.last_allocated_at AS last_allocated_at
FROM ` + from + `
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ms.last_allocated_at ASC, ms.quality_score DESC, ms.id ASC
LIMIT ?`
			var rows []candidateRow
			if err := r.dbFor(ctx).Raw(query, append(args, limit)...).Scan(&rows).Error; err != nil {
				return nil, err
			}
			return rows, nil
		}

		mainWhere, mainArgs := base()
		if suffix != "" {
			mainWhere = append(mainWhere, "ms.email_domain = ?")
			mainArgs = append(mainArgs, suffix)
		}
		mainWhere = append(mainWhere, microsoftUnusedMainCondition)
		mainArgs = append(mainArgs, projectID, projectID)
		if bucket != nil {
			mainWhere = append(mainWhere, "ms.alloc_bucket = ?")
			mainArgs = append(mainArgs, *bucket)
		}
		mainFrom := "microsoft_resources ms"
		if suffix != "" && bucket != nil {
			mainFrom += " FORCE INDEX (idx_microsoft_suffix_bucket)"
		}
		rows, err := list(mainFrom, false, mainWhere, mainArgs)
		if err != nil {
			return nil, fmt.Errorf("list microsoft main source allocation candidates: %w", err)
		}

		aliasWhere, aliasArgs := base()
		aliasWhere = append(aliasWhere, "ea.status = 'normal'")
		if suffix != "" {
			aliasWhere = append(aliasWhere, "ea.email_domain = ?")
			aliasArgs = append(aliasArgs, suffix)
		}
		aliasWhere = append(aliasWhere, `NOT EXISTS (
            SELECT 1 FROM microsoft_allocations history_alias
            WHERE history_alias.explicit_alias_id = ea.id
              AND history_alias.project_id = ?
              AND history_alias.mailbox = 'alias'
        )`, `NOT EXISTS (
            SELECT 1 FROM microsoft_allocations active_alias
            WHERE active_alias.active_kind = 2
              AND active_alias.project_id = ?
              AND active_alias.active_entity_id = ea.id
        )`)
		aliasArgs = append(aliasArgs, projectID, projectID)
		if bucket != nil {
			if suffix != "" {
				aliasWhere = append(aliasWhere, "ea.alloc_bucket = ?")
			} else {
				aliasWhere = append(aliasWhere, "ms.alloc_bucket = ?")
			}
			aliasArgs = append(aliasArgs, *bucket)
		}
		aliasFrom := "explicit_aliases ea"
		if suffix != "" && bucket != nil {
			aliasFrom += " FORCE INDEX (idx_explicit_aliases_suffix_bucket)"
		}
		aliasRows, err := list(aliasFrom+" JOIN microsoft_resources ms ON ms.id = ea.resource_id", true, aliasWhere, aliasArgs)
		if err != nil {
			return nil, fmt.Errorf("list microsoft explicit alias source allocation candidates: %w", err)
		}

		rows = append(rows, aliasRows...)
		sort.Slice(rows, func(i, j int) bool {
			left, right := rows[i], rows[j]
			if (left.LastAllocatedAt == nil) != (right.LastAllocatedAt == nil) {
				return left.LastAllocatedAt == nil
			}
			if left.LastAllocatedAt != nil && !left.LastAllocatedAt.Equal(*right.LastAllocatedAt) {
				return left.LastAllocatedAt.Before(*right.LastAllocatedAt)
			}
			if left.QualityScore != right.QualityScore {
				return left.QualityScore > right.QualityScore
			}
			return left.ResourceID < right.ResourceID
		})
		result := make([]allocapp.MicrosoftCandidate, 0, min(limit, len(rows)))
		seen := make(map[uint]struct{}, len(rows))
		for _, row := range rows {
			if _, exists := seen[row.ResourceID]; exists {
				continue
			}
			seen[row.ResourceID] = struct{}{}
			result = append(result, allocapp.MicrosoftCandidate{
				ResourceID: row.ResourceID, EmailAddress: row.EmailAddress, QualityScore: row.QualityScore,
			})
			if len(result) == limit {
				break
			}
		}
		return result, nil
	}

	args := []any{projectID}
	where := []string{
		"ms.status = 'normal'",
		microsoftNotUnderBlockingMaintenanceCondition,
		microsoftProjectUnmatchedCondition,
	}
	if suffix != "" {
		where = append(where, "ms.email_domain = ?")
		args = append(args, suffix)
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "ms.for_sale = FALSE", "er.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "ms.for_sale = TRUE", "u.status = 'active'", "u.role IN ('supplier', 'admin', 'super_admin')")
	}
	if bucket != nil {
		where = append(where, "ms.alloc_bucket = ?")
		args = append(args, *bucket)
	}
	args = append(args, limit)

	query := `
SELECT ms.id AS resource_id, ms.email_address AS email_address, ms.quality_score AS quality_score
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ms.last_allocated_at ASC, ms.quality_score DESC, ms.id ASC
LIMIT ?`

	var rows []allocapp.MicrosoftCandidate
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list microsoft source allocation candidates: %w", err)
	}
	return rows, nil
}

func (r *Repo) ListICloudSourceCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, _ time.Time, limit int) ([]allocapp.ICloudCandidate, error) {
	if projectID == 0 || limit <= 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	where := []string{
		"ir.status = 'normal'",
		"ia.status = 'normal'",
		`NOT EXISTS (
            SELECT 1 FROM icloud_allocations history
            WHERE history.alias_id = ia.id AND history.project_id = ?
        )`,
		`NOT EXISTS (
	            SELECT 1 FROM icloud_allocations active
	            WHERE active.alias_id = ia.id
              AND active.project_id = ?
              AND active.status = 'allocated'
	        )`,
	}
	args := []any{projectID, projectID}
	forwardingCondition, forwardingArgs := iCloudForwardingDomainCondition("ia.forward_to_email")
	where = append(where, forwardingCondition)
	args = append(args, forwardingArgs...)
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "er.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "ir.for_sale = TRUE")
	}
	args = append(args, limit)
	var rows []allocapp.ICloudCandidate
	query := `
SELECT ir.id AS resource_id, ia.id AS alias_id, ia.email AS email
FROM icloud_aliases ia
JOIN icloud_resources ir ON ir.id = ia.resource_id
JOIN email_resources er ON er.id = ir.id AND er.type = 'icloud'
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ir.last_allocated_at ASC, ia.last_allocated_at ASC, ia.id ASC
LIMIT ?`
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list iCloud allocation candidates: %w", err)
	}
	return rows, nil
}

func (r *Repo) ListDomainSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]allocapp.DomainCandidate, error) {
	args := []any{}
	where := []string{
		"dr.status = 'normal'",
		"ms.status = 'online'",
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "dr.purpose = 'not_sale'", "dr.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "dr.purpose = 'sale'", "u.status = 'active'", "u.role IN ('supplier', 'admin', 'super_admin')")
	}
	if condition, conditionArgs := domainResourceSelectionCondition(emailSuffix); condition != "" {
		where = append(where, condition)
		args = append(args, conditionArgs...)
	}
	if bucket != nil {
		where = append(where, "dr.alloc_bucket = ?")
		args = append(args, *bucket)
	}
	args = append(args, limit)
	query := `
SELECT dr.id AS resource_id, dr.owner_user_id AS owner_user_id, dr.domain AS domain
FROM domain_resources dr
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY dr.last_allocated_at ASC, dr.id ASC
LIMIT ?`
	var rows []allocapp.DomainCandidate
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list domain source allocation candidates: %w", err)
	}
	return rows, nil
}

func (r *Repo) ListGeneratedMailboxCandidates(ctx context.Context, projectID uint, buyerUserID uint, scope domain.SupplyScope, bucket *uint16, limit int, emailSuffix string) ([]allocapp.GeneratedMailboxCandidate, error) {
	args := []any{projectID}
	where := []string{
		"gm.status = 'normal'",
		"dr.status = 'normal'",
		"ms.status = 'online'",
		`NOT EXISTS (
            SELECT 1
            FROM domain_allocations da
            WHERE da.project_id = ?
              AND da.email = gm.email
        )`,
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "dr.purpose = 'not_sale'", "dr.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "dr.purpose = 'sale'", "u.status = 'active'", "u.role IN ('supplier', 'admin', 'super_admin')")
	}
	if condition, conditionArgs := generatedMailboxSelectionCondition(emailSuffix); condition != "" {
		where = append(where, condition)
		args = append(args, conditionArgs...)
	}
	if bucket != nil {
		where = append(where, "gm.alloc_bucket = ?")
		args = append(args, *bucket)
	}
	args = append(args, limit)

	query := `
SELECT gm.id AS id, gm.resource_id AS resource_id, gm.email AS email
FROM generated_mailboxes gm
JOIN domain_resources dr ON dr.id = gm.resource_id
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY gm.last_allocated_at ASC, gm.id ASC
LIMIT ?`

	var rows []allocapp.GeneratedMailboxCandidate
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list generated mailbox allocation candidates: %w", err)
	}
	return rows, nil
}

// LockResourceRoot establishes the cross-context lock order shared with Core:
// email_resources first, then the resource subtype and allocation-owned rows.
// The first selected candidate may wait for a short administrator transaction;
// later candidates use TryLockResourceRoot so a transaction never waits while
// retaining locks from an earlier candidate.
func (r *Repo) LockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error) {
	return r.lockResourceRoot(ctx, resourceID, allocationType, false)
}

func (r *Repo) TryLockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error) {
	return r.lockResourceRoot(ctx, resourceID, allocationType, true)
}

func (r *Repo) lockResourceRoot(ctx context.Context, resourceID uint, allocationType domain.AllocationType, skipLocked bool) (bool, error) {
	if resourceID == 0 || !domain.IsValidAllocationType(allocationType) {
		return false, domain.ErrInvalidAllocationRequest
	}
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return false, domain.ErrAllocationTxRequired
	}
	var row struct {
		ID uint `gorm:"column:id"`
	}
	if r.dbFor(ctx).Name() != "mysql" {
		if err := r.dbFor(ctx).Table("email_resources").
			Where("id = ? AND type = ?", resourceID, string(allocationType)).
			Limit(1).Scan(&row).Error; err != nil {
			return false, fmt.Errorf("lock allocation resource root: %w", err)
		}
		return row.ID != 0, nil
	}
	query := `
SELECT id
FROM email_resources
WHERE id = ? AND type = ?
LIMIT 1
FOR UPDATE`
	if skipLocked {
		query += " SKIP LOCKED"
	}
	if err := r.dbFor(ctx).Raw(query, resourceID, string(allocationType)).Scan(&row).Error; err != nil {
		return false, fmt.Errorf("lock allocation resource root: %w", err)
	}
	return row.ID != 0, nil
}

func (r *Repo) LockMicrosoftCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, emailSuffix string) (*allocapp.MicrosoftCandidate, error) {
	// The SELECT projection comes before the WHERE clause, so its project key
	// must be the first bound argument.
	args := []any{projectID, resourceID, projectID}
	where := []string{
		"ms.id = ?",
		"ms.status = 'normal'",
		microsoftNotUnderBlockingMaintenanceCondition,
		microsoftProjectUnmatchedCondition,
	}
	suffix := normalizeCandidateSuffix(emailSuffix)
	if mailbox == domain.MicrosoftMailboxMain {
		condition, conditionArgs := microsoftMainCandidateCondition(projectID, suffix)
		where = append(where, condition)
		args = append(args, conditionArgs...)
	} else if suffix != "" {
		where = append(where, "ms.email_domain = ?")
		args = append(args, suffix)
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where,
			"ms.for_sale = FALSE",
			`EXISTS (
                SELECT 1
                FROM email_resources er
                WHERE er.id = ms.id
                  AND er.type = 'microsoft'
                  AND er.owner_user_id = ?
            )`,
		)
		args = append(args, buyerUserID)
	default:
		where = append(where,
			"ms.for_sale = TRUE",
			`EXISTS (
                SELECT 1
                FROM email_resources er
                JOIN users u ON u.id = er.owner_user_id
                WHERE er.id = ms.id
                  AND er.type = 'microsoft'
                  AND u.status = 'active'
                  AND u.role IN ('supplier', 'admin', 'super_admin')
            )`,
		)
	}
	query := `
SELECT ms.id AS resource_id,
       ms.email_address AS email_address,
       ms.quality_score AS quality_score,
       ms.plus_daily_limit AS plus_daily_limit,
       EXISTS (
           SELECT 1
           FROM microsoft_allocations active_main
           WHERE active_main.active_kind = 1
             AND active_main.project_id = ?
             AND active_main.active_entity_id = ms.id
       ) AS main_allocated
FROM microsoft_resources ms
WHERE ` + strings.Join(where, " AND ") + `
LIMIT 1
FOR UPDATE SKIP LOCKED`
	var row allocapp.MicrosoftCandidate
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock microsoft allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) LockGmailCandidate(ctx context.Context, resourceID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, mailbox domain.GmailMailbox) (*allocapp.GmailCandidate, error) {
	if resourceID == 0 || projectID == 0 || buyerUserID == 0 || !domain.IsValidGmailMailbox(mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	query := gmailSourceCandidateQuery(r.dbFor(ctx), projectID, buyerUserID, scope, mailbox).
		Select("gr.id AS resource_id, gr.email AS email").Where("gr.id = ?", resourceID)
	if r.dbFor(ctx).Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
	var row allocapp.GmailCandidate
	if err := query.Limit(1).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock Gmail allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) LockICloudCandidate(ctx context.Context, resourceID uint, aliasID uint, projectID uint, buyerUserID uint, scope domain.SupplyScope, _ time.Time) (*allocapp.ICloudCandidate, error) {
	if resourceID == 0 || aliasID == 0 || projectID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	where := []string{
		"ia.id = ?", "ia.resource_id = ?", "ia.status = 'normal'",
		"ir.status = 'normal'",
		`NOT EXISTS (
            SELECT 1 FROM icloud_allocations history
            WHERE history.alias_id = ia.id AND history.project_id = ?
        )`,
		`NOT EXISTS (
	            SELECT 1 FROM icloud_allocations active
	            WHERE active.alias_id = ia.id
              AND active.project_id = ?
              AND active.status = 'allocated'
	        )`,
	}
	args := []any{aliasID, resourceID, projectID, projectID}
	forwardingCondition, forwardingArgs := iCloudForwardingDomainCondition("ia.forward_to_email")
	where = append(where, forwardingCondition)
	args = append(args, forwardingArgs...)
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "er.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "ir.for_sale = TRUE")
	}
	var row allocapp.ICloudCandidate
	query := `
SELECT ir.id AS resource_id, ia.id AS alias_id, ia.email AS email
FROM icloud_aliases ia
JOIN icloud_resources ir ON ir.id = ia.resource_id
JOIN email_resources er ON er.id = ir.id AND er.type = 'icloud'
WHERE ` + strings.Join(where, " AND ") + `
LIMIT 1
FOR UPDATE SKIP LOCKED`
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock iCloud allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func iCloudForwardingDomainCondition(aliasColumn string) (string, []any) {
	domains := runtimeconfig.ICloudForwardingSuffixes(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	if len(domains) == 0 {
		return "1 = 0", nil
	}
	aliasDomain := "LOWER(SUBSTR(" + aliasColumn + ", INSTR(" + aliasColumn + ", '@') + 1))"
	return aliasDomain + ` IN ? AND EXISTS (
		SELECT 1 FROM domain_resources icloud_forwarding_domain
		WHERE icloud_forwarding_domain.purpose = 'binding'
		  AND icloud_forwarding_domain.status NOT IN ('disabled', 'deleted')
		  AND LOWER(icloud_forwarding_domain.domain) = ` + aliasDomain +
		`)`, []any{domains}
}

func (r *Repo) LockDomainCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*allocapp.DomainCandidate, error) {
	args := []any{resourceID}
	where := []string{
		"dr.id = ?",
		"dr.status = 'normal'",
		`EXISTS (
	      SELECT 1
	      FROM mail_servers ms
	      WHERE ms.id = dr.mail_server_id
	        AND ms.status = 'online'
	  )`,
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "dr.purpose = 'not_sale'", "dr.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where,
			"dr.purpose = 'sale'",
			`EXISTS (
	      SELECT 1
	      FROM email_resources er
	      JOIN users u ON u.id = er.owner_user_id
	      WHERE er.id = dr.id
	        AND er.type = 'domain'
	        AND u.status = 'active'
	        AND u.role IN ('supplier', 'admin', 'super_admin')
	  )`,
		)
	}
	if condition, conditionArgs := domainResourceSelectionCondition(emailSuffix); condition != "" {
		where = append(where, condition)
		args = append(args, conditionArgs...)
	}
	var row allocapp.DomainCandidate
	query := `
	SELECT dr.id AS resource_id, dr.owner_user_id AS owner_user_id, dr.domain AS domain, dr.mailbox_daily_limit AS mailbox_daily_limit
	FROM domain_resources dr
	WHERE ` + strings.Join(where, " AND ") + `
	LIMIT 1
	FOR UPDATE SKIP LOCKED`
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock domain allocation candidate: %w", err)
	}
	if row.ResourceID == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *Repo) LockGeneratedMailboxCandidate(ctx context.Context, mailboxID uint, resourceID uint, projectID uint) (*allocapp.GeneratedMailboxCandidate, error) {
	var row allocapp.GeneratedMailboxCandidate
	if err := r.dbFor(ctx).Raw(`
SELECT gm.id AS id, gm.resource_id AS resource_id, gm.email AS email
FROM generated_mailboxes gm
WHERE gm.id = ?
  AND gm.resource_id = ?
  AND gm.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM domain_allocations da
      WHERE da.project_id = ?
        AND da.email = gm.email
  )
LIMIT 1
FOR UPDATE SKIP LOCKED`, mailboxID, resourceID, projectID).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("lock generated mailbox allocation candidate: %w", err)
	}
	if row.ID == 0 {
		return nil, nil
	}
	return &row, nil
}

// AssertNoActiveAllocations participates in the caller's existing transaction.
// Core owns and locks the email_resources roots before invoking this guard;
// locking reads here then observe the latest committed allocation state and
// serialize with release without reversing the root-first order.
func (r *Repo) AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	if _, ok := platform.GormTxFromContext(ctx); !ok {
		return domain.ErrAllocationTxRequired
	}
	queries := []struct {
		table     string
		label     string
		condition string
	}{
		{table: "microsoft_allocations", label: "microsoft", condition: "resource_id IN ? AND status = 'allocated'"},
		{table: "domain_allocations", label: "domain", condition: "resource_id IN ? AND status = 'allocated'"},
		{table: "gmail_allocations", label: "Gmail", condition: "resource_id IN ? AND source = 'local' AND status = 'allocated'"},
		{table: "icloud_allocations", label: "iCloud", condition: "resource_id IN ? AND status = 'allocated'"},
	}
	for _, query := range queries {
		var row struct {
			ID uint `gorm:"column:id"`
		}
		statement := fmt.Sprintf(`
SELECT id
FROM %s
WHERE %s
ORDER BY resource_id ASC, id ASC
LIMIT 1`, query.table, query.condition)
		if r.dbFor(ctx).Name() == "mysql" {
			statement += " FOR UPDATE"
		}
		if err := r.dbFor(ctx).Raw(statement, resourceIDs).Scan(&row).Error; err != nil {
			return fmt.Errorf("check active %s allocations: %w", query.label, err)
		}
		if row.ID != 0 {
			return domain.ErrActiveAllocation
		}
	}
	return nil
}

func (r *Repo) IsMicrosoftMailboxHistoricallyMatched(ctx context.Context, projectID uint, mailbox domain.MicrosoftMailbox, mailboxID uint) (bool, error) {
	if projectID == 0 || mailboxID == 0 || !domain.IsValidMicrosoftMailbox(mailbox) {
		return false, domain.ErrInvalidAllocationRequest
	}
	column := "resource_id"
	switch mailbox {
	case domain.MicrosoftMailboxAlias:
		column = "explicit_alias_id"
	case domain.MicrosoftMailboxDot:
		column = "dot_alias_id"
	case domain.MicrosoftMailboxPlus:
		column = "plus_alias_id"
	}
	var matched bool
	query := fmt.Sprintf(`
SELECT EXISTS (
    SELECT 1
    FROM microsoft_allocations
    WHERE project_id = ? AND mailbox = ? AND %s = ?
    LIMIT 1
)`, column)
	if err := r.dbFor(ctx).Raw(query, projectID, string(mailbox), mailboxID).Scan(&matched).Error; err != nil {
		return false, fmt.Errorf("check microsoft mailbox project history: %w", err)
	}
	return matched, nil
}

func (r *Repo) CountGmailDotHistory(ctx context.Context, resourceID uint, projectID uint) (uint64, error) {
	if resourceID == 0 || projectID == 0 {
		return 0, domain.ErrInvalidAllocationRequest
	}
	var count uint64
	if err := r.dbFor(ctx).Raw(`
SELECT COUNT(*)
FROM gmail_allocations
WHERE source = ?
  AND resource_id = ?
  AND project_id = ?
  AND mailbox = ?`, gmailLocalSource, resourceID, projectID, domain.GmailMailboxDot).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("count Gmail dot mailbox history: %w", err)
	}
	return count, nil
}

func (r *Repo) ListUnavailableGmailMailboxEmails(ctx context.Context, projectID uint, mailbox domain.GmailMailbox, emails []string) (map[string]struct{}, error) {
	if projectID == 0 || mailbox == domain.GmailMailboxMain || !domain.IsValidGmailMailbox(mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	normalized := make([]string, 0, len(emails))
	seen := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		normalized = append(normalized, email)
	}
	if len(normalized) == 0 {
		return map[string]struct{}{}, nil
	}
	var rows []struct {
		Email string `gorm:"column:email"`
	}
	if err := r.dbFor(ctx).Table("gmail_allocations").
		Select("LOWER(TRIM(email)) AS email").
		Where("source = ? AND project_id = ? AND mailbox = ?", gmailLocalSource, projectID, mailbox).
		Where("LOWER(TRIM(email)) IN ?", normalized).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list unavailable Gmail mailbox emails: %w", err)
	}
	unavailable := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		unavailable[strings.ToLower(strings.TrimSpace(row.Email))] = struct{}{}
	}
	return unavailable, nil
}

func (r *Repo) IsGmailMailboxAvailable(ctx context.Context, resourceID uint, projectID uint, mailbox domain.GmailMailbox, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if resourceID == 0 || projectID == 0 || email == "" || !domain.IsValidGmailMailbox(mailbox) {
		return false, domain.ErrInvalidAllocationRequest
	}
	var unavailable bool
	var err error
	if mailbox == domain.GmailMailboxMain {
		err = r.dbFor(ctx).Raw(`
SELECT EXISTS (
    SELECT 1
    FROM gmail_allocations
    WHERE source = ?
      AND resource_id = ?
      AND mailbox = ?
      AND project_id = ?
    LIMIT 1
)`, gmailLocalSource, resourceID, domain.GmailMailboxMain, projectID).Scan(&unavailable).Error
	} else {
		err = r.dbFor(ctx).Raw(`
SELECT EXISTS (
    SELECT 1
    FROM gmail_allocations
    WHERE source = ?
      AND project_id = ?
      AND mailbox = ?
      AND LOWER(TRIM(email)) = ?
    LIMIT 1
)`, gmailLocalSource, projectID, mailbox, email).Scan(&unavailable).Error
	}
	if err != nil {
		return false, fmt.Errorf("check Gmail mailbox history: %w", err)
	}
	return !unavailable, nil
}

func (r *Repo) IsDomainEmailHistoricallyAllocated(ctx context.Context, projectID uint, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if projectID == 0 || email == "" {
		return false, domain.ErrInvalidAllocationRequest
	}
	var allocated bool
	if err := r.dbFor(ctx).Raw(`
SELECT EXISTS (
    SELECT 1
    FROM domain_allocations
    WHERE project_id = ? AND email = ?
    LIMIT 1
)`, projectID, email).Scan(&allocated).Error; err != nil {
		return false, fmt.Errorf("check domain email history: %w", err)
	}
	return allocated, nil
}

func (r *Repo) FindReusableExplicitAlias(ctx context.Context, projectID uint, resourceID uint, emailSuffix string) (*allocapp.AliasCandidate, error) {
	suffix := normalizeCandidateSuffix(emailSuffix)
	suffixSQL := ""
	args := []any{resourceID, projectID, projectID}
	if suffix != "" {
		suffixSQL = " AND ea.email_domain = ?"
		args = append(args, suffix)
	}
	var candidate allocapp.AliasCandidate
	err := r.dbFor(ctx).Raw(`
SELECT ea.id AS id, ea.email AS email
FROM explicit_aliases ea
WHERE ea.resource_id = ?
  AND ea.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
	  FROM microsoft_allocations history_alias
	  WHERE history_alias.explicit_alias_id = ea.id
	    AND history_alias.project_id = ?
	    AND history_alias.mailbox = 'alias'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM microsoft_allocations ma
      WHERE ma.active_kind = 2
        AND ma.project_id = ?
        AND ma.active_entity_id = ea.id
	  )`+suffixSQL+`
ORDER BY ea.id ASC
LIMIT 1`, args...).Scan(&candidate).Error
	if err != nil {
		return nil, fmt.Errorf("find reusable explicit alias: %w", err)
	}
	if candidate.ID == 0 {
		return nil, nil
	}

	var locked allocapp.AliasCandidate
	lockArgs := []any{candidate.ID, resourceID}
	if suffix != "" {
		lockArgs = append(lockArgs, suffix)
	}
	err = r.dbFor(ctx).Raw(`
SELECT ea.id AS id, ea.email AS email
FROM explicit_aliases ea
WHERE ea.id = ?
  AND ea.resource_id = ?
  AND ea.status = 'normal'`+suffixSQL+`
LIMIT 1
FOR UPDATE SKIP LOCKED`, lockArgs...).Scan(&locked).Error
	if err != nil {
		return nil, fmt.Errorf("lock reusable explicit alias: %w", err)
	}
	if locked.ID == 0 {
		return nil, nil
	}
	return &locked, nil
}

func (r *Repo) FindReusableDotAlias(ctx context.Context, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	return r.findReusableProjectAlias(ctx, "dot_aliases", "dot", projectID, resourceID)
}

func (r *Repo) FindReusablePlusAlias(ctx context.Context, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	return r.findReusableProjectAlias(ctx, "plus_aliases", "plus", projectID, resourceID)
}

func (r *Repo) FindExplicitAlias(ctx context.Context, resourceID uint, email string) (*allocapp.AliasCandidate, error) {
	var found allocapp.AliasCandidate
	if err := r.dbFor(ctx).Raw(`
SELECT id, email
FROM explicit_aliases
WHERE resource_id = ? AND email = ?
LIMIT 1
FOR UPDATE`, resourceID, strings.ToLower(strings.TrimSpace(email))).Scan(&found).Error; err != nil {
		return nil, fmt.Errorf("find explicit alias: %w", err)
	}
	if found.ID == 0 {
		return nil, nil
	}
	return &found, nil
}

func (r *Repo) findReusableProjectAlias(ctx context.Context, table, mailbox string, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	activeKind := 3
	if mailbox == "plus" {
		activeKind = 4
	}
	validAliasSQL := ""
	if mailbox == "dot" {
		validAliasSQL = `AND EXISTS (
      SELECT 1
      FROM microsoft_resources ms
      WHERE ms.id = a.resource_id
        AND ` + microsoftDotAliasValidForResourceExpression("a", "ms") + `
  )`
	}
	var candidate allocapp.AliasCandidate
	query := fmt.Sprintf(`
SELECT a.id AS id, a.email AS email
FROM %s a
WHERE a.resource_id = ?
  AND a.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
	  FROM microsoft_allocations history_alias
	  WHERE history_alias.project_id = ?
	    AND history_alias.mailbox = ?
	    AND (
	        (? = 'dot' AND history_alias.dot_alias_id = a.id)
	        OR (? = 'plus' AND history_alias.plus_alias_id = a.id)
	    )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM microsoft_allocations ma
      WHERE ma.active_kind = ?
        AND ma.project_id = ?
        AND ma.active_entity_id = a.id
  )
  %s
ORDER BY a.id ASC
LIMIT 1`, table, validAliasSQL)
	if err := r.dbFor(ctx).Raw(query, resourceID, projectID, mailbox, mailbox, mailbox, activeKind, projectID).Scan(&candidate).Error; err != nil {
		return nil, fmt.Errorf("find reusable %s alias: %w", mailbox, err)
	}
	if candidate.ID == 0 {
		return nil, nil
	}

	var locked allocapp.AliasCandidate
	lockQuery := fmt.Sprintf(`
SELECT a.id AS id, a.email AS email
FROM %s a
WHERE a.id = ?
  AND a.resource_id = ?
  AND a.status = 'normal'
  %s
LIMIT 1
FOR UPDATE SKIP LOCKED`, table, validAliasSQL)
	if err := r.dbFor(ctx).Raw(lockQuery, candidate.ID, resourceID).Scan(&locked).Error; err != nil {
		return nil, fmt.Errorf("lock reusable %s alias: %w", mailbox, err)
	}
	if locked.ID == 0 {
		return nil, nil
	}
	return &locked, nil
}

func (r *Repo) FindOrCreateDotAlias(ctx context.Context, resourceID uint, email string) (*allocapp.AliasCandidate, error) {
	model := DotAliasModel{ResourceID: resourceID, Email: strings.ToLower(strings.TrimSpace(email)), Status: "normal"}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("create dot alias: %w", err)
	}
	var found DotAliasModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find dot alias: %w", err)
	}
	if found.Status != "normal" {
		return nil, nil
	}
	return &allocapp.AliasCandidate{ID: found.ID, Email: found.Email}, nil
}

func (r *Repo) FindOrCreatePlusAlias(ctx context.Context, resourceID uint, email string) (*allocapp.AliasCandidate, error) {
	model := PlusAliasModel{ResourceID: resourceID, Email: strings.ToLower(strings.TrimSpace(email)), Status: "normal"}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("create plus alias: %w", err)
	}
	var found PlusAliasModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find plus alias: %w", err)
	}
	if found.Status != "normal" {
		return nil, nil
	}
	return &allocapp.AliasCandidate{ID: found.ID, Email: found.Email}, nil
}

func (r *Repo) FindReusableGeneratedMailbox(ctx context.Context, projectID uint, resourceID uint) (*allocapp.GeneratedMailboxCandidate, error) {
	var candidate allocapp.GeneratedMailboxCandidate
	if err := r.dbFor(ctx).Raw(`
SELECT gm.id AS id, gm.resource_id AS resource_id, gm.email AS email
FROM generated_mailboxes gm
WHERE gm.resource_id = ?
  AND gm.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM domain_allocations da
      WHERE da.project_id = ?
        AND da.email = gm.email
  )
ORDER BY gm.last_allocated_at ASC, gm.id ASC
LIMIT 1`, resourceID, projectID).Scan(&candidate).Error; err != nil {
		return nil, fmt.Errorf("find reusable generated mailbox: %w", err)
	}
	if candidate.ID == 0 {
		return nil, nil
	}

	var locked allocapp.GeneratedMailboxCandidate
	if err := r.dbFor(ctx).Raw(`
SELECT gm.id AS id, gm.resource_id AS resource_id, gm.email AS email
FROM generated_mailboxes gm
WHERE gm.id = ?
  AND gm.resource_id = ?
  AND gm.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM domain_allocations da
      WHERE da.project_id = ?
        AND da.email = gm.email
  )
LIMIT 1
FOR UPDATE SKIP LOCKED`, candidate.ID, resourceID, projectID).Scan(&locked).Error; err != nil {
		return nil, fmt.Errorf("lock reusable generated mailbox: %w", err)
	}
	if locked.ID == 0 {
		return nil, nil
	}
	return &locked, nil
}

func (r *Repo) FindOrCreateGeneratedMailbox(ctx context.Context, resourceID uint, ownerUserID uint, email string) (*allocapp.GeneratedMailboxCandidate, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	model := GeneratedMailboxModel{
		ResourceID:  resourceID,
		OwnerUserID: ownerUserID,
		Email:       normalizedEmail,
		Status:      "normal",
		AllocBucket: coredomain.GeneratedMailboxBucket(normalizedEmail),
	}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return nil, fmt.Errorf("create generated mailbox: %w", err)
	}
	var found GeneratedMailboxModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find generated mailbox: %w", err)
	}
	if found.Status != "normal" {
		return nil, nil
	}
	return &allocapp.GeneratedMailboxCandidate{ID: found.ID, ResourceID: found.ResourceID, Email: found.Email}, nil
}

func (r *Repo) EnsureDailyUsageAvailable(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error {
	usageDate = strings.TrimSpace(usageDate)
	if usageDate == "" || resourceID == 0 || limit <= 0 || !domain.IsValidAllocationType(allocationType) || !isValidDailyUsageKind(kind) {
		return domain.ErrInsufficientInventory
	}
	db := r.dbFor(ctx)
	if err := db.Exec(`
INSERT INTO allocation_daily_usages (usage_date, resource_type, resource_id, usage_kind, used_count)
VALUES (?, ?, ?, ?, 0)
ON DUPLICATE KEY UPDATE used_count = used_count`,
		usageDate, string(allocationType), resourceID, string(kind),
	).Error; err != nil {
		return fmt.Errorf("ensure daily usage row: %w", err)
	}
	var usedCount int
	if err := db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE usage_date = ? AND resource_type = ? AND resource_id = ? AND usage_kind = ?
FOR UPDATE`,
		usageDate, string(allocationType), resourceID, string(kind),
	).Scan(&usedCount).Error; err != nil {
		return fmt.Errorf("lock daily usage row: %w", err)
	}
	if usedCount >= limit {
		return domain.ErrInsufficientInventory
	}
	return nil
}

func (r *Repo) ConsumeDailyUsage(ctx context.Context, usageDate string, allocationType domain.AllocationType, resourceID uint, kind domain.DailyUsageKind, limit int) error {
	usageDate = strings.TrimSpace(usageDate)
	if usageDate == "" || resourceID == 0 || limit <= 0 || !domain.IsValidAllocationType(allocationType) || !isValidDailyUsageKind(kind) {
		return domain.ErrInsufficientInventory
	}
	result := r.dbFor(ctx).Exec(`
UPDATE allocation_daily_usages
SET used_count = used_count + 1
WHERE usage_date = ?
  AND resource_type = ?
  AND resource_id = ?
  AND usage_kind = ?
  AND used_count < ?`,
		usageDate, string(allocationType), resourceID, string(kind), limit,
	)
	if result.Error != nil {
		return fmt.Errorf("consume daily usage: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInsufficientInventory
	}
	return nil
}

func (r *Repo) CreateMicrosoftAllocation(ctx context.Context, allocation *domain.MicrosoftAllocation) error {
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	model := microsoftAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create microsoft allocation: %w", err)
	}
	*allocation = model.toDomain()
	return nil
}

func (r *Repo) CreateGmailAllocation(ctx context.Context, allocation *domain.GmailAllocation) error {
	if allocation == nil || allocation.ProjectID == 0 || allocation.ProductID == 0 || allocation.ResourceID == 0 ||
		!domain.IsValidGmailMailbox(allocation.Mailbox) || !domain.IsValidGmailServiceMode(allocation.ServiceMode) {
		return domain.ErrInvalidAllocationRequest
	}
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	model := gmailAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create Gmail allocation: %w", err)
	}
	*allocation = model.toDomain()
	return nil
}

func (r *Repo) CreateICloudAllocation(ctx context.Context, allocation *domain.ICloudAllocation) error {
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	model := icloudAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create iCloud allocation: %w", err)
	}
	*allocation = model.toDomain()
	return nil
}

func (r *Repo) CreateDomainAllocation(ctx context.Context, allocation *domain.GeneratedMailboxAllocation) error {
	if allocation.Status == "" {
		allocation.Status = domain.AllocationStatusAllocated
	}
	model := domainAllocationFromDomain(allocation)
	if err := r.dbFor(ctx).Create(model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrAllocationConflict
		}
		if isForeignKeyError(err) {
			return domain.ErrInvalidAllocationRequest
		}
		return fmt.Errorf("create domain allocation: %w", err)
	}
	*allocation = model.toDomain()
	return nil
}
func (r *Repo) TouchMicrosoftAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error {
	db := r.dbFor(ctx)
	if err := db.Model(&struct{}{}).Table("microsoft_resources").
		Where("id = ?", resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch microsoft allocated: %w", err)
	}
	return nil
}

func (r *Repo) TouchGmailAllocated(ctx context.Context, resourceID uint, allocatedAt time.Time) error {
	if err := r.dbFor(ctx).Table("gmail_resources").Where("id = ?", resourceID).
		Update("last_allocated_at", allocatedAt).Error; err != nil {
		return fmt.Errorf("touch Gmail allocated: %w", err)
	}
	return nil
}

func (r *Repo) TouchICloudAllocated(ctx context.Context, resourceID uint, aliasID uint, allocatedAt time.Time) error {
	db := r.dbFor(ctx)
	if err := db.Model(&struct{}{}).Table("icloud_resources").
		Where("id = ?", resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch iCloud resource allocated: %w", err)
	}
	if err := db.Model(&struct{}{}).Table("icloud_aliases").
		Where("id = ? AND resource_id = ?", aliasID, resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch iCloud alias allocated: %w", err)
	}
	return nil
}

func (r *Repo) TouchDomainAllocated(ctx context.Context, resourceID uint, mailboxID uint, allocatedAt time.Time) error {
	db := r.dbFor(ctx)
	if err := db.Model(&struct{}{}).Table("domain_resources").
		Where("id = ?", resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch domain allocated: %w", err)
	}
	if err := db.Model(&GeneratedMailboxModel{}).
		Where("id = ?", mailboxID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch generated mailbox allocated: %w", err)
	}
	return nil
}

func (r *Repo) ReleaseByOrder(ctx context.Context, orderNo string, releasedAt time.Time) (*domain.UnifiedAllocation, error) {
	var guard OrderGuardModel
	if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&guard).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrAllocationNotFound
		}
		return nil, fmt.Errorf("lock allocation guard for release: %w", err)
	}
	switch domain.AllocationType(guard.Type) {
	case domain.AllocationTypeMicrosoft:
		var model MicrosoftAllocationModel
		if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find microsoft allocation for release")
		}
		if model.Status == string(domain.AllocationStatusAllocated) {
			if err := r.dbFor(ctx).Model(&MicrosoftAllocationModel{}).
				Where("id = ? AND status = ?", model.ID, string(domain.AllocationStatusAllocated)).
				Updates(map[string]any{"status": string(domain.AllocationStatusReleased), "released_at": releasedAt}).Error; err != nil {
				return nil, fmt.Errorf("release microsoft allocation: %w", err)
			}
			model.Status = string(domain.AllocationStatusReleased)
			model.ReleasedAt = &releasedAt
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeDomain:
		var model DomainAllocationModel
		if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find domain allocation for release")
		}
		if model.Status == string(domain.AllocationStatusAllocated) {
			if err := r.dbFor(ctx).Model(&DomainAllocationModel{}).
				Where("id = ? AND status = ?", model.ID, string(domain.AllocationStatusAllocated)).
				Updates(map[string]any{"status": string(domain.AllocationStatusReleased), "released_at": releasedAt}).Error; err != nil {
				return nil, fmt.Errorf("release domain allocation: %w", err)
			}
			model.Status = string(domain.AllocationStatusReleased)
			model.ReleasedAt = &releasedAt
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeGmail:
		var model GmailAllocationModel
		if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ? AND source = ? AND resource_id IS NOT NULL AND project_id IS NOT NULL AND product_id IS NOT NULL", orderNo, gmailLocalSource).
			First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find Gmail allocation for release")
		}
		if model.Status == string(domain.AllocationStatusAllocated) {
			if err := r.dbFor(ctx).Model(&GmailAllocationModel{}).
				Where("id = ? AND source = ? AND status = ?", model.ID, gmailLocalSource, string(domain.AllocationStatusAllocated)).
				Updates(map[string]any{"status": string(domain.AllocationStatusReleased), "released_at": releasedAt}).Error; err != nil {
				return nil, fmt.Errorf("release Gmail allocation: %w", err)
			}
			model.Status = string(domain.AllocationStatusReleased)
			model.ReleasedAt = &releasedAt
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeICloud:
		var model ICloudAllocationModel
		if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find iCloud allocation for release")
		}
		if model.Status == string(domain.AllocationStatusAllocated) {
			if err := r.dbFor(ctx).Model(&ICloudAllocationModel{}).
				Where("id = ? AND status = ?", model.ID, string(domain.AllocationStatusAllocated)).
				Updates(map[string]any{"status": string(domain.AllocationStatusReleased), "released_at": releasedAt}).Error; err != nil {
				return nil, fmt.Errorf("release iCloud allocation: %w", err)
			}
			model.Status = string(domain.AllocationStatusReleased)
			model.ReleasedAt = &releasedAt
		}
		result := model.unified()
		return &result, nil
	default:
		return nil, domain.ErrAllocationNotFound
	}
}

func (r *Repo) ListAllocations(ctx context.Context, filter allocapp.AllocationFilter) (*allocapp.AllocationListResult, error) {
	items, total, err := r.queryUnifiedAllocations(ctx, filter, true)
	if err != nil {
		return nil, err
	}
	return &allocapp.AllocationListResult{
		Items:  items,
		Total:  total,
		Offset: filter.Offset,
		Limit:  filter.Limit,
	}, nil
}

func (r *Repo) FindAllocationDetail(ctx context.Context, allocationType domain.AllocationType, allocationID uint) (*domain.UnifiedAllocation, error) {
	switch allocationType {
	case domain.AllocationTypeMicrosoft:
		var model MicrosoftAllocationModel
		if err := r.dbFor(ctx).Where("id = ?", allocationID).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find microsoft allocation detail")
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeDomain:
		var model DomainAllocationModel
		if err := r.dbFor(ctx).Where("id = ?", allocationID).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find domain allocation detail")
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeGmail:
		var model GmailAllocationModel
		if err := r.dbFor(ctx).
			Where("id = ? AND source = ? AND resource_id IS NOT NULL AND project_id IS NOT NULL AND product_id IS NOT NULL", allocationID, gmailLocalSource).
			First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find Gmail allocation detail")
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeICloud:
		var model ICloudAllocationModel
		if err := r.dbFor(ctx).Where("id = ?", allocationID).First(&model).Error; err != nil {
			return nil, findAllocationError(err, "find iCloud allocation detail")
		}
		result := model.unified()
		return &result, nil
	default:
		return nil, domain.ErrInvalidAllocationRequest
	}
}

func (r *Repo) FindAllocationByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	result, err := r.FindExistingAllocation(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, domain.ErrAllocationNotFound
	}
	return result, nil
}

func (r *Repo) FindAllocationsByOrders(ctx context.Context, orderNos []string) (map[string]domain.UnifiedAllocation, error) {
	items, _, err := r.queryUnifiedAllocations(ctx, allocapp.AllocationFilter{OrderNos: orderNos}, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string]domain.UnifiedAllocation, len(items))
	for _, item := range items {
		if _, exists := result[item.OrderNo]; exists {
			return nil, domain.ErrAllocationConflict
		}
		result[item.OrderNo] = item
	}
	return result, nil
}

func (r *Repo) ListActiveByRecipient(ctx context.Context, recipient string) ([]domain.UnifiedAllocation, error) {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	var ms []MicrosoftAllocationModel
	if err := r.dbFor(ctx).
		Where("email = ? AND status = ?", recipient, string(domain.AllocationStatusAllocated)).
		Find(&ms).Error; err != nil {
		return nil, fmt.Errorf("list microsoft allocation by recipient: %w", err)
	}
	var ds []DomainAllocationModel
	if err := r.dbFor(ctx).
		Where("email = ? AND status = ?", recipient, string(domain.AllocationStatusAllocated)).
		Find(&ds).Error; err != nil {
		return nil, fmt.Errorf("list domain allocation by recipient: %w", err)
	}
	var is []ICloudAllocationModel
	if err := r.dbFor(ctx).
		Where("email = ? AND status = ?", recipient, string(domain.AllocationStatusAllocated)).
		Find(&is).Error; err != nil {
		return nil, fmt.Errorf("list iCloud allocation by recipient: %w", err)
	}
	var gs []GmailAllocationModel
	if err := r.dbFor(ctx).
		Where("email = ? AND source = ? AND resource_id IS NOT NULL AND project_id IS NOT NULL AND product_id IS NOT NULL AND status = ?", recipient, gmailLocalSource, string(domain.AllocationStatusAllocated)).
		Find(&gs).Error; err != nil {
		return nil, fmt.Errorf("list Gmail allocation by recipient: %w", err)
	}
	result := make([]domain.UnifiedAllocation, 0, len(ms)+len(ds)+len(gs)+len(is))
	for _, item := range ms {
		result = append(result, item.unified())
	}
	for _, item := range ds {
		result = append(result, item.unified())
	}
	for _, item := range gs {
		result = append(result, item.unified())
	}
	for _, item := range is {
		result = append(result, item.unified())
	}
	return result, nil
}

func (r *Repo) ListInventoryProjectIDs(ctx context.Context) ([]uint, error) {
	projects, err := r.ListInventoryProjects(ctx)
	if err != nil {
		return nil, err
	}
	projectIDs := make([]uint, len(projects))
	for i := range projects {
		projectIDs[i] = projects[i].ID
	}
	return projectIDs, nil
}

func (r *Repo) ListInventoryProjects(ctx context.Context) ([]allocapp.InventoryProject, error) {
	var projects []allocapp.InventoryProject
	if err := r.dbFor(ctx).
		Table("projects AS p").
		Select("p.id, p.name").
		Where("p.status = 'listed'").
		Where("EXISTS (SELECT 1 FROM project_products pp WHERE pp.project_id = p.id AND pp.status = 'enabled' AND pp.type IN ('microsoft', 'domain', 'gmail', 'gmail_variant', 'icloud'))").
		Order("p.id ASC").
		Scan(&projects).Error; err != nil {
		return nil, fmt.Errorf("list inventory projects: %w", err)
	}
	return projects, nil
}

func (r *Repo) GetInventoryStats(ctx context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	stats := &allocapp.InventoryStats{ProjectID: projectID}
	var productRows []struct {
		Type            string
		CodeEnabled     bool
		PurchaseEnabled bool
		MainWeight      int
		DotWeight       int
		PlusWeight      int
	}
	if err := r.dbFor(ctx).Raw(`
SELECT pp.type AS type,
       pp.code_enabled AS code_enabled,
       pp.purchase_enabled AS purchase_enabled,
       pp.main_weight AS main_weight,
       pp.dot_weight AS dot_weight,
       pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
	WHERE p.id = ?
	  AND p.status = 'listed'
	  AND pp.status = 'enabled'
	  AND pp.type IN ('microsoft', 'domain', 'gmail', 'gmail_variant', 'icloud')`, projectID).Scan(&productRows).Error; err != nil {
		return nil, fmt.Errorf("load inventory project products: %w", err)
	}
	if len(productRows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, row := range productRows {
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeMicrosoft:
			stats.Microsoft.Enabled = true
			stats.Microsoft.MainEnabled = stats.Microsoft.MainEnabled || row.MainWeight > 0
			stats.Microsoft.DotEnabled = stats.Microsoft.DotEnabled || row.DotWeight > 0
			stats.Microsoft.PlusEnabled = stats.Microsoft.PlusEnabled || row.PlusWeight > 0
		case coredomain.ProductTypeDomain:
			stats.Domain.Enabled = true
		case coredomain.ProductTypeGmail:
			stats.Gmail.Enabled = true
			stats.Gmail.CodeEnabled = stats.Gmail.CodeEnabled || row.CodeEnabled
			stats.Gmail.PurchaseEnabled = stats.Gmail.PurchaseEnabled || row.PurchaseEnabled
			stats.Gmail.MainEnabled = true
		case coredomain.ProductTypeGmailVariant:
			stats.Gmail.Enabled = true
			stats.Gmail.CodeEnabled = stats.Gmail.CodeEnabled || row.CodeEnabled
			stats.Gmail.PurchaseEnabled = stats.Gmail.PurchaseEnabled || row.PurchaseEnabled
			stats.Gmail.DotEnabled = true
			stats.Gmail.PlusEnabled = true
		case coredomain.ProductTypeICloud:
			stats.ICloud.Enabled = true
		}
	}
	scan := func(target any, query string, args ...any) error {
		if err := r.dbFor(ctx).Raw(query, args...).Scan(target).Error; err != nil {
			return fmt.Errorf("inventory stats: %w", err)
		}
		return nil
	}
	if stats.Microsoft.Enabled {
		microsoftScope, microsoftScopeArgs := microsoftProjectInventoryScopeSQL(projectID)
		microsoftAllowed, microsoftAllowedArgs := microsoftProjectSuffixAllowedCondition(projectID, "ms.email_domain")
		microsoftAliasAllowed, microsoftAliasAllowedArgs := microsoftProjectSuffixAllowedCondition(projectID, "ea.email_domain")
		var capacity struct {
			EligibleResources int64
			DotCapacity       int64
			PlusDailyLimit    int64
		}
		if err := scan(&capacity, `
SELECT COUNT(*) AS eligible_resources,
       COALESCE(SUM(`+microsoftDotCapacityExpression("ms")+`), 0) AS dot_capacity,
       COALESCE(SUM(ms.plus_daily_limit), 0) AS plus_daily_limit
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
	  AND `+microsoftScope+`
  AND `+microsoftAllowed, append(microsoftScopeArgs, microsoftAllowedArgs...)...); err != nil {
			return nil, err
		}
		stats.Microsoft.EligibleResources = capacity.EligibleResources
		stats.Microsoft.PlusDailyLimit = capacity.PlusDailyLimit
		if stats.Microsoft.MainEnabled {
			if err := scan(&stats.Microsoft.MainAvailable, `
SELECT COUNT(*)
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+microsoftScope+`
  AND `+microsoftAllowed+`
  AND `+microsoftUnusedMainCondition, append(append(append([]any{}, microsoftScopeArgs...), microsoftAllowedArgs...), projectID, projectID)...); err != nil {
				return nil, err
			}
			if err := scan(&stats.Microsoft.ExplicitAliasAvailable, `
SELECT COUNT(*)
FROM explicit_aliases ea
JOIN microsoft_resources ms ON ms.id = ea.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ea.status = 'normal'
  AND ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+microsoftScope+`
  AND `+microsoftAliasAllowed+`
  AND NOT EXISTS (
      SELECT 1 FROM microsoft_allocations history_alias
      WHERE history_alias.explicit_alias_id = ea.id
        AND history_alias.project_id = ?
        AND history_alias.mailbox = 'alias'
  )
  AND NOT EXISTS (
      SELECT 1 FROM microsoft_allocations active_alias
      WHERE active_alias.active_kind = 2
        AND active_alias.project_id = ?
        AND active_alias.active_entity_id = ea.id
			  )`, append(append(append([]any{}, microsoftScopeArgs...), microsoftAliasAllowedArgs...), projectID, projectID)...); err != nil {
				return nil, err
			}
		}
		if stats.Microsoft.DotEnabled {
			stats.Microsoft.DotCapacity = capacity.DotCapacity
			if err := scan(&stats.Microsoft.ActiveDotAllocations, `
SELECT COUNT(*)
FROM microsoft_allocations ma
JOIN microsoft_resources ms ON ms.id = ma.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ma.project_id = ?
  AND ma.mailbox = 'dot'
  AND ma.status = 'allocated'
  AND ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
	  AND `+microsoftScope+`
	  AND `+microsoftAllowed, append(append([]any{projectID}, microsoftScopeArgs...), microsoftAllowedArgs...)...); err != nil {
				return nil, err
			}
			var dotAdjustment struct {
				CanonicalUnavailable int64
				NoncanonicalReusable int64
			}
			dotVariant := microsoftDotAliasMatchesResourceExpression("da", "ms")
			validDotAlias := microsoftDotAliasValidForResourceExpression("da", "ms")
			if err := scan(&dotAdjustment, `
SELECT
    COALESCE(SUM(CASE
        WHEN `+dotVariant+` AND (
            da.status <> 'normal'
            OR EXISTS (
                SELECT 1 FROM microsoft_allocations history_dot
                WHERE history_dot.dot_alias_id = da.id
                  AND history_dot.project_id = ?
                  AND history_dot.mailbox = 'dot'
            )
        ) THEN 1 ELSE 0 END), 0) AS canonical_unavailable,
    COALESCE(SUM(CASE
        WHEN NOT `+dotVariant+`
         AND `+validDotAlias+`
         AND da.status = 'normal'
         AND NOT EXISTS (
             SELECT 1 FROM microsoft_allocations history_dot
             WHERE history_dot.dot_alias_id = da.id
               AND history_dot.project_id = ?
               AND history_dot.mailbox = 'dot'
         ) THEN 1 ELSE 0 END), 0) AS noncanonical_reusable
FROM dot_aliases da
JOIN microsoft_resources ms ON ms.id = da.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
	  AND `+microsoftScope+`
	  AND `+microsoftAllowed, append(append([]any{projectID, projectID}, microsoftScopeArgs...), microsoftAllowedArgs...)...); err != nil {
				return nil, err
			}
			stats.Microsoft.DotAvailable = nonNegative(stats.Microsoft.DotCapacity-dotAdjustment.CanonicalUnavailable) + dotAdjustment.NoncanonicalReusable
		}
		if stats.Microsoft.PlusEnabled {
			if err := scan(&stats.Microsoft.PlusDailyUsed, `
SELECT COALESCE(SUM(adu.used_count), 0)
FROM allocation_daily_usages adu
JOIN microsoft_resources ms ON ms.id = adu.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE adu.usage_date = ?
  AND adu.resource_type = 'microsoft'
  AND adu.usage_kind = 'plus'
  AND ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
	  AND `+microsoftScope+`
	  AND `+microsoftAllowed, append(append([]any{today}, microsoftScopeArgs...), microsoftAllowedArgs...)...); err != nil {
				return nil, err
			}
			stats.Microsoft.PlusDailyAvailable = nonNegative(stats.Microsoft.PlusDailyLimit - stats.Microsoft.PlusDailyUsed)
		}
		if stats.Microsoft.MainEnabled {
			stats.Microsoft.TotalAvailable += stats.Microsoft.MainAvailable + stats.Microsoft.ExplicitAliasAvailable
		}
		if stats.Microsoft.DotEnabled {
			stats.Microsoft.TotalAvailable += stats.Microsoft.DotAvailable
		}
		if stats.Microsoft.PlusEnabled {
			stats.Microsoft.TotalAvailable += stats.Microsoft.PlusDailyAvailable
		}
	}
	if stats.Domain.Enabled {
		var capacity struct {
			EligibleResources int64
			MailboxDailyLimit int64
		}
		domainScope, domainScopeArgs := domainInventoryScopeSQL()
		if err := scan(&capacity, `
SELECT COUNT(*) AS eligible_resources, COALESCE(SUM(dr.mailbox_daily_limit), 0) AS mailbox_daily_limit
FROM domain_resources dr
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE dr.status = 'normal'
  AND ms.status = 'online'
  AND `+domainScope, domainScopeArgs...); err != nil {
			return nil, err
		}
		stats.Domain.EligibleResources = capacity.EligibleResources
		stats.Domain.MailboxDailyLimit = capacity.MailboxDailyLimit
		if err := scan(&stats.Domain.MailboxDailyUsed, `
SELECT COALESCE(SUM(adu.used_count), 0)
FROM allocation_daily_usages adu
JOIN domain_resources dr ON dr.id = adu.resource_id
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE adu.usage_date = ?
  AND adu.resource_type = 'domain'
  AND adu.usage_kind = 'domain_mailbox'
  AND dr.status = 'normal'
  AND ms.status = 'online'
  AND `+domainScope, append([]any{today}, domainScopeArgs...)...); err != nil {
			return nil, err
		}
		stats.Domain.MailboxDailyAvailable = nonNegative(stats.Domain.MailboxDailyLimit - stats.Domain.MailboxDailyUsed)
		stats.Domain.TotalAvailable = stats.Domain.MailboxDailyAvailable
	}
	if stats.Gmail.Enabled {
		gmailScope := "gr.for_sale = TRUE AND owner.status = 'active' AND owner.role IN ('supplier', 'admin', 'super_admin')"
		if err := scan(&stats.Gmail.EligibleResources, `
SELECT COUNT(*)
FROM gmail_resources gr
JOIN email_resources er ON er.id = gr.id AND er.type = 'gmail'
JOIN users owner ON owner.id = er.owner_user_id
WHERE gr.status IN ('normal', 'available')
  AND `+gmailScope); err != nil {
			return nil, err
		}
		stats.Gmail.PublicEligibleResources = stats.Gmail.EligibleResources
		if stats.Gmail.MainEnabled {
			if err := scan(&stats.Gmail.MainAvailable, `
SELECT COUNT(*)
FROM gmail_resources gr
JOIN email_resources er ON er.id = gr.id AND er.type = 'gmail'
JOIN users owner ON owner.id = er.owner_user_id
WHERE gr.status IN ('normal', 'available')
  AND `+gmailScope+`
  AND NOT EXISTS (
      SELECT 1 FROM gmail_allocations history
      WHERE history.source = 'local'
        AND history.resource_id = gr.id
        AND history.project_id = ?
        AND history.mailbox = 'main'
  )`, projectID); err != nil {
				return nil, err
			}
			stats.Gmail.MainPublicAvailable = stats.Gmail.MainAvailable
		}
		if stats.Gmail.DotEnabled {
			var dot struct {
				Capacity int64
				Used     int64
			}
			if err := scan(&dot, `
SELECT COALESCE(SUM(`+gmailDotCapacityExpression("gr")+`), 0) AS capacity,
       COALESCE((
           SELECT COUNT(*)
           FROM gmail_allocations history
           JOIN gmail_resources history_gr ON history_gr.id = history.resource_id
           JOIN email_resources history_er ON history_er.id = history_gr.id AND history_er.type = 'gmail'
           JOIN users history_owner ON history_owner.id = history_er.owner_user_id
           WHERE history.source = 'local'
             AND history.project_id = ?
             AND history.mailbox = 'dot'
             AND history_gr.status IN ('normal', 'available')
             AND history_gr.for_sale = TRUE
             AND history_owner.status = 'active'
             AND history_owner.role IN ('supplier', 'admin', 'super_admin')
       ), 0) AS used
FROM gmail_resources gr
JOIN email_resources er ON er.id = gr.id AND er.type = 'gmail'
JOIN users owner ON owner.id = er.owner_user_id
WHERE gr.status IN ('normal', 'available')
  AND `+gmailScope, projectID); err != nil {
				return nil, err
			}
			stats.Gmail.DotAvailable = nonNegative(dot.Capacity - dot.Used)
			stats.Gmail.DotPublicAvailable = stats.Gmail.DotAvailable
		}
		if stats.Gmail.PlusEnabled {
			if stats.Gmail.EligibleResources > 0 {
				stats.Gmail.PlusAvailable = allocapp.GmailVariantInventory
			}
			stats.Gmail.PlusPublicAvailable = stats.Gmail.PlusAvailable
		}
		stats.Gmail.TotalAvailable = stats.Gmail.MainAvailable + stats.Gmail.DotAvailable + stats.Gmail.PlusAvailable
		stats.Gmail.PublicAvailable = stats.Gmail.TotalAvailable
	}
	if stats.ICloud.Enabled {
		if err := scan(&stats.ICloud.EligibleResources, `
SELECT COUNT(*)
FROM icloud_resources ir
JOIN email_resources er ON er.id = ir.id AND er.type = 'icloud'
WHERE ir.status = 'normal'
  AND ir.for_sale = TRUE`); err != nil {
			return nil, err
		}
		if err := scan(&stats.ICloud.AliasAvailable, `
SELECT COUNT(*)
FROM icloud_aliases ia
JOIN icloud_resources ir ON ir.id = ia.resource_id
JOIN email_resources er ON er.id = ir.id AND er.type = 'icloud'
WHERE ia.status = 'normal'
  AND ir.status = 'normal'
  AND ir.for_sale = TRUE
  AND NOT EXISTS (
      SELECT 1 FROM icloud_allocations history
      WHERE history.alias_id = ia.id AND history.project_id = ?
  )
  AND NOT EXISTS (
      SELECT 1 FROM icloud_allocations active
      WHERE active.alias_id = ia.id
        AND active.project_id = ?
        AND active.status = 'allocated'
  )`, projectID, projectID); err != nil {
			return nil, err
		}
		stats.ICloud.TotalAvailable = stats.ICloud.AliasAvailable
	}
	if err := scan(&stats.ActiveMicrosoftAllocations, `SELECT COUNT(*) FROM microsoft_allocations WHERE project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	if err := scan(&stats.ActiveDomainAllocations, `SELECT COUNT(*) FROM domain_allocations WHERE project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	if err := scan(&stats.ActiveGmailAllocations, `SELECT COUNT(*) FROM gmail_allocations WHERE source = 'local' AND project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	if err := scan(&stats.ActiveICloudAllocations, `SELECT COUNT(*) FROM icloud_allocations WHERE project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	stats.TotalAvailable = stats.Microsoft.TotalAvailable + stats.Domain.TotalAvailable + stats.Gmail.TotalAvailable + stats.ICloud.TotalAvailable
	return stats, nil
}

func (r *Repo) AssertProjectInventoryAccess(ctx context.Context, projectID uint, buyerUserID uint) error {
	var visible bool
	if err := r.dbFor(ctx).Raw(`
SELECT EXISTS (
    SELECT 1
    FROM projects p
    WHERE p.id = ?
      AND p.status = 'listed'
      AND (
          p.access_type = 'public'
          OR EXISTS (
              SELECT 1
              FROM project_accesses pa
              WHERE pa.project_id = p.id AND pa.user_id = ?
          )
      )
      AND EXISTS (
          SELECT 1
          FROM project_products pp
          WHERE pp.project_id = p.id AND pp.status = 'enabled'
      )
)`, projectID, buyerUserID).Scan(&visible).Error; err != nil {
		return fmt.Errorf("check project inventory access: %w", err)
	}
	if !visible {
		return domain.ErrProjectNotAllocatable
	}
	return nil
}

type productInventoryRow struct {
	ProductID       uint
	Type            string
	CodeEnabled     bool
	PurchaseEnabled bool
	MainWeight      int
	DotWeight       int
	PlusWeight      int
}

func (r *Repo) ListProductSuffixInventory(ctx context.Context, config allocapp.ProductAllocationConfig, buyerUserID uint, supplyScope domain.SupplyScope) (map[string]int64, error) {
	switch config.ProductType {
	case coredomain.ProductTypeMicrosoft:
		row := productInventoryRow{
			ProductID: config.ProductID, Type: string(config.ProductType),
			MainWeight: config.MainWeight, DotWeight: config.DotWeight, PlusWeight: config.PlusWeight,
		}
		scope, args := microsoftProjectInventoryScopeSQL(config.ProjectID)
		if supplyScope == domain.SupplyScopeOwned {
			scope, args = microsoftPrivateProjectInventoryScopeSQL(config.ProjectID, buyerUserID)
		}
		return r.microsoftSuffixInventory(ctx, config.ProjectID, row, scope, args)
	case coredomain.ProductTypeDomain:
		scope, args := domainInventoryScopeSQL()
		if supplyScope == domain.SupplyScopeOwned {
			scope, args = domainPrivateInventoryScopeSQL(buyerUserID)
		}
		return r.domainInventoryByScope(ctx, scope, args, "dr.domain_tld")
	default:
		return nil, domain.ErrInvalidAllocationRequest
	}
}

func (r *Repo) GetProductInventoryTotals(ctx context.Context, projectID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	var productRows []productInventoryRow
	if err := r.dbFor(ctx).Raw(`
SELECT
    pp.id AS product_id,
    pp.type AS type,
	pp.code_enabled AS code_enabled,
	pp.purchase_enabled AS purchase_enabled,
    pp.main_weight AS main_weight,
    pp.dot_weight AS dot_weight,
    pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
	WHERE p.id = ?
	  AND p.status = 'listed'
	  AND pp.status = 'enabled'
	  AND pp.type IN ('microsoft', 'domain', 'gmail', 'gmail_variant', 'icloud')
	ORDER BY pp.id ASC`, projectID).Scan(&productRows).Error; err != nil {
		return nil, fmt.Errorf("load product inventory rows: %w", err)
	}
	if len(productRows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	stats, err := r.GetInventoryStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := &allocapp.ProjectProductInventoryTotals{
		ProjectID:      projectID,
		TotalAvailable: stats.TotalAvailable,
		Items:          make([]allocapp.ProductInventoryTotal, 0, len(productRows)),
	}
	for _, row := range productRows {
		item := allocapp.ProductInventoryTotal{
			ProductID:       row.ProductID,
			ProductType:     coredomain.ProductType(row.Type),
			TotalAvailable:  productInventoryTotalFromStats(row, stats),
			PublicAvailable: productInventoryPublicTotalFromStats(row, stats),
		}
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeMicrosoft:
			item.Suffixes, err = r.microsoftProductInventorySuffixTotals(ctx, projectID, row)
		case coredomain.ProductTypeDomain:
			item.Suffixes, err = r.domainProductInventorySuffixTotals(ctx)
		case coredomain.ProductTypeGmail, coredomain.ProductTypeGmailVariant, coredomain.ProductTypeICloud:
			codeAvailable, codePublicAvailable := item.TotalAvailable, item.PublicAvailable
			purchaseAvailable, purchasePublicAvailable := item.TotalAvailable, item.PublicAvailable
			item.CodeAvailable, item.CodePublicAvailable = &codeAvailable, &codePublicAvailable
			item.PurchaseAvailable, item.PurchasePublicAvailable = &purchaseAvailable, &purchasePublicAvailable
		default:
			return nil, domain.ErrProjectNotAllocatable
		}
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func productInventoryTotalFromStats(row productInventoryRow, stats *allocapp.InventoryStats) int64 {
	if stats == nil {
		return 0
	}
	switch coredomain.ProductType(row.Type) {
	case coredomain.ProductTypeMicrosoft:
		total := int64(0)
		if row.MainWeight > 0 {
			total += stats.Microsoft.MainAvailable + stats.Microsoft.ExplicitAliasAvailable
		}
		if row.DotWeight > 0 {
			total += stats.Microsoft.DotAvailable
		}
		if row.PlusWeight > 0 {
			total += stats.Microsoft.PlusDailyAvailable
		}
		return total
	case coredomain.ProductTypeDomain:
		return stats.Domain.TotalAvailable
	case coredomain.ProductTypeGmail:
		return stats.Gmail.MainAvailable
	case coredomain.ProductTypeGmailVariant:
		return stats.Gmail.DotAvailable + stats.Gmail.PlusAvailable
	case coredomain.ProductTypeICloud:
		return stats.ICloud.TotalAvailable
	default:
		return 0
	}
}

func productInventoryPublicTotalFromStats(row productInventoryRow, stats *allocapp.InventoryStats) int64 {
	if stats == nil {
		return 0
	}
	switch coredomain.ProductType(row.Type) {
	case coredomain.ProductTypeGmail:
		return stats.Gmail.MainPublicAvailable
	case coredomain.ProductTypeGmailVariant:
		return stats.Gmail.DotPublicAvailable + stats.Gmail.PlusPublicAvailable
	default:
		return productInventoryTotalFromStats(row, stats)
	}
}

type suffixInventoryValue struct {
	TotalAvailable  int64
	PublicAvailable int64
}

func (r *Repo) microsoftProductInventorySuffixTotals(ctx context.Context, projectID uint, row productInventoryRow) ([]allocapp.ProductInventorySuffixTotal, error) {
	scope, scopeArgs := microsoftProjectInventoryScopeSQL(projectID)
	total, err := r.microsoftSuffixInventory(ctx, projectID, row, scope, scopeArgs)
	if err != nil {
		return nil, err
	}
	return mergeSuffixInventory(total, total), nil
}

func (r *Repo) microsoftSuffixInventory(ctx context.Context, projectID uint, row productInventoryRow, scope string, scopeArgs []any) (map[string]int64, error) {
	result := map[string]int64{}
	allowedRoot, allowedRootArgs := microsoftProjectSuffixAllowedCondition(projectID, "ms.email_domain")
	allowedAlias, allowedAliasArgs := microsoftProjectSuffixAllowedCondition(projectID, "ea.email_domain")
	var capacities []struct {
		Suffix         string
		DotCapacity    int64
		PlusDailyLimit int64
	}
	if err := r.dbFor(ctx).Raw(`
SELECT ms.email_domain AS suffix,
       COALESCE(SUM(`+microsoftDotCapacityExpression("ms")+`), 0) AS dot_capacity,
       COALESCE(SUM(ms.plus_daily_limit), 0) AS plus_daily_limit
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+scope+`
  AND `+allowedRoot+`
GROUP BY ms.email_domain`, append(append([]any{}, scopeArgs...), allowedRootArgs...)...).Scan(&capacities).Error; err != nil {
		return nil, fmt.Errorf("microsoft suffix capacity: %w", err)
	}
	dotCapacityBySuffix := map[string]int64{}
	plusLimitBySuffix := map[string]int64{}
	for _, row := range capacities {
		suffix := normalizeCandidateSuffix(row.Suffix)
		if suffix == "" {
			continue
		}
		dotCapacityBySuffix[suffix] += row.DotCapacity
		plusLimitBySuffix[suffix] += row.PlusDailyLimit
	}

	if row.MainWeight > 0 {
		availableMain, err := r.microsoftSuffixCount(ctx, `
SELECT ms.email_domain AS suffix, COUNT(*) AS count
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+scope+`
  AND `+allowedRoot+`
  AND `+microsoftUnusedMainCondition+`
GROUP BY ms.email_domain`, append(append(append([]any{}, scopeArgs...), allowedRootArgs...), projectID, projectID)...)
		if err != nil {
			return nil, err
		}
		explicitAlias, err := r.microsoftSuffixCount(ctx, `
SELECT ea.email_domain AS suffix, COUNT(*) AS count
FROM explicit_aliases ea
JOIN microsoft_resources ms ON ms.id = ea.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ea.status = 'normal'
  AND ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+scope+`
  AND `+allowedAlias+`
  AND NOT EXISTS (
      SELECT 1 FROM microsoft_allocations history_alias
      WHERE history_alias.explicit_alias_id = ea.id
        AND history_alias.project_id = ?
        AND history_alias.mailbox = 'alias'
  )
  AND NOT EXISTS (
      SELECT 1 FROM microsoft_allocations active_alias
      WHERE active_alias.active_kind = 2
        AND active_alias.project_id = ?
        AND active_alias.active_entity_id = ea.id
  )
GROUP BY ea.email_domain`, append(append(append([]any{}, scopeArgs...), allowedAliasArgs...), projectID, projectID)...)
		if err != nil {
			return nil, err
		}
		for suffix, count := range availableMain {
			result[suffix] += count
		}
		for suffix, count := range explicitAlias {
			result[suffix] += count
		}
	}

	if row.DotWeight > 0 {
		type dotSuffixAdjustment struct {
			Suffix               string
			CanonicalUnavailable int64
			NoncanonicalReusable int64
		}
		adjustments := make([]dotSuffixAdjustment, 0, len(dotCapacityBySuffix))
		dotVariant := microsoftDotAliasMatchesResourceExpression("da", "ms")
		validDotAlias := microsoftDotAliasValidForResourceExpression("da", "ms")
		err := r.dbFor(ctx).Raw(`
SELECT
    ms.email_domain AS suffix,
    COALESCE(SUM(CASE
        WHEN `+dotVariant+` AND (
            da.status <> 'normal'
            OR EXISTS (
                SELECT 1 FROM microsoft_allocations history_dot
                WHERE history_dot.dot_alias_id = da.id
                  AND history_dot.project_id = ?
                  AND history_dot.mailbox = 'dot'
            )
        ) THEN 1 ELSE 0 END), 0) AS canonical_unavailable,
    COALESCE(SUM(CASE
        WHEN NOT `+dotVariant+`
         AND `+validDotAlias+`
         AND da.status = 'normal'
         AND NOT EXISTS (
             SELECT 1 FROM microsoft_allocations history_dot
             WHERE history_dot.dot_alias_id = da.id
               AND history_dot.project_id = ?
               AND history_dot.mailbox = 'dot'
         ) THEN 1 ELSE 0 END), 0) AS noncanonical_reusable
FROM dot_aliases da
JOIN microsoft_resources ms ON ms.id = da.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+scope+`
  AND `+allowedRoot+`
GROUP BY ms.email_domain`, append(append([]any{projectID, projectID}, scopeArgs...), allowedRootArgs...)...).Scan(&adjustments).Error
		if err != nil {
			return nil, fmt.Errorf("microsoft dot suffix adjustment: %w", err)
		}
		adjustmentBySuffix := make(map[string]dotSuffixAdjustment, len(adjustments))
		for _, adjustment := range adjustments {
			adjustmentBySuffix[normalizeCandidateSuffix(adjustment.Suffix)] = adjustment
		}
		for suffix, capacity := range dotCapacityBySuffix {
			adjustment := adjustmentBySuffix[suffix]
			result[suffix] += nonNegative(capacity-adjustment.CanonicalUnavailable) + adjustment.NoncanonicalReusable
		}
	}

	if row.PlusWeight > 0 {
		today := time.Now().UTC().Format("2006-01-02")
		plusUsed, err := r.microsoftSuffixCount(ctx, `
SELECT ms.email_domain AS suffix, COALESCE(SUM(adu.used_count), 0) AS count
FROM allocation_daily_usages adu
JOIN microsoft_resources ms ON ms.id = adu.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE adu.usage_date = ?
  AND adu.resource_type = 'microsoft'
  AND adu.usage_kind = 'plus'
  AND ms.status = 'normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+scope+`
  AND `+allowedRoot+`
GROUP BY ms.email_domain`, append(append([]any{today}, scopeArgs...), allowedRootArgs...)...)
		if err != nil {
			return nil, err
		}
		for suffix, limit := range plusLimitBySuffix {
			result[suffix] += nonNegative(limit - plusUsed[suffix])
		}
	}
	return result, nil
}

func (r *Repo) domainProductInventorySuffixTotals(ctx context.Context) ([]allocapp.ProductInventorySuffixTotal, error) {
	scope, scopeArgs := domainInventoryScopeSQL()
	total, err := r.domainInventoryByScope(ctx, scope, scopeArgs, "dr.domain_tld")
	if err != nil {
		return nil, err
	}
	return mergeSuffixInventory(total, total), nil
}

func (r *Repo) ListPrivateMicrosoftInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	row, err := r.enabledProductInventoryRow(ctx, projectID, coredomain.ProductTypeMicrosoft)
	if err != nil || row == nil {
		return nil, err
	}
	scope, scopeArgs := microsoftPrivateProjectInventoryScopeSQL(projectID, buyerUserID)
	totals, err := r.microsoftSuffixInventory(ctx, projectID, *row, scope, scopeArgs)
	if err != nil {
		return nil, err
	}
	return privateProductInventoryTotals(row.ProductID, totals), nil
}

func (r *Repo) ListPrivateDomainInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	row, err := r.enabledProductInventoryRow(ctx, projectID, coredomain.ProductTypeDomain)
	if err != nil || row == nil {
		return nil, err
	}
	scope, scopeArgs := domainPrivateInventoryScopeSQL(buyerUserID)
	totals, err := r.domainInventoryByScope(ctx, scope, scopeArgs, "dr.domain")
	if err != nil {
		return nil, err
	}
	return privateProductInventoryTotals(row.ProductID, totals), nil
}

func (r *Repo) enabledProductInventoryRow(ctx context.Context, projectID uint, productType coredomain.ProductType) (*productInventoryRow, error) {
	var row productInventoryRow
	if err := r.dbFor(ctx).Raw(`
SELECT
    pp.id AS product_id,
    pp.type AS type,
    pp.code_enabled AS code_enabled,
    pp.purchase_enabled AS purchase_enabled,
    pp.main_weight AS main_weight,
    pp.dot_weight AS dot_weight,
    pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
WHERE p.id = ?
  AND p.status = 'listed'
  AND pp.status = 'enabled'
  AND pp.type = ?
LIMIT 1`, projectID, string(productType)).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("load private product inventory row: %w", err)
	}
	if row.ProductID == 0 {
		return nil, nil
	}
	return &row, nil
}

func privateProductInventoryTotals(productID uint, totals map[string]int64) []allocapp.PrivateProductInventoryTotal {
	suffixes := make([]string, 0, len(totals))
	for suffix, available := range totals {
		if suffix != "" && available > 0 {
			suffixes = append(suffixes, suffix)
		}
	}
	sort.Strings(suffixes)
	result := make([]allocapp.PrivateProductInventoryTotal, 0, len(suffixes))
	for _, suffix := range suffixes {
		result = append(result, allocapp.PrivateProductInventoryTotal{
			ProductID: productID,
			Suffix:    suffix,
			Available: totals[suffix],
		})
	}
	return result
}

func (r *Repo) ListPrivateGmailInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]allocapp.PrivateSingletonInventoryTotal, error) {
	var rows []productInventoryRow
	if err := r.dbFor(ctx).Raw(`
SELECT pp.id AS product_id, pp.type AS type
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
WHERE p.id = ?
  AND p.status = 'listed'
  AND pp.status = 'enabled'
  AND pp.type IN ('gmail', 'gmail_variant')
ORDER BY pp.id ASC`, projectID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load private Gmail product inventory rows: %w", err)
	}
	result := make([]allocapp.PrivateSingletonInventoryTotal, 0, len(rows))
	for _, row := range rows {
		available := int64(0)
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeGmail:
			if err := r.dbFor(ctx).Raw(`
SELECT COUNT(*)
FROM gmail_resources gr
JOIN email_resources er ON er.id = gr.id AND er.type = 'gmail'
WHERE gr.status IN ('normal', 'available')
  AND gr.for_sale = FALSE
	  AND er.owner_user_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM gmail_allocations history
      WHERE history.source = 'local'
        AND history.resource_id = gr.id
        AND history.project_id = ?
        AND history.mailbox = 'main'
  )`, buyerUserID, projectID).Scan(&available).Error; err != nil {
				return nil, fmt.Errorf("private Gmail main inventory: %w", err)
			}
		case coredomain.ProductTypeGmailVariant:
			var plusResources int64
			if err := r.dbFor(ctx).Raw(`
SELECT COUNT(*)
FROM gmail_resources gr
JOIN email_resources er ON er.id = gr.id AND er.type = 'gmail'
WHERE gr.status IN ('normal', 'available')
  AND gr.for_sale = FALSE
  AND er.owner_user_id = ?`, buyerUserID).Scan(&plusResources).Error; err != nil {
				return nil, fmt.Errorf("private Gmail plus inventory: %w", err)
			}
			if plusResources > 0 {
				available = allocapp.GmailVariantInventory
			}
		}
		if available > 0 {
			result = append(result, allocapp.PrivateSingletonInventoryTotal{
				ProductID: row.ProductID, ProductType: coredomain.ProductType(row.Type), Available: available,
			})
		}
	}
	return result, nil
}

func (r *Repo) ListPrivateICloudInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) ([]allocapp.PrivateSingletonInventoryTotal, error) {
	var rows []allocapp.PrivateSingletonInventoryTotal
	if err := r.dbFor(ctx).Raw(`
SELECT
	pp.id AS product_id,
	COUNT(ia.id) AS available
FROM project_products pp
JOIN projects p ON p.id = pp.project_id AND p.status = 'listed'
JOIN icloud_aliases ia ON ia.status = 'normal'
JOIN icloud_resources ir ON ir.id = ia.resource_id
JOIN email_resources er ON er.id = ir.id AND er.type = 'icloud'
WHERE pp.project_id = ?
  AND pp.type = 'icloud'
	AND pp.status = 'enabled'
	AND ir.status = 'normal'
	AND er.owner_user_id = ?
	AND ir.for_sale = FALSE
	AND NOT EXISTS (
		SELECT 1 FROM icloud_allocations history
		WHERE history.alias_id = ia.id AND history.project_id = pp.project_id
  )
  AND NOT EXISTS (
		SELECT 1 FROM icloud_allocations active
		WHERE active.alias_id = ia.id
		  AND active.project_id = pp.project_id
		  AND active.status = 'allocated'
	)
GROUP BY pp.id
ORDER BY pp.id`, projectID, buyerUserID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list private iCloud inventory totals: %w", err)
	}
	return rows, nil
}

func (r *Repo) domainInventoryByScope(ctx context.Context, scope string, scopeArgs []any, groupBy string) (map[string]int64, error) {
	var capacities []struct {
		Suffix            string
		MailboxDailyLimit int64
	}
	if err := r.dbFor(ctx).Raw(`
SELECT `+groupBy+` AS suffix, COALESCE(SUM(dr.mailbox_daily_limit), 0) AS mailbox_daily_limit
FROM domain_resources dr
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE dr.status = 'normal'
  AND ms.status = 'online'
  AND `+scope+`
GROUP BY `+groupBy, scopeArgs...).Scan(&capacities).Error; err != nil {
		return nil, fmt.Errorf("domain suffix capacity: %w", err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	used, err := r.domainSuffixCount(ctx, `
SELECT `+groupBy+` AS suffix, COALESCE(SUM(adu.used_count), 0) AS count
FROM allocation_daily_usages adu
JOIN domain_resources dr ON dr.id = adu.resource_id
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE adu.usage_date = ?
  AND adu.resource_type = 'domain'
  AND adu.usage_kind = 'domain_mailbox'
  AND dr.status = 'normal'
  AND ms.status = 'online'
  AND `+scope+`
GROUP BY `+groupBy, append([]any{today}, scopeArgs...)...)
	if err != nil {
		return nil, err
	}
	result := map[string]int64{}
	for _, capacity := range capacities {
		suffix := normalizeCandidateSuffix(capacity.Suffix)
		if suffix == "" {
			continue
		}
		result[suffix] += nonNegative(capacity.MailboxDailyLimit - used[suffix])
	}
	return result, nil
}

func (r *Repo) microsoftSuffixCount(ctx context.Context, query string, args ...any) (map[string]int64, error) {
	rows, err := r.suffixCount(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("microsoft suffix count: %w", err)
	}
	return rows, nil
}

func (r *Repo) domainSuffixCount(ctx context.Context, query string, args ...any) (map[string]int64, error) {
	rows, err := r.suffixCount(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("domain suffix count: %w", err)
	}
	return rows, nil
}

func (r *Repo) suffixCount(ctx context.Context, query string, args ...any) (map[string]int64, error) {
	var rows []struct {
		Suffix string
		Count  int64
	}
	if err := r.dbFor(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		suffix := normalizeCandidateSuffix(row.Suffix)
		if suffix == "" {
			continue
		}
		result[suffix] += row.Count
	}
	return result, nil
}

func mergeSuffixInventory(total map[string]int64, public map[string]int64) []allocapp.ProductInventorySuffixTotal {
	merged := make(map[string]suffixInventoryValue, len(total)+len(public))
	for suffix, available := range total {
		value := merged[suffix]
		value.TotalAvailable = available
		merged[suffix] = value
	}
	for suffix, available := range public {
		value := merged[suffix]
		value.PublicAvailable = available
		if value.TotalAvailable < available {
			value.TotalAvailable = available
		}
		merged[suffix] = value
	}
	items := make([]allocapp.ProductInventorySuffixTotal, 0, len(merged))
	for suffix, value := range merged {
		if value.TotalAvailable <= 0 && value.PublicAvailable <= 0 {
			continue
		}
		items = append(items, allocapp.ProductInventorySuffixTotal{
			Suffix:          suffix,
			TotalAvailable:  value.TotalAvailable,
			PublicAvailable: value.PublicAvailable,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TotalAvailable == items[j].TotalAvailable {
			return items[i].Suffix < items[j].Suffix
		}
		return items[i].TotalAvailable > items[j].TotalAvailable
	})
	return items
}

func (r *Repo) findByGuard(ctx context.Context, guard OrderGuardModel) (*domain.UnifiedAllocation, error) {
	switch domain.AllocationType(guard.Type) {
	case domain.AllocationTypeMicrosoft:
		var model MicrosoftAllocationModel
		if err := r.dbFor(ctx).Where("order_no = ?", guard.OrderNo).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("find microsoft allocation by guard: %w", err)
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeDomain:
		var model DomainAllocationModel
		if err := r.dbFor(ctx).Where("order_no = ?", guard.OrderNo).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("find domain allocation by guard: %w", err)
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeGmail:
		var model GmailAllocationModel
		if err := r.dbFor(ctx).
			Where("order_no = ? AND source = ? AND resource_id IS NOT NULL AND project_id IS NOT NULL AND product_id IS NOT NULL", guard.OrderNo, gmailLocalSource).
			First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("find Gmail allocation by guard: %w", err)
		}
		result := model.unified()
		return &result, nil
	case domain.AllocationTypeICloud:
		var model ICloudAllocationModel
		if err := r.dbFor(ctx).Where("order_no = ?", guard.OrderNo).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("find iCloud allocation by guard: %w", err)
		}
		result := model.unified()
		return &result, nil
	default:
		return nil, domain.ErrAllocationNotFound
	}
}

type unifiedRow struct {
	Type        string
	ID          uint
	OrderNo     string
	ProjectID   uint
	ProductID   uint
	ResourceID  uint
	SupplyScope string
	Mailbox     string
	Email       string
	Status      string
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

func (r *Repo) queryUnifiedAllocations(ctx context.Context, filter allocapp.AllocationFilter, paginate bool) ([]domain.UnifiedAllocation, int64, error) {
	selects := []string{}
	args := []any{}
	addSelect := func(table, typ, mailboxExpr string, conditions []string, condArgs []any) {
		selects = append(selects, fmt.Sprintf(`SELECT '%s' AS type, id, order_no, project_id, product_id, resource_id, supply_scope, %s AS mailbox, email, status, created_at, released_at FROM %s WHERE %s`, typ, mailboxExpr, table, strings.Join(conditions, " AND ")))
		args = append(args, condArgs...)
	}
	if filter.Type == "" || filter.Type == domain.AllocationTypeMicrosoft {
		conditions, condArgs := allocationConditions(filter)
		if filter.Mailbox != "" {
			conditions = append(conditions, "mailbox = ?")
			condArgs = append(condArgs, filter.Mailbox)
		}
		addSelect("microsoft_allocations", string(domain.AllocationTypeMicrosoft), "mailbox", conditions, condArgs)
	}
	if filter.Type == "" || filter.Type == domain.AllocationTypeDomain {
		if filter.Mailbox == "" || filter.Mailbox == "domain" {
			conditions, condArgs := allocationConditions(filter)
			addSelect("domain_allocations", string(domain.AllocationTypeDomain), "'domain'", conditions, condArgs)
		}
	}
	if filter.Type == "" || filter.Type == domain.AllocationTypeGmail {
		if filter.Mailbox == "" || domain.IsValidGmailMailbox(domain.GmailMailbox(filter.Mailbox)) {
			conditions, condArgs := allocationConditions(filter)
			conditions = append(conditions, "source = 'local'", "resource_id IS NOT NULL", "project_id IS NOT NULL", "product_id IS NOT NULL")
			if filter.Mailbox != "" {
				conditions = append(conditions, "mailbox = ?")
				condArgs = append(condArgs, filter.Mailbox)
			}
			addSelect("gmail_allocations", string(domain.AllocationTypeGmail), "mailbox", conditions, condArgs)
		}
	}
	if filter.Type == "" || filter.Type == domain.AllocationTypeICloud {
		if filter.Mailbox == "" || filter.Mailbox == "alias" {
			conditions, condArgs := allocationConditions(filter)
			addSelect("icloud_allocations", string(domain.AllocationTypeICloud), "'alias'", conditions, condArgs)
		}
	}
	if len(selects) == 0 {
		return []domain.UnifiedAllocation{}, 0, nil
	}
	unionSQL := strings.Join(selects, " UNION ALL ")
	var total int64
	if err := r.dbFor(ctx).Raw("SELECT COUNT(*) FROM ("+unionSQL+") AS allocations", args...).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count allocations: %w", err)
	}
	queryArgs := append([]any{}, args...)
	query := "SELECT * FROM (" + unionSQL + ") AS allocations ORDER BY created_at DESC, id DESC"
	if paginate {
		query += " LIMIT ? OFFSET ?"
		queryArgs = append(queryArgs, filter.Limit, filter.Offset)
	}
	var rows []unifiedRow
	if err := r.dbFor(ctx).Raw(query, queryArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list allocations: %w", err)
	}
	items := make([]domain.UnifiedAllocation, len(rows))
	for i := range rows {
		items[i] = domain.UnifiedAllocation{
			Type:        domain.AllocationType(rows[i].Type),
			ID:          rows[i].ID,
			OrderNo:     rows[i].OrderNo,
			ProjectID:   rows[i].ProjectID,
			ProductID:   rows[i].ProductID,
			ResourceID:  rows[i].ResourceID,
			SupplyScope: domain.NormalizeSupplyScope(domain.SupplyScope(rows[i].SupplyScope)),
			Mailbox:     rows[i].Mailbox,
			Email:       rows[i].Email,
			Status:      domain.AllocationStatus(rows[i].Status),
			CreatedAt:   rows[i].CreatedAt,
			ReleasedAt:  rows[i].ReleasedAt,
		}
	}
	return items, total, nil
}

func allocationConditions(filter allocapp.AllocationFilter) ([]string, []any) {
	conditions := []string{"1 = 1"}
	args := []any{}
	if filter.OrderNo != "" {
		conditions = append(conditions, "order_no = ?")
		args = append(args, filter.OrderNo)
	}
	if len(filter.OrderNos) > 0 {
		conditions = append(conditions, "order_no IN ?")
		args = append(args, filter.OrderNos)
	}
	if filter.ProjectID > 0 {
		conditions = append(conditions, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if filter.ResourceID > 0 {
		conditions = append(conditions, "resource_id = ?")
		args = append(args, filter.ResourceID)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	return conditions, args
}

func isValidDailyUsageKind(kind domain.DailyUsageKind) bool {
	return kind == domain.DailyUsageKindPlus || kind == domain.DailyUsageKindDomainMailbox
}

func microsoftInventoryScopeSQL() (string, []any) {
	publicScope := "(ms.for_sale = TRUE AND u.status = 'active' AND u.role IN ('supplier', 'admin', 'super_admin'))"
	return publicScope, nil
}

func microsoftProjectInventoryScopeSQL(projectID uint) (string, []any) {
	scope, args := microsoftInventoryScopeSQL()
	return "(" + scope + ") AND " + microsoftProjectUnmatchedCondition, append(args, projectID)
}

func microsoftPrivateProjectInventoryScopeSQL(projectID uint, buyerUserID uint) (string, []any) {
	return "(ms.for_sale = FALSE AND er.owner_user_id = ?) AND " + microsoftProjectUnmatchedCondition, []any{buyerUserID, projectID}
}

func microsoftDotCapacityExpression(tableAlias string) string {
	return dotCapacityExpression("SUBSTRING_INDEX(" + tableAlias + ".email_address, '@', 1)")
}

func gmailDotCapacityExpression(tableAlias string) string {
	localPart := "REPLACE(SUBSTRING_INDEX(" + tableAlias + ".email, '@', 1), '.', '')"
	return fmt.Sprintf(
		"(CASE WHEN CHAR_LENGTH(%s) BETWEEN 2 AND %d THEN CAST(POWER(2, CHAR_LENGTH(%s) - 1) AS UNSIGNED) - 1 ELSE 0 END)",
		localPart, allocapp.GmailDotMaxLocalCharacters, localPart,
	)
}

func gmailDotCandidateCapacityExpression(db *gorm.DB, tableAlias string) string {
	if db.Name() != "sqlite" {
		return gmailDotCapacityExpression(tableAlias)
	}
	localPart := "REPLACE(SUBSTR(" + tableAlias + ".email, 1, INSTR(" + tableAlias + ".email, '@') - 1), '.', '')"
	return fmt.Sprintf(
		"(CASE WHEN LENGTH(%s) BETWEEN 2 AND %d THEN (1 << (LENGTH(%s) - 1)) - 1 ELSE 0 END)",
		localPart, allocapp.GmailDotMaxLocalCharacters, localPart,
	)
}

func dotCapacityExpression(localPart string) string {
	capacity := allocapp.DotAliasCapacityPerResourceValue()
	positions := make([]string, 0, capacity)
	for position := 1; position <= capacity; position++ {
		positions = append(positions, fmt.Sprintf(
			"CASE WHEN CHAR_LENGTH(%s) > %d AND SUBSTRING(%s, %d, 1) <> '.' AND SUBSTRING(%s, %d, 1) <> '.' THEN 1 ELSE 0 END",
			localPart, position, localPart, position, localPart, position+1,
		))
	}
	return "(" + strings.Join(positions, " + ") + ")"
}

func microsoftDotAliasMatchesResourceExpression(aliasTable, resourceTable string) string {
	localPart := "SUBSTRING_INDEX(" + resourceTable + ".email_address, '@', 1)"
	domainPart := "SUBSTRING_INDEX(" + resourceTable + ".email_address, '@', -1)"
	capacity := allocapp.DotAliasCapacityPerResourceValue()
	conditions := make([]string, 0, capacity)
	for position := 1; position <= capacity; position++ {
		conditions = append(conditions, fmt.Sprintf(
			"(CHAR_LENGTH(%s) > %d AND SUBSTRING(%s, %d, 1) <> '.' AND SUBSTRING(%s, %d, 1) <> '.' AND %s.email = CONCAT(LEFT(%s, %d), '.', SUBSTRING(%s, %d), '@', %s))",
			localPart, position, localPart, position, localPart, position+1, aliasTable, localPart, position, localPart, position+1, domainPart,
		))
	}
	return "(" + strings.Join(conditions, " OR ") + ")"
}

func microsoftDotAliasValidForResourceExpression(aliasTable, resourceTable string) string {
	aliasLocal := "SUBSTRING_INDEX(" + aliasTable + ".email, '@', 1)"
	resourceLocal := "SUBSTRING_INDEX(" + resourceTable + ".email_address, '@', 1)"
	aliasDomain := "SUBSTRING_INDEX(" + aliasTable + ".email, '@', -1)"
	resourceDomain := "SUBSTRING_INDEX(" + resourceTable + ".email_address, '@', -1)"
	return "(" + aliasTable + ".email <> " + resourceTable + ".email_address" +
		" AND " + aliasDomain + " = " + resourceDomain +
		" AND REPLACE(" + aliasLocal + ", '.', '') = REPLACE(" + resourceLocal + ", '.', '')" +
		" AND LEFT(" + aliasLocal + ", 1) <> '.'" +
		" AND RIGHT(" + aliasLocal + ", 1) <> '.'" +
		" AND LOCATE('..', " + aliasLocal + ") = 0)"
}

func domainInventoryScopeSQL() (string, []any) {
	publicScope := "(dr.purpose = 'sale' AND u.status = 'active' AND u.role IN ('supplier', 'admin', 'super_admin'))"
	return publicScope, nil
}

func domainPrivateInventoryScopeSQL(buyerUserID uint) (string, []any) {
	return "(dr.purpose = 'not_sale' AND dr.owner_user_id = ?)", []any{buyerUserID}
}

func normalizeCandidateSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "@")
	return strings.TrimSuffix(strings.TrimPrefix(value, "."), ".")
}

func domainResourceSelectionCondition(value string) (string, []any) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "@") {
		_, host, _ := strings.Cut(value, "@")
		return "dr.domain = ?", []any{host}
	}
	if suffix, err := coredomain.NormalizeDomainTLD(value); err == nil {
		return "dr.domain_tld = ?", []any{"." + suffix}
	}
	if host, err := coredomain.NormalizeDomainName(normalizeCandidateSuffix(value)); err == nil {
		return "dr.domain = ?", []any{host}
	}
	return "1 = 0", nil
}

func generatedMailboxSelectionCondition(value string) (string, []any) {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "@") {
		return "gm.email = ?", []any{value}
	}
	return domainResourceSelectionCondition(value)
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func findAllocationError(err error, action string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrAllocationNotFound
	}
	return fmt.Errorf("%s: %w", action, err)
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isForeignKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1452
}

func isDeadlockError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func isWholeTransactionRollbackError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func mysqlRetryEvent(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1205 {
		return "1205"
	}
	return "1213"
}

func deadlockBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	base := time.Duration(10*(1<<attempt)) * time.Millisecond
	jitter := time.Duration(rand.IntN(25+attempt*10)) * time.Millisecond
	return base + jitter
}
