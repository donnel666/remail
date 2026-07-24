package app

import (
	"context"
	"regexp"
	"strings"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

var settingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,190}$`)

// SystemSettingsUseCase handles the small amount of normalization required at
// the administrator API boundary before delegating persistence to the repo.
type SystemSettingsUseCase struct {
	repo Repository
}

func NewSystemSettingsUseCase(repo Repository) *SystemSettingsUseCase {
	return &SystemSettingsUseCase{repo: repo}
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

func (uc *SystemSettingsUseCase) Upsert(ctx context.Context, key, value string) (*domain.Setting, error) {
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	return uc.repo.Upsert(ctx, key, value)
}

func (uc *SystemSettingsUseCase) Delete(ctx context.Context, key string) error {
	key, err := normalizeKey(key)
	if err != nil {
		return err
	}
	return uc.repo.Delete(ctx, key)
}

func normalizeKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !settingKeyPattern.MatchString(key) {
		return "", domain.ErrInvalidKey
	}
	return key, nil
}
