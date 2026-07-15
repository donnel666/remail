package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MessageModel struct {
	ID                uint           `gorm:"primaryKey;autoIncrement"`
	EmailResourceID   uint           `gorm:"not null;column:email_resource_id"`
	ResourceType      string         `gorm:"type:varchar(32);not null;column:resource_type"`
	MatchedOrderID    *uint          `gorm:"column:matched_order_id"`
	Recipient         string         `gorm:"type:varchar(255);not null"`
	RecipientsJSON    sql.NullString `gorm:"type:json;column:recipients_json"`
	Sender            string         `gorm:"type:varchar(255);not null;default:''"`
	Subject           string         `gorm:"type:varchar(500);not null;default:''"`
	RawBody           sql.NullString `gorm:"type:mediumtext;column:raw_body"`
	BodyPreview       string         `gorm:"type:varchar(1000);not null;default:'';column:body_preview"`
	VerificationCode  string         `gorm:"type:varchar(64);not null;default:'';column:verification_code"`
	MessageIDHeader   string         `gorm:"type:varchar(500);not null;default:'';column:message_id_header"`
	ProviderMessageID string         `gorm:"type:varchar(500);not null;default:'';column:provider_message_id"`
	DedupeKey         string         `gorm:"type:char(64);not null;column:dedupe_key"`
	Protocol          string         `gorm:"type:varchar(32);not null;default:''"`
	Folder            string         `gorm:"type:varchar(64);not null;default:''"`
	Status            string         `gorm:"type:varchar(32);not null;default:'received'"`
	MatchDiagnostic   string         `gorm:"type:varchar(500);not null;default:'';column:match_diagnostic"`
	ReceivedAt        time.Time      `gorm:"not null;column:received_at"`
	CreatedAt         time.Time      `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt         time.Time      `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (MessageModel) TableName() string { return "mailmatch_messages" }

type FetchJobModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo         string     `gorm:"type:varchar(64);not null;column:order_no"`
	Purpose         string     `gorm:"type:varchar(32);not null;default:'order_fetch'"`
	AllocationType  string     `gorm:"type:varchar(32);not null;column:allocation_type"`
	AllocationID    uint       `gorm:"not null;column:allocation_id"`
	ProjectID       uint       `gorm:"not null;column:project_id"`
	EmailResourceID uint       `gorm:"not null;column:email_resource_id"`
	Recipient       string     `gorm:"type:varchar(255);not null"`
	Status          string     `gorm:"type:varchar(32);not null;default:'pending'"`
	Attempts        int        `gorm:"not null;default:0"`
	MaxAttempts     int        `gorm:"not null;default:3;column:max_attempts"`
	SinceAt         *time.Time `gorm:"column:since_at"`
	UntilAt         *time.Time `gorm:"column:until_at"`
	FetchedCount    int        `gorm:"not null;default:0;column:fetched_count"`
	StoredCount     int        `gorm:"not null;default:0;column:stored_count"`
	MatchedCount    int        `gorm:"not null;default:0;column:matched_count"`
	LastSafeError   string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID       string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	StartedAt       *time.Time `gorm:"column:started_at"`
	FinishedAt      *time.Time `gorm:"column:finished_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (FetchJobModel) TableName() string { return "mailmatch_fetch_jobs" }

type FetchStateModel struct {
	EmailResourceID uint       `gorm:"primaryKey;column:email_resource_id"`
	LastJobID       *uint      `gorm:"column:last_job_id"`
	LastStatus      string     `gorm:"type:varchar(32);not null;default:'';column:last_status"`
	LastSubmittedAt *time.Time `gorm:"column:last_submitted_at"`
	LastSuccessAt   *time.Time `gorm:"column:last_success_at"`
	LastReceivedAt  *time.Time `gorm:"column:last_received_at"`
	CooldownUntil   *time.Time `gorm:"column:cooldown_until"`
	LastSafeError   string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (FetchStateModel) TableName() string { return "mailmatch_resource_fetch_states" }

type OrderDeliveryHeadModel struct {
	OrderID           uint      `gorm:"primaryKey;column:order_id"`
	MessageID         *uint     `gorm:"column:message_id"`
	MessageReceivedAt time.Time `gorm:"not null;column:message_received_at"`
}

func (OrderDeliveryHeadModel) TableName() string { return "mailmatch_order_delivery_heads" }

type Repo struct {
	db    *gorm.DB
	files governanceapp.FilePort
}

func NewRepo(db *gorm.DB, files governanceapp.FilePort) *Repo {
	return &Repo{db: db, files: files}
}

func (r *Repo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx))
	})
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) LoadOrderScope(ctx context.Context, orderNo string, userID uint, isAdmin bool) (*app.OrderScope, error) {
	scope, err := r.loadOrderScope(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	if !isAdmin && scope.UserID != userID {
		return nil, domain.ErrOrderForbidden
	}
	return scope, nil
}

func (r *Repo) LoadOrderScopeForServiceToken(ctx context.Context, orderNo string) (*app.OrderScope, error) {
	return r.loadOrderScope(ctx, orderNo)
}

func (r *Repo) LoadPickupScope(ctx context.Context, token string, email string) (*app.OrderScope, error) {
	token = strings.TrimSpace(token)
	email = strings.ToLower(strings.TrimSpace(email))
	if token == "" || email == "" {
		return nil, domain.ErrPickupCredentialInvalid
	}
	var row orderScopeRow
	if err := r.dbFor(ctx).Raw(pickupScopeSQL, token, email, email).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("load pickup mail scope: %w", err)
	}
	if row.OrderNo == "" {
		return nil, domain.ErrPickupCredentialInvalid
	}
	return row.toScope(nil), nil
}

func (r *Repo) loadOrderScope(ctx context.Context, orderNo string) (*app.OrderScope, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidRequest
	}
	var row orderScopeRow
	err := r.dbFor(ctx).Raw(orderScopeSQL, orderNo, orderNo, orderNo).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("load order mail scope: %w", err)
	}
	if row.OrderNo == "" {
		return nil, domain.ErrOrderNotFound
	}
	rules, err := r.loadMailRules(ctx, row.ProjectID)
	if err != nil {
		return nil, err
	}
	return row.toScope(rules), nil
}

type orderScopeRow struct {
	OrderID           uint
	OrderNo           string
	UserID            uint
	ProjectID         uint
	ProductID         uint
	ServiceMode       string
	OrderStatus       string
	AllocationType    string
	AllocationID      uint
	RecipientKind     string
	EmailResourceID   uint
	Recipient         string
	ReceiveStartedAt  *time.Time
	ReceiveUntil      *time.Time
	ActivatedAt       *time.Time
	AfterSaleUntil    *time.Time
	LooseMatch        bool
	MicrosoftEmail    string
	MicrosoftClientID string
	MicrosoftRT       string
}

const orderScopeSQL = `
SELECT
	    o.id AS order_id,
    o.order_no,
    o.user_id,
    o.project_id,
    o.project_product_id AS product_id,
    o.service_mode,
	    o.status AS order_status,
	    o.allocation_type,
	    COALESCE(o.microsoft_alloc_id, o.domain_alloc_id, 0) AS allocation_id,
	    CASE
	      WHEN o.allocation_type = 'microsoft' AND ma.mailbox IN ('dot', 'plus') THEN ma.mailbox
	      ELSE 'exact'
	    END AS recipient_kind,
	    CASE WHEN o.allocation_type = 'microsoft' THEN ma.resource_id ELSE da.resource_id END AS email_resource_id,
    CASE WHEN o.allocation_type = 'microsoft' THEN ma.email ELSE da.email END AS recipient,
    o.receive_started_at,
    o.receive_until,
    o.activated_at,
    o.after_sale_until,
    p.loose_match,
    COALESCE(mr.email_address, '') AS microsoft_email,
    COALESCE(mr.client_id, '') AS microsoft_client_id,
    COALESCE(mr.refresh_token, '') AS microsoft_rt
FROM orders o
JOIN projects p ON p.id = o.project_id
LEFT JOIN microsoft_allocations ma ON ma.id = o.microsoft_alloc_id AND o.allocation_type = 'microsoft'
LEFT JOIN microsoft_resources mr ON mr.id = ma.resource_id
LEFT JOIN domain_allocations da ON da.id = o.domain_alloc_id AND o.allocation_type = 'domain'
WHERE o.order_no = ?
  AND (
    (o.allocation_type = 'microsoft' AND ma.order_no = ?)
    OR (o.allocation_type = 'domain' AND da.order_no = ?)
	  )
	LIMIT 1`

const pickupScopeSQL = `
SELECT
    o.id AS order_id,
    o.order_no,
    o.user_id,
    o.project_id,
    o.project_product_id AS product_id,
    o.service_mode,
    o.status AS order_status,
    o.allocation_type,
    COALESCE(o.microsoft_alloc_id, o.domain_alloc_id, 0) AS allocation_id,
    CASE
      WHEN o.allocation_type = 'microsoft' AND ma.mailbox IN ('dot', 'plus') THEN ma.mailbox
      ELSE 'exact'
    END AS recipient_kind,
    CASE WHEN o.allocation_type = 'microsoft' THEN ma.resource_id ELSE da.resource_id END AS email_resource_id,
    CASE WHEN o.allocation_type = 'microsoft' THEN ma.email ELSE da.email END AS recipient,
    o.receive_started_at,
    o.receive_until,
    o.activated_at,
    o.after_sale_until,
    p.loose_match,
    '' AS microsoft_email,
    '' AS microsoft_client_id,
    '' AS microsoft_rt
FROM order_tokens t
JOIN orders o ON o.order_no = t.order_no
JOIN projects p ON p.id = o.project_id
LEFT JOIN microsoft_allocations ma
  ON ma.id = o.microsoft_alloc_id AND o.allocation_type = 'microsoft'
LEFT JOIN domain_allocations da
  ON da.id = o.domain_alloc_id AND o.allocation_type = 'domain'
WHERE t.token_plain = ?
  AND t.enabled = 1
  AND (t.expire_at IS NULL OR t.expire_at > UTC_TIMESTAMP())
  AND (
    (o.allocation_type = 'microsoft' AND ma.order_no = o.order_no AND ma.email = ?)
    OR
    (o.allocation_type = 'domain' AND da.order_no = o.order_no AND da.email = ?)
  )
LIMIT 1`

const microsoftMatchingScopesSQL = `
SELECT
	    o.id AS order_id,
    o.order_no,
    o.user_id,
    o.project_id,
    o.project_product_id AS product_id,
    o.service_mode,
    o.status AS order_status,
    o.allocation_type,
    ma.id AS allocation_id,
    CASE WHEN ma.mailbox IN ('dot', 'plus') THEN ma.mailbox ELSE 'exact' END AS recipient_kind,
    ma.resource_id AS email_resource_id,
    ma.email AS recipient,
    o.receive_started_at,
    o.receive_until,
    o.activated_at,
    o.after_sale_until,
    p.loose_match,
    COALESCE(mr.email_address, '') AS microsoft_email,
    COALESCE(mr.client_id, '') AS microsoft_client_id,
    COALESCE(mr.refresh_token, '') AS microsoft_rt
FROM microsoft_allocations ma
JOIN orders o ON o.microsoft_alloc_id = ma.id AND o.allocation_type = 'microsoft'
JOIN projects p ON p.id = o.project_id
JOIN microsoft_resources mr ON mr.id = ma.resource_id
WHERE ma.resource_id = ?
  AND ma.email = ?
  AND ma.status = 'allocated'
  AND (o.receive_started_at IS NULL OR ? >= DATE_SUB(o.receive_started_at, INTERVAL 2 MINUTE))
  AND (
    (
      o.service_mode = 'code'
      AND o.status = 'active'
      AND (o.receive_until IS NULL OR ? <= o.receive_until)
    )
    OR
    (o.service_mode = 'purchase' AND o.status IN ('active', 'completed'))
  )
ORDER BY o.created_at ASC, o.id ASC`

const domainMatchingScopesSQL = `
SELECT
	    o.id AS order_id,
    o.order_no,
    o.user_id,
    o.project_id,
    o.project_product_id AS product_id,
    o.service_mode,
    o.status AS order_status,
    o.allocation_type,
    da.id AS allocation_id,
    'exact' AS recipient_kind,
    da.resource_id AS email_resource_id,
    da.email AS recipient,
    o.receive_started_at,
    o.receive_until,
    o.activated_at,
    o.after_sale_until,
    p.loose_match,
    '' AS microsoft_email,
    '' AS microsoft_client_id,
    '' AS microsoft_rt
FROM domain_allocations da
JOIN orders o ON o.domain_alloc_id = da.id AND o.allocation_type = 'domain'
JOIN projects p ON p.id = o.project_id
WHERE da.resource_id = ?
  AND da.email = ?
  AND da.status = 'allocated'
  AND (o.receive_started_at IS NULL OR ? >= DATE_SUB(o.receive_started_at, INTERVAL 2 MINUTE))
  AND (
    (
      o.service_mode = 'code'
      AND o.status = 'active'
      AND (o.receive_until IS NULL OR ? <= o.receive_until)
    )
    OR
    (o.service_mode = 'purchase' AND o.status IN ('active', 'completed'))
  )
ORDER BY o.created_at ASC, o.id ASC`

func (r orderScopeRow) toScope(rules []app.MailRule) *app.OrderScope {
	return &app.OrderScope{
		OrderID:           r.OrderID,
		OrderNo:           r.OrderNo,
		UserID:            r.UserID,
		ProjectID:         r.ProjectID,
		ProductID:         r.ProductID,
		ServiceMode:       r.ServiceMode,
		OrderStatus:       r.OrderStatus,
		AllocationType:    domain.ResourceType(r.AllocationType),
		AllocationID:      r.AllocationID,
		RecipientKind:     strings.ToLower(strings.TrimSpace(r.RecipientKind)),
		EmailResourceID:   r.EmailResourceID,
		Recipient:         strings.ToLower(strings.TrimSpace(r.Recipient)),
		ReceiveStartedAt:  r.ReceiveStartedAt,
		ReceiveUntil:      r.ReceiveUntil,
		ActivatedAt:       r.ActivatedAt,
		AfterSaleUntil:    r.AfterSaleUntil,
		LooseMatch:        r.LooseMatch,
		Rules:             rules,
		MicrosoftEmail:    r.MicrosoftEmail,
		MicrosoftClientID: r.MicrosoftClientID,
		MicrosoftRT:       r.MicrosoftRT,
	}
}

func (r *Repo) loadMailRules(ctx context.Context, projectID uint) ([]app.MailRule, error) {
	var rows []struct {
		RuleType string
		Pattern  string
		Enabled  bool
	}
	if err := r.dbFor(ctx).Table("project_mail_rules").
		Select("rule_type, pattern, enabled").
		Where("project_id = ? AND enabled = 1", projectID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load project mail rules: %w", err)
	}
	rules := make([]app.MailRule, len(rows))
	for i := range rows {
		rules[i] = app.MailRule{Type: app.MailRuleType(rows[i].RuleType), Pattern: rows[i].Pattern, Enabled: rows[i].Enabled}
	}
	return rules, nil
}

func (r *Repo) ListOrderMessages(ctx context.Context, scope app.OrderScope, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 30
	}
	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)
	if scope.AllocationType == domain.ResourceTypeMicrosoft {
		start = now.Add(-3 * 24 * time.Hour)
	}
	if scope.ReceiveStartedAt != nil {
		serviceStart := scope.ReceiveStartedAt.Add(-2 * time.Minute)
		if serviceStart.After(start) {
			start = serviceStart
		}
	}
	end := now
	if scope.ServiceMode == "code" {
		if scope.ReceiveUntil != nil {
			end = *scope.ReceiveUntil
		}
	}
	query := r.dbFor(ctx).Model(&MessageModel{}).
		Select("id, email_resource_id, resource_type, matched_order_id, recipient, recipients_json, sender, subject, body_preview, verification_code, message_id_header, provider_message_id, dedupe_key, protocol, folder, status, match_diagnostic, received_at, created_at, updated_at").
		Where("matched_order_id = ? AND received_at >= ? AND received_at <= ?",
			scope.OrderID,
			start,
			end,
		).
		Order("received_at DESC, id DESC").
		Limit(limit)
	var models []MessageModel
	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list order mail messages: %w", err)
	}
	items := make([]domain.Message, len(models))
	for i := range models {
		items[i] = messageModelToDomain(models[i])
	}
	return items, nil
}

func (r *Repo) FindOrderMessage(ctx context.Context, orderID uint, messageID uint) (*domain.Message, error) {
	if orderID == 0 || messageID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	var model MessageModel
	if err := r.dbFor(ctx).
		Where("id = ? AND matched_order_id = ?", messageID, orderID).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrMessageNotFound
		}
		return nil, fmt.Errorf("find order mail message: %w", err)
	}
	item := messageModelToDomain(model)
	return &item, nil
}

func (r *Repo) FindOrderDelivery(ctx context.Context, orderID uint) (*app.OrderDelivery, error) {
	if orderID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	var head OrderDeliveryHeadModel
	err := r.dbFor(ctx).
		Where("order_id = ?", orderID).
		Take(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find order mail delivery: %w", err)
	}
	delivery := &app.OrderDelivery{ReceivedAt: head.MessageReceivedAt}
	if head.MessageID == nil {
		return delivery, nil
	}
	var model MessageModel
	if err := r.dbFor(ctx).
		Select("id, email_resource_id, resource_type, matched_order_id, recipient, recipients_json, sender, subject, body_preview, verification_code, message_id_header, provider_message_id, dedupe_key, protocol, folder, status, match_diagnostic, received_at, created_at, updated_at").
		First(&model, "id = ?", *head.MessageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return delivery, nil
		}
		return nil, fmt.Errorf("find delivered mail message: %w", err)
	}
	item := messageModelToDomain(model)
	delivery.Message = &item
	return delivery, nil
}

func (r *Repo) CreateCodeOrderDelivery(ctx context.Context, orderID uint, message domain.Message) error {
	if orderID == 0 || message.ID == 0 || strings.TrimSpace(message.VerificationCode) == "" || message.ReceivedAt.IsZero() {
		return domain.ErrInvalidRequest
	}
	model := OrderDeliveryHeadModel{
		OrderID:           orderID,
		MessageID:         &message.ID,
		MessageReceivedAt: message.ReceivedAt.UTC(),
	}
	if err := r.dbFor(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model).Error; err != nil {
		return fmt.Errorf("create code order delivery: %w", err)
	}
	return nil
}

func (r *Repo) AdvancePurchaseOrderDelivery(ctx context.Context, orderID uint, message domain.Message) error {
	if orderID == 0 || message.ID == 0 || message.ReceivedAt.IsZero() {
		return domain.ErrInvalidRequest
	}
	err := r.dbFor(ctx).Exec(`
INSERT INTO mailmatch_order_delivery_heads (
    order_id, message_id, message_received_at
) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE
	    message_id = IF(
	        VALUES(message_received_at) > message_received_at
	        OR (VALUES(message_received_at) = message_received_at AND VALUES(message_id) > message_id),
	        VALUES(message_id),
	        message_id
	    ),
	    message_received_at = GREATEST(VALUES(message_received_at), message_received_at)`,
		orderID,
		message.ID,
		message.ReceivedAt.UTC(),
	).Error
	if err != nil {
		return fmt.Errorf("advance purchase order delivery: %w", err)
	}
	return nil
}

func (r *Repo) ListMatchingScopesByRecipient(ctx context.Context, resourceType domain.ResourceType, emailResourceID uint, recipient string, receivedAt time.Time) ([]app.OrderScope, error) {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if emailResourceID == 0 || recipient == "" {
		return nil, nil
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	var rows []orderScopeRow
	var err error
	switch resourceType {
	case domain.ResourceTypeMicrosoft:
		err = r.dbFor(ctx).Raw(microsoftMatchingScopesSQL, emailResourceID, recipient, receivedAt, receivedAt).Scan(&rows).Error
	case domain.ResourceTypeDomain:
		err = r.dbFor(ctx).Raw(domainMatchingScopesSQL, emailResourceID, recipient, receivedAt, receivedAt).Scan(&rows).Error
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list matching mail scopes: %w", err)
	}
	return r.orderScopeRowsToScopes(ctx, rows)
}

func (r *Repo) orderScopeRowsToScopes(ctx context.Context, rows []orderScopeRow) ([]app.OrderScope, error) {
	items := make([]app.OrderScope, 0, len(rows))
	rulesByProject := make(map[uint][]app.MailRule)
	for _, row := range rows {
		rules, ok := rulesByProject[row.ProjectID]
		if !ok {
			loaded, err := r.loadMailRules(ctx, row.ProjectID)
			if err != nil {
				return nil, err
			}
			rules = loaded
			rulesByProject[row.ProjectID] = rules
		}
		items = append(items, *row.toScope(rules))
	}
	return items, nil
}

func (r *Repo) FindLatestReceivedAt(ctx context.Context, emailResourceID uint) (*time.Time, error) {
	var state FetchStateModel
	err := r.dbFor(ctx).First(&state, "email_resource_id = ?", emailResourceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find latest received at: %w", err)
	}
	return state.LastReceivedAt, nil
}

func (r *Repo) FindActiveFetchJobByResource(ctx context.Context, emailResourceID uint) (*domain.FetchJob, error) {
	var model FetchJobModel
	err := r.dbFor(ctx).Where("email_resource_id = ? AND status IN ?", emailResourceID, activeFetchStatuses()).
		Order("created_at ASC, id ASC").
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find active fetch job: %w", err)
	}
	item := fetchJobModelToDomain(model)
	return &item, nil
}

func (r *Repo) FindFetchStateForUpdate(ctx context.Context, emailResourceID uint) (*domain.FetchState, error) {
	db := r.dbFor(ctx)
	if _, ok := platform.GormTxFromContext(ctx); ok {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var model FetchStateModel
	err := db.First(&model, "email_resource_id = ?", emailResourceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find fetch state: %w", err)
	}
	item := fetchStateModelToDomain(model)
	return &item, nil
}

func (r *Repo) CreateFetchState(ctx context.Context, emailResourceID uint) (*domain.FetchState, error) {
	model := FetchStateModel{EmailResourceID: emailResourceID}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if !isDuplicateKeyError(err) {
			return nil, fmt.Errorf("create fetch state: %w", err)
		}
		return r.FindFetchStateForUpdate(ctx, emailResourceID)
	}
	item := fetchStateModelToDomain(model)
	return &item, nil
}

func (r *Repo) CreateFetchJob(ctx context.Context, job *domain.FetchJob) error {
	model := fetchJobModelFromDomain(*job)
	if model.MaxAttempts <= 0 {
		model.MaxAttempts = 3
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrFetchJobConflict
		}
		return fmt.Errorf("create fetch job: %w", err)
	}
	*job = fetchJobModelToDomain(model)
	return nil
}

func (r *Repo) MarkFetchJobQueued(ctx context.Context, jobID uint) error {
	result := r.dbFor(ctx).Model(&FetchJobModel{}).
		Where("id = ? AND status = ?", jobID, string(domain.FetchJobPending)).
		Updates(map[string]any{"status": string(domain.FetchJobQueued), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("mark fetch job queued: %w", result.Error)
	}
	return nil
}

func (r *Repo) FindFetchJob(ctx context.Context, jobID uint) (*domain.FetchJob, error) {
	var model FetchJobModel
	err := r.dbFor(ctx).First(&model, "id = ?", jobID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find fetch job: %w", err)
	}
	item := fetchJobModelToDomain(model)
	return &item, nil
}

func (r *Repo) ClaimFetchJobRunning(ctx context.Context, jobID uint, now time.Time) (bool, error) {
	result := r.dbFor(ctx).Model(&FetchJobModel{}).
		Where("id = ? AND status IN ?", jobID, []string{string(domain.FetchJobPending), string(domain.FetchJobQueued)}).
		Updates(map[string]any{
			"status":          string(domain.FetchJobRunning),
			"attempts":        gorm.Expr("attempts + 1"),
			"last_safe_error": "",
			"started_at":      now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("claim fetch job running: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *Repo) MarkFetchJobSucceeded(ctx context.Context, jobID uint, fetched int, stored int, matched int, _ *time.Time, now time.Time) error {
	return r.updateFetchJobStatus(ctx, jobID, domain.FetchJobSucceeded, map[string]any{
		"fetched_count":   fetched,
		"stored_count":    stored,
		"matched_count":   matched,
		"last_safe_error": "",
		"finished_at":     now,
		"updated_at":      now,
	})
}

func (r *Repo) MarkFetchJobSkipped(ctx context.Context, jobID uint, safeError string, now time.Time) error {
	return r.updateFetchJobStatus(ctx, jobID, domain.FetchJobSkipped, map[string]any{
		"last_safe_error": safeDiagnostic(safeError),
		"finished_at":     now,
		"updated_at":      now,
	})
}

func (r *Repo) MarkFetchJobFailed(ctx context.Context, jobID uint, safeError string, retry bool, now time.Time) error {
	status := domain.FetchJobFailed
	if retry {
		status = domain.FetchJobPending
	}
	updates := map[string]any{
		"last_safe_error": safeDiagnostic(safeError),
		"updated_at":      now,
	}
	if !retry {
		updates["finished_at"] = now
	}
	return r.updateFetchJobStatus(ctx, jobID, status, updates)
}

func (r *Repo) updateFetchJobStatus(ctx context.Context, jobID uint, status domain.FetchJobStatus, updates map[string]any) error {
	updates["status"] = string(status)
	result := r.dbFor(ctx).Model(&FetchJobModel{}).Where("id = ?", jobID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update fetch job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrFetchJobNotFound
	}
	return nil
}

func (r *Repo) ClaimDispatchableFetchJobs(ctx context.Context, limit int, staleBefore time.Time) ([]domain.FetchJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []FetchJobModel
	now := time.Now().UTC()
	err := r.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? OR (status IN ? AND updated_at < ?)",
				string(domain.FetchJobPending),
				[]string{string(domain.FetchJobQueued), string(domain.FetchJobRunning)},
				staleBefore,
			).
			Order("created_at ASC, id ASC").
			Limit(limit).
			Find(&models).Error; err != nil {
			return fmt.Errorf("claim dispatchable fetch jobs: %w", err)
		}
		if len(models) == 0 {
			return nil
		}
		staleRunningIDs := make([]uint, 0, len(models))
		for i := range models {
			if models[i].Status == string(domain.FetchJobRunning) {
				staleRunningIDs = append(staleRunningIDs, models[i].ID)
				models[i].Status = string(domain.FetchJobPending)
			}
		}
		if len(staleRunningIDs) > 0 {
			result := tx.Model(&FetchJobModel{}).
				Where("id IN ? AND status = ?", staleRunningIDs, string(domain.FetchJobRunning)).
				Updates(map[string]any{"status": string(domain.FetchJobPending), "updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("recover stale running fetch jobs: %w", result.Error)
			}
			if result.RowsAffected != int64(len(staleRunningIDs)) {
				return domain.ErrFetchJobConflict
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	items := make([]domain.FetchJob, len(models))
	for i := range models {
		items[i] = fetchJobModelToDomain(models[i])
	}
	return items, nil
}

func (r *Repo) UpdateFetchStateSubmitted(ctx context.Context, emailResourceID uint, jobID uint, status string, cooldownUntil time.Time, now time.Time) error {
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ?", emailResourceID).
		Updates(map[string]any{
			"last_job_id":       jobID,
			"last_status":       status,
			"last_submitted_at": now,
			"cooldown_until":    cooldownUntil,
			"last_safe_error":   "",
			"updated_at":        now,
		})
	if result.Error != nil {
		return fmt.Errorf("update fetch state submitted: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrFetchJobNotFound
	}
	return nil
}

func (r *Repo) UpdateFetchStateCompleted(ctx context.Context, emailResourceID uint, jobID uint, status string, lastReceivedAt *time.Time, safeError string, now time.Time) error {
	updates := map[string]any{
		"last_job_id":     jobID,
		"last_status":     status,
		"last_safe_error": safeDiagnostic(safeError),
		"updated_at":      now,
	}
	if status == string(domain.FetchJobSucceeded) {
		updates["last_success_at"] = now
	}
	if lastReceivedAt != nil {
		updates["last_received_at"] = *lastReceivedAt
	}
	result := r.dbFor(ctx).Model(&FetchStateModel{}).Where("email_resource_id = ?", emailResourceID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update fetch state completed: %w", result.Error)
	}
	return nil
}

func (r *Repo) UpsertMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	models := make([]MessageModel, len(messages))
	for i := range messages {
		models[i] = messageModelFromDomain(messages[i])
	}
	err := r.dbFor(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email_resource_id"}, {Name: "dedupe_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"recipient":           gorm.Expr("VALUES(recipient)"),
			"recipients_json":     gorm.Expr("VALUES(recipients_json)"),
			"sender":              gorm.Expr("VALUES(sender)"),
			"subject":             gorm.Expr("VALUES(subject)"),
			"matched_order_id":    gorm.Expr("COALESCE(matched_order_id, VALUES(matched_order_id))"),
			"raw_body":            gorm.Expr("IF(NULLIF(TRIM(VALUES(raw_body)), '') IS NULL, raw_body, VALUES(raw_body))"),
			"body_preview":        gorm.Expr("IF(NULLIF(TRIM(VALUES(body_preview)), '') IS NULL, body_preview, VALUES(body_preview))"),
			"verification_code":   gorm.Expr("IF(verification_code <> '', verification_code, VALUES(verification_code))"),
			"message_id_header":   gorm.Expr("VALUES(message_id_header)"),
			"provider_message_id": gorm.Expr("VALUES(provider_message_id)"),
			"protocol":            gorm.Expr("VALUES(protocol)"),
			"folder":              gorm.Expr("VALUES(folder)"),
			"status":              gorm.Expr("CASE WHEN status = 'matched' OR VALUES(status) = 'matched' THEN 'matched' WHEN status = 'ignored' OR VALUES(status) = 'ignored' THEN 'ignored' ELSE 'received' END"),
			"match_diagnostic":    gorm.Expr("IF(status = 'matched' AND VALUES(status) <> 'matched', match_diagnostic, VALUES(match_diagnostic))"),
			"received_at":         gorm.Expr("VALUES(received_at)"),
			"updated_at":          gorm.Expr("CURRENT_TIMESTAMP"),
		}),
	}).Create(&models).Error
	if err != nil {
		return nil, fmt.Errorf("upsert mailmatch messages: %w", err)
	}

	storedByIdentity := make(map[string]domain.Message, len(models))
	dedupeKeysByResource := make(map[uint][]string)
	for i := range models {
		dedupeKeysByResource[models[i].EmailResourceID] = append(dedupeKeysByResource[models[i].EmailResourceID], models[i].DedupeKey)
	}
	for resourceID, dedupeKeys := range dedupeKeysByResource {
		var rows []MessageModel
		if err := r.dbFor(ctx).Model(&MessageModel{}).
			Where("email_resource_id = ? AND dedupe_key IN ?", resourceID, dedupeKeys).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("resolve upserted mailmatch messages: %w", err)
		}
		for _, row := range rows {
			item := messageModelToDomain(row)
			storedByIdentity[messageIdentity(item.EmailResourceID, item.DedupeKey)] = item
		}
	}
	stored := make([]domain.Message, len(messages))
	for i := range messages {
		item, ok := storedByIdentity[messageIdentity(messages[i].EmailResourceID, messages[i].DedupeKey)]
		if !ok || item.ID == 0 {
			return nil, fmt.Errorf("resolve upserted mailmatch message: %w", domain.ErrMessageNotFound)
		}
		stored[i] = item
	}
	return stored, nil
}

func (r *Repo) UpdateMicrosoftRefreshToken(ctx context.Context, resourceID uint, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if resourceID == 0 || refreshToken == "" {
		return nil
	}
	result := r.dbFor(ctx).Table("microsoft_resources").
		Where("id = ? AND resource_type = ?", resourceID, string(domain.ResourceTypeMicrosoft)).
		Where("refresh_token <> ?", refreshToken).
		Updates(map[string]any{
			"refresh_token": refreshToken,
			"updated_at":    time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("update microsoft refresh token: %w", result.Error)
	}
	return nil
}

func activeFetchStatuses() []string {
	return []string{string(domain.FetchJobPending), string(domain.FetchJobQueued), string(domain.FetchJobRunning)}
}

func messageModelToDomain(model MessageModel) domain.Message {
	rawBody := ""
	if model.RawBody.Valid {
		rawBody = model.RawBody.String
	}
	return domain.Message{
		ID:                model.ID,
		EmailResourceID:   model.EmailResourceID,
		ResourceType:      domain.ResourceType(model.ResourceType),
		MatchedOrderID:    model.MatchedOrderID,
		Recipient:         model.Recipient,
		Recipients:        decodeRecipients(model.RecipientsJSON),
		Sender:            model.Sender,
		Subject:           model.Subject,
		RawBody:           rawBody,
		BodyPreview:       model.BodyPreview,
		VerificationCode:  model.VerificationCode,
		MessageIDHeader:   model.MessageIDHeader,
		ProviderMessageID: model.ProviderMessageID,
		DedupeKey:         model.DedupeKey,
		Protocol:          model.Protocol,
		Folder:            model.Folder,
		Status:            domain.MessageStatus(model.Status),
		MatchDiagnostic:   model.MatchDiagnostic,
		ReceivedAt:        model.ReceivedAt,
		CreatedAt:         model.CreatedAt,
		UpdatedAt:         model.UpdatedAt,
	}
}

func messageModelFromDomain(item domain.Message) MessageModel {
	rawBodyValue := truncateUTF8Bytes(strings.TrimSpace(item.RawBody), 64*1024)
	rawBody := sql.NullString{String: rawBodyValue, Valid: rawBodyValue != ""}
	recipientsJSON := encodeRecipients(item.Recipients, item.Recipient)
	return MessageModel{
		EmailResourceID:   item.EmailResourceID,
		ResourceType:      string(item.ResourceType),
		MatchedOrderID:    item.MatchedOrderID,
		Recipient:         strings.ToLower(strings.TrimSpace(item.Recipient)),
		RecipientsJSON:    recipientsJSON,
		Sender:            truncate(item.Sender, 255),
		Subject:           truncate(item.Subject, 500),
		RawBody:           rawBody,
		BodyPreview:       truncate(item.BodyPreview, 1000),
		VerificationCode:  truncate(item.VerificationCode, 64),
		MessageIDHeader:   truncate(item.MessageIDHeader, 500),
		ProviderMessageID: truncate(item.ProviderMessageID, 500),
		DedupeKey:         item.DedupeKey,
		Protocol:          truncate(item.Protocol, 32),
		Folder:            truncate(item.Folder, 64),
		Status:            string(item.Status),
		MatchDiagnostic:   truncate(item.MatchDiagnostic, 500),
		ReceivedAt:        item.ReceivedAt.UTC(),
	}
}

func messageIdentity(resourceID uint, dedupeKey string) string {
	return fmt.Sprintf("%d:%s", resourceID, strings.TrimSpace(dedupeKey))
}

func encodeRecipients(recipients []string, primary string) sql.NullString {
	values := normalizeRecipients(append(recipients, primary))
	if len(values) == 0 {
		return sql.NullString{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(data), Valid: true}
}

func decodeRecipients(value sql.NullString) []string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(value.String), &values); err != nil {
		return nil
	}
	return normalizeRecipients(values)
}

func normalizeRecipients(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func fetchJobModelToDomain(model FetchJobModel) domain.FetchJob {
	return domain.FetchJob{
		ID:              model.ID,
		OrderNo:         model.OrderNo,
		Purpose:         domain.FetchPurpose(model.Purpose),
		AllocationType:  domain.ResourceType(model.AllocationType),
		AllocationID:    model.AllocationID,
		ProjectID:       model.ProjectID,
		EmailResourceID: model.EmailResourceID,
		Recipient:       model.Recipient,
		Status:          domain.FetchJobStatus(model.Status),
		Attempts:        model.Attempts,
		MaxAttempts:     model.MaxAttempts,
		SinceAt:         model.SinceAt,
		UntilAt:         model.UntilAt,
		FetchedCount:    model.FetchedCount,
		StoredCount:     model.StoredCount,
		MatchedCount:    model.MatchedCount,
		LastSafeError:   model.LastSafeError,
		RequestID:       model.RequestID,
		StartedAt:       model.StartedAt,
		FinishedAt:      model.FinishedAt,
		CreatedAt:       model.CreatedAt,
		UpdatedAt:       model.UpdatedAt,
	}
}

func fetchJobModelFromDomain(item domain.FetchJob) FetchJobModel {
	return FetchJobModel{
		ID:              item.ID,
		OrderNo:         item.OrderNo,
		Purpose:         string(item.Purpose),
		AllocationType:  string(item.AllocationType),
		AllocationID:    item.AllocationID,
		ProjectID:       item.ProjectID,
		EmailResourceID: item.EmailResourceID,
		Recipient:       strings.ToLower(strings.TrimSpace(item.Recipient)),
		Status:          string(item.Status),
		Attempts:        item.Attempts,
		MaxAttempts:     item.MaxAttempts,
		SinceAt:         item.SinceAt,
		UntilAt:         item.UntilAt,
		FetchedCount:    item.FetchedCount,
		StoredCount:     item.StoredCount,
		MatchedCount:    item.MatchedCount,
		LastSafeError:   truncate(item.LastSafeError, 500),
		RequestID:       truncate(item.RequestID, 64),
		StartedAt:       item.StartedAt,
		FinishedAt:      item.FinishedAt,
	}
}

func fetchStateModelToDomain(model FetchStateModel) domain.FetchState {
	return domain.FetchState{
		EmailResourceID: model.EmailResourceID,
		LastJobID:       model.LastJobID,
		LastStatus:      model.LastStatus,
		LastSubmittedAt: model.LastSubmittedAt,
		LastSuccessAt:   model.LastSuccessAt,
		LastReceivedAt:  model.LastReceivedAt,
		CooldownUntil:   model.CooldownUntil,
		LastSafeError:   model.LastSafeError,
		CreatedAt:       model.CreatedAt,
		UpdatedAt:       model.UpdatedAt,
	}
}

func safeDiagnostic(value string) string {
	return truncate(strings.Join(strings.Fields(strings.TrimSpace(value)), " "), 500)
}

func truncate(value string, limit int) string {
	return truncateUTF8Bytes(value, limit)
}

func truncateUTF8Bytes(value string, limit int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
