package infra

import (
	"context"
	"errors"
	"fmt"
	stdmail "net/mail"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var mailTransportMySQLTestServer = testmysql.New("remail_mailtransport_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = mailTransportMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newMailTransportMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return mailTransportMySQLTestServer.Database(t, mailTransportMigrationsDir(t))
}

func mailTransportMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func createMicrosoftAliasTestResource(t *testing.T, db *gorm.DB, resourceID uint, status string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT IGNORE INTO users(id, email, password_hash, role) VALUES (1, 'alias-owner@test.local', 'hash', 'super_admin')",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		resourceID,
		"owner"+time.Now().Format("150405.000000")+"@test.local",
		"hash",
		"supplier",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'microsoft', ?)",
		resourceID,
		resourceID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO microsoft_resources(id, email_address, email_domain, password, for_sale, status) VALUES (?, ?, 'outlook.com', 'secret', FALSE, ?)",
		resourceID,
		"account"+time.Now().Format("150405.000000")+"@outlook.com",
		status,
	).Error)
}

func createMicrosoftAliasTestBinding(t *testing.T, db *gorm.DB, resourceID uint, address, status, boundDisplay string) {
	t.Helper()
	ensureMicrosoftAliasTestBindingDomain(t, db, resourceID, address)
	result := db.Exec(`
INSERT INTO microsoft_binding_mailboxes (
    resource_id,
    resource_type,
    owner_user_id,
    account_email,
    binding_address,
    purpose,
    status,
    bound_display
)
SELECT
    root.id,
    root.type,
    root.owner_user_id,
    resource.email_address,
    ?,
    'validation',
    ?,
    ?
FROM email_resources AS root
JOIN microsoft_resources AS resource ON resource.id = root.id
WHERE root.id = ?`, address, status, boundDisplay, resourceID)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
}

func ensureMicrosoftAliasTestBindingDomain(t *testing.T, db *gorm.DB, resourceID uint, address string) {
	t.Helper()
	parsed, err := stdmail.ParseAddress(strings.ToLower(strings.TrimSpace(address)))
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), strings.TrimSpace(address)) {
		return
	}
	_, domainName, ok := strings.Cut(strings.ToLower(strings.TrimSpace(parsed.Address)), "@")
	if !ok || domainName == "" || strings.Contains(domainName, "@") {
		return
	}
	var exists int64
	require.NoError(t, db.Table("domain_resources").Where("domain = ?", domainName).Count(&exists).Error)
	if exists > 0 {
		return
	}
	ownerID := uint(8_000_000 + resourceID*2)
	domainResourceID := ownerID + 1
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, 'hash', 'supplier')",
		ownerID,
		fmt.Sprintf("alias-binding-domain-owner-%d@test.local", ownerID),
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, name, server_address, status) VALUES (?, ?, 'alias-binding-domain', ?, 'online')",
		ownerID,
		ownerID,
		"mx."+domainName,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'domain', ?)",
		domainResourceID,
		ownerID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, 'domain', ?, ?, ?, 'binding', 'normal')",
		domainResourceID,
		ownerID,
		domainName,
		ownerID,
	).Error)
}

func createVerifiedMicrosoftAliasBinding(t *testing.T, db *gorm.DB, resourceID uint) {
	t.Helper()
	createMicrosoftAliasTestBinding(
		t,
		db,
		resourceID,
		fmt.Sprintf("binding-%d@recovery.test", resourceID),
		"verified",
		"",
	)
}

func TestMicrosoftAliasStoreEnsuresValidatedResourceScheduleMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1008, "pending")
	store := NewMicrosoftAliasStore(db)
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)

	ensured, err := store.EnsureScheduleForResource(context.Background(), 1008, now)
	require.NoError(t, err)
	assert.False(t, ensured, "a validation callback must not schedule a resource that is not normal")

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'normal' WHERE id = ?", 1008).Error)
	ensured, err = store.EnsureScheduleForResource(context.Background(), 1008, now)
	require.NoError(t, err)
	assert.True(t, ensured)
	assert.Equal(t, "pending", loadAliasAdminSchedule(t, db, 1008).Status)

	require.NoError(t, db.Exec(`
UPDATE microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
SET schedule.status = 'paused',
    schedule.claim_token = 'old-claim',
    schedule.last_safe_error = 'Microsoft account is locked.',
    schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        '',
        '',
        '',
        ''
    ), 256)
WHERE schedule.resource_id = ?`, 1008).Error)
	ensured, err = store.EnsureScheduleForResource(context.Background(), 1008, now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, ensured, "a permanent alias failure remains paused when validation changed no relevant facts")
	paused := loadAliasAdminSchedule(t, db, 1008)
	assert.Equal(t, "paused", paused.Status)
	assert.Equal(t, "Microsoft account is locked.", paused.LastSafeError)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET password = 'new-secret' WHERE id = ?", 1008).Error)
	ensured, err = store.EnsureScheduleForResource(context.Background(), 1008, now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.True(t, ensured, "a successful validation after relevant account facts changed may retry")
	woken := loadAliasAdminSchedule(t, db, 1008)
	assert.Equal(t, "pending", woken.Status)
	assert.Empty(t, woken.ClaimToken)
	assert.True(t, woken.NextRunAt.Equal(now.Add(2*time.Minute)))
}

