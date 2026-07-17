package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	maxAttachments     = 6
	maxAttachmentBytes = 5 << 20 // 5 MiB decoded, per image
	platformFallback   = "平台客服"
)

// UseCase orchestrates the aftersale ticket lifecycle. It owns authorization,
// order validation, attachment storage and refund routing; state transitions
// and unread math live in the repository so they stay atomic with the write.
type UseCase struct {
	repo       Repository
	orders     OrderPort
	refunds    RefundPort
	files      FileStorePort
	owners     OwnerLookupPort
	mail       MailPort
	mailConfig TicketMailConfig
	now        func() time.Time
}

func NewUseCase(repo Repository, orders OrderPort, refunds RefundPort, files FileStorePort) *UseCase {
	return &UseCase{
		repo:    repo,
		orders:  orders,
		refunds: refunds,
		files:   files,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// SetOwnerLookupPort attaches the IAM-backed requester directory. Wired in the
// composition root to avoid a cross-context import cycle.
func (uc *UseCase) SetOwnerLookupPort(owners OwnerLookupPort) { uc.owners = owners }

// SetMailer attaches the outbound mail port and its addressing config. When
// unset, ticket emails are silently skipped.
func (uc *UseCase) SetMailer(mail MailPort, config TicketMailConfig) {
	uc.mail = mail
	uc.mailConfig = config
}

func (uc *UseCase) ListTickets(ctx context.Context, filter ListFilter, offset int, afterID uint, limit int) (*TicketListResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	items, nextAfterID, err := uc.repo.List(ctx, filter, offset, afterID, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.repo.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	facets, err := uc.repo.Facets(ctx, filter)
	if err != nil {
		return nil, err
	}
	views := make([]TicketView, len(items))
	for i := range items {
		views[i] = TicketView{Ticket: &items[i]}
	}
	if err := uc.enrichRequesters(ctx, views); err != nil {
		return nil, err
	}
	return &TicketListResult{Items: views, Total: total, NextAfterID: nextAfterID, Facets: facets}, nil
}

func (uc *UseCase) GetTicket(ctx context.Context, ticketNo string, userID uint, isAdmin bool) (*TicketView, error) {
	ticket, err := uc.repo.Get(ctx, ticketNo, true)
	if err != nil {
		return nil, err
	}
	if !isAdmin && ticket.RequesterUserID != userID {
		return nil, domain.ErrTicketForbidden
	}
	return uc.viewOf(ctx, ticket)
}

func (uc *UseCase) CreateTicket(ctx context.Context, req CreateTicketRequest) (*TicketView, error) {
	title := strings.TrimSpace(req.Title)
	first := strings.TrimSpace(req.FirstMessage)
	if title == "" || first == "" {
		return nil, domain.ErrInvalidTicketRequest
	}
	if req.TicketType != domain.TicketTypeOrder && req.TicketType != domain.TicketTypeGeneral {
		return nil, domain.ErrInvalidTicketRequest
	}

	var snapshot *domain.OrderSnapshot
	if req.TicketType == domain.TicketTypeOrder {
		orderNo := strings.TrimSpace(req.OrderNo)
		if orderNo == "" {
			return nil, domain.ErrInvalidTicketRequest
		}
		info, err := uc.orders.GetOrderForTicket(ctx, orderNo, req.RequesterUserID)
		if err != nil {
			return nil, err
		}
		if err := uc.checkOrderEligibility(info); err != nil {
			return nil, err
		}
		snapshot = buildSnapshot(info)
	}

	ticketNo := nextTicketNo()
	attachments, err := uc.decodeAndUpload(ctx, ticketNo, req.Attachments)
	if err != nil {
		return nil, err
	}
	created, err := uc.repo.Create(ctx, CreateTicketParams{
		TicketNo:        ticketNo,
		TicketType:      req.TicketType,
		Title:           title,
		RequesterUserID: req.RequesterUserID,
		ReplyToken:      newReplyToken(),
		Order:           snapshot,
		FirstMessage: MessageInsert{
			SenderType:   domain.SenderTypeUser,
			SenderUserID: req.RequesterUserID,
			SenderEmail:  req.RequesterEmail,
			Content:      first,
			Attachments:  attachments,
		},
	})
	if err != nil {
		return nil, err
	}
	view, err := uc.viewOf(ctx, created)
	if err != nil {
		return nil, err
	}
	uc.notifyRequester(ctx, view, ticketMailCreated)
	return view, nil
}

func (uc *UseCase) ReplyTicket(ctx context.Context, req ReplyTicketRequest) (*TicketView, error) {
	content := strings.TrimSpace(req.Content)
	if content == "" && len(req.Attachments) == 0 {
		return nil, domain.ErrInvalidTicketRequest
	}
	ticket, err := uc.repo.Get(ctx, req.TicketNo, false)
	if err != nil {
		return nil, err
	}
	if ticket.Status.IsTerminal() {
		return nil, domain.ErrTicketClosed
	}

	msg := MessageInsert{SenderUserID: req.UserID, SenderEmail: req.UserEmail, Content: content}
	if req.AsPlatform {
		// The admin route already enforced operate permission via middleware.
		msg.SenderType = domain.SenderTypePlatform
		msg.SenderName = uc.resolveOperatorName(ctx, req.UserID)
	} else {
		if ticket.RequesterUserID != req.UserID {
			return nil, domain.ErrTicketForbidden
		}
		msg.SenderType = domain.SenderTypeUser
	}

	attachments, err := uc.decodeAndUpload(ctx, req.TicketNo, req.Attachments)
	if err != nil {
		return nil, err
	}
	msg.Attachments = attachments

	updated, err := uc.repo.Reply(ctx, ReplyParams{TicketNo: req.TicketNo, Message: msg})
	if err != nil {
		return nil, err
	}
	view, err := uc.viewOf(ctx, updated)
	if err != nil {
		return nil, err
	}
	// Only platform replies notify the customer; the customer's own replies
	// (console or inbound email) are not echoed back to them.
	if req.AsPlatform {
		uc.notifyRequester(ctx, view, ticketMailReplied)
	}
	return view, nil
}

func (uc *UseCase) MarkRead(ctx context.Context, ticketNo string, userID uint, asPlatform bool) error {
	ticket, err := uc.repo.Get(ctx, ticketNo, false)
	if err != nil {
		return err
	}
	if !asPlatform && ticket.RequesterUserID != userID {
		return domain.ErrTicketForbidden
	}
	_, err = uc.repo.MarkRead(ctx, ticketNo, asPlatform)
	return err
}

func (uc *UseCase) CloseTicket(ctx context.Context, req CloseTicketRequest) (*TicketView, error) {
	ticket, err := uc.repo.Get(ctx, req.TicketNo, false)
	if err != nil {
		return nil, err
	}
	if !req.AsPlatform && ticket.RequesterUserID != req.UserID {
		return nil, domain.ErrTicketForbidden
	}
	if ticket.Status.IsTerminal() {
		return nil, domain.ErrTicketStateConflict
	}
	by := domain.SenderTypeUser
	systemMessage := "用户已主动关闭该工单。"
	if req.AsPlatform {
		by = domain.SenderTypePlatform
		systemMessage = "平台已关闭该工单。"
	}
	updated, err := uc.repo.Close(ctx, CloseParams{
		TicketNo:      req.TicketNo,
		By:            by,
		Resolution:    domain.Resolution{Kind: domain.ResolutionClosed},
		SystemMessage: systemMessage,
	})
	if err != nil {
		return nil, err
	}
	view, err := uc.viewOf(ctx, updated)
	if err != nil {
		return nil, err
	}
	uc.notifyRequester(ctx, view, ticketMailResolved)
	return view, nil
}

func (uc *UseCase) RefundAndCloseTicket(ctx context.Context, req RefundTicketRequest) (*TicketView, error) {
	ticket, err := uc.repo.Get(ctx, req.TicketNo, false)
	if err != nil {
		return nil, err
	}
	if ticket.Status.IsTerminal() {
		return nil, domain.ErrTicketStateConflict
	}
	if ticket.TicketType != domain.TicketTypeOrder || ticket.Order == nil || ticket.Order.OrderNo == "" {
		return nil, domain.ErrInvalidTicketRequest
	}
	refund, err := uc.refunds.RefundOrder(ctx, RefundCommand{
		OrderNo:        ticket.Order.OrderNo,
		Reason:         "aftersale ticket refund",
		IdempotencyKey: req.IdempotencyKey,
		RequestID:      req.RequestID,
		OperatorUserID: req.OperatorUserID,
	})
	if err != nil {
		return nil, err
	}
	amount := strings.TrimSpace(refund.RefundAmount)
	if amount == "" {
		amount = ticket.Order.PayAmount
	}
	updated, err := uc.repo.Close(ctx, CloseParams{
		TicketNo:      req.TicketNo,
		By:            domain.SenderTypePlatform,
		Resolution:    domain.Resolution{Kind: domain.ResolutionRefunded, RefundAmount: amount},
		SystemMessage: fmt.Sprintf("平台已退款 %s 并关闭工单。", formatMoney(amount)),
	})
	if err != nil {
		return nil, err
	}
	view, err := uc.viewOf(ctx, updated)
	if err != nil {
		return nil, err
	}
	uc.notifyRequester(ctx, view, ticketMailResolved)
	return view, nil
}

// LoadAttachment authorizes the caller then returns the image bytes for
// streaming. objectKey never leaves this method.
func (uc *UseCase) LoadAttachment(ctx context.Context, ticketNo, attachmentNo string, userID uint, isAdmin bool) (string, []byte, error) {
	ticket, err := uc.repo.Get(ctx, ticketNo, false)
	if err != nil {
		return "", nil, err
	}
	if !isAdmin && ticket.RequesterUserID != userID {
		return "", nil, domain.ErrTicketForbidden
	}
	attachment, err := uc.repo.FindAttachment(ctx, ticketNo, attachmentNo)
	if err != nil {
		return "", nil, err
	}
	mime, content, err := uc.files.Read(ctx, attachment.ObjectKey)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(mime) == "" {
		mime = attachment.Mime
	}
	return mime, content, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (uc *UseCase) viewOf(ctx context.Context, ticket *domain.Ticket) (*TicketView, error) {
	view := TicketView{Ticket: ticket}
	if uc.owners == nil || ticket.RequesterUserID == 0 {
		return &view, nil
	}
	summaries, err := uc.owners.GetByIDs(ctx, []uint{ticket.RequesterUserID})
	if err != nil {
		return nil, err
	}
	if summary, ok := summaries[ticket.RequesterUserID]; ok {
		copied := summary
		view.Requester = &copied
	}
	return &view, nil
}

func (uc *UseCase) enrichRequesters(ctx context.Context, views []TicketView) error {
	if uc.owners == nil || len(views) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(views))
	ids := make([]uint, 0, len(views))
	for i := range views {
		id := views[i].Ticket.RequesterUserID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	summaries, err := uc.owners.GetByIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range views {
		if summary, ok := summaries[views[i].Ticket.RequesterUserID]; ok {
			copied := summary
			views[i].Requester = &copied
		}
	}
	return nil
}

func (uc *UseCase) resolveOperatorName(ctx context.Context, operatorUserID uint) string {
	if uc.owners != nil && operatorUserID != 0 {
		if summaries, err := uc.owners.GetByIDs(ctx, []uint{operatorUserID}); err == nil {
			if summary, ok := summaries[operatorUserID]; ok {
				if nickname := strings.TrimSpace(summary.Nickname); nickname != "" {
					return nickname
				}
				if email := strings.TrimSpace(summary.Email); email != "" {
					return email
				}
			}
		}
	}
	return platformFallback
}

func (uc *UseCase) checkOrderEligibility(info *OrderInfo) error {
	switch info.Status {
	case "refunded", "pending_payment", "paid", "failed":
		return domain.ErrOrderNotEligible
	case "active":
		return nil
	default: // completed, closed
		deadline := afterSaleDeadline(info)
		if deadline != nil && deadline.After(uc.now()) {
			return nil
		}
		return domain.ErrOrderNotEligible
	}
}

func (uc *UseCase) decodeAndUpload(ctx context.Context, ticketNo string, dataURLs []string) ([]AttachmentInsert, error) {
	if len(dataURLs) == 0 {
		return nil, nil
	}
	if len(dataURLs) > maxAttachments {
		return nil, domain.ErrAttachmentInvalid
	}
	out := make([]AttachmentInsert, 0, len(dataURLs))
	for _, raw := range dataURLs {
		mime, data, err := decodeImageDataURL(raw)
		if err != nil {
			return nil, err
		}
		if len(data) > maxAttachmentBytes {
			return nil, domain.ErrAttachmentTooLarge
		}
		attachmentNo := nextAttachmentNo()
		objectKey := fmt.Sprintf("aftersale/%s/%s", ticketNo, attachmentNo)
		if err := uc.files.Save(ctx, objectKey, mime, attachmentNo+extForMime(mime), data); err != nil {
			return nil, err
		}
		out = append(out, AttachmentInsert{
			AttachmentNo: attachmentNo,
			ObjectKey:    objectKey,
			Mime:         mime,
			Size:         len(data),
		})
	}
	return out, nil
}

func afterSaleDeadline(info *OrderInfo) *time.Time {
	if info.AfterSaleUntil != nil {
		return info.AfterSaleUntil
	}
	return info.ReceiveUntil
}

func buildSnapshot(info *OrderInfo) *domain.OrderSnapshot {
	return &domain.OrderSnapshot{
		OrderNo:        info.OrderNo,
		ProjectName:    info.ProjectName,
		ProjectLogoURL: info.ProjectLogoURL,
		DeliveryEmail:  info.DeliveryEmail,
		PayAmount:      info.PayAmount,
		ServiceMode:    info.ServiceMode,
		AfterSaleUntil: afterSaleDeadline(info),
	}
}

func decodeImageDataURL(raw string) (string, []byte, error) {
	if !strings.HasPrefix(raw, "data:") {
		return "", nil, domain.ErrAttachmentInvalid
	}
	meta, payload, ok := strings.Cut(raw[len("data:"):], ",")
	if !ok || !strings.Contains(meta, "base64") {
		return "", nil, domain.ErrAttachmentInvalid
	}
	mime := strings.SplitN(meta, ";", 2)[0]
	if !strings.HasPrefix(mime, "image/") {
		return "", nil, domain.ErrAttachmentInvalid
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil || len(data) == 0 {
		return "", nil, domain.ErrAttachmentInvalid
	}
	return mime, data, nil
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".img"
	}
}

// formatMoney trims trailing decimal zeros and prefixes ¥, matching the
// console's amount formatting.
func formatMoney(amount string) string {
	value := strings.TrimSpace(amount)
	if value == "" {
		return "¥0"
	}
	if strings.Contains(value, ".") {
		value = strings.TrimRight(value, "0")
		value = strings.TrimRight(value, ".")
	}
	if value == "" || value == "-" {
		value = "0"
	}
	return "¥" + value
}

func nextTicketNo() string     { return "AS" + platform.NewUUIDV7CompactUpper() }
func nextAttachmentNo() string { return "AA" + platform.NewUUIDV7CompactUpper() }
