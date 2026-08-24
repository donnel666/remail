package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	lotteryapp "github.com/donnel666/remail/internal/lottery/app"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/gin-gonic/gin"
)

const maxLotteryRequestBytes = 16 << 10

type Handler struct{ module *Module }

func NewHandler(module *Module) *Handler { return &Handler{module: module} }

func (h *Handler) PostAdminLottery(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		writeLotteryError(c, lotterydomain.ErrLotteryNotEligible)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLotteryRequestBytes)
	var request CreateLotteryRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeInvalidLotteryBody(c)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeInvalidLotteryBody(c)
		return
	}
	result, err := h.module.Service.Create(c.Request.Context(), lotteryapp.CreateRequest{
		CreatedByUserID: userID, Title: request.Title, LotteryType: request.LotteryType,
		TotalAmount: request.TotalAmount, PoolIncrementAmount: request.PoolIncrementAmount,
		MinPayout: request.MinPayout, MaxPayout: request.MaxPayout, TierWeights: request.TierWeights,
		MinAccountAgeDays: request.MinAccountAgeDays, DrawAt: request.DrawAt,
		ParticipantTarget: request.ParticipantTarget, IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID: middleware.GetRequestID(c),
	})
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.Header("Location", "/v1/admin/lotteries/"+strconv.FormatUint(uint64(result.Lottery.ID), 10))
	c.JSON(status, lotteryResponse(result.Lottery))
}

func (h *Handler) GetAdminLotteries(c *gin.Context) {
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 100})
	if !ok {
		return
	}
	items, total, err := h.module.Repo.List(c.Request.Context(), lotteryapp.ListFilter{Status: strings.TrimSpace(c.Query("status")), Offset: offset, Limit: limit})
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	response := make([]LotteryResponse, len(items))
	for i := range items {
		response[i] = lotteryResponse(items[i])
	}
	c.JSON(http.StatusOK, LotteryListResponse{Items: response, Total: total, Offset: offset, Limit: limit})
}

func (h *Handler) GetAdminLottery(c *gin.Context) {
	id, ok := parseLotteryID(c)
	if !ok {
		return
	}
	item, err := h.module.Repo.GetByID(c.Request.Context(), id)
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	c.JSON(http.StatusOK, lotteryResponse(item))
}

func (h *Handler) GetAdminLotteryEntries(c *gin.Context) {
	id, ok := parseLotteryID(c)
	if !ok {
		return
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 100})
	if !ok {
		return
	}
	items, total, err := h.module.Repo.ListEntries(c.Request.Context(), id, offset, limit)
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	response := make([]EntryResponse, len(items))
	for i := range items {
		response[i] = entryResponse(items[i], false)
	}
	c.JSON(http.StatusOK, EntryListResponse{Items: response, Total: total, Offset: offset, Limit: limit})
}

func (h *Handler) GetAdminLotteryPayouts(c *gin.Context) {
	id, ok := parseLotteryID(c)
	if !ok {
		return
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 100})
	if !ok {
		return
	}
	items, total, err := h.module.Repo.ListPayouts(c.Request.Context(), id, offset, limit)
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	response := make([]PayoutResponse, len(items))
	for i := range items {
		response[i] = payoutResponse(items[i])
	}
	c.JSON(http.StatusOK, PayoutListResponse{Items: response, Total: total, Offset: offset, Limit: limit})
}

func (h *Handler) GetPublicLottery(c *gin.Context) {
	userID, _ := middleware.GetCurrentUserID(c)
	lottery, entry, payout, err := h.module.Service.Public(c.Request.Context(), c.Param("token"), userID)
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	response := PublicLotteryResponse{
		Lottery:    publicLotterySummary(lottery),
		HasEntered: entry != nil,
	}
	if payout != nil {
		mapped := PublicPayoutResponse{Amount: payout.Amount}
		response.MyPayout = &mapped
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, response)
}

func (h *Handler) PostPublicLotteryEntry(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.module.Service.Enter(c.Request.Context(), c.Param("token"), userID)
	if err != nil {
		writeLotteryError(c, err)
		return
	}
	status := http.StatusCreated
	if result.AlreadyExists {
		status = http.StatusOK
	}
	c.JSON(status, PublicEntryResponse{Already: result.AlreadyExists})
}

func parseLotteryID(c *gin.Context) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(c.Param("lotteryId")), 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid lottery id.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return uint(value), true
}

func writeInvalidLotteryBody(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
}

func writeLotteryError(c *gin.Context, err error) {
	var rejected *lotterydomain.EntryRejectedError
	if errors.As(err, &rejected) {
		status := http.StatusForbidden
		if errors.Is(err, lotterydomain.ErrLotteryClosed) {
			status = http.StatusConflict
		}
		fields := map[string]string{}
		if rejected.RequiredDays > 0 {
			fields["requiredDays"] = strconv.Itoa(rejected.RequiredDays)
		}
		body := gin.H{
			"code":      rejected.Code,
			"message":   lotteryEntryErrorMessage(rejected.Code),
			"requestId": middleware.GetRequestID(c),
		}
		if len(fields) > 0 {
			body["fields"] = fields
		}
		c.JSON(status, body)
		return
	}
	switch {
	case errors.Is(err, lotterydomain.ErrLotteryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Lottery not found.", "requestId": middleware.GetRequestID(c)})
	case errors.Is(err, lotterydomain.ErrLotteryAlreadyEntered):
		c.JSON(http.StatusConflict, gin.H{
			"code": "lottery_already_entered", "message": "You have already entered this lottery.",
			"requestId": middleware.GetRequestID(c),
		})
	case errors.Is(err, lotterydomain.ErrLotteryClosed), errors.Is(err, lotterydomain.ErrLotteryNotReady):
		c.JSON(http.StatusConflict, gin.H{"message": "Lottery is closed or already settled.", "requestId": middleware.GetRequestID(c)})
	case errors.Is(err, lotterydomain.ErrLotteryNotEligible):
		c.JSON(http.StatusForbidden, gin.H{"message": "This account is not eligible for the lottery.", "requestId": middleware.GetRequestID(c)})
	case errors.Is(err, lotterydomain.ErrLotteryInvalidRules):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Lottery rules are invalid.", "requestId": middleware.GetRequestID(c)})
	case errors.Is(err, lotterydomain.ErrLotteryIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "The idempotency key was already used with different lottery rules.", "requestId": middleware.GetRequestID(c)})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
	}
}

func lotteryEntryErrorMessage(code string) string {
	switch code {
	case lotterydomain.EntryRejectedInactive:
		return "Lottery account is not active."
	case lotterydomain.EntryRejectedAge:
		return "Lottery account age requirement not met."
	case lotterydomain.EntryRejectedCreator:
		return "The lottery creator cannot enter this activity."
	case lotterydomain.EntryRejectedFull:
		return "Lottery entry limit has been reached."
	default:
		return "Lottery entry is closed."
	}
}
