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

func (r *codeDiagnosisRepoStub) LookupCodeDiagnosis(context.Context, uint, string) (CodeDiagnosisLookup, error) {
	index := min(r.calls, len(r.lookups)-1)
	r.calls++
	return r.lookups[index], nil
}

type codeDiagnosisRefreshStub struct {
	calls      int
	orderNo    string
	email      string
	resourceID uint
	result     CodeDiagnosisRefreshResult
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

type diagnosisGmailFetchStub struct {
	calls int
}

func (f *diagnosisGmailFetchStub) FetchLocalOrderMailWithFence(context.Context, string, func(context.Context) error) error {
	f.calls++
	return nil
}

func (r *codeDiagnosisRefreshStub) RefreshCodeDiagnosis(_ context.Context, orderNo, email string, resourceID uint) (CodeDiagnosisRefreshResult, error) {
	r.calls++
	r.orderNo, r.email, r.resourceID = orderNo, email, resourceID
	return r.result, nil
}

func TestBotCodeDiagnosisReturnsProjectResolvedFromTheUsersOrder(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	active := CodeDiagnosisOrderFact{
		OrderNo: "ORDER-1", ProjectID: 10, ProjectName: "GitHub",
		ServiceMode: "code", Status: "active", EmailResourceID: 8,
	}
	repo := &codeDiagnosisRepoStub{lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{active}}}}
	refresh := &codeDiagnosisRefreshStub{}
	service := NewBotDiagnosisService(repo, refresh)
	service.now = func() time.Time { return now }

	result, err := service.DiagnoseCode(context.Background(), 2, "private@example.com")

	require.NoError(t, err)
	require.Equal(t, "cause_not_confirmed", result.Result)
	require.Equal(t, uint(10), result.ProjectID)
	require.Equal(t, "GitHub", result.ProjectName)
	require.Equal(t, 1, refresh.calls)
	require.Equal(t, "private@example.com", refresh.email)
	for _, internal := range []string{"pickup", "缓存", "凭证", "拉取"} {
		require.NotContains(t, result.Reason+result.Action, internal)
	}
}

func TestBotCodeDiagnosisUsesDeliveryFactsForBothServiceModes(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	oldMail := now.Add(-time.Minute)
	newMail := now.Add(-30 * time.Second)
	active := CodeDiagnosisOrderFact{OrderNo: "ORDER-1", ServiceMode: "code", Status: "active", EmailResourceID: 8}
	tests := []struct {
		name          string
		lookups       []CodeDiagnosisLookup
		want          string
		refreshCalls  int
		refreshResult CodeDiagnosisRefreshResult
	}{
		{name: "resource abnormal and refunded", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", ResourceAbnormalRefunded: true}}}}, want: "resource_abnormal_refunded"},
		{name: "matched for one minute", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", DeliveryStoredAt: &oldMail}}}}, want: "pickup_not_requested"},
		{name: "purchased mailbox matched for one minute", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "purchase", DeliveryStoredAt: &oldMail}}}}, want: "pickup_not_requested"},
		{name: "matched inside grace", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{ServiceMode: "code", DeliveryStoredAt: &newMail}}}}, want: "pickup_grace_period"},
		{name: "purchase can also receive mail", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{{OrderNo: "ORDER-P", ServiceMode: "purchase", Status: "completed", EmailResourceID: 9}}}}, want: "cause_not_confirmed", refreshCalls: 1},
		{
			name: "cache miss refreshes once then sees mail",
			lookups: []CodeDiagnosisLookup{
				{Orders: []CodeDiagnosisOrderFact{active}},
				{Orders: []CodeDiagnosisOrderFact{{OrderNo: "ORDER-1", ServiceMode: "code", Status: "active", EmailResourceID: 8, DeliveryStoredAt: &oldMail}}},
			},
			want: "pickup_not_requested", refreshCalls: 1,
		},
		{name: "no confirmed cause", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{active}}}, want: "cause_not_confirmed", refreshCalls: 1},
		{
			name: "refresh result reports a delivered code", lookups: []CodeDiagnosisLookup{{Orders: []CodeDiagnosisOrderFact{active}}},
			want: "pickup_not_requested", refreshCalls: 1,
			refreshResult: CodeDiagnosisRefreshResult{DeliveryFound: true, ReceivedAt: oldMail},
		},
	}
	causes := map[string]bool{"pickup_not_requested": true, "resource_abnormal_refunded": true}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &codeDiagnosisRepoStub{lookups: test.lookups}
			refresh := &codeDiagnosisRefreshStub{result: test.refreshResult}
			service := NewBotDiagnosisService(repo, refresh)
			service.now = func() time.Time { return now }

			result, err := service.DiagnoseCode(context.Background(), 2, "user@example.com")

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
	_, err := domainUseCase.RefreshCodeDiagnosis(context.Background(), "ORDER-1", "user@example.com", 8)
	require.NoError(t, err)
	require.Equal(t, 1, domainRepo.domainReads)

	gmailRepo := &diagnosisRefreshRepoStub{scope: base}
	gmailRepo.scope.AllocationType = "gmail"
	gmailFetch := &diagnosisGmailFetchStub{}
	gmailUseCase := NewUseCase(gmailRepo, nil, nil, nil)
	gmailUseCase.SetGmailMailFetchPort(gmailFetch)
	gmailUseCase.now = func() time.Time { return now }
	_, err = gmailUseCase.RefreshCodeDiagnosis(context.Background(), "ORDER-1", "user@example.com", 8)
	require.NoError(t, err)
	require.Equal(t, 1, gmailFetch.calls)

	gmailRepo.scope.ServiceMode = "purchase"
	gmailRepo.scope.OrderStatus = "completed"
	gmailRepo.scope.ReceiveUntil = nil
	_, err = gmailUseCase.RefreshCodeDiagnosis(context.Background(), "ORDER-1", "user@example.com", 8)
	require.NoError(t, err)
	require.Equal(t, 2, gmailFetch.calls)
}
