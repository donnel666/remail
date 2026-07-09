package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
)

const (
	retentionBatchSize    = 5000
	retentionBatchSleep   = 200 * time.Millisecond
	retentionDailyRunHour = 4
)

type RetentionRepository interface {
	DeleteAPILogsBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteIdempotencyKeysBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteMailmatchMessagesBefore(ctx context.Context, before time.Time, status string, limit int) (int64, error)
	DeleteFetchJobsTerminalBefore(ctx context.Context, before time.Time, limit int) (int64, error)
	ListInboundMailObjectsBefore(ctx context.Context, before time.Time, limit int) ([]RetentionInboundMailObject, error)
	DeleteInboundMailsByID(ctx context.Context, ids []uint64) (int64, error)
}

type RetentionInboundMailObject struct {
	ID        uint64
	ObjectKey string
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
			timer := time.NewTimer(time.Until(next))
			select {
			case <-timer.C:
				s.RunOnce(runCtx)
			case <-runCtx.Done():
				timer.Stop()
				return
			}
		}
	}()
	return func(context.Context) {
		cancel()
		<-done
	}
}

func nextRetentionRunAt(now time.Time, loc *time.Location) time.Time {
	localNow := now.In(loc)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), retentionDailyRunHour, 0, 0, 0, loc)
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
	summary := make([]string, 0, 6)
	summary = append(summary, s.deleteLoop(ctx, "api_logs", now.AddDate(0, 0, -30), func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteAPILogsBefore(ctx, before, retentionBatchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "idempotency_keys", now.AddDate(0, 0, -30), func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteIdempotencyKeysBefore(ctx, before, retentionBatchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "mailmatch_messages_ignored", now.AddDate(0, 0, -30), func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteMailmatchMessagesBefore(ctx, before, "ignored", retentionBatchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "mailmatch_messages_all", now.AddDate(0, 0, -180), func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteMailmatchMessagesBefore(ctx, before, "", retentionBatchSize)
	}))
	summary = append(summary, s.deleteLoop(ctx, "mailmatch_fetch_jobs", now.AddDate(0, 0, -14), func(ctx context.Context, before time.Time) (int64, error) {
		return s.repo.DeleteFetchJobsTerminalBefore(ctx, before, retentionBatchSize)
	}))
	summary = append(summary, s.deleteInboundMails(ctx, now.AddDate(0, 0, -90)))
	s.writeSummary(ctx, strings.Join(summary, "; "))
}

func (s *RetentionService) deleteLoop(ctx context.Context, name string, before time.Time, deleteBatch func(context.Context, time.Time) (int64, error)) string {
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
		if deleted == 0 || deleted < retentionBatchSize {
			return fmt.Sprintf("%s=%d", name, total)
		}
		sleepOrDone(ctx, retentionBatchSleep)
	}
}

func (s *RetentionService) deleteInboundMails(ctx context.Context, before time.Time) string {
	var total int64
	for {
		if ctx.Err() != nil {
			return fmt.Sprintf("inbound_mails=%d canceled", total)
		}
		objects, err := s.repo.ListInboundMailObjectsBefore(ctx, before, retentionBatchSize)
		if err != nil {
			return fmt.Sprintf("inbound_mails=%d error=%s", total, safeRetentionDetail(err))
		}
		if len(objects) == 0 {
			return fmt.Sprintf("inbound_mails=%d", total)
		}
		ids := make([]uint64, 0, len(objects))
		for _, object := range objects {
			if s.files != nil {
				if err := s.files.DeletePrivate(ctx, object.ObjectKey); err != nil {
					return fmt.Sprintf("inbound_mails=%d object_error=%s", total, safeRetentionDetail(err))
				}
			}
			ids = append(ids, object.ID)
		}
		deleted, err := s.repo.DeleteInboundMailsByID(ctx, ids)
		if err != nil {
			return fmt.Sprintf("inbound_mails=%d error=%s", total, safeRetentionDetail(err))
		}
		total += deleted
		if len(objects) < retentionBatchSize {
			return fmt.Sprintf("inbound_mails=%d", total)
		}
		sleepOrDone(ctx, retentionBatchSleep)
	}
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
