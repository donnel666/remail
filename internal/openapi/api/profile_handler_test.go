package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type profileKeyRepository struct {
	openapiapp.Repository
	key domain.APIKey
}

func (r profileKeyRepository) FindAPIKey(_ context.Context, userID, keyID uint) (*domain.APIKey, error) {
	if userID != 7 || keyID != 3 {
		return nil, domain.ErrAPIKeyNotFound
	}
	return &r.key, nil
}

func TestAPIKeyProfileWalletBalance(t *testing.T) {
	limit := int64(20)
	exhausted := int64(10)
	for _, tc := range []struct {
		name       string
		balance    string
		quotaLimit *int64
		queryError error
		want       string
		status     int
	}{
		{name: "points", balance: "45678.90", want: "45678.90", status: http.StatusOK},
		{name: "six decimal places", balance: "12.345678", want: "12.345678", status: http.StatusOK},
		{name: "large balance", balance: "999999999999.999999", want: "999999999999.999999", status: http.StatusOK},
		{name: "zero balance", balance: "0.00", want: "0.00", status: http.StatusOK},
		{name: "missing wallet", want: "0.00", status: http.StatusOK},
		{name: "key balance lower", balance: "50.00", quotaLimit: &limit, want: "7.654322", status: http.StatusOK},
		{name: "wallet balance lower", balance: "1.234567", quotaLimit: &limit, want: "1.234567", status: http.StatusOK},
		{name: "equal balances", balance: "7.654322", quotaLimit: &limit, want: "7.654322", status: http.StatusOK},
		{name: "quota exhausted", balance: "50.00", quotaLimit: &exhausted, want: "0.00", status: http.StatusOK},
		{name: "invalid wallet balance", balance: "invalid", status: http.StatusInternalServerError},
		{name: "wallet query failure", queryError: errors.New("wallet database unavailable"), status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uc := openapiapp.NewUseCase(profileKeyRepository{key: domain.APIKey{
				ID: 3, UserID: 7, KeyPlain: "rk-secret", Enabled: true,
				QuotaLimit: tc.quotaLimit, QuotaUsed: decimal.RequireFromString("12.345678"), RequestCount: 3000,
			}})
			t.Cleanup(func() { require.NoError(t, uc.Close(context.Background())) })
			mod := &Module{
				UseCase: uc,
				ConsumerBalances: func(_ context.Context, userIDs []uint) (map[uint]string, error) {
					require.Equal(t, []uint{7}, userIDs)
					balances := map[uint]string{99: "100.00"}
					if tc.balance != "" {
						balances[7] = tc.balance
					}
					return balances, tc.queryError
				},
			}
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/open/apikey/profile?userId=99", nil)
			middleware.SetCurrentUser(ctx, 7, iamdomain.RoleUser, "", "")
			ctx.Set(contextKeyAPIKeyID, uint(3))
			NewHandler(mod).GetAPIKeyProfile(ctx)

			require.Equal(t, tc.status, response.Code)
			require.NotContains(t, response.Body.String(), "rk-secret")
			if tc.status != http.StatusOK {
				require.NotContains(t, response.Body.String(), "balance")
				return
			}
			var result KeyProfileResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, tc.want, result.APIKey.Balance)
			require.Equal(t, "12.345678", result.APIKey.QuotaUsed)
			require.EqualValues(t, 3000, result.APIKey.RequestCount)
		})
	}
}
