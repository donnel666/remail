package infra

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
	systemLogs    *governanceinfra.SystemLogRepo
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		systemLogs:    governanceinfra.NewSystemLogRepo(db),
	}
}

func (r *Repo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		name := fmt.Sprintf("alloc_sp_%d", rand.Uint64())
		if err := tx.WithContext(ctx).SavePoint(name).Error; err != nil {
			return fmt.Errorf("create allocation savepoint: %w", err)
		}
		if err := fn(ctx); err != nil {
			if rollbackErr := tx.WithContext(ctx).RollbackTo(name).Error; rollbackErr != nil {
				return fmt.Errorf("rollback allocation savepoint: %w: %v", err, rollbackErr)
			}
			return err
		}
		return nil
	}
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(platform.WithGormTx(ctx, tx))
		})
		if err == nil || !isDeadlockError(err) {
			return err
		}
		time.Sleep(deadlockBackoff(attempt))
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
		Type:       domain.AllocationTypeMicrosoft,
		ID:         m.ID,
		OrderNo:    m.OrderNo,
		ProjectID:  m.ProjectID,
		ProductID:  m.ProductID,
		ResourceID: m.ResourceID,
		Mailbox:    m.Mailbox,
		Email:      m.Email,
		Status:     domain.AllocationStatus(m.Status),
		CreatedAt:  m.CreatedAt,
		ReleasedAt: m.ReleasedAt,
	}
}

type DomainAllocationModel struct {
	ID         uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo    string     `gorm:"type:varchar(64);not null;column:order_no"`
	ProjectID  uint       `gorm:"not null;column:project_id"`
	ProductID  uint       `gorm:"not null;column:product_id"`
	ResourceID uint       `gorm:"not null;column:resource_id"`
	MailboxID  uint       `gorm:"not null;column:mailbox_id"`
	Email      string     `gorm:"type:varchar(255);not null"`
	Status     string     `gorm:"type:varchar(32);not null;default:'allocated'"`
	CreatedAt  time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	ReleasedAt *time.Time `gorm:"column:released_at"`
}

func (DomainAllocationModel) TableName() string { return "domain_allocations" }

func domainAllocationFromDomain(allocation *domain.GeneratedMailboxAllocation) *DomainAllocationModel {
	return &DomainAllocationModel{
		ID:         allocation.ID,
		OrderNo:    allocation.OrderNo,
		ProjectID:  allocation.ProjectID,
		ProductID:  allocation.ProductID,
		ResourceID: allocation.ResourceID,
		MailboxID:  allocation.MailboxID,
		Email:      strings.ToLower(strings.TrimSpace(allocation.Email)),
		Status:     string(allocation.Status),
		CreatedAt:  allocation.CreatedAt,
		ReleasedAt: allocation.ReleasedAt,
	}
}

func (m DomainAllocationModel) toDomain() domain.GeneratedMailboxAllocation {
	return domain.GeneratedMailboxAllocation{
		ID:         m.ID,
		OrderNo:    m.OrderNo,
		ProjectID:  m.ProjectID,
		ProductID:  m.ProductID,
		ResourceID: m.ResourceID,
		MailboxID:  m.MailboxID,
		Email:      m.Email,
		Status:     domain.AllocationStatus(m.Status),
		CreatedAt:  m.CreatedAt,
		ReleasedAt: m.ReleasedAt,
	}
}

func (m DomainAllocationModel) unified() domain.UnifiedAllocation {
	return domain.UnifiedAllocation{
		Type:       domain.AllocationTypeDomain,
		ID:         m.ID,
		OrderNo:    m.OrderNo,
		ProjectID:  m.ProjectID,
		ProductID:  m.ProductID,
		ResourceID: m.ResourceID,
		Mailbox:    "domain",
		Email:      m.Email,
		Status:     domain.AllocationStatus(m.Status),
		CreatedAt:  m.CreatedAt,
		ReleasedAt: m.ReleasedAt,
	}
}

type ExplicitAliasModel struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	ResourceID uint      `gorm:"column:resource_id"`
	Email      string    `gorm:"type:varchar(255);not null"`
	Status     string    `gorm:"type:varchar(32);not null"`
	CreatedAt  time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt  time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ExplicitAliasModel) TableName() string { return "explicit_aliases" }

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
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
}

func (GeneratedMailboxModel) TableName() string { return "generated_mailboxes" }

type RoutingCandidateModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ProjectID       uint       `gorm:"column:project_id"`
	ResourceID      uint       `gorm:"column:resource_id"`
	EmailAddress    string     `gorm:"column:email_address"`
	DomainSuffix    string     `gorm:"column:domain_suffix"`
	ForSale         bool       `gorm:"column:for_sale"`
	QualityScore    int        `gorm:"column:quality_score"`
	Status          string     `gorm:"column:status"`
	Bucket          uint8      `gorm:"column:alloc_bucket"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (RoutingCandidateModel) TableName() string { return "microsoft_routing_candidates" }

type DomainRoutingCandidateModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ProjectID       uint       `gorm:"column:project_id"`
	ResourceID      uint       `gorm:"column:resource_id"`
	Domain          string     `gorm:"column:domain"`
	DomainTLD       string     `gorm:"column:domain_tld"`
	Purpose         string     `gorm:"column:purpose"`
	Status          string     `gorm:"column:status"`
	Bucket          uint8      `gorm:"column:alloc_bucket"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (DomainRoutingCandidateModel) TableName() string { return "domain_routing_candidates" }

type CandidateRefreshJobModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	ProjectID      uint       `gorm:"not null;column:project_id"`
	OperatorUserID uint       `gorm:"not null;column:operator_user_id"`
	Status         string     `gorm:"type:varchar(32);not null;default:'pending'"`
	Affected       int        `gorm:"not null;default:0"`
	Attempts       int        `gorm:"not null;default:0"`
	MaxAttempts    int        `gorm:"not null;default:1;column:max_attempts"`
	LastSafeError  string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID      string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path           string     `gorm:"type:varchar(255);not null;default:''"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	FinishedAt     *time.Time `gorm:"column:finished_at"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (CandidateRefreshJobModel) TableName() string { return "allocation_candidate_refresh_jobs" }

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

