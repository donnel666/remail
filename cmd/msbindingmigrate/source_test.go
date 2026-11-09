package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLclaitechInventoryAndCheckpointStaySeparateFromStoppedWSU(t *testing.T) {
	ctx := context.Background()
	db := fixture(t, 2)
	require.NoError(t, db.Table("microsoft_binding_mailboxes").Where("resource_id=1").Update("binding_address", "old1@lclaitech.com").Error)
	oldTable, oldSource := table, sourceDomain
	t.Cleanup(func() { table, sourceDomain = oldTable, oldSource })
	require.NoError(t, selectSource("lclaitech.com"))
	// A process selects its source before creating a fresh GORM schema cache.
	connection, err := db.DB()
	require.NoError(t, err)
	other, err := gorm.Open(mysql.New(mysql.Config{Conn: connection, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, seed(ctx, other))
	var rows []item
	require.NoError(t, other.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.EqualValues(t, 1, rows[0].ResourceID)
	require.Equal(t, "old1@lclaitech.com", rows[0].SourceAddress)
	i, evidence := prepared(t, other)
	require.Equal(t, "old1@aishop6.com", i.TargetAddress)
	require.NoError(t, commit(ctx, other, i, evidence))
	var oldRows []struct {
		ResourceID           uint
		State, SourceAddress string
	}
	require.NoError(t, other.Table(originalTable).Order("resource_id").Find(&oldRows).Error)
	require.Len(t, oldRows, 2)
	require.Equal(t, "queued", oldRows[0].State)
	require.Equal(t, "old1@wsu.edu.kg", oldRows[0].SourceAddress)
	_, err = destination("old1@wsu.edu.kg")
	require.Error(t, err)
}

func TestSourceScopeRejectsOtherDomainsAndSharedState(t *testing.T) {
	oldTable, oldSource := table, sourceDomain
	t.Cleanup(func() { table, sourceDomain = oldTable, oldSource })
	for _, invalid := range []string{"", "aishop6.com", "example.com", "lclaitech.com;DROP TABLE users"} {
		require.Error(t, selectSource(invalid))
		require.Equal(t, oldTable, table)
	}
	dir := t.TempDir()
	require.NoError(t, bindStateSource(dir))
	require.NoError(t, selectSource("lclaitech.com"))
	require.ErrorContains(t, bindStateSource(dir), "another source domain")
	legacy := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "1.json"), []byte("{}"), 0600))
	require.ErrorContains(t, bindStateSource(legacy), "separate checkpoint directory")
	separate := t.TempDir()
	require.NoError(t, bindStateSource(separate))
	require.NoError(t, bindStateSource(separate))
	data, err := os.ReadFile(filepath.Join(separate, "source-domain"))
	require.NoError(t, err)
	require.Equal(t, "lclaitech.com\n", string(data))
}
