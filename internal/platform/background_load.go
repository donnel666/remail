package platform

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	backgroundOverloadPercent    = 50.0
	backgroundRecoveryPercent    = 40.0
	backgroundLoadSampleInterval = 2 * time.Second

	// The Asynq server owns the hard ceiling. The adaptive window starts
	// conservatively, slow-starts and then probes upward additively while the
	// host has headroom, and never closes completely so every weighted
	// background queue can progress.
	backgroundWorkerMinimum             = 8
	backgroundWorkerInitial             = 16
	backgroundWorkerMinimumIncreaseStep = 8
	backgroundRecoverySamples           = 2
	backgroundMetricFailureLimit        = 3
)

// ErrBackgroundExecutionDeferred tells the background Asynq server to retry a
// task later without counting a business failure. The task remains fenced by
// its existing durable dispatch token; no database cleanup is needed.
var ErrBackgroundExecutionDeferred = errors.New("background execution capacity is temporarily unavailable")

type systemLoadReader interface {
	CPUPercent(ctx context.Context) (float64, error)
	MemoryPercent(ctx context.Context) (float64, error)
}

type gopsutilSystemLoadReader struct{}

func (gopsutilSystemLoadReader) CPUPercent(ctx context.Context) (float64, error) {
	values, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, errors.New("cpu usage sample is empty")
	}
	return values[0], nil
}

func (gopsutilSystemLoadReader) MemoryPercent(ctx context.Context) (float64, error) {
	stats, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, errors.New("memory usage sample is empty")
	}
	return stats.UsedPercent, nil
}

type systemLoadSample struct {
	cpuPercent    float64
	memoryPercent float64
	cpuValid      bool
	memoryValid   bool
	sampledAt     time.Time
}

// adaptiveConcurrencyGate is one process-wide, non-blocking execution window
// shared by every background task type. A denied task is deferred by Asynq
// instead of waiting in the handler, so admission control never consumes the
// task's execution timeout.
type adaptiveConcurrencyGate struct {
	mu      sync.Mutex
	maximum int
	limit   int
	active  int
}

func newAdaptiveConcurrencyGate(initial, maximum int) *adaptiveConcurrencyGate {
	if maximum <= 0 {
		maximum = 1
	}
	initial = clamp(initial, 1, maximum)
	return &adaptiveConcurrencyGate{
		maximum: maximum,
		limit:   initial,
	}
}

func (g *adaptiveConcurrencyGate) TryAcquire() (func(), bool) {
	g.mu.Lock()
	if g.active >= g.limit {
		g.mu.Unlock()
		return func() {}, false
	}
	g.active++
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(g.Release) }, true
}

func (g *adaptiveConcurrencyGate) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active <= 0 {
		panic("platform: background concurrency permit released without acquire")
	}
	g.active--
}

func (g *adaptiveConcurrencyGate) Resize(limit int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.limit = clamp(limit, 1, g.maximum)
}

func (g *adaptiveConcurrencyGate) Stats() (limit, active, maximum int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit, g.active, g.maximum
}

// BackgroundLoadSnapshot is the observable state of the adaptive background
// worker window. Limit is the number of handlers currently allowed to execute;
// Active is the live gate count. Denied tasks are returned without blocking.
type BackgroundLoadSnapshot struct {
	Limit         int
	Active        int
	Maximum       int
	CPUPercent    float64
	MemoryPercent float64
	CPUValid      bool
	MemoryValid   bool
	SampledAt     time.Time
}

