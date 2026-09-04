package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/trade/domain"
)

type OrderingQuote struct {
	ProjectID                uint
	ProductID                uint
	ProductType              domain.ProductType
	PayAmount                string
	LegacyMicrosoftPayAmount string
	LegacyDomainPayAmount    string
	CodeWindowMinutes        int
	ActivationWindowMinutes  int
	WarrantyMinutes          int
}

type OrderingPort interface {
	GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode domain.ServiceMode) (*OrderingQuote, error)
}

type OrderingByTypePort interface {
	GetOrderingQuoteByType(ctx context.Context, projectID uint, productType domain.ProductType, buyerUserID uint, serviceMode domain.ServiceMode) (*OrderingQuote, error)
}

type WalletCommand struct {
	UserID         uint
	Amount         string
	Reason         string
	IdempotencyKey string
	RequestID      string
}

type WalletTransaction struct {
	ID uint
}

type WalletPort interface {
	ConsumerBalance(ctx context.Context, userID uint) (string, error)
	LockConsumer(ctx context.Context, userID uint) error
	DebitConsumer(ctx context.Context, cmd WalletCommand) (*WalletTransaction, error)
	RecordHistoricalZeroDebit(ctx context.Context, cmd WalletCommand) (*WalletTransaction, error)
	RefundConsumer(ctx context.Context, cmd WalletCommand) (*WalletTransaction, error)
}

type SupplyScope string

const (
	SupplyScopeOwned  SupplyScope = "owned"
	SupplyScopePublic SupplyScope = "public"
)

type AllocationCommand struct {
	OrderNo          string
	BuyerUserID      uint
	ProjectProductID uint
	ServiceMode      domain.ServiceMode
	SupplyScope      SupplyScope
	SupplyScopes     []SupplyScope
	EmailSuffix      string
	RequiredUntil    time.Time
	// FulfillExistingOrder permits allocation after the product has been
	// delisted. Trade sets it only after an order has been durably created;
	// standalone/new allocation callers keep the current-sale guard.
	FulfillExistingOrder bool
}

type AllocationResult struct {
	OrderNo     string
	Type        domain.AllocationType
	ID          uint
	ProductID   uint
	Created     bool
	Email       string
	SupplyScope SupplyScope
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

type AllocationPort interface {
	Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error)
	ReleaseByOrder(ctx context.Context, orderNo string) error
}

type InventoryAvailabilityCommand struct {
	ProjectID   uint
	ProductID   uint
	EmailSuffix string
	PublicOnly  bool
}

type InventoryAvailabilityPort interface {
	HasAvailableInventory(ctx context.Context, cmd InventoryAvailabilityCommand) (bool, error)
}

type InventoryUnavailablePort interface {
	MarkInventoryUnavailable(ctx context.Context, cmd InventoryAvailabilityCommand) (bool, error)
}

type RandomSuffixSelectionCommand struct {
	ProjectID            uint
	ProductID            uint
	BuyerUserID          uint
	SupplyScopes         []SupplyScope
	Selector             string
	FulfillExistingOrder bool
}

type RandomSuffixSelectionPort interface {
	SelectRandomSuffix(ctx context.Context, cmd RandomSuffixSelectionCommand) (string, error)
}

type HistoricalMicrosoftAllocationCommand struct {
	AliasOwnerID uint
	ProjectID    uint
	ProductID    uint
	ResourceID   uint
	Mailbox      string
	Email        string
	CreatedAt    time.Time
	ReleasedAt   time.Time
}

type HistoricalMicrosoftAllocationPort interface {
	ImportHistoricalMicrosoftAllocation(ctx context.Context, cmd HistoricalMicrosoftAllocationCommand) (*AllocationResult, error)
}

type HistoricalGmailAllocationCommand struct {
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Mailbox    string
	Email      string
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type HistoricalGmailAllocationPort interface {
	ImportHistoricalGmailAllocation(ctx context.Context, cmd HistoricalGmailAllocationCommand) (*AllocationResult, error)
}

var ErrHistoricalAllocationOwnerRequired = errors.New("historical allocation owner is required")

const historicalMicrosoftOwnerUserID uint = 1
const historicalGmailOwnerUserID uint = 1
const staleCheckoutRecoveryAfter = 15 * time.Minute

type OrderToken struct {
	TokenPlain string
	ExpireAt   *time.Time
}

type OrderTokenPort interface {
	IssueOrderToken(ctx context.Context, orderNo string, expireAt *time.Time) (*OrderToken, error)
	FindOrderTokenByOrder(ctx context.Context, orderNo string) (*OrderToken, error)
	FindOrderTokensByOrders(ctx context.Context, orderNos []string) (map[string]OrderToken, error)
	ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error
	DisableOrderToken(ctx context.Context, orderNo string, reason string) error
}

type OrderDeliverySummary struct {
	VerificationCode string
	ReceivedAt       time.Time
}

type OrderDeliveryNotification struct {
	OrderID    uint
	OrderNo    string
	ReceivedAt time.Time
}

type OrderDeliveryPort interface {
	FindOrderDelivery(ctx context.Context, orderID uint) (*OrderDeliverySummary, error)
	ListOrderDeliveries(ctx context.Context, orderIDs []uint) (map[uint]OrderDeliverySummary, error)
	ListPendingNotifications(ctx context.Context, afterOrderID uint, limit int) ([]OrderDeliveryNotification, error)
}

type GmailSupplyQuote struct {
	Source     string
	CostPoints string
	Available  uint64
}

type GmailSupplyPort interface {
	CheckSupply(ctx context.Context, projectID, productID, buyerUserID uint, mode domain.ServiceMode, policy domain.SupplyPolicy, payAmount string) (*GmailSupplyQuote, error)
}

type GmailPurchaseDelivery struct {
	AllocationID    uint
	ResourceID      uint
	SupplyScope     SupplyScope
	Email           string
	Password        string
	TwoFactorSecret string
	AppPassword     string
}

type GmailPurchaseSupplyPort interface {
	FindLocalPurchase(ctx context.Context, orderNo string) (*GmailPurchaseDelivery, error)
}

type SystemLogPort interface {
	Create(ctx context.Context, log *governancedomain.SystemLog) error
}

// ProjectDisplay contains the mutable project presentation fields used by
// order read models. Orders retain the project ID as their durable fact while
// the current name and logo are resolved in one bounded batch query.
type ProjectDisplay struct {
	Name    string
	LogoURL string
}

type ProjectDisplayPort interface {
	ProjectDisplays(ctx context.Context, projectIDs []uint) (map[uint]ProjectDisplay, error)
}

// OrderOwnerSummary is the IAM-owned safe summary of an order's buyer, used to
// enrich the administrator site-wide order list. It carries no authentication
// or permission-policy facts.
type OrderOwnerSummary struct {
	ID        uint
	Email     string
	Nickname  string
	GroupName string
	Role      string
	Enabled   bool
}

// OwnerLookupPort is published by IAM; enrichment is batched over the buyer IDs
// of one page of orders and only runs for the administrator site-wide scope.
type OwnerLookupPort interface {
	GetByIDs(ctx context.Context, ids []uint) (map[uint]OrderOwnerSummary, error)
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	LockOrderForUpdate(ctx context.Context, orderNo string) (*domain.Order, error)
	LoadOrCreatePendingOrder(ctx context.Context, cmd CreatePendingOrderCommand) (*domain.Order, bool, error)
	FindOrderByIdempotency(ctx context.Context, channel domain.ClientChannel, userID uint, apiKeyID *uint, idempotencyKey, requestFingerprint, randomRequestFingerprint string) (*domain.Order, error)
	FindOrder(ctx context.Context, orderNo string) (*domain.Order, error)
	FindOrdersByOrderNos(ctx context.Context, orderNos []string) (map[string]domain.Order, error)
	MarkPaid(ctx context.Context, cmd MarkPaidCommand) (*domain.Order, error)
	MarkActive(ctx context.Context, cmd MarkActiveCommand) (*domain.Order, error)
	MarkFailed(ctx context.Context, cmd MarkFailedCommand) (*domain.Order, error)
	RefundOrder(ctx context.Context, cmd RefundOrderCommand) (*domain.Order, bool, error)
	AttachFailedOrderRefund(ctx context.Context, cmd RefundOrderCommand) (*domain.Order, bool, error)
	CompleteExpiredOrder(ctx context.Context, orderNo string, reason string) (*domain.Order, bool, error)
	CloseActiveOrder(ctx context.Context, orderNo string, reason string) (*domain.Order, bool, error)
	MarkServiceCleanup(ctx context.Context, orderNo string, status string) error
	Archive(ctx context.Context, orderNo string, userID uint, archivedAt time.Time) (*domain.Order, error)
	ListOrders(ctx context.Context, filter OrderListFilter, offset int, afterID uint, limit int) ([]domain.Order, *uint, error)
	CountOrders(ctx context.Context, filter OrderListFilter) (int64, error)
	OrderFacets(ctx context.Context, filter OrderListFilter) (*OrderListFacets, error)
	ListEvents(ctx context.Context, orderNo string, userID uint, isAdmin bool, offset, limit int) ([]domain.OrderEvent, int64, error)
	CompleteCodeOrder(ctx context.Context, orderNo string, matchedAt time.Time, readUntil time.Time) (*domain.Order, bool, error)
	ActivatePurchaseOrder(ctx context.Context, orderNo string, matchedAt time.Time, afterSaleUntil time.Time) (*domain.Order, bool, error)
	ListExpiredCodeOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error)
	ListExpiredPurchaseActivationOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error)
	ListExpiredPurchaseWarrantyOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error)
	ListCheckoutAllocationRecoveries(ctx context.Context, staleBefore time.Time, limit int) ([]CheckoutAllocationRecovery, error)
	ListUnavailableMicrosoftOrderNos(ctx context.Context, resourceID uint, limit int) ([]string, error)
	ListUnavailableGmailOrderNos(ctx context.Context, resourceID uint, limit int) ([]string, error)
	ListCodeOrderNosReadyForCleanup(ctx context.Context, now time.Time, limit int) ([]string, error)
	ListPartialCleanupOrderNos(ctx context.Context, limit int) ([]string, error)
}

type CheckoutAllocationRecovery struct {
	OrderNo     string
	Status      domain.OrderStatus
	ProductType domain.ProductType
}

type HistoricalOrderRepository interface {
	CreateHistoricalOrder(ctx context.Context, cmd CreateHistoricalOrderCommand) error
}

type HistoricalGmailOrderRepository interface {
	CreateHistoricalGmailOrder(ctx context.Context, cmd CreateHistoricalGmailOrderCommand) error
}

type CreatePendingOrderCommand struct {
	OrderNo                 string
	UserID                  uint
	ProjectID               uint
	ProjectProductID        uint
	ProductType             domain.ProductType
	ServiceMode             domain.ServiceMode
	SupplyPolicy            domain.SupplyPolicy
	PayAmount               string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	ClientChannel           domain.ClientChannel
	APIKeyID                *uint
	IdempotencyKey          string
	RequestFingerprint      string
	Now                     time.Time
}

type MarkActiveCommand struct {
	OrderNo          string
	AllocationType   domain.AllocationType
	AllocationID     uint
	DeliveryEmail    string
	ReceiveStartedAt time.Time
	ReceiveUntil     time.Time
	ActivatedAt      *time.Time
	AfterSaleUntil   *time.Time
}

type MarkPaidCommand struct {
	OrderNo   string
	DebitTxID uint
	PayAmount string
}

type MarkFailedCommand struct {
	OrderNo      string
	RefundTxID   *uint
	RefundAmount string
	FailureCode  domain.OrderFailureCode
	Reason       string
	Now          time.Time
}

type CreateHistoricalOrderCommand struct {
	OrderNo                 string
	UserID                  uint
	ProjectID               uint
	ProjectProductID        uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	DebitTxID               uint
	MicrosoftAllocationID   uint
	DeliveryEmail           string
	CreatedAt               time.Time
	ExpiredAt               time.Time
	Now                     time.Time
}

type CreateHistoricalGmailOrderCommand struct {
	OrderNo                 string
	UserID                  uint
	ProjectID               uint
	ProjectProductID        uint
	ProductType             domain.ProductType
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	DebitTxID               uint
	GmailAllocationID       uint
	DeliveryEmail           string
	CreatedAt               time.Time
	ExpiredAt               time.Time
	Now                     time.Time
}

type HistoricalMicrosoftUsage struct {
	ResourceID              uint
	ProjectID               uint
	ProductID               uint
	Mailbox                 string
	Email                   string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	FirstMatchedAt          time.Time
	LastMatchedAt           time.Time
	EvidenceCount           int
}

type HistoricalGmailUsage struct {
	ResourceID              uint
	ProjectID               uint
	ProductID               uint
	ProductType             domain.ProductType
	Mailbox                 string
	Email                   string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	FirstMatchedAt          time.Time
	LastMatchedAt           time.Time
	EvidenceCount           int
}

type RefundOrderCommand struct {
	OrderNo      string
	RefundTxID   uint
	RefundAmount string
	Reason       string
	Operator     domain.OperatorType
}

