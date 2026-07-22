package domain

import (
	"errors"
	"strings"
	"time"
)

type ServiceMode string

const (
	ServiceModeCode     ServiceMode = "code"
	ServiceModePurchase ServiceMode = "purchase"
)

type SupplyPolicy string

const (
	SupplyPolicyPrivateFirst SupplyPolicy = "private_first"
	SupplyPolicyPublicOnly   SupplyPolicy = "public_only"
)

type ProductType string

const (
	ProductTypeMicrosoft ProductType = "microsoft"
	ProductTypeDomain    ProductType = "domain"
)

type OrderStatus string

const (
	OrderStatusPendingPayment OrderStatus = "pending_payment"
	OrderStatusPaid           OrderStatus = "paid"
	OrderStatusActive         OrderStatus = "active"
	OrderStatusCompleted      OrderStatus = "completed"
	OrderStatusRefunded       OrderStatus = "refunded"
	OrderStatusFailed         OrderStatus = "failed"
	OrderStatusClosed         OrderStatus = "closed"
)

type OrderFailureCode string

const (
	OrderFailureUnknown               OrderFailureCode = "unknown"
	OrderFailureInsufficientInventory OrderFailureCode = "insufficient_inventory"
	OrderFailureInsufficientBalance   OrderFailureCode = "insufficient_balance"
	OrderFailureAllocation            OrderFailureCode = "allocation_failed"
	OrderFailureServiceToken          OrderFailureCode = "service_token_failed"
	OrderFailureActivation            OrderFailureCode = "activation_failed"
)

type ClientChannel string

const (
	ClientChannelConsole ClientChannel = "console"
	ClientChannelAPIKey  ClientChannel = "api_key"
)

type AllocationType string

const (
	AllocationTypeMicrosoft AllocationType = "microsoft"
	AllocationTypeDomain    AllocationType = "domain"
)

type OperatorType string

const (
	OperatorTypeUser    OperatorType = "user"
	OperatorTypeAdmin   OperatorType = "admin"
	OperatorTypeSystem  OperatorType = "system"
	OperatorTypeOpenAPI OperatorType = "openapi"
)

type Order struct {
	ID                      uint
	OrderNo                 string
	UserID                  uint
	ProjectID               uint
	ProjectProductID        uint
	ProductType             ProductType
	ServiceMode             ServiceMode
	SupplyPolicy            SupplyPolicy
	Status                  OrderStatus
	FailureCode             OrderFailureCode
	PayAmount               string
	RefundAmount            string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	DebitTxID               *uint
	RefundTxID              *uint
	AllocationType          *AllocationType
	MicrosoftAllocID        *uint
	DomainAllocID           *uint
	DeliveryEmail           string
	ReceiveStartedAt        *time.Time
	ReceiveUntil            *time.Time
	ActivatedAt             *time.Time
	AfterSaleUntil          *time.Time
	ClientChannel           ClientChannel
	APIKeyID                *uint
	IdempotencyKey          string
	RequestFingerprint      string
	ServiceCleanupStatus    string
	ArchivedAt              *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
	Version                 int
}

type OrderEvent struct {
	ID           uint
	EventNo      string
	OrderNo      string
	EventType    string
	FromStatus   *OrderStatus
	ToStatus     *OrderStatus
	OperatorType OperatorType
	Reason       string
	EventContext string
	CreatedAt    time.Time
}

var (
	ErrInvalidOrderRequest    = errors.New("trade: invalid order request")
	ErrOrderNotFound          = errors.New("trade: order not found")
	ErrOrderForbidden         = errors.New("trade: order forbidden")
	ErrOrderStateConflict     = errors.New("trade: order state conflict")
	ErrIdempotencyRequired    = errors.New("trade: idempotency key required")
	ErrIdempotencyConflict    = errors.New("trade: idempotency conflict")
	ErrInsufficientInventory  = errors.New("trade: insufficient inventory")
	ErrInsufficientBalance    = errors.New("trade: insufficient balance")
	ErrProjectUnavailable     = errors.New("trade: project is not available")
	ErrOrderCompensationError = errors.New("trade: order compensation failed")
	ErrCheckoutBusy           = errors.New("trade: checkout already queued for user")
	ErrCheckoutOverloaded     = errors.New("trade: checkout queue is full")
	ErrCheckoutTimeBudget     = errors.New("trade: checkout time budget exhausted")
)

func NormalizeServiceMode(value string) (ServiceMode, bool) {
	switch ServiceMode(strings.ToLower(strings.TrimSpace(value))) {
	case ServiceModeCode:
		return ServiceModeCode, true
	case ServiceModePurchase:
		return ServiceModePurchase, true
	default:
		return "", false
	}
}

func NormalizeSupplyPolicy(value string) (SupplyPolicy, bool) {
	switch SupplyPolicy(strings.ToLower(strings.TrimSpace(value))) {
	case "", SupplyPolicyPrivateFirst:
		return SupplyPolicyPrivateFirst, true
	case SupplyPolicyPublicOnly:
		return SupplyPolicyPublicOnly, true
	default:
		return "", false
	}
}

func IsTerminalStatus(status OrderStatus) bool {
	switch status {
	case OrderStatusCompleted, OrderStatusRefunded, OrderStatusFailed, OrderStatusClosed:
		return true
	default:
		return false
	}
}
