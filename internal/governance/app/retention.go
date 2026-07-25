package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	retentionBatchSize    = 5000
	retentionBatchSleep   = 200 * time.Millisecond
	retentionDailyRunHour = 4
	inboundObjectPrefix   = "mailtransport/inbound/"
)

type RetentionRepository interface {
	DeleteIdempotencyKeysBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteMailmatchMessagesBefore(ctx context.Context, before time.Time, resourceType string, limit int) (int64, error)
	DeleteAllocationDailyUsagesBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteOutboundMailsTerminalBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteSystemLogsBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	ListInboundMailObjectsBefore(ctx context.Context, before time.Time, limit int) ([]RetentionInboundMailObject, error)
	ListExistingInboundObjectKeys(ctx context.Context, objectKeys []string) (map[string]struct{}, error)
	DeleteInboundMailsByID(ctx context.Context, ids []uint64) (int64, error)
}

type RetentionInboundMailObject struct {
	ID        uint64
	ObjectKey string
}

type retentionSettings struct {
	batchSize           int
	batchSleep          time.Duration
	idempotencyDays     int
	mailmatchMSDays     int
	mailmatchDomainDays int
	dailyUsageDays      int
	outboundMailDays    int
	inboundMailDays     int
	systemLogDays       int
}

func currentRetentionSettings() retentionSettings {
	settings := runtimeconfig.Snapshot()
	return retentionSettings{
		batchSize:           min(settings.Int("retention_batch_size", retentionBatchSize, 1), 100000),
		batchSleep:          settings.Duration("retention_batch_sleep_ms", retentionBatchSleep, time.Millisecond, 0),
		idempotencyDays:     settings.Int("idempotency_key_retain_days", 30, 1),
		mailmatchMSDays:     settings.Int("mailmatch_ms_retain_days", 3, 1),
		mailmatchDomainDays: settings.Int("mailmatch_domain_retain_days", 30, 1),
		dailyUsageDays:      settings.Int("daily_usage_retain_days", 14, 1),
		outboundMailDays:    settings.Int("outbound_mail_retain_days", 30, 1),
		inboundMailDays:     settings.Int("inbound_mail_retain_days", 30, 1),
		systemLogDays:       settings.Int("system_log_retain_days", 30, 1),
	}
}

type RetentionService struct {
	repo  RetentionRepository
	files FilePort
	logs  SystemLogPort
	now   func() time.Time
}

