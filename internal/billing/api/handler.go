package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/gin-gonic/gin"
)

type BillingHandler struct {
	module  *BillingModule
	checker middleware.PermissionChecker
}

func NewBillingHandler(module *BillingModule, checker middleware.PermissionChecker) *BillingHandler {
	return &BillingHandler{module: module, checker: checker}
}

func (h *BillingHandler) GetWallet(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	summary, err := h.module.WalletUseCase.GetWallet(c.Request.Context(), userID)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, walletResponse(*summary))
}

func (h *BillingHandler) GetWalletReferrals(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	summary, err := h.module.WalletUseCase.GetReferralSummary(c.Request.Context(), userID)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, WalletReferralResponse{
		InviteCount:    summary.InviteCount,
		PendingRewards: summary.PendingRewards,
		TotalEarned:    summary.TotalEarned,
	})
}

func (h *BillingHandler) PostWalletReferralTransfer(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	result, err := h.module.WalletUseCase.TransferReferralRewards(c.Request.Context(), billingapp.TransferReferralRewardsRequest{
		UserID:         userID,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
	})
	if err != nil {
		writeBillingError(c, err)
		return
	}
	summary, err := h.module.WalletUseCase.GetWallet(c.Request.Context(), userID)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, WalletReferralTransferResponse{
		Wallet:            walletResponse(*summary),
		Transaction:       transactionResponse(result.Transaction),
		TransferredAmount: result.TransferredAmount,
		TransferredCount:  result.TransferredCount,
	})
}

func (h *BillingHandler) GetWalletTransactions(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	filter := billingapp.TransactionListFilter{
		UserID: userID,
		Search: c.Query("search"),
	}
	if strings.EqualFold(strings.TrimSpace(c.Query("scope")), "all") {
		if !h.canReadAll(c, "billing:wallet") {
			return
		}
		filter.UserID = 0
	}

	result, err := h.module.WalletUseCase.ListTransactions(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	items := make([]TransactionItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = transactionResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, TransactionListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *BillingHandler) GetRecharges(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	filter := billingapp.RechargeListFilter{
		UserID: userID,
		Search: c.Query("search"),
	}
	if rawStatus := strings.TrimSpace(c.Query("status")); rawStatus != "" {
		status, valid := domain.NormalizeRechargeStatus(rawStatus)
		if !valid {
			writeBillingError(c, domain.ErrInvalidFilter)
			return
		}
		filter.Status = status
	}
	if strings.EqualFold(strings.TrimSpace(c.Query("scope")), "all") {
		if !h.canReadAll(c, "billing:wallet") {
			return
		}
		filter.UserID = 0
	}

	result, err := h.module.WalletUseCase.ListRecharges(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	items := make([]RechargeItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = rechargeResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, RechargeListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *BillingHandler) PostCardRedeem(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	var req RedeemCardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	result, err := h.module.WalletUseCase.RedeemCard(c.Request.Context(), billingapp.RedeemCardRequest{
		UserID:         userID,
		CardKey:        req.CardKey,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
	})
	if err != nil {
		writeBillingError(c, err)
		return
	}
	summary, err := h.module.WalletUseCase.GetWallet(c.Request.Context(), userID)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, RedeemCardResponse{
		Wallet:      walletResponse(*summary),
		Transaction: transactionResponse(result.Transaction),
		Card:        cardResponse(result.Card),
	})
}

func (h *BillingHandler) PostAdminWalletCredit(c *gin.Context) {
	h.postAdminWalletAdjustment(c, true)
}

func (h *BillingHandler) PostAdminWalletDebit(c *gin.Context) {
	h.postAdminWalletAdjustment(c, false)
}

func (h *BillingHandler) postAdminWalletAdjustment(c *gin.Context, credit bool) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	userID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	var req AdminAdjustWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	command := billingapp.AdjustConsumerBalanceRequest{
		UserID:         userID,
		Amount:         req.Amount,
		Reason:         req.Reason,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
	}
	operationType := "billing.wallet.debit"
	if credit {
		operationType = "billing.wallet.credit"
	}
	command.OperationLog = h.operationLog(c, operatorUserID, operationType, fmt.Sprintf("%d", userID), "success", "Wallet adjusted.")
	var (
		result *billingapp.AdjustBalanceResult
		err    error
	)
	if credit {
		result, err = h.module.WalletUseCase.CreditConsumer(c.Request.Context(), command)
	} else {
		result, err = h.module.WalletUseCase.DebitConsumer(c.Request.Context(), command)
	}
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, operationType, fmt.Sprintf("%d", userID), "failure", "Wallet adjustment failed.")
		writeBillingError(c, err)
		return
	}
	summary, err := h.module.WalletUseCase.GetWallet(c.Request.Context(), userID)
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, operationType, fmt.Sprintf("%d", userID), "failure", "Wallet adjustment result reload failed.")
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, WalletAdjustmentResponse{
		Wallet:      walletResponse(*summary),
		Transaction: transactionResponse(result.Transaction),
	})
}

