package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	mod *Module
}

func NewHandler(mod *Module) *Handler {
	return &Handler{mod: mod}
}

// GetTickets lists the caller's own tickets (console after-sales inbox).
func (h *Handler) GetTickets(c *gin.Context) {
	h.listTickets(c, "mine", false)
}

// GetAdminTickets lists every ticket for the admin ticket-management console.
func (h *Handler) GetAdminTickets(c *gin.Context) {
	h.listTickets(c, "all", true)
}

func (h *Handler) listTickets(c *gin.Context, scope string, isAdmin bool) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	offset, limit, ok := parseTicketPagination(c)
	if !ok {
		return
	}
	afterID, ok := parseAfterID(c)
	if !ok {
		return
	}
	ticketType, ok := parseTicketType(c.Query("ticketType"))
	if !ok {
		invalidParams(c)
		return
	}
	status, ok := domain.NormalizeTicketStatus(c.Query("status"))
	if !ok {
		invalidParams(c)
		return
	}
	createdFrom, ok := parseOptionalTime(c.Query("createdFrom"))
	if !ok {
		invalidParams(c)
		return
	}
	createdTo, ok := parseOptionalTime(c.Query("createdTo"))
	if !ok {
		invalidParams(c)
		return
	}
	result, err := h.mod.UseCase.ListTickets(c.Request.Context(), aftersaleapp.ListFilter{
		RequesterUserID: userID,
		IsAdmin:         isAdmin,
		Scope:           scope,
		TicketType:      ticketType,
		Status:          status,
		Search:          strings.TrimSpace(c.Query("search")),
		CreatedFrom:     createdFrom,
		CreatedTo:       createdTo,
	}, offset, afterID, limit)
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	resp := TicketListResponse{
		Items:       make([]TicketResponse, len(result.Items)),
		Total:       result.Total,
		Offset:      offset,
		NextAfterID: result.NextAfterID,
		HasNext:     result.NextAfterID != nil,
		Limit:       limit,
		Facets:      toTicketFacetsResponse(result.Facets),
	}
	for i := range result.Items {
		resp.Items[i] = ticketListItemResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetTicket(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	view, err := h.mod.UseCase.GetTicket(c.Request.Context(), c.Param("ticketNo"), userID, role.HasAdminAccess())
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticketDetailResponse(*view))
}

func (h *Handler) GetAttachment(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	mime, content, err := h.mod.UseCase.LoadAttachment(
		c.Request.Context(), c.Param("ticketNo"), c.Param("attachmentNo"), userID, role.HasAdminAccess(),
	)
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	if strings.TrimSpace(mime) == "" {
		mime = "application/octet-stream"
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(http.StatusOK, mime, content)
}

func (h *Handler) PostTicket(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	email, _ := middleware.GetCurrentEmail(c)
	var req CreateTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c)
		return
	}
	ticketType, ok := domain.NormalizeTicketType(req.TicketType)
	if !ok {
		writeAftersaleError(c, domain.ErrInvalidTicketRequest)
		return
	}
	view, err := h.mod.UseCase.CreateTicket(c.Request.Context(), aftersaleapp.CreateTicketRequest{
		RequesterUserID: userID,
		RequesterEmail:  email,
		TicketType:      ticketType,
		Title:           req.Title,
		FirstMessage:    req.FirstMessage,
		OrderNo:         req.OrderNo,
		Attachments:     req.Attachments,
		RequestID:       middleware.GetRequestID(c),
	})
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, ticketDetailResponse(*view))
}

// PostTicketMessage is the requester replying on their own ticket.
func (h *Handler) PostTicketMessage(c *gin.Context) {
	h.postMessage(c, false)
}

// PostAdminTicketMessage is a platform operator replying (permission-gated).
func (h *Handler) PostAdminTicketMessage(c *gin.Context) {
	h.postMessage(c, true)
}

func (h *Handler) postMessage(c *gin.Context, asPlatform bool) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	email, _ := middleware.GetCurrentEmail(c)
	role, _ := middleware.GetCurrentRole(c)
	var req ReplyTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c)
		return
	}
	view, err := h.mod.UseCase.ReplyTicket(c.Request.Context(), aftersaleapp.ReplyTicketRequest{
		TicketNo:    c.Param("ticketNo"),
		UserID:      userID,
		UserEmail:   email,
		IsAdmin:     role.HasAdminAccess(),
		AsPlatform:  asPlatform,
		Content:     req.Content,
		Attachments: req.Attachments,
	})
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticketDetailResponse(*view))
}

func (h *Handler) PostTicketRead(c *gin.Context) {
	h.markRead(c, false)
}

func (h *Handler) PostAdminTicketRead(c *gin.Context) {
	h.markRead(c, true)
}

func (h *Handler) markRead(c *gin.Context, asPlatform bool) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	if err := h.mod.UseCase.MarkRead(c.Request.Context(), c.Param("ticketNo"), userID, asPlatform); err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PostTicketClose(c *gin.Context) {
	h.closeTicket(c, false)
}

func (h *Handler) PostAdminTicketClose(c *gin.Context) {
	h.closeTicket(c, true)
}

func (h *Handler) closeTicket(c *gin.Context, asPlatform bool) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	view, err := h.mod.UseCase.CloseTicket(c.Request.Context(), aftersaleapp.CloseTicketRequest{
		TicketNo:   c.Param("ticketNo"),
		UserID:     userID,
		IsAdmin:    role.HasAdminAccess(),
		AsPlatform: asPlatform,
	})
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticketDetailResponse(*view))
}

func (h *Handler) PostAdminTicketRefund(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	view, err := h.mod.UseCase.RefundAndCloseTicket(c.Request.Context(), aftersaleapp.RefundTicketRequest{
		TicketNo:       c.Param("ticketNo"),
		OperatorUserID: operatorUserID,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
	})
	if err != nil {
		writeAftersaleError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticketDetailResponse(*view))
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func currentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return userID, true
}

func parseTicketPagination(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 1000})
}

func parseAfterID(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Query("afterId"))
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		invalidParams(c)
		return 0, false
	}
	return uint(parsed), true
}

func parseTicketType(raw string) (domain.TicketType, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	return domain.NormalizeTicketType(raw)
}

func parseOptionalTime(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

func invalidParams(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
}

func badRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
}

func writeAftersaleError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrTicketNotFound), errors.Is(err, domain.ErrAttachmentNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Ticket not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrTicketForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": requestID})
	case errors.Is(err, domain.ErrTicketClosed), errors.Is(err, domain.ErrTicketStateConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Ticket state conflict.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderNotEligible):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Order is not eligible for after-sale.", "requestId": requestID})
	case errors.Is(err, domain.ErrAttachmentTooLarge):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Attachment is too large.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidTicketRequest), errors.Is(err, domain.ErrAttachmentInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid ticket request.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