func TestMicrosoftAliasStoreWakesPausedScheduleAfterBindingRecoveryMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1018, "normal")
	createMicrosoftAliasTestBinding(t, db, 1018, "same-address@recovery.test", "failed", "sa**********@recovery.test")

	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC)
	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, account)
	paused := loadAliasAdminSchedule(t, db, 1018)
	require.Equal(t, "paused", paused.Status)
	require.Equal(t, mailapp.MicrosoftAliasExternalRecoveryMessage, paused.LastSafeError)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1018).
		Updates(map[string]any{
			"status":        "verified",
			"bound_display": "",
			"updated_at":    now.Add(time.Minute),
		}).Error)

	ensured, err := store.EnsureScheduleForResource(ctx, 1018, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ensured, "clearing the recovered binding mask must invalidate the paused signature")
	woken := loadAliasAdminSchedule(t, db, 1018)
	require.Equal(t, "pending", woken.Status)
	require.Empty(t, woken.LastSafeError)
}

func TestMicrosoftAliasStoreBroadScanWakesLegacyExternalBindingAfterRecoveryMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1019, "normal")
	createMicrosoftAliasTestBinding(t, db, 1019, "legacy-address@recovery.test", "failed", "le************@recovery.test")

	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC)
	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
UPDATE microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = resource.id
SET schedule.status = 'paused',
    schedule.last_safe_error = ?,
    schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        binding.binding_address
    ), 256)
WHERE schedule.resource_id = ?`, mailapp.MicrosoftAliasExternalRecoveryMessage, 1019).Error)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1019).
		Updates(map[string]any{
			"status":        "verified",
			"bound_display": "",
			"updated_at":    now.Add(time.Minute),
		}).Error)

	_, err = store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	woken := loadAliasAdminSchedule(t, db, 1019)
	require.Equal(t, "pending", woken.Status)
	require.Empty(t, woken.LastSafeError)
}

func TestMicrosoftAliasStoreBroadScanWakesLegacyUnresolvedBindingAfterRecoveryMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1032, "normal")
	createMicrosoftAliasTestBinding(t, db, 1032, "binding-1032@recovery.test", "pending", "")

	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 14, 9, 30, 0, 0, time.UTC)
	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, account)
	paused := loadAliasAdminSchedule(t, db, 1032)
	require.Equal(t, "paused", paused.Status)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, paused.LastSafeError)

	require.NoError(t, db.Exec(`
UPDATE microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
JOIN microsoft_binding_mailboxes AS binding ON binding.resource_id = resource.id
SET schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        binding.binding_address
    ), 256)
WHERE schedule.resource_id = ?`, 1032).Error)
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1032).
		Updates(map[string]any{
			"status":        "verified",
			"bound_display": "",
			"updated_at":    now.Add(time.Minute),
		}).Error)

	_, err = store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	woken := loadAliasAdminSchedule(t, db, 1032)
	require.Equal(t, "pending", woken.Status)
	require.Empty(t, woken.LastSafeError)
}

func TestMicrosoftAliasStoreTargetedWakeAcceptsRecoveredBindingWithMatchingSignatureMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1033, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1033)
	now := time.Date(2026, time.July, 14, 9, 45, 0, 0, time.UTC)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID:               1033,
		Status:                   "paused",
		NextRunAt:                now.Add(24 * time.Hour),
		BlockedResourceSignature: currentAliasAdminResourceSignature(t, db, 1033),
		LastSafeError:            mailapp.MicrosoftAliasBindingUnresolvedMessage,
	}).Error)

	ensured, err := NewMicrosoftAliasStore(db).EnsureScheduleForResource(context.Background(), 1033, now)
	require.NoError(t, err)
	require.True(t, ensured)
	woken := loadAliasAdminSchedule(t, db, 1033)
	require.Equal(t, "pending", woken.Status)
	require.Empty(t, woken.LastSafeError)
}

func TestMicrosoftAliasStoreBroadScanKeepsMalformedVerifiedBindingPausedMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1035, "normal")
	createMicrosoftAliasTestBinding(t, db, 1035, "bad@address@recovery.test", "verified", "")
	now := time.Date(2026, time.July, 14, 9, 50, 0, 0, time.UTC)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID:               1035,
		Status:                   "paused",
		NextRunAt:                now.Add(24 * time.Hour),
		BlockedResourceSignature: currentAliasAdminResourceSignature(t, db, 1035),
		LastSafeError:            mailapp.MicrosoftAliasBindingUnresolvedMessage,
	}).Error)

	ensured, err := NewMicrosoftAliasStore(db).EnsureSchedules(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, ensured)
	paused := loadAliasAdminSchedule(t, db, 1035)
	require.Equal(t, "paused", paused.Status)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, paused.LastSafeError)
}

func TestMicrosoftAliasStoreWakesAfterBindingAccountMatchIsRestoredMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1037, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1037)
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1037).
		Update("account_email", "different-account@outlook.test").Error)

	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 14, 9, 52, 0, 0, time.UTC)
	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, account)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, loadAliasAdminSchedule(t, db, 1037).LastSafeError)

	require.NoError(t, db.Exec(`