func (h *BillingHandler) GetAdminCards(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter := billingapp.CardListFilter{Search: c.Query("search")}
	if rawStatus := strings.TrimSpace(c.Query("status")); rawStatus != "" {
		status, valid := domain.NormalizeCardStatus(rawStatus)
		if !valid {
			writeBillingError(c, domain.ErrInvalidCardStatus)
			return
		}
		filter.Status = status
	}
	result, err := h.module.WalletUseCase.ListCards(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	items := make([]CardKeyResponse, len(result.Items))
	for i := range result.Items {
		items[i] = cardResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, CardKeyListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *BillingHandler) PostAdminCards(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	var req CreateCardsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	result, err := h.module.WalletUseCase.CreateCards(c.Request.Context(), billingapp.CreateCardsRequest{
		Amount:          req.Amount,
		Count:           req.Count,
		MaxRedemptions:  req.MaxRedemptions,
		ExpireAt:        req.ExpireAt,
		CardKeys:        req.CardKeys,
		CreatedByUserID: operatorUserID,
		IdempotencyKey:  c.GetHeader("Idempotency-Key"),
		OperationLog:    h.operationLog(c, operatorUserID, "billing.card.create", "batch", "success", "Card keys created."),
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "billing.card.create", "batch", "failure", "Card keys create failed.")
		writeBillingError(c, err)
		return
	}
	items := make([]CardKeyResponse, len(result.Items))
	for i := range result.Items {
		items[i] = cardResponse(result.Items[i])
	}
	c.JSON(http.StatusCreated, CreateCardsResponse{
		Items:   items,
		Created: result.Created,
	})
}

func (h *BillingHandler) PatchAdminCard(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	cardKey := strings.TrimSpace(c.Param("cardKey"))
	var req UpdateCardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	var status *domain.CardKeyStatus
	if req.Status != nil {
		normalized, ok := domain.NormalizeCardStatus(*req.Status)
		if !ok {
			writeBillingError(c, domain.ErrInvalidCardStatus)
			return
		}
		status = &normalized
	}
	card, err := h.module.WalletUseCase.UpdateCard(c.Request.Context(), billingapp.UpdateCardRequest{
		CardKey:      cardKey,
		Status:       status,
		ExpireAt:     req.ExpireAt,
		ExpireAtSet:  req.ExpireAt != nil,
		OperationLog: h.operationLog(c, operatorUserID, "billing.card.update", safeCardResourceID(cardKey), "success", "Card key updated."),
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "billing.card.update", safeCardResourceID(cardKey), "failure", "Card key update failed.")
		if errors.Is(err, domain.ErrCardNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"message": "Card key not found.", "requestId": middleware.GetRequestID(c)})
			return
		}
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, cardResponse(*card))
}

func (h *BillingHandler) writeOperationLog(c *gin.Context, operatorUserID uint, operationType, resourceID, result, summary string) error {
	log := h.operationLog(c, operatorUserID, operationType, resourceID, result, summary)
	if log == nil {
		return nil
	}
	return h.module.OperationLogs.Create(c.Request.Context(), log)
}

func (h *BillingHandler) operationLog(c *gin.Context, operatorUserID uint, operationType, resourceID, result, summary string) *governancedomain.OperationLog {
	if h.module.OperationLogs == nil {
		return nil
	}
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "billing",
		ResourceID:     resourceID,
		Path:           c.FullPath(),
		Result:         result,
		SafeSummary:    summary,
		RequestID:      middleware.GetRequestID(c),
	}
}

func (h *BillingHandler) canReadAll(c *gin.Context, resource string) bool {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	role, ok := middleware.GetCurrentRole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	allowed, err := h.checker.Check(c.Request.Context(), userID, role, resource, "read")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	return true
}

func requireCurrentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return userID, true
}

func parseUserIDParam(c *gin.Context) (uint, bool) {
	raw := c.Param("userId")
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid user ID.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return uint(parsed), true
}

func parsePagination(c *gin.Context) (int, int, bool) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid query parameters.", "requestId": middleware.GetRequestID(c)})
		return 0, 0, false
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid query parameters.", "requestId": middleware.GetRequestID(c)})
		return 0, 0, false
	}
	return offset, limit, true
}

