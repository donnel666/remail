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

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/trade/domain"
)

type OrderingQuote struct {
	ProjectID               uint
	ProductID               uint
	ProductType             domain.ProductType
	PayAmount               string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
}

type OrderingPort interface {
	GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode domain.ServiceMode) (*OrderingQuote, error)
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
	LockConsumer(ctx context.Context, userID uint) error
	DebitConsumer(ctx context.Context, cmd WalletCommand) (*WalletTransaction, error)
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
	SupplyScope      SupplyScope
	SupplyScopes     []SupplyScope
	EmailSuffix      string
	// FulfillExistingOrder permits allocation after the product has been
	// delisted. Trade sets it only after an order has been durably created;
	// standalone/new allocation callers keep the current-sale guard.
	FulfillExistingOrder bool
}

type AllocationResult struct {
	OrderNo     string
	Type        domain.AllocationType
	ID          uint
	Email       string
	SupplyScope SupplyScope
}

type AllocationPort interface {
	Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error)
	ReleaseByOrder(ctx context.Context, orderNo string) error
}

type InventoryAvailabilityCommand struct {
	ProjectID   uint
	ProductID   uint
	BuyerUserID uint
	EmailSuffix string
	PublicOnly  bool
}

type InventoryAvailabilityPort interface {
	HasAvailableInventory(ctx context.Context, cmd InventoryAvailabilityCommand) (bool, error)
}