UPDATE microsoft_binding_mailboxes AS binding
JOIN microsoft_resources AS resource ON resource.id = binding.resource_id
SET binding.account_email = resource.email_address
WHERE binding.resource_id = ?`, 1037).Error)
	ensured, err := store.EnsureScheduleForResource(ctx, 1037, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ensured)
	require.Equal(t, "pending", loadAliasAdminSchedule(t, db, 1037).Status)
}

func TestMicrosoftAliasStoreBroadScanPaginatesPastMalformedRecoveredBindingsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	now := time.Date(2026, time.July, 14, 9, 55, 0, 0, time.UTC)
	for _, resourceID := range []uint{1040, 1041, 1042} {
		createMicrosoftAliasTestResource(t, db, resourceID, "normal")
		createMicrosoftAliasTestBinding(
			t,
			db,
			resourceID,
			fmt.Sprintf("bad@address@%d.test", resourceID),
			"verified",
			"",
		)
		require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
			ResourceID:               resourceID,
			Status:                   "paused",
			NextRunAt:                now.Add(24 * time.Hour),
			BlockedResourceSignature: currentAliasAdminResourceSignature(t, db, resourceID),
			LastSafeError:            mailapp.MicrosoftAliasBindingUnresolvedMessage,
		}).Error)
	}
	createMicrosoftAliasTestResource(t, db, 1043, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1043)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID:               1043,
		Status:                   "paused",
		NextRunAt:                now.Add(24 * time.Hour),
		BlockedResourceSignature: currentAliasAdminResourceSignature(t, db, 1043),
		LastSafeError:            mailapp.MicrosoftAliasBindingUnresolvedMessage,
	}).Error)

	store := NewMicrosoftAliasStore(db)
	store.recoveredBindingScanPageSize = 2
	ensured, err := store.EnsureSchedules(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 1, ensured)
	for _, resourceID := range []uint{1040, 1041, 1042} {
		require.Equal(t, "paused", loadAliasAdminSchedule(t, db, resourceID).Status)
	}
	require.Equal(t, "pending", loadAliasAdminSchedule(t, db, 1043).Status)
}

func TestMicrosoftAliasStoreClaimRequiresVerifiedConcreteBindingMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	now := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name            string
		resourceID      uint
		bindingAddress  string
		bindingStatus   string
		accountMismatch bool
		wantClaimed     bool
	}{
		{name: "no binding row", resourceID: 1020},
		{name: "verified empty address", resourceID: 1021, bindingStatus: "verified"},
		{name: "pending binding", resourceID: 1022, bindingAddress: "binding-1022@recovery.test", bindingStatus: "pending"},
		{name: "code sent binding", resourceID: 1023, bindingAddress: "binding-1023@recovery.test", bindingStatus: "code_sent"},
		{name: "timeout binding", resourceID: 1024, bindingAddress: "binding-1024@recovery.test", bindingStatus: "timeout"},
		{name: "failed binding", resourceID: 1025, bindingAddress: "binding-1025@recovery.test", bindingStatus: "failed"},
		{name: "verified masked address", resourceID: 1026, bindingAddress: "b**********@recovery.test", bindingStatus: "verified"},
		{name: "expired binding", resourceID: 1027, bindingAddress: "binding-1027@recovery.test", bindingStatus: "expired"},
		{name: "verified malformed address", resourceID: 1028, bindingAddress: "bad@address@recovery.test", bindingStatus: "verified"},
		{name: "verified whitespace address", resourceID: 1030, bindingAddress: "bad address@recovery.test", bindingStatus: "verified"},
		{name: "verified binding for another account", resourceID: 1036, bindingAddress: "binding-1036@recovery.test", bindingStatus: "verified", accountMismatch: true},
		{name: "verified concrete binding", resourceID: 1031, bindingAddress: "binding-1031@recovery.test", bindingStatus: "verified", wantClaimed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createMicrosoftAliasTestResource(t, db, test.resourceID, "normal")
			if test.bindingStatus != "" {
				createMicrosoftAliasTestBinding(t, db, test.resourceID, test.bindingAddress, test.bindingStatus, "")
			}
			if test.accountMismatch {
				require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
					Where("resource_id = ?", test.resourceID).
					Update("account_email", "different-account@outlook.test").Error)
			}
			claimToken := fmt.Sprintf("%032d", test.resourceID)
			require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
				ResourceID: test.resourceID,
				Status:     "queued",
				NextRunAt:  now,
				ClaimToken: claimToken,
			}).Error)

			store := NewMicrosoftAliasStore(db)
			account, claimed, err := store.Claim(context.Background(), mailapp.MicrosoftAliasTask{
				ResourceID:    test.resourceID,
				DispatchToken: claimToken,
			}, now)
			require.NoError(t, err)
			require.Equal(t, test.wantClaimed, claimed)

			schedule := loadAliasAdminSchedule(t, db, test.resourceID)
			if test.wantClaimed {
				require.NotNil(t, account)
				require.Equal(t, test.bindingAddress, account.BindingAddress)
				require.Equal(t, "running", schedule.Status)
				require.Empty(t, schedule.LastSafeError)
				return
			}
			require.Nil(t, account)
			require.Equal(t, "paused", schedule.Status)
			require.Empty(t, schedule.ClaimToken)
			require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, schedule.LastSafeError)
			require.NotEmpty(t, schedule.BlockedResourceSignature)
		})
	}
}

func TestMicrosoftAliasStoreCheckEligibilityRejectsBindingThatBecomesInvalidMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1029, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1029)
	now := time.Date(2026, time.July, 14, 11, 0, 0, 0, time.UTC)
	claimToken := fmt.Sprintf("%032d", 1029)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1029,
		Status:     "queued",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)

	store := NewMicrosoftAliasStore(db)
	account, claimed, err := store.Claim(context.Background(), mailapp.MicrosoftAliasTask{
		ResourceID:    1029,
		DispatchToken: claimToken,
	}, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, account)

	eligible, safeMessage, err := store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, safeMessage)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1029).
		Update("status", "failed").Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1029).
		Updates(map[string]any{
			"status":          "verified",
			"binding_address": "b**********@recovery.test",
		}).Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1029).
		Updates(map[string]any{
			"binding_address": "binding-1029@recovery.test",
			"bound_display":   "bi**********@recovery.test",
		}).Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasExternalRecoveryMessage, safeMessage)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1029).
		Updates(map[string]any{
			"bound_display": "",
			"account_email": "different-account@outlook.test",
		}).Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)

	require.NoError(t, db.Where("resource_id = ?", 1029).Delete(&MicrosoftBindingMailboxModel{}).Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1029, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)
}

func TestMicrosoftAliasStoreBindingDomainMustRemainActiveMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1044, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1044)
	now := time.Date(2026, time.July, 14, 11, 15, 0, 0, time.UTC)
	claimToken := fmt.Sprintf("%032d", 1044)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1044,
		Status:     "queued",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)
	require.NoError(t, db.Table("domain_resources").
		Where("domain = ?", "recovery.test").
		Update("status", "abnormal").Error)

	store := NewMicrosoftAliasStore(db)
	account, claimed, err := store.Claim(context.Background(), mailapp.MicrosoftAliasTask{
		ResourceID:    1044,
		DispatchToken: claimToken,
	}, now)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, account)
	paused := loadAliasAdminSchedule(t, db, 1044)
	require.Equal(t, "paused", paused.Status)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, paused.LastSafeError)

	changed, err := store.EnsureSchedules(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, changed, "an inactive binding domain must not wake the alias schedule")
	require.Equal(t, "paused", loadAliasAdminSchedule(t, db, 1044).Status)

	require.NoError(t, db.Table("domain_resources").
		Where("domain = ?", "recovery.test").
		Update("status", "normal").Error)
	changed, err = store.EnsureSchedules(context.Background(), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)
	require.Equal(t, "pending", loadAliasAdminSchedule(t, db, 1044).Status)
}

func TestMicrosoftAliasStoreRuntimeEligibilityRejectsInactiveBindingDomainMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1045, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1045)
	now := time.Date(2026, time.July, 14, 11, 20, 0, 0, time.UTC)
	claimToken := fmt.Sprintf("%032d", 1045)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1045,
		Status:     "queued",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)

	store := NewMicrosoftAliasStore(db)
	account, claimed, err := store.Claim(context.Background(), mailapp.MicrosoftAliasTask{
		ResourceID:    1045,
		DispatchToken: claimToken,
	}, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, account)

	require.NoError(t, db.Table("domain_resources").
		Where("domain = ?", "recovery.test").
		Update("status", "disabled").Error)
	eligible, safeMessage, err := store.CheckEligibility(context.Background(), 1045, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)

	require.NoError(t, db.Table("domain_resources").
		Where("domain = ?", "recovery.test").
		Update("status", "normal").Error)
	eligible, safeMessage, err = store.CheckEligibility(context.Background(), 1045, claimToken)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, safeMessage)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1045).
		Update("binding_address", "reloaded-binding-1045@recovery.test").Error)
	reloaded, eligible, safeMessage, err := store.ReloadEligibleAccount(context.Background(), 1045, claimToken)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, safeMessage)
	require.NotNil(t, reloaded)
	require.Equal(t, "reloaded-binding-1045@recovery.test", reloaded.BindingAddress)
	require.Equal(t, claimToken, reloaded.ClaimToken)
}

func TestMicrosoftAliasStoreRuntimeBindingPauseCapturesCurrentSignatureMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1034, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1034)
	now := time.Date(2026, time.July, 14, 11, 30, 0, 0, time.UTC)
	claimToken := fmt.Sprintf("%032d", 1034)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1034,
		Status:     "queued",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)

	store := NewMicrosoftAliasStore(db)
	account, claimed, err := store.Claim(context.Background(), mailapp.MicrosoftAliasTask{
		ResourceID:    1034,
		DispatchToken: claimToken,
	}, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, account)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1034).
		Update("status", "failed").Error)
	eligible, safeMessage, err := store.CheckEligibility(context.Background(), 1034, claimToken)
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, safeMessage)
	require.NoError(t, store.Pause(context.Background(), 1034, claimToken, safeMessage))

	paused := loadAliasAdminSchedule(t, db, 1034)
	require.Equal(t, "paused", paused.Status)
	require.Equal(t, mailapp.MicrosoftAliasBindingUnresolvedMessage, paused.LastSafeError)
	require.Equal(t, currentAliasAdminResourceSignature(t, db, 1034), paused.BlockedResourceSignature)
	ensured, err := store.EnsureScheduleForResource(context.Background(), 1034, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ensured, "unchanged invalid binding must remain stably paused")

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1034).
		Update("status", "verified").Error)
	ensured, err = store.EnsureScheduleForResource(context.Background(), 1034, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, ensured)
	require.Equal(t, "pending", loadAliasAdminSchedule(t, db, 1034).Status)
}

func TestMicrosoftAliasStoreAssignsDeterministicSuperAdminOwnerMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1010, "normal")
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (2, 'second-alias-owner@test.local', 'hash', 'super_admin')",
	).Error)

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	claimToken := "0123456789abcdef0123456789abcdef"
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1010,
		Status:     "running",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)
	attempt := &MicrosoftAliasAttemptModel{
		ResourceID: 1010,
		Candidate:  "deterministic101010@outlook.com",
		Status:     mailapp.MicrosoftAliasAttemptRunning,
		QuotaAt:    now,
	}
	require.NoError(t, db.Create(attempt).Error)
	require.NoError(t, db.Create(&MicrosoftExplicitAliasModel{
		ResourceID:  1010,
		OwnerUserID: 2,
		Email:       attempt.Candidate,
		Status:      "normal",
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Hour),
	}).Error)
	require.NoError(t, db.Exec("UPDATE users SET role = 'admin' WHERE id = 2").Error)

	store := NewMicrosoftAliasStore(db)
	require.NoError(t, store.Complete(context.Background(), 1010, claimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID: attempt.ID,
		Status:    mailapp.MicrosoftAliasAttemptSucceeded,
		Category:  "added",
		Attempted: true,
	}}, now))

	var alias MicrosoftExplicitAliasModel
	require.NoError(t, db.Where("resource_id = ?", 1010).First(&alias).Error)
	assert.Equal(t, uint(1), alias.OwnerUserID)
}

func TestMicrosoftAliasStoreRefusesUnownedSuccessMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1011, "normal")
	require.NoError(t, db.Exec("UPDATE users SET role = 'admin' WHERE role = 'super_admin'").Error)

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	claimToken := "fedcba9876543210fedcba9876543210"
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 1011,
		Status:     "running",
		NextRunAt:  now,
		ClaimToken: claimToken,
	}).Error)
	attempt := &MicrosoftAliasAttemptModel{
		ResourceID: 1011,
		Candidate:  "unowned101101@outlook.com",
		Status:     mailapp.MicrosoftAliasAttemptRunning,
		QuotaAt:    now,
	}
	require.NoError(t, db.Create(attempt).Error)

	store := NewMicrosoftAliasStore(db)
	err := store.Complete(context.Background(), 1011, claimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID: attempt.ID,
		Status:    mailapp.MicrosoftAliasAttemptSucceeded,
		Category:  "added",
		Attempted: true,
	}}, now)
	require.ErrorIs(t, err, mailapp.ErrMicrosoftAliasOwnerUnavailable)

	var aliases int64
	require.NoError(t, db.Model(&MicrosoftExplicitAliasModel{}).Where("resource_id = ?", 1011).Count(&aliases).Error)
	assert.Zero(t, aliases)
	require.NoError(t, db.Where("id = ?", attempt.ID).First(attempt).Error)
	assert.Equal(t, mailapp.MicrosoftAliasAttemptRunning, attempt.Status)
}

func TestMicrosoftAliasStoreFencesStaleWorkerAndResumesCandidatesMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1001, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1001)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	yearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	yearEnd := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, time.July, 5, 16, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)

	ensured, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	tasks, err := store.FindDispatchable(ctx, 10, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	firstTask := tasks[0]
	firstAccount, claimed, err := store.Claim(ctx, firstTask, now)
	require.NoError(t, err)
	require.True(t, claimed)

	firstAttempts, usage, err := store.Reserve(
		ctx,
		1001,
		firstAccount.ClaimToken,
		[]string{"david123456@outlook.com", "liming654321@outlook.com"},
		yearStart,
		yearEnd,
		weekStart,
		weekEnd,
		now,
	)
	require.NoError(t, err)
	require.Len(t, firstAttempts, 2)
	assert.Equal(t, 2, usage.WeekCount)

	require.NoError(t, db.Exec(
		"UPDATE microsoft_alias_schedules SET updated_at = ? WHERE resource_id = ?",
		now.Add(-31*time.Minute),
		1001,
	).Error)
	replacementTasks, err := store.FindDispatchable(ctx, 10, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, replacementTasks, 1)
	replacementTask := replacementTasks[0]
	assert.NotEqual(t, firstTask.DispatchToken, replacementTask.DispatchToken)

	err = store.Complete(ctx, 1001, firstAccount.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{
		{AttemptID: firstAttempts[0].ID, Status: mailapp.MicrosoftAliasAttemptSucceeded},
	}, now)
	assert.ErrorIs(t, err, mailapp.ErrMicrosoftAliasStaleClaim)

	replacementAccount, claimed, err := store.Claim(ctx, replacementTask, now)
	require.NoError(t, err)
	require.True(t, claimed)
	resumed, _, err := store.Reserve(
		ctx,
		1001,
		replacementAccount.ClaimToken,
		[]string{"other123456@outlook.com", "other654321@outlook.com"},
		yearStart,
		yearEnd,
		weekStart,
		weekEnd,
		now,
	)
	require.NoError(t, err)
	require.Len(t, resumed, 2)
	assert.True(t, resumed[0].WasUncertain)
	assert.True(t, resumed[1].WasUncertain)
	assert.ElementsMatch(t,
		[]string{"david123456@outlook.com", "liming654321@outlook.com"},
		[]string{resumed[0].Alias, resumed[1].Alias},
	)

	outcomes := []mailapp.MicrosoftAliasAttemptOutcome{
		{AttemptID: resumed[0].ID, Status: mailapp.MicrosoftAliasAttemptSucceeded, Category: "added"},
		{AttemptID: resumed[1].ID, Status: mailapp.MicrosoftAliasAttemptSucceeded, Category: "added"},
	}
	require.NoError(t, store.Complete(ctx, 1001, replacementAccount.ClaimToken, outcomes, now.Add(time.Minute)))
	usage, err = store.Usage(ctx, 1001, yearStart, yearEnd, weekStart, weekEnd)
	require.NoError(t, err)
	assert.Equal(t, 2, usage.YearCount)
	assert.Equal(t, 2, usage.WeekCount)

	var attempts, aliases int64
	require.NoError(t, db.Model(&MicrosoftAliasAttemptModel{}).Where("resource_id = ?", 1001).Count(&attempts).Error)
	require.NoError(t, db.Model(&MicrosoftExplicitAliasModel{}).Where("resource_id = ?", 1001).Count(&aliases).Error)
	assert.EqualValues(t, 2, attempts)
	assert.EqualValues(t, 2, aliases)
	var nonSuperAdminOwnedAliases int64
	require.NoError(t, db.Table("explicit_aliases AS alias_row").
		Joins("JOIN users AS owner ON owner.id = alias_row.owner_user_id").
		Where("alias_row.resource_id = ? AND owner.role <> ?", 1001, "super_admin").
		Count(&nonSuperAdminOwnedAliases).Error)
	assert.Zero(t, nonSuperAdminOwnedAliases)
}

func TestMicrosoftAliasStoreMovesSuccessAtBoundaryAndReleasesFailureMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1002, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1002)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	startedAt := time.Date(2026, time.July, 12, 15, 59, 0, 0, time.UTC)
	completedAt := time.Date(2026, time.July, 12, 16, 1, 0, 0, time.UTC)
	yearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	yearEnd := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	oldWeekStart := time.Date(2026, time.July, 5, 16, 0, 0, 0, time.UTC)
	boundary := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)
	newWeekEnd := boundary.AddDate(0, 0, 7)

	_, err := store.EnsureSchedules(ctx, startedAt)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, startedAt, startedAt.Add(-4*time.Hour), startedAt.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], startedAt)
	require.NoError(t, err)
	require.True(t, claimed)
	attempts, _, err := store.Reserve(
		ctx,
		1002,
		account.ClaimToken,
		[]string{"mary123456@outlook.com", "wangfang654321@outlook.com"},
		yearStart,
		yearEnd,
		oldWeekStart,
		boundary,
		startedAt,
	)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	require.NoError(t, store.Complete(ctx, 1002, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{
		{AttemptID: attempts[0].ID, Status: mailapp.MicrosoftAliasAttemptSucceeded, Category: "added"},
		{AttemptID: attempts[1].ID, Status: mailapp.MicrosoftAliasAttemptFailed, Category: "alias_exists"},
	}, completedAt))

	oldUsage, err := store.Usage(ctx, 1002, yearStart, yearEnd, oldWeekStart, boundary)
	require.NoError(t, err)
	newUsage, err := store.Usage(ctx, 1002, yearStart, yearEnd, boundary, newWeekEnd)
	require.NoError(t, err)
	assert.Equal(t, 0, oldUsage.WeekCount)
	assert.Equal(t, 1, newUsage.WeekCount)
	assert.Equal(t, 1, newUsage.YearCount)

	more, usage, err := store.Reserve(
		ctx,
		1002,
		account.ClaimToken,
		[]string{"ruth111111@outlook.com", "sunli222222@outlook.com"},
		yearStart,
		yearEnd,
		boundary,
		newWeekEnd,
		completedAt,
	)
	require.NoError(t, err)
	require.Len(t, more, 1)
	assert.Equal(t, 2, usage.WeekCount)
}

func TestMicrosoftAliasStoreWakesPausedNormalResourceMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1003, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1003)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'abnormal' WHERE id = 1003").Error)
	require.NoError(t, store.Pause(ctx, 1003, account.ClaimToken, mailapp.MicrosoftAliasResourceNotNormalMessage))

	ensured, err := store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Zero(t, ensured)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'normal', updated_at = ? WHERE id = 1003", now.Add(48*time.Hour)).Error)
	ensured, err = store.EnsureSchedules(ctx, now.Add(49*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)

	var status string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1003").Scan(&status).Error)
	assert.Equal(t, "pending", status)
}

func TestMicrosoftAliasStoreSchedulesNormalResourcesRegardlessOfSaleStateMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1006, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1006)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	ensured, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	var scheduleCount int64
	require.NoError(t, db.Model(&MicrosoftAliasScheduleModel{}).Where("resource_id = 1006").Count(&scheduleCount).Error)
	assert.EqualValues(t, 1, scheduleCount)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	account, claimed, err := store.Claim(ctx, tasks[0], now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, account)

	eligible, _, err := store.CheckEligibility(ctx, 1006, account.ClaimToken)
	require.NoError(t, err)
	assert.True(t, eligible)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET for_sale = TRUE, updated_at = ? WHERE id = 1006", now.Add(2*time.Minute)).Error)
	eligible, _, err = store.CheckEligibility(ctx, 1006, account.ClaimToken)
	require.NoError(t, err)
	assert.True(t, eligible)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET for_sale = FALSE, updated_at = ? WHERE id = 1006", now.Add(3*time.Minute)).Error)
	eligible, _, err = store.CheckEligibility(ctx, 1006, account.ClaimToken)
	require.NoError(t, err)
	assert.True(t, eligible)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'abnormal', updated_at = ? WHERE id = 1006", now.Add(4*time.Minute)).Error)
	eligible, _, err = store.CheckEligibility(ctx, 1006, account.ClaimToken)
	require.NoError(t, err)
	assert.False(t, eligible)
}

func TestMicrosoftAliasStoreClaimPausesResourceThatBecomesAbnormalMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1017, "normal")
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	ensured, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'abnormal', updated_at = ? WHERE id = 1017", now.Add(time.Minute)).Error)

	account, claimed, err := store.Claim(ctx, tasks[0], now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Nil(t, account)
	var schedule MicrosoftAliasScheduleModel
	require.NoError(t, db.First(&schedule, "resource_id = ?", 1017).Error)
	assert.Equal(t, "paused", schedule.Status)
	assert.Equal(t, mailapp.MicrosoftAliasResourceNotNormalMessage, schedule.LastSafeError)
}

func TestMicrosoftAliasStoreWakesLegacyPrivatePauseMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1016, "normal")
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_schedules (
    resource_id,
    status,
    next_run_at,
    blocked_resource_signature,
    last_safe_error,
    created_at,
    updated_at
)
SELECT
    mr.id,
    'paused',
    ?,
    SHA2(CONCAT_WS(
        CHAR(0),
        mr.status,
        mr.for_sale,
        mr.email_address,
        mr.password,
        mr.client_id,
        mr.refresh_token,
        ''
    ), 256),
    'Microsoft resource is not publicly available for alias creation.',
    ?,
    ?
FROM microsoft_resources AS mr
WHERE mr.id = 1016`, now, now, now).Error)

	ensured, err := store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	var status string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1016").Scan(&status).Error)
	assert.Equal(t, "pending", status)
}