// BackgroundLoadController applies an AIMD-style feedback loop to one global
// background execution window. CPU and memory are the only pressure signals:
//
//   - below 40% for both signals while the current window is saturated: after
//     two confirming samples, double up to the remembered slow-start threshold,
//     then grow additively without increasing by more than half the window;
//   - from 40% up to 50%: hold the current window;
//   - at or above 50% for either signal: halve the window.
//
// The 10-point hysteresis band prevents boundary chatter. Multiplicative
// decrease gives foreground work capacity quickly. Startup doubles only to
// half of the hard ceiling; after the first overload, that measured safe point
// replaces the startup threshold and recovery becomes additive. Requiring real
// saturation prevents an idle process from silently ramping to the 512 hard
// ceiling before its first burst. The window never makes queue-specific
// decisions; Asynq's non-strict weighted polling and durable dispatchers
// preserve progress across background task types.
type BackgroundLoadController struct {
	systemLoad         systemLoadReader
	gate               *adaptiveConcurrencyGate
	minimum            int
	maximum            int
	increaseStep       int
	slowStartThreshold int
	sampleInterval     time.Duration

	tuneMu            sync.Mutex
	sampleMu          sync.RWMutex
	lastSample        systemLoadSample
	metricsStateKnown bool
	metricsHealthy    bool
	headroomSamples   int
	metricFailures    int

	lifecycleMu sync.Mutex
	started     bool
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewBackgroundLoadController(maximum int) *BackgroundLoadController {
	return newBackgroundLoadController(gopsutilSystemLoadReader{}, maximum)
}

func newBackgroundLoadController(load systemLoadReader, maximum int) *BackgroundLoadController {
	if maximum <= 0 {
		maximum = 1
	}
	minimum := min(backgroundWorkerMinimum, maximum)
	initial := min(max(backgroundWorkerInitial, minimum), maximum)
	increaseStep := min(max(backgroundWorkerMinimumIncreaseStep, maximum/16, 1), maximum)
	slowStartThreshold := min(maximum, max(initial, maximum/2))
	return &BackgroundLoadController{
		systemLoad:         load,
		gate:               newAdaptiveConcurrencyGate(initial, maximum),
		minimum:            minimum,
		maximum:            maximum,
		increaseStep:       increaseStep,
		slowStartThreshold: slowStartThreshold,
		sampleInterval:     backgroundLoadSampleInterval,
	}
}

// Start performs one load sample synchronously and then starts the feedback
// loop. The returned cleanup function is idempotent and matches router cleanup
// functions. A controller has one lifecycle and is not restarted after Stop.
func (c *BackgroundLoadController) Start(parent context.Context) func(context.Context) {
	if c == nil {
		return func(context.Context) {}
	}
	if parent == nil {
		parent = context.Background()
	}

	c.lifecycleMu.Lock()
	if c.started {
		c.lifecycleMu.Unlock()
		return c.Stop
	}
	runCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	c.started = true
	c.cancel = cancel
	c.done = done
	c.lifecycleMu.Unlock()

	// Establish the first P-state before the background Asynq server starts.
	c.sampleAndTune(runCtx)
	go c.run(runCtx, done)
	return c.Stop
}

func (c *BackgroundLoadController) Stop(ctx context.Context) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.lifecycleMu.Lock()
	cancel := c.cancel
	done := c.done
	c.lifecycleMu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (c *BackgroundLoadController) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(c.sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sampleAndTune(ctx)
		}
	}
}

