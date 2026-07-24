package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

type fakeRepository struct {
	setting     *domain.Setting
	getKey      string
	upsertKey   string
	upsertValue string
	deleteKey   string
}

func (f *fakeRepository) List(context.Context) ([]domain.Setting, error) {
	if f.setting == nil {
		return []domain.Setting{}, nil
	}
	return []domain.Setting{*f.setting}, nil
}

func (f *fakeRepository) Get(_ context.Context, key string) (*domain.Setting, error) {
	f.getKey = key
	if f.setting == nil {
		return nil, domain.ErrSettingNotFound
	}
	copy := *f.setting
	return &copy, nil
}

func (f *fakeRepository) Upsert(_ context.Context, key, value string) (*domain.Setting, error) {
	f.upsertKey, f.upsertValue = key, value
	f.setting = &domain.Setting{Key: key, Value: value}
	return f.setting, nil
}

func (f *fakeRepository) Delete(_ context.Context, key string) error {
	f.deleteKey = key
	return nil
}

func TestSystemSettingsUseCaseNormalizesKeys(t *testing.T) {
	repo := &fakeRepository{}
	uc := NewSystemSettingsUseCase(repo)

	if _, err := uc.Upsert(context.Background(), "  mail.foo  ", "value"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if repo.upsertKey != "mail.foo" {
		t.Fatalf("key = %q, want mail.foo", repo.upsertKey)
	}
	if _, err := uc.Get(context.Background(), "  mail.foo  "); err != nil {
		t.Fatalf("get: %v", err)
	}
	if repo.getKey != "mail.foo" {
		t.Fatalf("get key = %q, want mail.foo", repo.getKey)
	}
	if err := uc.Delete(context.Background(), "  mail.foo  "); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repo.deleteKey != "mail.foo" {
		t.Fatalf("delete key = %q, want mail.foo", repo.deleteKey)
	}
}

func TestSystemSettingsUseCaseRejectsInvalidKeys(t *testing.T) {
	uc := NewSystemSettingsUseCase(&fakeRepository{})
	for _, key := range []string{"", "with space", "../secret", "-starts-with-dash"} {
		if _, err := uc.Upsert(context.Background(), key, "value"); err != domain.ErrInvalidKey {
			t.Fatalf("upsert key %q error = %v, want ErrInvalidKey", key, err)
		}
	}
}