func TestMicrosoftAliasStorePermanentPauseWakesOnlyAfterResourceChangeMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1007, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1007)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.Pause(ctx, 1007, account.ClaimToken, "Microsoft account password is incorrect."))

	ensured, err := store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Zero(t, ensured)
	var status string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1007").Scan(&status).Error)
	assert.Equal(t, "paused", status)
	saleChangedAt := time.Now().UTC().Add(15 * time.Second).Truncate(time.Millisecond)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET for_sale = TRUE, updated_at = ? WHERE id = 1007", saleChangedAt).Error)
	ensured, err = store.EnsureSchedules(ctx, saleChangedAt)
	require.NoError(t, err)
	assert.Zero(t, ensured)
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1007").Scan(&status).Error)
	assert.Equal(t, "paused", status)
	allocationUpdateAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Millisecond)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET last_allocated_at = ?, updated_at = ? WHERE id = 1007", allocationUpdateAt, allocationUpdateAt).Error)
	ensured, err = store.EnsureSchedules(ctx, allocationUpdateAt)
	require.NoError(t, err)
	assert.Zero(t, ensured)

	resourceChangedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 1007).
		Updates(map[string]any{
			"binding_address": "changed-binding-1007@recovery.test",
			"updated_at":      resourceChangedAt,
		}).Error)
	ensured, err = store.EnsureSchedules(ctx, resourceChangedAt)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1007").Scan(&status).Error)
	assert.Equal(t, "pending", status)
}

