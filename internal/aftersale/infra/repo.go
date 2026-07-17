package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	previewMaxLen    = 200
	systemSenderName = "系统"
)

type TicketModel struct {
	ID                    uint       `gorm:"primaryKey;autoIncrement"`
	TicketNo              string     `gorm:"type:varchar(64);not null;column:ticket_no"`
	TicketType            string     `gorm:"type:varchar(32);not null;column:ticket_type"`
	Title                 string     `gorm:"type:varchar(200);not null"`
	Status                string     `gorm:"type:varchar(32);not null"`
	RequesterUserID       uint       `gorm:"not null;column:requester_user_id"`
	ReplyToken            string     `gorm:"type:varchar(64);not null;column:reply_token"`
	OrderNo               string     `gorm:"type:varchar(64);not null;column:order_no"`
	ProjectName           string     `gorm:"type:varchar(255);not null;column:project_name"`
	ProjectLogoURL        string     `gorm:"type:varchar(1024);not null;column:project_logo_url"`
	DeliveryEmail         string     `gorm:"type:varchar(255);not null;column:delivery_email"`
	PayAmount             string     `gorm:"type:decimal(18,6);not null;column:pay_amount"`
	ServiceMode           string     `gorm:"type:varchar(32);not null;column:service_mode"`
	AfterSaleUntil        *time.Time `gorm:"column:after_sale_until"`
	ResolutionKind        string     `gorm:"type:varchar(32);not null;column:resolution_kind"`
	RefundAmount          string     `gorm:"type:decimal(18,6);not null;column:refund_amount"`
	RequesterUnreadCount  int        `gorm:"not null;column:requester_unread_count"`
	PlatformUnreadCount   int        `gorm:"not null;column:platform_unread_count"`
	LastMessagePreview    string     `gorm:"type:varchar(500);not null;column:last_message_preview"`
	LastMessageSenderType string     `gorm:"type:varchar(32);not null;column:last_message_sender_type"`
	LastMessageAt         *time.Time `gorm:"column:last_message_at"`
	CreatedAt             time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt             time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (TicketModel) TableName() string { return "aftersale_tickets" }

type TicketMessageModel struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	TicketNo     string    `gorm:"type:varchar(64);not null;column:ticket_no"`
	SenderType   string    `gorm:"type:varchar(32);not null;column:sender_type"`
	SenderUserID uint      `gorm:"not null;column:sender_user_id"`
	SenderName   string    `gorm:"type:varchar(128);not null;column:sender_name"`
	SenderEmail  string    `gorm:"type:varchar(255);not null;column:sender_email"`
	Content      string    `gorm:"type:varchar(2000);not null"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (TicketMessageModel) TableName() string { return "aftersale_ticket_messages" }

type TicketAttachmentModel struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	AttachmentNo string    `gorm:"type:varchar(64);not null;column:attachment_no"`
	TicketNo     string    `gorm:"type:varchar(64);not null;column:ticket_no"`
	MessageID    uint      `gorm:"not null;column:message_id"`
	ObjectKey    string    `gorm:"type:varchar(255);not null;column:object_key"`
	Mime         string    `gorm:"type:varchar(128);not null"`
	Size         int       `gorm:"not null"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (TicketAttachmentModel) TableName() string { return "aftersale_ticket_attachments" }

type Repo struct {
	db  *gorm.DB
	now func() time.Time
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) withTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx))
	})
}

func (r *Repo) Create(ctx context.Context, params aftersaleapp.CreateTicketParams) (*domain.Ticket, error) {
	now := r.now()
	model := TicketModel{
		TicketNo:              params.TicketNo,
		TicketType:            string(params.TicketType),
		Title:                 params.Title,
		Status:                string(domain.TicketStatusOpen),
		RequesterUserID:       params.RequesterUserID,
		ReplyToken:            params.ReplyToken,
		PayAmount:             "0",
		RefundAmount:          "0",
		PlatformUnreadCount:   1,
		LastMessagePreview:    preview(params.FirstMessage.Content),
		LastMessageSenderType: string(params.FirstMessage.SenderType),
		LastMessageAt:         &now,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if params.Order != nil {
		model.OrderNo = params.Order.OrderNo
		model.ProjectName = params.Order.ProjectName
		model.ProjectLogoURL = params.Order.ProjectLogoURL
		model.DeliveryEmail = params.Order.DeliveryEmail
		if amount := strings.TrimSpace(params.Order.PayAmount); amount != "" {
			model.PayAmount = amount
		}
		model.ServiceMode = params.Order.ServiceMode
		model.AfterSaleUntil = params.Order.AfterSaleUntil
	}
	err := r.withTx(ctx, func(txCtx context.Context) error {
		if err := r.dbFor(txCtx).Create(&model).Error; err != nil {
			return fmt.Errorf("create ticket: %w", err)
		}
		return r.insertMessage(txCtx, params.TicketNo, params.FirstMessage, now)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, params.TicketNo, true)
}

func (r *Repo) Get(ctx context.Context, ticketNo string, withMessages bool) (*domain.Ticket, error) {
	var model TicketModel
	if err := r.dbFor(ctx).Where("ticket_no = ?", ticketNo).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrTicketNotFound
		}
		return nil, fmt.Errorf("find ticket: %w", err)
	}
	ticket := ticketModelToDomain(model)
	if withMessages {
		messages, err := r.loadMessages(ctx, ticketNo)
		if err != nil {
			return nil, err
		}
		ticket.Messages = messages
	}
	return &ticket, nil
}

