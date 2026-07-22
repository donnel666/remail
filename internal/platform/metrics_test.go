package platform

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetricsHandlerExposesReMailMetrics(t *testing.T) {
	RecordBusinessEvent("test", "succeeded")
	ObserveTaskService("test", time.Now())
	AddWorkUnits("test", "small", "succeeded", 2)
	SetWorkloadState("test", 1, 2, 3)
	RecordMySQLTransactionEvent("test", "deadlock")
	ObserveExternalService("test", "request", "succeeded", time.Now())
	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))

	require.Equal(t, 200, recorder.Code)
	body := recorder.Body.String()
	require.True(t, strings.Contains(body, "remail_business_events_total"))
	require.True(t, strings.Contains(body, "remail_db_open_connections"))
	require.True(t, strings.Contains(body, "remail_background_worker_limit"))
	require.True(t, strings.Contains(body, "remail_background_load_metrics_healthy"))
	require.True(t, strings.Contains(body, "remail_task_service_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_work_units_total"))
	require.True(t, strings.Contains(body, "remail_workload_state"))
	require.True(t, strings.Contains(body, "remail_mysql_transaction_events_total"))
	require.True(t, strings.Contains(body, "remail_external_service_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_db_wait_duration_seconds_total"))
}
