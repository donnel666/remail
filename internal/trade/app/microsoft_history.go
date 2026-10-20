package app

import (
	"crypto/sha256"
	"fmt"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
)

func sameHistoricalMicrosoftOrder(
	order *domain.Order,
	allocation *AllocationResult,
	match HistoricalMicrosoftUsage,
) bool {
	if order == nil || allocation == nil {
		return false
	}
	requestFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(allocation.OrderNo))))
	if order.OrderNo != strings.TrimSpace(allocation.OrderNo) ||
		order.UserID != historicalMicrosoftOwnerUserID || order.ProjectID != match.ProjectID ||
		order.ProjectProductID != allocation.ProductID || order.ServiceMode != domain.ServiceModePurchase ||
		order.SupplyPolicy != domain.SupplyPolicyPublicOnly || order.Status != domain.OrderStatusCompleted ||
		order.FailureCode != "" || order.PayAmount != "0.00" || order.RefundAmount != "0.00" ||
		order.LegacyRandomMicrosoftPayAmount != "" || order.LegacyRandomDomainPayAmount != "" ||
		order.DebitTxID == nil || *order.DebitTxID == 0 || order.RefundTxID != nil ||
		order.AllocationType == nil || *order.AllocationType != allocation.Type ||
		!strings.EqualFold(order.DeliveryEmail, allocation.Email) || order.ClientChannel != domain.ClientChannelConsole ||
		order.APIKeyID != nil || order.IdempotencyKey != "history:"+order.OrderNo ||
		order.RequestFingerprint != requestFingerprint || order.ServiceCleanupStatus != "succeeded" ||
		allocation.CreatedAt.IsZero() || allocation.ReleasedAt == nil || allocation.ReleasedAt.Before(allocation.CreatedAt) ||
		!sameHistoricalMicrosoftTime(order.CreatedAt, allocation.CreatedAt) ||
		order.ReceiveStartedAt == nil || !sameHistoricalMicrosoftTime(*order.ReceiveStartedAt, allocation.CreatedAt) ||
		order.ReceiveUntil == nil || !sameHistoricalMicrosoftTime(*order.ReceiveUntil, *allocation.ReleasedAt) ||
		order.ActivatedAt == nil || !sameHistoricalMicrosoftTime(*order.ActivatedAt, allocation.CreatedAt) ||
		order.AfterSaleUntil == nil || !sameHistoricalMicrosoftTime(*order.AfterSaleUntil, *allocation.ReleasedAt) {
		return false
	}
	if allocation.ProductID == match.ProductID {
		if coredomain.IsMicrosoftEnterpriseEmail(allocation.Email) {
			return order.ProductType == domain.ProductTypeMicrosoftEnterprise
		}
		return order.ProductType == domain.ProductTypeMicrosoft
	}
	return false
}

func sameHistoricalMicrosoftTime(actual, expected time.Time) bool {
	return actual.UTC().Truncate(time.Second).Equal(expected.UTC().Truncate(time.Second))
}
