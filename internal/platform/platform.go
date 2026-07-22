package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	asynqWorkerConcurrency           = 768
	asynqRealtimeWorkerConcurrency   = 256
	asynqBackgroundWorkerConcurrency = 512
	asynqShutdownTimeout             = 30 * time.Second
	backgroundRetryDelayMinimum      = 5 * time.Second
	backgroundRetryDelayJitter       = 5 * time.Second
)

// realtimeQueueConfig holds the latency-critical queues that back
// trade/接码 delivery. They run on a dedicated worker pool so a burst of
// long-running lower-priority tasks in the shared pool can never starve them.
func realtimeQueueConfig() map[string]int {
	return map[string]int{
		QueueMailfetch: 1,
	}
}

// foregroundQueueConfig uses weighted fairness. Realtime code pickup has its
// own pool, so the shared pool must never starve mailtransport/default work.
func foregroundQueueConfig() map[string]int {
	return map[string]int{
		QueueMailtransport: 4,
		QueueDefault:       3,
	}
}

// backgroundQueueConfig holds bulk, best-effort work that must yield to the
// realtime and foreground tiers.
func backgroundQueueConfig() map[string]int {
	return map[string]int{
		QueueBackgroundValidation:     3,
		QueueBackgroundAlias:          1,
		QueueBackgroundTokenRefresh:   1,
		QueueBackgroundProjectHistory: 1,
		QueueBackgroundInventory:      1,
		// Admin resource bulk operations (validate/publish/unpublish/delete) are
		// enqueued to the resource queue; without it here no server consumes them
		// and every bulk command sits queued forever.
		QueueResource: 2,
	}
}

// Platform holds all initialized external service clients and shared config.
type Platform struct {
	DB                    *gorm.DB
	SQLDB                 *sql.DB
	Redis                 *redis.Client
	MinIO                 *minio.Client
	MinIOBucket           string
	Asynq                 *asynq.Client
	RealtimeAsynqServer   *asynq.Server
	AsynqServer           *asynq.Server
	BackgroundAsynqServer *asynq.Server
	BackgroundLoad        *BackgroundLoadController
	SMTP                  SMTPConfig
	TrustedProxies        []string
	SessionMaxAge         int
	SessionSecure         bool
	Turnstile             TurnstileConfig
	Diagnostics           DiagnosticsConfig
	workersReady          atomic.Bool
	workerStop            sync.Once
	clientClose           sync.Once
}

// New initializes all external service clients and returns the Platform.
// The returned cleanup function should be deferred to close connections gracefully.
func New(ctx context.Context, cfg *Config) (*Platform, func(), error) {
	p := &Platform{}

	db, sqlDB, err := initMySQL(ctx, cfg.MySQL, cfg.Diagnostics.SlowSQLThreshold)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql init: %w", err)
	}
	p.DB = db
	p.SQLDB = sqlDB

	rdb, err := initRedis(ctx, cfg.Redis)
	if err != nil {
		sqlDB.Close()
		return nil, nil, fmt.Errorf("redis init: %w", err)
	}
	p.Redis = rdb

	mc, err := initMinIO(cfg.MinIO)
	if err != nil {
		sqlDB.Close()
		rdb.Close()
		return nil, nil, fmt.Errorf("minio init: %w", err)
	}
	p.MinIO = mc
	p.MinIOBucket = cfg.MinIO.Bucket

	p.Asynq = initAsynq(cfg.Redis)
	p.RealtimeAsynqServer = initRealtimeAsynqServer(cfg.Redis)
	p.AsynqServer = initAsynqServer(cfg.Redis)
	p.BackgroundAsynqServer = initBackgroundAsynqServer(cfg.Redis)
	p.BackgroundLoad = NewBackgroundLoadController(asynqBackgroundWorkerConcurrency)
	SetMetricsBackgroundLoad(p.BackgroundLoad)
	p.SMTP = cfg.SMTP
	p.TrustedProxies = append([]string(nil), cfg.Server.TrustedProxies...)

	cleanup := func() {
		p.Close()
	}

	p.SessionMaxAge = cfg.Session.MaxAge
	p.SessionSecure = cfg.Session.Secure
	p.Turnstile = cfg.Turnstile
	p.Diagnostics = cfg.Diagnostics

	return p, cleanup, nil
}

func (p *Platform) ShutdownWorkers() {
	if p == nil {
		return
	}
	p.workerStop.Do(func() {
		p.workersReady.Store(false)
		slog.Info("shutting down task workers")
		var workers sync.WaitGroup
		shutdown := func(server *asynq.Server) {
			if server == nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				server.Shutdown()
			}()
		}
		shutdown(p.RealtimeAsynqServer)
		shutdown(p.AsynqServer)
		shutdown(p.BackgroundAsynqServer)
		workers.Wait()
		slog.Info("task workers shut down")
	})
}

