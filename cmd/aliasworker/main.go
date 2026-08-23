// Command aliasworker drains Microsoft explicit-alias tasks while the
// production background_alias queue remains paused. This is intentional:
// the application owns the queue name too, so unpausing it would let both
// processes consume the same backlog.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapi "github.com/donnel666/remail/internal/mailtransport/api"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

const (
	defaultConcurrency         = 30
	defaultPollInterval        = time.Second
	defaultBufferSize          = 1000
	defaultShutdownLimit       = 30 * time.Second
	defaultAliasTimeout        = 20 * time.Minute
	mailboxHistoryWindow       = 90 * 24 * time.Hour
	defaultCredentialTypeRPS   = 3
	defaultCredentialTypeBurst = 1
	defaultStartupInterval     = 2 * time.Second
)

type config struct {
	concurrency         int
	pollInterval        time.Duration
	bufferSize          int
	shutdownTimeout     time.Duration
	exitWhenEmpty       bool
	credentialTypeRPS   int
	credentialTypeBurst int
	startupInterval     time.Duration
}

type aliasTaskResult struct {
	id       string
	deleted  bool
	retryAt  time.Time
	err      error
	category string
}

type progress struct {
	processed atomic.Int64
	deleted   atomic.Int64
	retried   atomic.Int64
	failed    atomic.Int64
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.IntVar(&cfg.concurrency, "concurrency", defaultConcurrency, "number of alias workers")
	flag.DurationVar(&cfg.pollInterval, "poll-interval", defaultPollInterval, "pending queue polling interval")
	flag.IntVar(&cfg.bufferSize, "buffer-size", defaultBufferSize, "maximum queued tasks held by the CMD")
	flag.DurationVar(&cfg.shutdownTimeout, "shutdown-timeout", defaultShutdownLimit, "maximum time to wait for workers on shutdown")
	flag.BoolVar(&cfg.exitWhenEmpty, "exit-when-empty", false, "exit after the paused queue and CMD buffer become empty")
	flag.IntVar(&cfg.credentialTypeRPS, "credential-type-rps", defaultCredentialTypeRPS, "CMD-wide GetCredentialType requests per second; zero disables the gate")
	flag.IntVar(&cfg.credentialTypeBurst, "credential-type-burst", defaultCredentialTypeBurst, "maximum immediate GetCredentialType burst")
	flag.DurationVar(&cfg.startupInterval, "startup-interval", defaultStartupInterval, "delay between starting alias workers")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}

	appCfg, err := platform.Load()
	if err != nil {
		return err
	}
	p, cleanup, err := platform.New(ctx, appCfg)
	if err != nil {
		return fmt.Errorf("initialize platform: %w", err)
	}
	defer cleanup()

	if err := loadRuntimeSettings(ctx, p.DB); err != nil {
		return err
	}
	if err := configureMicrosoftAliasRuntime(ctx, p.DB, p.MinIO, p.MinIOBucket); err != nil {
		return err
	}

	redisOpt := asynq.RedisClientOpt{
		Addr:     appCfg.Redis.Addr,
		Password: appCfg.Redis.Password,
		DB:       appCfg.Redis.DB,
		PoolSize: max(appCfg.Redis.PoolSize, cfg.concurrency*4),
	}
	inspector := asynq.NewInspector(redisOpt)
	defer inspector.Close()
	queue := platform.QueueBackgroundAlias
	info, err := inspector.GetQueueInfo(queue)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", queue, err)
	}
	if !info.Paused {
		return fmt.Errorf("queue %s must remain paused; refusing to process an unpaused queue", queue)
	}
	if info.Active != 0 {
		return fmt.Errorf("queue %s has %d active tasks; stop existing consumers and recover them before starting", queue, info.Active)
	}
	log.Printf("aliasworker attached queue=%s paused=true pending=%d scheduled=%d retry=%d concurrency=%d", queue, info.Pending, info.Scheduled, info.Retry, cfg.concurrency)

	store := mailinfra.NewMicrosoftAliasStore(p.DB)
	proxyModule, err := proxyapi.NewProxyModule(p.DB, p.Asynq)
	if err != nil {
		return fmt.Errorf("initialize proxy module: %w", err)
	}
	creator := mailapi.NewMicrosoftAliasCreationAdapter(proxyModule.ProxyUseCase)
	service := mailapp.NewMicrosoftAliasService(store, nil, creator)

	workCtx := ctx
	if cfg.credentialTypeRPS > 0 {
		limiter := rate.NewLimiter(rate.Limit(cfg.credentialTypeRPS), cfg.credentialTypeBurst)
		workCtx = msacl.WithCredentialTypeRateLimiter(workCtx, limiter.Wait)
		workCtx = msacl.WithCredentialTypeRateLimitRetry(workCtx)
		log.Printf("credential_type_rate_gate rps=%d burst=%d", cfg.credentialTypeRPS, cfg.credentialTypeBurst)
	}
	return drainPausedQueue(workCtx, inspector, store, service, cfg)
}

