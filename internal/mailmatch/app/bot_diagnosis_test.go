package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codeDiagnosisRepoStub struct {
	lookups []CodeDiagnosisLookup
	calls   int
}

func (r *codeDiagnosisRepoStub) LookupCodeDiagnosis(context.Context, uint, string, uint) (CodeDiagnosisLookup, error) {
	index := min(r.calls, len(r.lookups)-1)
	r.calls++
	return r.lookups[index], nil
}

type codeDiagnosisRefreshStub struct {
	calls      int
	orderNo    string
	resourceID uint
}

type diagnosisRefreshRepoStub struct {
	Repository
	scope       OrderScope
	domainReads int
}

func (r *diagnosisRefreshRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*OrderScope, error) {
	scope := r.scope
	return &scope, nil
}

func (r *diagnosisRefreshRepoStub) ListDomainMailboxMessages(context.Context, OrderScope, time.Time, time.Time, int) ([]FetchedMessage, error) {
	r.domainReads++
	return nil, nil
}

type diagnosisGmailFetchStub struct{ codeCalls int }

func (*diagnosisGmailFetchStub) FetchLocalPurchaseMail(context.Context, string) error { return nil }
func (f *diagnosisGmailFetchStub) FetchLocalCodeMail(context.Context, string) error {
	f.codeCalls++
	return nil
}

func (r *codeDiagnosisRefreshStub) RefreshCodeDiagnosis(_ context.Context, orderNo, _ string, resourceID uint) error {
	r.calls++
	r.orderNo, r.resourceID = orderNo, resourceID
	return nil
}

func TestBotCodeDiagnosisUsesExistingPickupFactsAndOnlyThreeCauses(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	oldMail := now.Add(-time.Minute)
	newMail := now.Add(-30 * time.Second)
	active := CodeDiagnosisOrderFact{OrderNo: "ORDER-1", ServiceMode: "code", Status: "active", EmailResourceID: 8}
	tests := []struct {
		name         string
		lookups      []CodeDiagnosisLookup
		want         string
		refreshCalls int
	}{
		{name: "project mismatch", lookups: []CodeDiagnosisLookup{{EmailOrderExists: true}}, want: "project_mismatch"},
		{name: "resource abnormal and refunded", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", ResourceAbnormalRefunded: true}}}}, want: "resource_abnormal_refunded"},
		{name: "matched for one minute", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", DeliveryStoredAt: &oldMail}}}}, want: "pickup_not_requested"},
		{name: "matched inside grace", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", DeliveryStoredAt: &newMail}}}}, want: "pickup_grace_period"},
		{name: "purchase is wrong project mode", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "purchase"}}}}, want: "project_mismatch"},
		{
			name: "cache miss refreshes once then sees mail",
			lookups: []CodeDiagnosisLookup{
				{Orders: []CodeDiagnosisOrderFact{active}},
				{Orders: []CodeDiagnosisOrderFact{{OrderNo: "ORDER-1", ServiceMode: "code", Status: "active", EmailResourceID: 8, DeliveryStoredAt: &oldMail}}},
			},
			want: "pickup_not_requested", refreshCalls: 1,
		},
		{name: "no confirmed cause", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{active}}}, want: "cause_not_confirmed", refreshCalls: 1},
	}
	causes := map[string]bool{
		"project_mismatch": true, "pickup_not_requested": true, "resource_abnormal_refunded": true,
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &codeDiagnosisRepoStub{lookups: test.lookups}
			refresh := &codeDiagnosisRefreshStub{}
			service := NewBotDiagnosisService(repo, refresh)
			service.now = func() time.Time { return now }

			result, err := service.DiagnoseCode(context.Background(), 2, "user@example.com", 10)

			require.NoError(t, err)
			require.Equal(t, test.want, result.Result)
			require.NotEmpty(t, result.Reason)
			require.NotEmpty(t, result.Action)
			require.Equal(t, test.refreshCalls, refresh.calls)
			if causes[result.Result] {
				require.Contains(t, causes, result.Result)
			}
		})
	}
}

func TestRefreshCodeDiagnosisUsesDomainAndGmailOwnedFetchPaths(t *testing.T) {
	now := time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC)
	until := now.Add(time.Minute)
	base := OrderScope{
		OrderID: 1, OrderNo: "ORDER-1", EmailResourceID: 8, Recipient: "user@example.com",
		ServiceMode: "code", OrderStatus: "active", ReceiveUntil: &until,
	}

	domainRepo := &diagnosisRefreshRepoStub{scope: base}
	domainRepo.scope.AllocationType = "domain"
	domainUseCase := NewUseCase(domainRepo, nil, nil, nil)
	domainUseCase.now = func() time.Time { return now }
	require.NoError(t, domainUseCase.RefreshCodeDiagnosis(context.Background(), "ORDER-1", "user@example.com", 8))
	require.Equal(t, 1, domainRepo.domainReads)

	gmailRepo := &diagnosisRefreshRepoStub{scope: base}
	gmailRepo.scope.AllocationType = "gmail"
	gmailFetch := &diagnosisGmailFetchStub{}
	gmailUseCase := NewUseCase(gmailRepo, nil, nil, nil)
	gmailUseCase.SetGmailPurchaseFetchPort(gmailFetch)
	gmailUseCase.now = func() time.Time { return now }
	require.NoError(t, gmailUseCase.RefreshCodeDiagnosis(context.Background(), "ORDER-1", "user@example.com", 8))
	require.Equal(t, 1, gmailFetch.codeCalls)
}
