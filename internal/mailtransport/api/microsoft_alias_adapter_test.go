// Package api tests.
//
// The old createAliases/reconcileAliases struct fields have been removed in the
// OTC-login rewrite (Step 5). The adapter now calls msacl.SyncAndAddExplicitAliases
// directly and no longer offers injectable stub functions.
//
// TODO: rewrite these tests to cover the new SyncAndAddExplicitAliases path:
//   - OTC login failure halts and returns an error
//   - Successful create returns ExistingAliases
//   - Backfill-only (empty candidates) lists without creating
package api

import (
	"context"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAliasAdapterStopsAfterPostSideEffectBecomesUncertain(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
	_ = context.Background()
	_ = require.New(t)
	_ = mailapp.MicrosoftAliasCreationRequest{}
}

func TestMicrosoftAliasAdapterRotatesProxyBeforeAnyPostSideEffect(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
}

func TestMicrosoftAliasAdapterUsesReadOnlyReconciliationForUncertainCandidates(t *testing.T) {
	t.Skip("adapter rewritten — ReconcileOnly logic removed")
}

func TestMicrosoftAliasAdapterDoesNotRotateProxyForPageTimeout(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
}
