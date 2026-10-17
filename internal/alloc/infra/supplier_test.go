package infra

import (
	"context"
	"fmt"
	"testing"

	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAllocationSupplierRemainsOriginalOwner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:allocation-supplier-snapshot?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&OrderGuardModel{}))
	require.NoError(t, db.Exec("CREATE TABLE email_resources (id INTEGER PRIMARY KEY, owner_user_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources VALUES (10, 7)").Error)
	repo := NewRepo(db)
	ctx := context.Background()
	require.NoError(t, repo.CreateOrderGuard(ctx, "OR-OWNER", domain.AllocationTypeMicrosoft))
	require.NoError(t, repo.snapshotAllocationSupplier(ctx, "OR-OWNER", 10, false))
	require.NoError(t, db.Exec("UPDATE email_resources SET owner_user_id = 8 WHERE id = 10").Error)
	owner, err := repo.AllocationSupplierUserID(ctx, "OR-OWNER")
	require.NoError(t, err)
	require.EqualValues(t, 7, owner)
	require.ErrorIs(t, repo.snapshotAllocationSupplier(ctx, "OR-OWNER", 10, false), domain.ErrAllocationConflict)
}

func TestExplicitAliasSupplierIsPlatformAndExistingSnapshotsAreImmutable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&OrderGuardModel{}))
	require.NoError(t, db.Exec("CREATE TABLE email_resources (id INTEGER PRIMARY KEY, owner_user_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources VALUES (10, 7)").Error)
	repo := NewRepo(db)
	ctx := context.Background()
	for _, provider := range []domain.AllocationType{domain.AllocationTypeMicrosoft, domain.AllocationTypeDaum} {
		for _, alias := range []bool{false, true} {
			orderNo := fmt.Sprintf("%s-%t", provider, alias)
			require.NoError(t, repo.CreateOrderGuard(ctx, orderNo, provider))
			require.NoError(t, repo.snapshotAllocationSupplier(ctx, orderNo, 10, alias))
			owner, err := repo.AllocationSupplierUserID(ctx, orderNo)
			require.NoError(t, err)
			want := uint(7)
			if alias {
				want = 1
			}
			require.Equal(t, want, owner)
			require.ErrorIs(t, repo.snapshotAllocationSupplier(ctx, orderNo, 10, !alias), domain.ErrAllocationConflict)
			owner, err = repo.AllocationSupplierUserID(ctx, orderNo)
			require.NoError(t, err)
			require.Equal(t, want, owner, "replaying a policy change cannot move already snapshotted income")
		}
	}
}
