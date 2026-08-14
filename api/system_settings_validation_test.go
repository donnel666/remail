package api

import (
	"context"
	"testing"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateICloudForwardingSuffixes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-forwarding-suffix-validation?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE domain_resources (domain TEXT PRIMARY KEY, purpose TEXT NOT NULL, status TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO domain_resources (domain, purpose, status) VALUES
		('binding.example', 'binding', 'normal'),
		('disabled.example', 'binding', 'disabled'),
		('deleted.example', 'binding', 'deleted'),
		('sale.example', 'sale', 'normal')`).Error)

	validate := func(value string) error {
		return validateICloudForwardingSuffixes(context.Background(), db, []settingsdomain.Setting{{
			Key: runtimeconfig.ICloudForwardingSuffixesKey, Value: value,
		}})
	}
	require.NoError(t, validate(""))
	require.NoError(t, validate(" BINDING.EXAMPLE. "))
	require.ErrorIs(t, validate("disabled.example"), settingsdomain.ErrInvalidValue)
	require.ErrorIs(t, validate("deleted.example"), settingsdomain.ErrInvalidValue)
	require.ErrorIs(t, validate("sale.example"), settingsdomain.ErrInvalidValue)
	require.ErrorIs(t, validateICloudForwardingSuffixes(context.Background(), db, []settingsdomain.Setting{
		{Key: runtimeconfig.ICloudForwardingSuffixesKey, Value: ""},
		{Key: runtimeconfig.ICloudForwardingSuffixesKey, Value: "deleted.example"},
	}), settingsdomain.ErrInvalidValue)
}

func TestApplyICloudForwardingSuffixesUpdateRevokesRemovedDomains(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-forwarding-suffix-update?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT NOT NULL, version INTEGER NOT NULL, updated_at DATETIME)`,
		`CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, selected_forward_to TEXT NOT NULL, status TEXT NOT NULL, next_validation_at DATETIME, next_provision_at DATETIME, validation_generation INTEGER NOT NULL, last_safe_error TEXT NOT NULL, updated_at DATETIME)`,
		`CREATE TABLE icloud_aliases (id INTEGER PRIMARY KEY, resource_id INTEGER NOT NULL, forward_to_email TEXT NOT NULL, status TEXT NOT NULL, updated_at DATETIME)`,
		`CREATE TABLE icloud_maintenance_runs (id INTEGER PRIMARY KEY, resource_id INTEGER NOT NULL, status TEXT NOT NULL, finished_at DATETIME, last_safe_error TEXT NOT NULL, updated_at DATETIME)`,
		`INSERT INTO email_resources (id, type, version) VALUES (1, 'icloud', 3), (2, 'icloud', 4)`,
		`INSERT INTO icloud_resources (id, selected_forward_to, status, validation_generation, last_safe_error) VALUES (1, 'mail@kept.example', 'normal', 7, ''), (2, 'mail@removed.example', 'validating', 8, '')`,
		`INSERT INTO icloud_aliases (id, resource_id, forward_to_email, status) VALUES (11, 1, 'mail@kept.example', 'normal'), (12, 1, 'mail@removed.example', 'normal')`,
		`INSERT INTO icloud_maintenance_runs (id, resource_id, status, last_safe_error) VALUES (21, 2, 'running', '')`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	require.NoError(t, applyICloudForwardingSuffixesUpdate(context.Background(), db, []settingsdomain.Setting{{
		Key: runtimeconfig.ICloudForwardingSuffixesKey, Value: " KEPT.EXAMPLE. ",
	}}))

	var keptStatus, removedStatus, removedAliasStatus, runStatus string
	var generation, version int
	require.NoError(t, db.Raw(`SELECT status FROM icloud_resources WHERE id = 1`).Scan(&keptStatus).Error)
	require.NoError(t, db.Raw(`SELECT status, validation_generation FROM icloud_resources WHERE id = 2`).Row().Scan(&removedStatus, &generation))
	require.NoError(t, db.Raw(`SELECT status FROM icloud_aliases WHERE id = 12`).Scan(&removedAliasStatus).Error)
	require.NoError(t, db.Raw(`SELECT status FROM icloud_maintenance_runs WHERE id = 21`).Scan(&runStatus).Error)
	require.NoError(t, db.Raw(`SELECT version FROM email_resources WHERE id = 2`).Scan(&version).Error)
	require.Equal(t, "normal", keptStatus)
	require.Equal(t, "abnormal", removedStatus)
	require.Equal(t, 9, generation)
	require.Equal(t, "disabled", removedAliasStatus)
	require.Equal(t, "canceled", runStatus)
	require.Equal(t, 5, version)
}