func (p *Platform) MarkWorkersReady() {
	if p != nil {
		p.workersReady.Store(true)
	}
}

func (p *Platform) WorkersReady() bool {
	return p != nil && p.workersReady.Load()
}

func (p *Platform) Close() {
	if p == nil {
		return
	}
	p.clientClose.Do(func() {
		slog.Info("shutting down platform clients")
		if p.BackgroundLoad != nil {
			p.BackgroundLoad.Stop(context.Background())
		}
		p.ShutdownWorkers()
		if p.Asynq != nil {
			_ = p.Asynq.Close()
		}
		if p.Redis != nil {
			_ = p.Redis.Close()
		}
		if p.SQLDB != nil {
			_ = p.SQLDB.Close()
		}
		slog.Info("platform clients shut down")
	})
}

func initMySQL(ctx context.Context, cfg MySQLConfig, slowSQLThreshold time.Duration) (*gorm.DB, *sql.DB, error) {
	formattedDSN, err := mysqlDSN(cfg.DSN)
	if err != nil {
		return nil, nil, err
	}
	gormCfg := &gorm.Config{
		Logger:         NewGormLogger(slowSQLThreshold).LogMode(gormlogger.Warn),
		TranslateError: true, // Map MySQL errors (e.g. 1062 duplicate) to gorm sentinels
	}

	db, err := gorm.Open(gormmysql.Open(formattedDSN), gormCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("gorm open: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("get sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	sqlDB.SetConnMaxIdleTime(2 * time.Minute)

	// Verify connectivity
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("mysql ping: %w", err)
	}

	return db, sqlDB, nil
}

func mysqlDSN(raw string) (string, error) {
	dsn, err := mysqldriver.ParseDSN(raw)
	if err != nil {
		return "", fmt.Errorf("parse mysql dsn: %w", err)
	}
	// The production workload previously prepared almost every parameterized
	// statement exactly once. Driver-side interpolation removes that extra
	// round trip without an unbounded prepared-statement cache.
	dsn.InterpolateParams = true
	return dsn.FormatDSN(), nil
}

func initRedis(ctx context.Context, cfg RedisConfig) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return rdb, nil
}

func initMinIO(cfg MinIOConfig) (*minio.Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio new: %w", err)
	}

	return mc, nil
}

func initAsynq(cfg RedisConfig) *asynq.Client {
	return asynq.NewClient(asynqRedisOptions(cfg))
}

func initAsynqServer(cfg RedisConfig) *asynq.Server {
	return asynq.NewServer(asynqRedisOptions(cfg), asynq.Config{
		Concurrency:     asynqWorkerConcurrency,
		Queues:          foregroundQueueConfig(),
		StrictPriority:  false, // weighted polling prevents a large queue from starving another queue
		ShutdownTimeout: asynqShutdownTimeout,
	})
}

// initRealtimeAsynqServer runs a dedicated pool that only serves the
// latency-critical 接码 queue. Because nothing else can occupy these workers,
// verification-code fetches always have capacity even when the shared pool is
// saturated by long-running bulk tasks.
func initRealtimeAsynqServer(cfg RedisConfig) *asynq.Server {
	return asynq.NewServer(asynqRedisOptions(cfg), asynq.Config{
		Concurrency:     asynqRealtimeWorkerConcurrency,
		Queues:          realtimeQueueConfig(),
		StrictPriority:  false,
		ShutdownTimeout: asynqShutdownTimeout,
	})
}

func initBackgroundAsynqServer(cfg RedisConfig) *asynq.Server {
	return asynq.NewServer(asynqRedisOptions(cfg), asynq.Config{
		Concurrency:     asynqBackgroundWorkerConcurrency,
		Queues:          backgroundQueueConfig(),
		StrictPriority:  false, // every non-empty background queue keeps a weighted share
		RetryDelayFunc:  backgroundRetryDelay,
		IsFailure:       backgroundIsFailure,
		ShutdownTimeout: asynqShutdownTimeout,
	})
}

func asynqRedisOptions(cfg RedisConfig) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	}
}

func backgroundRetryDelay(retried int, err error, task *asynq.Task) time.Duration {
	if errors.Is(err, ErrBackgroundExecutionDeferred) {
		hash := fnv.New32a()
		if task != nil {
			_, _ = hash.Write([]byte(task.Type()))
			_, _ = hash.Write(task.Payload())
		}
		jitterSteps := uint32(backgroundRetryDelayJitter/time.Second) + 1
		return backgroundRetryDelayMinimum + time.Duration(hash.Sum32()%jitterSteps)*time.Second
	}
	return asynq.DefaultRetryDelayFunc(retried, err, task)
}

func backgroundIsFailure(err error) bool {
	return !errors.Is(err, ErrBackgroundExecutionDeferred)
}
