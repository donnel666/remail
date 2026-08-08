package api

import (
	"encoding/json"
	"testing"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestOrderResponseMapsOptionalProjectLogoURL(t *testing.T) {
	resp := orderResponse(tradeapp.CheckoutResult{
		Order: domain.Order{
			ProjectID: 17,
		},
		ProjectName:    "Logo Project",
		ProjectLogoURL: "/v1/projects/logos/logo-project",
	})

	require.NotNil(t, resp.ProjectLogoURL)
	require.Equal(t, "/v1/projects/logos/logo-project", *resp.ProjectLogoURL)

	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"projectLogoUrl":"/v1/projects/logos/logo-project"`)
}

func TestOrderResponseOmitsBlankProjectLogoURL(t *testing.T) {
	resp := orderResponse(tradeapp.CheckoutResult{
		Order:          domain.Order{ProjectID: 17},
		ProjectName:    "No Logo Project",
		ProjectLogoURL: "   ",
	})

	require.Nil(t, resp.ProjectLogoURL)
	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "projectLogoUrl")
}

func TestOrderResponseUsesUnifiedAllocationID(t *testing.T) {
	allocationType := domain.AllocationTypeGmail
	resp := orderResponse(tradeapp.CheckoutResult{
		Order:        domain.Order{AllocationType: &allocationType},
		AllocationID: 77,
	})

	require.Equal(t, "gmail", resp.AllocationType)
	require.EqualValues(t, 77, resp.AllocationID)
}

func TestOrderResponseUsesUnifiedICloudAllocationID(t *testing.T) {
	allocationType := domain.AllocationTypeICloud
	resp := orderResponse(tradeapp.CheckoutResult{
		Order:        domain.Order{AllocationType: &allocationType},
		AllocationID: 88,
	})

	require.Equal(t, "icloud", resp.AllocationType)
	require.EqualValues(t, 88, resp.AllocationID)
}
