package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestPermissionCatalogCoversAdminFrontendContract(t *testing.T) {
	catalog := (&AdminUseCase{}).ListPermissions(context.Background())
	actual := make(map[string]struct{})
	for _, item := range catalog {
		for _, action := range item.Actions {
			actual[item.Resource+":"+action] = struct{}{}
		}
	}

	required := []string{
		"iam:user:read",
		"iam:permission:sensitive",
		"core:resource:read",
		"core:project:read",
		"proxy:proxy:read",
		"trade:order:read",
		"trade:order:write",
		"trade:order:operate",
		"billing:wallet:read",
		"billing:wallet:write",
		"billing:wallet:operate",
		"billing:wallet:sensitive",
		"billing:card:read",
		"billing:card:write",
		"billing:card:operate",
		"billing:card:sensitive",
		"governance:log:read",
		"governance:log:operate",
	}
	for _, permission := range required {
		require.Contains(t, actual, permission)
	}
}

func TestValidPermissionPolicyAcceptsAddedAdminPermissions(t *testing.T) {
	for _, policy := range []domain.PermissionPolicy{
		{Resource: "iam:permission", Action: "sensitive", Effect: "allow"},
		{Resource: "trade:order", Action: "write", Effect: "allow"},
		{Resource: "billing:wallet", Action: "operate", Effect: "allow"},
		{Resource: "billing:card", Action: "sensitive", Effect: "deny"},
	} {
		require.True(t, validPermissionPolicy(policy), policy.Resource+":"+policy.Action)
	}

	require.False(t, validPermissionPolicy(domain.PermissionPolicy{
		Resource: "billing:wallet",
		Action:   "*",
		Effect:   "allow",
	}))
}