type InventoryUnavailablePort interface {
	MarkInventoryUnavailable(ctx context.Context, cmd InventoryAvailabilityCommand) (bool, error)
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

var ErrHistoricalAllocationOwnerRequired = errors.New("historical allocation owner is required")

type OrderToken struct {
	TokenPlain string
	ExpireAt   *time.Time
}

type OrderTokenPort interface {
	IssueOrderToken(ctx context.Context, orderNo string, expireAt *time.Time) (*OrderToken, error)
	FindOrderTokenByOrder(ctx context.Context, orderNo string) (*OrderToken, error)
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
	FindOrderByIdempotency(ctx context.Context, channel domain.ClientChannel, userID uint, apiKeyID *uint, idempotencyKey, requestFingerprint string) (*domain.Order, error)
	FindOrder(ctx context.Context, orderNo string) (*domain.Order, error)
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
	ListCodeOrderNosReadyForCleanup(ctx context.Context, now time.Time, limit int) ([]string, error)
	ListPartialCleanupOrderNos(ctx context.Context, limit int) ([]string, error)
}

type HistoricalOrderRepository interface {
	FindHistoricalOrderOwner(ctx context.Context) (uint, error)
	CreateHistoricalOrder(ctx context.Context, cmd CreateHistoricalOrderCommand) error
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
	Search      string
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

type OrderKeyFacet struct {
	Key   string
	Count int64
}

// OrderListFacets aggregates list counts; each dimension is computed with the
// list filter minus that dimension itself, mirroring the resource facets.
type OrderListFacets struct {
	Status      OrderStatusFacets
	ServiceMode OrderServiceModeFacets
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
	Order              domain.Order
	ProjectName        string
	ProjectLogoURL     string
	ServiceToken       string
	Created            bool
	HasDelivery        bool
	VerificationCode   string
	LastMailReceivedAt *time.Time
	// Owner is populated only for the administrator site-wide order list.
	Owner *OrderOwnerSummary
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
	CodeTimedOut                int
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
	systemLogs                 SystemLogPort
	projectDisplays            ProjectDisplayPort
	owners                     OwnerLookupPort
	historicalOrders           HistoricalOrderRepository
	historicalAllocations      HistoricalMicrosoftAllocationPort
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
	uc.historicalAllocations, _ = allocation.(HistoricalMicrosoftAllocationPort)
	return uc
}

func (uc *UseCase) SetOrderDeliveryPort(deliveries OrderDeliveryPort) {
	uc.deliveries = deliveries
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
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		ownerID, err := uc.historicalOrders.FindHistoricalOrderOwner(txCtx)
		if err != nil {
			return err
		}
		if ownerID != 0 {
			// Checkout takes the wallet before Allocation's resource root. Historical
			// imports use the same order so the two flows cannot wait on each other.
			if err := uc.wallet.LockConsumer(txCtx, ownerID); err != nil {
				return err
			}
		}
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
			if ownerID == 0 {
				return ErrHistoricalAllocationOwnerRequired
			}
			if strings.TrimSpace(allocation.OrderNo) == "" || allocation.ID == 0 || allocation.Type != domain.AllocationTypeMicrosoft {
				return domain.ErrInvalidOrderRequest
			}
			orderNo := strings.TrimSpace(allocation.OrderNo)
			existing, err := uc.repo.FindOrder(txCtx, orderNo)
			if err == nil {
				if !sameHistoricalMicrosoftOrder(*existing, ownerID, allocation.ID, match) {
					return domain.ErrIdempotencyConflict
				}
				continue
			}
			if !errors.Is(err, domain.ErrOrderNotFound) {
				return err
			}
			debit, err := uc.wallet.DebitConsumer(txCtx, WalletCommand{
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

func validHistoricalMicrosoftMailbox(mailbox string) bool {
	switch mailbox {
	case "main", "alias", "dot", "plus":
		return true
	default:
		return false
	}
}

func sameHistoricalMicrosoftOrder(order domain.Order, ownerID uint, allocationID uint, match HistoricalMicrosoftUsage) bool {
	emailMatches := match.Mailbox == "main" || order.DeliveryEmail == strings.ToLower(strings.TrimSpace(match.Email))
	return order.UserID == ownerID && order.ProjectID == match.ProjectID && order.ProjectProductID == match.ProductID &&
		order.ProductType == domain.ProductTypeMicrosoft && order.ServiceMode == domain.ServiceModePurchase &&
		order.Status == domain.OrderStatusCompleted && emailMatches &&
		order.MicrosoftAllocID != nil && *order.MicrosoftAllocID == allocationID
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
	)
	if err != nil {
		return nil, err
	}
	if prepared.existing == nil {
		if err := uc.prepareCheckoutQuote(ctx, &prepared, nil); err != nil {
			return nil, err
		}
	}
	preparedItems := []checkoutPreparation{prepared}
	uc.precheckCheckoutInventory(ctx, preparedItems)
	result, runErr = uc.checkoutPrepared(ctx, preparedItems[0])
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
	projectID uint
	productID uint
	mode      domain.ServiceMode
}

type checkoutPreparation struct {
	request        CheckoutRequest
	mode           domain.ServiceMode
	policy         domain.SupplyPolicy
	idempotencyKey string
	fingerprint    string
	emailSuffix    string
	requestID      string
	existing       *domain.Order
	quote          *OrderingQuote
	prepareErr     error
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
	if req.UserID == 0 || req.ProjectID == 0 || req.ProductID == 0 {
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

	emailSuffix := normalizeEmailSuffix(req.EmailSuffix)
	fingerprintParts := []any{req.UserID, req.ProjectID, req.ProductID, mode, policy, emailSuffix, req.ClientChannel, apiKeyFingerprint(req.APIKeyID)}
	if req.BatchQuantity > 1 {
		fingerprintParts = append(fingerprintParts, req.BatchQuantity)
	}
	return checkoutPreparation{
		request:        req,
		mode:           mode,
		policy:         policy,
		idempotencyKey: idempotencyKey,
		fingerprint:    checkoutFingerprint(fingerprintParts...),
		emailSuffix:    emailSuffix,
		requestID:      strings.TrimSpace(req.RequestID),
	}, nil
}

func (uc *UseCase) prepareCheckoutQuote(ctx context.Context, prepared *checkoutPreparation, quotes map[checkoutQuoteKey]*OrderingQuote) error {
	key := checkoutQuoteKey{
		projectID: prepared.request.ProjectID,
		productID: prepared.request.ProductID,
		mode:      prepared.mode,
	}
	// This is the only path that evaluates current product sale status. Once an
	// order has been persisted, subsequent fulfilment must use that order's
	// immutable service-window snapshot instead.
	quote := quotes[key]
	if quote == nil {
		var err error
		quote, err = uc.ordering.GetOrderingQuote(
			ctx,
			prepared.request.ProjectID,
			prepared.request.ProductID,
			prepared.request.UserID,
			prepared.mode,
		)
		if err != nil {
			return err
		}
		if quotes != nil {
			quotes[key] = quote
		}
	}
	prepared.quote = quote
	return nil
}

func (uc *UseCase) checkoutPrepared(ctx context.Context, prepared checkoutPreparation) (*CheckoutResult, error) {
	if prepared.prepareErr != nil {
		return nil, prepared.prepareErr
	}
	if prepared.existing != nil {
		return uc.resumeExistingCheckout(
			ctx,
			prepared.request.UserID,
			prepared.existing.OrderNo,
			prepared.emailSuffix,
			prepared.requestID,
		)
	}
	if prepared.quote == nil {
		return nil, errors.New("checkout quote was not prepared")
	}
	var result *CheckoutResult
	var checkoutErr error
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, prepared.request.UserID); err != nil {
			return err
		}
		order, created, err := uc.repo.LoadOrCreatePendingOrder(txCtx, CreatePendingOrderCommand{
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
			return err
		}
		orderQuote := *prepared.quote
		if !created {
			storedQuote, quoteErr := orderingQuoteFromOrder(*order)
			if quoteErr != nil {
				return quoteErr
			}
			orderQuote = *storedQuote
		}
		result, err = uc.resumeCheckout(txCtx, *order, orderQuote, prepared.emailSuffix, prepared.requestID)
		if err != nil {
			if shouldCommitCheckoutError(err) {
				if result != nil {
					result.Created = created
				}
				checkoutErr = err
				return nil
			}
			return err
		}
		result.Created = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	if checkoutErr != nil {
		return result, checkoutErr
	}
	return result, nil
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
	uc.precheckCheckoutInventory(ctx, prepared)
	items, runErr = uc.checkoutBatch(ctx, prepared)
	return items, runErr
}

func (uc *UseCase) precheckCheckoutInventory(ctx context.Context, prepared []checkoutPreparation) {
	checker, ok := uc.allocation.(InventoryAvailabilityPort)
	if !ok || checker == nil {
		return
	}
	type inventoryKey struct {
		projectID   uint
		productID   uint
		buyerUserID uint
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
			buyerUserID: item.request.UserID, emailSuffix: item.emailSuffix,
			publicOnly: item.policy == domain.SupplyPolicyPublicOnly,
		}
		available, exists := availability[key]
		if !checked[key] {
			var err error
			available, err = checker.HasAvailableInventory(ctx, InventoryAvailabilityCommand{
				ProjectID: key.projectID, ProductID: key.productID, BuyerUserID: key.buyerUserID,
				EmailSuffix: key.emailSuffix, PublicOnly: key.publicOnly,
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
		BuyerUserID: prepared.request.UserID, EmailSuffix: prepared.emailSuffix,
		PublicOnly: prepared.policy == domain.SupplyPolicyPublicOnly,
	})
	if err != nil {
		slog.Warn("mark checkout inventory unavailable failed", "project_id", prepared.quote.ProjectID, "product_id", prepared.quote.ProductID, "error", err)
		return false
	}
	return marked
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
		if errors.Is(itemErr, domain.ErrInsufficientInventory) && result != nil && result.Created &&
			uc.markCheckoutInventoryUnavailable(ctx, prepared[index]) {
			for tail := index + 1; tail < len(prepared); tail++ {
				if sameCheckoutInventoryKey(prepared[index], prepared[tail]) && prepared[tail].existing == nil {
					prepared[tail].prepareErr = domain.ErrInsufficientInventory
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
		left.request.UserID == right.request.UserID &&
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
			return nil, err
		}
	}
	return prepared, nil
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
		)
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			prepared[i].prepareErr = err
			continue
		}
		if err != nil {
			return err
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
func (uc *UseCase) resumeExistingCheckout(ctx context.Context, userID uint, orderNo, emailSuffix, requestID string) (*CheckoutResult, error) {
	var result *CheckoutResult
	var checkoutErr error
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.wallet.LockConsumer(txCtx, userID); err != nil {
			return err
		}
		order, err := uc.repo.LockOrderForUpdate(txCtx, orderNo)
		if err != nil {
			return err
		}
		quote, err := orderingQuoteFromOrder(*order)
		if err != nil {
			return err
		}
		result, err = uc.resumeCheckout(txCtx, *order, *quote, emailSuffix, requestID)
		if err != nil {
			if shouldCommitCheckoutError(err) {
				checkoutErr = err
				return nil
			}
			return err
		}
		result.Created = false
		return nil
	})
	if err != nil {
		return nil, err
	}
	if checkoutErr != nil {
		return result, checkoutErr
	}
	return result, nil
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
		if token != nil {
			result.ServiceToken = token.TokenPlain
		}
	}
	if err := uc.attachOrderDelivery(ctx, result); err != nil {
		return nil, err
	}
	displayed := []CheckoutResult{*result}
	if err := uc.attachProjectDisplays(ctx, displayed); err != nil {
		return nil, err
	}
	result.ProjectName = displayed[0].ProjectName
	result.ProjectLogoURL = displayed[0].ProjectLogoURL
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
	if len(orderIDs) == 0 {
		return list, nil
	}
	if uc.deliveries != nil {
		deliveries, err := uc.deliveries.ListOrderDeliveries(ctx, orderIDs)
		if err != nil {
			return nil, err
		}
		for i := range results {
			attachOrderDeliverySummary(&results[i], deliveries[results[i].Order.ID])
		}
	}
	if err := uc.attachProjectDisplays(ctx, results); err != nil {
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

func (uc *UseCase) attachProjectDisplays(ctx context.Context, results []CheckoutResult) error {
	if uc.projectDisplays == nil || len(results) == 0 {
		return nil
	}
	idSet := make(map[uint]struct{}, len(results))
	ids := make([]uint, 0, len(results))
	for i := range results {
		id := results[i].Order.ProjectID
		if id == 0 {
			continue
		}
		if _, ok := idSet[id]; ok {
			continue
		}
		idSet[id] = struct{}{}
		ids = append(ids, id)
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
	if result == nil || strings.TrimSpace(delivery.VerificationCode) == "" || delivery.ReceivedAt.IsZero() {
		return
	}
	receivedAt := delivery.ReceivedAt.UTC()
	result.HasDelivery = true
	result.VerificationCode = delivery.VerificationCode
	result.LastMailReceivedAt = &receivedAt
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
	OrderNo         string
	Reason          string
	IdempotencyKey  string
	RequestID       string
	Operator        domain.OperatorType
	AllowedStatuses []domain.OrderStatus
}

func (uc *UseCase) expireCodeOrder(ctx context.Context, orderNo string, now time.Time) error {
	if uc.deliveries != nil {
		order, err := uc.repo.FindOrder(ctx, orderNo)
		if err != nil {
			return err
		}
		delivery, err := uc.deliveries.FindOrderDelivery(ctx, order.ID)
		if err != nil {
			return err
		}
		if delivery != nil {
			matchedAt := delivery.ReceivedAt
			if matchedAt.IsZero() {
				matchedAt = now
			}
			return uc.NotifyMatchedCode(ctx, MatchCodeResultRequest{OrderNo: orderNo, MatchedAt: matchedAt})
		}
	}
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo:        orderNo,
		Reason:         "Code receive window expired.",
		IdempotencyKey: "order:" + strings.TrimSpace(orderNo) + ":refund",
		Operator:       domain.OperatorTypeSystem,
		AllowedStatuses: []domain.OrderStatus{
			domain.OrderStatusActive,
		},
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
	failures := make([]string, 0, 2)
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
					failed, markErr := uc.repo.MarkFailed(ctx, MarkFailedCommand{
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
					return &CheckoutResult{Order: *failed}, err
				}
				return nil, err
			}
			currentAllocation = allocation
			payAmount := checkoutPayAmount(order.PayAmount, allocation.SupplyScope)
			debit, err := uc.wallet.DebitConsumer(ctx, WalletCommand{
				UserID:         order.UserID,
				Amount:         payAmount,
				Reason:         "order:" + order.OrderNo,
				IdempotencyKey: "order:" + order.OrderNo + ":debit",
				RequestID:      requestID,
			})
			if err != nil {
				if errors.Is(err, domain.ErrInsufficientBalance) {
					if releaseErr := uc.allocation.ReleaseByOrder(ctx, order.OrderNo); releaseErr != nil {
						return nil, releaseErr
					}
					failed, markErr := uc.repo.MarkFailed(ctx, MarkFailedCommand{
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
					return &CheckoutResult{Order: *failed}, err
				}
				return nil, err
			}
			updated, err := uc.repo.MarkPaid(ctx, MarkPaidCommand{
				OrderNo:   order.OrderNo,
				DebitTxID: debit.ID,
				PayAmount: payAmount,
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
				return nil, err
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
					failed, refundErr := uc.refundPaidOrder(ctx, order, domain.OrderFailureInsufficientInventory, "Allocation failed.")
					if refundErr != nil {
						return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
					}
					if failed == nil {
						return nil, errors.New("refund failed order returned no order")
					}
					return &CheckoutResult{Order: *failed}, err
				}
			}
			receiveStartedAt := uc.now()
			receiveUntil := serviceReceiveUntil(receiveStartedAt, quote, order.ServiceMode)
			afterSaleUntil := initialAfterSaleUntil(receiveUntil, order.ServiceMode)
			token, err := uc.tokens.IssueOrderToken(ctx, order.OrderNo, tokenExpireAt(order.ServiceMode, receiveUntil))
			if err != nil {
				if releaseErr := uc.allocation.ReleaseByOrder(ctx, order.OrderNo); releaseErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, releaseErr)
				}
				if _, refundErr := uc.refundPaidOrder(ctx, order, domain.OrderFailureServiceToken, "Service token failed."); refundErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
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
				if disableErr := uc.tokens.DisableOrderToken(ctx, order.OrderNo, "Order activation failed."); disableErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, disableErr)
				}
				if releaseErr := uc.allocation.ReleaseByOrder(ctx, order.OrderNo); releaseErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, releaseErr)
				}
				if _, refundErr := uc.refundPaidOrder(ctx, order, domain.OrderFailureActivation, "Order activation failed."); refundErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
				}
				return nil, err
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
			return &CheckoutResult{Order: order, ServiceToken: token.TokenPlain}, nil

		case domain.OrderStatusFailed:
			return &CheckoutResult{Order: order}, checkoutErrorForFailedOrder(order)

		default:
			return &CheckoutResult{Order: order}, nil
		}
	}
}

func (uc *UseCase) allocate(ctx context.Context, order domain.Order, emailSuffix string) (*AllocationResult, error) {
	scopes := []SupplyScope{SupplyScopePublic}
	if order.SupplyPolicy == domain.SupplyPolicyPrivateFirst {
		scopes = []SupplyScope{SupplyScopeOwned, SupplyScopePublic}
	}
	return uc.allocation.Allocate(ctx, AllocationCommand{
		OrderNo:              order.OrderNo,
		BuyerUserID:          order.UserID,
		ProjectProductID:     order.ProjectProductID,
		SupplyScopes:         scopes,
		EmailSuffix:          emailSuffix,
		FulfillExistingOrder: true,
	})
}

func checkoutPayAmount(listedAmount string, scope SupplyScope) string {
	if scope == SupplyScopeOwned {
		return "0.00"
	}
	return listedAmount
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
	return uc.repo.MarkFailed(ctx, MarkFailedCommand{
		OrderNo:      order.OrderNo,
		RefundTxID:   &refund.ID,
		RefundAmount: order.PayAmount,
		FailureCode:  failureCode,
		Reason:       reason,
		Now:          uc.now(),
	})
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

func apiKeyFingerprint(apiKeyID *uint) uint {
	if apiKeyID == nil {
		return 0
	}
	return *apiKeyID
}

func normalizeEmailSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "@")
}

func orderAllowsServiceToken(status domain.OrderStatus) bool {
	return status == domain.OrderStatusActive || status == domain.OrderStatusCompleted
}

func nextOrderNo() string {
	return "OR" + platform.NewUUIDV7CompactUpper()
}
