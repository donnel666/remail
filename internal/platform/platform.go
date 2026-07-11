package platform

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	asynqWorkerConcurrency           = 64
	asynqRealtimeWorkerConcurrency   = 32
	asynqBackgroundWorkerConcurrency = 32
)

// realtimeQueueConfig holds the latency-critical queues that back
// trade/接码 delivery. They run on a dedicated worker pool so a burst of
// long-running lower-priority tasks in the shared pool can never starve them.
func realtimeQueueConfig() map[string]int {
	return map[string]int{
		"mailfetch": 1,
	}
}

// foregroundQueueConfig uses weighted fairness. Realtime code pickup has its
// own pool, so the shared pool must never starve mailtransport/default work.
func foregroundQueueConfig() map[string]int {
	return map[string]int{
		"mailtransport": 4,
		"default":       3,
	}
}

// backgroundQueueConfig holds bulk, best-effort work that must yield to the
// realtime and foreground tiers.
func backgroundQueueConfig() map[string]int {
	return map[string]int{
		"background_validation": 3,
		"background_alias":      1,
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
	SessionMaxAge         int
	SessionSecure         bool
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
	p.BackgroundLoad = NewBackgroundLoadController(
		sqlDB,
		asynq.NewInspectorFromRedisClient(rdb),
		rdb,
		asynqWorkerConcurrency+asynqRealtimeWorkerConcurrency,
	)
	p.SMTP = cfg.SMTP

	cleanup := func() {
		p.Close()
	}

	p.SessionMaxAge = cfg.Session.MaxAge
	p.SessionSecure = cfg.Session.Secure
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
	gormCfg := &gorm.Config{
		Logger:         NewGormLogger(slowSQLThreshold).LogMode(gormlogger.Warn),
		TranslateError: true, // Map MySQL errors (e.g. 1062 duplicate) to gorm sentinels
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), gormCfg)
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

func initRedis(ctx context.Context, cfg RedisConfig) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
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
	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	return asynq.NewClient(redisOpt)
}

func initAsynqServer(cfg RedisConfig) *asynq.Server {
	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	return asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: asynqWorkerConcurrency,
		Queues:      foregroundQueueConfig(),
	})
}

// initRealtimeAsynqServer runs a dedicated pool that only serves the
// latency-critical 接码 queue. Because nothing else can occupy these workers,
// verification-code fetches always have capacity even when the shared pool is
// saturated by long-running bulk tasks.
func initRealtimeAsynqServer(cfg RedisConfig) *asynq.Server {
	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	return asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: asynqRealtimeWorkerConcurrency,
		Queues:      realtimeQueueConfig(),
	})
}

func initBackgroundAsynqServer(cfg RedisConfig) *asynq.Server {
	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	return asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: asynqBackgroundWorkerConcurrency,
		Queues:      backgroundQueueConfig(),
	})
}
