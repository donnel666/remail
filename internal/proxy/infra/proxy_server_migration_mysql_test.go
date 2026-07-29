package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProxyServerMigrationBackfillsCanonicalMachineIdentityMySQL(t *testing.T) {
	db, migrationsDir := newProxyLegacyMigrationTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 63))

	require.NoError(t, db.Exec(`
INSERT INTO proxies(id, pool, url, url_hash, url_host, status)
VALUES
    (990641, 'resource', 'socks5://first:secret@192.0.2.40:1080', REPEAT('a', 64), '192.0.2.40', 'pending'),
    (990642, 'system',   'socks5://second:secret@192.0.2.40:2080', REPEAT('b', 64), '192.0.2.40', 'pending'),
    (990643, 'resource', 'socks5://third:secret@[2001:db8::1]:1080', REPEAT('c', 64), '2001:0DB8:0:0:0:0:0:1', 'pending'),
    (990644, 'system',   'socks5://fourth:secret@[2001:db8::1]:2080', REPEAT('d', 64), '2001:db8::1', 'pending'),
    (990645, 'resource', 'socks5://bound:secret@192.0.2.40:3080', REPEAT('e', 64), '192.0.2.40', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO proxy_bindings(bind_key, proxy_id, ip_version, expire_at, created_at, last_used_at)
VALUES ('historical@example.test', 990645, 'ipv4', '2026-08-01 00:00:00', '2026-07-01 00:00:00', '2026-07-02 00:00:00')`).Error)

	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 64))
	var rows []struct {
		ID            uint   `gorm:"column:id"`
		ProxyServerID uint   `gorm:"column:proxy_server_id"`
		ServerIP      string `gorm:"column:server_ip"`
	}
	require.NoError(t, db.Table("proxies AS p").
		Select("p.id, p.proxy_server_id, s.server_ip").
		Joins("JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("p.id IN ?", []uint{990641, 990642, 990643, 990644}).
		Order("p.id").Scan(&rows).Error)
	require.Len(t, rows, 4)
	require.Equal(t, rows[0].ProxyServerID, rows[1].ProxyServerID)
	require.Equal(t, "192.0.2.40", rows[0].ServerIP)
	require.Equal(t, rows[2].ProxyServerID, rows[3].ProxyServerID)
	require.Equal(t, "2001:db8::1", rows[2].ServerIP)
	require.NotEqual(t, rows[0].ProxyServerID, rows[2].ProxyServerID)
	var lastAssignedAt string
	require.NoError(t, db.Raw(
		"SELECT DATE_FORMAT(last_assigned_at, '%Y-%m-%d %H:%i:%s') FROM proxies WHERE id = ?",
		990645,
	).Scan(&lastAssignedAt).Error)
	require.Equal(t, "2026-07-02 00:00:00", lastAssignedAt)
}
