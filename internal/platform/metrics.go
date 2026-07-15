package platform

import (
	"database/sql"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	metricsDB             atomic.Pointer[sql.DB]
	metricsBackgroundLoad atomic.Pointer[BackgroundLoadController]

	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "remail_http_requests_total", Help: "HTTP requests by route and status."},
		[]string{"method", "route", "status"},
	)
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "remail_http_request_duration_seconds",
			Help:    "HTTP request duration by route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)
	businessEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "remail_business_events_total", Help: "Important business outcomes."},
		[]string{"event", "result"},
	)
	mailVisibleDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "remail_mail_visible_duration_seconds",
			Help:    "Time from provider receive timestamp to matched message persistence.",
			Buckets: []float64{0.25, 0.5, 1, 2, 5, 10, 20, 30, 60},
		},
		[]string{"resource_type"},
	)
	queueWaitDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "remail_task_queue_wait_seconds",
			Help:    "Time from durable task creation to worker processing.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"task_type"},
	)
)

func init() {
	prometheus.MustRegister(
		httpRequests,
		httpDuration,
		businessEvents,
		mailVisibleDuration,
		queueWaitDuration,
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_db_open_connections", Help: "Current open database connections."},
			func() float64 {
				if db := metricsDB.Load(); db != nil {
					return float64(db.Stats().OpenConnections)
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_db_in_use_connections", Help: "Current in-use database connections."},
			func() float64 {
				if db := metricsDB.Load(); db != nil {
					return float64(db.Stats().InUse)
				}
				return 0
			},
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{Name: "remail_db_wait_count_total", Help: "Total database pool waits."},
			func() float64 {
				if db := metricsDB.Load(); db != nil {
					return float64(db.Stats().WaitCount)
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_background_worker_limit", Help: "Adaptive limit for concurrently executing background task handlers."},
			func() float64 {
				if controller := metricsBackgroundLoad.Load(); controller != nil {
					return float64(controller.Snapshot().Limit)
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_background_workers_active", Help: "Background task handlers currently executing inside the adaptive window."},
			func() float64 {
				if controller := metricsBackgroundLoad.Load(); controller != nil {
					return float64(controller.Snapshot().Active)
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_background_cpu_percent", Help: "Latest host CPU utilization sample used by the background worker governor."},
			func() float64 {
				if controller := metricsBackgroundLoad.Load(); controller != nil {
					snapshot := controller.Snapshot()
					if snapshot.CPUValid {
						return snapshot.CPUPercent
					}
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_background_memory_percent", Help: "Latest host memory utilization sample used by the background worker governor."},
			func() float64 {
				if controller := metricsBackgroundLoad.Load(); controller != nil {
					snapshot := controller.Snapshot()
					if snapshot.MemoryValid {
						return snapshot.MemoryPercent
					}
				}
				return 0
			},
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Name: "remail_background_load_metrics_healthy", Help: "Whether both CPU and memory samples are currently valid (1 for valid, 0 otherwise)."},
			func() float64 {
				if controller := metricsBackgroundLoad.Load(); controller != nil {
					snapshot := controller.Snapshot()
					if snapshot.CPUValid && snapshot.MemoryValid {
						return 1
					}
				}
				return 0
			},
		),
	)
}

func SetMetricsDB(db *sql.DB) {
	metricsDB.Store(db)
}

func SetMetricsBackgroundLoad(controller *BackgroundLoadController) {
	metricsBackgroundLoad.Store(controller)
}

func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

func HTTPMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(c.Writer.Status())
		httpRequests.WithLabelValues(c.Request.Method, route, status).Inc()
		httpDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(startedAt).Seconds())
	}
}

func RecordBusinessEvent(event string, result string) {
	businessEvents.WithLabelValues(event, result).Inc()
}

func ObserveMailVisible(resourceType string, receivedAt time.Time) {
	if receivedAt.IsZero() {
		return
	}
	seconds := time.Since(receivedAt).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	mailVisibleDuration.WithLabelValues(resourceType).Observe(seconds)
}

func ObserveQueueWait(taskType string, createdAt time.Time) {
	if createdAt.IsZero() {
		return
	}
	seconds := time.Since(createdAt).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	queueWaitDuration.WithLabelValues(taskType).Observe(seconds)
}
