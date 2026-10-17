package app

import (
	"context"
	"errors"
	"strings"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
)

type AuxiliaryPickupBinding struct {
	OwnerUserID uint
	ResourceID  uint
	OrderNo     string
	Email       string
	Token       string `json:"-"`
}
type AuxiliaryPickupScopeReader interface {
	LoadPickupScope(context.Context, string, string) (*mailmatchapp.OrderScope, error)
	ListOrderMessagePage(context.Context, mailmatchapp.OrderScope, mailmatchapp.AdminMessageListQuery) ([]mailmatchapp.AdminMessageSummary, int64, bool, error)
}
type AuxiliaryPickupMailReader interface {
	ListPickupMail(context.Context, string, string) ([]mailmatchdomain.MailContent, *mailmatchdomain.FetchState, error)
	GetPickupMessage(context.Context, string, string, uint) (*mailmatchdomain.MailContent, error)
}

func (s *AuxiliaryMailQueryService) SetDaumPickupSource(scopes AuxiliaryPickupScopeReader, messages AuxiliaryPickupMailReader) {
	s.pickupScopes = scopes
	s.pickupMail = messages
}

func (s *AuxiliaryMailQueryService) daumPickupScope(ctx context.Context, id uint) (*AuxiliaryPickupBinding, *mailmatchapp.OrderScope, bool, error) {
	repo, ok := s.repo.(interface {
		DaumPickupBinding(context.Context, uint) (*AuxiliaryPickupBinding, bool, error)
	})
	if !ok {
		return nil, nil, false, nil
	}
	binding, handled, err := repo.DaumPickupBinding(ctx, id)
	if err != nil || !handled || binding == nil {
		return nil, nil, handled, err
	}
	if s.pickupScopes == nil || s.pickupMail == nil {
		return nil, nil, true, domain.ErrAuxiliaryMailUnavailable
	}
	scope, err := s.pickupScopes.LoadPickupScope(ctx, binding.Token, binding.Email)
	if errors.Is(err, mailmatchdomain.ErrPickupCredentialInvalid) || errors.Is(err, mailmatchdomain.ErrOrderUnavailable) {
		return nil, nil, true, nil
	}
	if err != nil {
		return nil, nil, true, err
	}
	if scope == nil || scope.UserID != binding.OwnerUserID || scope.OrderNo != binding.OrderNo || scope.EmailResourceID != binding.ResourceID || !strings.EqualFold(scope.Recipient, binding.Email) || (scope.OrderStatus != "active" && scope.OrderStatus != "completed") || (scope.ServiceMode != "purchase" && scope.ReceiveUntil != nil && !scope.ReceiveUntil.After(time.Now())) {
		return nil, nil, true, nil
	}
	return binding, scope, true, nil
}
func (s *AuxiliaryMailQueryService) listDaumPickup(ctx context.Context, filter AuxiliaryMailFilter) (*AuxiliaryMailPage, bool, error) {
	binding, _, handled, err := s.daumPickupScope(ctx, filter.ResourceID)
	if err != nil || !handled {
		return nil, handled, err
	}
	page := &AuxiliaryMailPage{Items: []AuxiliaryMessageSummary{}, TotalIncluded: !filter.SkipTotal, Offset: filter.Offset, Limit: filter.Limit}
	if filter.SkipTotal {
		page.Total = -1
	}
	if binding == nil {
		return page, true, nil
	}
	_, _, err = s.pickupMail.ListPickupMail(ctx, binding.Token, binding.Email)
	if errors.Is(err, mailmatchdomain.ErrPickupCredentialInvalid) || errors.Is(err, mailmatchdomain.ErrOrderUnavailable) {
		return page, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	current, scope, _, err := s.daumPickupScope(ctx, filter.ResourceID)
	if err != nil {
		return nil, true, err
	}
	if current == nil || *current != *binding {
		return page, true, nil
	}
	if scope.ServiceMode == "code" && scope.OrderStatus != "completed" {
		return page, true, nil
	}
	rows, total, hasMore, err := s.pickupScopes.ListOrderMessagePage(ctx, *scope, mailmatchapp.AdminMessageListQuery{Search: filter.Search, Offset: filter.Offset, Limit: filter.Limit, BeforeReceivedAt: filter.BeforeReceivedAt, BeforeID: filter.BeforeID, SkipTotal: filter.SkipTotal})
	if err != nil {
		return nil, true, err
	}
	current, currentScope, _, err := s.daumPickupScope(ctx, filter.ResourceID)
	if err != nil {
		return nil, true, err
	}
	if current == nil || *current != *binding || !sameAuxiliaryWindow(*scope, *currentScope) {
		return page, true, nil
	}
	page.Total, page.HasMore = total, hasMore
	for _, row := range rows {
		page.Items = append(page.Items, AuxiliaryMessageSummary{ID: row.ID, Recipient: row.Recipient, Sender: row.Sender, Subject: row.Subject, Preview: row.Preview, Status: AuxiliaryMessageMatched, VerificationCode: row.VerificationCode, OrderNo: &binding.OrderNo, ReceivedAt: row.ReceivedAt})
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextBeforeID = last.ID
		page.NextBeforeReceivedAt = &last.ReceivedAt
	}

	return page, true, nil
}
func (s *AuxiliaryMailQueryService) getDaumPickup(ctx context.Context, req AuxiliaryMailDetailRequest) (*AuxiliaryMessageDetail, bool, error) {
	binding, scope, handled, err := s.daumPickupScope(ctx, req.ResourceID)
	if err != nil || !handled || binding == nil {
		return nil, handled, err
	}
	message, err := s.pickupMail.GetPickupMessage(ctx, binding.Token, binding.Email, req.MessageID)
	if errors.Is(err, mailmatchdomain.ErrPickupCredentialInvalid) || errors.Is(err, mailmatchdomain.ErrOrderUnavailable) || errors.Is(err, mailmatchdomain.ErrMessageNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	if message == nil {
		return nil, true, nil
	}
	current, currentScope, _, err := s.daumPickupScope(ctx, req.ResourceID)
	if err != nil {
		return nil, true, err
	}
	if current == nil || *current != *binding || !sameAuxiliaryWindow(*scope, *currentScope) {
		return nil, true, nil
	}
	start, end := mailmatchapp.OrderReadWindow(*scope, time.Now())
	if message.ReceivedAt.Before(start) || message.ReceivedAt.After(end) {
		return nil, true, nil
	}
	return &AuxiliaryMessageDetail{AuxiliaryMessageSummary: pickupAuxiliarySummary(*message, binding.OrderNo), Body: message.Body}, true, nil
}
func pickupAuxiliarySummary(message mailmatchdomain.MailContent, orderNo string) AuxiliaryMessageSummary {
	var code *string
	if message.VerificationCode != "" {
		value := message.VerificationCode
		code = &value
	}
	return AuxiliaryMessageSummary{ID: message.ID, Recipient: message.Recipient, Sender: message.Sender, Subject: message.Subject, Preview: message.BodyPreview, Status: AuxiliaryMessageMatched, VerificationCode: code, OrderNo: &orderNo, ReceivedAt: message.ReceivedAt}
}

func sameAuxiliaryWindow(a, b mailmatchapp.OrderScope) bool {
	if a.ServiceMode != b.ServiceMode || a.OrderStatus != b.OrderStatus {
		return false
	}
	for _, pair := range [][2]*time.Time{{a.ReceiveStartedAt, b.ReceiveStartedAt}, {a.ReceiveUntil, b.ReceiveUntil}} {
		if (pair[0] == nil) != (pair[1] == nil) {
			return false
		}
		if pair[0] != nil && !pair[0].Equal(*pair[1]) {
			return false
		}
	}
	return true
}