func (r *Repo) List(ctx context.Context, filter aftersaleapp.ListFilter, offset int, afterID uint, limit int) ([]domain.Ticket, *uint, error) {
	query := applyTicketFilter(r.dbFor(ctx).Model(&TicketModel{}), filter)
	if afterID > 0 {
		query = query.Where("id < ?", afterID)
	} else if offset > 0 {
		query = query.Offset(offset)
	}
	var models []TicketModel
	if err := query.Order("id DESC").Limit(limit + 1).Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("list tickets: %w", err)
	}
	var nextAfterID *uint
	if len(models) > limit {
		models = models[:limit]
		next := models[len(models)-1].ID
		nextAfterID = &next
	}
	items := make([]domain.Ticket, len(models))
	for i := range models {
		items[i] = ticketModelToDomain(models[i])
	}
	return items, nextAfterID, nil
}

func (r *Repo) Count(ctx context.Context, filter aftersaleapp.ListFilter) (int64, error) {
	var total int64
	if err := applyTicketFilter(r.dbFor(ctx).Model(&TicketModel{}), filter).Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count tickets: %w", err)
	}
	return total, nil
}

// Facets computes list aggregates; each dimension excludes its own filter value
// so the console can render selectable counts.
func (r *Repo) Facets(ctx context.Context, filter aftersaleapp.ListFilter) (*aftersaleapp.TicketFacets, error) {
	facets := &aftersaleapp.TicketFacets{}

	typeBase := filter
	typeBase.TicketType = ""
	var typeRow struct {
		All     int64 `gorm:"column:all_count"`
		Order   int64 `gorm:"column:order_count"`
		General int64 `gorm:"column:general_count"`
	}
	if err := applyTicketFilter(r.dbFor(ctx).Model(&TicketModel{}), typeBase).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN ticket_type = ? THEN 1 ELSE 0 END), 0) AS order_count,
			COALESCE(SUM(CASE WHEN ticket_type = ? THEN 1 ELSE 0 END), 0) AS general_count`,
			string(domain.TicketTypeOrder),
			string(domain.TicketTypeGeneral),
		).
		Scan(&typeRow).Error; err != nil {
		return nil, fmt.Errorf("ticket type facets: %w", err)
	}
	facets.TicketType = aftersaleapp.TicketTypeFacets{All: typeRow.All, Order: typeRow.Order, General: typeRow.General}

	statusBase := filter
	statusBase.Status = ""
	var statusRow struct {
		All        int64 `gorm:"column:all_count"`
		Open       int64 `gorm:"column:open_count"`
		Processing int64 `gorm:"column:processing_count"`
		Closed     int64 `gorm:"column:closed_count"`
	}
	if err := applyTicketFilter(r.dbFor(ctx).Model(&TicketModel{}), statusBase).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS open_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS processing_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS closed_count`,
			string(domain.TicketStatusOpen),
			string(domain.TicketStatusProcessing),
			string(domain.TicketStatusClosed),
		).
		Scan(&statusRow).Error; err != nil {
		return nil, fmt.Errorf("ticket status facets: %w", err)
	}
	facets.Status = aftersaleapp.TicketStatusFacets{All: statusRow.All, Open: statusRow.Open, Processing: statusRow.Processing, Closed: statusRow.Closed}

	return facets, nil
}

