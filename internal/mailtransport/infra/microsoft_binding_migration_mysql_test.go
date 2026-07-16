package infra

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftBindingMaskMigrationsRejectDirtyLegacyDisplayMySQL(t *testing.T) {
	tests := []struct {
		name   string
		from   int64
		to     int64
		baseID uint
	}{
		{name: "migration 20", from: 19, to: 20, baseID: 9_920_000},
		{name: "migration 21 deployment window", from: 20, to: 21, baseID: 9_921_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newMailTransportMySQLTestDB(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, goose.SetDialect("mysql"))
			require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), tt.from))

			insertLegacyBindingDisplay(t, db, tt.baseID+1, "original-one@recovery.test", "a***b@@recovery.test")
			insertLegacyBindingDisplay(t, db, tt.baseID+2, "original-two@recovery.test", "Name <a***b@recovery.test>")
			insertLegacyBindingDisplay(t, db, tt.baseID+3, "original-three@recovery.test", " A***B@Recovery.Test ")

			require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), tt.to))
			require.Equal(t, "original-one@recovery.test", bindingAddressForMigrationTest(t, db, tt.baseID+1))
			require.Equal(t, "original-two@recovery.test", bindingAddressForMigrationTest(t, db, tt.baseID+2))
			require.Equal(t, "a***b@recovery.test", bindingAddressForMigrationTest(t, db, tt.baseID+3))
		})
	}
}

func TestMicrosoftBindingMaskMigrationRollbackPreservesLongDisplayMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 20))

	const resourceID = 9_922_001
	longMask := strings.Repeat("a", 280) + "*@recovery.test"
	insertLegacyBindingDisplay(t, db, resourceID, longMask, "")
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 19))

	var display string
	require.NoError(t, db.Raw("SELECT bound_display FROM microsoft_binding_mailboxes WHERE resource_id = ?", resourceID).Scan(&display).Error)
	require.Equal(t, longMask, display)
}

func insertLegacyBindingDisplay(t *testing.T, db *gorm.DB, resourceID uint, bindingAddress, boundDisplay string) {
	t.Helper()
	createMicrosoftAliasTestResource(t, db, resourceID, "normal")
	result := db.Exec(`
INSERT INTO microsoft_binding_mailboxes (
    resource_id, resource_type, owner_user_id, account_email,
    binding_address, purpose, status, bound_display
)
SELECT root.id, root.type, root.owner_user_id, resource.email_address,
       ?, 'validation', 'failed', ?
FROM email_resources AS root
JOIN microsoft_resources AS resource ON resource.id = root.id
WHERE root.id = ?`, bindingAddress, boundDisplay, resourceID)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected, fmt.Sprintf("insert binding for resource %d", resourceID))
}

func bindingAddressForMigrationTest(t *testing.T, db *gorm.DB, resourceID uint) string {
	t.Helper()
	var address string
	require.NoError(t, db.Raw("SELECT binding_address FROM microsoft_binding_mailboxes WHERE resource_id = ?", resourceID).Scan(&address).Error)
	return address
}
