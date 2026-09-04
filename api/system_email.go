package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const typeAnnouncementBroadcast = "system:announcement_broadcast"

const (
	systemLoadAlertInterval        = 2 * time.Second
	systemLoadAlertConfirmWindow   = 10 * time.Minute
	systemLoadAlertRetryInterval   = time.Minute
	systemLoadAlertStateTTL        = time.Hour
	systemLoadAlertLeaseTTL        = time.Minute
	systemLoadAlertRefreshInterval = time.Minute
)

var systemLoadAlertThresholds = [...]int{80, 90, 95, 96, 97, 98, 99, 100}

type announcementBroadcastTask struct {
	ID        int64  `json:"id"`
	StartTime string `json:"startTime"`
}

type announcementRecipientSource interface {
	ListByFilter(ctx context.Context, filter iamdomain.UserListFilter, offset, limit int) ([]iamdomain.User, error)
}

type announcementMailer struct {
	users    announcementRecipientSource
	delivery mailapp.DeliveryPort
	client   *asynq.Client
}

type systemLoadAlerter struct {
	users    announcementRecipientSource
	delivery mailapp.DeliveryPort
	redis    redis.UniversalClient
	hostname string

	mu           sync.Mutex
	initialized  bool
	cached       systemLoadAlertState
	refreshAt    time.Time
	redisRetryAt time.Time
	interrupted  bool
}

type systemLoadAlertState struct {
	EpisodeID     string                 `json:"episodeId,omitempty"`
	ExceededSince time.Time              `json:"exceededSince,omitempty"`
	NextRetryAt   time.Time              `json:"nextRetryAt,omitempty"`
	Pending       []systemLoadAlertEvent `json:"pending,omitempty"`
}

