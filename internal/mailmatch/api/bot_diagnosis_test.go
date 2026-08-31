package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type botDiagnosisRepoStub struct{}

func (botDiagnosisRepoStub) LookupCodeDiagnosis(context.Context, uint, string, uint) (mailmatchapp.CodeDiagnosisLookup, error) {
	receivedAt := time.Now().UTC().Add(-2 * time.Minute)
	return mailmatchapp.CodeDiagnosisLookup{EmailOrderExists: true, Orders: []mailmatchapp.CodeDiagnosisOrderFact{{
		OrderNo: "SECRET-ORDER-NO", ServiceMode: "code", Status: "completed", DeliveryStoredAt: &receivedAt,
	}}}, nil
}

func TestBotCodeDiagnosisResponseCannotExposeRawFacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(botDiagnosisRepoStub{})}
	RegisterBotRoutes(router.Group("/v1/bot"), module, func(*gin.Context) (uint, bool) { return 2, true })
	req := httptest.NewRequest(http.MethodPost, "/v1/bot/diagnoses/code",
		bytes.NewBufferString(`{"email":"private@example.com","projectId":10}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{"result", "reason", "action", "requestId"}, mapKeys(body))
	require.Equal(t, "pickup_not_requested", body["result"])
	for _, secret := range []string{"private@example.com", "SECRET-ORDER-NO", "verificationCode", "token", "message"} {
		require.NotContains(t, response.Body.String(), secret)
	}
}

func TestBotCodeDiagnosisRejectsUnknownBodyFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(botDiagnosisRepoStub{})}
	RegisterBotRoutes(router.Group("/v1/bot"), module, func(*gin.Context) (uint, bool) { return 2, true })
	req := httptest.NewRequest(http.MethodPost, "/v1/bot/diagnoses/code",
		strings.NewReader(`{"email":"private@example.com","projectId":10,"orderNo":"SECRET"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.ElementsMatch(t, []string{"result", "reason", "action", "requestId"}, mapKeys(body))
	require.Equal(t, "invalid_request", body["result"])
	require.NotContains(t, response.Body.String(), "SECRET")
}

func TestBotCodeDiagnosisPreservesResolverFailureResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	module := &Module{BotDiagnosis: mailmatchapp.NewBotDiagnosisService(botDiagnosisRepoStub{})}
	RegisterBotRoutes(router.Group("/v1/bot"), module, func(c *gin.Context) (uint, bool) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable."})
		return 0, false
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/bot/diagnoses/code",
		strings.NewReader(`{"email":"private@example.com","projectId":10}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"message":"Service is temporarily unavailable."}`, response.Body.String())
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
