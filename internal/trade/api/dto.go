package api

import "time"

type CreateOrderRequest struct {
	ProjectID   uint   `json:"projectId"`
	ProductID   uint   `json:"productId"`
	EmailSuffix string `json:"emailSuffix,omitempty"`
}

type CreateOrderBatchRequest struct {
	CreateOrderRequest
	Quantity int `json:"quantity"`
}

type OrderBatchItemErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CreateOrderBatchItemResponse struct {
	Index  int                          `json:"index"`
	Status string                       `json:"status"`
	Order  OrderResponse                `json:"order"`
	Error  *OrderBatchItemErrorResponse `json:"error,omitempty"`
}

type CreateOrderBatchResponse []CreateOrderBatchItemResponse

type AdminOrderCommandRequest struct {
	Reason string `json:"reason"`
}

// OrderOwnerResponse is the buyer summary attached to admin site-wide order
// rows. It is nil on the buyer's own order list.
type OrderOwnerResponse struct {
	UserID    uint   `json:"userId"`
	Email     string `json:"email,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	GroupName string `json:"groupName,omitempty"`
	Role      string `json:"role,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type OrderResponse struct {
	ID                   uint                `json:"id"`
	OrderNo              string              `json:"orderNo"`
	UserID               uint                `json:"userId"`
	Owner                *OrderOwnerResponse `json:"owner,omitempty"`
	ProjectID            uint                `json:"projectId"`
	ProjectName          string              `json:"projectName,omitempty"`
	ProjectLogoURL       *string             `json:"projectLogoUrl,omitempty"`
	ProjectProductID     uint                `json:"projectProductId"`
	ProductType          string              `json:"productType"`
	ServiceMode          string              `json:"serviceMode"`
	SupplyPolicy         string              `json:"supplyPolicy"`
	Status               string              `json:"status"`
	FailureCode          string              `json:"failureCode,omitempty"`
	PayAmount            string              `json:"payAmount"`
	RefundAmount         string              `json:"refundAmount"`
	AllocationType       string              `json:"allocationType,omitempty"`
	AllocationID         uint                `json:"allocationId,omitempty"`
	DeliveryEmail        string              `json:"deliveryEmail"`
	ReceiveStartedAt     *time.Time          `json:"receiveStartedAt,omitempty"`
	ReceiveUntil         *time.Time          `json:"receiveUntil,omitempty"`
	ActivatedAt          *time.Time          `json:"activatedAt,omitempty"`
	AfterSaleUntil       *time.Time          `json:"afterSaleUntil,omitempty"`
	ClientChannel        string              `json:"clientChannel"`
	APIKeyID             *uint               `json:"apiKeyId,omitempty"`
	ServiceCleanupStatus string              `json:"serviceCleanupStatus"`
	ServiceToken         string              `json:"serviceToken,omitempty"`
	HasDelivery          bool                `json:"hasDelivery"`
	VerificationCode     string              `json:"verificationCode,omitempty"`
	LastMailReceivedAt   *time.Time          `json:"lastMailReceivedAt,omitempty"`
	ArchivedAt           *time.Time          `json:"archivedAt,omitempty"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
}

type OrderListResponse struct {
	Items       []OrderResponse          `json:"items"`
	Total       int64                    `json:"total"`
	Offset      int                      `json:"offset"`
	NextAfterID *uint                    `json:"nextAfterId,omitempty"`
	HasNext     bool                     `json:"hasNext"`
	Limit       int                      `json:"limit"`
	Facets      *OrderListFacetsResponse `json:"facets,omitempty"`
}

type OrderStatusFacetsResponse struct {
	All            int64 `json:"all"`
	PendingPayment int64 `json:"pending_payment"`
	Paid           int64 `json:"paid"`
	Active         int64 `json:"active"`
	Completed      int64 `json:"completed"`
	Refunded       int64 `json:"refunded"`
	Failed         int64 `json:"failed"`
	Closed         int64 `json:"closed"`
}

type OrderServiceModeFacetsResponse struct {
	All      int64 `json:"all"`
	Code     int64 `json:"code"`
	Purchase int64 `json:"purchase"`
}

type OrderKeyFacetResponse struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type OrderListFacetsResponse struct {
	Status      OrderStatusFacetsResponse      `json:"status"`
	ServiceMode OrderServiceModeFacetsResponse `json:"serviceMode"`
	Domains     []OrderKeyFacetResponse        `json:"domains"`
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
