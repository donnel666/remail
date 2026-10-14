package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestICloudSMTPTestEndpointPermissionsAndSharedSendingInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, sender, now := newICloudLivenessTestService(t)
	red := enableLivenessSMTPTest(t, s, sender)
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPEnabledKey, "false")
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPIntervalKey, "90")
	saved := runtimeconfig.String(runtimeconfig.ICloudLivenessSMTPServersKey, "")
	var servers []runtimeconfig.ICloudLivenessSMTPServer
	require.NoError(t, json.Unmarshal([]byte(saved), &servers))
	servers[0].Enabled = false
	raw, err := json.Marshal(servers)
	require.NoError(t, err)
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, string(raw))
	permissions := &deviceBalancePermissions{}
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), &Module{Service: s}, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 7, "admin", "admin@example.com", true
	}), permissions)
	request := func(id string, session, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/liveness-smtp/"+id+"/test", nil)
		if session {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "sid"})
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			r.Header.Set(middleware.CSRFHeaderName, "csrf")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, 401, request("qq-1", false, false).Code)
	require.Equal(t, 403, request("qq-1", true, false).Code)
	for _, permission := range []string{"system:settings/write", "system:settings/sensitive"} {
		permissions.deny = permission
		require.Equal(t, 403, request("qq-1", true, true).Code)
	}
	permissions.deny = ""
	require.Equal(t, 404, request("unknown", true, true).Code)
	require.Empty(t, sender.messages)
	w := request("qq-1", true, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.JSONEq(t, `{"accepted":true,"recipient":"probe@qq.com"}`, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Len(t, sender.messages, 1)
	require.Equal(t, "probe@qq.com", sender.messages[0].From)
	require.Equal(t, "probe@qq.com", sender.messages[0].To)
	require.NotContains(t, sender.messages[0].TextBody, "authorization-code")
	require.False(t, runtimeconfig.Bool(runtimeconfig.ICloudLivenessSMTPEnabledKey, true), "testing must not enable SMTP mode")
	w = request("qq-1", true, true)
	require.Equal(t, 429, w.Code)
	require.Equal(t, "90", w.Header().Get("Retry-After"))
	require.Len(t, sender.messages, 1)

	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPEnabledKey, "true")
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, saved)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueSMTPTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Len(t, sender.messages, 1, "liveness must share the test email's cooldown")
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceQueued, run.Status)
	require.Zero(t, run.Attempts)

	red.FastForward(90 * time.Second)
	*now = now.Add(90 * time.Second)
	sender.err = &mailapp.OutboundNotAcceptedError{Cause: errors.New("dial failed: 535 AUTH rejected authorization-code")}
	w = request("qq-1", true, true)
	require.Equal(t, 502, w.Code)
	require.Contains(t, w.Body.String(), "authentication failed")
	require.NotContains(t, w.Body.String(), "authorization-code")
	require.Equal(t, 429, request("qq-1", true, true).Code, "failed tests must also consume the interval")
	require.Len(t, sender.messages, 2, "failed tests must not retry or rotate servers")
	var logs []governanceinfra.OperationLogModel
	require.NoError(t, s.db.Where("operation_type=?", "icloud.smtp.test").Find(&logs).Error)
	require.Len(t, logs, 2)
	encoded, err := json.Marshal(logs)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "authorization-code")
	require.NotContains(t, string(encoded), "probe@qq.com")
}

func TestICloudSMTPTestRequiresStoredCredentials(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	enableLivenessSMTPTest(t, s, sender)
	for _, raw := range []string{
		`invalid`,
		`[{"id":"qq-1","name":"QQ","enabled":false,"addr":"smtp.qq.com:465","username":"probe@qq.com","from":"probe@qq.com"}]`,
	} {
		runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, raw)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
		c.Params = gin.Params{{Key: "serverId", Value: "qq-1"}}
		(&handler{service: s}).testLivenessSMTP(c)
		require.Equal(t, 422, w.Code)
		require.Empty(t, sender.messages)
	}
}

func TestICloudSMTPTestFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct{ diagnostic, want string }{
		{"dial failed: 535 bad credentials", "authentication failed"},
		{"SMTP AUTH failed: 535 bad credentials", "authentication failed"},
		{"dial failed: tls: certificate failure", "TLS negotiation failed"},
		{"mail from failed: 550 not permitted", "rejected the sender"},
		{"smtp send failed: mail from failed: 501 mail from address must be same as authorization user", "rejected the sender"},
		{"rcpt to failed: 550 mailbox missing", "rejected the test recipient"},
		{"rcpt to failed: 550 unauthorized recipient", "rejected the test recipient"},
		{"data close failed: 554 policy", "rejected the test message"},
		{"data close failed: 550 unauthenticated email blocked by DMARC", "rejected the test message"},
		{"dial failed: connection refused", "connection failed"},
		{"dial failed: lookup auth.smtp.example.com: no such host", "connection failed"},
		{"other failure secret", "did not accept"},
	} {
		message := smtpTestFailureMessage(&mailapp.OutboundNotAcceptedError{Cause: errors.New(tc.diagnostic)})
		require.Contains(t, message, tc.want)
		require.NotContains(t, message, "secret")
	}
	require.Contains(t, smtpTestFailureMessage(errors.New("data close failed: EOF secret")), "unconfirmed")
}
