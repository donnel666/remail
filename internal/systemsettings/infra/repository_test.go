package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBulkUpsertUsesCallerTransaction(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-settings?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&SettingModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewRepository(db)
	ctx := context.Background()
	seeded, err := repo.BulkUpsert(ctx, []domain.Setting{
		{Key: "one", Value: "1"},
		{Key: "two", Value: "2"},
	})
	if err != nil {
		t.Fatalf("seed bulk upsert: %v", err)
	}
	one, err := repo.Get(ctx, "one")
	if err != nil || len(seeded) != 2 || !seeded[0].CreatedAt.Equal(one.CreatedAt) {
		t.Fatalf("bulk response was not reloaded from storage: %+v, %v", seeded, err)
	}

	rollback := errors.New("rollback")
	err = repo.WithTx(ctx, func(txCtx context.Context) error {
		if _, err := repo.BulkUpsert(txCtx, []domain.Setting{
			{Key: "one", Value: "changed"},
			{Key: "three", Value: "3"},
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error = %v, want rollback", err)
	}
	one, err = repo.Get(ctx, "one")
	if err != nil || one.Value != "1" {
		t.Fatalf("one after rollback = %+v, %v", one, err)
	}
	if _, err := repo.Get(ctx, "three"); !errors.Is(err, domain.ErrSettingNotFound) {
		t.Fatalf("three error = %v, want not found", err)
	}
}

func TestSettingModelToDomainCanonicalizesKey(t *testing.T) {
	setting := (SettingModel{Key: " SMTP_TASK_RETRY_COUNT "}).toDomain()
	if setting.Key != "smtp_task_retry_count" {
		t.Fatalf("key = %q, want smtp_task_retry_count", setting.Key)
	}
}

func TestRepositoryFindsLegacyUppercaseKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-settings-uppercase?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&SettingModel{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&SettingModel{Key: "SMTP_TASK_RETRY_COUNT", Value: "4"}).Error; err != nil {
		t.Fatal(err)
	}

	setting, err := NewRepository(db).Get(context.Background(), "smtp_task_retry_count")
	if err != nil {
		t.Fatal(err)
	}
	if setting.Key != "smtp_task_retry_count" || setting.Value != "4" {
		t.Fatalf("setting = %+v, want canonical key and value", setting)
	}
}

func TestInsertMissingPreservesExistingValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-settings-missing?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&SettingModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewRepository(db)
	ctx := context.Background()
	if _, err := repo.Upsert(ctx, "smtp_task_retry_count", "9"); err != nil {
		t.Fatalf("seed existing setting: %v", err)
	}
	defaults := []domain.Setting{
		{Key: "smtp_task_retry_count", Value: "3"},
		{Key: "smtp_outbound_payload_ttl_minutes", Value: "5"},
	}
	if err := repo.InsertMissing(ctx, defaults); err != nil {
		t.Fatalf("insert missing settings: %v", err)
	}
	if err := repo.InsertMissing(ctx, defaults); err != nil {
		t.Fatalf("repeat insert missing settings: %v", err)
	}

	existing, err := repo.Get(ctx, "smtp_task_retry_count")
	if err != nil || existing.Value != "9" {
		t.Fatalf("existing setting = %+v, %v; want value 9", existing, err)
	}
	missing, err := repo.Get(ctx, "smtp_outbound_payload_ttl_minutes")
	if err != nil || missing.Value != "5" {
		t.Fatalf("inserted setting = %+v, %v; want value 5", missing, err)
	}
}
