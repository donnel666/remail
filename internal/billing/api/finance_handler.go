package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/gin-gonic/gin"
)

// GET /v1/admin/cards/:cardKey/redemptions
func (h *BillingHandler) GetAdminCardRedemptions(c *gin.Context) {
	cardKey := strings.TrimSpace(c.Param("cardKey"))
	items, err := h.module.WalletUseCase.ListCardRedemptions(c.Request.Context(), cardKey)
	if err != nil {
		if errors.Is(err, domain.ErrCardNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"message": "Card key not found.", "requestId": middleware.GetRequestID(c)})
			return
		}
		writeBillingError(c, err)
		return
	}
	redemptions := make([]CardRedemptionResponse, len(items))
	for i := range items {
		redemptions[i] = cardRedemptionResponse(items[i])
	}
	c.JSON(http.StatusOK, CardRedemptionListResponse{Redemptions: redemptions})
}

// POST /v1/admin/cards/enable
func (h *BillingHandler) PostAdminCardsEnable(c *gin.Context) {
	h.postAdminCardsBulk(c, domain.CardKeyStatusEnabled)
}

// POST /v1/admin/cards/disable
func (h *BillingHandler) PostAdminCardsDisable(c *gin.Context) {
	h.postAdminCardsBulk(c, domain.CardKeyStatusDisabled)
}

func (h *BillingHandler) postAdminCardsBulk(c *gin.Context, status domain.CardKeyStatus) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	var req CardBulkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	selection := billingapp.CardBulkSelection{
		Mode:     req.Selection.Mode,
		CardKeys: req.Selection.CardKeys,
	}
	if req.Selection.Filter != nil {
		f := req.Selection.Filter
		filter := billingapp.CardBulkFilter{
			Search:       f.Search,
			OwnerRole:    strings.TrimSpace(f.OwnerRole),
			OwnerGroupID: f.OwnerGroupID,
		}
		if f.Status != "" {
			normalized, valid := domain.NormalizeCardStatus(f.Status)
			if !valid {
				writeBillingError(c, domain.ErrInvalidCardStatus)
				return
			}
			filter.Status = normalized
		}
		selection.Filter = &filter
	}
	result, err := h.module.WalletUseCase.BulkSetCardStatus(c.Request.Context(), selection, status)
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "billing.card.bulk_status", "bulk", "failure", "Bulk card status update failed.")
		writeBillingError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "billing.card.bulk_status", "bulk", "success", "Bulk card status updated.")
	c.JSON(http.StatusOK, AdminBulkResponse{Requested: result.Requested, Affected: result.Affected, Skipped: result.Skipped})
}