func TestMicrosoftAliasStoreReusesConfirmedUnattemptedCandidatesMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1008, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1008)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	yearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	yearEnd := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, time.July, 5, 16, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)
	first, _, err := store.Reserve(ctx, 1008, account.ClaimToken,
		[]string{"david123456@outlook.com", "liming654321@outlook.com"},
		yearStart, yearEnd, weekStart, weekEnd, now)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NoError(t, store.Complete(ctx, 1008, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{
		{AttemptID: first[0].ID, Status: mailapp.MicrosoftAliasAttemptFailed, Category: "request"},
		{AttemptID: first[1].ID, Status: mailapp.MicrosoftAliasAttemptFailed, Category: "request"},
	}, now.Add(time.Minute)))

	reused, _, err := store.Reserve(ctx, 1008, account.ClaimToken,
		[]string{"other111111@outlook.com", "other222222@outlook.com"},
		yearStart, yearEnd, weekStart, weekEnd, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Len(t, reused, 2)
	assert.ElementsMatch(t, []uint{first[0].ID, first[1].ID}, []uint{reused[0].ID, reused[1].ID})
	assert.ElementsMatch(t, []string{first[0].Alias, first[1].Alias}, []string{reused[0].Alias, reused[1].Alias})
	var attemptCount int64
	require.NoError(t, db.Model(&MicrosoftAliasAttemptModel{}).Where("resource_id = 1008").Count(&attemptCount).Error)
	assert.EqualValues(t, 2, attemptCount)
}