func walletResponse(summary domain.WalletSummary) WalletResponse {
	return walletOnlyResponse(summary.Wallet, summary.HistoricalSpend, summary.OrderCount)
}

func walletOnlyResponse(wallet domain.Wallet, historicalSpend string, orderCount int64) WalletResponse {
	return WalletResponse{
		UserID:            wallet.UserID,
		ConsumerBalance:   wallet.ConsumerBalance,
		SupplierAvailable: wallet.SupplierAvailable,
		SupplierFrozen:    wallet.SupplierFrozen,
		HistoricalSpend:   historicalSpend,
		OrderCount:        orderCount,
		UpdatedAt:         wallet.UpdatedAt,
	}
}

func transactionResponse(transaction domain.Transaction) TransactionItemResponse {
	return TransactionItemResponse{
		ID:              transaction.ID,
		TransactionNo:   transaction.TransactionNo,
		UserID:          transaction.UserID,
		TransactionType: string(transaction.TransactionType),
		BalanceBucket:   string(transaction.BalanceBucket),
		Direction:       string(transaction.Direction),
		Amount:          transaction.Amount,
		BalanceBefore:   transaction.BalanceBefore,
		BalanceAfter:    transaction.BalanceAfter,
		BizType:         transaction.BizType,
		BizID:           transaction.BizID,
		CreatedAt:       transaction.CreatedAt,
	}
}

func rechargeResponse(recharge domain.Recharge) RechargeItemResponse {
	return RechargeItemResponse{
		ID:            recharge.ID,
		RechargeNo:    recharge.RechargeNo,
		UserID:        recharge.UserID,
		PaymentMethod: recharge.PaymentMethod,
		RechargeQuota: recharge.RechargeQuota,
		PaymentAmount: recharge.PaymentAmount,
		Status:        string(recharge.Status),
		CreatedAt:     recharge.CreatedAt,
		UpdatedAt:     recharge.UpdatedAt,
	}
}

func cardResponse(card domain.CardKey) CardKeyResponse {
	return CardKeyResponse{
		CardKey:         card.Key,
		Amount:          card.Amount,
		Status:          string(card.Status),
		MaxRedemptions:  card.MaxRedemptions,
		RedeemedCount:   card.RedeemedCount,
		ExpireAt:        card.ExpireAt,
		CreatedByUserID: card.CreatedByUserID,
		CreatedAt:       card.CreatedAt,
		UpdatedAt:       card.UpdatedAt,
	}
}

func writeInvalidBody(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid request body.",
		"fields":    validationErrors(err),
		"requestId": middleware.GetRequestID(c),
	})
}

func writeBillingError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidAmount):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid amount.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidRecharge):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid recharge request.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidCardKey):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid card key.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidCardStatus):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid card status.", "requestId": requestID})
	case errors.Is(err, domain.ErrInsufficientBalance):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Insufficient balance.", "requestId": requestID})
	case errors.Is(err, domain.ErrCardNotFound), errors.Is(err, domain.ErrCardDisabled), errors.Is(err, domain.ErrCardExpired), errors.Is(err, domain.ErrCardExhausted):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Card key cannot be redeemed.", "requestId": requestID})
	case errors.Is(err, domain.ErrCardAlreadyRedeemed):
		c.JSON(http.StatusConflict, gin.H{"message": "Card key already redeemed.", "requestId": requestID})
	case errors.Is(err, domain.ErrDuplicateCardKey):
		c.JSON(http.StatusConflict, gin.H{"message": "Card key already exists.", "requestId": requestID})
	case errors.Is(err, domain.ErrIdempotencyRequired):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key is required.", "requestId": requestID})
	case errors.Is(err, domain.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidFilter):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid filter.", "requestId": requestID})
	case errors.Is(err, domain.ErrNoReferralRewards):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "No referral rewards available.", "requestId": requestID})
	case errors.Is(err, domain.ErrReferralRewardStateConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Current reward status does not allow this operation.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}

func validationErrors(err error) map[string]string {
	type validator interface {
		Field() string
		Tag() string
	}
	fields := map[string]string{}
	if errs, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range errs.Unwrap() {
			if v, ok := e.(validator); ok {
				fields[v.Field()] = v.Tag() + " validation failed"
			}
		}
	} else {
		fields["body"] = err.Error()
	}
	return fields
}

func safeCardResourceID(cardKey string) string {
	trimmed := strings.TrimSpace(cardKey)
	if len(trimmed) <= 10 {
		return "card_key"
	}
	return trimmed[:6] + "..." + trimmed[len(trimmed)-4:]
}
