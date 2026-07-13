package api

import (
	"context"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAliasAdapterStopsAfterPostSideEffectBecomesUncertain(t *testing.T) {
	calls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		calls++
		return msacl.ExplicitAliasResult{
			Aliases:      []string{"first123456@outlook.com"},
			Attempted:    []string{"first123456@outlook.com", "second123456@outlook.com"},
			Category:     "request",
			ProxyFailure: true,
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
	require.Equal(t, 1, calls)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Aliases)
	require.Equal(t, []string{
		"first123456@outlook.com",
		"second123456@outlook.com",
	}, result.Attempted)
	require.Equal(t, []string{"second123456@outlook.com"}, result.Uncertain)
	require.True(t, result.ProxyFailure)
}

func TestMicrosoftAliasAdapterRotatesProxyBeforeAnyPostSideEffect(t *testing.T) {
	calls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		calls++
		if calls == 1 {
			return msacl.ExplicitAliasResult{
				Category:     "request",
				ProxyFailure: true,
			}, nil
		}
		return msacl.ExplicitAliasResult{
			Aliases:   []string{"first123456@outlook.com"},
			Attempted: []string{"first123456@outlook.com"},
		}, nil
	}

	result, err := adapter.CreateMicrosoftAliases(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress: "owner@example.com",
		Password:     "secret",
		Candidates:   []string{"first123456@outlook.com"},
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Aliases)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Attempted)
	require.Empty(t, result.Uncertain)
	require.False(t, result.ProxyFailure)
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

func TestMicrosoftAliasAdapterDoesNotRotateProxyForPageTimeout(t *testing.T) {
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	calls := 0
	adapter.createAliases = func(context.Context, string, string, string, string, []string) (msacl.ExplicitAliasResult, error) {
		calls++
		return msacl.ExplicitAliasResult{
			Attempted:    []string{"first123456@outlook.com"},
			Category:     "auth_timeout",
			SafeMessage:  "Microsoft alias authorization timed out.",
			ProxyFailure: false,
		}, nil
	}

	result, err := adapter.CreateMicrosoftAliases(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress: "owner@example.com",
		Password:     "secret",
		Candidates:   []string{"first123456@outlook.com"},
	})

	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Attempted)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Uncertain)
	require.Equal(t, "auth_timeout", result.Category)
	require.False(t, result.ProxyFailure)
}
