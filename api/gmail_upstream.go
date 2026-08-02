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
)

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

type smsbowerAlertMailer struct {
	users    announcementRecipientSource
	delivery mailapp.DeliveryPort
}

func (m smsbowerAlertMailer) NotifySMSBower(ctx context.Context, alert gmail.Alert) error {
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
	service *gmail.Service
	tokens  *openapiapp.UseCase
}

func (a gmailPickupAdapter) ReadCodeOnlyPickup(ctx context.Context, email, tokenPlain string) (*mailmatchapi.CodeOnlyPickupResult, bool, error) {
	if a.service == nil || a.tokens == nil {
		return nil, false, nil
	}
	token, err := a.tokens.FindOrderTokenByPlain(ctx, tokenPlain)
	if err != nil {
		return nil, false, mailmatchdomain.ErrPickupCredentialInvalid
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
