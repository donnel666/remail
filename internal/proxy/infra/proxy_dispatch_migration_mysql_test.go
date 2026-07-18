package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProxyPendingDispatchMigrationRoundTripRehydratesLegacyJobsMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO proxies(
    id, pool, url, url_hash, url_host, status,
    check_operator_user_id, check_request_id, check_path
)
VALUES
    (990271, 'resource', 'http://127.0.0.1:9001', REPEAT('a', 64), '127.0.0.1', 'pending', 71, 'request-pending', '/pending'),
    (990272, 'resource', 'http://127.0.0.1:9002', REPEAT('b', 64), '127.0.0.1', 'checking', 72, 'request-checking', '/checking')`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, proxyMigrationsDir(t), 26))

	var statuses []string
	require.NoError(t, db.Table("proxies").Where("id IN ?", []uint{990271, 990272}).Order("id").Pluck("status", &statuses).Error)
	require.Equal(t, []string{"checking", "checking"}, statuses)

	var jobs []struct {
		ProxyID  uint   `gorm:"column:proxy_id"`
		Status   string `gorm:"column:status"`
		Operator uint   `gorm:"column:operator_user_id"`
		Request  string `gorm:"column:request_id"`
		Path     string `gorm:"column:path"`
	}
	require.NoError(t, db.Table("proxy_check_jobs").Where("proxy_id IN ?", []uint{990271, 990272}).Order("proxy_id").Find(&jobs).Error)
	require.Equal(t, []struct {
		ProxyID  uint   `gorm:"column:proxy_id"`
		Status   string `gorm:"column:status"`
		Operator uint   `gorm:"column:operator_user_id"`
		Request  string `gorm:"column:request_id"`
		Path     string `gorm:"column:path"`
	}{
		{ProxyID: 990271, Status: "pending", Operator: 71, Request: "request-pending", Path: "/pending"},
		{ProxyID: 990272, Status: "pending", Operator: 72, Request: "request-checking", Path: "/checking"},
	}, jobs)

	require.NoError(t, goose.UpTo(sqlDB, proxyMigrationsDir(t), 27))
	require.False(t, db.Migrator().HasTable("proxy_check_jobs"))
	var rows []struct {
		Status     string `gorm:"column:status"`
		Generation uint64 `gorm:"column:check_generation"`
	}
	require.NoError(t, db.Table("proxies").Where("id IN ?", []uint{990271, 990272}).Order("id").Find(&rows).Error)
	require.Equal(t, []struct {
		Status     string `gorm:"column:status"`
		Generation uint64 `gorm:"column:check_generation"`
	}{
		{Status: "pending", Generation: 2},
		{Status: "pending", Generation: 2},
	}, rows)
}
