package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	"github.com/donnel666/remail/internal/smsbower"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/upstream"
)

type productInventoryOverlayChain []allocapp.ProductInventoryOverlay

func (chain productInventoryOverlayChain) OverlayProductInventory(ctx context.Context, projectIDs []uint, snapshots map[uint]*allocapp.ProjectProductInventoryTotals) error {
	for _, overlay := range chain {
		if overlay != nil {
			if err := overlay.OverlayProductInventory(ctx, projectIDs, snapshots); err != nil {
				return err
			}
		}
	}
	return nil
}

type smsbowerInventoryOverlay struct{ smsbower *smsbower.Service }

func (o smsbowerInventoryOverlay) OverlayProductInventory(ctx context.Context, projectIDs []uint, snapshots map[uint]*allocapp.ProjectProductInventoryTotals) error {
	items, err := o.smsbower.ListInventory(ctx, projectIDs)
	if err != nil {
		return err
	}
	overlaySMSBowerInventory(snapshots, items)
	return nil
}

func overlaySMSBowerInventory(snapshots map[uint]*allocapp.ProjectProductInventoryTotals, items []smsbower.InventoryItem) {
	for _, upstreamItem := range items {
		snapshot := snapshots[upstreamItem.ProjectID]
		if snapshot == nil {
			snapshot = &allocapp.ProjectProductInventoryTotals{ProjectID: upstreamItem.ProjectID}
			snapshots[upstreamItem.ProjectID] = snapshot
		}
		found := false
		for i := range snapshot.Items {
			if snapshot.Items[i].ProductID != upstreamItem.ProductID {
				continue
			}
			snapshot.Items[i].ProductType = coredomain.ProductTypeGmail
			previous := snapshot.Items[i].TotalAvailable
			addSMSBowerInventory(&snapshot.Items[i], upstreamItem.CodeAvailable)
			snapshot.TotalAvailable = max(0, snapshot.TotalAvailable+snapshot.Items[i].TotalAvailable-previous)
			found = true
			break
		}
		if !found {
			item := allocapp.ProductInventoryTotal{
				ProductID: upstreamItem.ProductID, ProductType: coredomain.ProductTypeGmail,
			}
			addSMSBowerInventory(&item, upstreamItem.CodeAvailable)
			snapshot.Items = append(snapshot.Items, item)
			snapshot.TotalAvailable += item.TotalAvailable
		}
	}
}

func addSMSBowerInventory(item *allocapp.ProductInventoryTotal, available int64) {
	code, publicCode := available, available
	if item.CodeAvailable != nil {
		code += *item.CodeAvailable
	}
	if item.CodePublicAvailable != nil {
		publicCode += *item.CodePublicAvailable
	}
	item.CodeAvailable, item.CodePublicAvailable = &code, &publicCode
	purchase, publicPurchase := int64(0), int64(0)
	if item.PurchaseAvailable != nil {
		purchase = *item.PurchaseAvailable
	}
	if item.PurchasePublicAvailable != nil {
		publicPurchase = *item.PurchasePublicAvailable
	}
	item.TotalAvailable = max(code, purchase)
	item.PublicAvailable = max(publicCode, publicPurchase)
}

type smsbowerAlertMailer struct {
	users    announcementRecipientSource
	delivery mailapp.DeliveryPort
}

func (m smsbowerAlertMailer) NotifySMSBower(ctx context.Context, alert smsbower.Alert) error {
	if m.users == nil || m.delivery == nil {
		return nil
	}
	role, enabled := iamdomain.RoleSuperAdmin, true
	users, err := m.users.ListByFilter(ctx, iamdomain.UserListFilter{Role: &role, Enabled: &enabled}, 0, -1)
	if err != nil {
		return fmt.Errorf("list SMSBower alert recipients: %w", err)
	}
	var result error
	for _, user := range users {
		if user.Role != iamdomain.RoleSuperAdmin || !user.IsActive() || strings.TrimSpace(user.Email) == "" {
			continue
		}
		if err := m.delivery.Send(ctx, mailapp.SMSBowerAlertMessage(user.Email, alert.ID, alert.Subject, alert.Body)); err != nil {
			result = errors.Join(result, fmt.Errorf("send SMSBower alert to user %d: %w", user.ID, err))
		}
	}
	return result
}

type gmailOrderTokenReader interface {
	FindOrderTokenByPlain(ctx context.Context, tokenPlain string) (*openapidomain.OrderToken, error)
}

type gmailPickupAdapter struct {
	upstreams *upstream.Router
	tokens    gmailOrderTokenReader
}

