package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type FetchStateModel struct {
	EmailResourceID            uint       `gorm:"primaryKey;column:email_resource_id"`
	Status                     string     `gorm:"type:varchar(32);not null;default:'normal'"`
	Generation                 uint64     `gorm:"not null;default:0"`
	Failures                   int        `gorm:"not null;default:0"`
	OperationKind              string     `gorm:"type:varchar(32);not null;default:'order_fetch';column:operation_kind"`
	OrderNo                    string     `gorm:"type:varchar(64);not null;default:'';column:order_no"`
	Purpose                    string     `gorm:"type:varchar(32);not null;default:'order_fetch'"`
	OperatorUserID             *uint      `gorm:"column:operator_user_id"`
	ExpectedCredentialRevision uint64     `gorm:"not null;default:0;column:expected_credential_revision"`
	SinceAt                    *time.Time `gorm:"column:since_at"`
	UntilAt                    *time.Time `gorm:"column:until_at"`
	FetchedCount               int        `gorm:"not null;default:0;column:fetched_count"`
	StoredCount                int        `gorm:"not null;default:0;column:stored_count"`
	MatchedCount               int        `gorm:"not null;default:0;column:matched_count"`
	RequestID                  string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	Path                       string     `gorm:"type:varchar(255);not null;default:''"`
	IdempotencyKey             string     `gorm:"type:varchar(128);not null;default:'';column:idempotency_key"`
	RequestedAt                *time.Time `gorm:"column:requested_at"`
	StartedAt                  *time.Time `gorm:"column:started_at"`
	FinishedAt                 *time.Time `gorm:"column:finished_at"`
	LastSuccessAt              *time.Time `gorm:"column:last_success_at"`
	LastReceivedAt             *time.Time `gorm:"column:last_received_at"`
	CooldownUntil              *time.Time `gorm:"column:cooldown_until"`
	LastSafeError              string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	CreatedAt                  time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt                  time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
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

type mailmatchTransactionMetricKey struct{}

func NewRepo(db *gorm.DB, files governanceapp.FilePort) *Repo {
	return &Repo{db: db, files: files}
}

func (r *Repo) WithTx(ctx context.Context, fn func(context.Context) error) (err error) {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		if ctx.Value(mailmatchTransactionMetricKey{}) != nil {
			return fn(ctx)
		}
		defer func() { recordMailmatchMySQLContention(err) }()
		return fn(context.WithValue(ctx, mailmatchTransactionMetricKey{}, struct{}{}))
	}
	defer func() { recordMailmatchMySQLContention(err) }()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		return fn(context.WithValue(txCtx, mailmatchTransactionMetricKey{}, struct{}{}))
	})
}

func mysqlContentionEvent(err error) (string, bool) {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return "", false
	}
	switch mysqlErr.Number {
	case 1205:
		return "1205", true
	case 1213:
		return "1213", true
	default:
		return "", false
	}
}

func recordMailmatchMySQLContention(err error) {
	if event, ok := mysqlContentionEvent(err); ok {
		platform.RecordMySQLTransactionEvent("mailmatch", event)
	}
}

