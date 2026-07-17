package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

// Repository is the persistence port for aftersale tickets.
type Repository interface {
	Create(ctx context.Context, params CreateTicketParams) (*domain.Ticket, error)
	Get(ctx context.Context, ticketNo string, withMessages bool) (*domain.Ticket, error)
	List(ctx context.Context, filter ListFilter, offset int, afterID uint, limit int) ([]domain.Ticket, *uint, error)
	Count(ctx context.Context, filter ListFilter) (int64, error)
	Facets(ctx context.Context, filter ListFilter) (*TicketFacets, error)
	Reply(ctx context.Context, params ReplyParams) (*domain.Ticket, error)
	MarkRead(ctx context.Context, ticketNo string, platformSide bool) (*domain.Ticket, error)
	Close(ctx context.Context, params CloseParams) (*domain.Ticket, error)
	FindAttachment(ctx context.Context, ticketNo, attachmentNo string) (*domain.TicketAttachment, error)
}

// OrderPort resolves an order from BC-TRADE for creation-time validation and
// the display snapshot. Ownership is enforced by passing the requester id.
type OrderPort interface {
	GetOrderForTicket(ctx context.Context, orderNo string, requesterUserID uint) (*OrderInfo, error)
}

// OrderInfo is the trade-owned view of an order needed to open a ticket.
type OrderInfo struct {
	OrderNo        string
	ProjectName    string
	ProjectLogoURL string
	DeliveryEmail  string
	PayAmount      string
	ServiceMode    string
	Status         string
	RefundAmount   string
	AfterSaleUntil *time.Time
	ReceiveUntil   *time.Time
}

// RefundPort issues an order refund via BC-TRADE (INV-AS3: refunds only route
// through Trade so wallet, allocation and receipts stay consistent).
type RefundPort interface {
	RefundOrder(ctx context.Context, cmd RefundCommand) (*RefundResult, error)
}

type RefundCommand struct {
	OrderNo        string
	Reason         string
	IdempotencyKey string
	RequestID      string
	OperatorUserID uint
}

type RefundResult struct {
	RefundAmount string
}

// OwnerLookupPort enriches ticket rows with the requester's safe summary,
// published by IAM and batched over requester ids.
type OwnerLookupPort interface {
	GetByIDs(ctx context.Context, ids []uint) (map[uint]RequesterSummary, error)
}

type RequesterSummary struct {
	ID        uint
	Email     string
	Nickname  string
	GroupName string
	Role      string
	Enabled   bool
}

// FileStorePort stores and reads private image attachments; object keys never
// leave the server (INV-AS6).
type FileStorePort interface {
	Save(ctx context.Context, objectKey, mime, fileName string, content []byte) error
	Read(ctx context.Context, objectKey string) (mime string, content []byte, err error)
}

// MailPort sends a ticket notification email. It is a bypass concern: failures
// are logged and never roll back the ticket state machine (INV-AS7).
type MailPort interface {
	SendTicketMail(ctx context.Context, mail TicketMailCommand) error
}

type TicketMailCommand struct {
	IdempotencyKey string
	To             string
	ReplyTo        string
	Subject        string
	TextBody       string
	HTMLBody       string
}

// TicketMailConfig holds the addressing used to build ticket emails. When the
// reply domain is unset the mailer is treated as disabled.
type TicketMailConfig struct {
	ReplyLocalPart string
	ReplyDomain    string
}

func (c TicketMailConfig) enabled() bool {
	return strings.TrimSpace(c.ReplyDomain) != "" && strings.TrimSpace(c.ReplyLocalPart) != ""
}

// replyAddress builds the per-ticket Reply-To plus-address. The token guards
// against forgery; the hyphen separators are safe because ticketNo and token
// are hyphen-free hex.
func (c TicketMailConfig) replyAddress(ticketNo, token string) string {
	return fmt.Sprintf("%s+%s-%s@%s", c.ReplyLocalPart, ticketNo, token, c.ReplyDomain)
}

// InboundReplyCommand is a parsed inbound email routed to a ticket. Body is the
// raw text body; the use case strips quoted history from it.
type InboundReplyCommand struct {
	Recipient string // the plus-addressed RCPT TO, e.g. support+AS1-tok@domain
	FromEmail string
	FromName  string
	Body      string
}

// ---------------------------------------------------------------------------
// Persistence command DTOs
// ---------------------------------------------------------------------------

type MessageInsert struct {
	SenderType   domain.SenderType
	SenderUserID uint
	SenderName   string
	SenderEmail  string
	Content      string
	Attachments  []AttachmentInsert
}

type AttachmentInsert struct {
	AttachmentNo string
	ObjectKey    string
	Mime         string
	Size         int
}

type CreateTicketParams struct {
	TicketNo        string
	TicketType      domain.TicketType
	Title           string
	RequesterUserID uint
	ReplyToken      string
	Order           *domain.OrderSnapshot
	FirstMessage    MessageInsert
}

type ReplyParams struct {
	TicketNo string
	Message  MessageInsert
}

type CloseParams struct {
	TicketNo      string
	By            domain.SenderType
	Resolution    domain.Resolution
	SystemMessage string
}

type ListFilter struct {
	RequesterUserID uint
	IsAdmin         bool
	Scope           string
	TicketType      domain.TicketType
	Status          domain.TicketStatus
	Search          string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
}

// ---------------------------------------------------------------------------
// Read models
// ---------------------------------------------------------------------------

type TicketFacets struct {
	TicketType TicketTypeFacets
	Status     TicketStatusFacets
}

type TicketTypeFacets struct {
	All     int64
	Order   int64
	General int64
}

type TicketStatusFacets struct {
	All        int64
	Open       int64
	Processing int64
	Closed     int64
}

// TicketView pairs a ticket with the requester's enriched summary.
type TicketView struct {
	Ticket    *domain.Ticket
	Requester *RequesterSummary
}

type TicketListResult struct {
	Items       []TicketView
	Total       int64
	NextAfterID *uint
	Facets      *TicketFacets
}

// ---------------------------------------------------------------------------
// Use-case request DTOs (from the API layer)
// ---------------------------------------------------------------------------

type CreateTicketRequest struct {
	RequesterUserID uint
	RequesterEmail  string
	TicketType      domain.TicketType
	Title           string
	FirstMessage    string
	OrderNo         string
	Attachments     []string
	RequestID       string
}

type ReplyTicketRequest struct {
	TicketNo    string
	UserID      uint
	UserEmail   string
	IsAdmin     bool
	AsPlatform  bool
	Content     string
	Attachments []string
}

type CloseTicketRequest struct {
	TicketNo   string
	UserID     uint
	IsAdmin    bool
	AsPlatform bool
}

type RefundTicketRequest struct {
	TicketNo       string
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
}