func (r *Repo) Reply(ctx context.Context, params aftersaleapp.ReplyParams) (*domain.Ticket, error) {
	now := r.now()
	err := r.withTx(ctx, func(txCtx context.Context) error {
		model, err := r.lockTicket(txCtx, params.TicketNo)
		if err != nil {
			return err
		}
		if domain.TicketStatus(model.Status).IsTerminal() {
			return domain.ErrTicketClosed
		}
		if err := r.insertMessage(txCtx, params.TicketNo, params.Message, now); err != nil {
			return err
		}
		updates := map[string]any{
			"last_message_preview":     preview(params.Message.Content),
			"last_message_sender_type": string(params.Message.SenderType),
			"last_message_at":          now,
			"updated_at":               now,
		}
		if params.Message.SenderType == domain.SenderTypePlatform {
			updates["platform_unread_count"] = 0
			updates["requester_unread_count"] = gorm.Expr("requester_unread_count + 1")
			if model.Status == string(domain.TicketStatusOpen) {
				updates["status"] = string(domain.TicketStatusProcessing)
			}
		} else {
			updates["requester_unread_count"] = 0
			updates["platform_unread_count"] = gorm.Expr("platform_unread_count + 1")
		}
		return r.applyTicketUpdates(txCtx, params.TicketNo, updates)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, params.TicketNo, true)
}

func (r *Repo) MarkRead(ctx context.Context, ticketNo string, platformSide bool) (*domain.Ticket, error) {
	column := "requester_unread_count"
	if platformSide {
		column = "platform_unread_count"
	}
	// UpdateColumn skips autoUpdateTime so marking read never reorders the inbox.
	result := r.dbFor(ctx).Model(&TicketModel{}).Where("ticket_no = ?", ticketNo).UpdateColumn(column, 0)
	if result.Error != nil {
		return nil, fmt.Errorf("mark ticket read: %w", result.Error)
	}
	return r.Get(ctx, ticketNo, false)
}

func (r *Repo) Close(ctx context.Context, params aftersaleapp.CloseParams) (*domain.Ticket, error) {
	now := r.now()
	err := r.withTx(ctx, func(txCtx context.Context) error {
		model, err := r.lockTicket(txCtx, params.TicketNo)
		if err != nil {
			return err
		}
		if domain.TicketStatus(model.Status).IsTerminal() {
			return domain.ErrTicketStateConflict
		}
		systemMessage := aftersaleapp.MessageInsert{
			SenderType: domain.SenderTypeSystem,
			SenderName: systemSenderName,
			Content:    params.SystemMessage,
		}
		if err := r.insertMessage(txCtx, params.TicketNo, systemMessage, now); err != nil {
			return err
		}
		updates := map[string]any{
			"status":                   string(domain.TicketStatusClosed),
			"resolution_kind":          string(params.Resolution.Kind),
			"last_message_preview":     preview(params.SystemMessage),
			"last_message_sender_type": string(domain.SenderTypeSystem),
			"last_message_at":          now,
			"updated_at":               now,
		}
		if amount := strings.TrimSpace(params.Resolution.RefundAmount); amount != "" {
			updates["refund_amount"] = amount
		}
		if params.By == domain.SenderTypePlatform {
			updates["platform_unread_count"] = 0
			updates["requester_unread_count"] = gorm.Expr("requester_unread_count + 1")
		} else {
			updates["requester_unread_count"] = 0
			updates["platform_unread_count"] = gorm.Expr("platform_unread_count + 1")
		}
		return r.applyTicketUpdates(txCtx, params.TicketNo, updates)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, params.TicketNo, true)
}

func (r *Repo) FindAttachment(ctx context.Context, ticketNo, attachmentNo string) (*domain.TicketAttachment, error) {
	var model TicketAttachmentModel
	if err := r.dbFor(ctx).Where("ticket_no = ? AND attachment_no = ?", ticketNo, attachmentNo).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("find attachment: %w", err)
	}
	attachment := attachmentModelToDomain(model)
	return &attachment, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (r *Repo) lockTicket(ctx context.Context, ticketNo string) (*TicketModel, error) {
	var model TicketModel
	if err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("ticket_no = ?", ticketNo).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrTicketNotFound
		}
		return nil, fmt.Errorf("lock ticket: %w", err)
	}
	return &model, nil
}

func (r *Repo) applyTicketUpdates(ctx context.Context, ticketNo string, updates map[string]any) error {
	if err := r.dbFor(ctx).Model(&TicketModel{}).Where("ticket_no = ?", ticketNo).Updates(updates).Error; err != nil {
		return fmt.Errorf("update ticket: %w", err)
	}
	return nil
}

