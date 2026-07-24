package platform

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMetricsHandlerExposesReMailMetrics(t *testing.T) {
	RecordBusinessEvent("test", "succeeded")
	ObserveTaskService("test", time.Now())
	ObserveServiceDuration("test", "single", "succeeded", time.Now())
	ObserveServiceEndToEnd("test", "single", "succeeded", time.Now())
	RecordTaskEvent("test", "completed")
	RecordServiceDBTransaction("test", "committed")
	AddWorkUnits("test", "small", "succeeded", 2)
	SetWorkloadState("test", 1, 2, 3)
	RecordMySQLTransactionEvent("test", "deadlock")
	ObserveAllocationDuration("microsoft", "succeeded", time.Now())
	RecordAllocationResult("microsoft", "succeeded")
	AddAllocationCandidateAttempts("microsoft", 2)
	RecordAllocationResourceLockSkip("microsoft")
	RecordAllocationCandidateRecheckMiss("microsoft")
	RecordAllocationBucketFallback("microsoft", "first_bucket_empty")
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
	require.True(t, strings.Contains(body, "remail_service_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_service_end_to_end_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_task_events_total"))
	require.True(t, strings.Contains(body, "remail_service_db_transactions_total"))
	require.True(t, strings.Contains(body, "remail_work_units_total"))
	require.True(t, strings.Contains(body, "remail_workload_state"))
	require.True(t, strings.Contains(body, "remail_mysql_transaction_events_total"))
	require.True(t, strings.Contains(body, "remail_allocation_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_allocation_results_total"))
	require.True(t, strings.Contains(body, "remail_allocation_candidate_attempts_total"))
	require.True(t, strings.Contains(body, "remail_allocation_resource_lock_skips_total"))
	require.True(t, strings.Contains(body, "remail_allocation_candidate_recheck_misses_total"))
	require.True(t, strings.Contains(body, "remail_allocation_bucket_fallbacks_total"))
	require.True(t, strings.Contains(body, "remail_external_service_duration_seconds"))
	require.True(t, strings.Contains(body, "remail_db_wait_duration_seconds_total"))
}

func TestNormalizeAllocationResultPreservesExistingHit(t *testing.T) {
	require.Equal(t, "existing", normalizeAllocationResult("existing"))
	require.Equal(t, "system_failed", normalizeAllocationResult("unexpected"))
}

func TestHTTPMetricsMiddlewareRecordsStatusClassAndLongDurationBuckets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(HTTPMetricsMiddleware())
	router.GET("/metrics-test", func(c *gin.Context) { c.Status(http.StatusCreated) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics-test", nil))
	require.Equal(t, http.StatusCreated, recorder.Code)

	metrics := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	require.Contains(t, body, `route="/metrics-test",status_class="2xx"`)
	require.Contains(t, body, `remail_http_request_duration_seconds_bucket{method="GET",route="/metrics-test",status_class="2xx",le="120"}`)
}