type OrderListFilter struct {
	UserID      uint
	IsAdmin     bool
	Scope       string
	Status      domain.OrderStatus
	ServiceMode domain.ServiceMode
	ProductType domain.ProductType
	Search      string
	ProjectID   uint
	// Domain filters by the delivery email domain without the "@" prefix.
	Domain      string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

type OrderStatusFacets struct {
	All            int64
	PendingPayment int64
	Paid           int64
	Active         int64
	Completed      int64
	Refunded       int64
	Failed         int64
	Closed         int64
}

type OrderServiceModeFacets struct {
	All      int64
	Code     int64
	Purchase int64
}

type OrderProductTypeFacets struct {
	All          int64
	Microsoft    int64
	Domain       int64
	Random       int64
	Gmail        int64
	GmailVariant int64
	ICloud       int64
}

type OrderProjectFacet struct {
	ProjectID uint
	Name      string
	LogoURL   string
	Count     int64
}

type OrderKeyFacet struct {
	Key   string
	Count int64
}

// OrderListFacets aggregates list counts; each dimension is computed with the
// list filter minus that dimension itself, mirroring the resource facets.
type OrderListFacets struct {
	Status      OrderStatusFacets
	ServiceMode OrderServiceModeFacets
	ProductType OrderProductTypeFacets
	Projects    []OrderProjectFacet
	Domains     []OrderKeyFacet
}

type OrderListResult struct {
	Items       []CheckoutResult
	Total       int64
	NextAfterID *uint
	Facets      *OrderListFacets
}

type CheckoutRequest struct {
	UserID         uint
	ProjectID      uint
	ProductID      uint
	BatchQuantity  int
	ServiceMode    string
	SupplyPolicy   string
	EmailSuffix    string
	ClientChannel  domain.ClientChannel
	APIKeyID       *uint
	IdempotencyKey string
	RequestID      string
}

type CheckoutResult struct {
	Order                domain.Order
	AllocationID         uint
	ProjectName          string
	ProjectLogoURL       string
	ServiceToken         string
	Created              bool
	HasDelivery          bool
	VerificationCode     string
	LastMailReceivedAt   *time.Time
	GmailPassword        string
	GmailTwoFactorSecret string
	GmailAppPassword     string
	// Owner is populated only for the administrator site-wide order list.
	Owner *OrderOwnerSummary
}

type OrderPickupCredential struct {
	OrderNo       string
	DeliveryEmail string
	ServiceToken  string
}

type CheckoutBatchItem struct {
	Result    *CheckoutResult
	Err       error
	attempted bool
}

type MatchCodeResultRequest struct {
	OrderNo   string
	MatchedAt time.Time
}

type AdminOrderCommandRequest struct {
	OrderNo        string
	Reason         string
	IdempotencyKey string
	RequestID      string
	OperatorUserID uint
}

type ExpireOrdersResult struct {
	CheckoutRecovered           int
	CodeTimedOut                int
	ResourceUnavailableRefunded int
	PurchaseActivationCompleted int
	PurchaseWarrantyCompleted   int
	CodeCleaned                 int
	CleanupRetried              int
	DeliveryReconciled          int
	Failed                      int
}

type UseCase struct {
	repo                       Repository
	ordering                   OrderingPort
	wallet                     WalletPort
	allocation                 AllocationPort
	tokens                     OrderTokenPort
	deliveries                 OrderDeliveryPort
	gmailSupply                GmailSupplyPort
	systemLogs                 SystemLogPort
	projectDisplays            ProjectDisplayPort
	owners                     OwnerLookupPort
	historicalOrders           HistoricalOrderRepository
	historicalGmailOrders      HistoricalGmailOrderRepository
	historicalAllocations      HistoricalMicrosoftAllocationPort
	historicalGmailAllocations HistoricalGmailAllocationPort
	now                        func() time.Time
	deliveryNotificationCursor atomic.Uint64
	checkoutBatches            *checkoutBatchGate
}

func NewUseCase(repo Repository, ordering OrderingPort, wallet WalletPort, allocation AllocationPort, tokens OrderTokenPort) *UseCase {
	uc := &UseCase{
		repo:            repo,
		ordering:        ordering,
		wallet:          wallet,
		allocation:      allocation,
		tokens:          tokens,
		now:             func() time.Time { return time.Now().UTC() },
		checkoutBatches: newCheckoutBatchGate(),
	}
	uc.historicalOrders, _ = repo.(HistoricalOrderRepository)
	uc.historicalGmailOrders, _ = repo.(HistoricalGmailOrderRepository)
	uc.historicalAllocations, _ = allocation.(HistoricalMicrosoftAllocationPort)
	uc.historicalGmailAllocations, _ = allocation.(HistoricalGmailAllocationPort)
	return uc
}

func (uc *UseCase) SetOrderDeliveryPort(deliveries OrderDeliveryPort) {
	uc.deliveries = deliveries
}

func (uc *UseCase) SetGmailSupplyPort(supply GmailSupplyPort) {
	uc.gmailSupply = supply
}

func (uc *UseCase) SetProjectDisplayPort(projectDisplays ProjectDisplayPort) {
	uc.projectDisplays = projectDisplays
}

func (uc *UseCase) SetSystemLogPort(systemLogs SystemLogPort) {
	uc.systemLogs = systemLogs
}

func (uc *UseCase) SetOwnerLookupPort(owners OwnerLookupPort) {
	uc.owners = owners
}

func (uc *UseCase) ImportHistoricalMicrosoftUsage(ctx context.Context, matches []HistoricalMicrosoftUsage) error {
	if len(matches) == 0 {
		return nil
	}
	if uc == nil || uc.repo == nil || uc.wallet == nil || uc.historicalOrders == nil || uc.historicalAllocations == nil {
		return domain.ErrInvalidOrderRequest
	}
	ownerID := historicalMicrosoftOwnerUserID
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		now := uc.now()
		expiryCutoff := now.Add(-time.Second).Truncate(time.Second)
		for _, match := range matches {
			match.Mailbox = strings.ToLower(strings.TrimSpace(match.Mailbox))
			match.Email = strings.ToLower(strings.TrimSpace(match.Email))
			if match.ResourceID == 0 || match.ProjectID == 0 || match.ProductID == 0 || match.Email == "" ||
				match.EvidenceCount <= 0 || !validHistoricalMicrosoftMailbox(match.Mailbox) {
				return domain.ErrInvalidOrderRequest
			}
			createdAt := match.FirstMatchedAt.UTC()
			if createdAt.IsZero() || !createdAt.Before(now) {
				createdAt = expiryCutoff
			}
			expiredAt := match.LastMatchedAt.UTC()
			if expiredAt.IsZero() || expiredAt.After(expiryCutoff) {
				expiredAt = expiryCutoff
			}
			if createdAt.After(expiredAt) {
				createdAt = expiredAt
			}
			command := HistoricalMicrosoftAllocationCommand{
				AliasOwnerID: ownerID, ProjectID: match.ProjectID, ProductID: match.ProductID,
				ResourceID: match.ResourceID, Mailbox: match.Mailbox, Email: match.Email,
				CreatedAt: createdAt, ReleasedAt: expiredAt,
			}
			allocation, err := uc.historicalAllocations.ImportHistoricalMicrosoftAllocation(txCtx, command)
			if err != nil {
				return err
			}
			if allocation == nil {
				continue
			}
			if strings.TrimSpace(allocation.OrderNo) == "" || allocation.ID == 0 || allocation.Type != domain.AllocationTypeMicrosoft {
				return domain.ErrInvalidOrderRequest
			}
			orderNo := strings.TrimSpace(allocation.OrderNo)
			debit, err := uc.wallet.RecordHistoricalZeroDebit(txCtx, WalletCommand{
				UserID: ownerID, Amount: "0", Reason: "order:" + orderNo,
				IdempotencyKey: "history:" + orderNo + ":debit",
			})
			if err != nil {
				return err
			}
			if debit == nil || debit.ID == 0 {
				return domain.ErrInvalidOrderRequest
			}
			if err := uc.historicalOrders.CreateHistoricalOrder(txCtx, CreateHistoricalOrderCommand{
				OrderNo: orderNo, UserID: ownerID, ProjectID: match.ProjectID, ProjectProductID: match.ProductID,
				CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
				WarrantyMinutes: match.WarrantyMinutes, DebitTxID: debit.ID,
				MicrosoftAllocationID: allocation.ID, DeliveryEmail: match.Email,
				CreatedAt: createdAt, ExpiredAt: expiredAt, Now: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (uc *UseCase) ImportHistoricalGmailUsage(ctx context.Context, matches []HistoricalGmailUsage) error {
	if len(matches) == 0 {
		return nil
	}
	if uc == nil || uc.repo == nil || uc.wallet == nil || uc.historicalGmailOrders == nil || uc.historicalGmailAllocations == nil {
		return domain.ErrInvalidOrderRequest
	}
	ownerID := historicalGmailOwnerUserID
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		now := uc.now()
		expiryCutoff := now.Add(-time.Second).Truncate(time.Second)
		for _, match := range matches {
			match.Mailbox = strings.ToLower(strings.TrimSpace(match.Mailbox))
			match.Email = strings.ToLower(strings.TrimSpace(match.Email))
			if match.ResourceID == 0 || match.ProjectID == 0 || match.ProductID == 0 || match.Email == "" ||
				!domain.IsGmailProductType(match.ProductType) ||
				match.EvidenceCount <= 0 || !validHistoricalGmailMailbox(match.Mailbox) {
				return domain.ErrInvalidOrderRequest
			}
			createdAt := match.FirstMatchedAt.UTC()
			if createdAt.IsZero() || !createdAt.Before(now) {
				createdAt = expiryCutoff
			}
			expiredAt := match.LastMatchedAt.UTC()
			if expiredAt.IsZero() || expiredAt.After(expiryCutoff) {
				expiredAt = expiryCutoff
			}
			if createdAt.After(expiredAt) {
				createdAt = expiredAt
			}
			allocation, err := uc.historicalGmailAllocations.ImportHistoricalGmailAllocation(txCtx, HistoricalGmailAllocationCommand{
				ProjectID: match.ProjectID, ProductID: match.ProductID, ResourceID: match.ResourceID,
				Mailbox: match.Mailbox, Email: match.Email, CreatedAt: createdAt, ReleasedAt: expiredAt,
			})
			if err != nil {
				return err
			}
			if allocation == nil {
				continue
			}
			if strings.TrimSpace(allocation.OrderNo) == "" || allocation.ID == 0 || allocation.ProductID == 0 ||
				allocation.Type != domain.AllocationTypeGmail || allocation.SupplyScope != SupplyScopePublic ||
				!strings.EqualFold(allocation.Email, match.Email) {
				return domain.ErrInvalidOrderRequest
			}
			orderNo := strings.TrimSpace(allocation.OrderNo)
			existing, findErr := uc.repo.FindOrder(txCtx, orderNo)
			if findErr == nil {
				if allocation.Created || !sameHistoricalGmailOrder(existing, allocation, match) {
					return domain.ErrIdempotencyConflict
				}
				continue
			}
			if !errors.Is(findErr, domain.ErrOrderNotFound) {
				return findErr
			}
			// Only a newly created allocation may create an order. An older
			// allocation without its order is an inconsistent historical fact.
			if !allocation.Created || allocation.ProductID != match.ProductID {
				return domain.ErrIdempotencyConflict
			}
			debit, err := uc.wallet.RecordHistoricalZeroDebit(txCtx, WalletCommand{
				UserID: ownerID, Amount: "0", Reason: "order:" + orderNo,
				IdempotencyKey: "history:" + orderNo + ":debit",
			})
			if err != nil {
				return err
			}
			if debit == nil || debit.ID == 0 {
				return domain.ErrInvalidOrderRequest
			}
			if err := uc.historicalGmailOrders.CreateHistoricalGmailOrder(txCtx, CreateHistoricalGmailOrderCommand{
				OrderNo: orderNo, UserID: ownerID, ProjectID: match.ProjectID, ProjectProductID: match.ProductID,
				ProductType:       match.ProductType,
				CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
				WarrantyMinutes: match.WarrantyMinutes, DebitTxID: debit.ID, GmailAllocationID: allocation.ID,
				DeliveryEmail: match.Email, CreatedAt: createdAt, ExpiredAt: expiredAt, Now: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func validHistoricalMicrosoftMailbox(mailbox string) bool {
	switch mailbox {
	case "main", "alias", "dot", "plus":
		return true
	default:
		return false
	}
}

func validHistoricalGmailMailbox(mailbox string) bool {
	return mailbox == "main" || mailbox == "dot" || mailbox == "plus"
}

func sameHistoricalGmailOrder(
	order *domain.Order,
	allocation *AllocationResult,
	match HistoricalGmailUsage,
) bool {
	if order == nil || allocation == nil {
		return false
	}
	requestFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(allocation.OrderNo))))
	if order.OrderNo != strings.TrimSpace(allocation.OrderNo) ||
		order.UserID != historicalGmailOwnerUserID || order.ProjectID != match.ProjectID ||
		order.ProjectProductID != allocation.ProductID || order.ServiceMode != domain.ServiceModePurchase ||
		order.SupplyPolicy != domain.SupplyPolicyPublicOnly || order.Status != domain.OrderStatusCompleted ||
		order.FailureCode != "" || order.PayAmount != "0.00" || order.RefundAmount != "0.00" ||
		order.LegacyRandomMicrosoftPayAmount != "" || order.LegacyRandomDomainPayAmount != "" ||
		order.DebitTxID == nil || *order.DebitTxID == 0 || order.RefundTxID != nil ||
		order.AllocationType == nil || *order.AllocationType != domain.AllocationTypeGmail ||
		!strings.EqualFold(order.DeliveryEmail, match.Email) || order.ClientChannel != domain.ClientChannelConsole ||
		order.APIKeyID != nil || order.IdempotencyKey != "history:"+order.OrderNo ||
		order.RequestFingerprint != requestFingerprint || order.ServiceCleanupStatus != "succeeded" ||
		allocation.CreatedAt.IsZero() || allocation.ReleasedAt == nil || allocation.ReleasedAt.Before(allocation.CreatedAt) ||
		!sameHistoricalGmailTime(order.CreatedAt, allocation.CreatedAt) ||
		order.ReceiveStartedAt == nil || !sameHistoricalGmailTime(*order.ReceiveStartedAt, allocation.CreatedAt) ||
		order.ReceiveUntil == nil || !sameHistoricalGmailTime(*order.ReceiveUntil, *allocation.ReleasedAt) ||
		order.ActivatedAt == nil || !sameHistoricalGmailTime(*order.ActivatedAt, allocation.CreatedAt) ||
		order.AfterSaleUntil == nil || !sameHistoricalGmailTime(*order.AfterSaleUntil, *allocation.ReleasedAt) {
		return false
	}
	if allocation.ProductID == match.ProductID {
		return order.ProductType == match.ProductType
	}
	return match.ProductType == domain.ProductTypeGmailVariant && match.Mailbox == "plus" &&
		order.ProductType == domain.ProductTypeGmail
}

func sameHistoricalGmailTime(actual, expected time.Time) bool {
	return actual.UTC().Truncate(time.Second).Equal(expected.UTC().Truncate(time.Second))
}

func (uc *UseCase) Checkout(ctx context.Context, req CheckoutRequest) (result *CheckoutResult, runErr error) {
	startedAt := time.Now()
	defer func() {
		outcome := checkoutServiceOutcome(runErr)
		platform.ObserveServiceDuration("checkout", "001", outcome, startedAt)
		platform.AddWorkUnits("checkout", "001", "requested", 1)
		platform.AddWorkUnits("checkout", "001", outcome, 1)
	}()
	prepared, err := prepareCheckoutRequest(req)
	if err != nil {
		return nil, err
	}
	prepared.existing, err = uc.repo.FindOrderByIdempotency(
		ctx,
		prepared.request.ClientChannel,
		prepared.request.UserID,
		prepared.request.APIKeyID,
		prepared.idempotencyKey,
		prepared.fingerprint,
		checkoutPreparationFingerprint(prepared, ""),
	)
	if err != nil {
		return nil, err
	}
	if prepared.existing != nil {
		if err := finalizeCheckoutProduct(&prepared, prepared.existing.ProductType); err != nil {
			return nil, err
		}
	} else {
		if err := uc.prepareCheckoutQuote(ctx, &prepared, nil); err != nil {
			return nil, err
		}
	}
	preparedItems := []checkoutPreparation{prepared}
	uc.precheckCheckoutBalance(ctx, preparedItems)
	if err := uc.resolveRandomCheckoutSuffixes(ctx, preparedItems); err != nil {
		return nil, err
	}
	uc.precheckCheckoutInventory(ctx, preparedItems)
	result, runErr = uc.checkoutPrepared(ctx, preparedItems[0])
	if result != nil {
		if err := uc.attachAllocationIDs(ctx, result); err != nil {
			return nil, err
		}
	}
	if errors.Is(runErr, domain.ErrInsufficientInventory) && result != nil && result.Created {
		uc.markCheckoutInventoryUnavailable(ctx, preparedItems[0])
	}
	return result, runErr
}

func checkoutServiceOutcome(err error) string {
	switch {
	case err == nil:
		return "succeeded"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case shouldCommitCheckoutError(err),
		errors.Is(err, domain.ErrIdempotencyRequired),
		errors.Is(err, domain.ErrIdempotencyConflict),
		errors.Is(err, domain.ErrInvalidOrderRequest),
		errors.Is(err, domain.ErrProjectUnavailable),
		errors.Is(err, domain.ErrOrderStateConflict):
		return "business_failed"
	default:
		return "system_failed"
	}
}

type checkoutQuoteKey struct {
	projectID   uint
	productID   uint
	productType domain.ProductType
	mode        domain.ServiceMode
}

type checkoutPreparation struct {
	request              CheckoutRequest
	mode                 domain.ServiceMode
	policy               domain.SupplyPolicy
	idempotencyKey       string
	fingerprint          string
	selectorSuffix       string
	selectorProductType  domain.ProductType
	selectedBySuffix     bool
	emailSuffix          string
	requestID            string
	existing             *domain.Order
	quote                *OrderingQuote
	inventoryUnavailable bool
	prepareErr           error
}

func prepareCheckoutRequest(req CheckoutRequest) (checkoutPreparation, error) {
	mode, ok := domain.NormalizeServiceMode(req.ServiceMode)
	if !ok {
		return checkoutPreparation{}, domain.ErrInvalidOrderRequest
	}
	policy, ok := domain.NormalizeSupplyPolicy(req.SupplyPolicy)
	if !ok {
		return checkoutPreparation{}, domain.ErrInvalidOrderRequest
	}
	if req.UserID == 0 || req.ProjectID == 0 {
		return checkoutPreparation{}, domain.ErrInvalidOrderRequest
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return checkoutPreparation{}, domain.ErrIdempotencyRequired
	}
	if req.ClientChannel == "" {
		req.ClientChannel = domain.ClientChannelConsole
	}
	if req.ClientChannel == domain.ClientChannelAPIKey && (req.APIKeyID == nil || *req.APIKeyID == 0) {
		return checkoutPreparation{}, domain.ErrInvalidOrderRequest
	}
	if req.ClientChannel == domain.ClientChannelConsole {
		req.APIKeyID = nil
	}

	prepared := checkoutPreparation{
		request:        req,
		mode:           mode,
		policy:         policy,
		idempotencyKey: idempotencyKey,
		requestID:      strings.TrimSpace(req.RequestID),
	}
	if req.ProductID > 0 {
		prepared.emailSuffix = normalizeCheckoutEmailSelection(req.EmailSuffix)
	} else {
		selectorSuffix, err := normalizeCheckoutProductSuffix(req.EmailSuffix)
		if err != nil {
			return checkoutPreparation{}, err
		}
		prepared.selectorSuffix = selectorSuffix
		prepared.selectedBySuffix = true
	}
	prepared.fingerprint = checkoutPreparationFingerprint(prepared, prepared.emailSuffix)
	return prepared, nil
}

func finalizeCheckoutProduct(prepared *checkoutPreparation, productType domain.ProductType) error {
	if prepared.selectedBySuffix {
		if prepared.selectorProductType != "" && prepared.selectorProductType != productType {
			return domain.ErrInvalidOrderRequest
		}
		switch productType {
		case domain.ProductTypeMicrosoft:
			if prepared.selectorSuffix == coredomain.RandomDomainSuffixSelector {
				return domain.ErrInvalidOrderRequest
			}
			prepared.emailSuffix = prepared.selectorSuffix
		case domain.ProductTypeDomain:
			if prepared.selectorSuffix == coredomain.RandomDomainSuffixSelector {
				prepared.emailSuffix = prepared.selectorSuffix
				break
			}
			if prepared.selectorSuffix == coredomain.RandomMicrosoftSuffixSelector {
				return domain.ErrInvalidOrderRequest
			}
			normalized, privateDomain, err := normalizeCheckoutDomainSelection(prepared.selectorSuffix)
			if err != nil || privateDomain && prepared.policy == domain.SupplyPolicyPublicOnly {
				return domain.ErrInvalidOrderRequest
			}
			prepared.emailSuffix = normalized
		case domain.ProductTypeGmail:
			if prepared.selectorSuffix != "gmail.com" {
				return domain.ErrInvalidOrderRequest
			}
			prepared.emailSuffix = ""
		case domain.ProductTypeGmailVariant:
			if prepared.selectorSuffix != "gmail_variant" {
				return domain.ErrInvalidOrderRequest
			}
			prepared.emailSuffix = ""
		case domain.ProductTypeICloud:
			if prepared.selectorSuffix != "icloud.com" {
				return domain.ErrInvalidOrderRequest
			}
			prepared.emailSuffix = ""
		default:
			return domain.ErrInvalidOrderRequest
		}
		prepared.fingerprint = checkoutPreparationFingerprint(*prepared, prepared.emailSuffix)
		return nil
	}

	switch productType {
	case domain.ProductTypeMicrosoft:
		if prepared.emailSuffix == coredomain.RandomDomainSuffixSelector {
			return domain.ErrInvalidOrderRequest
		}
	case domain.ProductTypeDomain:
		if prepared.emailSuffix != "" {
			if prepared.emailSuffix == coredomain.RandomDomainSuffixSelector {
				break
			}
			if prepared.emailSuffix == coredomain.RandomMicrosoftSuffixSelector {
				return domain.ErrInvalidOrderRequest
			}
			if strings.Contains(prepared.emailSuffix, "@") {
				normalized, err := coredomain.NormalizeDomainMailbox(prepared.emailSuffix)
				if err != nil || prepared.policy == domain.SupplyPolicyPublicOnly {
					return domain.ErrInvalidOrderRequest
				}
				prepared.emailSuffix = normalized
			} else {
				normalized, privateDomain, err := normalizeCheckoutDomainSelection(prepared.emailSuffix)
				if err != nil || privateDomain && prepared.policy == domain.SupplyPolicyPublicOnly {
					return domain.ErrInvalidOrderRequest
				}
				prepared.emailSuffix = normalized
			}
		}
	case domain.ProductTypeLegacyRandom, domain.ProductTypeGmail, domain.ProductTypeGmailVariant, domain.ProductTypeICloud:
		prepared.emailSuffix = ""
	default:
		return domain.ErrInvalidOrderRequest
	}
	prepared.fingerprint = checkoutPreparationFingerprint(*prepared, prepared.emailSuffix)
	return nil
}

func checkoutPreparationFingerprint(prepared checkoutPreparation, emailSuffix string) string {
	if prepared.selectedBySuffix {
		parts := []any{
			prepared.request.UserID,
			prepared.request.ProjectID,
			"suffix",
			prepared.selectorSuffix,
			prepared.mode,
			prepared.policy,
			prepared.request.ClientChannel,
			apiKeyFingerprint(prepared.request.APIKeyID),
		}
		if prepared.request.BatchQuantity > 1 {
			parts = append(parts, prepared.request.BatchQuantity)
		}
		return checkoutFingerprint(parts...)
	}

	parts := []any{
		prepared.request.UserID,
		prepared.request.ProjectID,
		prepared.request.ProductID,
		prepared.mode,
		prepared.policy,
		emailSuffix,
		prepared.request.ClientChannel,
		apiKeyFingerprint(prepared.request.APIKeyID),
	}
	if prepared.request.BatchQuantity > 1 {
		parts = append(parts, prepared.request.BatchQuantity)
	}
	return checkoutFingerprint(parts...)
}

func (uc *UseCase) prepareCheckoutQuote(ctx context.Context, prepared *checkoutPreparation, quotes map[checkoutQuoteKey]*OrderingQuote) error {
	if prepared.selectedBySuffix && prepared.selectorProductType == "" {
		productType, err := checkoutProductTypeForSuffix(prepared.selectorSuffix)
		if err != nil {
			return err
		}
		prepared.selectorProductType = productType
	}
	key := checkoutQuoteKey{
		projectID:   prepared.request.ProjectID,
		productID:   prepared.request.ProductID,
		productType: prepared.selectorProductType,
		mode:        prepared.mode,
	}
	// This is the only path that evaluates current product sale status. Once an
	// order has been persisted, subsequent fulfilment must use that order's
	// immutable service-window snapshot instead.
	quote := quotes[key]
	if quote == nil {
		var err error
		if prepared.selectedBySuffix {
			ordering, ok := uc.ordering.(OrderingByTypePort)
			if !ok {
				return domain.ErrInvalidOrderRequest
			}
			quote, err = ordering.GetOrderingQuoteByType(
				ctx,
				prepared.request.ProjectID,
				prepared.selectorProductType,
				prepared.request.UserID,
				prepared.mode,
			)
		} else {
			quote, err = uc.ordering.GetOrderingQuote(
				ctx,
				prepared.request.ProjectID,
				prepared.request.ProductID,
				prepared.request.UserID,
				prepared.mode,
			)
		}
		if err != nil {
			return err
		}
		if quotes != nil {
			quotes[key] = quote
		}
	}
	if quote.ProductType == domain.ProductTypeLegacyRandom {
		return domain.ErrProjectUnavailable
	}
	if err := finalizeCheckoutProduct(prepared, quote.ProductType); err != nil {
		return err
	}
	prepared.quote = quote
	return nil
}

func (uc *UseCase) checkoutPrepared(ctx context.Context, prepared checkoutPreparation) (*CheckoutResult, error) {
	if prepared.prepareErr != nil {
		return nil, prepared.prepareErr
	}
	if prepared.existing != nil {
		result, err := uc.resumeExistingCheckout(
			ctx,
			prepared.existing.OrderNo,
			prepared.emailSuffix,
			prepared.requestID,
		)
		if err == nil {
			err = uc.attachGmailPurchase(ctx, result)
		}
		return result, err
	}
	if prepared.quote == nil {
		return nil, errors.New("checkout quote was not prepared")
	}
	order, created, err := uc.repo.LoadOrCreatePendingOrder(ctx, CreatePendingOrderCommand{
		OrderNo:                 nextOrderNo(),
		UserID:                  prepared.request.UserID,
		ProjectID:               prepared.quote.ProjectID,
		ProjectProductID:        prepared.quote.ProductID,
		ProductType:             prepared.quote.ProductType,
		ServiceMode:             prepared.mode,
		SupplyPolicy:            prepared.policy,
		PayAmount:               prepared.quote.PayAmount,
		CodeWindowMinutes:       prepared.quote.CodeWindowMinutes,
		ActivationWindowMinutes: prepared.quote.ActivationWindowMinutes,
		WarrantyMinutes:         prepared.quote.WarrantyMinutes,
		ClientChannel:           prepared.request.ClientChannel,
		APIKeyID:                prepared.request.APIKeyID,
		IdempotencyKey:          prepared.idempotencyKey,
		RequestFingerprint:      prepared.fingerprint,
		Now:                     uc.now(),
	})
	if err != nil {
		return nil, err
	}
	orderQuote := *prepared.quote
	if !created {
		storedQuote, quoteErr := orderingQuoteFromOrder(*order)
		if quoteErr != nil {
			return nil, quoteErr
		}
		orderQuote = *storedQuote
	}
	if created && prepared.inventoryUnavailable {
		// ponytail: persist skipped tail failures one-by-one to preserve the
		// existing idempotency contract; batch them only if 100 bounded writes matter.
		failed, failErr := uc.failPendingCheckout(ctx, MarkFailedCommand{
			OrderNo:     order.OrderNo,
			FailureCode: domain.OrderFailureInsufficientInventory,
			Reason:      "Allocation failed.",
			Now:         uc.now(),
		})
		if failErr != nil {
			return nil, failErr
		}
		if failed == nil {
			return nil, errors.New("mark failed returned no order")
		}
		return &CheckoutResult{Order: *failed, Created: true}, domain.ErrInsufficientInventory
	}
	result, err := uc.resumeCheckout(ctx, *order, orderQuote, prepared.emailSuffix, prepared.requestID)
	if result != nil {
		result.Created = created
	}
	if err == nil {
		err = uc.attachGmailPurchase(ctx, result)
	}
	return result, err
}

func (uc *UseCase) attachGmailPurchase(ctx context.Context, result *CheckoutResult) error {
	if result == nil || !domain.IsGmailProductType(result.Order.ProductType) || result.Order.ServiceMode != domain.ServiceModePurchase ||
		(result.Order.Status != domain.OrderStatusActive && result.Order.Status != domain.OrderStatusCompleted) {
		return nil
	}
	purchases, ok := uc.gmailSupply.(GmailPurchaseSupplyPort)
	if !ok || purchases == nil {
		return errors.New("gmail purchase service is unavailable")
	}
	delivery, err := purchases.FindLocalPurchase(ctx, result.Order.OrderNo)
	if err != nil {
		return err
	}
	result.AllocationID = delivery.AllocationID
	result.GmailPassword = delivery.Password
	result.GmailTwoFactorSecret = delivery.TwoFactorSecret
	result.GmailAppPassword = delivery.AppPassword
	return nil
}

func (uc *UseCase) CheckoutBatch(ctx context.Context, requests []CheckoutRequest) (items []CheckoutBatchItem, runErr error) {
	if len(requests) == 0 {
		return []CheckoutBatchItem{}, nil
	}
	userID := requests[0].UserID
	if userID == 0 {
		return nil, domain.ErrInvalidOrderRequest
	}
	for _, req := range requests[1:] {
		if req.UserID != userID {
			return nil, domain.ErrInvalidOrderRequest
		}
	}
	metricType, metricSize := checkoutBatchMetric(len(requests))
	requestStarted := time.Now()
	defer func() {
		succeeded, businessFailed, systemFailed, unprocessed := checkoutBatchCounts(len(requests), items, runErr)
		serviceResult := checkoutBatchServiceResult(businessFailed, systemFailed, unprocessed, runErr)
		platform.ObserveServiceDuration("checkout_batch", metricSize, serviceResult, requestStarted)
		platform.AddWorkUnits("checkout_batch", metricSize, "requested", len(requests))
		platform.AddWorkUnits("checkout_batch", metricSize, "succeeded", succeeded)
		platform.AddWorkUnits("checkout_batch", metricSize, "business_failed", businessFailed)
		platform.AddWorkUnits("checkout_batch", metricSize, "system_failed", systemFailed)
		platform.AddWorkUnits("checkout_batch", metricSize, "unprocessed", unprocessed)
	}()
	queuedAt := time.Now()
	release, runErr := uc.checkoutBatches.acquire(ctx, userID, len(requests))
	if runErr != nil {
		return nil, runErr
	}
	defer release()
	platform.ObserveQueueWait(metricType, queuedAt)
	queueWait := time.Since(queuedAt)
	serviceStarted := time.Now()
	defer platform.ObserveTaskService(metricType, serviceStarted)

	defer func() {
		succeeded, businessFailed, systemFailed, unprocessed := checkoutBatchCounts(len(requests), items, runErr)
		slog.Info(
			"checkout batch capacity sample",
			"quantity", len(requests),
			"size", metricSize,
			"slot_limit", checkoutBatchConcurrency,
			"queue_wait_ms", queueWait.Milliseconds(),
			"service_ms", time.Since(serviceStarted).Milliseconds(),
			"succeeded", succeeded,
			"business_failed", businessFailed,
			"system_failed", systemFailed,
			"unprocessed", unprocessed,
		)
	}()
	prepared, prepareErr := uc.prepareCheckoutBatch(ctx, requests)
	if prepareErr != nil {
		if errors.Is(prepareErr, context.Canceled) || errors.Is(prepareErr, context.DeadlineExceeded) ||
			errors.Is(prepareErr, domain.ErrIdempotencyConflict) || errors.Is(prepareErr, domain.ErrIdempotencyRequired) ||
			errors.Is(prepareErr, domain.ErrInvalidOrderRequest) {
			return nil, prepareErr
		}
		items = checkoutBatchFailedItems(len(requests), prepareErr)
		return items, nil
	}
	uc.precheckCheckoutBalance(ctx, prepared)
	if resolveErr := uc.resolveRandomCheckoutSuffixes(ctx, prepared); resolveErr != nil {
		if errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, context.DeadlineExceeded) ||
			errors.Is(resolveErr, domain.ErrIdempotencyConflict) || errors.Is(resolveErr, domain.ErrIdempotencyRequired) ||
			errors.Is(resolveErr, domain.ErrInvalidOrderRequest) {
			return nil, resolveErr
		}
		items = checkoutBatchFailedItems(len(requests), resolveErr)
		return items, nil
	}
	uc.precheckCheckoutInventory(ctx, prepared)
	items, runErr = uc.checkoutBatch(ctx, prepared)
	if runErr == nil {
		results := make([]*CheckoutResult, 0, len(items))
		for i := range items {
			if items[i].Result != nil {
				results = append(results, items[i].Result)
			}
		}
		if err := uc.attachAllocationIDs(ctx, results...); err != nil {
			return items, err
		}
	}
	return items, runErr
}

func (uc *UseCase) precheckCheckoutBalance(ctx context.Context, prepared []checkoutPreparation) {
	userID := uint(0)
	for index := range prepared {
		item := &prepared[index]
		if item.prepareErr == nil && item.existing == nil && item.quote != nil {
			userID = item.request.UserID
			break
		}
	}
	if userID == 0 {
		return
	}
	balanceValue, err := uc.wallet.ConsumerBalance(ctx, userID)
	if err != nil {
		slog.Debug("checkout balance precheck skipped", "user_id", userID, "error", err)
		return
	}
	balance, err := moneyfmt.Parse(balanceValue)
	if err != nil {
		slog.Debug("checkout balance precheck skipped", "user_id", userID, "error", err)
		return
	}
	remaining := balance
	for index := range prepared {
		item := &prepared[index]
		if item.prepareErr != nil || item.existing != nil || item.quote == nil {
			continue
		}
		amount, amountErr := moneyfmt.Parse(item.quote.PayAmount)
		if amountErr != nil {
			continue
		}
		if remaining.LessThan(amount) {
			item.prepareErr = domain.ErrInsufficientBalance
			continue
		}
		remaining = remaining.Sub(amount)
	}
}

func (uc *UseCase) precheckCheckoutInventory(ctx context.Context, prepared []checkoutPreparation) {
	checker, ok := uc.allocation.(InventoryAvailabilityPort)
	if !ok || checker == nil {
		return
	}
	type inventoryKey struct {
		projectID   uint
		productID   uint
		emailSuffix string
		publicOnly  bool
	}
	availability := make(map[inventoryKey]bool)
	checked := make(map[inventoryKey]bool)
	for index := range prepared {
		item := &prepared[index]
		if item.prepareErr != nil || item.existing != nil || item.quote == nil {
			continue
		}
		key := inventoryKey{
			projectID: item.quote.ProjectID, productID: item.quote.ProductID,
			emailSuffix: item.emailSuffix, publicOnly: item.policy == domain.SupplyPolicyPublicOnly,
		}
		available, exists := availability[key]
		if !checked[key] {
			var err error
			available, err = checker.HasAvailableInventory(ctx, InventoryAvailabilityCommand{
				ProjectID: key.projectID, ProductID: key.productID, EmailSuffix: key.emailSuffix,
				PublicOnly: key.publicOnly,
			})
			checked[key] = true
			if err != nil {
				slog.Debug("checkout inventory precheck skipped", "project_id", key.projectID, "product_id", key.productID, "error", err)
				continue
			}
			availability[key] = available
			exists = true
		}
		if exists && !available {
			item.prepareErr = domain.ErrInsufficientInventory
		}
	}
}

func (uc *UseCase) markCheckoutInventoryUnavailable(ctx context.Context, prepared checkoutPreparation) bool {
	marker, ok := uc.allocation.(InventoryUnavailablePort)
	if !ok || marker == nil || prepared.quote == nil {
		return false
	}
	marked, err := marker.MarkInventoryUnavailable(ctx, InventoryAvailabilityCommand{
		ProjectID: prepared.quote.ProjectID, ProductID: prepared.quote.ProductID,
		EmailSuffix: prepared.emailSuffix, PublicOnly: prepared.policy == domain.SupplyPolicyPublicOnly,
	})
	if err != nil {
		slog.Warn("mark checkout inventory unavailable failed", "project_id", prepared.quote.ProjectID, "product_id", prepared.quote.ProductID, "error", err)
		return false
	}
	return marked && prepared.policy == domain.SupplyPolicyPublicOnly
}

func (uc *UseCase) checkoutBatch(ctx context.Context, prepared []checkoutPreparation) ([]CheckoutBatchItem, error) {
	items := make([]CheckoutBatchItem, len(prepared))
	for index := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if prepared[index].prepareErr != nil {
			items[index] = CheckoutBatchItem{Err: prepared[index].prepareErr, attempted: true}
			continue
		}
		result, itemErr := uc.checkoutPrepared(ctx, prepared[index])
		items[index] = CheckoutBatchItem{Result: result, Err: itemErr, attempted: true}
		if !prepared[index].inventoryUnavailable && errors.Is(itemErr, domain.ErrInsufficientInventory) && result != nil && result.Created {
			marked := uc.markCheckoutInventoryUnavailable(ctx, prepared[index])
			if marked || errors.Is(itemErr, domain.ErrDefinitiveInventoryExhausted) {
				for tail := index + 1; tail < len(prepared); tail++ {
					if sameCheckoutInventoryKey(prepared[index], prepared[tail]) && prepared[tail].existing == nil {
						prepared[tail].inventoryUnavailable = true
					}
				}
			}
		}
		transactionResult := "committed"
		if itemErr != nil && !shouldCommitCheckoutError(itemErr) {
			transactionResult = "rolled_back"
		}
		platform.RecordServiceDBTransaction("checkout_batch", transactionResult)
		if errors.Is(itemErr, context.Canceled) || errors.Is(itemErr, context.DeadlineExceeded) {
			return nil, itemErr
		}
		if itemErr != nil && !shouldCommitCheckoutError(itemErr) && !errors.Is(itemErr, domain.ErrIdempotencyConflict) {
			// Earlier orders are already committed. Stop on infrastructure failure
			// and preserve the fixed-Q response by marking only the unattempted tail.
			items[index].Result = nil
			for tail := index + 1; tail < len(items); tail++ {
				items[tail].Err = itemErr
			}
			break
		}
	}
	return items, nil
}

func sameCheckoutInventoryKey(left, right checkoutPreparation) bool {
	if left.quote == nil || right.quote == nil {
		return false
	}
	return left.quote.ProjectID == right.quote.ProjectID &&
		left.quote.ProductID == right.quote.ProductID &&
		left.emailSuffix == right.emailSuffix &&
		left.policy == right.policy
}

func checkoutBatchFailedItems(quantity int, err error) []CheckoutBatchItem {
	items := make([]CheckoutBatchItem, quantity)
	for i := range items {
		items[i].Err = err
	}
	return items
}

func checkoutBatchCounts(requested int, items []CheckoutBatchItem, runErr error) (succeeded, businessFailed, systemFailed, unprocessed int) {
	for i := range items {
		if !items[i].attempted {
			continue
		}
		switch {
		case items[i].Err == nil:
			succeeded++
		case shouldCommitCheckoutError(items[i].Err), errors.Is(items[i].Err, domain.ErrIdempotencyConflict):
			businessFailed++
		default:
			systemFailed++
		}
	}
	accounted := succeeded + businessFailed + systemFailed
	if accounted < requested && errors.Is(runErr, domain.ErrIdempotencyConflict) {
		businessFailed++
		accounted++
	}
	unprocessed = max(requested-accounted, 0)
	return succeeded, businessFailed, systemFailed, unprocessed
}

func checkoutBatchServiceResult(businessFailed, systemFailed, unprocessed int, runErr error) string {
	switch {
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return "canceled"
	case systemFailed > 0, runErr != nil && !errors.Is(runErr, domain.ErrIdempotencyConflict):
		return "system_failed"
	case unprocessed > 0 && businessFailed == 0:
		return "system_failed"
	case businessFailed > 0, unprocessed > 0:
		return "partial"
	default:
		return "succeeded"
	}
}

type checkoutBatchIdempotencyReader interface {
	FindOrdersByIdempotencyBatch(
		ctx context.Context,
		channel domain.ClientChannel,
		userID uint,
		apiKeyID *uint,
		idempotencyKeys []string,
	) (map[string]domain.Order, error)
}

func (uc *UseCase) prepareCheckoutBatch(ctx context.Context, requests []CheckoutRequest) ([]checkoutPreparation, error) {
	prepared := make([]checkoutPreparation, len(requests))
	for i := range requests {
		item, err := prepareCheckoutRequest(requests[i])
		if err != nil {
			return nil, err
		}
		prepared[i] = item
	}
	if err := uc.preloadCheckoutBatch(ctx, prepared); err != nil {
		return nil, err
	}
	if len(prepared) > 0 && errors.Is(prepared[0].prepareErr, domain.ErrIdempotencyConflict) {
		return nil, prepared[0].prepareErr
	}
	quotes := make(map[checkoutQuoteKey]*OrderingQuote, 1)
	for i := range prepared {
		if prepared[i].prepareErr != nil || prepared[i].existing != nil {
			continue
		}
		if err := uc.prepareCheckoutQuote(ctx, &prepared[i], quotes); err != nil {
			if errors.Is(err, domain.ErrInsufficientInventory) {
				prepared[i].prepareErr = err
				continue
			}
			return nil, err
		}
	}
	return prepared, nil
}

func (uc *UseCase) resolveRandomCheckoutSuffixes(ctx context.Context, prepared []checkoutPreparation) error {
	resolver, ok := uc.allocation.(RandomSuffixSelectionPort)
	if !ok || resolver == nil {
		return nil
	}
	type selectionKey struct {
		projectID uint
		productID uint
		buyerID   uint
		selector  string
		policy    domain.SupplyPolicy
	}
	type selectionResult struct {
		suffix string
		err    error
	}
	selections := make(map[selectionKey]selectionResult)
	for i := range prepared {
		item := &prepared[i]
		if item.prepareErr != nil || item.existing != nil || item.quote == nil ||
			(item.emailSuffix != coredomain.RandomMicrosoftSuffixSelector && item.emailSuffix != coredomain.RandomDomainSuffixSelector) {
			continue
		}
		key := selectionKey{
			projectID: item.quote.ProjectID,
			productID: item.quote.ProductID,
			buyerID:   item.request.UserID,
			selector:  item.emailSuffix,
			policy:    item.policy,
		}
		selection, exists := selections[key]
		if !exists {
			selection.suffix, selection.err = resolver.SelectRandomSuffix(ctx, RandomSuffixSelectionCommand{
				ProjectID:    key.projectID,
				ProductID:    key.productID,
				BuyerUserID:  key.buyerID,
				SupplyScopes: checkoutSupplyScopes(key.policy),
				Selector:     key.selector,
			})
			selections[key] = selection
		}
		if selection.err != nil {
			if errors.Is(selection.err, domain.ErrInsufficientInventory) {
				item.prepareErr = selection.err
				continue
			}
			return selection.err
		}
		if selection.suffix == "" {
			return domain.ErrInvalidOrderRequest
		}
		item.emailSuffix = selection.suffix
	}
	return nil
}

func (uc *UseCase) preloadCheckoutBatch(ctx context.Context, prepared []checkoutPreparation) error {
	if len(prepared) == 0 {
		return nil
	}
	reader, canBatch := uc.repo.(checkoutBatchIdempotencyReader)
	channel := prepared[0].request.ClientChannel
	apiKeyID := prepared[0].request.APIKeyID
	for i := 1; i < len(prepared); i++ {
		if prepared[i].request.ClientChannel != channel ||
			apiKeyFingerprint(prepared[i].request.APIKeyID) != apiKeyFingerprint(apiKeyID) {
			canBatch = false
			break
		}
	}
	if canBatch {
		keys := make([]string, len(prepared))
		for i := range prepared {
			keys[i] = prepared[i].idempotencyKey
		}
		loaded, err := reader.FindOrdersByIdempotencyBatch(
			ctx,
			channel,
			prepared[0].request.UserID,
			apiKeyID,
			keys,
		)
		if err != nil {
			return err
		}
		for i := range prepared {
			order, exists := loaded[prepared[i].idempotencyKey]
			if !exists {
				continue
			}
			if err := finalizeCheckoutProduct(&prepared[i], order.ProductType); err != nil {
				prepared[i].prepareErr = err
				continue
			}
			if order.RequestFingerprint != prepared[i].fingerprint {
				prepared[i].prepareErr = domain.ErrIdempotencyConflict
				continue
			}
			orderCopy := order
			prepared[i].existing = &orderCopy
		}
		return nil
	}

	for i := range prepared {
		existing, err := uc.repo.FindOrderByIdempotency(
			ctx,
			prepared[i].request.ClientChannel,
			prepared[i].request.UserID,
			prepared[i].request.APIKeyID,
			prepared[i].idempotencyKey,
			prepared[i].fingerprint,
			checkoutPreparationFingerprint(prepared[i], ""),
		)
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			prepared[i].prepareErr = err
			continue
		}
		if err != nil {
			return err
		}
		if existing != nil {
			if err := finalizeCheckoutProduct(&prepared[i], existing.ProductType); err != nil {
				prepared[i].prepareErr = err
				continue
			}
		}
		prepared[i].existing = existing
	}
	return nil
}

func checkoutBatchMetric(quantity int) (taskType, size string) {
	switch {
	case quantity <= 20:
		size = "001_020"
	case quantity <= 40:
		size = "021_040"
	case quantity <= 60:
		size = "041_060"
	case quantity <= 80:
		size = "061_080"
	default:
		size = "081_100"
	}
	return "checkout_batch_" + size, size
}

// resumeExistingCheckout retries a persisted order without consulting current
// project-product sale state. This keeps idempotent checkout retries usable
// after a product is delisted while preserving the original order terms.
func (uc *UseCase) resumeExistingCheckout(ctx context.Context, orderNo, emailSuffix, requestID string) (*CheckoutResult, error) {
	order, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	quote, err := orderingQuoteFromOrder(*order)
	if err != nil {
		return nil, err
	}
	result, err := uc.resumeCheckout(ctx, *order, *quote, emailSuffix, requestID)
	if result != nil {
		result.Created = false
	}
	return result, err
}

func (uc *UseCase) GetOrder(ctx context.Context, orderNo string, userID uint, isAdmin bool) (*CheckoutResult, error) {
	order, err := uc.repo.FindOrder(ctx, strings.TrimSpace(orderNo))
	if err != nil {
		return nil, err
	}
	if !isAdmin && order.UserID != userID {
		return nil, domain.ErrOrderForbidden
	}
	result := &CheckoutResult{Order: *order}
	if orderAllowsServiceToken(order.Status) {
		token, err := uc.tokens.FindOrderTokenByOrder(ctx, order.OrderNo)
		if err != nil {
			return nil, err
		}
		if token == nil && domain.IsGmailProductType(order.ProductType) {
			token, err = uc.tokens.IssueOrderToken(ctx, order.OrderNo, tokenExpireAtFromOrder(*order))
			if err != nil {
				return nil, err
			}
			if token == nil {
				return nil, errors.New("issue order token returned no token")
			}
		}
		if token != nil {
			result.ServiceToken = token.TokenPlain
		}
	}
	if err := uc.attachOrderDelivery(ctx, result); err != nil {
		return nil, err
	}
	if domain.IsGmailProductType(result.Order.ProductType) {
		if err := uc.attachGmailPurchase(ctx, result); err != nil {
			return nil, err
		}
	}
	if err := uc.attachAllocationIDs(ctx, result); err != nil {
		return nil, err
	}
	displayed := []CheckoutResult{*result}
	if err := uc.attachProjectDisplays(ctx, displayed, nil); err != nil {
		return nil, err
	}
	result.ProjectName = displayed[0].ProjectName
	result.ProjectLogoURL = displayed[0].ProjectLogoURL
	return result, nil
}

func (uc *UseCase) GetOrderPickupCredentials(ctx context.Context, orderNos []string, userID uint) ([]OrderPickupCredential, error) {
	orders, err := uc.repo.FindOrdersByOrderNos(ctx, orderNos)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range orderNos {
		order, exists := orders[orderNo]
		if !exists {
			return nil, domain.ErrOrderNotFound
		}
		if order.UserID != userID {
			return nil, domain.ErrOrderForbidden
		}
	}
	tokens, err := uc.tokens.FindOrderTokensByOrders(ctx, orderNos)
	if err != nil {
		return nil, err
	}
	now := uc.now()
	result := make([]OrderPickupCredential, 0, len(orderNos))
	for _, orderNo := range orderNos {
		order := orders[orderNo]
		token, exists := tokens[orderNo]
		if !orderAllowsServiceToken(order.Status) || !exists || token.TokenPlain == "" ||
			(token.ExpireAt != nil && !token.ExpireAt.After(now)) || strings.TrimSpace(order.DeliveryEmail) == "" {
			continue
		}
		result = append(result, OrderPickupCredential{
			OrderNo: orderNo, DeliveryEmail: order.DeliveryEmail, ServiceToken: token.TokenPlain,
		})
	}
	return result, nil
}

func (uc *UseCase) ListOrders(ctx context.Context, filter OrderListFilter, offset int, afterID uint, limit int) (*OrderListResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	items, nextAfterID, err := uc.repo.ListOrders(ctx, filter, offset, afterID, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.repo.CountOrders(ctx, filter)
	if err != nil {
		return nil, err
	}
	facets, err := uc.repo.OrderFacets(ctx, filter)
	if err != nil {
		return nil, err
	}
	results := make([]CheckoutResult, len(items))
	orderIDs := make([]uint, len(items))
	for i := range items {
		results[i].Order = items[i]
		orderIDs[i] = items[i].ID
	}
	list := &OrderListResult{
		Items:       results,
		Total:       total,
		NextAfterID: nextAfterID,
		Facets:      facets,
	}
	if uc.deliveries != nil && len(orderIDs) > 0 {
		deliveries, err := uc.deliveries.ListOrderDeliveries(ctx, orderIDs)
		if err != nil {
			return nil, err
		}
		for i := range results {
			attachOrderDeliverySummary(&results[i], deliveries[results[i].Order.ID])
		}
	}
	allocationResults := make([]*CheckoutResult, len(results))
	for i := range results {
		allocationResults[i] = &results[i]
	}
	if err := uc.attachAllocationIDs(ctx, allocationResults...); err != nil {
		return nil, err
	}
	if err := uc.attachProjectDisplays(ctx, results, facets.Projects); err != nil {
		return nil, err
	}
	if err := uc.attachOwners(ctx, filter, results); err != nil {
		return nil, err
	}
	return list, nil
}

// attachOwners enriches each row with its buyer summary. It only runs for the
// administrator site-wide scope; the buyer's own order list never needs it.
func (uc *UseCase) attachOwners(ctx context.Context, filter OrderListFilter, results []CheckoutResult) error {
	if uc.owners == nil || !filter.IsAdmin || filter.Scope != "all" || len(results) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(results))
	userIDs := make([]uint, 0, len(results))
	for i := range results {
		id := results[i].Order.UserID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		userIDs = append(userIDs, id)
	}
	if len(userIDs) == 0 {
		return nil
	}
	owners, err := uc.owners.GetByIDs(ctx, userIDs)
	if err != nil {
		return err
	}
	for i := range results {
		if owner, ok := owners[results[i].Order.UserID]; ok {
			ownerCopy := owner
			results[i].Owner = &ownerCopy
		}
	}
	return nil
}

func (uc *UseCase) attachProjectDisplays(ctx context.Context, results []CheckoutResult, facets []OrderProjectFacet) error {
	if uc.projectDisplays == nil || len(results)+len(facets) == 0 {
		return nil
	}
	idSet := make(map[uint]struct{}, len(results)+len(facets))
	ids := make([]uint, 0, len(results)+len(facets))
	addID := func(id uint) {
		if id == 0 {
			return
		}
		if _, ok := idSet[id]; ok {
			return
		}
		idSet[id] = struct{}{}
		ids = append(ids, id)
	}
	for i := range results {
		addID(results[i].Order.ProjectID)
	}
	for i := range facets {
		addID(facets[i].ProjectID)
	}
	if len(ids) == 0 {
		return nil
	}
	displays, err := uc.projectDisplays.ProjectDisplays(ctx, ids)
	if err != nil {
		return err
	}
	for i := range results {
		display := displays[results[i].Order.ProjectID]
		results[i].ProjectName = display.Name
		results[i].ProjectLogoURL = display.LogoURL
	}
	for i := range facets {
		display := displays[facets[i].ProjectID]
		facets[i].Name = display.Name
		facets[i].LogoURL = display.LogoURL
	}
	return nil
}

func (uc *UseCase) attachOrderDelivery(ctx context.Context, result *CheckoutResult) error {
	if uc.deliveries == nil || result == nil || result.Order.ID == 0 {
		return nil
	}
	delivery, err := uc.deliveries.FindOrderDelivery(ctx, result.Order.ID)
	if err != nil {
		return err
	}
	if delivery != nil {
		attachOrderDeliverySummary(result, *delivery)
	}
	return nil
}

func attachOrderDeliverySummary(result *CheckoutResult, delivery OrderDeliverySummary) {
	if result == nil || delivery.ReceivedAt.IsZero() {
		return
	}
	receivedAt := delivery.ReceivedAt.UTC()
	result.HasDelivery = true
	result.VerificationCode = delivery.VerificationCode
	result.LastMailReceivedAt = &receivedAt
}

func (uc *UseCase) attachAllocationIDs(ctx context.Context, results ...*CheckoutResult) error {
	reader, ok := uc.allocation.(interface {
		FindAllocationsByOrders(context.Context, []string) (map[string]AllocationResult, error)
	})
	if !ok || len(results) == 0 {
		return nil
	}
	orderNos := make([]string, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result == nil || result.AllocationID > 0 || result.Order.AllocationType == nil {
			continue
		}
		orderNo := strings.TrimSpace(result.Order.OrderNo)
		if orderNo == "" {
			continue
		}
		if _, exists := seen[orderNo]; exists {
			continue
		}
		seen[orderNo] = struct{}{}
		orderNos = append(orderNos, orderNo)
	}
	if len(orderNos) == 0 {
		return nil
	}
	allocations, err := reader.FindAllocationsByOrders(ctx, orderNos)
	if err != nil {
		return err
	}
	for _, result := range results {
		if result == nil || result.AllocationID > 0 || result.Order.AllocationType == nil {
			continue
		}
		orderNo := strings.TrimSpace(result.Order.OrderNo)
		allocation, exists := allocations[orderNo]
		if !exists {
			if *result.Order.AllocationType == domain.AllocationTypeGmail &&
				(result.Order.Status == domain.OrderStatusCompleted || result.Order.Status == domain.OrderStatusFailed ||
					result.Order.Status == domain.OrderStatusRefunded || result.Order.Status == domain.OrderStatusClosed) {
				continue
			}
			return fmt.Errorf("%w: allocation missing for order %s", domain.ErrOrderStateConflict, orderNo)
		}
		if allocation.ID == 0 || allocation.Type != *result.Order.AllocationType {
			return fmt.Errorf("%w: allocation mismatch for order %s", domain.ErrOrderStateConflict, orderNo)
		}
		result.AllocationID = allocation.ID
	}
	return nil
}

func (uc *UseCase) ListEvents(ctx context.Context, orderNo string, userID uint, isAdmin bool, offset, limit int) ([]domain.OrderEvent, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return uc.repo.ListEvents(ctx, strings.TrimSpace(orderNo), userID, isAdmin, offset, limit)
}

func (uc *UseCase) Archive(ctx context.Context, orderNo string, userID uint) (*domain.Order, error) {
	return uc.repo.Archive(ctx, strings.TrimSpace(orderNo), userID, uc.now())
}

func (uc *UseCase) AdminRefundOrder(ctx context.Context, req AdminOrderCommandRequest) (*domain.Order, error) {
	orderNo := strings.TrimSpace(req.OrderNo)
	reason := strings.TrimSpace(req.Reason)
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if orderNo == "" || reason == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo:        orderNo,
		Reason:         reason,
		IdempotencyKey: idempotencyKey,
		RequestID:      strings.TrimSpace(req.RequestID),
		Operator:       domain.OperatorTypeAdmin,
		AllowedStatuses: []domain.OrderStatus{
			domain.OrderStatusActive,
			domain.OrderStatusCompleted,
		},
	})
	if err != nil || order == nil || !changed {
		return order, err
	}
	if cleanupErr := uc.cleanupOrderService(ctx, *order, true, "Order refunded.", req.RequestID); cleanupErr != nil {
		return order, cleanupErr
	}
	return order, nil
}

func (uc *UseCase) AdminTerminateOrder(ctx context.Context, req AdminOrderCommandRequest) (*domain.Order, error) {
	orderNo := strings.TrimSpace(req.OrderNo)
	reason := strings.TrimSpace(req.Reason)
	if orderNo == "" || reason == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	order, changed, err := uc.repo.CloseActiveOrder(ctx, orderNo, reason)
	if err != nil || order == nil || !changed {
		return order, err
	}
	if cleanupErr := uc.cleanupOrderService(ctx, *order, true, "Order terminated.", req.RequestID); cleanupErr != nil {
		return order, cleanupErr
	}
	return order, nil
}

func (uc *UseCase) AdminRetryOrderCleanup(ctx context.Context, orderNo string, requestID string) (*domain.Order, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	order, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	releaseAllocation := cleanupRetryShouldReleaseAllocation(*order)
	if !releaseAllocation && order.ServiceMode == domain.ServiceModePurchase && order.Status == domain.OrderStatusCompleted {
		return nil, domain.ErrOrderStateConflict
	}
	if err := uc.cleanupOrderService(ctx, *order, releaseAllocation, "Order cleanup retried.", requestID); err != nil {
		return order, err
	}
	return order, nil
}

func (uc *UseCase) AdminRetryOrderRefund(ctx context.Context, req AdminOrderCommandRequest) (*domain.Order, error) {
	orderNo := strings.TrimSpace(req.OrderNo)
	reason := strings.TrimSpace(req.Reason)
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if orderNo == "" || reason == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	var refunded *domain.Order
	changed := false
	owner, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, owner.UserID); err != nil {
			return err
		}
		locked, err := uc.repo.LockOrderForUpdate(txCtx, orderNo)
		if err != nil {
			return err
		}
		if locked.UserID != owner.UserID {
			return domain.ErrOrderStateConflict
		}
		refunded = locked
		if locked.Status != domain.OrderStatusFailed || locked.DebitTxID == nil || locked.RefundTxID != nil {
			return domain.ErrOrderStateConflict
		}
		refund, err := uc.wallet.RefundConsumer(txCtx, WalletCommand{
			UserID:         locked.UserID,
			Amount:         locked.PayAmount,
			Reason:         "order:" + locked.OrderNo,
			IdempotencyKey: idempotencyKey,
			RequestID:      strings.TrimSpace(req.RequestID),
		})
		if err != nil {
			return err
		}
		updated, didChange, err := uc.repo.AttachFailedOrderRefund(txCtx, RefundOrderCommand{
			OrderNo:      locked.OrderNo,
			RefundTxID:   refund.ID,
			RefundAmount: locked.PayAmount,
			Reason:       reason,
			Operator:     domain.OperatorTypeAdmin,
		})
		if err != nil {
			return err
		}
		refunded = updated
		changed = didChange
		return nil
	})
	if err != nil || refunded == nil || !changed {
		return refunded, err
	}
	if cleanupErr := uc.cleanupOrderService(ctx, *refunded, true, "Order refund retried.", req.RequestID); cleanupErr != nil {
		return refunded, cleanupErr
	}
	return refunded, nil
}

