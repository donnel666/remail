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
