package api

import (
	"time"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"github.com/donnel666/remail/internal/aftersale/domain"
)

type CreateTicketRequest struct {
	TicketType   string   `json:"ticketType"`
	Title        string   `json:"title"`
	FirstMessage string   `json:"firstMessage"`
	OrderNo      string   `json:"orderNo,omitempty"`
	Attachments  []string `json:"attachments,omitempty"`
}

type ReplyTicketRequest struct {
	Content     string   `json:"content"`
	Attachments []string `json:"attachments,omitempty"`
}

type TicketOrderResponse struct {
	OrderNo        string     `json:"orderNo"`
	ProjectName    string     `json:"projectName,omitempty"`
	ProjectLogoURL string     `json:"projectLogoUrl,omitempty"`
	DeliveryEmail  string     `json:"deliveryEmail"`
	PayAmount      string     `json:"payAmount"`
	ServiceMode    string     `json:"serviceMode"`
	AfterSaleUntil *time.Time `json:"afterSaleUntil,omitempty"`
	HasSupplier    bool       `json:"hasSupplier"`
}

type TicketResolutionResponse struct {
	Kind         string `json:"kind"`
	RefundAmount string `json:"refundAmount,omitempty"`
}

type TicketMessageResponse struct {
	ID           uint      `json:"id"`
	SenderType   string    `json:"senderType"`
	SenderName   string    `json:"senderName,omitempty"`
	SenderUserID uint      `json:"senderUserId,omitempty"`
	SenderEmail  string    `json:"senderEmail,omitempty"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"createdAt"`
	Attachments  []string  `json:"attachments,omitempty"`
}

type TicketResponse struct {
	ID                   uint                      `json:"id"`
	TicketNo             string                    `json:"ticketNo"`
	TicketType           string                    `json:"ticketType"`
	Title                string                    `json:"title"`
	Status               string                    `json:"status"`
	Order                *TicketOrderResponse      `json:"order,omitempty"`
	RequesterUserID      uint                      `json:"requesterUserId"`
	RequesterEmail       string                    `json:"requesterEmail"`
	RequesterName        string                    `json:"requesterName"`
	RequesterRole        string                    `json:"requesterRole"`
	RequesterGroupName   string                    `json:"requesterGroupName"`
	Resolution           *TicketResolutionResponse `json:"resolution,omitempty"`
	RequesterUnreadCount int                       `json:"requesterUnreadCount"`
	PlatformUnreadCount  int                       `json:"platformUnreadCount"`
	Messages             []TicketMessageResponse   `json:"messages"`
	CreatedAt            time.Time                 `json:"createdAt"`
	UpdatedAt            time.Time                 `json:"updatedAt"`
}

type TicketTypeFacetsResponse struct {
	All     int64 `json:"all"`
	Order   int64 `json:"order"`
	General int64 `json:"general"`
}

type TicketStatusFacetsResponse struct {
	All        int64 `json:"all"`
	Open       int64 `json:"open"`
	Processing int64 `json:"processing"`
	Closed     int64 `json:"closed"`
}

type TicketFacetsResponse struct {
	TicketType TicketTypeFacetsResponse   `json:"ticketType"`
	Status     TicketStatusFacetsResponse `json:"status"`
}

type TicketListResponse struct {
	Items       []TicketResponse      `json:"items"`
	Total       int64                 `json:"total"`
	Offset      int                   `json:"offset"`
	NextAfterID *uint                 `json:"nextAfterId,omitempty"`
	HasNext     bool                  `json:"hasNext"`
	Limit       int                   `json:"limit"`
	Facets      *TicketFacetsResponse `json:"facets,omitempty"`
}