func (uc *UseCase) ExpireDueOrders(ctx context.Context, limit int) (*ExpireOrdersResult, error) {
	if limit <= 0 {
		limit = 200
	}
	now := uc.now()
	result := &ExpireOrdersResult{}
	if uc.allocation != nil {
		recoveries, err := uc.repo.ListCheckoutAllocationRecoveries(ctx, now.Add(-staleCheckoutRecoveryAfter), limit)
		if err != nil {
			return nil, err
		}
		for _, recovery := range recoveries {
			if uc.allocation == nil {
				result.Failed++
				continue
			}
			if recovery.Status == domain.OrderStatusPaid {
				if _, err := uc.resumeExistingCheckout(ctx, recovery.OrderNo, "", ""); err != nil {
					result.Failed++
					continue
				}
				result.CheckoutRecovered++
				continue
			}
			if _, err := uc.failPendingCheckout(ctx, MarkFailedCommand{
				OrderNo: recovery.OrderNo, FailureCode: domain.OrderFailureAllocation,
				Reason: "Checkout payment recovery timed out.", Now: now,
			}); err != nil {
				result.Failed++
				continue
			}
			result.CheckoutRecovered++
		}
	}
	if uc.deliveries != nil {
		pendingNotifications, err := uc.deliveries.ListPendingNotifications(ctx, uint(uc.deliveryNotificationCursor.Load()), limit)
		if err != nil {
			return nil, err
		}
		for _, notification := range pendingNotifications {
			uc.deliveryNotificationCursor.Store(uint64(notification.OrderID))
			if err := uc.NotifyMatchedCode(ctx, MatchCodeResultRequest{
				OrderNo:   notification.OrderNo,
				MatchedAt: notification.ReceivedAt,
			}); err != nil {
				result.Failed++
				continue
			}
			result.DeliveryReconciled++
		}
	}
	unavailable, err := uc.repo.ListUnavailableMicrosoftOrderNos(ctx, 0, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range unavailable {
		refunded, err := uc.refundUnavailableMicrosoftOrder(ctx, orderNo, "")
		if err != nil {
			result.Failed++
			continue
		}
		if refunded {
			result.ResourceUnavailableRefunded++
		}
	}
	unavailableGmail, err := uc.repo.ListUnavailableGmailOrderNos(ctx, 0, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range unavailableGmail {
		refunded, err := uc.refundUnavailableGmailOrder(ctx, orderNo, "")
		if err != nil {
			result.Failed++
			continue
		}
		if refunded {
			result.ResourceUnavailableRefunded++
		}
	}
	codeExpired, err := uc.repo.ListExpiredCodeOrderNos(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range codeExpired {
		if err := uc.expireCodeOrder(ctx, orderNo, now); err != nil {
			result.Failed++
			continue
		}
		result.CodeTimedOut++
	}
	purchaseActivationExpired, err := uc.repo.ListExpiredPurchaseActivationOrderNos(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range purchaseActivationExpired {
		if err := uc.completeExpiredOrder(ctx, orderNo, "Purchase activation window expired."); err != nil {
			result.Failed++
			continue
		}
		result.PurchaseActivationCompleted++
	}
	purchaseWarrantyExpired, err := uc.repo.ListExpiredPurchaseWarrantyOrderNos(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range purchaseWarrantyExpired {
		if err := uc.completeExpiredOrder(ctx, orderNo, "Purchase warranty window expired."); err != nil {
			result.Failed++
			continue
		}
		result.PurchaseWarrantyCompleted++
	}
	codeCleanup, err := uc.repo.ListCodeOrderNosReadyForCleanup(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range codeCleanup {
		if err := uc.cleanupExpiredCodeOrder(ctx, orderNo); err != nil {
			result.Failed++
			continue
		}
		result.CodeCleaned++
	}
	partialCleanup, err := uc.repo.ListPartialCleanupOrderNos(ctx, limit)
	if err != nil {
		return nil, err
	}
	for _, orderNo := range partialCleanup {
		order, findErr := uc.repo.FindOrder(ctx, orderNo)
		if findErr != nil {
			result.Failed++
			continue
		}
		if cleanupErr := uc.cleanupOrderService(ctx, *order, cleanupRetryShouldReleaseAllocation(*order), "Order cleanup automatically retried.", ""); cleanupErr != nil {
			result.Failed++
			continue
		}
		result.CleanupRetried++
	}
	return result, nil
}

func (uc *UseCase) RefundUnavailableMicrosoftOrders(ctx context.Context, resourceID uint, requestID string) (int, error) {
	if uc == nil || uc.repo == nil || uc.wallet == nil || resourceID == 0 {
		return 0, domain.ErrInvalidOrderRequest
	}
	orderNos, err := uc.repo.ListUnavailableMicrosoftOrderNos(ctx, resourceID, 200)
	if err != nil {
		return 0, err
	}
	refunded := 0
	var resultErr error
	for _, orderNo := range orderNos {
		changed, err := uc.refundUnavailableMicrosoftOrder(ctx, orderNo, requestID)
		if changed {
			refunded++
		}
		resultErr = errors.Join(resultErr, err)
	}
	return refunded, resultErr
}

func (uc *UseCase) RefundUnavailableGmailOrders(ctx context.Context, resourceID uint, requestID string) (int, error) {
	if uc == nil || uc.repo == nil || uc.wallet == nil || resourceID == 0 {
		return 0, domain.ErrInvalidOrderRequest
	}
	orderNos, err := uc.repo.ListUnavailableGmailOrderNos(ctx, resourceID, 200)
	if err != nil {
		return 0, err
	}
	refunded := 0
	var resultErr error
	for _, orderNo := range orderNos {
		changed, refundErr := uc.refundUnavailableGmailOrder(ctx, orderNo, requestID)
		if changed {
			refunded++
		}
		resultErr = errors.Join(resultErr, refundErr)
	}
	return refunded, resultErr
}

func (uc *UseCase) refundUnavailableGmailOrder(ctx context.Context, orderNo, requestID string) (bool, error) {
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo:           orderNo,
		Reason:            "Gmail resource is permanently unavailable.",
		IdempotencyKey:    "order:" + strings.TrimSpace(orderNo) + ":refund",
		RequestID:         strings.TrimSpace(requestID),
		Operator:          domain.OperatorTypeSystem,
		AllowedStatuses:   []domain.OrderStatus{domain.OrderStatusActive},
		ReconcileDelivery: true,
	})
	if errors.Is(err, domain.ErrOrderStateConflict) {
		return false, nil
	}
	if err != nil || order == nil || !changed {
		return false, err
	}
	return true, uc.cleanupOrderService(ctx, *order, true, "Order refunded because its Gmail resource is permanently unavailable.", requestID)
}

func (uc *UseCase) NotifyMatchedCode(ctx context.Context, req MatchCodeResultRequest) error {
	orderNo := strings.TrimSpace(req.OrderNo)
	if orderNo == "" {
		return domain.ErrInvalidOrderRequest
	}
	matchedAt := req.MatchedAt.UTC()
	if matchedAt.IsZero() {
		matchedAt = uc.now()
	}
	order, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return err
	}
	if order.ServiceMode == domain.ServiceModePurchase {
		if order.ActivatedAt != nil {
			return nil
		}
		if order.ReceiveUntil != nil && matchedAt.After(order.ReceiveUntil.UTC()) {
			return nil
		}
		quote, err := orderingQuoteFromOrder(*order)
		if err != nil {
			return err
		}
		afterSaleUntil := purchaseWarrantyUntil(*order, quote.WarrantyMinutes, matchedAt)
		return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
			_, _, err := uc.repo.ActivatePurchaseOrder(txCtx, orderNo, matchedAt, afterSaleUntil)
			return err
		})
	}
	readUntil := matchedAt.Add(time.Hour)
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		_, changed, err := uc.repo.CompleteCodeOrder(txCtx, orderNo, matchedAt, readUntil)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return uc.tokens.ExtendOrderToken(txCtx, orderNo, readUntil)
	})
}