type systemLoadAlertEvent struct {
	EpisodeID     string    `json:"episodeId"`
	Threshold     int       `json:"threshold"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemoryPercent float64   `json:"memoryPercent"`
	MemoryValid   bool      `json:"memoryValid"`
	ObservedAt    time.Time `json:"observedAt"`
}

func startSystemLoadAlerts(parent context.Context, source *platform.BackgroundLoadController, alerter *systemLoadAlerter) func(context.Context) {
	if source == nil || alerter == nil {
		return func(context.Context) {}
	}
	if parent == nil {
		parent = context.Background()
	}
	if strings.TrimSpace(alerter.hostname) == "" {
		alerter.hostname, _ = os.Hostname()
	}
	if strings.TrimSpace(alerter.hostname) == "" {
		alerter.hostname = "unknown"
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(systemLoadAlertInterval)
		defer ticker.Stop()
		for {
			if err := alerter.observe(ctx, source.Snapshot()); err != nil {
				slog.Error("system load alert failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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

func (a *systemLoadAlerter) observe(ctx context.Context, snapshot platform.BackgroundLoadSnapshot) (resultErr error) {
	if a == nil || a.users == nil || a.delivery == nil || a.redis == nil {
		return nil
	}
	observedAt := snapshot.SampledAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	observedAt = observedAt.UTC()

	a.mu.Lock()
	defer a.mu.Unlock()
	if (!snapshot.CPUValid || snapshot.CPUPercent < float64(systemLoadAlertThresholds[0])) &&
		(a.cached.EpisodeID != "" || !a.cached.ExceededSince.IsZero()) {
		a.interrupted = true
	}
	if !a.shouldRefresh(snapshot, observedAt) {
		return nil
	}
	if strings.TrimSpace(a.hostname) == "" {
		a.hostname, _ = os.Hostname()
	}
	if strings.TrimSpace(a.hostname) == "" {
		a.hostname = "unknown"
	}

	stateKey := systemLoadAlertStateKey(a.hostname)
	lockKey := stateKey + ":lock"
	token := platform.NewUUIDV7String()
	locked, err := a.redis.SetNX(ctx, lockKey, token, systemLoadAlertLeaseTTL).Result()
	if err != nil {
		a.redisRetryAt = observedAt.Add(systemLoadAlertRetryInterval)
		return fmt.Errorf("acquire system load alert lease: %w", err)
	}
	if !locked {
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := systemLoadAlertReleaseScript.Run(releaseCtx, a.redis, []string{lockKey}, token).Err(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release system load alert lease: %w", err))
		}
	}()

	state, err := loadSystemLoadAlertState(ctx, a.redis, stateKey)
	if err != nil {
		a.redisRetryAt = observedAt.Add(systemLoadAlertRetryInterval)
		return err
	}
	changed := false
	if a.interrupted {
		changed = state.resetEpisode()
	}
	changed = state.apply(snapshot, observedAt) || changed
	ready := len(state.Pending) > 0 && (state.NextRetryAt.IsZero() || !observedAt.Before(state.NextRetryAt))
	if ready {
		state.NextRetryAt = observedAt.Add(systemLoadAlertRetryInterval)
		changed = true
	}
	if changed || state.EpisodeID != "" || !state.ExceededSince.IsZero() || len(state.Pending) > 0 {
		if err := storeSystemLoadAlertState(ctx, a.redis, stateKey, state); err != nil {
			a.redisRetryAt = observedAt.Add(systemLoadAlertRetryInterval)
			return err
		}
	}
	a.interrupted = false
	a.cache(state, observedAt)
	if !ready {
		return nil
	}

	role, enabled := iamdomain.RoleSuperAdmin, true
	users, err := a.users.ListByFilter(ctx, iamdomain.UserListFilter{Role: &role, Enabled: &enabled}, 0, -1)
	if err != nil {
		return fmt.Errorf("list super administrators for system load alert: %w", err)
	}
	remaining := make([]systemLoadAlertEvent, 0, len(state.Pending))
	var sendErrors []error
	for _, event := range state.Pending {
		failed := false
		for _, user := range users {
			message := mailapp.SystemLoadAlertMessage(
				user.Email,
				a.hostname,
				event.EpisodeID,
				event.Threshold,
				event.CPUPercent,
				event.MemoryPercent,
				event.MemoryValid,
				event.ObservedAt,
			)
			if err := a.delivery.Send(ctx, message); err != nil {
				failed = true
				sendErrors = append(sendErrors, fmt.Errorf("send system load alert %d to user %d: %w", event.Threshold, user.ID, err))
			}
		}
		if failed {
			remaining = append(remaining, event)
		}
	}
	state.Pending = remaining
	if len(remaining) == 0 {
		state.NextRetryAt = time.Time{}
	}
	if err := storeSystemLoadAlertState(ctx, a.redis, stateKey, state); err != nil {
		a.redisRetryAt = observedAt.Add(systemLoadAlertRetryInterval)
		return errors.Join(errors.Join(sendErrors...), err)
	}
	a.cache(state, observedAt)
	return errors.Join(sendErrors...)
}

func (a *systemLoadAlerter) shouldRefresh(snapshot platform.BackgroundLoadSnapshot, observedAt time.Time) bool {
	if !a.redisRetryAt.IsZero() && observedAt.Before(a.redisRetryAt) {
		return false
	}
	if a.interrupted {
		return true
	}
	if !a.initialized || !observedAt.Before(a.refreshAt) {
		return true
	}
	if len(a.cached.Pending) > 0 && (a.cached.NextRetryAt.IsZero() || !observedAt.Before(a.cached.NextRetryAt)) {
		return true
	}
	if !snapshot.CPUValid || snapshot.CPUPercent < float64(systemLoadAlertThresholds[0]) {
		return a.cached.EpisodeID != "" || !a.cached.ExceededSince.IsZero()
	}
	if a.cached.EpisodeID != "" {
		return false
	}
	return a.cached.ExceededSince.IsZero() || !observedAt.Before(a.cached.ExceededSince.Add(systemLoadAlertConfirmWindow))
}

func (a *systemLoadAlerter) cache(state systemLoadAlertState, observedAt time.Time) {
	a.initialized = true
	a.cached = state
	a.refreshAt = observedAt.Add(systemLoadAlertRefreshInterval)
	a.redisRetryAt = time.Time{}
}

func (s *systemLoadAlertState) apply(snapshot platform.BackgroundLoadSnapshot, observedAt time.Time) bool {
	if !snapshot.CPUValid || snapshot.CPUPercent < float64(systemLoadAlertThresholds[0]) {
		return s.resetEpisode()
	}
	if s.EpisodeID != "" {
		return false
	}
	if s.ExceededSince.IsZero() {
		s.ExceededSince = observedAt
		return true
	}
	if observedAt.Before(s.ExceededSince.Add(systemLoadAlertConfirmWindow)) {
		return false
	}
	s.EpisodeID = platform.NewUUIDV7String()
	s.ExceededSince = time.Time{}
	threshold := systemLoadAlertThreshold(snapshot.CPUPercent)
	s.Pending = append(s.Pending, systemLoadAlertEvent{
		EpisodeID:     s.EpisodeID,
		Threshold:     threshold,
		CPUPercent:    snapshot.CPUPercent,
		MemoryPercent: snapshot.MemoryPercent,
		MemoryValid:   snapshot.MemoryValid,
		ObservedAt:    observedAt,
	})
	return true
}

func (s *systemLoadAlertState) resetEpisode() bool {
	if s.EpisodeID == "" && s.ExceededSince.IsZero() {
		return false
	}
	s.EpisodeID = ""
	s.ExceededSince = time.Time{}
	return true
}

func systemLoadAlertThreshold(cpuPercent float64) int {
	highest := 0
	for _, threshold := range systemLoadAlertThresholds {
		if cpuPercent < float64(threshold) {
			break
		}
		highest = threshold
	}
	return highest
}

func loadSystemLoadAlertState(ctx context.Context, client redis.UniversalClient, key string) (systemLoadAlertState, error) {
	payload, err := client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return systemLoadAlertState{}, nil
	}
	if err != nil {
		return systemLoadAlertState{}, fmt.Errorf("load system load alert state: %w", err)
	}
	var state systemLoadAlertState
	if err := json.Unmarshal(payload, &state); err != nil {
		return systemLoadAlertState{}, fmt.Errorf("decode system load alert state: %w", err)
	}
	return state, nil
}

func storeSystemLoadAlertState(ctx context.Context, client redis.UniversalClient, key string, state systemLoadAlertState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode system load alert state: %w", err)
	}
	if err := client.Set(ctx, key, payload, systemLoadAlertStateTTL).Err(); err != nil {
		return fmt.Errorf("store system load alert state: %w", err)
	}
	return nil
}

func systemLoadAlertStateKey(hostname string) string {
	return "remail:system-load-alert:" + strings.TrimSpace(hostname)
}

var systemLoadAlertReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("DEL", KEYS[1])
`)

