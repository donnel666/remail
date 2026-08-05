package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/gmail"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/donnel666/remail/internal/smsbower"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
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

type gmailInventoryOverlay struct {
	gmail *gmail.Service
}

func (o gmailInventoryOverlay) OverlayProductInventory(ctx context.Context, projectIDs []uint, snapshots map[uint]*allocapp.ProjectProductInventoryTotals) error {
	items, err := o.gmail.ListInventory(ctx, projectIDs)
	if err != nil {
		return err
	}
	overlayGmailInventory(snapshots, items)
	return nil
}

func overlayGmailInventory(snapshots map[uint]*allocapp.ProjectProductInventoryTotals, items []gmail.InventoryItem) {
	for _, upstream := range items {
		snapshot := snapshots[upstream.ProjectID]
		if snapshot == nil {
			snapshot = &allocapp.ProjectProductInventoryTotals{ProjectID: upstream.ProjectID}
			snapshots[upstream.ProjectID] = snapshot
		}
		found := false
		for i := range snapshot.Items {
			if snapshot.Items[i].ProductID != upstream.ProductID {
				continue
			}
			previous := snapshot.Items[i].TotalAvailable
			applyGmailModeInventory(&snapshot.Items[i], upstream)
			snapshot.TotalAvailable = max(0, snapshot.TotalAvailable+snapshot.Items[i].TotalAvailable-previous)
			found = true
			break
		}
		if !found {
			item := allocapp.ProductInventoryTotal{ProductID: upstream.ProductID}
			applyGmailModeInventory(&item, upstream)
			snapshot.Items = append(snapshot.Items, item)
			snapshot.TotalAvailable += item.TotalAvailable
		}
	}
}

func applyGmailModeInventory(item *allocapp.ProductInventoryTotal, upstream gmail.InventoryItem) {
	code, purchase := upstream.CodeAvailable, upstream.PurchaseAvailable
	item.CodeAvailable, item.CodePublicAvailable = &code, &code
	item.PurchaseAvailable, item.PurchasePublicAvailable = &purchase, &purchase
	item.TotalAvailable = max(code, purchase)
	item.PublicAvailable = item.TotalAvailable
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
			previous := snapshot.Items[i].TotalAvailable
			addSMSBowerInventory(&snapshot.Items[i], upstreamItem.CodeAvailable)
			snapshot.TotalAvailable = max(0, snapshot.TotalAvailable+snapshot.Items[i].TotalAvailable-previous)
			found = true
			break
		}
		if !found {
			item := allocapp.ProductInventoryTotal{ProductID: upstreamItem.ProductID}
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

type gmailPickupAdapter struct {
	service   *gmail.Service
	upstreams *upstream.Router
	tokens    *openapiapp.UseCase
}

func (a gmailPickupAdapter) ReadCodeOnlyPickup(ctx context.Context, email, tokenPlain string) (*mailmatchapi.CodeOnlyPickupResult, bool, error) {
	if a.service == nil || a.tokens == nil {
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
	if err != nil || handled {
		if err != nil || upstreamPickup == nil {
			return nil, handled, err
		}
		codes := make([]mailmatchapi.CodeOnlyPickupCode, len(upstreamPickup.Codes))
		for i := range upstreamPickup.Codes {
			codes[i] = mailmatchapi.CodeOnlyPickupCode{
				Seq: upstreamPickup.Codes[i].Seq, Code: upstreamPickup.Codes[i].Value, ReceivedAt: upstreamPickup.Codes[i].ReceivedAt,
			}
		}
		return &mailmatchapi.CodeOnlyPickupResult{
			Email: upstreamPickup.Email, Codes: codes, ReceivedCount: upstreamPickup.ReceivedCount,
			MaxCodes: upstreamPickup.MaxCodes, ExpiresAt: upstreamPickup.ExpiresAt,
		}, true, nil
	}
	pickup, matched, err := a.service.PickupByOrder(ctx, token.OrderNo, email)
	if errors.Is(err, gmail.ErrPickupInvalid) {
		return nil, true, mailmatchdomain.ErrPickupCredentialInvalid
	}
	if err != nil || !matched {
		return nil, matched, err
	}
	codes := make([]mailmatchapi.CodeOnlyPickupCode, len(pickup.Codes))
	for i := range pickup.Codes {
		codes[i] = mailmatchapi.CodeOnlyPickupCode{
			Seq: pickup.Codes[i].Seq, Code: pickup.Codes[i].Code, ReceivedAt: pickup.Codes[i].ReceivedAt,
		}
	}
	return &mailmatchapi.CodeOnlyPickupResult{
		Email: pickup.Email, Codes: codes, ReceivedCount: pickup.ReceivedCount,
		MaxCodes: pickup.MaxCodes, ExpiresAt: pickup.ExpiresAt,
	}, true, nil
}

type gmailDeliveryComposite struct {
	gmail    *gmail.Service
	smsbower *smsbower.Service
}

func (c gmailDeliveryComposite) ListGmailDeliveries(ctx context.Context, orderNos []string) (map[string]tradeapp.GmailDeliverySummary, error) {
	result, err := c.gmail.ListGmailDeliveries(ctx, orderNos)
	if err != nil {
		return nil, err
	}
	upstreamDeliveries, err := c.smsbower.ListDeliveries(ctx, orderNos)
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
		result[orderNo] = tradeapp.GmailDeliverySummary{
			Codes: codes, ReceivedCount: delivery.ReceivedCount, MaxCodes: delivery.MaxCodes, ExpiresAt: delivery.ExpiresAt,
		}
	}
	return result, nil
}