type refundOrderRequest struct {
	OrderNo           string
	Reason            string
	IdempotencyKey    string
	RequestID         string
	Operator          domain.OperatorType
	AllowedStatuses   []domain.OrderStatus
	ReconcileDelivery bool
}

func (uc *UseCase) refundUnavailableMicrosoftOrder(ctx context.Context, orderNo string, requestID string) (bool, error) {
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo:           orderNo,
		Reason:            "Microsoft resource is permanently unavailable.",
		IdempotencyKey:    "order:" + strings.TrimSpace(orderNo) + ":refund",
		RequestID:         strings.TrimSpace(requestID),
		Operator:          domain.OperatorTypeSystem,
		AllowedStatuses:   []domain.OrderStatus{domain.OrderStatusActive},
		ReconcileDelivery: true,
	})
	if errors.Is(err, domain.ErrOrderStateConflict) {
		return false, nil
	}
	if err != nil || order == nil || !changed {
		return false, err
	}
	return true, uc.cleanupOrderService(ctx, *order, true, "Order refunded because its Microsoft resource is permanently unavailable.", requestID)
}

func (uc *UseCase) expireCodeOrder(ctx context.Context, orderNo string, _ time.Time) error {
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo:        orderNo,
		Reason:         "Code receive window expired.",
		IdempotencyKey: "order:" + strings.TrimSpace(orderNo) + ":refund",
		Operator:       domain.OperatorTypeSystem,
		AllowedStatuses: []domain.OrderStatus{
			domain.OrderStatusActive,
		},
		ReconcileDelivery: true,
	})
	if err != nil || order == nil || !changed {
		return err
	}
	return uc.cleanupOrderService(ctx, *order, true, "Code order expired.", "")
}

