package platform

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsHandlerExposesReMailMetrics(t *testing.T) {
	RecordBusinessEvent("test", "succeeded")
	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))

	require.Equal(t, 200, recorder.Code)
	body := recorder.Body.String()
	require.True(t, strings.Contains(body, "remail_business_events_total"))
	require.True(t, strings.Contains(body, "remail_db_open_connections"))
	require.True(t, strings.Contains(body, "remail_background_worker_limit"))
	require.True(t, strings.Contains(body, "remail_background_load_metrics_healthy"))
}
