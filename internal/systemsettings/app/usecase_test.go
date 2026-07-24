package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	setting     *domain.Setting
	getKey      string
	upsertKey   string
	upsertValue string
	deleteKey   string
	bulkCalled  bool
}

type blockingRepository struct {
	*fakeRepository
	entered chan string
	release chan struct{}
}

func (r *blockingRepository) Upsert(ctx context.Context, key, value string) (*domain.Setting, error) {
	r.entered <- value
	if value == "first" {
		<-r.release
	}
	return r.fakeRepository.Upsert(ctx, key, value)
}

func TestSystemSettingMutationsAreSerializedWithRuntimePublish(t *testing.T) {
	repo := &blockingRepository{
		fakeRepository: &fakeRepository{},
		entered:        make(chan string, 2),
		release:        make(chan struct{}),
	}
	uc := NewSystemSettingsUseCase(repo, &fakeOperationLogs{})
	t.Cleanup(func() { runtimeconfig.Delete("concurrent_test_key") })
	errs := make(chan error, 2)

	go func() {
		_, err := uc.Upsert(context.Background(), "concurrent_test_key", "first", MutationMeta{})
		errs <- err
	}()
	require.Equal(t, "first", <-repo.entered)
	go func() {
		_, err := uc.Upsert(context.Background(), "concurrent_test_key", "second", MutationMeta{})
		errs <- err
	}()

	select {
	case value := <-repo.entered:
		t.Fatalf("second mutation entered before first runtime publish: %s", value)
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	require.Equal(t, "second", <-repo.entered)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, "second", runtimeconfig.String("concurrent_test_key", ""))
}

func (f *fakeRepository) WithTx(ctx context.Context, fn func(context.Context) error) error {
	var snapshot *domain.Setting
	if f.setting != nil {
		cloned := *f.setting
		snapshot = &cloned
	}
	if err := fn(ctx); err != nil {
		f.setting = snapshot
		return err
	}
	return nil
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
	cloned := *f.setting
	return &cloned, nil
}

func (f *fakeRepository) Upsert(_ context.Context, key, value string) (*domain.Setting, error) {
	f.upsertKey, f.upsertValue = key, value
	f.setting = &domain.Setting{Key: key, Value: value}
	return f.setting, nil
}