func (uc *UseCase) completeExpiredOrder(ctx context.Context, orderNo string, reason string) error {
	_, _, err := uc.repo.CompleteExpiredOrder(ctx, strings.TrimSpace(orderNo), reason)
	return err
}

func (uc *UseCase) cleanupExpiredCodeOrder(ctx context.Context, orderNo string) error {
	order, err := uc.repo.FindOrder(ctx, strings.TrimSpace(orderNo))
	if err != nil {
		return err
	}
	return uc.cleanupOrderService(ctx, *order, true, "Code read window expired.", "")
}

func (uc *UseCase) refundOrder(ctx context.Context, req refundOrderRequest) (*domain.Order, bool, error) {
	orderNo := strings.TrimSpace(req.OrderNo)
	reason := strings.TrimSpace(req.Reason)
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if orderNo == "" || reason == "" || idempotencyKey == "" {
		return nil, false, domain.ErrInvalidOrderRequest
	}
	if req.Operator == "" {
		req.Operator = domain.OperatorTypeSystem
	}
	var refunded *domain.Order
	changed := false
	owner, err := uc.repo.FindOrder(ctx, orderNo)
	if err != nil {
		return nil, false, err
	}
	err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, owner.UserID); err != nil {
			return err
		}
		locked, err := uc.repo.LockOrderForUpdate(txCtx, orderNo)
		if err != nil {
			return err
		}
		if locked.UserID != owner.UserID {
			return domain.ErrOrderStateConflict
		}
		refunded = locked
		if locked.Status == domain.OrderStatusRefunded {
			return nil
		}
		if !statusAllowed(locked.Status, req.AllowedStatuses) {
			return domain.ErrOrderStateConflict
		}
		if req.ReconcileDelivery && locked.ServiceMode == domain.ServiceModeCode && uc.deliveries != nil {
			delivery, err := uc.deliveries.FindOrderDelivery(txCtx, locked.ID)
			if err != nil {
				return err
			}
			if delivery != nil {
				matchedAt := delivery.ReceivedAt
				if matchedAt.IsZero() {
					matchedAt = uc.now()
				}
				return uc.NotifyMatchedCode(txCtx, MatchCodeResultRequest{OrderNo: locked.OrderNo, MatchedAt: matchedAt})
			}
		}
		refund, err := uc.wallet.RefundConsumer(txCtx, WalletCommand{
			UserID:         locked.UserID,
			Amount:         locked.PayAmount,
			Reason:         "order:" + locked.OrderNo,
			IdempotencyKey: idempotencyKey,
			RequestID:      strings.TrimSpace(req.RequestID),
		})
		if err != nil {
			return err
		}
		updated, didChange, err := uc.repo.RefundOrder(txCtx, RefundOrderCommand{
			OrderNo:      locked.OrderNo,
			RefundTxID:   refund.ID,
			RefundAmount: locked.PayAmount,
			Reason:       reason,
			Operator:     req.Operator,
		})
		if err != nil {
			return err
		}
		refunded = updated
		changed = didChange
		return nil
	})
	return refunded, changed, err
}

