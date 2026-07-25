package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type GormLogger struct {
	level         gormlogger.LogLevel
	parameterized bool
}

func NewGormLogger() *GormLogger {
	return &GormLogger{
		level:         gormlogger.Warn,
		parameterized: true,
	}
}

func (l *GormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	next := *l
	next.level = level
	return &next
}

func (l *GormLogger) Info(ctx context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Info {
		Logger(ctx).Info(fmt.Sprintf(msg, data...))
	}
}

func (l *GormLogger) Warn(ctx context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Warn {
		Logger(ctx).Warn(fmt.Sprintf(msg, data...))
	}
}

func (l *GormLogger) Error(ctx context.Context, msg string, data ...any) {
	if l.level >= gormlogger.Error {
		Logger(ctx).Error(fmt.Sprintf(msg, data...))
	}
}

func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	slowThreshold := runtimeconfig.Duration("slow_sql_threshold_ms", 200*time.Millisecond, time.Millisecond, 0)

	switch {
	case err != nil && l.level >= gormlogger.Error && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		Logger(ctx).Error(
			"sql error",
			"error", err,
			"elapsed_ms", elapsed.Seconds()*1000,
			"rows", rows,
			"sql", sql,
		)
	case slowThreshold > 0 && elapsed > slowThreshold && l.level >= gormlogger.Warn:
		sql, rows := fc()
		Logger(ctx).Warn(
			"slow sql",
			"elapsed_ms", elapsed.Seconds()*1000,
			"threshold_ms", slowThreshold.Seconds()*1000,
			"rows", rows,
			"sql", sql,
		)
	case l.level >= gormlogger.Info:
		sql, rows := fc()
		Logger(ctx).Info(
			"sql",
			"elapsed_ms", elapsed.Seconds()*1000,
			"rows", rows,
			"sql", sql,
		)
	}
}

func (l *GormLogger) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	if l.parameterized {
		return sql, nil
	}
	return sql, params
}

var _ gormlogger.Interface = (*GormLogger)(nil)
var _ interface {
	ParamsFilter(context.Context, string, ...any) (string, []any)
} = (*GormLogger)(nil)
