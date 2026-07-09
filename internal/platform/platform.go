package platform

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Platform holds all initialized external service clients and shared config.
type Platform struct {
	DB            *gorm.DB
	SQLDB         *sql.DB
	Redis         *redis.Client
	MinIO         *minio.Client
	MinIOBucket   string
	Asynq         *asynq.Client
	AsynqServer   *asynq.Server
	SMTP          SMTPConfig
	SessionMaxAge int
	SessionSecure bool
	Diagnostics   DiagnosticsConfig
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
	p.AsynqServer = initAsynqServer(cfg.Redis)
	p.SMTP = cfg.SMTP

	cleanup := func() {
		slog.Info("shutting down platform clients")
		p.AsynqServer.Shutdown()
		p.Asynq.Close()
		rdb.Close()
		sqlDB.Close()
		slog.Info("platform clients shut down")
	}

	p.SessionMaxAge = cfg.Session.MaxAge
	p.SessionSecure = cfg.Session.Secure
	p.Diagnostics = cfg.Diagnostics

	return p, cleanup, nil
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
		Concurrency: 128,
		Queues: map[string]int{
			"mailfetch":     8,
			"default":       3,
			"mailtransport": 1,
		},
	})
}