func (uc *UseCase) cleanupOrderService(ctx context.Context, order domain.Order, releaseAllocation bool, reason string, requestID string) error {
	failures := make([]string, 0, 3)
	if releaseAllocation && uc.allocation != nil {
		if err := uc.allocation.ReleaseByOrder(ctx, order.OrderNo); err != nil {
			failures = append(failures, "release allocation: "+err.Error())
		}
	}
	if uc.tokens != nil {
		if err := uc.tokens.DisableOrderToken(ctx, order.OrderNo, reason); err != nil {
			failures = append(failures, "disable order token: "+err.Error())
		}
	}
	status := "succeeded"
	if len(failures) > 0 {
		status = "partial_failure"
	}
	if err := uc.repo.MarkServiceCleanup(ctx, order.OrderNo, status); err != nil {
		return err
	}
	if len(failures) > 0 {
		uc.writeSystemLog(ctx, "warning", "trade.order_cleanup_partial_failure", requestID, order.OrderNo, strings.Join(failures, "; "))
		return fmt.Errorf("%w: %s", domain.ErrOrderCompensationError, strings.Join(failures, "; "))
	}
	return nil
}

func (uc *UseCase) writeSystemLog(ctx context.Context, level string, eventType string, requestID string, orderNo string, detail string) {
	if uc.systemLogs == nil {
		return
	}
	_ = uc.systemLogs.Create(ctx, &governancedomain.SystemLog{
		Level:     level,
		Module:    "trade",
		EventType: eventType,
		RequestID: strings.TrimSpace(requestID),
		BizType:   "order",
		BizID:     strings.TrimSpace(orderNo),
		Message:   "Order lifecycle cleanup requires attention.",
		Detail:    strings.TrimSpace(detail),
	})
}

