package infra

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const daumAuxiliaryPickupURL = "https://remail.test/pickup?email=aux%40relay.test&token=pickup-secret"

func newDaumAuxiliaryTestDB(t *testing.T, pickup bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE domain_resources (id INTEGER PRIMARY KEY,status TEXT,domain TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE daum_resources (id INTEGER PRIMARY KEY,status TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE daum_binding_mailboxes (id INTEGER PRIMARY KEY,resource_id INTEGER,owner_user_id INTEGER,binding_address TEXT,domain_resource_id INTEGER,mailbox_resource_id INTEGER,pickup_url TEXT,order_no TEXT,status TEXT,updated_at DATETIME)`,
		`CREATE TABLE orders (order_no TEXT PRIMARY KEY,user_id INTEGER,allocation_type TEXT,delivery_email TEXT,service_mode TEXT,status TEXT,receive_started_at DATETIME,receive_until DATETIME)`,
		`CREATE TABLE order_tokens (order_no TEXT PRIMARY KEY,token_plain TEXT,enabled BOOLEAN,expire_at DATETIME)`,
		`CREATE TABLE domain_allocations (order_no TEXT PRIMARY KEY,resource_id INTEGER,email TEXT,status TEXT)`,
		`INSERT INTO email_resources VALUES (42,'daum',1),(50,'domain',1),(51,'domain',1)`,
		`INSERT INTO daum_resources VALUES (42,'normal',1)`,
		`INSERT INTO domain_resources VALUES (50,'normal','relay.test',1),(51,'normal','other.test',1)`,
		`INSERT INTO daum_binding_mailboxes VALUES (1,42,1,'aux@relay.test',50,NULL,'','','linked','2026-10-17 12:00:00')`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.AutoMigrate(&InboundMailModel{}))
	now := time.Now().UTC()
	for _, item := range []struct {
		id, resource uint
		address      string
	}{{1, 50, "aux@relay.test"}, {2, 50, "someone-else@relay.test"}, {3, 51, "aux@relay.test"}} {
		require.NoError(t, db.Create(&InboundMailModel{ID: item.id, ResourceID: item.resource, ResourceType: "domain", OwnerUserID: 1, Recipient: item.address, Status: "stored", Subject: "Kakao code", BodyPreview: "123456", SourceObjectKey: "private/source.eml", ReceivedAt: &now, CreatedAt: now, UpdatedAt: now}).Error)
	}
	if pickup {
		// The source order authorizes the buyer's address, even when the domain belongs to its supplier.
		require.NoError(t, db.Exec(`UPDATE domain_resources SET owner_user_id=9 WHERE id=50`).Error)
		require.NoError(t, db.Exec(`UPDATE email_resources SET owner_user_id=9 WHERE id=50`).Error)
		require.NoError(t, db.Exec(`INSERT INTO orders VALUES ('original-order',1,'domain','aux@relay.test','purchase','active',?,?)`, now.Add(-time.Hour), now.Add(time.Hour)).Error)
		require.NoError(t, db.Exec(`INSERT INTO order_tokens VALUES ('original-order','pickup-secret',TRUE,?)`, now.Add(time.Hour)).Error)
		require.NoError(t, db.Exec(`INSERT INTO domain_allocations VALUES ('original-order',50,'aux@relay.test','allocated')`).Error)
		require.NoError(t, db.Exec(`UPDATE daum_binding_mailboxes SET pickup_url=?, order_no='original-order'`, daumAuxiliaryPickupURL).Error)
	}
	return db
}

func assertDaumAuxiliaryReadable(t *testing.T, db *gorm.DB, want bool) {
	t.Helper()
	repo := NewAuxiliaryMailRepo(db)
	filter := mailapp.AuxiliaryMailFilter{ResourceID: 42, ResourceType: domain.InboundResourceDaum, Limit: 20}
	items, total, more, err := repo.ListMessages(context.Background(), filter)
	require.NoError(t, err)
	require.False(t, more)
	message, err := repo.FindMessage(context.Background(), 42, domain.InboundResourceDaum, 1)
	require.NoError(t, err)
	if want {
		require.Len(t, items, 1)
		require.EqualValues(t, 1, total)
		require.NotNil(t, message)
		require.EqualValues(t, 1, items[0].ID)
		require.Empty(t, items[0].SourceObjectKey)
		require.EqualValues(t, 50, message.ResourceID)
		require.Equal(t, domain.InboundResourceDomain, message.ResourceType)
	} else {
		require.Empty(t, items)
		require.Zero(t, total)
		require.Nil(t, message)
	}
	filter.SkipTotal = true
	items, total, _, err = repo.ListMessages(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, -1, total)
	require.Equal(t, want, len(items) > 0)
}

func TestDaumAuxiliaryMailPreservesOriginalDomainAndExactAddress(t *testing.T) {
	db := newDaumAuxiliaryTestDB(t, false)
	repo, ctx := NewAuxiliaryMailRepo(db), context.Background()
	exists, err := repo.ResourceExists(ctx, 42, domain.InboundResourceDaum)
	require.NoError(t, err)
	require.True(t, exists)
	assertDaumAuxiliaryReadable(t, db, true)
	for _, id := range []uint{2, 3} {
		message, err := repo.FindMessage(ctx, 42, domain.InboundResourceDaum, id)
		require.NoError(t, err)
		require.Nil(t, message)
	}
	binding, err := repo.DaumBinding(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "aux@relay.test", binding.EmailAddress)
	require.Equal(t, "linked", binding.Status)
	require.NoError(t, db.Exec(`UPDATE daum_binding_mailboxes SET status='conflict'`).Error)
	assertDaumAuxiliaryReadable(t, db, false)
}

func TestDaumAuxiliaryReadsRevalidateOriginalPickupAuthority(t *testing.T) {
	for _, test := range []struct {
		name, mutation string
		args           []any
		allowed        bool
	}{
		{name: "valid source link", allowed: true},
		{name: "completed purchase remains readable", mutation: `UPDATE orders SET status='completed'`, allowed: true},
		{name: "active code remains readable", mutation: `UPDATE orders SET service_mode='code'`, allowed: true},
		{name: "rotated token", mutation: `UPDATE order_tokens SET token_plain='replacement-secret'`},
		{name: "revoked token", mutation: `UPDATE order_tokens SET enabled=FALSE`},
		{name: "expired token", mutation: `UPDATE order_tokens SET expire_at=?`, args: []any{time.Now().UTC().Add(-time.Minute)}},
		{name: "refunded source order", mutation: `UPDATE orders SET status='refunded'`},
		{name: "source order ownership transfer", mutation: `UPDATE orders SET user_id=2`},
		{name: "Daum root ownership transfer", mutation: `UPDATE email_resources SET owner_user_id=2 WHERE id=42`},
		{name: "Daum provider ownership transfer", mutation: `UPDATE daum_resources SET owner_user_id=2`},
		{name: "binding ownership transfer", mutation: `UPDATE daum_binding_mailboxes SET owner_user_id=2`},
		{name: "domain root ownership mismatch", mutation: `UPDATE email_resources SET owner_user_id=2 WHERE id=50`},
		{name: "disabled source domain", mutation: `UPDATE domain_resources SET status='disabled' WHERE id=50`},
		{name: "replaced source domain", mutation: `UPDATE daum_binding_mailboxes SET domain_resource_id=51`},
		{name: "changed source domain name", mutation: `UPDATE domain_resources SET domain='other.test' WHERE id=50`},
		{name: "released allocation", mutation: `UPDATE domain_allocations SET status='released'`},
		{name: "changed allocation address", mutation: `UPDATE domain_allocations SET email='someone-else@relay.test'`},
		{name: "changed delivered address", mutation: `UPDATE orders SET delivery_email='someone-else@relay.test'`},
		{name: "changed URL address", mutation: `UPDATE daum_binding_mailboxes SET pickup_url='https://remail.test/pickup?email=other%40relay.test&token=pickup-secret'`},
		{name: "invalid URL", mutation: `UPDATE daum_binding_mailboxes SET pickup_url='invalid'`},
		{name: "expired code window", mutation: `UPDATE orders SET service_mode='code',receive_until=?`, args: []any{time.Now().UTC().Add(-time.Minute)}},
		{name: "message before source window", mutation: `UPDATE inbound_mails SET received_at=?`, args: []any{time.Now().UTC().Add(-24 * time.Hour)}},
		{name: "message after source window", mutation: `UPDATE inbound_mails SET received_at=?`, args: []any{time.Now().UTC().Add(time.Hour)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newDaumAuxiliaryTestDB(t, true)
			if test.mutation != "" {
				require.NoError(t, db.Exec(test.mutation, test.args...).Error)
			}
			assertDaumAuxiliaryReadable(t, db, false)
			binding, handled, err := NewAuxiliaryMailRepo(db).DaumPickupBinding(context.Background(), 42)
			require.NoError(t, err)
			require.True(t, handled)
			require.Equal(t, test.allowed || test.name == "message before source window" || test.name == "message after source window", binding != nil)
		})
	}
}

func TestDaumDirectAuxiliaryBindingRequiresCurrentDomainOwner(t *testing.T) {
	db := newDaumAuxiliaryTestDB(t, false)
	require.NoError(t, db.Exec(`UPDATE domain_resources SET owner_user_id=2 WHERE id=50`).Error)
	require.NoError(t, db.Exec(`UPDATE email_resources SET owner_user_id=2 WHERE id=50`).Error)
	assertDaumAuxiliaryReadable(t, db, false)
}

func TestDaumAuxiliaryReadUsesCallerTransactionWithoutChangingSource(t *testing.T) {
	db := newDaumAuxiliaryTestDB(t, true)
	repo := NewAuxiliaryMailRepo(db)
	rollback := errors.New("rollback fixture")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`UPDATE order_tokens SET enabled=FALSE`).Error)
		message, err := repo.FindMessage(platform.WithGormTx(ctx, tx), 42, domain.InboundResourceDaum, 1)
		require.NoError(t, err)
		require.Nil(t, message)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	assertDaumAuxiliaryReadable(t, db, false)
	binding, handled, err := repo.DaumPickupBinding(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, binding)
	var original struct {
		ResourceID, OwnerUserID uint
		ResourceType, Recipient string
	}
	require.NoError(t, db.Table("inbound_mails").Where("id=1").Take(&original).Error)
	require.EqualValues(t, 50, original.ResourceID)
	require.EqualValues(t, 1, original.OwnerUserID)
	require.Equal(t, "domain", original.ResourceType)
	require.Equal(t, "aux@relay.test", original.Recipient)
	var token struct {
		TokenPlain string
		Enabled    bool
	}
	require.NoError(t, db.Table("order_tokens").Where("order_no='original-order'").Take(&token).Error)
	require.Equal(t, "pickup-secret", token.TokenPlain)
	require.True(t, token.Enabled)
}

func TestDaumAuxiliaryReadDoesNotLogPickupCapabilities(t *testing.T) {
	db := newDaumAuxiliaryTestDB(t, true)
	require.NoError(t, db.Exec(`DROP TABLE order_tokens`).Error)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, _, err := NewAuxiliaryMailRepo(db).DaumPickupBinding(context.Background(), 42)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "pickup-secret")
	require.NotContains(t, logs.String(), "pickup-secret")
	require.NotContains(t, logs.String(), "https://remail.test/pickup")
}