// GET /v1/admin/transactions
func (h *BillingHandler) GetAdminTransactions(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter := billingapp.AdminTransactionFilter{
		Search:      c.Query("search"),
		CreatedFrom: parseTimeQuery(c, "createdFrom"),
		CreatedTo:   parseTimeQuery(c, "createdTo"),
	}
	if rawType := strings.TrimSpace(c.Query("type")); rawType != "" {
		txType, valid := domain.NormalizeTransactionType(rawType)
		if !valid {
			writeBillingError(c, domain.ErrInvalidFilter)
			return
		}
		filter.Type = txType
	}
	if rawDir := strings.TrimSpace(c.Query("direction")); rawDir != "" {
		dir, valid := domain.NormalizeTransactionDirection(rawDir)
		if !valid {
			writeBillingError(c, domain.ErrInvalidFilter)
			return
		}
		filter.Direction = dir
	}
	result, err := h.module.WalletUseCase.ListAdminTransactions(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	items := make([]AdminTransactionItem, len(result.Items))
	for i := range result.Items {
		items[i] = adminTransactionItem(result.Items[i])
	}
	c.JSON(http.StatusOK, AdminTransactionListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

// POST /v1/admin/transactions/:id/reverse
func (h *BillingHandler) PostAdminTransactionReverse(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		writeBillingError(c, domain.ErrIdempotencyRequired)
		return
	}
	result, err := h.module.WalletUseCase.ReverseTransaction(c.Request.Context(), billingapp.ReverseTransactionRequest{
		TransactionID:  id,
		IdempotencyKey: idempotencyKey,
		RequestID:      middleware.GetRequestID(c),
		OperationLog:   h.operationLog(c, operatorUserID, "billing.transaction.reverse", fmt.Sprintf("%d", id), "success", "Transaction reversed."),
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "billing.transaction.reverse", fmt.Sprintf("%d", id), "failure", "Transaction reversal failed.")
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, ReverseTransactionResponse{
		Original: adminTransactionItem(result.Original),
		Reversal: adminTransactionItem(result.Reversal),
	})
}

// GET /v1/admin/wallets
func (h *BillingHandler) GetAdminWallets(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	if h.module.UserDirectory == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.module.WalletUseCase.ListAdminWallets(c.Request.Context(), c.Query("search"), offset, limit)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	items := make([]AdminWalletItem, len(result.Items))
	for i := range result.Items {
		items[i] = adminWalletItem(result.Items[i])
	}
	c.JSON(http.StatusOK, AdminWalletListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

// POST /v1/admin/wallets/:userId/withdraw
func (h *BillingHandler) PostAdminWalletWithdraw(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	userID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	var req AdminWithdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeInvalidBody(c, err)
		return
	}
	result, err := h.module.WalletUseCase.WithdrawSupplier(c.Request.Context(), billingapp.WithdrawSupplierRequest{
		UserID:         userID,
		Amount:         req.Amount,
		Note:           req.Note,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
		OperationLog:   h.operationLog(c, operatorUserID, "billing.wallet.withdraw", fmt.Sprintf("%d", userID), "success", "Supplier balance withdrawn."),
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "billing.wallet.withdraw", fmt.Sprintf("%d", userID), "failure", "Supplier withdrawal failed.")
		writeBillingError(c, err)
		return
	}
	summary, err := h.module.WalletUseCase.GetWallet(c.Request.Context(), userID)
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, WalletAdjustmentResponse{
		Wallet:      walletResponse(*summary),
		Transaction: transactionResponse(result.Transaction),
	})
}

// GET /v1/admin/finance/summary
func (h *BillingHandler) GetAdminFinanceSummary(c *gin.Context) {
	result, err := h.module.WalletUseCase.FinanceSummary(c.Request.Context(), parseTimeQuery(c, "createdFrom"), parseTimeQuery(c, "createdTo"))
	if err != nil {
		writeBillingError(c, err)
		return
	}
	c.JSON(http.StatusOK, financeSummaryResponse(*result))
}

// ---- response builders --------------------------------------------------

func adminCardResponse(card billingapp.AdminCard) CardKeyResponse {
	resp := cardResponse(card.Card)
	if card.Owner != nil {
		o := *card.Owner
		id := o.UserID
		resp.OwnerUserID = &id
		resp.OwnerEmail = &o.Email
		resp.OwnerNickname = &o.Nickname
		resp.OwnerRole = &o.Role
		resp.OwnerGroupName = &o.GroupName
		if o.GroupID != 0 {
			gid := o.GroupID
			resp.OwnerGroupID = &gid
		}
	}
	return resp
}

func cardFacetsResponse(f billingapp.CardFacets) CardKeyFacets {
	groups := make([]GroupFacetResponse, len(f.Groups))
	for i, g := range f.Groups {
		groups[i] = GroupFacetResponse{ID: g.ID, Name: g.Name, Count: g.Count}
	}
	return CardKeyFacets{
		Role: CardRoleFacetResponse{
			All:        f.Role.All,
			User:       f.Role.User,
			Supplier:   f.Role.Supplier,
			Admin:      f.Role.Admin,
			SuperAdmin: f.Role.SuperAdmin,
		},
		Group: groups,
		Status: CardStatusFacetResponse{
			All:      f.Status.All,
			Enabled:  f.Status.Enabled,
			Disabled: f.Status.Disabled,
		},
	}
}

func cardRedemptionResponse(r billingapp.AdminCardRedemption) CardRedemptionResponse {
	resp := CardRedemptionResponse{
		ID:         r.Redemption.ID,
		CardKey:    r.Redemption.CardKey,
		UserID:     r.Redemption.UserID,
		Amount:     r.Amount,
		RedeemedAt: r.Redemption.RedeemedAt,
	}
	if r.User != nil {
		resp.UserEmail = r.User.Email
		resp.UserNickname = r.User.Nickname
		resp.UserRole = r.User.Role
		resp.UserGroupName = r.User.GroupName
	}
	return resp
}

func adminTransactionItem(at billingapp.AdminTransaction) AdminTransactionItem {
	t := at.Transaction
	item := AdminTransactionItem{
		ID:              t.ID,
		TransactionNo:   t.TransactionNo,
		UserID:          t.UserID,
		TransactionType: string(t.TransactionType),
		BalanceBucket:   string(t.BalanceBucket),
		Direction:       string(t.Direction),
		Amount:          t.Amount,
		BalanceBefore:   t.BalanceBefore,
		BalanceAfter:    t.BalanceAfter,
		BizType:         t.BizType,
		BizID:           t.BizID,
		CreatedAt:       t.CreatedAt,
		Reversed:        at.Reversed,
		ReversedByNo:    at.ReversedByNo,
		ReversalOfNo:    t.ReversalOfNo,
	}
	if at.User != nil {
		item.UserEmail = at.User.Email
		item.UserNickname = at.User.Nickname
		item.UserRole = at.User.Role
		item.UserGroupName = at.User.GroupName
	}
	return item
}

func adminWalletItem(w billingapp.AdminWallet) AdminWalletItem {
	return AdminWalletItem{
		UserID:            w.Entry.UserID,
		UserEmail:         w.Entry.Email,
		UserNickname:      w.Entry.Nickname,
		UserRole:          w.Entry.Role,
		UserGroupName:     w.Entry.GroupName,
		ConsumerBalance:   w.Wallet.ConsumerBalance,
		SupplierAvailable: w.Wallet.SupplierAvailable,
		SupplierFrozen:    w.Wallet.SupplierFrozen,
		UpdatedAt:         nilIfZeroTime(w.Wallet.UpdatedAt),
	}
}

// nilIfZeroTime maps a zero timestamp (a user with no wallet row, zero-filled)
// to null so the UI renders "-" instead of a year-0001 date.
func nilIfZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func financeSummaryResponse(s billingapp.FinanceSummaryResult) FinanceSummaryResponse {
	trend := make([]FinanceTrendPoint, len(s.Trend))
	for i, p := range s.Trend {
		trend[i] = FinanceTrendPoint{
			Label:           p.Label,
			Recharge:        p.Recharge,
			Spend:           p.Spend,
			Withdraw:        p.Withdraw,
			Refund:          p.Refund,
			PlatformRevenue: p.PlatformRevenue,
			AccountRevenue:  p.AccountRevenue,
		}
	}
	return FinanceSummaryResponse{
		RechargeAmount:  s.RechargeAmount,
		SpendAmount:     s.SpendAmount,
		WithdrawAmount:  s.WithdrawAmount,
		RefundAmount:    s.RefundAmount,
		PlatformRevenue: s.PlatformRevenue,
		AccountRevenue:  s.AccountRevenue,
		Trend:           trend,
		HotProjects:     financeHotItems(s.HotProjects),
		HotProducts:     financeHotItems(s.HotProducts),
	}
}

func financeHotItems(items []billingapp.HotItem) []FinanceHotItem {
	out := make([]FinanceHotItem, len(items))
	for i, it := range items {
		out[i] = FinanceHotItem{Name: it.Name, Amount: it.Amount, Count: it.Count}
	}
	return out
}

// ---- query helpers ------------------------------------------------------

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	parsed, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || parsed == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return uint(parsed), true
}

func parseUintQuery(c *gin.Context, name string) uint {
	parsed, err := strconv.ParseUint(strings.TrimSpace(c.Query(name)), 10, 64)
	if err != nil {
		return 0
	}
	return uint(parsed)
}

func parseTimeQuery(c *gin.Context, name string) *time.Time {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &parsed
}