func statusAllowed(status domain.OrderStatus, allowed []domain.OrderStatus) bool {
	for _, item := range allowed {
		if status == item {
			return true
		}
	}
	return false
}

func cleanupRetryShouldReleaseAllocation(order domain.Order) bool {
	if order.Status == domain.OrderStatusRefunded || order.Status == domain.OrderStatusClosed {
		return true
	}
	return order.ServiceMode == domain.ServiceModeCode &&
		(order.Status == domain.OrderStatusCompleted || order.Status == domain.OrderStatusRefunded)
}

func (uc *UseCase) resumeCheckout(ctx context.Context, order domain.Order, quote OrderingQuote, emailSuffix string, requestID string) (*CheckoutResult, error) {
	var currentAllocation *AllocationResult
	for {
		switch order.Status {
		case domain.OrderStatusPendingPayment:
			allocation, err := uc.allocate(ctx, order, emailSuffix)
			if err != nil {
				if errors.Is(err, domain.ErrInsufficientInventory) {
					failed, markErr := uc.failPendingCheckout(ctx, MarkFailedCommand{
						OrderNo:     order.OrderNo,
						FailureCode: domain.OrderFailureInsufficientInventory,
						Reason:      "Allocation failed.",
						Now:         uc.now(),
					})
					if markErr != nil {
						return nil, markErr
					}
					if failed == nil {
						return nil, errors.New("mark failed returned no order")
					}
					if failed.Status != domain.OrderStatusFailed {
						order = *failed
						continue
					}
					return &CheckoutResult{Order: *failed}, checkoutInventoryError(*failed, err)
				}
				return nil, err
			}
			if allocation == nil {
				return nil, errors.New("allocation returned no result")
			}
			currentAllocation = allocation
			payAmount, err := allocatedCheckoutPayAmount(order, quote, *allocation)
			if err != nil {
				failed, failErr := uc.failPendingCheckout(ctx, MarkFailedCommand{
					OrderNo: order.OrderNo, FailureCode: domain.OrderFailureAllocation,
					Reason: "Allocated checkout price is invalid.", Now: uc.now(),
				})
				if failErr != nil {
					return nil, failErr
				}
				if failed == nil {
					return nil, errors.New("mark failed returned no order")
				}
				if failed.Status != domain.OrderStatusFailed {
					order = *failed
					continue
				}
				return &CheckoutResult{Order: *failed}, err
			}
			updated, err := uc.payPendingCheckout(ctx, order.OrderNo, order.UserID, payAmount, requestID)
			if err != nil {
				if errors.Is(err, domain.ErrInsufficientBalance) {
					failed, markErr := uc.failPendingCheckout(ctx, MarkFailedCommand{
						OrderNo:     order.OrderNo,
						FailureCode: domain.OrderFailureInsufficientBalance,
						Reason:      "Payment failed.",
						Now:         uc.now(),
					})
					if markErr != nil {
						return nil, markErr
					}
					if failed == nil {
						return nil, errors.New("mark failed returned no order")
					}
					if failed.Status != domain.OrderStatusFailed {
						order = *failed
						continue
					}
					return &CheckoutResult{Order: *failed}, checkoutErrorForFailedOrder(*failed)
				}
				return nil, err
			}
			if updated == nil {
				return nil, errors.New("pay pending checkout returned no order")
			}
			order = *updated

		case domain.OrderStatusPaid:
			allocation := currentAllocation
			if allocation == nil {
				var err error
				allocation, err = uc.allocate(ctx, order, emailSuffix)
				if err != nil {
					if !errors.Is(err, domain.ErrInsufficientInventory) {
						return nil, err
					}
					failed, refundErr := uc.compensatePaidCheckout(ctx, order, domain.OrderFailureInsufficientInventory, "Allocation failed.")
					if refundErr != nil {
						return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
					}
					if failed == nil {
						return nil, errors.New("refund failed order returned no order")
					}
					if failed.Status != domain.OrderStatusFailed {
						order = *failed
						continue
					}
					return &CheckoutResult{Order: *failed}, checkoutInventoryError(*failed, err)
				}
			}
			receiveStartedAt := uc.now()
			receiveUntil := serviceReceiveUntil(receiveStartedAt, quote, order.ServiceMode)
			afterSaleUntil := initialAfterSaleUntil(receiveUntil, order.ServiceMode)
			token, err := uc.tokens.IssueOrderToken(ctx, order.OrderNo, tokenExpireAt(order.ServiceMode, receiveUntil))
			if err == nil && token == nil {
				err = errors.New("issue order token returned no token")
			}
			if err != nil {
				failed, refundErr := uc.compensatePaidCheckout(ctx, order, domain.OrderFailureServiceToken, "Service token failed.")
				if refundErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
				}
				if failed == nil {
					return nil, errors.New("refund failed order returned no order")
				}
				if failed.Status != domain.OrderStatusFailed {
					order = *failed
					continue
				}
				return nil, err
			}
			activated, err := uc.repo.MarkActive(ctx, MarkActiveCommand{
				OrderNo:          order.OrderNo,
				AllocationType:   allocation.Type,
				AllocationID:     allocation.ID,
				DeliveryEmail:    allocation.Email,
				ReceiveStartedAt: receiveStartedAt,
				ReceiveUntil:     receiveUntil,
				AfterSaleUntil:   afterSaleUntil,
			})
			if err != nil {
				if err == domain.ErrOrderStateConflict {
					reloaded, reloadErr := uc.repo.FindOrder(ctx, order.OrderNo)
					if reloadErr != nil {
						return nil, reloadErr
					}
					order = *reloaded
					continue
				}
				failed, refundErr := uc.compensatePaidCheckout(ctx, order, domain.OrderFailureActivation, "Order activation failed.")
				if refundErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
				}
				if failed == nil {
					return nil, errors.New("refund failed order returned no order")
				}
				if failed.Status != domain.OrderStatusFailed {
					order = *failed
					continue
				}
				return nil, err
			}
			if activated == nil {
				return nil, errors.New("mark active returned no order")
			}
			return &CheckoutResult{Order: *activated, ServiceToken: token.TokenPlain}, nil

		case domain.OrderStatusActive, domain.OrderStatusCompleted:
			token, err := uc.tokens.FindOrderTokenByOrder(ctx, order.OrderNo)
			if err != nil {
				return nil, err
			}
			if token == nil {
				token, err = uc.tokens.IssueOrderToken(ctx, order.OrderNo, tokenExpireAtFromOrder(order))
				if err != nil {
					return nil, err
				}
			}
			if token == nil {
				return nil, errors.New("issue order token returned no token")
			}
			return &CheckoutResult{Order: order, ServiceToken: token.TokenPlain}, nil

		case domain.OrderStatusFailed:
			if currentAllocation != nil {
				if err := uc.allocation.ReleaseByOrder(ctx, order.OrderNo); err != nil {
					return nil, err
				}
			}
			return &CheckoutResult{Order: order}, checkoutErrorForFailedOrder(order)

		default:
			return &CheckoutResult{Order: order}, nil
		}
	}
}