func (f *fakeRepository) BulkUpsert(ctx context.Context, settings []domain.Setting) ([]domain.Setting, error) {
	f.bulkCalled = true
	result := make([]domain.Setting, len(settings))
	for i, setting := range settings {
		saved, err := f.Upsert(ctx, setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		result[i] = *saved
	}
	return result, nil
}

func (f *fakeRepository) Delete(_ context.Context, key string) error {
	f.deleteKey = key
	return nil
}

type fakeOperationLogs struct {
	items []*governancedomain.OperationLog
	err   error
}

func (f *fakeOperationLogs) Create(_ context.Context, log *governancedomain.OperationLog) error {
	cloned := *log
	f.items = append(f.items, &cloned)
	return f.err
}

func TestSystemSettingsUseCaseNormalizesKeys(t *testing.T) {
	repo := &fakeRepository{}
	logs := &fakeOperationLogs{}
	uc := NewSystemSettingsUseCase(repo, logs)

	if _, err := uc.Upsert(context.Background(), "  mail.foo  ", "value", MutationMeta{}); err != nil {
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
	if err := uc.Delete(context.Background(), "  mail.foo  ", MutationMeta{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repo.deleteKey != "mail.foo" {
		t.Fatalf("delete key = %q, want mail.foo", repo.deleteKey)
	}
}

func TestSystemSettingsUseCaseRejectsInvalidKeys(t *testing.T) {
	uc := NewSystemSettingsUseCase(&fakeRepository{}, &fakeOperationLogs{})
	for _, key := range []string{"", "with space", "../secret", "-starts-with-dash"} {
		if _, err := uc.Upsert(context.Background(), key, "value", MutationMeta{}); err != domain.ErrInvalidKey {
			t.Fatalf("upsert key %q error = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestSystemSettingsUseCaseRejectsInvalidKnownValues(t *testing.T) {
	uc := NewSystemSettingsUseCase(&fakeRepository{}, &fakeOperationLogs{})
	if _, err := uc.Upsert(context.Background(), "smtp_outbound_payload_ttl_minutes", "0", MutationMeta{}); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("error = %v, want ErrInvalidValue", err)
	}
}

func TestBulkUpsertValidatesEverythingBeforeWriting(t *testing.T) {
	repo := &fakeRepository{}
	uc := NewSystemSettingsUseCase(repo, &fakeOperationLogs{})
	_, err := uc.BulkUpsert(context.Background(), []domain.Setting{
		{Key: "valid_key", Value: "one"},
		{Key: "invalid key", Value: "two"},
	}, MutationMeta{})
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("error = %v, want ErrInvalidKey", err)
	}
	if repo.bulkCalled || repo.setting != nil {
		t.Fatal("bulk repository was called before all keys were validated")
	}
}

func TestMutationAndSafeAuditShareTransaction(t *testing.T) {
	repo := &fakeRepository{setting: &domain.Setting{Key: "github_client_secret", Value: "old"}}
	logs := &fakeOperationLogs{err: errors.New("audit unavailable")}
	uc := NewSystemSettingsUseCase(repo, logs)
	secret := "must-not-enter-audit"

	_, err := uc.Upsert(context.Background(), "github_client_secret", secret, MutationMeta{
		OperatorUserID: 42,
		RequestID:      "request-1",
		Path:           "/v1/admin/settings/github_client_secret",
	})
	if err == nil {
		t.Fatal("expected audit failure")
	}
	if repo.setting.Value != "old" {
		t.Fatalf("value = %q, want transaction rollback to old", repo.setting.Value)
	}
	if len(logs.items) != 1 {
		t.Fatalf("audit attempts = %d, want 1", len(logs.items))
	}
	log := logs.items[0]
	if strings.Contains(log.SafeSummary, secret) {
		t.Fatal("audit summary contains the setting value")
	}
	if log.OperatorUserID != 42 || log.RequestID != "request-1" || log.ResourceID != "github_client_secret" {
		t.Fatalf("unexpected audit: %+v", log)
	}
}

func TestAllMutationsWriteValueFreeAuditLogs(t *testing.T) {
	repo := &fakeRepository{}
	logs := &fakeOperationLogs{}
	uc := NewSystemSettingsUseCase(repo, logs)
	meta := MutationMeta{OperatorUserID: 7, RequestID: "request-7", Path: "/v1/admin/settings"}

	if _, err := uc.Upsert(context.Background(), "one", "single-private-value", meta); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := uc.BulkUpsert(context.Background(), []domain.Setting{
		{Key: "two", Value: "bulk-private-value"},
		{Key: "three", Value: "another-private-value"},
	}, meta); err != nil {
		t.Fatalf("bulk upsert: %v", err)
	}
	if err := uc.Delete(context.Background(), "three", meta); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(logs.items) != 3 {
		t.Fatalf("audit logs = %d, want 3", len(logs.items))
	}
	for i, operation := range []string{
		"system_settings.upsert",
		"system_settings.bulk_upsert",
		"system_settings.delete",
	} {
		log := logs.items[i]
		if log.OperationType != operation || log.OperatorUserID != 7 || log.RequestID != "request-7" || log.Path != meta.Path {
			t.Fatalf("unexpected audit[%d]: %+v", i, log)
		}
		for _, value := range []string{"single-private-value", "bulk-private-value", "another-private-value"} {
			if strings.Contains(log.SafeSummary, value) {
				t.Fatalf("audit[%d] contains setting value", i)
			}
		}
	}
	if logs.items[0].ResourceID != "one" || logs.items[1].SafeSummary != "updated system settings count=2" || logs.items[2].ResourceID != "three" {
		t.Fatalf("unexpected audit keys/counts: %+v", logs.items)
	}
}