func candidateRefreshJobModel(job *domain.CandidateRefreshJob) *CandidateRefreshJobModel {
	return &CandidateRefreshJobModel{
		ID:             job.ID,
		ProjectID:      job.ProjectID,
		OperatorUserID: job.OperatorUserID,
		Status:         string(job.Status),
		Affected:       job.Affected,
		Attempts:       job.Attempts,
		MaxAttempts:    normalizeCandidateRefreshMaxAttempts(job.MaxAttempts),
		LastSafeError:  safeCandidateRefreshMessage(job.LastSafeError),
		RequestID:      strings.TrimSpace(job.RequestID),
		Path:           strings.TrimSpace(job.Path),
		StartedAt:      job.StartedAt,
		FinishedAt:     job.FinishedAt,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}
}

func (m CandidateRefreshJobModel) toDomain() domain.CandidateRefreshJob {
	return domain.CandidateRefreshJob{
		ID:             m.ID,
		ProjectID:      m.ProjectID,
		OperatorUserID: m.OperatorUserID,
		Status:         domain.CandidateRefreshStatus(m.Status),
		Affected:       m.Affected,
		Attempts:       m.Attempts,
		MaxAttempts:    normalizeCandidateRefreshMaxAttempts(m.MaxAttempts),
		LastSafeError:  m.LastSafeError,
		RequestID:      m.RequestID,
		Path:           m.Path,
		StartedAt:      m.StartedAt,
		FinishedAt:     m.FinishedAt,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

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

func (r *Repo) LoadProductConfig(ctx context.Context, productID uint, buyerUserID uint) (*allocapp.ProductAllocationConfig, error) {
	type row struct {
		ProjectID  uint
		ProductID  uint
		Type       string
		MainWeight int
		DotWeight  int
		PlusWeight int
	}
	var item row
	err := r.dbFor(ctx).Raw(`
SELECT
    pp.project_id AS project_id,
    pp.id AS product_id,
    pp.type AS type,
    pp.main_weight AS main_weight,
    pp.dot_weight AS dot_weight,
    pp.plus_weight AS plus_weight
FROM project_products pp
JOIN projects p ON p.id = pp.project_id
WHERE pp.id = ?
  AND pp.status = 'enabled'
  AND p.status = 'listed'
  AND (
      p.access_type = 'public'
      OR EXISTS (
          SELECT 1 FROM project_accesses pa
          WHERE pa.project_id = p.id AND pa.user_id = ?
      )
  )
LIMIT 1`, productID, buyerUserID).Scan(&item).Error
	if err != nil {
		return nil, fmt.Errorf("load product config: %w", err)
	}
	if item.ProductID == 0 {
		return nil, nil
	}
	allocationType := domain.AllocationType(item.Type)
	if !domain.IsValidAllocationType(allocationType) {
		return nil, domain.ErrProjectNotAllocatable
	}
	return &allocapp.ProductAllocationConfig{
		ProjectID:   item.ProjectID,
		ProductID:   item.ProductID,
		ProductType: allocationType,
		MainWeight:  item.MainWeight,
		DotWeight:   item.DotWeight,
		PlusWeight:  item.PlusWeight,
	}, nil
}

func (r *Repo) ListMicrosoftSourceCandidates(ctx context.Context, buyerUserID uint, scope domain.SupplyScope, bucket *uint8, limit int, emailSuffix string) ([]allocapp.MicrosoftCandidate, error) {
	args := []any{}
	where := []string{"ms.status = 'normal'"}
	if suffix := normalizeCandidateSuffix(emailSuffix); suffix != "" {
		where = append(where, "ms.email_domain = ?")
		args = append(args, suffix)
	}
	switch scope {
	case domain.SupplyScopeOwned:
		where = append(where, "ms.for_sale = FALSE", "er.owner_user_id = ?")
		args = append(args, buyerUserID)
	default:
		where = append(where, "ms.for_sale = TRUE", "u.enabled = TRUE", "u.role_level IN (20, 80, 100)")
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

func (r *Repo) ListDomainSourceCandidates(ctx context.Context, bucket *uint8, limit int, emailSuffix string) ([]allocapp.DomainCandidate, error) {
	args := []any{}
	where := []string{
		"dr.purpose = 'sale'",
		"dr.status = 'normal'",
		"ms.status = 'online'",
		"u.enabled = TRUE",
		"u.role_level IN (20, 80, 100)",
	}
	if suffix := normalizeCandidateSuffix(emailSuffix); suffix != "" {
		where = append(where, "dr.domain = ?")
		args = append(args, suffix)
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

func (r *Repo) LockMicrosoftCandidate(ctx context.Context, resourceID uint, buyerUserID uint, scope domain.SupplyScope, emailSuffix string) (*allocapp.MicrosoftCandidate, error) {
	args := []any{resourceID}
	where := []string{
		"ms.id = ?",
		"ms.status = 'normal'",
	}
	if suffix := normalizeCandidateSuffix(emailSuffix); suffix != "" {
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
                  AND u.enabled = TRUE
                  AND u.role_level IN (20, 80, 100)
            )`,
		)
	}
	query := `
SELECT ms.id AS resource_id, ms.email_address AS email_address, ms.quality_score AS quality_score, ms.plus_daily_limit AS plus_daily_limit
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

func (r *Repo) LockDomainCandidate(ctx context.Context, resourceID uint, emailSuffix string) (*allocapp.DomainCandidate, error) {
	args := []any{resourceID}
	where := []string{
		"dr.id = ?",
		"dr.purpose = 'sale'",
		"dr.status = 'normal'",
		`EXISTS (
	      SELECT 1
	      FROM mail_servers ms
	      WHERE ms.id = dr.mail_server_id
	        AND ms.status = 'online'
	  )`,
		`EXISTS (
	      SELECT 1
	      FROM email_resources er
	      JOIN users u ON u.id = er.owner_user_id
	      WHERE er.id = dr.id
	        AND er.type = 'domain'
	        AND u.enabled = TRUE
	        AND u.role_level IN (20, 80, 100)
	  )`,
	}
	if suffix := normalizeCandidateSuffix(emailSuffix); suffix != "" {
		where = append(where, "dr.domain = ?")
		args = append(args, suffix)
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

func (r *Repo) FindReusableExplicitAlias(ctx context.Context, resourceID uint) (*allocapp.AliasCandidate, error) {
	var candidate allocapp.AliasCandidate
	err := r.dbFor(ctx).Raw(`
SELECT ea.id AS id, ea.email AS email
FROM explicit_aliases ea
WHERE ea.resource_id = ?
  AND ea.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM microsoft_allocations ma
      WHERE ma.explicit_alias_id = ea.id
        AND ma.mailbox = 'alias'
        AND ma.status = 'allocated'
  )
ORDER BY ea.id ASC
LIMIT 1`, resourceID).Scan(&candidate).Error
	if err != nil {
		return nil, fmt.Errorf("find reusable explicit alias: %w", err)
	}
	if candidate.ID == 0 {
		return nil, nil
	}

	var locked allocapp.AliasCandidate
	err = r.dbFor(ctx).Raw(`
SELECT ea.id AS id, ea.email AS email
FROM explicit_aliases ea
WHERE ea.id = ?
  AND ea.resource_id = ?
  AND ea.status = 'normal'
LIMIT 1
FOR UPDATE SKIP LOCKED`, candidate.ID, resourceID).Scan(&locked).Error
	if err != nil {
		return nil, fmt.Errorf("lock reusable explicit alias: %w", err)
	}
	if locked.ID == 0 {
		return nil, nil
	}
	return &locked, nil
}

func (r *Repo) FindReusableDotAlias(ctx context.Context, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	return r.findReusableProjectAlias(ctx, "dot_aliases", "dot_alias_id", "dot", projectID, resourceID)
}

func (r *Repo) FindReusablePlusAlias(ctx context.Context, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	return r.findReusableProjectAlias(ctx, "plus_aliases", "plus_alias_id", "plus", projectID, resourceID)
}

func (r *Repo) findReusableProjectAlias(ctx context.Context, table, column, mailbox string, projectID uint, resourceID uint) (*allocapp.AliasCandidate, error) {
	var candidate allocapp.AliasCandidate
	query := fmt.Sprintf(`
SELECT a.id AS id, a.email AS email
FROM %s a
WHERE a.resource_id = ?
  AND a.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM microsoft_allocations ma
      WHERE ma.%s = a.id
        AND ma.project_id = ?
        AND ma.mailbox = ?
        AND ma.status = 'allocated'
  )
ORDER BY a.id ASC
LIMIT 1`, table, column)
	if err := r.dbFor(ctx).Raw(query, resourceID, projectID, mailbox).Scan(&candidate).Error; err != nil {
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
LIMIT 1
FOR UPDATE SKIP LOCKED`, table)
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
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if !isDuplicateKeyError(err) {
			return nil, fmt.Errorf("create dot alias: %w", err)
		}
	}
	var found DotAliasModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find dot alias: %w", err)
	}
	return &allocapp.AliasCandidate{ID: found.ID, Email: found.Email}, nil
}

func (r *Repo) FindOrCreatePlusAlias(ctx context.Context, resourceID uint, email string) (*allocapp.AliasCandidate, error) {
	model := PlusAliasModel{ResourceID: resourceID, Email: strings.ToLower(strings.TrimSpace(email)), Status: "normal"}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if !isDuplicateKeyError(err) {
			return nil, fmt.Errorf("create plus alias: %w", err)
		}
	}
	var found PlusAliasModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find plus alias: %w", err)
	}
	return &allocapp.AliasCandidate{ID: found.ID, Email: found.Email}, nil
}

func (r *Repo) FindReusableGeneratedMailbox(ctx context.Context, projectID uint, resourceID uint) (*allocapp.GeneratedMailboxCandidate, error) {
	var candidate allocapp.GeneratedMailboxCandidate
	if err := r.dbFor(ctx).Raw(`
SELECT gm.id AS id, gm.email AS email
FROM generated_mailboxes gm
WHERE gm.resource_id = ?
  AND gm.status = 'normal'
  AND NOT EXISTS (
      SELECT 1
      FROM domain_allocations da
      WHERE da.mailbox_id = gm.id
        AND da.project_id = ?
        AND da.status = 'allocated'
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
SELECT gm.id AS id, gm.email AS email
FROM generated_mailboxes gm
WHERE gm.id = ?
  AND gm.resource_id = ?
  AND gm.status = 'normal'
LIMIT 1
FOR UPDATE SKIP LOCKED`, candidate.ID, resourceID).Scan(&locked).Error; err != nil {
		return nil, fmt.Errorf("lock reusable generated mailbox: %w", err)
	}
	if locked.ID == 0 {
		return nil, nil
	}
	return &locked, nil
}

func (r *Repo) FindOrCreateGeneratedMailbox(ctx context.Context, resourceID uint, ownerUserID uint, email string) (*allocapp.GeneratedMailboxCandidate, error) {
	model := GeneratedMailboxModel{
		ResourceID:  resourceID,
		OwnerUserID: ownerUserID,
		Email:       strings.ToLower(strings.TrimSpace(email)),
		Status:      "normal",
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if !isDuplicateKeyError(err) {
			return nil, fmt.Errorf("create generated mailbox: %w", err)
		}
	}
	var found GeneratedMailboxModel
	if err := r.dbFor(ctx).Where("resource_id = ? AND email = ?", resourceID, model.Email).First(&found).Error; err != nil {
		return nil, fmt.Errorf("find generated mailbox: %w", err)
	}
	return &allocapp.GeneratedMailboxCandidate{ID: found.ID, Email: found.Email}, nil
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

func (r *Repo) TouchMicrosoftAllocated(ctx context.Context, projectID uint, resourceID uint, allocatedAt time.Time) error {
	db := r.dbFor(ctx)
	if err := db.Model(&struct{}{}).Table("microsoft_resources").
		Where("id = ?", resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch microsoft allocated: %w", err)
	}
	if err := db.Model(&RoutingCandidateModel{}).
		Where("project_id = ? AND resource_id = ?", projectID, resourceID).
		Updates(map[string]any{"last_allocated_at": allocatedAt}).Error; err != nil {
		return fmt.Errorf("touch microsoft candidate allocated: %w", err)
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
	result := make([]domain.UnifiedAllocation, 0, len(ms)+len(ds))
	for _, item := range ms {
		result = append(result, item.unified())
	}
	for _, item := range ds {
		result = append(result, item.unified())
	}
	return result, nil
}

func (r *Repo) GetInventoryStats(ctx context.Context, projectID uint, buyerUserID uint) (*allocapp.InventoryStats, error) {
	stats := &allocapp.InventoryStats{ProjectID: projectID}
	var productRows []struct {
		Type       string
		MainWeight int
		DotWeight  int
		PlusWeight int
	}
	if err := r.dbFor(ctx).Raw(`
SELECT pp.type AS type, pp.main_weight AS main_weight, pp.dot_weight AS dot_weight, pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
WHERE p.id = ?
  AND p.status = 'listed'
  AND pp.status = 'enabled'
  AND (
      ? = 0
      OR p.access_type = 'public'
      OR EXISTS (
          SELECT 1
          FROM project_accesses pa
          WHERE pa.project_id = p.id AND pa.user_id = ?
      )
  )`, projectID, buyerUserID, buyerUserID).Scan(&productRows).Error; err != nil {
		return nil, fmt.Errorf("load inventory project products: %w", err)
	}
	if len(productRows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, row := range productRows {
		switch domain.AllocationType(row.Type) {
		case domain.AllocationTypeMicrosoft:
			stats.Microsoft.Enabled = true
			stats.Microsoft.MainEnabled = row.MainWeight > 0
			stats.Microsoft.DotEnabled = row.DotWeight > 0
			stats.Microsoft.PlusEnabled = row.PlusWeight > 0
		case domain.AllocationTypeDomain:
			stats.Domain.Enabled = true
		}
	}
	scan := func(target any, query string, args ...any) error {
		if err := r.dbFor(ctx).Raw(query, args...).Scan(target).Error; err != nil {
			return fmt.Errorf("inventory stats: %w", err)
		}
		return nil
	}
	if stats.Microsoft.Enabled {
		microsoftScope, microsoftScopeArgs := microsoftInventoryScopeSQL(buyerUserID)
		var capacity struct {
			EligibleResources int64
			PlusDailyLimit    int64
		}
		if err := scan(&capacity, `
SELECT COUNT(*) AS eligible_resources, COALESCE(SUM(ms.plus_daily_limit), 0) AS plus_daily_limit
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND `+microsoftScope, microsoftScopeArgs...); err != nil {
			return nil, err
		}
		stats.Microsoft.EligibleResources = capacity.EligibleResources
		stats.Microsoft.PlusDailyLimit = capacity.PlusDailyLimit
		if stats.Microsoft.MainEnabled {
			var activeMain int64
			if err := scan(&activeMain, `
SELECT COUNT(*)
FROM microsoft_allocations ma
JOIN microsoft_resources ms ON ms.id = ma.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ma.status = 'allocated'
  AND ma.mailbox = 'main'
  AND ms.status = 'normal'
  AND `+microsoftScope, microsoftScopeArgs...); err != nil {
				return nil, err
			}
			stats.Microsoft.MainAvailable = nonNegative(capacity.EligibleResources - activeMain)
			if err := scan(&stats.Microsoft.ExplicitAliasAvailable, `
SELECT COUNT(*)
FROM explicit_aliases ea
JOIN microsoft_resources ms ON ms.id = ea.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
LEFT JOIN microsoft_allocations ma
  ON ma.explicit_alias_id = ea.id
 AND ma.mailbox = 'alias'
 AND ma.status = 'allocated'
WHERE ea.status = 'normal'
  AND ma.id IS NULL
  AND ms.status = 'normal'
  AND `+microsoftScope, microsoftScopeArgs...); err != nil {
				return nil, err
			}
		}
		if stats.Microsoft.DotEnabled {
			stats.Microsoft.DotCapacity = capacity.EligibleResources * int64(allocapp.DotAliasCapacityPerResource)
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
  AND `+microsoftScope, append([]any{projectID}, microsoftScopeArgs...)...); err != nil {
				return nil, err
			}
			stats.Microsoft.DotAvailable = nonNegative(stats.Microsoft.DotCapacity - stats.Microsoft.ActiveDotAllocations)
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
  AND `+microsoftScope, append([]any{today}, microsoftScopeArgs...)...); err != nil {
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
		if err := scan(&capacity, `
SELECT COUNT(*) AS eligible_resources, COALESCE(SUM(dr.mailbox_daily_limit), 0) AS mailbox_daily_limit
FROM domain_resources dr
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE dr.purpose = 'sale' AND dr.status = 'normal' AND ms.status = 'online' AND u.enabled = TRUE AND u.role_level IN (20, 80, 100)`); err != nil {
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
  AND dr.purpose = 'sale'
  AND dr.status = 'normal'
  AND ms.status = 'online'
  AND u.enabled = TRUE
  AND u.role_level IN (20, 80, 100)`, today); err != nil {
			return nil, err
		}
		stats.Domain.MailboxDailyAvailable = nonNegative(stats.Domain.MailboxDailyLimit - stats.Domain.MailboxDailyUsed)
		stats.Domain.TotalAvailable = stats.Domain.MailboxDailyAvailable
	}
	if err := scan(&stats.ActiveMicrosoftAllocations, `SELECT COUNT(*) FROM microsoft_allocations WHERE project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	if err := scan(&stats.ActiveDomainAllocations, `SELECT COUNT(*) FROM domain_allocations WHERE project_id = ? AND status = 'allocated'`, projectID); err != nil {
		return nil, err
	}
	stats.TotalAvailable = stats.Microsoft.TotalAvailable + stats.Domain.TotalAvailable
	return stats, nil
}

func (r *Repo) GetProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	var productRows []struct {
		ProductID  uint
		Type       string
		MainWeight int
		DotWeight  int
		PlusWeight int
	}
	if err := r.dbFor(ctx).Raw(`
SELECT
    pp.id AS product_id,
    pp.type AS type,
    pp.main_weight AS main_weight,
    pp.dot_weight AS dot_weight,
    pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
WHERE p.id = ?
  AND p.status = 'listed'
  AND pp.status = 'enabled'
  AND (
      p.access_type = 'public'
      OR EXISTS (
          SELECT 1
          FROM project_accesses pa
          WHERE pa.project_id = p.id AND pa.user_id = ?
      )
  )
ORDER BY pp.id ASC`, projectID, buyerUserID).Scan(&productRows).Error; err != nil {
		return nil, fmt.Errorf("load product inventory rows: %w", err)
	}
	if len(productRows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	stats, err := r.GetInventoryStats(ctx, projectID, buyerUserID)
	if err != nil {
		return nil, err
	}
	result := &allocapp.ProjectProductInventoryTotals{
		ProjectID: projectID,
		Items:     make([]allocapp.ProductInventoryTotal, 0, len(productRows)),
	}
	for _, row := range productRows {
		item := allocapp.ProductInventoryTotal{ProductID: row.ProductID}
		switch domain.AllocationType(row.Type) {
		case domain.AllocationTypeMicrosoft:
			if row.MainWeight > 0 {
				item.TotalAvailable += stats.Microsoft.MainAvailable + stats.Microsoft.ExplicitAliasAvailable
			}
			if row.DotWeight > 0 {
				item.TotalAvailable += stats.Microsoft.DotAvailable
			}
			if row.PlusWeight > 0 {
				item.TotalAvailable += stats.Microsoft.PlusDailyAvailable
			}
		case domain.AllocationTypeDomain:
			item.TotalAvailable = stats.Domain.TotalAvailable
		default:
			return nil, domain.ErrProjectNotAllocatable
		}
		result.TotalAvailable += item.TotalAvailable
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (r *Repo) RefreshRoutingCandidates(ctx context.Context, projectID uint) (int, error) {
	total := 0
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		microsoftCount, err := r.refreshMicrosoftCandidatesInTx(txCtx, projectID)
		if err != nil {
			return err
		}
		domainCount, err := r.refreshDomainCandidatesInTx(txCtx, projectID)
		if err != nil {
			return err
		}
		total = microsoftCount + domainCount
		return nil
	})
	return total, err
}

func (r *Repo) refreshMicrosoftCandidatesInTx(ctx context.Context, projectID uint) (int, error) {
	db := r.dbFor(ctx)
	var projectCount int64
	if err := db.Raw(`
SELECT COUNT(*)
FROM projects p
JOIN project_products pp ON pp.project_id = p.id AND pp.type = 'microsoft' AND pp.status = 'enabled'
WHERE p.id = ? AND p.status = 'listed'`, projectID).Scan(&projectCount).Error; err != nil {
		return 0, fmt.Errorf("check microsoft candidate project: %w", err)
	}
	if projectCount == 0 {
		if err := db.Where("project_id = ?", projectID).Delete(&RoutingCandidateModel{}).Error; err != nil {
			return 0, fmt.Errorf("clear microsoft candidates: %w", err)
		}
		return 0, nil
	}
	if err := db.Exec(`
DELETE rc
FROM microsoft_routing_candidates rc
LEFT JOIN microsoft_resources ms ON ms.id = rc.resource_id
LEFT JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
LEFT JOIN users u ON u.id = er.owner_user_id
WHERE rc.project_id = ?
  AND (
      ms.id IS NULL
      OR ms.status <> 'normal'
      OR ms.for_sale <> TRUE
      OR u.enabled <> TRUE
      OR u.role_level NOT IN (20, 80, 100)
  )`, projectID).Error; err != nil {
		return 0, fmt.Errorf("delete stale microsoft candidates: %w", err)
	}
	if err := db.Exec(`
INSERT INTO microsoft_routing_candidates (
    project_id, resource_id, email_address, domain_suffix, for_sale, quality_score, status, alloc_bucket, last_allocated_at
)
SELECT
    ?,
    ms.id,
    ms.email_address,
    SUBSTRING_INDEX(ms.email_address, '@', -1),
    ms.for_sale,
    ms.quality_score,
    ms.status,
    ms.alloc_bucket,
    ms.last_allocated_at
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal'
  AND ms.for_sale = TRUE
  AND u.enabled = TRUE
  AND u.role_level IN (20, 80, 100)
ON DUPLICATE KEY UPDATE
    email_address = VALUES(email_address),
    domain_suffix = VALUES(domain_suffix),
    for_sale = VALUES(for_sale),
    quality_score = VALUES(quality_score),
    status = VALUES(status),
    alloc_bucket = VALUES(alloc_bucket),
    last_allocated_at = VALUES(last_allocated_at)`, projectID).Error; err != nil {
		return 0, fmt.Errorf("upsert microsoft candidates: %w", err)
	}
	var count int64
	if err := db.Model(&RoutingCandidateModel{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count microsoft candidates: %w", err)
	}
	return int(count), nil
}

func (r *Repo) refreshDomainCandidatesInTx(ctx context.Context, projectID uint) (int, error) {
	db := r.dbFor(ctx)
	var projectCount int64
	if err := db.Raw(`
SELECT COUNT(*)
FROM projects p
JOIN project_products pp ON pp.project_id = p.id AND pp.type = 'domain' AND pp.status = 'enabled'
WHERE p.id = ? AND p.status = 'listed'`, projectID).Scan(&projectCount).Error; err != nil {
		return 0, fmt.Errorf("check domain candidate project: %w", err)
	}
	if projectCount == 0 {
		if err := db.Where("project_id = ?", projectID).Delete(&DomainRoutingCandidateModel{}).Error; err != nil {
			return 0, fmt.Errorf("clear domain candidates: %w", err)
		}
		return 0, nil
	}
	if err := db.Exec(`
DELETE dc
FROM domain_routing_candidates dc
LEFT JOIN domain_resources dr ON dr.id = dc.resource_id
LEFT JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
LEFT JOIN mail_servers ms ON ms.id = dr.mail_server_id
LEFT JOIN users u ON u.id = er.owner_user_id
WHERE dc.project_id = ?
  AND (
      dr.id IS NULL
      OR dr.purpose <> 'sale'
      OR dr.status <> 'normal'
      OR ms.status <> 'online'
      OR u.enabled <> TRUE
      OR u.role_level NOT IN (20, 80, 100)
  )`, projectID).Error; err != nil {
		return 0, fmt.Errorf("delete stale domain candidates: %w", err)
	}
	if err := db.Exec(`
INSERT INTO domain_routing_candidates (
    project_id, resource_id, domain, domain_tld, purpose, status, alloc_bucket, last_allocated_at
)
SELECT
    ?,
    dr.id,
    dr.domain,
    dr.domain_tld,
    dr.purpose,
    dr.status,
    dr.alloc_bucket,
    dr.last_allocated_at
FROM domain_resources dr
JOIN email_resources er ON er.id = dr.id AND er.type = 'domain'
JOIN mail_servers ms ON ms.id = dr.mail_server_id
JOIN users u ON u.id = er.owner_user_id
WHERE dr.purpose = 'sale'
  AND dr.status = 'normal'
  AND ms.status = 'online'
  AND u.enabled = TRUE
  AND u.role_level IN (20, 80, 100)
ON DUPLICATE KEY UPDATE
    domain = VALUES(domain),
    domain_tld = VALUES(domain_tld),
    purpose = VALUES(purpose),
    status = VALUES(status),
    alloc_bucket = VALUES(alloc_bucket),
    last_allocated_at = VALUES(last_allocated_at)`, projectID).Error; err != nil {
		return 0, fmt.Errorf("upsert domain candidates: %w", err)
	}
	var count int64
	if err := db.Model(&DomainRoutingCandidateModel{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count domain candidates: %w", err)
	}
	return int(count), nil
}

func (r *Repo) ListRoutingCandidates(ctx context.Context, filter allocapp.CandidateFilter) (*allocapp.CandidateListResult, error) {
	sql, args := routingCandidateListSQL(filter)
	countArgs := append([]any{}, args...)
	var total int64
	if err := r.dbFor(ctx).Raw("SELECT COUNT(*) FROM ("+sql+") AS candidates", countArgs...).Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("count routing candidates: %w", err)
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, filter.Limit, filter.Offset)
	var rows []routingCandidateRow
	if err := r.dbFor(ctx).Raw(sql+" ORDER BY last_allocated_at ASC, quality_score DESC, resource_id ASC LIMIT ? OFFSET ?", queryArgs...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list routing candidates: %w", err)
	}
	items := make([]allocapp.RoutingCandidate, len(rows))
	for i := range rows {
		items[i] = rows[i].toApp()
	}
	return &allocapp.CandidateListResult{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit}, nil
}

type routingCandidateRow struct {
	Type            string     `gorm:"column:type"`
	ID              uint       `gorm:"column:id"`
	ProjectID       uint       `gorm:"column:project_id"`
	ResourceID      uint       `gorm:"column:resource_id"`
	Address         string     `gorm:"column:address"`
	DomainSuffix    string     `gorm:"column:domain_suffix"`
	ForSale         bool       `gorm:"column:for_sale"`
	QualityScore    int        `gorm:"column:quality_score"`
	Status          string     `gorm:"column:status"`
	Bucket          uint8      `gorm:"column:alloc_bucket"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (r routingCandidateRow) toApp() allocapp.RoutingCandidate {
	return allocapp.RoutingCandidate{
		ID:              r.ID,
		Type:            domain.AllocationType(r.Type),
		ProjectID:       r.ProjectID,
		ResourceID:      r.ResourceID,
		Address:         r.Address,
		DomainSuffix:    r.DomainSuffix,
		ForSale:         r.ForSale,
		QualityScore:    r.QualityScore,
		Status:          r.Status,
		Bucket:          r.Bucket,
		LastAllocatedAt: r.LastAllocatedAt,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func routingCandidateListSQL(filter allocapp.CandidateFilter) (string, []any) {
	microsoftSQL := `
SELECT
    'microsoft' AS type,
    id,
    project_id,
    resource_id,
    email_address AS address,
    domain_suffix,
    for_sale,
    quality_score,
    status,
    alloc_bucket,
    last_allocated_at,
    created_at,
    updated_at
FROM microsoft_routing_candidates
WHERE project_id = ?`
	domainSQL := `
SELECT
    'domain' AS type,
    id,
    project_id,
    resource_id,
    domain AS address,
    domain_tld AS domain_suffix,
    TRUE AS for_sale,
    0 AS quality_score,
    status,
    alloc_bucket,
    last_allocated_at,
    created_at,
    updated_at
FROM domain_routing_candidates
WHERE project_id = ?`
	switch filter.Type {
	case domain.AllocationTypeMicrosoft:
		return microsoftSQL, []any{filter.ProjectID}
	case domain.AllocationTypeDomain:
		return domainSQL, []any{filter.ProjectID}
	default:
		return microsoftSQL + " UNION ALL " + domainSQL, []any{filter.ProjectID, filter.ProjectID}
	}
}

func (r *Repo) CreateCandidateRefreshJobWithLog(ctx context.Context, job *domain.CandidateRefreshJob) (bool, error) {
	if job == nil || job.ProjectID == 0 || job.OperatorUserID == 0 {
		return false, domain.ErrInvalidAllocationRequest
	}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing CandidateRefreshJobModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND status IN ?", job.ProjectID, []string{
				string(domain.CandidateRefreshPending),
				string(domain.CandidateRefreshQueued),
				string(domain.CandidateRefreshRunning),
			}).
			Order("id DESC").
			First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find active candidate refresh job: %w", err)
		}
		if err == nil {
			*job = existing.toDomain()
			return nil
		}

		job.Status = domain.CandidateRefreshPending
		job.MaxAttempts = normalizeCandidateRefreshMaxAttempts(job.MaxAttempts)
		model := candidateRefreshJobModel(job)
		if err := tx.Create(model).Error; err != nil {
			if isDuplicateKeyError(err) {
				var duplicate CandidateRefreshJobModel
				findErr := tx.Where("project_id = ? AND status IN ?", job.ProjectID, []string{
					string(domain.CandidateRefreshPending),
					string(domain.CandidateRefreshQueued),
					string(domain.CandidateRefreshRunning),
				}).Order("id DESC").First(&duplicate).Error
				if findErr == nil {
					*job = duplicate.toDomain()
					return nil
				}
				if !errors.Is(findErr, gorm.ErrRecordNotFound) {
					return fmt.Errorf("find duplicate candidate refresh job: %w", findErr)
				}
			}
			if isForeignKeyError(err) {
				return domain.ErrInvalidAllocationRequest
			}
			return fmt.Errorf("create candidate refresh job: %w", err)
		}
		*job = model.toDomain()
		created = true
		if r.operationLogs != nil {
			log := &governancedomain.OperationLog{
				OperatorUserID: job.OperatorUserID,
				OperationType:  "alloc.candidates.refresh",
				ResourceType:   "project",
				ResourceID:     fmt.Sprintf("%d", job.ProjectID),
				Path:           job.Path,
				Result:         "success",
				SafeSummary:    "Candidate refresh job queued.",
				RequestID:      job.RequestID,
			}
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return fmt.Errorf("create candidate refresh operation log: %w", err)
			}
		}
		return nil
	})
	return created, err
}

func (r *Repo) FindCandidateRefreshJob(ctx context.Context, jobID uint) (*domain.CandidateRefreshJob, error) {
	var model CandidateRefreshJobModel
	if err := r.dbFor(ctx).First(&model, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find candidate refresh job: %w", err)
	}
	job := model.toDomain()
	return &job, nil
}

func (r *Repo) ExpireStaleCandidateRefreshJobs(ctx context.Context, staleBefore time.Time) (int, error) {
	now := time.Now().UTC()
	message := "Candidate refresh job expired before completion."
	var expired []CandidateRefreshJobModel
	err := r.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND attempts >= max_attempts AND updated_at < ?",
				string(domain.CandidateRefreshRunning),
				staleBefore,
			).
			Order("id ASC").
			Limit(100).
			Find(&expired).Error; err != nil {
			return fmt.Errorf("find stale candidate refresh jobs: %w", err)
		}
		if len(expired) == 0 {
			return nil
		}
		ids := make([]uint, len(expired))
		for i := range expired {
			ids[i] = expired[i].ID
		}
		if err := tx.Model(&CandidateRefreshJobModel{}).
			Where("id IN ? AND status = ?", ids, string(domain.CandidateRefreshRunning)).
			Updates(map[string]any{
				"status":          string(domain.CandidateRefreshFailed),
				"last_safe_error": message,
				"finished_at":     now,
				"updated_at":      now,
			}).Error; err != nil {
			return fmt.Errorf("expire stale candidate refresh jobs: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for i := range expired {
		if r.systemLogs == nil {
			continue
		}
		_ = r.systemLogs.Create(ctx, &governancedomain.SystemLog{
			Level:     "error",
			Module:    "alloc",
			EventType: "alloc.candidates.refresh_expired",
			RequestID: expired[i].RequestID,
			BizType:   "candidate_refresh_job",
			BizID:     fmt.Sprintf("%d", expired[i].ID),
			Message:   "Candidate refresh job expired.",
			Detail:    message,
		})
	}
	return len(expired), nil
}

func (r *Repo) ClaimDispatchableCandidateRefreshJobs(ctx context.Context, limit int, staleBefore time.Time) ([]domain.CandidateRefreshJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []CandidateRefreshJobModel
	err := r.dbFor(ctx).
		Where("status = ? OR (status IN ? AND updated_at < ?)",
			string(domain.CandidateRefreshPending),
			[]string{string(domain.CandidateRefreshQueued), string(domain.CandidateRefreshRunning)},
			staleBefore,
		).
		Order("id ASC").
		Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("claim candidate refresh jobs: %w", err)
	}
	result := make([]domain.CandidateRefreshJob, len(models))
	for i := range models {
		result[i] = models[i].toDomain()
	}
	return result, nil
}

func (r *Repo) MarkCandidateRefreshJobQueued(ctx context.Context, jobID uint) (bool, error) {
	now := time.Now().UTC()
	result := r.dbFor(ctx).Model(&CandidateRefreshJobModel{}).
		Where("id = ? AND status IN ?", jobID, []string{
			string(domain.CandidateRefreshPending),
			string(domain.CandidateRefreshQueued),
			string(domain.CandidateRefreshRunning),
		}).
		Updates(map[string]any{
			"status":          string(domain.CandidateRefreshQueued),
			"last_safe_error": "",
			"updated_at":      now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark candidate refresh queued: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *Repo) MarkCandidateRefreshJobDispatchFailed(ctx context.Context, jobID uint, safeError string) error {
	return r.dbFor(ctx).Model(&CandidateRefreshJobModel{}).
		Where("id = ? AND status IN ?", jobID, []string{string(domain.CandidateRefreshPending), string(domain.CandidateRefreshQueued)}).
		Updates(map[string]any{
			"status":          string(domain.CandidateRefreshPending),
			"last_safe_error": safeCandidateRefreshMessage(safeError),
			"updated_at":      time.Now().UTC(),
		}).Error
}

func (r *Repo) MarkCandidateRefreshJobRunning(ctx context.Context, jobID uint) (bool, error) {
	now := time.Now().UTC()
	staleBefore := now.Add(-10 * time.Minute)
	result := r.dbFor(ctx).Model(&CandidateRefreshJobModel{}).
		Where("id = ? AND attempts < max_attempts AND (status = ? OR (status = ? AND updated_at < ?))",
			jobID,
			string(domain.CandidateRefreshQueued),
			string(domain.CandidateRefreshRunning),
			staleBefore,
		).
		Updates(map[string]any{
			"status":          string(domain.CandidateRefreshRunning),
			"attempts":        gorm.Expr("attempts + 1"),
			"last_safe_error": "",
			"started_at":      now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark candidate refresh running: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *Repo) MarkCandidateRefreshJobSucceeded(ctx context.Context, jobID uint, affected int) error {
	if affected < 0 {
		affected = 0
	}
	now := time.Now().UTC()
	return r.dbFor(ctx).Model(&CandidateRefreshJobModel{}).
		Where("id = ? AND status = ?", jobID, string(domain.CandidateRefreshRunning)).
		Updates(map[string]any{
			"status":          string(domain.CandidateRefreshSucceeded),
			"affected":        affected,
			"last_safe_error": "",
			"finished_at":     now,
			"updated_at":      now,
		}).Error
}

func (r *Repo) MarkCandidateRefreshJobFailed(ctx context.Context, jobID uint, safeError string) error {
	now := time.Now().UTC()
	message := safeCandidateRefreshMessage(safeError)
	err := r.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var job CandidateRefreshJobModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, jobID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrAllocationNotFound
			}
			return fmt.Errorf("lock candidate refresh job failure: %w", err)
		}
		if err := tx.Model(&CandidateRefreshJobModel{}).
			Where("id = ? AND status = ?", jobID, string(domain.CandidateRefreshRunning)).
			Updates(map[string]any{
				"status":          string(domain.CandidateRefreshFailed),
				"last_safe_error": message,
				"finished_at":     now,
				"updated_at":      now,
			}).Error; err != nil {
			return fmt.Errorf("mark candidate refresh failed: %w", err)
		}
		if r.systemLogs != nil {
			if err := r.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
				Level:     "error",
				Module:    "alloc",
				EventType: "alloc.candidates.refresh_failed",
				RequestID: job.RequestID,
				BizType:   "candidate_refresh_job",
				BizID:     fmt.Sprintf("%d", job.ID),
				Message:   "Candidate refresh job failed.",
				Detail:    message,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return err
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
	default:
		return nil, domain.ErrAllocationNotFound
	}
}

type unifiedRow struct {
	Type       string
	ID         uint
	OrderNo    string
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Mailbox    string
	Email      string
	Status     string
	CreatedAt  time.Time
	ReleasedAt *time.Time
}

func (r *Repo) queryUnifiedAllocations(ctx context.Context, filter allocapp.AllocationFilter, paginate bool) ([]domain.UnifiedAllocation, int64, error) {
	selects := []string{}
	args := []any{}
	addSelect := func(table, typ, mailboxExpr string, conditions []string, condArgs []any) {
		selects = append(selects, fmt.Sprintf(`SELECT '%s' AS type, id, order_no, project_id, product_id, resource_id, %s AS mailbox, email, status, created_at, released_at FROM %s WHERE %s`, typ, mailboxExpr, table, strings.Join(conditions, " AND ")))
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
			Type:       domain.AllocationType(rows[i].Type),
			ID:         rows[i].ID,
			OrderNo:    rows[i].OrderNo,
			ProjectID:  rows[i].ProjectID,
			ProductID:  rows[i].ProductID,
			ResourceID: rows[i].ResourceID,
			Mailbox:    rows[i].Mailbox,
			Email:      rows[i].Email,
			Status:     domain.AllocationStatus(rows[i].Status),
			CreatedAt:  rows[i].CreatedAt,
			ReleasedAt: rows[i].ReleasedAt,
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

func normalizeCandidateRefreshMaxAttempts(maxAttempts int) int {
	if maxAttempts != 1 {
		return 1
	}
	return maxAttempts
}

func safeCandidateRefreshMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if len([]rune(message)) > 500 {
		message = string([]rune(message)[:500])
	}
	return message
}

func isValidDailyUsageKind(kind domain.DailyUsageKind) bool {
	return kind == domain.DailyUsageKindPlus || kind == domain.DailyUsageKindDomainMailbox
}

func microsoftInventoryScopeSQL(buyerUserID uint) (string, []any) {
	publicScope := "(ms.for_sale = TRUE AND u.enabled = TRUE AND u.role_level IN (20, 80, 100))"
	if buyerUserID == 0 {
		return publicScope, nil
	}
	return "(" + publicScope + " OR (ms.for_sale = FALSE AND er.owner_user_id = ?))", []any{buyerUserID}
}

func normalizeCandidateSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "@")
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
