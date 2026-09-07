package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
)

const historicalProtoOwnerUserID uint = 1

type HistoricalProtoAllocationCommand struct {
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Mailbox    string
	Email      string
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type HistoricalProtoAllocationPort interface {
	ImportHistoricalProtoAllocation(context.Context, HistoricalProtoAllocationCommand) (*AllocationResult, error)
}

type HistoricalProtoOrderRepository interface {
	CreateHistoricalProtoOrder(context.Context, CreateHistoricalProtoOrderCommand) error
}

type CreateHistoricalProtoOrderCommand struct {
	OrderNo                 string
	UserID                  uint
	ProjectID               uint
	ProjectProductID        uint
	ProductType             domain.ProductType
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	DebitTxID               uint
	AllocationID            uint
	DeliveryEmail           string
	CreatedAt               time.Time
	ExpiredAt               time.Time
	Now                     time.Time
}

type HistoricalProtoUsage struct {
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

func (uc *UseCase) ImportHistoricalProtoUsage(ctx context.Context, matches []HistoricalProtoUsage) error {
	if len(matches) == 0 {
		return nil
	}
	if uc == nil || uc.repo == nil || uc.wallet == nil || uc.allocation == nil {
		return domain.ErrInvalidOrderRequest
	}
	orders, ordersOK := uc.repo.(HistoricalProtoOrderRepository)
	allocations, allocOK := uc.allocation.(HistoricalProtoAllocationPort)
	if !ordersOK || !allocOK {
		return domain.ErrInvalidOrderRequest
	}
	ownerID := historicalProtoOwnerUserID
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		now := uc.now()
		expiryCutoff := now.Add(-time.Second).Truncate(time.Second)
		for _, match := range matches {
			match.Mailbox = strings.ToLower(strings.TrimSpace(match.Mailbox))
			match.Email = strings.ToLower(strings.TrimSpace(match.Email))
			if match.ResourceID == 0 || match.ProjectID == 0 || match.ProductID == 0 || match.Email == "" ||
				match.ProductType != domain.ProductTypeProto ||
				match.EvidenceCount <= 0 || match.Mailbox != "main" {
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
			allocation, err := allocations.ImportHistoricalProtoAllocation(txCtx, HistoricalProtoAllocationCommand{
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
				allocation.Type != domain.AllocationTypeProto || allocation.SupplyScope != SupplyScopePublic ||
				!strings.EqualFold(allocation.Email, match.Email) {
				return domain.ErrInvalidOrderRequest
			}
			orderNo := strings.TrimSpace(allocation.OrderNo)
			existing, findErr := uc.repo.FindOrder(txCtx, orderNo)
			if findErr == nil {
				if allocation.Created || !sameHistoricalProtoOrder(existing, allocation, match) {
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
			if err := orders.CreateHistoricalProtoOrder(txCtx, CreateHistoricalProtoOrderCommand{
				OrderNo: orderNo, UserID: ownerID, ProjectID: match.ProjectID, ProjectProductID: match.ProductID,
				ProductType:       match.ProductType,
				CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
				WarrantyMinutes: match.WarrantyMinutes, DebitTxID: debit.ID, AllocationID: allocation.ID,
				DeliveryEmail: match.Email, CreatedAt: createdAt, ExpiredAt: expiredAt, Now: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func sameHistoricalProtoOrder(
	order *domain.Order,
	allocation *AllocationResult,
	match HistoricalProtoUsage,
) bool {
	if order == nil || allocation == nil {
		return false
	}
	requestFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(allocation.OrderNo))))
	if order.OrderNo != strings.TrimSpace(allocation.OrderNo) ||
		order.UserID != historicalProtoOwnerUserID || order.ProjectID != match.ProjectID ||
		order.ProjectProductID != allocation.ProductID || order.ServiceMode != domain.ServiceModePurchase ||
		order.SupplyPolicy != domain.SupplyPolicyPublicOnly || order.Status != domain.OrderStatusCompleted ||
		order.FailureCode != "" || order.PayAmount != "0.00" || order.RefundAmount != "0.00" ||
		order.LegacyRandomMicrosoftPayAmount != "" || order.LegacyRandomDomainPayAmount != "" ||
		order.DebitTxID == nil || *order.DebitTxID == 0 || order.RefundTxID != nil ||
		order.AllocationType == nil || *order.AllocationType != domain.AllocationTypeProto ||
		!strings.EqualFold(order.DeliveryEmail, match.Email) || order.ClientChannel != domain.ClientChannelConsole ||
		order.APIKeyID != nil || order.IdempotencyKey != "history:"+order.OrderNo ||
		order.RequestFingerprint != requestFingerprint || order.ServiceCleanupStatus != "succeeded" ||
		allocation.CreatedAt.IsZero() || allocation.ReleasedAt == nil || allocation.ReleasedAt.Before(allocation.CreatedAt) ||
		!sameHistoricalProtoTime(order.CreatedAt, allocation.CreatedAt) ||
		order.ReceiveStartedAt == nil || !sameHistoricalProtoTime(*order.ReceiveStartedAt, allocation.CreatedAt) ||
		order.ReceiveUntil == nil || !sameHistoricalProtoTime(*order.ReceiveUntil, *allocation.ReleasedAt) ||
		order.ActivatedAt == nil || !sameHistoricalProtoTime(*order.ActivatedAt, allocation.CreatedAt) ||
		order.AfterSaleUntil == nil || !sameHistoricalProtoTime(*order.AfterSaleUntil, *allocation.ReleasedAt) {
		return false
	}
	if allocation.ProductID == match.ProductID {
		return order.ProductType == match.ProductType
	}
	return false
}

func sameHistoricalProtoTime(actual, expected time.Time) bool {
	return actual.UTC().Truncate(time.Second).Equal(expected.UTC().Truncate(time.Second))
}
