package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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
	EmailSuffix      string
}

type AllocationResult struct {
	Type        domain.AllocationType
	ID          uint
	Email       string
	SupplyScope SupplyScope
}

type AllocationPort interface {
	Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error)
	ReleaseByOrder(ctx context.Context, orderNo string) error
}

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

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	LoadOrCreatePendingOrder(ctx context.Context, cmd CreatePendingOrderCommand) (*domain.Order, bool, error)
	FindOrder(ctx context.Context, orderNo string) (*domain.Order, error)
	MarkPaid(ctx context.Context, cmd MarkPaidCommand) (*domain.Order, error)
	MarkActive(ctx context.Context, cmd MarkActiveCommand) (*domain.Order, error)
	MarkFailed(ctx context.Context, cmd MarkFailedCommand) (*domain.Order, error)
	Archive(ctx context.Context, orderNo string, userID uint, archivedAt time.Time) (*domain.Order, error)
	ListOrders(ctx context.Context, filter OrderListFilter, offset, limit int) ([]domain.Order, int64, error)
	ListEvents(ctx context.Context, orderNo string, userID uint, isAdmin bool, offset, limit int) ([]domain.OrderEvent, int64, error)
	CompleteCodeOrder(ctx context.Context, orderNo string, matchedAt time.Time, readUntil time.Time) (*domain.Order, bool, error)
	ActivatePurchaseOrder(ctx context.Context, orderNo string, matchedAt time.Time, afterSaleUntil time.Time) (*domain.Order, bool, error)
}

type CreatePendingOrderCommand struct {
	OrderNo            string
	UserID             uint
	ProjectID          uint
	ProjectProductID   uint
	ProductType        domain.ProductType
	ServiceMode        domain.ServiceMode
	SupplyPolicy       domain.SupplyPolicy
	PayAmount          string
	ClientChannel      domain.ClientChannel
	APIKeyID           *uint
	IdempotencyKey     string
	RequestFingerprint string
	Now                time.Time
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
	Reason       string
	Now          time.Time
}

type OrderListFilter struct {
	UserID      uint
	IsAdmin     bool
	Scope       string
	Status      domain.OrderStatus
	ServiceMode domain.ServiceMode
	Search      string
}

type CheckoutRequest struct {
	UserID         uint
	ProjectID      uint
	ProductID      uint
	ServiceMode    string
	SupplyPolicy   string
	EmailSuffix    string
	ClientChannel  domain.ClientChannel
	APIKeyID       *uint
	IdempotencyKey string
	RequestID      string
}

type CheckoutResult struct {
	Order        domain.Order
	ServiceToken string
	Created      bool
}

type MatchCodeResultRequest struct {
	OrderNo   string
	MatchedAt time.Time
}

type UseCase struct {
	repo       Repository
	ordering   OrderingPort
	wallet     WalletPort
	allocation AllocationPort
	tokens     OrderTokenPort
	now        func() time.Time
}