func recordMailmatchAutocommitContention(ctx context.Context, err error) {
	if ctx.Value(mailmatchTransactionMetricKey{}) != nil {
		return
	}
	recordMailmatchMySQLContention(err)
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

func (r *Repo) ReadPickupBatch(
	ctx context.Context,
	credentials []app.PickupCredential,
	now time.Time,
	limit int,
) ([]app.PickupBatchRead, error) {
	reads := make([]app.PickupBatchRead, len(credentials))
	if len(credentials) == 0 {
		return reads, nil
	}
	if limit <= 0 {
		limit = 40
	}

	tokens := make([]string, 0, len(credentials))
	seenTokens := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		token := strings.TrimSpace(credential.Token)
		if token == "" {
			continue
		}
		if _, ok := seenTokens[token]; ok {
			continue
		}
		seenTokens[token] = struct{}{}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		for i := range reads {
			reads[i].Err = domain.ErrPickupCredentialInvalid
		}
		return reads, nil
	}

	for i := range reads {
		reads[i].Err = domain.ErrPickupCredentialInvalid
	}
	return reads, r.WithTx(ctx, func(txCtx context.Context) error {
		db := r.dbFor(txCtx)
		var scopeRows []orderScopeRow
		if err := db.Raw(pickupBatchScopeSQL, tokens).Scan(&scopeRows).Error; err != nil {
			return fmt.Errorf("load pickup batch scopes: %w", err)
		}

		scopesByToken := make(map[string]app.OrderScope, len(scopeRows))
		rowsByToken := make(map[string]orderScopeRow, len(scopeRows))
		orderIDs := make([]uint, 0, len(scopeRows))
		seenOrders := make(map[uint]struct{}, len(scopeRows))
		for _, row := range scopeRows {
			scope := *row.toScope(nil)
			scopesByToken[row.TokenPlain] = scope
			rowsByToken[row.TokenPlain] = row
		}
		validScopeRows := make([]orderScopeRow, 0, len(scopeRows))

		for i, credential := range credentials {
			scope, ok := scopesByToken[strings.TrimSpace(credential.Token)]
			if !ok || !strings.EqualFold(strings.TrimSpace(credential.Email), strings.TrimSpace(scope.Recipient)) {
				continue
			}
			reads[i].Scope = &scope
			reads[i].Err = nil
			validScopeRows = append(validScopeRows, rowsByToken[strings.TrimSpace(credential.Token)])
			if _, ok := seenOrders[scope.OrderID]; !ok {
				seenOrders[scope.OrderID] = struct{}{}
				orderIDs = append(orderIDs, scope.OrderID)
			}
		}

		if len(orderIDs) == 0 {
			return nil
		}

		deliveries, err := r.readPickupDeliveries(txCtx, orderIDs)
		if err != nil {
			return err
		}
		messages, err := r.readPickupMessages(txCtx, orderIDs, validScopeRows, now, limit)
		if err != nil {
			return err
		}

		for i := range reads {
			if reads[i].Scope == nil {
				continue
			}
			reads[i].Delivery = deliveries[reads[i].Scope.OrderID]
			reads[i].Messages = messages[reads[i].Scope.OrderID]
		}
		return nil
	})
}

func (r *Repo) readPickupDeliveries(ctx context.Context, orderIDs []uint) (map[uint]*app.OrderDelivery, error) {
	result := make(map[uint]*app.OrderDelivery, len(orderIDs))
	var heads []OrderDeliveryHeadModel
	if err := r.dbFor(ctx).Where("order_id IN ?", orderIDs).Find(&heads).Error; err != nil {
		return nil, fmt.Errorf("read pickup deliveries: %w", err)
	}
	messageIDs := make([]uint, 0, len(heads))
	for _, head := range heads {
		result[head.OrderID] = &app.OrderDelivery{ReceivedAt: head.MessageReceivedAt}
		if head.MessageID != nil {
			messageIDs = append(messageIDs, *head.MessageID)
		}
	}
	if len(messageIDs) == 0 {
		return result, nil
	}
	var models []MessageModel
	if err := r.dbFor(ctx).
		Table("mailmatch_messages AS m").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Select(projectedMessageColumns(false)).
		Where("m.id IN ?", messageIDs).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("read pickup delivery messages: %w", err)
	}
	byID := make(map[uint]domain.Message, len(models))
	for _, model := range models {
		byID[model.ID] = messageModelToDomain(model)
	}
	for _, head := range heads {
		if head.MessageID == nil {
			continue
		}
		message, ok := byID[*head.MessageID]
		if !ok {
			continue
		}
		delivery := result[head.OrderID]
		delivery.Message = &message
	}
	return result, nil
}

