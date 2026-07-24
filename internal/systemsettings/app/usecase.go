package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

var settingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,190}$`)

// SystemSettingsUseCase handles the small amount of normalization required at
// the administrator API boundary before delegating persistence to the repo.
type SystemSettingsUseCase struct {
	repo Repository
	logs governanceapp.OperationLogPort
	mu   sync.Mutex
}

type MutationMeta struct {
	OperatorUserID uint
	RequestID      string
	Path           string
}

func NewSystemSettingsUseCase(repo Repository, logs governanceapp.OperationLogPort) *SystemSettingsUseCase {
	return &SystemSettingsUseCase{repo: repo, logs: logs}
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
	defer uc.mu.Unlock()
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	if err := runtimeconfig.ValidateUpdates([]domain.Setting{{Key: key, Value: value}}); err != nil {
		return nil, err
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
		setting, err = uc.repo.Upsert(txCtx, key, value)
		return err
	})
	if err != nil {
		return nil, err
	}
	runtimeconfig.Set(setting.Key, setting.Value)
	return setting, nil
}

func (uc *SystemSettingsUseCase) BulkUpsert(ctx context.Context, settings []domain.Setting, meta MutationMeta) ([]domain.Setting, error) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	normalized := make([]domain.Setting, len(settings))
	for i, setting := range settings {
		key, err := normalizeKey(setting.Key)
		if err != nil {
			return nil, err
		}
		if err := runtimeconfig.Validate(key, setting.Value); err != nil {
			return nil, err
		}
		normalized[i] = domain.Setting{Key: key, Value: setting.Value}
	}
	if err := runtimeconfig.ValidateUpdates(normalized); err != nil {
		return nil, err
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
		var err error
		saved, err = uc.repo.BulkUpsert(txCtx, normalized)
		return err
	})
	if err != nil {
		return nil, err
	}
	runtimeconfig.SetMany(saved)
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
	return nil
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
	return key, nil
}

func auditResourceID(key string) string {
	if len(key) > 100 {
		return key[:100]
	}
	return key
}
