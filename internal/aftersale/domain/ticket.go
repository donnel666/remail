package domain

import (
	"errors"
	"strings"
	"time"
)

// TicketType distinguishes an order-linked ticket from a general one.
type TicketType string

const (
	TicketTypeOrder   TicketType = "order"
	TicketTypeGeneral TicketType = "general"
)

// TicketStatus is the conversation lifecycle. closed is terminal.
type TicketStatus string

const (
	TicketStatusOpen       TicketStatus = "open"
	TicketStatusProcessing TicketStatus = "processing"
	TicketStatusClosed     TicketStatus = "closed"
)

// SenderType identifies who authored a message.
type SenderType string

const (
	SenderTypeUser     SenderType = "user"
	SenderTypePlatform SenderType = "platform"
	SenderTypeSystem   SenderType = "system"
)

// ResolutionKind records how a ticket was closed.
type ResolutionKind string

const (
	ResolutionRefunded ResolutionKind = "refunded"
	ResolutionClosed   ResolutionKind = "closed"
)

// OrderSnapshot holds the immutable order display fields captured at ticket
// creation. Ownership, status and refund facts are always resolved live via
// BC-TRADE, never from this snapshot.
type OrderSnapshot struct {
	OrderNo        string
	ProjectName    string
	ProjectLogoURL string
	DeliveryEmail  string
	PayAmount      string
	ServiceMode    string
	AfterSaleUntil *time.Time
}

// Resolution is the terminal outcome recorded on a closed ticket.
type Resolution struct {
	Kind         ResolutionKind
	RefundAmount string
}

// Ticket is the aftersale conversation aggregate root.
type Ticket struct {
	ID              uint
	TicketNo        string
	TicketType      TicketType
	Title           string
	Status          TicketStatus
	RequesterUserID uint
	// ReplyToken is the secret embedded in the outbound Reply-To plus-address;
	// inbound email replies must present it to be accepted.
	ReplyToken            string
	Order                 *OrderSnapshot
	Resolution            *Resolution
	RequesterUnreadCount  int
	PlatformUnreadCount   int
	LastMessagePreview    string
	LastMessageSenderType SenderType
	LastMessageAt         *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// Messages is populated only for the detail read; the list leaves it nil.
	Messages []TicketMessage
}

// TicketMessage is one entry in the conversation thread.
type TicketMessage struct {
	ID           uint
	TicketNo     string
	SenderType   SenderType
	SenderUserID uint
	SenderName   string
	SenderEmail  string
	Content      string
	CreatedAt    time.Time
	Attachments  []TicketAttachment
}

// TicketAttachment is safe metadata for one stored image; object_key stays
// server-side only.
type TicketAttachment struct {
	ID           uint
	AttachmentNo string
	TicketNo     string
	MessageID    uint
	ObjectKey    string
	Mime         string
	Size         int
	CreatedAt    time.Time
}

var (
	ErrTicketNotFound       = errors.New("aftersale: ticket not found")
	ErrTicketForbidden      = errors.New("aftersale: ticket forbidden")
	ErrTicketClosed         = errors.New("aftersale: ticket already closed")
	ErrTicketStateConflict  = errors.New("aftersale: ticket state conflict")
	ErrInvalidTicketRequest = errors.New("aftersale: invalid ticket request")
	ErrOrderNotEligible     = errors.New("aftersale: order not eligible for aftersale")
	ErrAttachmentNotFound   = errors.New("aftersale: attachment not found")
	ErrAttachmentInvalid    = errors.New("aftersale: invalid attachment")
	ErrAttachmentTooLarge   = errors.New("aftersale: attachment too large")
)

// NormalizeTicketType parses an untrusted type string.
func NormalizeTicketType(value string) (TicketType, bool) {
	switch TicketType(strings.ToLower(strings.TrimSpace(value))) {
	case TicketTypeOrder:
		return TicketTypeOrder, true
	case TicketTypeGeneral:
		return TicketTypeGeneral, true
	default:
		return "", false
	}
}

// NormalizeTicketStatus parses an untrusted status filter; empty is allowed
// (meaning "no status filter").
func NormalizeTicketStatus(value string) (TicketStatus, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return "", true
	}
	switch TicketStatus(trimmed) {
	case TicketStatusOpen, TicketStatusProcessing, TicketStatusClosed:
		return TicketStatus(trimmed), true
	default:
		return "", false
	}
}

// IsTerminal reports whether the ticket can no longer change state.
func (s TicketStatus) IsTerminal() bool {
	return s == TicketStatusClosed
}