func (m announcementMailer) PublishAnnouncements(ctx context.Context, announcements []runtimeconfig.Announcement) error {
	if !runtimeconfig.Bool("announcement_email_enabled", false) {
		return nil
	}
	if m.client == nil {
		return fmt.Errorf("announcement queue is unavailable")
	}
	now := time.Now()
	var enqueueErrors []error
	for _, announcement := range announcements {
		start, startErr := time.Parse(time.RFC3339, announcement.StartTime)
		future := startErr == nil && start.After(now)
		if !future && !runtimeconfig.Bool("announcement_enabled", true) {
			continue
		}
		payload, err := json.Marshal(announcementBroadcastTask{ID: announcement.ID, StartTime: announcement.StartTime})
		if err != nil {
			enqueueErrors = append(enqueueErrors, err)
			continue
		}
		options := []asynq.Option{
			asynq.Queue(platform.QueueMailtransport),
			asynq.MaxRetry(0),
			asynq.Timeout(30 * time.Second),
		}
		uniqueFor := 10 * time.Minute
		if future {
			options = append(options, asynq.ProcessAt(start))
			uniqueFor += start.Sub(now)
		}
		options = append(options, asynq.Unique(uniqueFor))
		if _, err := m.client.EnqueueContext(ctx, asynq.NewTask(typeAnnouncementBroadcast, payload), options...); err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
			enqueueErrors = append(enqueueErrors, err)
		}
	}
	return errors.Join(enqueueErrors...)
}

func (m announcementMailer) notifyProjectApplication(ctx context.Context, project coredomain.Project, requestID string) error {
	if m.users == nil || m.delivery == nil || project.ID == 0 || project.ApplicantUserID == nil {
		return nil
	}
	role, enabled := iamdomain.RoleSuperAdmin, true
	users, err := m.users.ListByFilter(ctx, iamdomain.UserListFilter{Role: &role, Enabled: &enabled}, 0, -1)
	if err != nil {
		return fmt.Errorf("list super administrators for project application: %w", err)
	}
	var sendErrors []error
	for _, user := range users {
		if user.Role != iamdomain.RoleSuperAdmin || !user.IsActive() || strings.TrimSpace(user.Email) == "" {
			continue
		}
		message := mailapp.ProjectApplicationMessage(user.Email, project.ID, *project.ApplicantUserID, project.Name, project.TargetPlatform, requestID)
		if err := m.delivery.Send(ctx, message); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("send project application notification to user %d: %w", user.ID, err))
		}
	}
	return errors.Join(sendErrors...)
}

func registerSystemEmailTaskHandlers(mux *asynq.ServeMux, mailer announcementMailer) {
	mux.HandleFunc(typeAnnouncementBroadcast, func(ctx context.Context, task *asynq.Task) error {
		var payload announcementBroadcastTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode announcement broadcast task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ID <= 0 {
			return fmt.Errorf("decode announcement broadcast task: %w", asynq.SkipRetry)
		}
		return mailer.processAnnouncementBroadcast(ctx, payload, time.Now())
	})
}

func (m announcementMailer) processAnnouncementBroadcast(ctx context.Context, task announcementBroadcastTask, now time.Time) error {
	var current *runtimeconfig.Announcement
	for _, announcement := range runtimeconfig.ActiveAnnouncements(now, 100) {
		if announcement.ID == task.ID && strings.TrimSpace(announcement.StartTime) == strings.TrimSpace(task.StartTime) {
			matched := announcement
			current = &matched
			break
		}
	}
	if current == nil {
		return nil
	}
	return m.sendAnnouncements(ctx, []runtimeconfig.Announcement{*current})
}

func (m announcementMailer) sendAnnouncements(ctx context.Context, announcements []runtimeconfig.Announcement) error {
	if !runtimeconfig.Bool("announcement_email_enabled", false) || m.users == nil || m.delivery == nil {
		return nil
	}
	enabled := true
	var sendErrors []error
	for offset := 0; ; offset += 200 {
		users, err := m.users.ListByFilter(ctx, iamdomain.UserListFilter{Enabled: &enabled}, offset, 200)
		if err != nil {
			return errors.Join(append(sendErrors, err)...)
		}
		for _, user := range users {
			for _, announcement := range announcements {
				if err := m.delivery.Send(ctx, mailapp.AnnouncementMessage(user.Email, announcement.ID, announcement.Title, announcement.Content)); err != nil {
					sendErrors = append(sendErrors, err)
				}
			}
		}
		if len(users) < 200 {
			break
		}
	}
	return errors.Join(sendErrors...)
}
