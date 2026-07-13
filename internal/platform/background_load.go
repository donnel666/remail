package platform

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	backgroundModerateDBUtilization = 0.40
	backgroundBusyDBUtilization     = 0.70
	backgroundCriticalDBUtilization = 0.90

	backgroundIdleExecutionCap     = 32
	backgroundModerateExecutionCap = 8
	backgroundBusyExecutionCap     = 2
	backgroundCriticalExecutionCap = 0
	backgroundIdleDispatchCap      = 2 * backgroundIdleExecutionCap
	backgroundModerateDispatchCap  = 2 * backgroundModerateExecutionCap
	backgroundBusyDispatchCap      = backgroundBusyExecutionCap
	backgroundCriticalDispatchCap  = 0

	backgroundExecutionLeaseTTL = 25 * time.Minute
)

const (
	backgroundLoadIdle backgroundLoadLevel = iota
	backgroundLoadModerate
	backgroundLoadBusy
	backgroundLoadCritical
)

const backgroundExecutionKey = "remail:background-execution-slots"

var acquireBackgroundExecutionScript = redis.NewScript(`
redis.call("zremrangebyscore", KEYS[1], "-inf", ARGV[1])
if redis.call("zcard", KEYS[1]) >= tonumber(ARGV[2]) then
    return 0
end
redis.call("zadd", KEYS[1], ARGV[3], ARGV[4])
redis.call("pexpire", KEYS[1], ARGV[5])
return 1
`)

type backgroundLoadLevel uint8

type queueInfoReader interface {
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
}

// BackgroundLoadController keeps low-priority queues full while the service is
// idle and stops adding work when foreground queues or MySQL are busy.
type BackgroundLoadController struct {
	db                 *sql.DB
	queues             queueInfoReader
	redis              redis.UniversalClient
	workerConcurrency  int
	foregroundQueues   []string
	backgroundQueues   []string
	foregroundRequests atomic.Int64
	dispatchMu         sync.Mutex
	executionMu        sync.Mutex
	localExecutions    int
}

func NewBackgroundLoadController(db *sql.DB, queues queueInfoReader, redisClient redis.UniversalClient, workerConcurrency int) *BackgroundLoadController {
	if workerConcurrency <= 0 {
		workerConcurrency = 1
	}
	return &BackgroundLoadController{
		db:                db,
		queues:            queues,
		redis:             redisClient,
		workerConcurrency: workerConcurrency,
		foregroundQueues:  []string{"mailfetch", "default", "mailtransport"},
		backgroundQueues:  []string{"background_validation", "background_alias"},
	}
}

func (c *BackgroundLoadController) AcquireDispatchBudget(_ context.Context, queue string, minimum, maximum int) (int, func()) {
	if c == nil {
		return minimum, func() {}
	}

	// Dispatchers are short database/queue operations. Serializing them in this
	// process prevents competing scans from overfilling the background queues
	// without turning a normal lock race into a silently dropped dispatch.
	// Cross-process correctness remains with each durable store's claim.
	c.dispatchMu.Lock()
	return c.dispatchLimit(queue, minimum, maximum, true), c.dispatchMu.Unlock
}

// TryAcquireExecution admits a low-priority task immediately before it claims
// durable work or starts an external request. Dispatch limiting keeps Redis
// reasonably sized; this gate is what makes already-pending tasks yield when
// foreground load rises.
func (c *BackgroundLoadController) TryAcquireExecution(ctx context.Context, _ string) (bool, func()) {
	if c == nil {
		return true, func() {}
	}
	limit := c.executionLimit()
	if limit <= 0 {
		return false, func() {}
	}
	if c.redis == nil {
		return c.tryAcquireLocalExecution(limit)
	}

	now := time.Now().UTC()
	token := NewUUIDV4String()
	result, err := acquireBackgroundExecutionScript.Run(
		ctx,
		c.redis,
		[]string{backgroundExecutionKey},
		now.UnixMilli(),
		limit,
		now.Add(backgroundExecutionLeaseTTL).UnixMilli(),
		token,
		backgroundExecutionLeaseTTL.Milliseconds(),
	).Int()
	if err != nil {
		if ctx.Err() != nil {
			return false, func() {}
		}
		// Keep a bounded process-local fallback if distributed admission is
		// temporarily unavailable. This avoids both stranding durable work and
		// admitting every pending task without a limit.
		return c.tryAcquireLocalExecution(limit)
	}
	if result == 0 {
		return false, func() {}
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = c.redis.ZRem(releaseCtx, backgroundExecutionKey, token).Err()
		})
	}
	return true, release
}

func (c *BackgroundLoadController) tryAcquireLocalExecution(limit int) (bool, func()) {
	c.executionMu.Lock()
	if c.localExecutions >= limit {
		c.executionMu.Unlock()
		return false, func() {}
	}
	c.localExecutions++
	c.executionMu.Unlock()

	var once sync.Once
	return true, func() {
		once.Do(func() {
			c.executionMu.Lock()
			c.localExecutions--
			c.executionMu.Unlock()
		})
	}
}

func (c *BackgroundLoadController) BeginForegroundRequest() func() {
	if c == nil {
		return func() {}
	}
	c.foregroundRequests.Add(1)
	return func() {
		c.foregroundRequests.Add(-1)
	}
}

// DispatchLimit returns how many additional tasks may be queued now.
func (c *BackgroundLoadController) DispatchLimit(queue string, minimum, maximum int) int {
	return c.dispatchLimit(queue, minimum, maximum, false)
}