func (r *Repo) insertMessage(ctx context.Context, ticketNo string, msg aftersaleapp.MessageInsert, at time.Time) error {
	model := TicketMessageModel{
		TicketNo:     ticketNo,
		SenderType:   string(msg.SenderType),
		SenderUserID: msg.SenderUserID,
		SenderName:   msg.SenderName,
		SenderEmail:  msg.SenderEmail,
		Content:      msg.Content,
		CreatedAt:    at,
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	for _, attachment := range msg.Attachments {
		row := TicketAttachmentModel{
			AttachmentNo: attachment.AttachmentNo,
			TicketNo:     ticketNo,
			MessageID:    model.ID,
			ObjectKey:    attachment.ObjectKey,
			Mime:         attachment.Mime,
			Size:         attachment.Size,
			CreatedAt:    at,
		}
		if err := r.dbFor(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("insert attachment: %w", err)
		}
	}
	return nil
}

func (r *Repo) loadMessages(ctx context.Context, ticketNo string) ([]domain.TicketMessage, error) {
	var models []TicketMessageModel
	if err := r.dbFor(ctx).Where("ticket_no = ?", ticketNo).Order("id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	if len(models) == 0 {
		return nil, nil
	}
	ids := make([]uint, len(models))
	for i := range models {
		ids[i] = models[i].ID
	}
	var attachmentModels []TicketAttachmentModel
	if err := r.dbFor(ctx).Where("message_id IN ?", ids).Order("id ASC").Find(&attachmentModels).Error; err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	byMessage := make(map[uint][]domain.TicketAttachment, len(models))
	for _, attachment := range attachmentModels {
		byMessage[attachment.MessageID] = append(byMessage[attachment.MessageID], attachmentModelToDomain(attachment))
	}
	out := make([]domain.TicketMessage, len(models))
	for i := range models {
		out[i] = messageModelToDomain(models[i])
		out[i].Attachments = byMessage[models[i].ID]
	}
	return out, nil
}

func applyTicketFilter(query *gorm.DB, filter aftersaleapp.ListFilter) *gorm.DB {
	if !filter.IsAdmin || filter.Scope != "all" {
		query = query.Where("requester_user_id = ?", filter.RequesterUserID)
	}
	if filter.TicketType != "" {
		query = query.Where("ticket_type = ?", string(filter.TicketType))
	}
	if filter.Status != "" {
		query = query.Where("status = ?", string(filter.Status))
	}
	if filter.CreatedFrom != nil {
		query = query.Where("created_at >= ?", filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		query = query.Where("created_at <= ?", filter.CreatedTo.UTC())
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := escapeLikePattern(search) + "%"
		query = query.Where(
			"ticket_no LIKE ? OR title LIKE ? OR order_no LIKE ? OR delivery_email LIKE ? OR project_name LIKE ?",
			like, like, like, like, like,
		)
	}
	return query
}

func escapeLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func preview(content string) string {
	collapsed := strings.Join(strings.Fields(content), " ")
	runes := []rune(collapsed)
	if len(runes) > previewMaxLen {
		return string(runes[:previewMaxLen])
	}
	return collapsed
}

func ticketModelToDomain(m TicketModel) domain.Ticket {
	ticket := domain.Ticket{
		ID:                    m.ID,
		TicketNo:              m.TicketNo,
		TicketType:            domain.TicketType(m.TicketType),
		Title:                 m.Title,
		Status:                domain.TicketStatus(m.Status),
		RequesterUserID:       m.RequesterUserID,
		ReplyToken:            m.ReplyToken,
		RequesterUnreadCount:  m.RequesterUnreadCount,
		PlatformUnreadCount:   m.PlatformUnreadCount,
		LastMessagePreview:    m.LastMessagePreview,
		LastMessageSenderType: domain.SenderType(m.LastMessageSenderType),
		LastMessageAt:         m.LastMessageAt,
		CreatedAt:             m.CreatedAt,
		UpdatedAt:             m.UpdatedAt,
	}
	if m.TicketType == string(domain.TicketTypeOrder) && strings.TrimSpace(m.OrderNo) != "" {
		ticket.Order = &domain.OrderSnapshot{
			OrderNo:        m.OrderNo,
			ProjectName:    m.ProjectName,
			ProjectLogoURL: m.ProjectLogoURL,
			DeliveryEmail:  m.DeliveryEmail,
			PayAmount:      m.PayAmount,
			ServiceMode:    m.ServiceMode,
			AfterSaleUntil: m.AfterSaleUntil,
		}
	}
	if m.ResolutionKind != "" {
		ticket.Resolution = &domain.Resolution{
			Kind:         domain.ResolutionKind(m.ResolutionKind),
			RefundAmount: m.RefundAmount,
		}
	}
	return ticket
}

func messageModelToDomain(m TicketMessageModel) domain.TicketMessage {
	return domain.TicketMessage{
		ID:           m.ID,
		TicketNo:     m.TicketNo,
		SenderType:   domain.SenderType(m.SenderType),
		SenderUserID: m.SenderUserID,
		SenderName:   m.SenderName,
		SenderEmail:  m.SenderEmail,
		Content:      m.Content,
		CreatedAt:    m.CreatedAt,
	}
}

func attachmentModelToDomain(m TicketAttachmentModel) domain.TicketAttachment {
	return domain.TicketAttachment{
		ID:           m.ID,
		AttachmentNo: m.AttachmentNo,
		TicketNo:     m.TicketNo,
		MessageID:    m.MessageID,
		ObjectKey:    m.ObjectKey,
		Mime:         m.Mime,
		Size:         m.Size,
		CreatedAt:    m.CreatedAt,
	}
}