func TestMicrosoftAliasStorePersistsConservativeUncertainReconciliationMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1009, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1009)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	yearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	yearEnd := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, time.July, 5, 16, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)
	attempts, _, err := store.Reserve(ctx, 1009, account.ClaimToken,
		[]string{"david123456@outlook.com"}, yearStart, yearEnd, weekStart, weekEnd, now)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	var scheduleBeforeComplete MicrosoftAliasScheduleModel
	require.NoError(t, db.Where("resource_id = ?", 1009).First(&scheduleBeforeComplete).Error)
	require.Equal(t, "running", scheduleBeforeComplete.Status)
	require.Equal(t, account.ClaimToken, scheduleBeforeComplete.ClaimToken)
	require.NoError(t, store.Complete(ctx, 1009, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID: attempts[0].ID,
		Status:    mailapp.MicrosoftAliasAttemptUncertain,
		Category:  "request",
		Attempted: true,
	}}, now))

	reconciliationAt := now.Add(25 * time.Hour)
	resumed, _, err := store.Reserve(ctx, 1009, account.ClaimToken,
		[]string{"other123456@outlook.com"}, yearStart, yearEnd, weekStart, weekEnd, reconciliationAt)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.True(t, resumed[0].WasUncertain)
	assert.True(t, resumed[0].WasAttempted)
	require.NotNil(t, resumed[0].UncertainSince)
	assert.True(t, resumed[0].UncertainSince.Equal(now))
	require.NoError(t, store.Complete(ctx, 1009, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID:        resumed[0].ID,
		Status:           mailapp.MicrosoftAliasAttemptUncertain,
		Category:         "alias_failed",
		ReconciledAbsent: true,
	}}, reconciliationAt))

	var persisted MicrosoftAliasAttemptModel
	require.NoError(t, db.Where("id = ?", resumed[0].ID).First(&persisted).Error)
	assert.True(t, persisted.WasAttempted)
	require.NotNil(t, persisted.UncertainSince)
	assert.True(t, persisted.UncertainSince.Equal(now))
	assert.Equal(t, 1, persisted.NegativeConfirmations)
	require.NotNil(t, persisted.LastNegativeConfirmationAt)
	assert.True(t, persisted.LastNegativeConfirmationAt.Equal(reconciliationAt))
}

