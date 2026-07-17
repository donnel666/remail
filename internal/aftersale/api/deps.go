package api

import (
	"context"
	"errors"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/donnel666/remail/internal/aftersale/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *aftersaleapp.UseCase
}

func NewModule(db *gorm.DB, trade *tradeapp.UseCase, fileStore governanceapp.FilePort) *Module {
	repo := infra.NewRepo(db)
	uc := aftersaleapp.NewUseCase(
		repo,
		tradeOrderAdapter{trade: trade},
		tradeRefundAdapter{trade: trade},
		fileStoreAdapter{store: fileStore},
	)
	return &Module{UseCase: uc}
}

// tradeOrderAdapter resolves an order from BC-TRADE for ticket creation. Passing
// the requester id makes Trade enforce ownership for us.
type tradeOrderAdapter struct {
	trade *tradeapp.UseCase
}

func (a tradeOrderAdapter) GetOrderForTicket(ctx context.Context, orderNo string, requesterUserID uint) (*aftersaleapp.OrderInfo, error) {
	result, err := a.trade.GetOrder(ctx, orderNo, requesterUserID, false)
	if err != nil {
		return nil, mapTradeOrderError(err)
	}
	order := result.Order
	return &aftersaleapp.OrderInfo{
		OrderNo:        order.OrderNo,
		ProjectName:    result.ProjectName,
		ProjectLogoURL: result.ProjectLogoURL,
		DeliveryEmail:  order.DeliveryEmail,
		PayAmount:      order.PayAmount,
		ServiceMode:    string(order.ServiceMode),
		Status:         string(order.Status),
		RefundAmount:   order.RefundAmount,
		AfterSaleUntil: order.AfterSaleUntil,
		ReceiveUntil:   order.ReceiveUntil,
	}, nil
}

// tradeRefundAdapter issues an order refund through BC-TRADE so wallet,
// allocation and receipts stay consistent (INV-AS3).
type tradeRefundAdapter struct {
	trade *tradeapp.UseCase
}

func (a tradeRefundAdapter) RefundOrder(ctx context.Context, cmd aftersaleapp.RefundCommand) (*aftersaleapp.RefundResult, error) {
	order, err := a.trade.AdminRefundOrder(ctx, tradeapp.AdminOrderCommandRequest{
		OrderNo:        cmd.OrderNo,
		Reason:         cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey,
		RequestID:      cmd.RequestID,
		OperatorUserID: cmd.OperatorUserID,
	})
	if err != nil {
		return nil, mapTradeRefundError(err)
	}
	return &aftersaleapp.RefundResult{RefundAmount: order.RefundAmount}, nil
}

// fileStoreAdapter keeps governance's file domain types out of the aftersale
// application layer.
type fileStoreAdapter struct {
	store governanceapp.FilePort
}

func (a fileStoreAdapter) Save(ctx context.Context, objectKey, mime, fileName string, content []byte) error {
	_, err := a.store.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    objectKey,
		FileName:     fileName,
		ContentType:  mime,
		ContentBytes: content,
	})
	return err
}

func (a fileStoreAdapter) Read(ctx context.Context, objectKey string) (string, []byte, error) {
	file, err := a.store.ReadPrivate(ctx, objectKey)
	if err != nil {
		return "", nil, err
	}
	return file.ContentType, file.ContentBytes, nil
}

func mapTradeOrderError(err error) error {
	switch {
	case errors.Is(err, tradedomain.ErrOrderNotFound):
		return domain.ErrOrderNotEligible
	case errors.Is(err, tradedomain.ErrOrderForbidden):
		return domain.ErrTicketForbidden
	default:
		return err
	}
}

func mapTradeRefundError(err error) error {
	switch {
	case errors.Is(err, tradedomain.ErrOrderNotFound):
		return domain.ErrTicketNotFound
	case errors.Is(err, tradedomain.ErrOrderForbidden):
		return domain.ErrTicketForbidden
	case errors.Is(err, tradedomain.ErrOrderStateConflict), errors.Is(err, tradedomain.ErrIdempotencyConflict):
		return domain.ErrTicketStateConflict
	default:
		return err
	}
}