type botCodeDiagnosisRefreshAdapter struct {
	local     *mailmatchapp.UseCase
	upstreams *upstream.Router
}

func (a botCodeDiagnosisRefreshAdapter) RefreshCodeDiagnosis(ctx context.Context, orderNo, email string, emailResourceID uint) (mailmatchapp.CodeDiagnosisRefreshResult, error) {
	if emailResourceID > 0 {
		if a.local == nil {
			return mailmatchapp.CodeDiagnosisRefreshResult{}, nil
		}
		return a.local.RefreshCodeDiagnosis(ctx, orderNo, email, emailResourceID)
	}
	if a.upstreams == nil {
		return mailmatchapp.CodeDiagnosisRefreshResult{}, nil
	}
	pickup, handled, err := a.upstreams.Pickup(ctx, upstream.PickupRequest{OrderNo: orderNo, Email: email})
	result := mailmatchapp.CodeDiagnosisRefreshResult{}
	if handled && pickup != nil && len(pickup.Codes) > 0 {
		result.DeliveryFound = true
		for _, code := range pickup.Codes {
			if code.ReceivedAt.After(result.ReceivedAt) {
				result.ReceivedAt = code.ReceivedAt
			}
		}
	}
	return result, err
}

func (a gmailPickupAdapter) ReadUpstreamPickup(ctx context.Context, email, tokenPlain string) ([]mailmatchdomain.MailContent, bool, error) {
	_, host, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || !strings.EqualFold(host, "gmail.com") {
		return nil, false, nil
	}
	if a.tokens == nil {
		return nil, false, nil
	}
	token, err := a.tokens.FindOrderTokenByPlain(ctx, tokenPlain)
	if err != nil {
		return nil, false, mailmatchdomain.ErrPickupCredentialInvalid
	}
	upstreamPickup, handled, err := a.upstreams.Pickup(ctx, upstream.PickupRequest{OrderNo: token.OrderNo, Email: email})
	if errors.Is(err, upstream.ErrPickupInvalid) {
		return nil, true, mailmatchdomain.ErrPickupCredentialInvalid
	}
	if err != nil || !handled {
		return nil, handled, err
	}
	if upstreamPickup == nil {
		return []mailmatchdomain.MailContent{}, true, nil
	}
	items := make([]mailmatchdomain.MailContent, len(upstreamPickup.Codes))
	for i := range upstreamPickup.Codes {
		items[i] = mailmatchdomain.MailContent{
			Recipient: upstreamPickup.Email, ReceivedAt: upstreamPickup.Codes[i].ReceivedAt,
			VerificationCode: upstreamPickup.Codes[i].Value,
		}
	}
	return items, true, nil
}

type gmailDeliveryReader interface {
	ListGmailDeliveries(ctx context.Context, orderNos []string) (map[string]tradeapp.GmailDeliverySummary, error)
}

type smsbowerDeliveryReader interface {
	ListDeliveries(ctx context.Context, orderNos []string) (map[string]upstream.PickupResult, error)
}

type gmailDeliveryComposite struct {
	gmail    gmailDeliveryReader
	smsbower smsbowerDeliveryReader
}

func (c gmailDeliveryComposite) ListGmailDeliveries(ctx context.Context, orders []tradeapp.GmailDeliveryOrder) (map[string]tradeapp.GmailDeliverySummary, error) {
	orderNos := make([]string, 0, len(orders))
	for _, order := range orders {
		orderNos = append(orderNos, order.OrderNo)
	}
	result, err := c.gmail.ListGmailDeliveries(ctx, orderNos)
	if err != nil {
		return nil, err
	}
	upstreamOrderNos := make([]string, 0, len(orders))
	for _, order := range orders {
		if order.ProductType != tradedomain.ProductTypeGmail {
			continue
		}
		if _, local := result[order.OrderNo]; !local {
			upstreamOrderNos = append(upstreamOrderNos, order.OrderNo)
		}
	}
	if len(upstreamOrderNos) == 0 {
		return result, nil
	}
	upstreamDeliveries, err := c.smsbower.ListDeliveries(ctx, upstreamOrderNos)
	if err != nil {
		return nil, err
	}
	for orderNo, delivery := range upstreamDeliveries {
		codes := make([]tradeapp.GmailCode, len(delivery.Codes))
		for i := range delivery.Codes {
			codes[i] = tradeapp.GmailCode{
				Seq: delivery.Codes[i].Seq, Code: delivery.Codes[i].Value, ReceivedAt: delivery.Codes[i].ReceivedAt,
			}
		}
		result[orderNo] = tradeapp.GmailDeliverySummary{Codes: codes}
	}
	return result, nil
}