func TestMicrosoftAliasStorePermanentPauseDetectsCredentialChangeDuringRunMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1010, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1010)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET password = 'changed-during-run' WHERE id = 1010").Error)
	require.NoError(t, store.Pause(ctx, 1010, account.ClaimToken, "Microsoft account password is incorrect."))
	ensured, err := store.EnsureSchedules(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 1, ensured)
	var status string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_alias_schedules WHERE resource_id = 1010").Scan(&status).Error)
	assert.Equal(t, "pending", status)
}

func TestMicrosoftAliasStoreRotatesExpiredQueuedDispatchTokenMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1012, "normal")
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, db.Exec(
		"UPDATE microsoft_alias_schedules SET updated_at = ? WHERE resource_id = 1012",
		now.Add(-4*time.Hour-time.Minute),
	).Error)

	recovered, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	assert.NotEqual(t, tasks[0].DispatchToken, recovered[0].DispatchToken)
}

func TestMicrosoftAliasStoreReconciledSuccessKeepsOriginalQuotaWindowMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1011, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1011)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	startedAt := time.Date(2026, time.December, 31, 15, 0, 0, 0, time.UTC)
	reconciledAt := time.Date(2027, time.January, 1, 1, 0, 0, 0, time.UTC)
	oldYearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	boundary := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	newYearEnd := time.Date(2027, time.December, 31, 16, 0, 0, 0, time.UTC)
	oldWeekStart := time.Date(2026, time.December, 27, 16, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2027, time.January, 3, 16, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, startedAt)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, startedAt, startedAt.Add(-4*time.Hour), startedAt.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], startedAt)
	require.NoError(t, err)
	require.True(t, claimed)
	attempts, _, err := store.Reserve(ctx, 1011, account.ClaimToken,
		[]string{"david123456@outlook.com"}, oldYearStart, boundary, oldWeekStart, weekEnd, startedAt)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.NoError(t, store.Complete(ctx, 1011, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID: attempts[0].ID,
		Status:    mailapp.MicrosoftAliasAttemptUncertain,
		Category:  "request",
		Attempted: true,
	}}, startedAt.Add(time.Minute)))
	resumed, _, err := store.Reserve(ctx, 1011, account.ClaimToken,
		[]string{"other123456@outlook.com"}, boundary, newYearEnd, oldWeekStart, weekEnd, reconciledAt)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.NoError(t, store.Complete(ctx, 1011, account.ClaimToken, []mailapp.MicrosoftAliasAttemptOutcome{{
		AttemptID: resumed[0].ID,
		Status:    mailapp.MicrosoftAliasAttemptSucceeded,
		Category:  "added",
	}}, reconciledAt))

	oldUsage, err := store.Usage(ctx, 1011, oldYearStart, boundary, oldWeekStart, weekEnd)
	require.NoError(t, err)
	newUsage, err := store.Usage(ctx, 1011, boundary, newYearEnd, oldWeekStart, weekEnd)
	require.NoError(t, err)
	assert.Equal(t, 1, oldUsage.YearCount)
	assert.Equal(t, 0, newUsage.YearCount)
}