func (uc *UseCase) payPendingCheckout(ctx context.Context, orderNo string, userID uint, payAmount, requestID string) (*domain.Order, error) {
	var order *domain.Order
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, userID); err != nil {
			return err
		}
		locked, err := uc.repo.LockOrderForUpdate(txCtx, orderNo)
		if err != nil {
			return err
		}
		if locked == nil {
			return errors.New("lock pending order returned no order")
		}
		order = locked
		if locked.UserID != userID {
			return domain.ErrOrderStateConflict
		}
		if locked.Status != domain.OrderStatusPendingPayment {
			return nil
		}
		debit, err := uc.wallet.DebitConsumer(txCtx, WalletCommand{
			UserID:         userID,
			Amount:         payAmount,
			Reason:         "order:" + orderNo,
			IdempotencyKey: "order:" + orderNo + ":debit",
			RequestID:      requestID,
		})
		if err != nil {
			return err
		}
		if debit == nil || debit.ID == 0 {
			return domain.ErrInvalidOrderRequest
		}
		paid, err := uc.repo.MarkPaid(txCtx, MarkPaidCommand{
			OrderNo:   orderNo,
			DebitTxID: debit.ID,
			PayAmount: payAmount,
		})
		if err != nil {
			return err
		}
		if paid == nil {
			return errors.New("mark paid returned no order")
		}
		order = paid
		return nil
	})
	return order, err
}

func (uc *UseCase) failPendingCheckout(ctx context.Context, cmd MarkFailedCommand) (*domain.Order, error) {
	var order *domain.Order
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		locked, err := uc.repo.LockOrderForUpdate(txCtx, cmd.OrderNo)
		if err != nil {
			return err
		}
		if locked == nil {
			return errors.New("lock pending order returned no order")
		}
		order = locked
		if locked.Status != domain.OrderStatusPendingPayment && locked.Status != domain.OrderStatusFailed {
			return nil
		}
		if err := uc.allocation.ReleaseByOrder(txCtx, cmd.OrderNo); err != nil {
			return err
		}
		if locked.Status == domain.OrderStatusFailed {
			return nil
		}
		failed, err := uc.repo.MarkFailed(txCtx, cmd)
		if err != nil {
			return err
		}
		if failed == nil {
			return errors.New("mark failed returned no order")
		}
		order = failed
		return nil
	})
	return order, err
}

func (uc *UseCase) compensatePaidCheckout(ctx context.Context, order domain.Order, failureCode domain.OrderFailureCode, reason string) (*domain.Order, error) {
	var result *domain.Order
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, order.UserID); err != nil {
			return err
		}
		locked, err := uc.repo.LockOrderForUpdate(txCtx, order.OrderNo)
		if err != nil {
			return err
		}
		if locked == nil {
			return errors.New("lock paid order returned no order")
		}
		result = locked
		if locked.UserID != order.UserID {
			return domain.ErrOrderStateConflict
		}
		if locked.Status == domain.OrderStatusFailed {
			if err := uc.tokens.DisableOrderToken(txCtx, locked.OrderNo, reason); err != nil {
				return err
			}
			return uc.allocation.ReleaseByOrder(txCtx, locked.OrderNo)
		}
		if locked.Status == domain.OrderStatusRefunded {
			return uc.allocation.ReleaseByOrder(txCtx, locked.OrderNo)
		}
		if locked.Status != domain.OrderStatusPaid {
			return nil
		}
		if err := uc.tokens.DisableOrderToken(txCtx, locked.OrderNo, reason); err != nil {
			return err
		}
		if err := uc.allocation.ReleaseByOrder(txCtx, locked.OrderNo); err != nil {
			return err
		}
		result, err = uc.refundPaidOrder(txCtx, *locked, failureCode, reason)
		return err
	})
	return result, err
}

func (uc *UseCase) allocate(ctx context.Context, order domain.Order, emailSuffix string) (*AllocationResult, error) {
	scopes := checkoutSupplyScopes(order.SupplyPolicy)
	result, err := uc.allocation.Allocate(ctx, AllocationCommand{
		OrderNo:              order.OrderNo,
		BuyerUserID:          order.UserID,
		ProjectProductID:     order.ProjectProductID,
		ServiceMode:          order.ServiceMode,
		SupplyScopes:         scopes,
		EmailSuffix:          emailSuffix,
		RequiredUntil:        allocationRequiredUntil(order),
		FulfillExistingOrder: true,
	})
	if order.ProductType == domain.ProductTypeLegacyRandom && errors.Is(err, domain.ErrProjectUnavailable) {
		return nil, domain.ErrInsufficientInventory
	}
	return result, err
}

func checkoutSupplyScopes(policy domain.SupplyPolicy) []SupplyScope {
	if policy == domain.SupplyPolicyPrivateFirst {
		return []SupplyScope{SupplyScopeOwned, SupplyScopePublic}
	}
	return []SupplyScope{SupplyScopePublic}
}

func allocationRequiredUntil(order domain.Order) time.Time {
	now := time.Now().UTC()
	var until time.Time
	if order.ServiceMode == domain.ServiceModeCode {
		until = now.Add(time.Duration(order.CodeWindowMinutes) * time.Minute)
	} else {
		until = now.Add(time.Duration(order.ActivationWindowMinutes+order.WarrantyMinutes) * time.Minute)
	}
	return until
}

func checkoutPayAmount(listedAmount string, scope SupplyScope) string {
	if scope == SupplyScopeOwned {
		return "0.00"
	}
	return listedAmount
}

func allocatedCheckoutPayAmount(order domain.Order, quote OrderingQuote, allocation AllocationResult) (string, error) {
	listedAmount := order.PayAmount
	if order.ProductType == domain.ProductTypeLegacyRandom {
		switch allocation.Type {
		case domain.AllocationTypeMicrosoft:
			listedAmount = quote.LegacyMicrosoftPayAmount
		case domain.AllocationTypeDomain:
			listedAmount = quote.LegacyDomainPayAmount
		case domain.AllocationTypeICloud:
			listedAmount = quote.PayAmount
		default:
			return "", domain.ErrInvalidOrderRequest
		}
		if listedAmount == "" {
			return "", domain.ErrInvalidOrderRequest
		}
	}
	return checkoutPayAmount(listedAmount, allocation.SupplyScope), nil
}

func (uc *UseCase) refundPaidOrder(ctx context.Context, order domain.Order, failureCode domain.OrderFailureCode, reason string) (*domain.Order, error) {
	refund, err := uc.wallet.RefundConsumer(ctx, WalletCommand{
		UserID:         order.UserID,
		Amount:         order.PayAmount,
		Reason:         "order:" + order.OrderNo,
		IdempotencyKey: "order:" + order.OrderNo + ":refund",
	})
	if err != nil {
		return nil, err
	}
	if refund == nil || refund.ID == 0 {
		return nil, errors.New("refund consumer returned no transaction")
	}
	failed, err := uc.repo.MarkFailed(ctx, MarkFailedCommand{
		OrderNo:      order.OrderNo,
		RefundTxID:   &refund.ID,
		RefundAmount: order.PayAmount,
		FailureCode:  failureCode,
		Reason:       reason,
		Now:          uc.now(),
	})
	if err != nil {
		return nil, err
	}
	if failed == nil {
		return nil, errors.New("mark refunded order failed returned no order")
	}
	return failed, nil
}

func serviceReceiveUntil(now time.Time, quote OrderingQuote, mode domain.ServiceMode) time.Time {
	switch mode {
	case domain.ServiceModePurchase:
		return now.Add(time.Duration(quote.ActivationWindowMinutes) * time.Minute)
	default:
		return now.Add(time.Duration(quote.CodeWindowMinutes) * time.Minute)
	}
}

func initialAfterSaleUntil(receiveUntil time.Time, mode domain.ServiceMode) *time.Time {
	if mode == domain.ServiceModePurchase {
		return nil
	}
	return &receiveUntil
}

func purchaseWarrantyUntil(order domain.Order, warrantyMinutes int, matchedAt time.Time) time.Time {
	start := matchedAt.UTC()
	if order.ReceiveStartedAt != nil && !order.ReceiveStartedAt.IsZero() {
		start = order.ReceiveStartedAt.UTC()
	}
	until := start.Add(time.Duration(warrantyMinutes) * time.Minute)
	if until.Before(matchedAt.UTC()) {
		return matchedAt.UTC()
	}
	return until
}

func orderingQuoteFromOrder(order domain.Order) (*OrderingQuote, error) {
	quote := &OrderingQuote{
		ProjectID:               order.ProjectID,
		ProductID:               order.ProjectProductID,
		ProductType:             order.ProductType,
		PayAmount:               order.PayAmount,
		CodeWindowMinutes:       order.CodeWindowMinutes,
		ActivationWindowMinutes: order.ActivationWindowMinutes,
		WarrantyMinutes:         order.WarrantyMinutes,
	}
	if order.ProductType == domain.ProductTypeLegacyRandom {
		if order.LegacyRandomMicrosoftPayAmount == "" || order.LegacyRandomDomainPayAmount == "" {
			return nil, domain.ErrInvalidOrderRequest
		}
		quote.LegacyMicrosoftPayAmount = order.LegacyRandomMicrosoftPayAmount
		quote.LegacyDomainPayAmount = order.LegacyRandomDomainPayAmount
	}
	if quote.ProjectID == 0 || quote.ProductID == 0 || quote.ProductType == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	switch order.ServiceMode {
	case domain.ServiceModeCode:
		if quote.CodeWindowMinutes <= 0 {
			return nil, domain.ErrInvalidOrderRequest
		}
	case domain.ServiceModePurchase:
		if quote.ActivationWindowMinutes <= 0 || quote.WarrantyMinutes <= 0 {
			return nil, domain.ErrInvalidOrderRequest
		}
	default:
		return nil, domain.ErrInvalidOrderRequest
	}
	return quote, nil
}

func tokenExpireAt(mode domain.ServiceMode, receiveUntil time.Time) *time.Time {
	if mode == domain.ServiceModePurchase {
		return nil
	}
	return &receiveUntil
}

func tokenExpireAtFromOrder(order domain.Order) *time.Time {
	if order.ServiceMode == domain.ServiceModePurchase {
		return nil
	}
	if order.ReceiveUntil != nil {
		return order.ReceiveUntil
	}
	return order.AfterSaleUntil
}

func checkoutFingerprint(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprint(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func shouldCommitCheckoutError(err error) bool {
	return errors.Is(err, domain.ErrInsufficientBalance) || errors.Is(err, domain.ErrInsufficientInventory)
}

func checkoutErrorForFailedOrder(order domain.Order) error {
	switch order.FailureCode {
	case domain.OrderFailureInsufficientBalance:
		return domain.ErrInsufficientBalance
	case domain.OrderFailureInsufficientInventory:
		return domain.ErrInsufficientInventory
	default:
		return domain.ErrInvalidOrderRequest
	}
}

func checkoutInventoryError(order domain.Order, allocationErr error) error {
	if errors.Is(allocationErr, domain.ErrDefinitiveInventoryExhausted) {
		return domain.ErrDefinitiveInventoryExhausted
	}
	return checkoutErrorForFailedOrder(order)
}

func apiKeyFingerprint(apiKeyID *uint) uint {
	if apiKeyID == nil {
		return 0
	}
	return *apiKeyID
}

func normalizeEmailSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "@")
	return strings.TrimPrefix(value, ".")
}

func normalizeCheckoutEmailSelection(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "@") {
		if normalized, err := coredomain.NormalizeDomainMailbox(value); err == nil {
			return normalized
		}
		return value
	}
	if strings.Count(value, "@") > 1 {
		return value
	}
	return normalizeEmailSuffix(value)
}

func normalizeCheckoutProductSuffix(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "@") || strings.HasPrefix(value, ".") {
		value = value[1:]
	}
	if value == "" || strings.Contains(value, "@") || strings.HasPrefix(value, ".") {
		return "", domain.ErrInvalidOrderRequest
	}
	if value == "gmail_variant" {
		return value, nil
	}
	normalized, err := coredomain.NormalizeDomainSuffix(value)
	if err != nil {
		return "", domain.ErrInvalidOrderRequest
	}
	return strings.TrimPrefix(normalized, "."), nil
}

func normalizeCheckoutDomainSelection(value string) (string, bool, error) {
	if normalized, err := coredomain.NormalizeDomainTLD(value); err == nil {
		return normalized, false, nil
	}
	normalized, err := coredomain.NormalizeDomainName(value)
	if err != nil {
		return "", false, domain.ErrInvalidOrderRequest
	}
	return normalized, true, nil
}

func checkoutProductTypeForSuffix(suffix string) (domain.ProductType, error) {
	switch suffix {
	case "gmail.com":
		return domain.ProductTypeGmail, nil
	case "gmail_variant":
		return domain.ProductTypeGmailVariant, nil
	case "icloud.com":
		return domain.ProductTypeICloud, nil
	case coredomain.RandomMicrosoftSuffixSelector:
		return domain.ProductTypeMicrosoft, nil
	case coredomain.RandomDomainSuffixSelector:
		return domain.ProductTypeDomain, nil
	}
	if coredomain.IsMicrosoftEmailDomain("selector@" + suffix) {
		return domain.ProductTypeMicrosoft, nil
	}
	if normalized, _, err := normalizeCheckoutDomainSelection(suffix); err == nil && normalized == suffix {
		return domain.ProductTypeDomain, nil
	}
	return "", domain.ErrInvalidOrderRequest
}

func orderAllowsServiceToken(status domain.OrderStatus) bool {
	return status == domain.OrderStatusActive || status == domain.OrderStatusCompleted
}

func nextOrderNo() string {
	return "OR" + platform.NewUUIDV7CompactUpper()
}
