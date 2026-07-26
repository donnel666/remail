package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

var settingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,190}$`)

// SystemSettingsUseCase handles the small amount of normalization required at
// the administrator API boundary before delegating persistence to the repo.
type SystemSettingsUseCase struct {
	repo          Repository
	logs          governanceapp.OperationLogPort
	publisher     RuntimeSettingsPublisher
	announcements AnnouncementPublisher
	runtimeHook   func(context.Context, []domain.Setting) error
	mu            sync.Mutex
}

type MutationMeta struct {
	OperatorUserID uint
	RequestID      string
	Path           string
}

func NewSystemSettingsUseCase(repo Repository, logs governanceapp.OperationLogPort) *SystemSettingsUseCase {
	return &SystemSettingsUseCase{repo: repo, logs: logs}
}

func (uc *SystemSettingsUseCase) SetRuntimeSettingsPublisher(publisher RuntimeSettingsPublisher) {
	if uc != nil {
		uc.publisher = publisher
	}
}

func (uc *SystemSettingsUseCase) SetAnnouncementPublisher(publisher AnnouncementPublisher) {
	if uc != nil {
		uc.announcements = publisher
	}
}

func (uc *SystemSettingsUseCase) SetRuntimeUpdateHook(hook func(context.Context, []domain.Setting) error) {
	if uc != nil {
		uc.runtimeHook = hook
	}
}

func (uc *SystemSettingsUseCase) List(ctx context.Context) ([]domain.Setting, error) {
	return uc.repo.List(ctx)
}

func (uc *SystemSettingsUseCase) Get(ctx context.Context, key string) (*domain.Setting, error) {
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	return uc.repo.Get(ctx, key)
}

func (uc *SystemSettingsUseCase) Upsert(ctx context.Context, key, value string, meta MutationMeta) (*domain.Setting, error) {
	uc.mu.Lock()
	var published []runtimeconfig.Announcement
	committed := false
	defer func() {
		if committed {
			uc.publishAnnouncements(ctx, published)
		}
	}()
	defer uc.mu.Unlock()
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	update := domain.Setting{Key: key, Value: value}
	if err := runtimeconfig.Validate(key, value); err != nil {
		return nil, invalidValueField(key, err)
	}
	var setting *domain.Setting
	err = uc.mutate(ctx, &governancedomain.OperationLog{
		OperatorUserID: meta.OperatorUserID,
		OperationType:  "system_settings.upsert",
		ResourceType:   "system_setting",
		ResourceID:     auditResourceID(key),
		Path:           meta.Path,
		Result:         "success",
		SafeSummary:    "updated system setting key=" + key,
		RequestID:      meta.RequestID,
	}, func(txCtx context.Context) error {
		persisted, err := uc.repo.List(txCtx)
		if err != nil {
			return err
		}
		if err := runtimeconfig.ValidatePersistedUpdates(persisted, []domain.Setting{update}); err != nil {
			return err
		}
		published = newlyPublishedAnnouncements(persisted, []domain.Setting{update}, time.Now())
		setting, err = uc.repo.Upsert(txCtx, key, value)
		return err
	})
	if err != nil {
		return nil, err
	}
	committed = true
	if err := uc.runRuntimeUpdateHook(ctx, []domain.Setting{*setting}); err != nil {
		return setting, err
	}
	runtimeconfig.Set(setting.Key, setting.Value)
	uc.publishRuntimeSettings(ctx)
	return setting, nil
}

func (uc *SystemSettingsUseCase) BulkUpsert(ctx context.Context, settings []domain.Setting, meta MutationMeta) ([]domain.Setting, error) {
	uc.mu.Lock()
	var published []runtimeconfig.Announcement
	committed := false
	defer func() {
		if committed {
			uc.publishAnnouncements(ctx, published)
		}
	}()
	defer uc.mu.Unlock()
	normalized := make([]domain.Setting, 0, len(settings))
	for _, setting := range settings {
		key, err := normalizeKey(setting.Key)
		if err != nil {
			return nil, err
		}
		if err := runtimeconfig.Validate(key, setting.Value); err != nil {
			// Keep an invalid legacy value from blocking unrelated fields in the
			// same form. It is skipped only when the client sent the exact value
			// already stored; changing it still requires a valid replacement.
			if !errors.Is(err, domain.ErrInvalidValue) {
				return nil, err
			}
			existing, getErr := uc.repo.Get(ctx, key)
			if getErr != nil || existing == nil || existing.Value != setting.Value {
				return nil, invalidValueField(key, err)
			}
			continue
		}
		normalized = append(normalized, domain.Setting{Key: key, Value: setting.Value})
	}
	if len(normalized) == 0 {
		return []domain.Setting{}, nil
	}
	var saved []domain.Setting
	err := uc.mutate(ctx, &governancedomain.OperationLog{
		OperatorUserID: meta.OperatorUserID,
		OperationType:  "system_settings.bulk_upsert",
		ResourceType:   "system_setting",
		ResourceID:     "bulk",
		Path:           meta.Path,
		Result:         "success",
		SafeSummary:    fmt.Sprintf("updated system settings count=%d", len(normalized)),
		RequestID:      meta.RequestID,
	}, func(txCtx context.Context) error {
		persisted, err := uc.repo.List(txCtx)
		if err != nil {
			return err
		}
		if err := runtimeconfig.ValidatePersistedUpdates(persisted, normalized); err != nil {
			return err
		}
		published = newlyPublishedAnnouncements(persisted, normalized, time.Now())
		saved, err = uc.repo.BulkUpsert(txCtx, normalized)
		return err
	})
	if err != nil {
		return nil, err
	}
	committed = true
	if err := uc.runRuntimeUpdateHook(ctx, saved); err != nil {
		return saved, err
	}
	runtimeconfig.SetMany(saved)
	uc.publishRuntimeSettings(ctx)
	return saved, nil
}

func (uc *SystemSettingsUseCase) Delete(ctx context.Context, key string, meta MutationMeta) error {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	key, err := normalizeKey(key)
	if err != nil {
		return err
	}
	if err := runtimeconfig.ValidateDelete(key); err != nil {
		return err
	}
	if err := uc.mutate(ctx, &governancedomain.OperationLog{
		OperatorUserID: meta.OperatorUserID,
		OperationType:  "system_settings.delete",
		ResourceType:   "system_setting",
		ResourceID:     auditResourceID(key),
		Path:           meta.Path,
		Result:         "success",
		SafeSummary:    "deleted system setting key=" + key,
		RequestID:      meta.RequestID,
	}, func(txCtx context.Context) error {
		return uc.repo.Delete(txCtx, key)
	}); err != nil {
		return err
	}
	runtimeconfig.Delete(key)
	uc.publishRuntimeSettings(ctx)
	return nil
}

func (uc *SystemSettingsUseCase) publishRuntimeSettings(ctx context.Context) {
	if uc == nil || uc.publisher == nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := uc.publisher.Publish(notifyCtx); err != nil {
		// The database write and this process' snapshot are already committed.
		// Pub/Sub is an acceleration path; a later write or restart can recover
		// another replica if Redis is temporarily unavailable.
		slog.Warn("publish system settings runtime update failed", "error", err)
	}
}

func (uc *SystemSettingsUseCase) runRuntimeUpdateHook(ctx context.Context, settings []domain.Setting) error {
	if uc == nil || uc.runtimeHook == nil || len(settings) == 0 {
		return nil
	}
	if err := uc.runtimeHook(ctx, settings); err != nil {
		return fmt.Errorf("apply runtime setting side effect: %w", err)
	}
	return nil
}

func (uc *SystemSettingsUseCase) publishAnnouncements(ctx context.Context, announcements []runtimeconfig.Announcement) {
	if uc == nil || uc.announcements == nil || len(announcements) == 0 {
		return
	}
	// Scheduling is best effort; notification loss must not roll back settings.
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := uc.announcements.PublishAnnouncements(publishCtx, announcements); err != nil {
		slog.Warn("publish announcement emails failed", "error", err)
	}
}

func newlyPublishedAnnouncements(persisted, updates []domain.Setting, now time.Time) []runtimeconfig.Announcement {
	oldValue, newValue := "[]", ""
	for _, setting := range persisted {
		if strings.EqualFold(strings.TrimSpace(setting.Key), "announcements") {
			oldValue = setting.Value
		}
	}
	for _, setting := range updates {
		if strings.EqualFold(strings.TrimSpace(setting.Key), "announcements") {
			newValue = setting.Value
		}
	}
	if newValue == "" {
		return nil
	}
	var before, after []runtimeconfig.Announcement
	if json.Unmarshal([]byte(oldValue), &before) != nil || json.Unmarshal([]byte(newValue), &after) != nil {
		return nil
	}
	previous := make(map[int64]runtimeconfig.Announcement, len(before))
	for _, announcement := range before {
		previous[announcement.ID] = announcement
	}
	published := make([]runtimeconfig.Announcement, 0, len(after))
	for _, announcement := range after {
		old, exists := previous[announcement.ID]
		if announcement.Enabled && (!exists || !old.Enabled) {
			published = append(published, announcement)
			continue
		}
		oldStart, err := time.Parse(time.RFC3339, old.StartTime)
		if announcement.Enabled && old.Enabled && old.StartTime != announcement.StartTime && err == nil && oldStart.After(now) {
			published = append(published, announcement)
		}
	}
	return published
}

func (uc *SystemSettingsUseCase) mutate(ctx context.Context, log *governancedomain.OperationLog, fn func(context.Context) error) error {
	if uc.logs == nil {
		return fmt.Errorf("system settings operation log is required")
	}
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := fn(txCtx); err != nil {
			return err
		}
		if err := uc.logs.Create(txCtx, log); err != nil {
			return fmt.Errorf("audit system settings mutation: %w", err)
		}
		return nil
	})
}

func normalizeKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !settingKeyPattern.MatchString(key) {
		return "", domain.ErrInvalidKey
	}
	return strings.ToLower(key), nil
}

func auditResourceID(key string) string {
	if len(key) > 100 {
		return key[:100]
	}
	return key
}

func invalidValueField(key string, err error) error {
	if errors.Is(err, domain.ErrInvalidValue) {
		return &domain.InvalidValueFieldsError{Fields: map[string]string{key: "Invalid value."}}
	}
	return err
}