func TestMicrosoftAliasStoreSerializesConcurrentQuotaReservationsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1005, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 1005)
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	yearStart := time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC)
	yearEnd := time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, time.July, 5, 16, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	tasks, err := store.FindDispatchable(ctx, 1, now, now.Add(-4*time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	account, claimed, err := store.Claim(ctx, tasks[0], now)
	require.NoError(t, err)
	require.True(t, claimed)

	candidateSets := [][]string{
		{"david111111@outlook.com", "liming222222@outlook.com"},
		{"mary333333@outlook.com", "sunli444444@outlook.com"},
	}
	var wg sync.WaitGroup
	counts := make(chan int, len(candidateSets))
	errs := make(chan error, len(candidateSets))
	for _, candidates := range candidateSets {
		candidates := candidates
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempts, _, reserveErr := store.Reserve(
				ctx,
				1005,
				account.ClaimToken,
				candidates,
				yearStart,
				yearEnd,
				weekStart,
				weekEnd,
				now,
			)
			errs <- reserveErr
			counts <- len(attempts)
		}()
	}
	wg.Wait()
	close(errs)
	close(counts)

	total := 0
	for reserveErr := range errs {
		require.NoError(t, reserveErr)
	}
	for count := range counts {
		total += count
	}
	assert.Equal(t, 2, total)
	usage, err := store.Usage(ctx, 1005, yearStart, yearEnd, weekStart, weekEnd)
	require.NoError(t, err)
	assert.Equal(t, 2, usage.WeekCount)
}

func TestMicrosoftAliasStoreRejectsStaleDeferMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 1004, "normal")
	store := NewMicrosoftAliasStore(db)
	ctx := context.Background()
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	_, err := store.EnsureSchedules(ctx, now)
	require.NoError(t, err)
	err = store.Defer(ctx, 1004, "wrong-token", now.Add(time.Hour), "", false)
	require.True(t, errors.Is(err, mailapp.ErrMicrosoftAliasStaleClaim))
}
