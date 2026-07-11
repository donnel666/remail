package api

import (
	"context"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAliasAdapterRotatesAfterPartialProxyFailure(t *testing.T) {
	calls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		calls++
		if calls == 1 {
			return msacl.ExplicitAliasResult{
				Aliases:      []string{"first123456@outlook.com"},
				Attempted:    []string{"first123456@outlook.com", "second123456@outlook.com"},
				Category:     "request",
				ProxyFailure: true,
			}, nil
		}
		return msacl.ExplicitAliasResult{
			Aliases: []string{"first123456@outlook.com", "second123456@outlook.com"},
		}, nil
	}

	result, err := adapter.CreateMicrosoftAliases(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress: "owner@example.com",
		Password:     "secret",
		Candidates: []string{
			"first123456@outlook.com",
			"second123456@outlook.com",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, []string{
		"first123456@outlook.com",
		"second123456@outlook.com",
	}, result.Aliases)
	require.Equal(t, []string{
		"first123456@outlook.com",
		"second123456@outlook.com",
	}, result.Attempted)
	require.False(t, result.ProxyFailure)
}

func TestMicrosoftAliasAdapterPreservesEarlierUncertainCandidateAcrossLaterLoginFailure(t *testing.T) {
	calls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		calls++
		if calls == 1 {
			return msacl.ExplicitAliasResult{
				Attempted:    []string{"first123456@outlook.com"},
				Category:     "request",
				ProxyFailure: true,
			}, nil
		}
		return msacl.ExplicitAliasResult{
			Category:    "mfa",
			SafeMessage: "Microsoft account requires additional verification.",
		}, nil
	}

	result, err := adapter.CreateMicrosoftAliases(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress: "owner@example.com",
		Password:     "secret",
		Candidates:   []string{"first123456@outlook.com"},
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Uncertain)
	require.Equal(t, "mfa", result.Category)
}

func TestMicrosoftAliasAdapterUsesReadOnlyReconciliationForUncertainCandidates(t *testing.T) {
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	createCalls := 0
	reconcileCalls := 0
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		createCalls++
		return msacl.ExplicitAliasResult{}, nil
	}
	adapter.reconcileAliases = func(_ context.Context, _ string, _ string, _ string, _ string, candidates []string) (msacl.ExplicitAliasResult, error) {
		reconcileCalls++
		return msacl.ExplicitAliasResult{
			Absent:   append([]string(nil), candidates...),
			Category: "alias_failed",
		}, nil
	}

	result, err := adapter.CreateMicrosoftAliases(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress:  "owner@example.com",
		Password:      "secret",
		Candidates:    []string{"first123456@outlook.com"},
		ReconcileOnly: true,
	})

	require.NoError(t, err)
	require.Zero(t, createCalls)
	require.Equal(t, 1, reconcileCalls)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Absent)
	require.Empty(t, result.Attempted)
}