func NewUseCase(repo Repository, ordering OrderingPort, wallet WalletPort, allocation AllocationPort, tokens OrderTokenPort) *UseCase {
	return &UseCase{
		repo:       repo,
		ordering:   ordering,
		wallet:     wallet,
		allocation: allocation,
		tokens:     tokens,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func (uc *UseCase) Checkout(ctx context.Context, req CheckoutRequest) (*CheckoutResult, error) {
	mode, ok := domain.NormalizeServiceMode(req.ServiceMode)
	if !ok {
		return nil, domain.ErrInvalidOrderRequest
	}
	policy, ok := domain.NormalizeSupplyPolicy(req.SupplyPolicy)
	if !ok {
		return nil, domain.ErrInvalidOrderRequest
	}
	if req.UserID == 0 || req.ProjectID == 0 || req.ProductID == 0 {
		return nil, domain.ErrInvalidOrderRequest
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	if req.ClientChannel == "" {
		req.ClientChannel = domain.ClientChannelConsole
	}
	if req.ClientChannel == domain.ClientChannelAPIKey && (req.APIKeyID == nil || *req.APIKeyID == 0) {
		return nil, domain.ErrInvalidOrderRequest
	}
	if req.ClientChannel == domain.ClientChannelConsole {
		req.APIKeyID = nil
	}

	quote, err := uc.ordering.GetOrderingQuote(ctx, req.ProjectID, req.ProductID, req.UserID, mode)
	if err != nil {
		return nil, err
	}
	emailSuffix := normalizeEmailSuffix(req.EmailSuffix)
	fingerprint := checkoutFingerprint(req.UserID, req.ProjectID, req.ProductID, mode, policy, emailSuffix, req.ClientChannel, apiKeyFingerprint(req.APIKeyID))
	var result *CheckoutResult
	var checkoutErr error
	err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		order, created, err := uc.repo.LoadOrCreatePendingOrder(txCtx, CreatePendingOrderCommand{
			OrderNo:            nextOrderNo(),
			UserID:             req.UserID,
			ProjectID:          quote.ProjectID,
			ProjectProductID:   quote.ProductID,
			ProductType:        quote.ProductType,
			ServiceMode:        mode,
			SupplyPolicy:       policy,
			PayAmount:          quote.PayAmount,
			ClientChannel:      req.ClientChannel,
			APIKeyID:           req.APIKeyID,
			IdempotencyKey:     idempotencyKey,
			RequestFingerprint: fingerprint,
			Now:                uc.now(),
		})
		if err != nil {
			return err
		}
		result, err = uc.resumeCheckout(txCtx, *order, *quote, emailSuffix, strings.TrimSpace(req.RequestID))
		if err != nil {
			if shouldCommitCheckoutError(err) {
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
		return nil, checkoutErr
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
	return result, nil
}

func (uc *UseCase) ListOrders(ctx context.Context, filter OrderListFilter, offset, limit int) ([]CheckoutResult, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	items, total, err := uc.repo.ListOrders(ctx, filter, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	results := make([]CheckoutResult, len(items))
	for i := range items {
		results[i].Order = items[i]
	}
	return results, total, nil
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
		quote, err := uc.ordering.GetOrderingQuote(ctx, order.ProjectID, order.ProjectProductID, order.UserID, domain.ServiceModePurchase)
		if err != nil {
			return err
		}
		afterSaleUntil := purchaseWarrantyUntil(*order, *quote, matchedAt)
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

func (uc *UseCase) resumeCheckout(ctx context.Context, order domain.Order, quote OrderingQuote, emailSuffix string, requestID string) (*CheckoutResult, error) {
	for {
		switch order.Status {
		case domain.OrderStatusPendingPayment:
			allocation, err := uc.allocate(ctx, order, emailSuffix)
			if err != nil {
				_, _ = uc.repo.MarkFailed(ctx, MarkFailedCommand{OrderNo: order.OrderNo, Reason: "Allocation failed.", Now: uc.now()})
				return nil, err
			}
			payAmount := checkoutPayAmount(order.PayAmount, allocation.SupplyScope)
			debit, err := uc.wallet.DebitConsumer(ctx, WalletCommand{
				UserID:         order.UserID,
				Amount:         payAmount,
				Reason:         "order:" + order.OrderNo,
				IdempotencyKey: "order:" + order.OrderNo + ":debit",
				RequestID:      requestID,
			})
			if err != nil {
				if err == domain.ErrInsufficientBalance {
					_ = uc.allocation.ReleaseByOrder(ctx, order.OrderNo)
					_, _ = uc.repo.MarkFailed(ctx, MarkFailedCommand{OrderNo: order.OrderNo, Reason: "Payment failed.", Now: uc.now()})
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
			allocation, err := uc.allocate(ctx, order, emailSuffix)
			if err != nil {
				if _, refundErr := uc.refundPaidOrder(ctx, order, "Allocation failed."); refundErr != nil {
					return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, refundErr)
				}
				return nil, err
			}
			receiveStartedAt := uc.now()
			receiveUntil := serviceReceiveUntil(receiveStartedAt, quote, order.ServiceMode)
			afterSaleUntil := initialAfterSaleUntil(receiveUntil, order.ServiceMode)
			token, err := uc.tokens.IssueOrderToken(ctx, order.OrderNo, tokenExpireAt(order.ServiceMode, receiveUntil))
			if err != nil {
				_ = uc.allocation.ReleaseByOrder(ctx, order.OrderNo)
				if _, refundErr := uc.refundPaidOrder(ctx, order, "Service token failed."); refundErr != nil {
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
				_ = uc.tokens.DisableOrderToken(ctx, order.OrderNo, "Order activation failed.")
				_ = uc.allocation.ReleaseByOrder(ctx, order.OrderNo)
				if _, refundErr := uc.refundPaidOrder(ctx, order, "Order activation failed."); refundErr != nil {
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
	var lastErr error
	for _, scope := range scopes {
		result, err := uc.allocation.Allocate(ctx, AllocationCommand{
			OrderNo:          order.OrderNo,
			BuyerUserID:      order.UserID,
			ProjectProductID: order.ProjectProductID,
			SupplyScope:      scope,
			EmailSuffix:      emailSuffix,
		})
		if err == nil {
			return result, nil
		}
		lastErr = err
		if err != domain.ErrInsufficientInventory {
			return nil, err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, domain.ErrInsufficientInventory
}

func checkoutPayAmount(listedAmount string, scope SupplyScope) string {
	if scope == SupplyScopeOwned {
		return "0.00"
	}
	return listedAmount
}

func (uc *UseCase) refundPaidOrder(ctx context.Context, order domain.Order, reason string) (*domain.Order, error) {
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

func purchaseWarrantyUntil(order domain.Order, quote OrderingQuote, matchedAt time.Time) time.Time {
	start := matchedAt.UTC()
	if order.ReceiveStartedAt != nil && !order.ReceiveStartedAt.IsZero() {
		start = order.ReceiveStartedAt.UTC()
	}
	until := start.Add(time.Duration(quote.WarrantyMinutes) * time.Minute)
	if until.Before(matchedAt.UTC()) {
		return matchedAt.UTC()
	}
	return until
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
	return err == domain.ErrInsufficientBalance || err == domain.ErrInsufficientInventory
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
