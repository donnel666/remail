package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestPickupReloadsCodeCompletionAfterSynchronousMatching(t *testing.T) {
	for _, resourceType := range []domain.ResourceType{domain.ResourceTypeDomain, domain.ResourceTypeMicrosoft} {
		for _, test := range []struct {
			name   string
			status string
			err    error
		}{
			{name: "completed", status: "completed"},
			{name: "notification pending", status: "active"},
			{name: "refunded", status: "refunded"},
			{name: "reload failed", status: "completed", err: errors.New("scope reload failed")},
		} {
			t.Run(string(resourceType)+"/"+test.name, func(t *testing.T) {
				now := time.Now().UTC()
				scope := OrderScope{
					OrderID: 42, OrderNo: "OR_COMPLETION", EmailResourceID: 9, AllocationID: 1,
					AllocationType: resourceType, Recipient: "user@example.com", RecipientKind: "exact",
					ServiceMode: "code", OrderStatus: "active", LooseMatch: true,
					Rules: []MailRule{
						{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
						{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
					},
				}
				messages := []FetchedMessage{{
					EmailResourceID: 9, ResourceType: resourceType, Recipient: scope.Recipient,
					Sender: "sender@example.net", Body: "Your code is 123456", ReceivedAt: now,
				}}
				repo := &pickupCompletionRepoStub{
					domainMailboxRepoStub: &domainMailboxRepoStub{
						matchingRepoStub: &matchingRepoStub{scopes: []OrderScope{scope}}, messages: messages,
					},
					scope: scope, statusAfterMatch: test.status, reloadErr: test.err,
				}
				uc := NewUseCase(repo, nil, nil, repo)
				uc.SetPickupMessageCachePort(&pickupMessageCacheStub{found: true, messages: messages})
				uc.now = func() time.Time { return now }

				items, _, err := uc.ListPickupMail(context.Background(), "token", scope.Recipient)

				require.NotNil(t, repo.purchaseDelivery)
				require.Equal(t, 1, repo.reloads)
				switch {
				case test.err != nil:
					require.ErrorIs(t, err, test.err)
					require.Empty(t, items)
				case test.status == "refunded":
					require.ErrorIs(t, err, domain.ErrOrderUnavailable)
					require.Empty(t, items)
				case test.status == "completed":
					require.NoError(t, err)
					require.Len(t, items, 1)
					require.Equal(t, "123456", items[0].VerificationCode)
					require.Equal(t, messages[0].Body, items[0].Body)
				default:
					require.NoError(t, err)
					require.Empty(t, items)
				}
			})
		}
	}
}

type pickupCompletionRepoStub struct {
	*domainMailboxRepoStub
	scope            OrderScope
	statusAfterMatch string
	reloadErr        error
	reloads          int
}

func (r *pickupCompletionRepoStub) LoadPickupScope(context.Context, string, string) (*OrderScope, error) {
	scope := r.scope
	return &scope, nil
}

func (r *pickupCompletionRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*OrderScope, error) {
	r.reloads++
	scope := r.scope
	return &scope, r.reloadErr
}

func (r *pickupCompletionRepoStub) NotifyMatchedCode(context.Context, MatchResult) error {
	r.scope.OrderStatus = r.statusAfterMatch
	return nil
}