func (r *Repo) readPickupMessages(
	ctx context.Context,
	orderIDs []uint,
	scopeRows []orderScopeRow,
	now time.Time,
	limit int,
) (map[uint][]domain.Message, error) {
	result := make(map[uint][]domain.Message, len(orderIDs))
	start := now
	end := now
	for _, row := range scopeRows {
		candidate := now.Add(-30 * 24 * time.Hour)
		if domain.ResourceType(row.AllocationType) == domain.ResourceTypeMicrosoft {
			candidate = now.Add(-3 * 24 * time.Hour)
		}
		if row.ReceiveStartedAt != nil {
			serviceStart := row.ReceiveStartedAt.Add(-2 * time.Minute)
			if serviceStart.After(candidate) {
				candidate = serviceStart
			}
		}
		if candidate.Before(start) {
			start = candidate
		}
		if row.ServiceMode == "code" && row.ReceiveUntil != nil && row.ReceiveUntil.After(end) {
			end = *row.ReceiveUntil
		}
	}
	var models []MessageModel
	query := fmt.Sprintf(`
SELECT id, email_resource_id, resource_type, matched_order_id, recipient, recipients_json,
       sender, subject, body_preview, verification_code, message_id_header,
       provider_message_id, dedupe_key, protocol, folder, status, match_diagnostic,
       received_at, created_at, updated_at
FROM (
	    SELECT owned.*,
	           ROW_NUMBER() OVER (
	               PARTITION BY owned.matched_order_id
	               ORDER BY owned.received_at DESC, owned.id DESC
	           ) AS rn
	    FROM (
	        SELECT %s
	        FROM mailmatch_message_projections AS mp
	        JOIN mailmatch_messages AS m ON m.id = mp.message_id
	        WHERE mp.matched_order_id IN ?
	          AND mp.message_received_at >= ?
	          AND mp.message_received_at <= ?
	        UNION ALL
	        SELECT %s
	        FROM mailmatch_messages AS m
	        LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id
	        WHERE mp.message_id IS NULL
	          AND m.matched_order_id IN ?
	          AND m.received_at >= ?
	          AND m.received_at <= ?
	    ) AS owned
) AS ranked
WHERE ranked.rn <= ?
	ORDER BY ranked.matched_order_id ASC, ranked.received_at DESC, ranked.id DESC`,
		projectionOwnedMessageColumns(false), legacyMessageColumns(false))
	err := r.dbFor(ctx).Raw(query,
		orderIDs, start, end,
		orderIDs, start, end,
		limit,
	).Scan(&models).Error
	if err != nil {
		return nil, fmt.Errorf("read pickup messages: %w", err)
	}
	for _, model := range models {
		if model.MatchedOrderID == nil {
			continue
		}
		orderID := *model.MatchedOrderID
		result[orderID] = append(result[orderID], messageModelToDomain(model))
	}
	return result, nil
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
	TokenPlain         string
	OrderID            uint
	OrderNo            string
	UserID             uint
	ProjectID          uint
	ProductID          uint
	ServiceMode        string
	OrderStatus        string
	AllocationType     string
	AllocationID       uint
	RecipientKind      string
	EmailResourceID    uint
	Recipient          string
	ReceiveStartedAt   *time.Time
	ReceiveUntil       *time.Time
	ActivatedAt        *time.Time
	AfterSaleUntil     *time.Time
	LooseMatch         bool
	MicrosoftEmail     string
	MicrosoftClientID  string
	MicrosoftRT        string
	CredentialRevision uint64
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
    COALESCE(mr.refresh_token, '') AS microsoft_rt,
    COALESCE(mr.credential_revision, 0) AS credential_revision
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
    '' AS microsoft_rt,
    0 AS credential_revision
FROM order_tokens t
JOIN orders o ON o.order_no = t.order_no
JOIN projects p ON p.id = o.project_id
LEFT JOIN microsoft_allocations ma
  ON ma.id = o.microsoft_alloc_id
 AND o.allocation_type = 'microsoft'
 AND ma.order_no = o.order_no
LEFT JOIN domain_allocations da
  ON da.id = o.domain_alloc_id
 AND o.allocation_type = 'domain'
 AND da.order_no = o.order_no
WHERE t.token_plain = ?
  AND t.enabled = 1
  AND (t.expire_at IS NULL OR t.expire_at > UTC_TIMESTAMP())
  AND (
    (o.allocation_type = 'microsoft' AND ma.order_no = o.order_no AND ma.email = ?)
    OR
    (o.allocation_type = 'domain' AND da.order_no = o.order_no AND da.email = ?)
  )
LIMIT 1`

const pickupBatchScopeSQL = `
SELECT
    t.token_plain,
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
    '' AS microsoft_rt,
    COALESCE(mr.credential_revision, 0) AS credential_revision
FROM order_tokens t
JOIN orders o ON o.order_no = t.order_no
JOIN projects p ON p.id = o.project_id
LEFT JOIN microsoft_allocations ma
  ON ma.id = o.microsoft_alloc_id
 AND o.allocation_type = 'microsoft'
 AND ma.order_no = o.order_no
LEFT JOIN microsoft_resources mr ON mr.id = ma.resource_id
LEFT JOIN domain_allocations da
  ON da.id = o.domain_alloc_id
 AND o.allocation_type = 'domain'
 AND da.order_no = o.order_no
WHERE t.token_plain IN ?
  AND t.enabled = 1
  AND (t.expire_at IS NULL OR t.expire_at > UTC_TIMESTAMP())`

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
    COALESCE(mr.refresh_token, '') AS microsoft_rt,
    COALESCE(mr.credential_revision, 0) AS credential_revision
FROM microsoft_allocations ma
JOIN orders o ON o.microsoft_alloc_id = ma.id AND o.allocation_type = 'microsoft'
JOIN projects p ON p.id = o.project_id
JOIN microsoft_resources mr ON mr.id = ma.resource_id
WHERE ma.resource_id = ?
  AND (
    ma.email = ?
    OR (
      ma.mailbox IN ('main', 'alias')
      AND SUBSTRING_INDEX(ma.email, '@', -1) = ?
      AND REPLACE(SUBSTRING_INDEX(ma.email, '@', 1), '.', '') = ?
    )
  )
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
    '' AS microsoft_rt,
    0 AS credential_revision
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
		OrderID:            r.OrderID,
		OrderNo:            r.OrderNo,
		UserID:             r.UserID,
		ProjectID:          r.ProjectID,
		ProductID:          r.ProductID,
		ServiceMode:        r.ServiceMode,
		OrderStatus:        r.OrderStatus,
		AllocationType:     domain.ResourceType(r.AllocationType),
		AllocationID:       r.AllocationID,
		RecipientKind:      strings.ToLower(strings.TrimSpace(r.RecipientKind)),
		EmailResourceID:    r.EmailResourceID,
		Recipient:          strings.ToLower(strings.TrimSpace(r.Recipient)),
		ReceiveStartedAt:   r.ReceiveStartedAt,
		ReceiveUntil:       r.ReceiveUntil,
		ActivatedAt:        r.ActivatedAt,
		AfterSaleUntil:     r.AfterSaleUntil,
		LooseMatch:         r.LooseMatch,
		Rules:              rules,
		MicrosoftEmail:     r.MicrosoftEmail,
		MicrosoftClientID:  r.MicrosoftClientID,
		MicrosoftRT:        r.MicrosoftRT,
		CredentialRevision: r.CredentialRevision,
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
	var models []MessageModel
	query := fmt.Sprintf(`
SELECT *
FROM (
    SELECT %s
    FROM mailmatch_message_projections AS mp
    JOIN mailmatch_messages AS m ON m.id = mp.message_id
    WHERE mp.matched_order_id = ?
      AND mp.message_received_at >= ?
      AND mp.message_received_at <= ?
    UNION ALL
    SELECT %s
    FROM mailmatch_messages AS m
    LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id
    WHERE mp.message_id IS NULL
      AND m.matched_order_id = ?
      AND m.received_at >= ?
      AND m.received_at <= ?
) AS owned
ORDER BY owned.received_at DESC, owned.id DESC
LIMIT ?`, projectionOwnedMessageColumns(false), legacyMessageColumns(false))
	if err := r.dbFor(ctx).Raw(
		query,
		scope.OrderID, start, end,
		scope.OrderID, start, end,
		limit,
	).Scan(&models).Error; err != nil {
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
		Table("mailmatch_messages AS m").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Select(projectedMessageColumns(true)).
		Where("m.id = ? AND "+effectiveMessageOwnerSQL+" = ?", messageID, orderID).
		Take(&model).Error; err != nil {
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
		Table("mailmatch_messages AS m").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Select(projectedMessageColumns(false)).
		Where("m.id = ?", *head.MessageID).
		Take(&model).Error; err != nil {
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
		_, _, canonical, ok := domain.RecipientAliasForms(recipient)
		if !ok {
			return nil, nil
		}
		canonicalLocal, canonicalHost, _ := strings.Cut(canonical, "@")
		err = r.dbFor(ctx).Raw(
			microsoftMatchingScopesSQL,
			emailResourceID,
			recipient,
			canonicalHost,
			canonicalLocal,
			receivedAt,
			receivedAt,
		).Scan(&rows).Error
		if err == nil {
			exact := make([]orderScopeRow, 0, len(rows))
			for _, row := range rows {
				if strings.EqualFold(strings.TrimSpace(row.Recipient), recipient) {
					exact = append(exact, row)
				}
			}
			if len(exact) > 0 {
				rows = exact
			}
		}
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

func (r *Repo) EnsureFetchStates(ctx context.Context, emailResourceIDs []uint) error {
	ids := sortedUniqueResourceIDs(emailResourceIDs)
	if len(ids) == 0 {
		return nil
	}
	models := make([]FetchStateModel, len(ids))
	for i, id := range ids {
		models[i].EmailResourceID = id
	}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&models).Error; err != nil {
		return fmt.Errorf("ensure fetch states: %w", err)
	}
	return nil
}

func (r *Repo) FindFetchStatesForUpdate(ctx context.Context, emailResourceIDs []uint) (map[uint]*domain.FetchState, error) {
	ids := sortedUniqueResourceIDs(emailResourceIDs)
	result := make(map[uint]*domain.FetchState, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var models []FetchStateModel
	if err := r.dbFor(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("email_resource_id IN ?", ids).
		Order("email_resource_id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("lock fetch states: %w", err)
	}
	for _, model := range models {
		state := fetchStateModelToDomain(model)
		result[model.EmailResourceID] = &state
	}
	return result, nil
}

func (r *Repo) RequestFetch(ctx context.Context, job *domain.FetchJob, cooldownUntil time.Time, now time.Time) error {
	if job == nil || job.EmailResourceID == 0 {
		return domain.ErrInvalidRequest
	}
	return r.RequestFetchBatch(ctx, []*domain.FetchJob{job}, cooldownUntil, now)
}

func (r *Repo) RequestFetchBatch(ctx context.Context, jobs []*domain.FetchJob, cooldownUntil time.Time, now time.Time) error {
	if len(jobs) == 0 {
		return nil
	}
	for _, job := range jobs {
		if job == nil || job.EmailResourceID == 0 {
			return domain.ErrInvalidRequest
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].EmailResourceID < jobs[j].EmailResourceID
	})
	models := make([]FetchStateModel, len(jobs))
	ids := make([]uint, len(jobs))
	for i, job := range jobs {
		if i > 0 && jobs[i-1].EmailResourceID == job.EmailResourceID {
			return domain.ErrInvalidRequest
		}
		ids[i] = job.EmailResourceID
		models[i] = FetchStateModel{
			EmailResourceID:            job.EmailResourceID,
			Status:                     string(domain.FetchJobPending),
			Generation:                 1,
			OperationKind:              "order_fetch",
			OrderNo:                    strings.TrimSpace(job.OrderNo),
			Purpose:                    string(job.Purpose),
			ExpectedCredentialRevision: job.ExpectedCredentialRevision,
			SinceAt:                    job.SinceAt,
			UntilAt:                    job.UntilAt,
			RequestID:                  strings.TrimSpace(job.RequestID),
			RequestedAt:                &now,
			CooldownUntil:              &cooldownUntil,
		}
	}
	db := r.dbFor(ctx)
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email_resource_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"status":                       gorm.Expr("VALUES(status)"),
			"generation":                   gorm.Expr("generation + 1"),
			"failures":                     0,
			"operation_kind":               gorm.Expr("VALUES(operation_kind)"),
			"order_no":                     gorm.Expr("VALUES(order_no)"),
			"purpose":                      gorm.Expr("VALUES(purpose)"),
			"operator_user_id":             nil,
			"expected_credential_revision": gorm.Expr("VALUES(expected_credential_revision)"),
			"since_at":                     gorm.Expr("VALUES(since_at)"),
			"until_at":                     gorm.Expr("VALUES(until_at)"),
			"fetched_count":                0,
			"stored_count":                 0,
			"matched_count":                0,
			"request_id":                   gorm.Expr("VALUES(request_id)"),
			"path":                         "",
			"idempotency_key":              "",
			"requested_at":                 gorm.Expr("VALUES(requested_at)"),
			"started_at":                   nil,
			"finished_at":                  nil,
			"cooldown_until":               gorm.Expr("VALUES(cooldown_until)"),
			"last_safe_error":              "",
		}),
	}).Create(&models).Error
	if err != nil {
		return fmt.Errorf("request fetch batch: %w", err)
	}
	var saved []FetchStateModel
	if err := db.Where("email_resource_id IN ?", ids).Order("email_resource_id ASC").Find(&saved).Error; err != nil {
		return fmt.Errorf("read requested fetch states: %w", err)
	}
	if len(saved) != len(jobs) {
		return fmt.Errorf("read requested fetch states: expected %d rows, got %d", len(jobs), len(saved))
	}
	byID := make(map[uint]domain.FetchJob, len(saved))
	for _, model := range saved {
		byID[model.EmailResourceID] = fetchStateModelToJob(model)
	}
	for _, job := range jobs {
		savedJob, ok := byID[job.EmailResourceID]
		if !ok {
			return fmt.Errorf("read requested fetch state %d: %w", job.EmailResourceID, domain.ErrFetchJobConflict)
		}
		*job = savedJob
	}
	return nil
}

func sortedUniqueResourceIDs(ids []uint) []uint {
	result := append([]uint(nil), ids...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	write := 0
	for _, id := range result {
		if id == 0 || (write > 0 && result[write-1] == id) {
			continue
		}
		result[write] = id
		write++
	}
	return result[:write]
}

func (r *Repo) ListPendingFetches(ctx context.Context, limit int) ([]domain.FetchJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var models []FetchStateModel
	if err := r.dbFor(ctx).
		Where("status = ? AND operation_kind = ?", string(domain.FetchJobPending), "order_fetch").
		Order("requested_at ASC, email_resource_id ASC").Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending fetches: %w", err)
	}
	items := make([]domain.FetchJob, len(models))
	for i := range models {
		items[i] = fetchStateModelToJob(models[i])
	}
	return items, nil
}

func (r *Repo) MarkFetchProcessing(ctx context.Context, emailResourceID uint, generation uint64, now time.Time) (bool, error) {
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobPending)).
		Updates(map[string]any{
			"status": string(domain.FetchJobRunning), "started_at": now, "finished_at": nil, "last_safe_error": "",
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark fetch processing: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	err := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
		Count(&count).Error
	return count == 1, err
}

func (r *Repo) FindFetch(ctx context.Context, emailResourceID uint, generation uint64) (*domain.FetchJob, error) {
	var model FetchStateModel
	err := r.dbFor(ctx).First(&model, "email_resource_id = ? AND generation = ?", emailResourceID, generation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find fetch: %w", err)
	}
	job := fetchStateModelToJob(model)
	return &job, nil
}

func (r *Repo) AssertFetchFence(ctx context.Context, emailResourceID uint, generation uint64) error {
	var count int64
	err := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("assert fetch fence: %w", err)
	}
	if count != 1 {
		return domain.ErrFetchJobConflict
	}
	return nil
}

func (r *Repo) CompleteFetch(ctx context.Context, emailResourceID uint, generation uint64, fetched int, stored int, matched int, lastReceivedAt *time.Time, now time.Time) (bool, error) {
	updates := map[string]any{
		"status": string(domain.FetchJobSucceeded), "failures": 0,
		"fetched_count": max(fetched, 0), "stored_count": max(stored, 0), "matched_count": max(matched, 0),
		"last_success_at": now, "last_safe_error": "", "finished_at": now,
	}
	if lastReceivedAt != nil {
		updates["last_received_at"] = *lastReceivedAt
	}
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
		Updates(updates)
	if result.Error != nil {
		return false, fmt.Errorf("complete fetch: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *Repo) SkipFetch(ctx context.Context, emailResourceID uint, generation uint64, safeError string, now time.Time) (bool, error) {
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
		Updates(map[string]any{
			"status": string(domain.FetchJobSucceeded), "failures": 0,
			"last_safe_error": safeDiagnostic(safeError), "finished_at": now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("skip fetch: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *Repo) ReleaseFetchInfrastructureFailure(ctx context.Context, emailResourceID uint, generation uint64, safeError string) (bool, error) {
	result := r.dbFor(ctx).Model(&FetchStateModel{}).
		Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
		Updates(map[string]any{
			"status": string(domain.FetchJobPending), "generation": gorm.Expr("generation + 1"),
			"started_at": nil, "last_safe_error": safeDiagnostic(safeError),
		})
	if result.Error != nil {
		return false, fmt.Errorf("release fetch infrastructure failure: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *Repo) RecordFetchFailure(ctx context.Context, emailResourceID uint, generation uint64, safeError string, retryable bool, now time.Time) (recorded bool, abnormal bool, err error) {
	err = r.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var model FetchStateModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
			First(&model).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		failures := model.Failures + 1
		status := string(domain.FetchJobPending)
		if !retryable || failures >= 3 {
			status = string(domain.FetchJobFailed)
			failures = min(failures, 3)
			abnormal = true
		}
		updates := map[string]any{
			"status": status, "failures": failures, "last_safe_error": safeDiagnostic(safeError), "started_at": nil,
		}
		if abnormal {
			updates["finished_at"] = now
		}
		result := tx.Model(&FetchStateModel{}).
			Where("email_resource_id = ? AND generation = ? AND status = ?", emailResourceID, generation, string(domain.FetchJobRunning)).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		recorded = result.RowsAffected == 1
		return nil
	})
	return recorded, abnormal && recorded, err
}

func (r *Repo) UpsertMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	models := make([]MessageModel, len(messages))
	for i := range messages {
		models[i] = messageModelFromDomain(messages[i])
	}
	// Keep unique-key locks deterministic across concurrent resource fetches.
	// Results are rebuilt from message identity below, so input order is kept.
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].EmailResourceID != models[j].EmailResourceID {
			return models[i].EmailResourceID < models[j].EmailResourceID
		}
		return models[i].DedupeKey < models[j].DedupeKey
	})
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

func fetchStateModelToJob(model FetchStateModel) domain.FetchJob {
	createdAt := model.CreatedAt
	if model.RequestedAt != nil {
		createdAt = *model.RequestedAt
	}
	return domain.FetchJob{
		ID:                         model.EmailResourceID,
		Generation:                 model.Generation,
		ExpectedCredentialRevision: model.ExpectedCredentialRevision,
		OrderNo:                    model.OrderNo,
		Purpose:                    domain.FetchPurpose(model.Purpose),
		EmailResourceID:            model.EmailResourceID,
		Status:                     domain.FetchJobStatus(model.Status),
		Attempts:                   model.Failures,
		MaxAttempts:                3,
		SinceAt:                    model.SinceAt,
		UntilAt:                    model.UntilAt,
		FetchedCount:               model.FetchedCount,
		StoredCount:                model.StoredCount,
		MatchedCount:               model.MatchedCount,
		LastSafeError:              model.LastSafeError,
		RequestID:                  model.RequestID,
		StartedAt:                  model.StartedAt,
		FinishedAt:                 model.FinishedAt,
		CreatedAt:                  createdAt,
		UpdatedAt:                  model.UpdatedAt,
	}
}

func fetchStateModelToDomain(model FetchStateModel) domain.FetchState {
	return domain.FetchState{
		EmailResourceID: model.EmailResourceID, Generation: model.Generation, Failures: model.Failures,
		OperationKind: model.OperationKind, OrderNo: model.OrderNo, Purpose: domain.FetchPurpose(model.Purpose),
		OperatorUserID: model.OperatorUserID, CredentialRev: model.ExpectedCredentialRevision,
		SinceAt: model.SinceAt, UntilAt: model.UntilAt,
		FetchedCount: model.FetchedCount, StoredCount: model.StoredCount, MatchedCount: model.MatchedCount,
		LastStatus: model.Status, LastSubmittedAt: model.RequestedAt,
		LastSuccessAt: model.LastSuccessAt, LastReceivedAt: model.LastReceivedAt,
		CooldownUntil: model.CooldownUntil, LastSafeError: model.LastSafeError,
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
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