func validateConfig(cfg config) error {
	if cfg.concurrency < 1 || cfg.concurrency > 512 {
		return errors.New("concurrency must be between 1 and 512")
	}
	if cfg.pollInterval <= 0 || cfg.shutdownTimeout <= 0 || cfg.startupInterval < 0 {
		return errors.New("poll-interval and shutdown-timeout must be positive; startup-interval cannot be negative")
	}
	if cfg.credentialTypeRPS < 0 || cfg.credentialTypeRPS > 1000 || cfg.credentialTypeBurst < 1 || cfg.credentialTypeBurst > 100 {
		return errors.New("credential-type-rps must be between 0 and 1000 and credential-type-burst between 1 and 100")
	}
	if cfg.bufferSize < cfg.concurrency || cfg.bufferSize > 100000 {
		return fmt.Errorf("buffer-size must be between concurrency (%d) and 100000", cfg.concurrency)
	}
	return nil
}

func loadRuntimeSettings(ctx context.Context, db *gorm.DB) error {
	settings, err := infra.NewRepository(db).List(ctx)
	if err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	runtimeconfig.Replace(settings)
	return nil
}

func configureMicrosoftAliasRuntime(ctx context.Context, db *gorm.DB, minioClient *minio.Client, bucket string) error {
	resources := coreinfra.NewResourceRepo(db)
	domains, allocationDomains, err := resources.ListBindingDomains(ctx)
	if err != nil {
		return fmt.Errorf("load auxiliary binding domains: %w", err)
	}
	if len(domains) == 0 {
		return errors.New("no normal binding-purpose domains are configured")
	}
	msacl.SetAuxiliaryDomainPolicy(domains, allocationDomains)
	files := governanceinfra.NewMinIOFileStore(minioClient, bucket)
	msacl.SetMailboxReader(mailinfra.NewMSACLMailboxReaderWithContentWindow(db, files, mailboxHistoryWindow))
	msacl.SetRecoveryLeaseStore(mailinfra.NewMicrosoftBindingRecoveryLeaseStore(db))
	return nil
}

