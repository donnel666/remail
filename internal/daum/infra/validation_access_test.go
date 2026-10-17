package infra

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestValidationUsesStoredAPPAndBoundsTransportFailuresBeforeHistory(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "private-account@daum.net", Password: "fixture-login", AppPassword: "fixture-app"})
	require.NoError(t, err)
	proxies := &daumNetworkProxies{}
	s.Proxies = proxies
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	reachable := false
	calls := 0
	s.Protocol = daumValidationClientStub{validate: func(_ context.Context, credentials protocol.Credentials) error {
		calls++
		require.Equal(t, "private-account@daum.net", credentials.Email)
		require.Equal(t, "fixture-app", credentials.AppPassword)
		require.Equal(t, "imap", credentials.Protocol)
		if reachable {
			return nil
		}
		return &protocol.Failure{Category: "transport", SafeMessage: "Daum mailbox connection is temporarily unavailable.", Stage: "connect_tls", Retryable: true,
			Cause: errors.New("socks connect private-proxy-user:private-proxy-secret: unknown error network unreachable")}
	}}
	for attempt := 1; attempt <= 3; attempt++ {
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.NoError(t, s.ProcessValidation(ctx, daumapp.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, CredentialRevision: row.CredentialRevision, ValidationGeneration: row.ValidationGeneration, RequestID: "validation-diagnostic"}))
		row, err = s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, attempt, row.ValidationFailures)
		require.Empty(t, row.FailureCategory, "transport failure does not become a permanent credential rejection")
		require.Contains(t, row.LastSafeError, "stage=connect_tls; reason=network_unreachable")
		if attempt < 3 {
			require.Equal(t, domain.StatusPending, row.Status)
		} else {
			require.Equal(t, domain.StatusAbnormal, row.Status)
		}
		var history int64
		require.NoError(t, s.DB.Model(&MaintenanceRun{}).Where("resource_id=? AND kind='history'", id).Count(&history).Error)
		require.Zero(t, history, "history only follows proven mailbox access")
	}
	require.Equal(t, 9, calls, "three validation rounds each honor the three-proxy attempt budget")
	for i, request := range proxies.requests {
		require.Equal(t, i%3, request.Attempt)
		require.Equal(t, "validation-diagnostic", request.RequestID)
		require.Equal(t, proxydomain.ProxyIPv4, request.IPVersion)
		require.Equal(t, proxydomain.ProxyPurposeAuth, request.Purpose)
		require.True(t, request.AllowSystemFallback)
		require.Len(t, request.AvoidProxyServerIDs, i%3)
	}
	queued, err := s.DispatchPendingValidations(ctx, &daumQueueStub{}, 10)
	require.NoError(t, err)
	require.Zero(t, queued, "exhausted validation must not retry forever")
	for _, secret := range []string{"private-account", "fixture-login", "fixture-app", "private-proxy-user", "private-proxy-secret", "proxy.fixture"} {
		require.NotContains(t, logs.String(), secret)
	}
	require.Contains(t, logs.String(), `"request_id":"validation-diagnostic"`)
	require.Contains(t, logs.String(), `"reason":"network_unreachable"`)
	reachable = true
	generation, err := s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(ctx, daumapp.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, CredentialRevision: 1, ValidationGeneration: generation}))
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, row.Status, "read access succeeds before historical project matching")
	history, err := s.FindMaintenanceRun(ctx, id, generation, maintenanceKindHistory)
	require.NoError(t, err)
	require.Equal(t, maintenanceQueued, history.Status)
}

func TestDaumValidationUsesConfiguredProxyAttemptBudget(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()["max_proxy_attempts"]
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set("max_proxy_attempts", previous)
		} else {
			runtimeconfig.Delete("max_proxy_attempts")
		}
	})
	for _, budget := range []int{1, 2, 4} {
		runtimeconfig.Set("max_proxy_attempts", fmt.Sprint(budget))
		proxies := &daumNetworkProxies{}
		s := &Service{Proxies: proxies, Protocol: daumValidationClientStub{validate: func(context.Context, protocol.Credentials) error {
			return &protocol.Failure{Category: "transport", SafeMessage: "Daum mailbox connection is temporarily unavailable.", Retryable: true}
		}}}
		require.Error(t, s.validateProtocol(context.Background(), Resource{ID: 7, EmailAddress: "fixture@daum.net", AppPassword: "fixture"}, "attempt-budget"))
		require.Len(t, proxies.requests, budget)
		for attempt, request := range proxies.requests {
			require.Equal(t, attempt, request.Attempt)
			require.Len(t, request.AvoidProxyServerIDs, attempt)
			require.Equal(t, "attempt-budget", request.RequestID)
		}
	}
}
