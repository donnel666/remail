package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

func TestProtoUncertainRevalidationPreservesPreviouslyVerifiedSession(t *testing.T) {
	for _, category := range []string{"protocol", "request", "action_required", "invalid_credentials", "identity_mismatch"} {
		t.Run(category, func(t *testing.T) {
			s, id := newValidatedProto(t)
			ctx := context.Background()
			row, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			require.NoError(t, s.CompleteHistorySuccess(ctx, id, row.ValidationGeneration))
			require.NoError(t, s.SetForSale(ctx, id, nil, true))
			previous, err := s.ReadSession(ctx, id, row.CredentialRevision)
			require.NoError(t, err)
			s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
				return proton.Session{}, &proton.Failure{Category: category, SafeMessage: "Login could not be confirmed.", Retryable: category == "request"}
			}}
			_, err = s.ClaimForValidation(ctx, id, nil)
			require.NoError(t, err)
			require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
			current, err := s.ReadSession(ctx, id, row.CredentialRevision)
			if category == "invalid_credentials" || category == "identity_mismatch" {
				require.ErrorIs(t, err, ErrSessionUnavailable)
			} else {
				require.NoError(t, err)
				require.Equal(t, previous, current)
			}
			after, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			require.True(t, after.ForSale)
			require.NotEqual(t, domain.StatusNormal, after.Status, "uncertain revalidation must not allow new allocations")
		})
	}
}
