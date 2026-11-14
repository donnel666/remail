package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

func TestDisabledMicrosoftAuxiliaryMailKeepsResourceOwnershipMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9401, "disabled")
	createMicrosoftAliasTestBinding(t, db, 9401, "proof@recovery.test", "verified")
	resolver, ctx := NewInboundResourceResolver(db), context.Background()
	for _, status := range []string{"disabled", "normal", "pending", "validating", "identifying", "abnormal"} {
		t.Run(status, func(t *testing.T) {
			require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 9401).Update("status", status).Error)
			got, err := resolver.ResolveInboundRecipient(ctx, "PROOF@RECOVERY.TEST")
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, domain.InboundResourceMicrosoft, got.ResourceType)
			require.EqualValues(t, 9401, got.ResourceID)
			require.EqualValues(t, 9401, got.OwnerUserID)
		})
	}
	for _, state := range []struct{ resource, binding string }{
		{"deleted", "verified"}, {"disabled", "expired"},
	} {
		require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 9401).Update("status", state.resource).Error)
		require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9401).Update("status", state.binding).Error)
		got, err := resolver.ResolveInboundRecipient(ctx, "proof@recovery.test")
		require.NoError(t, err)
		require.Equal(t, domain.InboundResourceDomain, got.ResourceType)
		require.NotEqualValues(t, 9401, got.ResourceID)
	}
}
