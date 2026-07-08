package api

import (
	"context"
	"errors"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/trade/infra"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *tradeapp.UseCase
}

func NewModule(db *gorm.DB, coreProjects *coreapp.ProjectUseCase, billingWallet *billingapp.WalletUseCase, alloc *allocapp.UseCase, tokens *openapiapp.UseCase) *Module {
	repo := infra.NewRepo(db)
	return &Module{
		UseCase: tradeapp.NewUseCase(
			repo,
			coreOrderingAdapter{projects: coreProjects},
			billingWalletAdapter{wallet: billingWallet},
			allocationAdapter{alloc: alloc},
			orderTokenAdapter{tokens: tokens},
		),
	}
}

type coreOrderingAdapter struct {
	projects *coreapp.ProjectUseCase
}

func (a coreOrderingAdapter) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode domain.ServiceMode) (*tradeapp.OrderingQuote, error) {
	quote, err := a.projects.GetOrderingQuote(ctx, projectID, productID, buyerUserID, string(serviceMode))
	if err != nil {
		return nil, mapCoreError(err)
	}
	return &tradeapp.OrderingQuote{
		ProjectID:               quote.ProjectID,
		ProductID:               quote.ProductID,
		ProductType:             domain.ProductType(quote.ProductType),
		PayAmount:               quote.PayAmount,
		CodeWindowMinutes:       quote.CodeWindowMinutes,
		ActivationWindowMinutes: quote.ActivationWindowMinutes,
		WarrantyMinutes:         quote.WarrantyMinutes,
	}, nil
}

type billingWalletAdapter struct {
	wallet *billingapp.WalletUseCase
}

func (a billingWalletAdapter) DebitConsumer(ctx context.Context, cmd tradeapp.WalletCommand) (*tradeapp.WalletTransaction, error) {
	result, err := a.wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID:         cmd.UserID,
		Amount:         cmd.Amount,
		Reason:         cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey,
		RequestID:      cmd.RequestID,
	})
	if err != nil {
		return nil, mapBillingError(err)
	}
	return &tradeapp.WalletTransaction{ID: result.Transaction.ID}, nil
}

func (a billingWalletAdapter) RefundConsumer(ctx context.Context, cmd tradeapp.WalletCommand) (*tradeapp.WalletTransaction, error) {
	result, err := a.wallet.RefundConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID:         cmd.UserID,
		Amount:         cmd.Amount,
		Reason:         cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey,
		RequestID:      cmd.RequestID,
	})
	if err != nil {
		return nil, mapBillingError(err)
	}
	return &tradeapp.WalletTransaction{ID: result.Transaction.ID}, nil
}

type allocationAdapter struct {
	alloc *allocapp.UseCase
}

func (a allocationAdapter) Allocate(ctx context.Context, cmd tradeapp.AllocationCommand) (*tradeapp.AllocationResult, error) {
	scope := allocdomain.SupplyScopePublic
	if cmd.SupplyScope == tradeapp.SupplyScopeOwned {
		scope = allocdomain.SupplyScopeOwned
	}
	result, err := a.alloc.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo:          cmd.OrderNo,
		BuyerUserID:      cmd.BuyerUserID,
		ProjectProductID: cmd.ProjectProductID,
		SupplyScope:      scope,
		EmailSuffix:      cmd.EmailSuffix,
	})
	if err != nil {
		return nil, mapAllocationError(err)
	}
	return &tradeapp.AllocationResult{
		Type:  domain.AllocationType(result.Type),
		ID:    result.ID,
		Email: result.Email,
	}, nil
}

func (a allocationAdapter) ReleaseByOrder(ctx context.Context, orderNo string) error {
	_, err := a.alloc.ReleaseByOrder(ctx, orderNo)
	if err != nil && !errors.Is(err, allocdomain.ErrAllocationNotFound) {
		return mapAllocationError(err)
	}
	return nil
}

type orderTokenAdapter struct {
	tokens *openapiapp.UseCase
}

func (a orderTokenAdapter) IssueOrderToken(ctx context.Context, orderNo string, expireAt *time.Time) (*tradeapp.OrderToken, error) {
	token, err := a.tokens.IssueOrderToken(ctx, orderNo, expireAt)
	if err != nil {
		return nil, err
	}
	return &tradeapp.OrderToken{TokenPlain: token.TokenPlain, ExpireAt: token.ExpireAt}, nil
}

func (a orderTokenAdapter) FindOrderTokenByOrder(ctx context.Context, orderNo string) (*tradeapp.OrderToken, error) {
	token, err := a.tokens.FindOrderTokenByOrder(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	return &tradeapp.OrderToken{TokenPlain: token.TokenPlain, ExpireAt: token.ExpireAt}, nil
}

func (a orderTokenAdapter) DisableOrderToken(ctx context.Context, orderNo string, reason string) error {
	return a.tokens.DisableOrderToken(ctx, orderNo, reason)
}

func mapCoreError(err error) error {
	switch {
	case errors.Is(err, coredomain.ErrForbiddenProject), errors.Is(err, coredomain.ErrProjectNotFound):
		return domain.ErrProjectUnavailable
	case errors.Is(err, coredomain.ErrInvalidProduct), errors.Is(err, coredomain.ErrInvalidProjectStatus), errors.Is(err, coredomain.ErrInvalidProject):
		return domain.ErrProjectUnavailable
	default:
		return err
	}
}

func mapBillingError(err error) error {
	switch {
	case errors.Is(err, billingdomain.ErrInsufficientBalance):
		return domain.ErrInsufficientBalance
	case errors.Is(err, billingdomain.ErrIdempotencyRequired):
		return domain.ErrIdempotencyRequired
	case errors.Is(err, billingdomain.ErrIdempotencyConflict):
		return domain.ErrIdempotencyConflict
	default:
		return err
	}
}

func mapAllocationError(err error) error {
	switch {
	case errors.Is(err, allocdomain.ErrInsufficientInventory):
		return domain.ErrInsufficientInventory
	case errors.Is(err, allocdomain.ErrProjectNotAllocatable), errors.Is(err, allocdomain.ErrInvalidAllocationRequest):
		return domain.ErrProjectUnavailable
	default:
		return err
	}
}
