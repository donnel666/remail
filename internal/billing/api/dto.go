package api

import "time"

type WalletResponse struct {
	UserID            uint      `json:"userId"`
	ConsumerBalance   string    `json:"consumerBalance"`
	SupplierAvailable string    `json:"supplierAvailable"`
	SupplierFrozen    string    `json:"supplierFrozen"`
	HistoricalSpend   string    `json:"historicalSpend"`
	OrderCount        int64     `json:"orderCount"`
	UpdatedAt         time.Time `json:"updatedAt"`
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
	Items  []TransactionItemResponse `json:"items"`
	Total  int64                     `json:"total"`
	Offset int                       `json:"offset"`
	Limit  int                       `json:"limit"`
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
}

type CardKeyListResponse struct {
	Items  []CardKeyResponse `json:"items"`
	Total  int64             `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
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

type UpdateCardRequest struct {
	Status   *string    `json:"status,omitempty"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
}