func (c *BackgroundLoadController) sampleAndTune(ctx context.Context) {
	if c == nil || c.gate == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	c.tuneMu.Lock()
	defer c.tuneMu.Unlock()

	sample := systemLoadSample{sampledAt: time.Now()}
	if c.systemLoad != nil {
		if value, err := c.systemLoad.CPUPercent(ctx); err == nil && validSystemPercent(value) {
			sample.cpuPercent = value
			sample.cpuValid = true
		}
		if value, err := c.systemLoad.MemoryPercent(ctx); err == nil && validSystemPercent(value) {
			sample.memoryPercent = value
			sample.memoryValid = true
		}
	}

	current, active, _ := c.gate.Stats()
	next := current
	reason := "hysteresis"
	high := (sample.cpuValid && sample.cpuPercent >= backgroundOverloadPercent) ||
		(sample.memoryValid && sample.memoryPercent >= backgroundOverloadPercent)
	low := sample.cpuValid && sample.memoryValid &&
		sample.cpuPercent < backgroundRecoveryPercent &&
		sample.memoryPercent < backgroundRecoveryPercent
	healthy := sample.cpuValid && sample.memoryValid
	if healthy {
		c.metricFailures = 0
	} else {
		c.metricFailures = min(c.metricFailures+1, backgroundMetricFailureLimit)
	}
	switch {
	case high:
		c.headroomSamples = 0
		next = max(c.minimum, (current+1)/2)
		c.slowStartThreshold = next
		reason = "overload"
	case low:
		if active != current {
			c.headroomSamples = 0
			reason = "underutilized"
		} else {
			c.headroomSamples = min(c.headroomSamples+1, backgroundRecoverySamples)
		}
		if active == current && c.headroomSamples >= backgroundRecoverySamples {
			if current < c.slowStartThreshold {
				next = min(c.slowStartThreshold, current*2)
				reason = "slow_start"
			} else {
				increase := min(c.increaseStep, max(1, current/2))
				next = min(c.maximum, current+increase)
				reason = "congestion_avoidance"
			}
		} else if active == current {
			reason = "headroom_confirming"
		}
	case !sample.cpuValid || !sample.memoryValid:
		c.headroomSamples = 0
		if c.metricFailures >= backgroundMetricFailureLimit {
			next = max(c.minimum, (current+1)/2)
			c.slowStartThreshold = next
			reason = "metrics_stale"
		} else {
			reason = "metrics_unavailable"
		}
	default:
		c.headroomSamples = 0
	}
	if next != current {
		c.gate.Resize(next)
		slog.Info(
			"background worker window adjusted",
			"previous", current,
			"limit", next,
			"maximum", c.maximum,
			"active", active,
			"reason", reason,
			"cpu_percent", sample.cpuPercent,
			"cpu_valid", sample.cpuValid,
			"memory_percent", sample.memoryPercent,
			"memory_valid", sample.memoryValid,
		)
	}

	if !healthy && (!c.metricsStateKnown || c.metricsHealthy) {
		slog.Warn(
			"background load metrics unavailable; holding worker window",
			"cpu_valid", sample.cpuValid,
			"memory_valid", sample.memoryValid,
			"limit", next,
		)
	} else if healthy && c.metricsStateKnown && !c.metricsHealthy {
		slog.Info("background load metrics recovered", "limit", next)
	}
	c.metricsStateKnown = true
	c.metricsHealthy = healthy

	c.sampleMu.Lock()
	c.lastSample = sample
	c.sampleMu.Unlock()
}

// TryAcquire attempts one permit without blocking. On denial, background
// handlers return ErrBackgroundExecutionDeferred so Asynq preserves the task
// and its durable dispatch token while scheduling a short retry. Waiting here
// would consume the task's execution timeout before business work starts.
func (c *BackgroundLoadController) TryAcquire() (func(), bool) {
	if c == nil || c.gate == nil {
		return func() {}, true
	}
	return c.gate.TryAcquire()
}

// Available returns the currently unused portion of the adaptive execution
// window. Durable dispatchers use this as a bounded prefetch hint so a large
// database backlog does not become an unbounded Asynq retry backlog. It is not
// an overload signal; CPU and memory remain the only inputs that resize Limit.
func (c *BackgroundLoadController) Available() int {
	if c == nil || c.gate == nil {
		return 0
	}
	limit, active, _ := c.gate.Stats()
	return max(0, limit-active)
}

func (c *BackgroundLoadController) Snapshot() BackgroundLoadSnapshot {
	if c == nil || c.gate == nil {
		return BackgroundLoadSnapshot{}
	}
	limit, active, maximum := c.gate.Stats()
	c.sampleMu.RLock()
	sample := c.lastSample
	c.sampleMu.RUnlock()
	return BackgroundLoadSnapshot{
		Limit:         limit,
		Active:        active,
		Maximum:       maximum,
		CPUPercent:    sample.cpuPercent,
		MemoryPercent: sample.memoryPercent,
		CPUValid:      sample.cpuValid,
		MemoryValid:   sample.memoryValid,
		SampledAt:     sample.sampledAt,
	}
}

func validSystemPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func clamp(value, minimum, maximum int) int {
	return min(max(value, minimum), maximum)
}
