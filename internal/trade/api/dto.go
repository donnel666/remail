package api

import "time"

type CreateOrderRequest struct {
	ProjectID   uint   `json:"projectId"`
	ProductID   uint   `json:"productId"`
	EmailSuffix string `json:"emailSuffix,omitempty"`
}

type AdminOrderCommandRequest struct {
	Reason string `json:"reason"`
}

type OrderResponse struct {
	ID                   uint       `json:"id"`
	OrderNo              string     `json:"orderNo"`
	UserID               uint       `json:"userId"`
	ProjectID            uint       `json:"projectId"`
	ProjectProductID     uint       `json:"projectProductId"`
	ProductType          string     `json:"productType"`
	ServiceMode          string     `json:"serviceMode"`
	SupplyPolicy         string     `json:"supplyPolicy"`
	Status               string     `json:"status"`
	FailureCode          string     `json:"failureCode,omitempty"`
	PayAmount            string     `json:"payAmount"`
	RefundAmount         string     `json:"refundAmount"`
	AllocationType       string     `json:"allocationType,omitempty"`
	AllocationID         uint       `json:"allocationId,omitempty"`
	DeliveryEmail        string     `json:"deliveryEmail"`
	ReceiveStartedAt     *time.Time `json:"receiveStartedAt,omitempty"`
	ReceiveUntil         *time.Time `json:"receiveUntil,omitempty"`
	ActivatedAt          *time.Time `json:"activatedAt,omitempty"`
	AfterSaleUntil       *time.Time `json:"afterSaleUntil,omitempty"`
	ClientChannel        string     `json:"clientChannel"`
	APIKeyID             *uint      `json:"apiKeyId,omitempty"`
	ServiceCleanupStatus string     `json:"serviceCleanupStatus"`
	ServiceToken         string     `json:"serviceToken,omitempty"`
	HasDelivery          bool       `json:"hasDelivery"`
	VerificationCode     string     `json:"verificationCode,omitempty"`
	LastMailReceivedAt   *time.Time `json:"lastMailReceivedAt,omitempty"`
	ArchivedAt           *time.Time `json:"archivedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type OrderListResponse struct {
	Items       []OrderResponse `json:"items"`
	NextAfterID *uint           `json:"nextAfterId,omitempty"`
	HasNext     bool            `json:"hasNext"`
	Limit       int             `json:"limit"`
}

type OrderEventResponse struct {
	EventNo      string    `json:"eventNo"`
	OrderNo      string    `json:"orderNo"`
	EventType    string    `json:"eventType"`
	FromStatus   string    `json:"fromStatus,omitempty"`
	ToStatus     string    `json:"toStatus,omitempty"`
	OperatorType string    `json:"operatorType"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type OrderEventListResponse struct {
	Items  []OrderEventResponse `json:"items"`
	Total  int64                `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

type ExpireOrdersResponse struct {
	CodeTimedOut                int `json:"codeTimedOut"`
	PurchaseActivationCompleted int `json:"purchaseActivationCompleted"`
	PurchaseWarrantyCompleted   int `json:"purchaseWarrantyCompleted"`
	CodeCleaned                 int `json:"codeCleaned"`
	CleanupRetried              int `json:"cleanupRetried"`
	DeliveryReconciled          int `json:"deliveryReconciled"`
	Failed                      int `json:"failed"`
}
