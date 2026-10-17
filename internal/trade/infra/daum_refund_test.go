package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDaumRefundSelectionRequiresCurrentAbnormalRevision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	for _, sql := range []string{
		`CREATE TABLE orders(id INTEGER,order_no TEXT,allocation_type TEXT,status TEXT,debit_tx_id INTEGER,refund_tx_id INTEGER)`,
		`CREATE TABLE email_resources(id INTEGER,type TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE daum_resources(id INTEGER,resource_type TEXT,owner_user_id INTEGER,status TEXT,credential_revision INTEGER,validation_generation INTEGER,failure_category TEXT,last_safe_error TEXT)`,
		`CREATE TABLE daum_allocations(order_no TEXT,guard_type TEXT,status TEXT,resource_id INTEGER)`,
		`INSERT INTO orders VALUES(1,'ACTIVE-PURCHASE','daum','active',1,NULL)`,
		`INSERT INTO email_resources VALUES(7,'daum',1)`,
		`INSERT INTO daum_resources VALUES(7,'daum',1,'abnormal',3,5,'','transport: retry budget exhausted')`,
		`INSERT INTO daum_allocations VALUES('ACTIVE-PURCHASE','daum','allocated',7)`} {
		require.NoError(t, db.Exec(sql).Error)
	}
	repo := NewRepo(db)
	ctx := context.Background()
	selected, err := repo.ListUnavailableDaumOrders(ctx, 0, 200)
	require.NoError(t, err)
	require.Len(t, selected, 1, "authoritative abnormal resources compensate customers regardless of diagnostic category")
	for _, category := range []string{"", "invalid_credentials", "identity_mismatch", "account_disabled"} {
		require.NoError(t, db.Table("daum_resources").Where("id = 7").Update("failure_category", category).Error)
		selected, err = repo.ListUnavailableDaumOrders(ctx, 7, 200)
		require.NoError(t, err)
		require.Len(t, selected, 1)
		proof := selected[0]
		require.EqualValues(t, 3, proof.CredentialRevision)
		require.EqualValues(t, 5, proof.ValidationGeneration)
		require.Equal(t, category, proof.FailureCategory)
		require.ErrorIs(t, repo.AssertDaumRefundFailure(ctx, proof), domain.ErrInvalidOrderRequest, "proof checks must hold locks until a caller-owned refund commits")
		require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertDaumRefundFailure(txCtx, proof) }))
		require.NoError(t, db.Table("daum_resources").Where("id = 7").Update("validation_generation", 6).Error)
		require.ErrorIs(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertDaumRefundFailure(txCtx, proof) }), domain.ErrOrderStateConflict)
		require.NoError(t, db.Table("daum_resources").Where("id = 7").Update("validation_generation", 5).Error)
	}
}

func TestMicrosoftRefundProofRejectsRecoveryRevalidationAndAllocationRelease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.Exec(`
 CREATE TABLE orders(id INTEGER,order_no TEXT,allocation_type TEXT,status TEXT,debit_tx_id INTEGER,refund_tx_id INTEGER);
 CREATE TABLE email_resources(id INTEGER,type TEXT,owner_user_id INTEGER);
 CREATE TABLE microsoft_resources(id INTEGER,status TEXT,credential_revision INTEGER,validation_generation INTEGER);
 CREATE TABLE microsoft_allocations(order_no TEXT,status TEXT,resource_id INTEGER);
 INSERT INTO orders VALUES(1,'MS-PROOF','microsoft','active',1,NULL);
 INSERT INTO email_resources VALUES(7,'microsoft',1);
 INSERT INTO microsoft_resources VALUES(7,'abnormal',3,5);
 INSERT INTO microsoft_allocations VALUES('MS-PROOF','allocated',7);
 `).Error)
	repo := NewRepo(db)
	ctx := context.Background()
	proofs, err := repo.ListUnavailableMicrosoftOrders(ctx, 7, 200)
	require.NoError(t, err)
	require.Len(t, proofs, 1)
	proof := proofs[0]
	require.ErrorIs(t, repo.AssertMicrosoftRefundFailure(ctx, proof), domain.ErrInvalidOrderRequest)
	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertMicrosoftRefundFailure(txCtx, proof) }))
	for _, change := range []struct{ sql, restore string }{
		{"UPDATE microsoft_resources SET status='normal'", "UPDATE microsoft_resources SET status='abnormal'"},
		{"UPDATE microsoft_resources SET credential_revision=4", "UPDATE microsoft_resources SET credential_revision=3"},
		{"UPDATE microsoft_resources SET validation_generation=6", "UPDATE microsoft_resources SET validation_generation=5"},
		{"UPDATE microsoft_allocations SET status='released'", "UPDATE microsoft_allocations SET status='allocated'"},
	} {
		require.NoError(t, db.Exec(change.sql).Error)
		require.ErrorIs(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertMicrosoftRefundFailure(txCtx, proof) }), domain.ErrOrderStateConflict, change.sql)
		require.NoError(t, db.Exec(change.restore).Error)
	}
	// Microsoft resources imported before the first validation may have generation zero.
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET validation_generation=0").Error)
	proofs, err = repo.ListUnavailableMicrosoftOrders(ctx, 7, 200)
	require.NoError(t, err)
	require.Len(t, proofs, 1)
	proof = proofs[0]
	require.Zero(t, proof.ValidationGeneration)
	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertMicrosoftRefundFailure(txCtx, proof) }))
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET validation_generation=1").Error)
	require.ErrorIs(t, repo.WithTx(ctx, func(txCtx context.Context) error { return repo.AssertMicrosoftRefundFailure(txCtx, proof) }), domain.ErrOrderStateConflict)
}
