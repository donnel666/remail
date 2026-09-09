package api

import (
	"encoding/json"
	"reflect"
	"testing"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestOrderResponseOnlyExposesOrderAndPickupFields(t *testing.T) {
	allowedFields := []string{
		"id", "orderNo", "userId", "owner", "projectId", "projectName", "projectLogoUrl",
		"productType", "serviceMode", "supplyPolicy", "status", "failureCode",
		"payAmount", "refundAmount", "allocationType", "allocationId", "deliveryEmail",
		"receiveStartedAt", "receiveUntil", "activatedAt", "afterSaleUntil",
		"clientChannel", "apiKeyId", "serviceCleanupStatus", "serviceToken",
		"hasDelivery", "verificationCode", "lastMailReceivedAt", "archivedAt", "createdAt", "updatedAt",
	}
	for _, product := range []domain.ProductType{
		domain.ProductTypeMicrosoft, domain.ProductTypeDomain, domain.ProductTypeLegacyRandom,
		domain.ProductTypeGmail, domain.ProductTypeGmailVariant, domain.ProductTypeICloud, domain.ProductTypeProto,
	} {
		for _, mode := range []domain.ServiceMode{domain.ServiceModePurchase, domain.ServiceModeCode} {
			t.Run(string(product)+"/"+string(mode), func(t *testing.T) {
				result := tradeapp.CheckoutResult{Order: domain.Order{
					OrderNo: "ORDER-1", ProjectID: 17, ProductType: product, ServiceMode: mode,
					DeliveryEmail: "delivery@example.test", Status: domain.OrderStatusActive,
				}}
				// Populate optional fields so omitempty cannot hide a resource credential leak.
				fields := reflect.ValueOf(&result).Elem()
				for i := 0; i < fields.NumField(); i++ {
					if field := fields.Field(i); field.Kind() == reflect.String {
						field.SetString("populated")
					}
				}
				payload, err := json.Marshal(orderResponse(result))
				require.NoError(t, err)
				var response map[string]any
				require.NoError(t, json.Unmarshal(payload, &response))
				for field := range response {
					require.Contains(t, allowedFields, field, "order responses must never expose resource credentials")
				}
				require.Equal(t, "populated", response["serviceToken"])
				require.Equal(t, float64(17), response["projectId"])
				require.Equal(t, "delivery@example.test", response["deliveryEmail"])
				require.Equal(t, string(product), response["productType"])
				require.Equal(t, string(mode), response["serviceMode"])
			})
		}
	}
}

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

func TestOrderResponseDoesNotExposeInternalProductID(t *testing.T) {
	payload, err := json.Marshal(orderResponse(tradeapp.CheckoutResult{Order: domain.Order{
		ProjectProductID: 99,
	}}))
	require.NoError(t, err)
	require.NotContains(t, string(payload), "productId")
	require.NotContains(t, string(payload), "projectProductId")
}

func TestOrderResponseKeepsSingleDeliveryShape(t *testing.T) {
	payload, err := json.Marshal(orderResponse(tradeapp.CheckoutResult{
		VerificationCode: "654321",
	}))

	require.NoError(t, err)
	require.Contains(t, string(payload), `"verificationCode":"654321"`)
	require.NotContains(t, string(payload), "contentMode")
	require.NotContains(t, string(payload), "codes")
	require.NotContains(t, string(payload), "receivedCount")
	require.NotContains(t, string(payload), "maxCodes")
}
