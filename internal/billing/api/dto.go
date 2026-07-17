package api

import (
	"bytes"
	"encoding/json"
	"time"
)

type WalletResponse struct {
	UserID            uint      `json:"userId"`
	ConsumerBalance   string    `json:"consumerBalance"`
	SupplierAvailable string    `json:"supplierAvailable"`
	SupplierFrozen    string    `json:"supplierFrozen"`
	HistoricalSpend   string    `json:"historicalSpend"`
	OrderCount        int64     `json:"orderCount"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type WalletReferralResponse struct {
	InviteCount    int64  `json:"inviteCount"`
	PendingRewards string `json:"pendingRewards"`
	TotalEarned    string `json:"totalEarned"`
}

type WalletReferralTransferResponse struct {
	Wallet            WalletResponse          `json:"wallet"`
	Transaction       TransactionItemResponse `json:"transaction"`
	TransferredAmount string                  `json:"transferredAmount"`
	TransferredCount  int                     `json:"transferredCount"`
}

type TransactionItemResponse struct {
	ID              uint      `json:"id"`
	TransactionNo   string    `json:"transactionNo"`
	UserID          uint      `json:"userId"`
	TransactionType string    `json:"transactionType"`
	BalanceBucket   string    `json:"balanceBucket"`
	Direction       string    `json:"direction"`
	Amount          string    `json:"amount"`
	BalanceBefore   string    `json:"balanceBefore"`
	BalanceAfter    string    `json:"balanceAfter"`
	BizType         string    `json:"bizType"`
	BizID           string    `json:"bizId"`
	CreatedAt       time.Time `json:"createdAt"`
}

type TransactionListResponse struct {
	Items       []TransactionItemResponse `json:"items"`
	NextAfterID *uint                     `json:"nextAfterId,omitempty"`
	HasNext     bool                      `json:"hasNext"`
	Limit       int                       `json:"limit"`
}

type RechargeItemResponse struct {
	ID            uint      `json:"id"`
	RechargeNo    string    `json:"rechargeNo"`
	UserID        uint      `json:"userId"`
	PaymentMethod string    `json:"paymentMethod"`
	RechargeQuota string    `json:"rechargeQuota"`
	PaymentAmount string    `json:"paymentAmount"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type RechargeListResponse struct {
	Items  []RechargeItemResponse `json:"items"`
	Total  int64                  `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

type RedeemCardRequest struct {
	CardKey string `json:"cardKey" binding:"required"`
}

type RedeemCardResponse struct {
	Wallet      WalletResponse          `json:"wallet"`
	Transaction TransactionItemResponse `json:"transaction"`
	Card        CardKeyResponse         `json:"card"`
}

type WalletAdjustmentResponse struct {
	Wallet      WalletResponse          `json:"wallet"`
	Transaction TransactionItemResponse `json:"transaction"`
}

type AdminAdjustWalletRequest struct {
	Amount string `json:"amount" binding:"required"`
	Reason string `json:"reason" binding:"required"`
}

type AdminBulkAdjustWalletFilterRequest struct {
	Search      string     `json:"search"`
	Role        string     `json:"role" binding:"omitempty,oneof=user supplier admin super_admin"`
	Enabled     *bool      `json:"enabled"`
	UserGroupID uint       `json:"userGroupId"`
	CreatedFrom *time.Time `json:"createdFrom"`
	CreatedTo   *time.Time `json:"createdTo"`
}

type AdminBulkAdjustWalletSelectionRequest struct {
	Mode    string                              `json:"mode" binding:"required,oneof=ids filter"`
	UserIDs []uint                              `json:"userIds" binding:"omitempty,dive,gt=0"`
	Filter  *AdminBulkAdjustWalletFilterRequest `json:"filter"`
}

type AdminBulkAdjustWalletRequest struct {
	Selection AdminBulkAdjustWalletSelectionRequest `json:"selection" binding:"required"`
	Amount    string                                `json:"amount" binding:"required"`
	Reason    string                                `json:"reason" binding:"required"`
}

type AdminWalletBulkResponse struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

type AdminWalletBalanceResponse struct {
	UserID          uint   `json:"userId"`
	ConsumerBalance string `json:"consumerBalance"`
}

type AdminWalletBalanceListResponse struct {
	Balances []AdminWalletBalanceResponse `json:"balances"`
}

type CardKeyResponse struct {
	CardKey         string     `json:"cardKey"`
	Amount          string     `json:"amount"`
	Status          string     `json:"status"`
	MaxRedemptions  int        `json:"maxRedemptions"`
	RedeemedCount   int        `json:"redeemedCount"`
	ExpireAt        *time.Time `json:"expireAt"`
	CreatedByUserID *uint      `json:"createdByUserId"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	OwnerUserID     *uint      `json:"ownerUserId"`
	OwnerEmail      *string    `json:"ownerEmail"`
	OwnerNickname   *string    `json:"ownerNickname"`
	OwnerRole       *string    `json:"ownerRole"`
	OwnerGroupID    *uint      `json:"ownerGroupId"`
	OwnerGroupName  *string    `json:"ownerGroupName"`
}

type GroupFacetResponse struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type CardRoleFacetResponse struct {
	All        int `json:"all"`
	User       int `json:"user"`
	Supplier   int `json:"supplier"`
	Admin      int `json:"admin"`
	SuperAdmin int `json:"super_admin"`
}

type CardStatusFacetResponse struct {
	All      int `json:"all"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
}

type CardKeyFacets struct {
	Role   CardRoleFacetResponse   `json:"role"`
	Group  []GroupFacetResponse    `json:"group"`
	Status CardStatusFacetResponse `json:"status"`
}

type CardKeyListResponse struct {
	Items  []CardKeyResponse `json:"items"`
	Total  int64             `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
	Facets *CardKeyFacets    `json:"facets,omitempty"`
}

type CreateCardsRequest struct {
	Amount         string     `json:"amount" binding:"required"`
	Count          int        `json:"count,omitempty"`
	MaxRedemptions int        `json:"maxRedemptions,omitempty"`
	ExpireAt       *time.Time `json:"expireAt,omitempty"`
	CardKeys       []string   `json:"cardKeys,omitempty"`
}

type CreateCardsResponse struct {
	Items   []CardKeyResponse `json:"items"`
	Created int               `json:"created"`
}

// nullableTime distinguishes an absent JSON field (Set=false → leave unchanged)
// from an explicit null (Set=true, Value=nil → clear) from a value (Set=true,
// Value set). A plain *time.Time with omitempty cannot tell absent from null,
// which would make a PATCH either wipe an omitted field or ignore a null.
type nullableTime struct {
	Set   bool
	Value *time.Time
}

func (n *nullableTime) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.Value = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	n.Value = &t
	return nil
}