func NewRetentionService(repo RetentionRepository, files FilePort, logs SystemLogPort) *RetentionService {
	return &RetentionService{
		repo:  repo,
		files: files,
		logs:  logs,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func (s *RetentionService) StartDaily(ctx context.Context, loc *time.Location) func(context.Context) {
	if s == nil || s.repo == nil {
		return func(context.Context) {}
	}
	if loc == nil {
		loc = time.Local
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			next := nextRetentionRunAt(s.now(), loc)
			wait := time.Until(next)
			if wait > time.Minute {
				wait = time.Minute
			}
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
				if !s.now().Before(next) {
					s.RunOnce(runCtx)
				}
			case <-runCtx.Done():
				timer.Stop()
				return
			}
		}
	}()
	return func(shutdownCtx context.Context) {
		cancel()
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func nextRetentionRunAt(now time.Time, loc *time.Location) time.Time {
	localNow := now.In(loc)
	hour := runtimeconfig.Int("retention_daily_run_hour", retentionDailyRunHour, 0)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, 0, 0, 0, loc)
	if !next.After(localNow) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (s *RetentionService) RunOnce(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	now := s.now()
	settings := currentRetentionSettings()
	summary := make([]string, 0, 12)
	summary = append(summary, s.deleteLoop(ctx, "idempotency_keys", now.AddDate(0, 0, -settings.idempotencyDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteIdempotencyKeysBefore(ctx, before, settings.batchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "mailmatch_messages_microsoft", now.AddDate(0, 0, -settings.mailmatchMSDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteMailmatchMessagesBefore(ctx, before, "microsoft", settings.batchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "mailmatch_messages_domain", now.AddDate(0, 0, -settings.mailmatchDomainDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteMailmatchMessagesBefore(ctx, before, "domain", settings.batchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "allocation_daily_usages", now.AddDate(0, 0, -settings.dailyUsageDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteAllocationDailyUsagesBefore(ctx, before, settings.batchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "outbound_mails", now.AddDate(0, 0, -settings.outboundMailDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteOutboundMailsTerminalBefore(ctx, before, settings.batchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "system_logs", now.AddDate(0, 0, -settings.systemLogDays), settings, func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteSystemLogsBefore(ctx, before, settings.batchSize)
	}))
	inboundBefore := now.AddDate(0, 0, -settings.inboundMailDays)
	summary = append(summary, s.deleteInboundMails(ctx, inboundBefore, settings))
	summary = append(summary, s.deleteOrphanInboundObjects(ctx, inboundBefore, settings))
	s.writeSummary(ctx, strings.Join(summary, "; "))
	platform.RecordBusinessEvent("retention", "completed")
}

func (s *RetentionService) deleteLoop(ctx context.Context, name string, before time.Time, settings retentionSettings, deleteBatch func(context.Context, time.Time) (int64, error)) string {
	var total int64
	for {
		if ctx.Err() != nil {
			return fmt.Sprintf("%s=%d canceled", name, total)
		}
		deleted, err := deleteBatch(ctx, before)
		if err != nil {
			return fmt.Sprintf("%s=%d error=%s", name, total, safeRetentionDetail(err))
		}
		total += deleted
		if deleted == 0 || deleted < int64(settings.batchSize) {
			return fmt.Sprintf("%s=%d", name, total)
		}
		sleepOrDone(ctx, settings.batchSleep)
	}
}

func (s *RetentionService) deleteInboundMails(ctx context.Context, before time.Time, settings retentionSettings) string {
	var total int64
	for {
		if ctx.Err() != nil {
			return fmt.Sprintf("inbound_mails=%d canceled", total)
		}
		objects, err := s.repo.ListInboundMailObjectsBefore(ctx, before, settings.batchSize)
		if err != nil {
			return fmt.Sprintf("inbound_mails=%d error=%s", total, safeRetentionDetail(err))
		}
		if len(objects) == 0 {
			return fmt.Sprintf("inbound_mails=%d", total)
		}
		ids := make([]uint64, 0, len(objects))
		for _, object := range objects {
			ids = append(ids, object.ID)
		}
		deleted, err := s.repo.DeleteInboundMailsByID(ctx, ids)
		if err != nil {
			return fmt.Sprintf("inbound_mails=%d error=%s", total, safeRetentionDetail(err))
		}
		total += deleted
		if s.files != nil {
			for _, object := range objects {
				if err := s.files.DeletePrivate(ctx, object.ObjectKey); err != nil {
					return fmt.Sprintf("inbound_mails=%d object_error=%s", total, safeRetentionDetail(err))
				}
			}
		}
		if len(objects) < settings.batchSize {
			return fmt.Sprintf("inbound_mails=%d", total)
		}
		sleepOrDone(ctx, settings.batchSleep)
	}
}

func (s *RetentionService) deleteOrphanInboundObjects(ctx context.Context, before time.Time, settings retentionSettings) string {
	if s.files == nil {
		return "inbound_orphans=0 no_file_store"
	}
	var total int64
	startAfter := ""
	for {
		if ctx.Err() != nil {
			return fmt.Sprintf("inbound_orphans=%d canceled", total)
		}
		objects, err := s.files.ListPrivate(ctx, inboundObjectPrefix, startAfter, settings.batchSize)
		if err != nil {
			return fmt.Sprintf("inbound_orphans=%d error=%s", total, safeRetentionDetail(err))
		}
		if len(objects) == 0 {
			return fmt.Sprintf("inbound_orphans=%d", total)
		}
		candidates := make([]string, 0, len(objects))
		for _, object := range objects {
			if inboundObjectBefore(object, before) {
				candidates = append(candidates, object.ObjectKey)
			}
		}
		if len(candidates) > 0 {
			existing, err := s.repo.ListExistingInboundObjectKeys(ctx, candidates)
			if err != nil {
				return fmt.Sprintf("inbound_orphans=%d error=%s", total, safeRetentionDetail(err))
			}
			for _, objectKey := range candidates {
				if ctx.Err() != nil {
					return fmt.Sprintf("inbound_orphans=%d canceled", total)
				}
				if _, ok := existing[objectKey]; ok {
					continue
				}
				if err := s.files.DeletePrivate(ctx, objectKey); err != nil {
					return fmt.Sprintf("inbound_orphans=%d object_error=%s", total, safeRetentionDetail(err))
				}
				total++
			}
		}
		if len(objects) < settings.batchSize {
			return fmt.Sprintf("inbound_orphans=%d", total)
		}
		startAfter = objects[len(objects)-1].ObjectKey
		sleepOrDone(ctx, settings.batchSleep)
	}
}

func inboundObjectBefore(object domain.PrivateObject, before time.Time) bool {
	if strings.TrimSpace(object.ObjectKey) == "" {
		return false
	}
	if !object.LastModified.IsZero() {
		return object.LastModified.Before(before)
	}
	createdAt, ok := inboundObjectDate(object.ObjectKey)
	return ok && createdAt.Before(before)
}

func inboundObjectDate(objectKey string) (time.Time, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(objectKey), inboundObjectPrefix)
	parts := strings.Split(trimmed, "/")
	if len(parts) < 4 {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006/01/02", strings.Join(parts[:3], "/"), time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *RetentionService) writeSummary(ctx context.Context, detail string) {
	if s.logs == nil {
		return
	}
	_ = s.logs.Create(ctx, &domain.SystemLog{
		Level:     "info",
		Module:    "governance",
		EventType: "governance.retention_completed",
		BizType:   "retention",
		BizID:     "daily",
		Message:   "Daily retention cleanup completed.",
		Detail:    strings.TrimSpace(detail),
	})
}

func safeRetentionDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.TrimSpace(err.Error())
	if len(detail) > 300 {
		return detail[:300]
	}
	return detail
}
