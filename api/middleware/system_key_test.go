package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type systemKeyAuthenticatorFunc func(context.Context, string) (uint, error)

func (f systemKeyAuthenticatorFunc) AuthenticateSystemKey(ctx context.Context, plain string) (uint, error) {
	return f(ctx, plain)
}

func systemKeyMiddlewareResponse(t *testing.T, authenticator SystemKeyAuthenticator) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/open", SystemKeyRequired(authenticator), func(c *gin.Context) {
		keyID, ok := GetCurrentSystemKeyID(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"keyId": keyID})
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/open", nil)
	request.Header.Set(SystemKeyHeaderName, "sk_test")
	router.ServeHTTP(response, request)
	return response
}

func TestSystemKeyRequiredStoresAuthenticatedApplication(t *testing.T) {
	response := systemKeyMiddlewareResponse(t, systemKeyAuthenticatorFunc(func(_ context.Context, plain string) (uint, error) {
		require.Equal(t, "sk_test", plain)
		return 7, nil
	}))

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"keyId":7}`, response.Body.String())
}

func TestSystemKeyRequiredHidesAuthenticationFailures(t *testing.T) {
	for _, authErr := range []error{settingsdomain.ErrInvalidSystemKey, settingsdomain.ErrSystemKeyNotFound} {
		response := systemKeyMiddlewareResponse(t, systemKeyAuthenticatorFunc(func(context.Context, string) (uint, error) {
			return 0, authErr
		}))
		require.Equal(t, http.StatusUnauthorized, response.Code)
		require.NotContains(t, response.Body.String(), authErr.Error())
	}

	internalErr := errors.New("database password leaked")
	response := systemKeyMiddlewareResponse(t, systemKeyAuthenticatorFunc(func(context.Context, string) (uint, error) {
		return 0, internalErr
	}))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), internalErr.Error())
	require.Equal(t, http.StatusServiceUnavailable, systemKeyMiddlewareResponse(t, nil).Code)
}