type UpdateCardRequest struct {
	Status         *string      `json:"status,omitempty"`
	ExpireAt       nullableTime `json:"expireAt"`
	MaxRedemptions *int         `json:"maxRedemptions,omitempty"`
}

type CardBulkFilterRequest struct {
	Search       string `json:"search"`
	Status       string `json:"status" binding:"omitempty,oneof=enabled disabled"`
	OwnerRole    string `json:"ownerRole" binding:"omitempty,oneof=user supplier admin super_admin"`
	OwnerGroupID uint   `json:"ownerGroupId"`
}

type CardBulkSelectionRequest struct {
	Mode     string                 `json:"mode" binding:"required,oneof=ids filter"`
	CardKeys []string               `json:"cardKeys" binding:"omitempty,dive,required"`
	Filter   *CardBulkFilterRequest `json:"filter"`
}

type CardBulkRequest struct {
	Selection CardBulkSelectionRequest `json:"selection" binding:"required"`
}

type AdminBulkResponse struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

type CardRedemptionResponse struct {
	ID            uint      `json:"id"`
	CardKey       string    `json:"cardKey"`
	UserID        uint      `json:"userId"`
	UserEmail     string    `json:"userEmail"`
	UserNickname  string    `json:"userNickname"`
	UserRole      string    `json:"userRole"`
	UserGroupName string    `json:"userGroupName"`
	Amount        string    `json:"amount"`
	RedeemedAt    time.Time `json:"redeemedAt"`
}

type CardRedemptionListResponse struct {
	Redemptions []CardRedemptionResponse `json:"redemptions"`
}

type AdminTransactionItem struct {
	ID              uint      `json:"id"`
	TransactionNo   string    `json:"transactionNo"`
	UserID          uint      `json:"userId"`
	UserEmail       string    `json:"userEmail"`
	UserNickname    string    `json:"userNickname"`
	UserRole        string    `json:"userRole"`
	UserGroupName   string    `json:"userGroupName"`
	TransactionType string    `json:"transactionType"`
	BalanceBucket   string    `json:"balanceBucket"`
	Direction       string    `json:"direction"`
	Amount          string    `json:"amount"`
	BalanceBefore   string    `json:"balanceBefore"`
	BalanceAfter    string    `json:"balanceAfter"`
	BizType         string    `json:"bizType"`
	BizID           string    `json:"bizId"`
	CreatedAt       time.Time `json:"createdAt"`
	Reversed        bool      `json:"reversed"`
	ReversedByNo    *string   `json:"reversedByNo"`
	ReversalOfNo    *string   `json:"reversalOfNo"`
}

type AdminTransactionListResponse struct {
	Items  []AdminTransactionItem `json:"items"`
	Total  int64                  `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

type ReverseTransactionResponse struct {
	Original AdminTransactionItem `json:"original"`
	Reversal AdminTransactionItem `json:"reversal"`
}

type AdminWalletItem struct {
	UserID            uint       `json:"userId"`
	UserEmail         string     `json:"userEmail"`
	UserNickname      string     `json:"userNickname"`
	UserRole          string     `json:"userRole"`
	UserGroupName     string     `json:"userGroupName"`
	ConsumerBalance   string     `json:"consumerBalance"`
	SupplierAvailable string     `json:"supplierAvailable"`
	SupplierFrozen    string     `json:"supplierFrozen"`
	UpdatedAt         *time.Time `json:"updatedAt"`
}

type AdminWalletListResponse struct {
	Items  []AdminWalletItem `json:"items"`
	Total  int               `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
}

type AdminWithdrawRequest struct {
	Amount string `json:"amount" binding:"required"`
	Note   string `json:"note"`
}

type FinanceTrendPoint struct {
	Label           string  `json:"label"`
	Recharge        float64 `json:"recharge"`
	Spend           float64 `json:"spend"`
	Withdraw        float64 `json:"withdraw"`
	Refund          float64 `json:"refund"`
	PlatformRevenue float64 `json:"platformRevenue"`
	AccountRevenue  float64 `json:"accountRevenue"`
}

type FinanceHotItem struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
	Count  int64  `json:"count"`
}

type FinanceSummaryResponse struct {
	RechargeAmount  string              `json:"rechargeAmount"`
	SpendAmount     string              `json:"spendAmount"`
	WithdrawAmount  string              `json:"withdrawAmount"`
	RefundAmount    string              `json:"refundAmount"`
	PlatformRevenue string              `json:"platformRevenue"`
	AccountRevenue  string              `json:"accountRevenue"`
	Trend           []FinanceTrendPoint `json:"trend"`
	HotProjects     []FinanceHotItem    `json:"hotProjects"`
	HotProducts     []FinanceHotItem    `json:"hotProducts"`
}