// ticketResponseBase maps a view without messages; list and detail attach the
// message list differently (a synthesized preview vs the full thread).
func ticketResponseBase(view aftersaleapp.TicketView) TicketResponse {
	ticket := view.Ticket
	resp := TicketResponse{
		ID:                   ticket.ID,
		TicketNo:             ticket.TicketNo,
		TicketType:           string(ticket.TicketType),
		Title:                ticket.Title,
		Status:               string(ticket.Status),
		RequesterUserID:      ticket.RequesterUserID,
		RequesterUnreadCount: ticket.RequesterUnreadCount,
		PlatformUnreadCount:  ticket.PlatformUnreadCount,
		Messages:             []TicketMessageResponse{},
		CreatedAt:            ticket.CreatedAt,
		UpdatedAt:            ticket.UpdatedAt,
	}
	if view.Requester != nil {
		resp.RequesterEmail = view.Requester.Email
		resp.RequesterName = view.Requester.Nickname
		resp.RequesterRole = view.Requester.Role
		resp.RequesterGroupName = view.Requester.GroupName
	}
	if ticket.Order != nil {
		resp.Order = &TicketOrderResponse{
			OrderNo:        ticket.Order.OrderNo,
			ProjectName:    ticket.Order.ProjectName,
			ProjectLogoURL: ticket.Order.ProjectLogoURL,
			DeliveryEmail:  ticket.Order.DeliveryEmail,
			PayAmount:      ticket.Order.PayAmount,
			ServiceMode:    ticket.Order.ServiceMode,
			AfterSaleUntil: ticket.Order.AfterSaleUntil,
			HasSupplier:    false,
		}
	}
	if ticket.Resolution != nil {
		resp.Resolution = &TicketResolutionResponse{
			Kind:         string(ticket.Resolution.Kind),
			RefundAmount: ticket.Resolution.RefundAmount,
		}
	}
	return resp
}

// ticketDetailResponse returns the full conversation thread.
func ticketDetailResponse(view aftersaleapp.TicketView) TicketResponse {
	resp := ticketResponseBase(view)
	messages := view.Ticket.Messages
	resp.Messages = make([]TicketMessageResponse, len(messages))
	for i := range messages {
		resp.Messages[i] = messageResponse(view.Ticket.TicketNo, messages[i])
	}
	return resp
}

// ticketListItemResponse returns a single synthesized preview message so the
// inbox row can render its last-message line without loading the thread.
func ticketListItemResponse(view aftersaleapp.TicketView) TicketResponse {
	resp := ticketResponseBase(view)
	ticket := view.Ticket
	if ticket.LastMessagePreview == "" {
		return resp
	}
	createdAt := ticket.UpdatedAt
	if ticket.LastMessageAt != nil {
		createdAt = *ticket.LastMessageAt
	}
	resp.Messages = []TicketMessageResponse{{
		SenderType: string(ticket.LastMessageSenderType),
		Content:    ticket.LastMessagePreview,
		CreatedAt:  createdAt,
	}}
	return resp
}

func messageResponse(ticketNo string, message domain.TicketMessage) TicketMessageResponse {
	resp := TicketMessageResponse{
		ID:           message.ID,
		SenderType:   string(message.SenderType),
		SenderName:   message.SenderName,
		SenderUserID: message.SenderUserID,
		SenderEmail:  message.SenderEmail,
		Content:      message.Content,
		CreatedAt:    message.CreatedAt,
	}
	for i := range message.Attachments {
		resp.Attachments = append(resp.Attachments, attachmentURL(ticketNo, message.Attachments[i].AttachmentNo))
	}
	return resp
}

func attachmentURL(ticketNo, attachmentNo string) string {
	return "/v1/tickets/" + ticketNo + "/attachments/" + attachmentNo
}

func toTicketFacetsResponse(facets *aftersaleapp.TicketFacets) *TicketFacetsResponse {
	if facets == nil {
		return nil
	}
	return &TicketFacetsResponse{
		TicketType: TicketTypeFacetsResponse{
			All:     facets.TicketType.All,
			Order:   facets.TicketType.Order,
			General: facets.TicketType.General,
		},
		Status: TicketStatusFacetsResponse{
			All:        facets.Status.All,
			Open:       facets.Status.Open,
			Processing: facets.Status.Processing,
			Closed:     facets.Status.Closed,
		},
	}
}