func (c *BackgroundLoadController) dispatchLimit(queue string, minimum, maximum int, ignoreCurrentDispatcher bool) int {
	if maximum <= 0 {
		return 0
	}
	if minimum <= 0 {
		minimum = 1
	}
	if minimum > maximum {
		minimum = maximum
	}
	if c == nil {
		return minimum
	}

	target := min(maximum, backgroundIdleDispatchCap)
	switch c.loadLevel(ignoreCurrentDispatcher) {
	case backgroundLoadCritical:
		target = min(maximum, backgroundCriticalDispatchCap)
	case backgroundLoadBusy:
		target = min(maximum, backgroundBusyDispatchCap)
	case backgroundLoadModerate:
		target = min(maximum, backgroundModerateDispatchCap)
	}

	if target <= 0 {
		return 0
	}
	globalOutstanding := 0
	queueOutstanding := 0
	otherQueueHasWork := false
	for _, backgroundQueue := range c.backgroundQueues {
		outstanding, reliable := c.queueOutstanding(backgroundQueue)
		if !reliable {
			return 0
		}
		globalOutstanding += outstanding
		if backgroundQueue == queue {
			queueOutstanding = outstanding
		} else if outstanding > 0 {
			otherQueueHasWork = true
		}
	}
	globalAvailable := target - globalOutstanding
	if globalAvailable <= 0 {
		// A single queue is allowed to borrow the whole idle budget, but that
		// must not prevent the other periodically-run dispatcher from ever
		// seeding its first task. Admit one bounded probe batch; once that work
		// is visible in Asynq, the normal 3:1 targets make the borrowing queue
		// drain back to its share. The execution gate remains the hard resource
		// cap, so this small dispatch overshoot cannot increase running work.
		if queueOutstanding == 0 {
			return min(minimum, target, maximum)
		}
		return 0
	}
	queueTarget := target
	if otherQueueHasWork {
		queueTarget = c.backgroundQueueTarget(queue, target)
	}
	queueAvailable := queueTarget - queueOutstanding
	if queueAvailable <= 0 {
		return 0
	}
	if queueAvailable < globalAvailable {
		return queueAvailable
	}
	return globalAvailable
}

func (c *BackgroundLoadController) backgroundQueueTarget(queue string, total int) int {
	switch queue {
	case "background_validation":
		if total >= 2 {
			return total - max(1, total/4)
		}
		return total
	case "background_alias":
		if total >= 2 {
			return max(1, total/4)
		}
		return total
	default:
		return total
	}
}

func (c *BackgroundLoadController) executionLimit() int {
	switch c.loadLevel(false) {
	case backgroundLoadCritical:
		return backgroundCriticalExecutionCap
	case backgroundLoadBusy:
		return backgroundBusyExecutionCap
	case backgroundLoadModerate:
		return backgroundModerateExecutionCap
	default:
		return backgroundIdleExecutionCap
	}
}

func (c *BackgroundLoadController) loadLevel(ignoreCurrentDispatcher bool) backgroundLoadLevel {
	if c == nil {
		return backgroundLoadIdle
	}
	dbUtilization := c.databaseUtilization()
	foregroundActive, foregroundPending, telemetryReliable := c.foregroundLoad()
	// Dispatchers run on the default foreground queue. While one is asking for
	// a budget, its own active task must not turn an otherwise idle system into
	// moderate load. Execution admission does not use this adjustment, and the
	// process-local dispatcher mutex serializes these budget calculations.
	if ignoreCurrentDispatcher && foregroundActive > 0 {
		foregroundActive--
	}
	foregroundRequests := int(c.foregroundRequests.Load())
	criticalRequests := max(1, c.workerConcurrency/4)
	criticalActive := max(1, c.workerConcurrency/2)
	criticalPending := max(1, c.workerConcurrency)
	busyRequests := max(1, c.workerConcurrency/8)
	busyActive := max(1, c.workerConcurrency/4)
	busyPending := max(1, c.workerConcurrency/2)

	switch {
	case dbUtilization >= backgroundCriticalDBUtilization ||
		foregroundRequests >= criticalRequests ||
		foregroundActive >= criticalActive ||
		foregroundPending >= criticalPending:
		return backgroundLoadCritical
	case dbUtilization >= backgroundBusyDBUtilization ||
		!telemetryReliable ||
		foregroundRequests >= busyRequests ||
		foregroundActive >= busyActive ||
		foregroundPending >= busyPending:
		return backgroundLoadBusy
	case dbUtilization >= backgroundModerateDBUtilization ||
		foregroundRequests > 0 ||
		foregroundActive > 0 ||
		foregroundPending > 0:
		return backgroundLoadModerate
	default:
		return backgroundLoadIdle
	}
}

func (c *BackgroundLoadController) databaseUtilization() float64 {
	if c == nil || c.db == nil {
		return 0
	}
	stats := c.db.Stats()
	if stats.MaxOpenConnections <= 0 {
		return 0
	}
	return float64(stats.InUse) / float64(stats.MaxOpenConnections)
}

func (c *BackgroundLoadController) foregroundLoad() (active, pending int, reliable bool) {
	if c == nil || c.queues == nil {
		return 0, 0, true
	}
	for _, queue := range c.foregroundQueues {
		info, err := c.queues.GetQueueInfo(queue)
		if errors.Is(err, asynq.ErrQueueNotFound) {
			continue
		}
		if err != nil {
			return 0, 0, false
		}
		if info == nil {
			continue
		}
		active += info.Active
		pending += info.Pending + info.Retry
	}
	return active, pending, true
}

func (c *BackgroundLoadController) queueOutstanding(queue string) (int, bool) {
	if c == nil || c.queues == nil || queue == "" {
		return 0, true
	}
	info, err := c.queues.GetQueueInfo(queue)
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	if info == nil {
		return 0, true
	}
	return info.Active + info.Pending + info.Retry, true
}