func drainPausedQueue(
	ctx context.Context,
	inspector *asynq.Inspector,
	store *mailinfra.MicrosoftAliasStore,
	service *mailapp.MicrosoftAliasService,
	cfg config,
) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	work := make(chan *asynq.TaskInfo, cfg.bufferSize)
	results := make(chan aliasTaskResult, cfg.bufferSize)
	var workers sync.WaitGroup
	var stats progress
	for i := 0; i < cfg.concurrency; i++ {
		workers.Add(1)
		workerIndex := i
		go func() {
			defer workers.Done()
			if workerIndex > 0 && cfg.startupInterval > 0 {
				timer := time.NewTimer(time.Duration(workerIndex) * cfg.startupInterval)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-workCtx.Done():
					return
				}
			}
			aliasWorker(workCtx, inspector, store, service, work, results, &stats, cfg.shutdownTimeout)
		}()
	}

	assigned := make(map[string]struct{}, cfg.bufferSize)
	retryAfter := make(map[string]time.Time, cfg.bufferSize)
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()
	lastProcessed := int64(0)
	lastLog := time.Now()

	stopWorkers := func() {
		cancel()
		close(work)
		workers.Wait()
	}
	defer stopWorkers()

	for {
		drainResults(results, assigned, retryAfter)
		info, err := inspector.GetQueueInfo(platform.QueueBackgroundAlias)
		if err != nil {
			return fmt.Errorf("inspect paused alias queue: %w", err)
		}
		if !info.Paused {
			return errors.New("background_alias was unpaused while aliasworker was running; workers stopped for safety")
		}
		if now := time.Now(); now.Sub(lastLog) >= time.Minute {
			current := stats.processed.Load()
			log.Printf("aliasworker progress pending=%d active=%d scheduled=%d retry=%d processed=%d deleted=%d retried=%d failed=%d throughput=%d/min assigned=%d", info.Pending, info.Active, info.Scheduled, info.Retry, current, stats.deleted.Load(), stats.retried.Load(), stats.failed.Load(), current-lastProcessed, len(assigned))
			lastProcessed = current
			lastLog = now
		}
		if cfg.exitWhenEmpty && info.Pending == 0 && info.Active == 0 && info.Scheduled == 0 && info.Retry == 0 && len(assigned) == 0 && len(work) == 0 {
			return nil
		}

		available := cfg.bufferSize - len(assigned)
		if available > 0 {
			batchSize := min(available, 200)
			tasks, listErr := inspector.ListPendingTasks(platform.QueueBackgroundAlias, asynq.PageSize(batchSize), asynq.Page(1))
			if listErr != nil {
				return fmt.Errorf("list paused alias tasks: %w", listErr)
			}
			for _, task := range tasks {
				if task == nil || strings.TrimSpace(task.ID) == "" {
					continue
				}
				if _, ok := assigned[task.ID]; ok {
					continue
				}
				if until, ok := retryAfter[task.ID]; ok && time.Now().Before(until) {
					continue
				}
				select {
				case work <- task:
					assigned[task.ID] = struct{}{}
				default:
					break
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case result := <-results:
			applyResult(result, assigned, retryAfter)
		case <-ticker.C:
		}
	}
}

func aliasWorker(
	ctx context.Context,
	inspector *asynq.Inspector,
	store *mailinfra.MicrosoftAliasStore,
	service *mailapp.MicrosoftAliasService,
	work <-chan *asynq.TaskInfo,
	results chan<- aliasTaskResult,
	stats *progress,
	shutdownTimeout time.Duration,
) {
	for task := range work {
		result := processAliasTask(ctx, store, service, task, shutdownTimeout)
		if result.deleted {
			if err := inspector.DeleteTask(platform.QueueBackgroundAlias, task.ID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "task not found") {
				result.deleted = false
				result.retryAt = time.Now().Add(5 * time.Second)
				result.err = errors.Join(result.err, fmt.Errorf("delete task: %w", err))
			}
		}
		if result.deleted {
			stats.deleted.Add(1)
		}
		if !result.retryAt.IsZero() {
			stats.retried.Add(1)
		}
		if result.err != nil {
			stats.failed.Add(1)
		}
		stats.processed.Add(1)
		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
	}
}

func processAliasTask(ctx context.Context, store *mailinfra.MicrosoftAliasStore, service *mailapp.MicrosoftAliasService, task *asynq.TaskInfo, shutdownTimeout time.Duration) aliasTaskResult {
	result := aliasTaskResult{id: task.ID}
	if task.Type != mailinfra.TypeMicrosoftAlias {
		result.deleted = true
		result.category = "unsupported_task"
		return result
	}
	var payload mailapp.MicrosoftAliasTask
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.ResourceID == 0 || payload.Generation == 0 {
		result.deleted = true
		result.category = "invalid_payload"
		return result
	}
	taskTimeout := task.Timeout
	if taskTimeout <= 0 {
		taskTimeout = defaultAliasTimeout
	}
	taskCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	// The standalone CMD has enough information to safely use concrete
	// recovery-recipient lease keys. The production APP/weekly dispatcher does
	// not inherit this context and keeps its existing scheduling policy.
	taskCtx = msacl.WithConcreteRecoveryLeaseKeys(taskCtx)
	err := service.Process(taskCtx, payload)
	cancel()
	if err == nil {
		result.deleted = true
		result.category = "processed"
		return result
	}
	log.Printf("aliasworker task failed resource_id=%d generation=%d error=%v", payload.ResourceID, payload.Generation, err)
	if ctx.Err() != nil {
		result.retryAt = time.Now().Add(5 * time.Second)
		result.err = ctx.Err()
		return result
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	releaseErr := store.MarkDispatchFailed(
		releaseCtx,
		payload,
		time.Now().UTC().Add(runtimeconfig.Duration("legacy_alias_retry_delay_seconds", 30*time.Second, time.Second, 1)),
		"Microsoft alias task infrastructure failed; dispatcher will retry.",
	)
	releaseCancel()
	if releaseErr != nil {
		result.retryAt = time.Now().Add(5 * time.Second)
		result.err = errors.Join(err, releaseErr)
		return result
	}
	// Match the production handler: once the durable schedule has been
	// released, the old Asynq envelope is complete and the dispatcher creates
	// the next one at its persisted next_run_at.
	result.deleted = true
	result.category = "released_for_retry"
	result.err = err
	return result
}

func drainResults(results <-chan aliasTaskResult, assigned map[string]struct{}, retryAfter map[string]time.Time) {
	for {
		select {
		case result := <-results:
			applyResult(result, assigned, retryAfter)
		default:
			return
		}
	}
}

func applyResult(result aliasTaskResult, assigned map[string]struct{}, retryAfter map[string]time.Time) {
	delete(assigned, result.id)
	if result.retryAt.IsZero() {
		delete(retryAfter, result.id)
		return
	}
	retryAfter[result.id] = result.retryAt
}
