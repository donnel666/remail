package infra

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtoMigrationAndTypedAllocationConstraintsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	for _, table := range []string{"proto_resources", "proto_resource_imports", "proto_resource_import_items", "proto_maintenance_runs", "proto_allocations"} {
		require.True(t, db.Migrator().HasTable(table), table)
	}
	for _, column := range []string{"proto_alloc_id", "proto_resource_id", "resource_id"} {
		require.False(t, db.Migrator().HasColumn("orders", column), column)
	}
	for _, pair := range [][2]string{{"email_resources", "chk_email_resources_type"}, {"orders", "chk_orders_product_type"}, {"orders", "chk_orders_allocation_shape"}, {"allocation_order_guards", "chk_allocation_order_guards_type"}} {
		var clause string
		require.NoError(t, db.Raw(`SELECT check_clause FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = ?`, pair[1]).Scan(&clause).Error)
		require.Contains(t, strings.ToLower(clause), "proto")
	}
	require.True(t, db.Migrator().HasIndex("proto_allocations", "uq_proto_allocation_active_project_resource"))

	seedAllocBase(t, db, "proto", 1, 0, 0)
	var resourceID uint
	require.NoError(t, db.Exec(`INSERT INTO email_resources(type, owner_user_id) VALUES ('proto', 1)`).Error)
	require.NoError(t, db.Raw(`SELECT LAST_INSERT_ID()`).Scan(&resourceID).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_resources(id, resource_type, owner_user_id, email_address, email_domain, password, status) VALUES (?, 'proto', 1, 'one@proton.me', 'proton.me', 'pw', 'normal')`, resourceID).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no, type) VALUES ('PROTO-MIGRATION-1', 'proto')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_allocations(order_no, project_id, product_id, resource_id, owner_user_id, guard_type, supply_scope, service_mode, mailbox, email) VALUES ('PROTO-MIGRATION-1', 10, 20, ?, 1, 'proto', 'public', 'code', 'main', 'one@proton.me')`, resourceID).Error)

	// A second active allocation for the same resource and project is rejected,
	// while a released historical row remains representable.
	require.NoError(t, db.Exec(`UPDATE proto_allocations SET status = 'released', released_at = CURRENT_TIMESTAMP WHERE order_no = 'PROTO-MIGRATION-1'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no, type) VALUES ('PROTO-MIGRATION-2', 'proto')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_allocations(order_no, project_id, product_id, resource_id, owner_user_id, guard_type, supply_scope, service_mode, mailbox, email) VALUES ('PROTO-MIGRATION-2', 10, 20, ?, 1, 'proto', 'public', 'code', 'main', 'one@proton.me')`, resourceID).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no, type) VALUES ('PROTO-MIGRATION-3', 'proto')`).Error)
	require.Error(t, db.Exec(`INSERT INTO proto_allocations(order_no, project_id, product_id, resource_id, owner_user_id, guard_type, supply_scope, service_mode, mailbox, email) VALUES ('PROTO-MIGRATION-3', 10, 20, ?, 1, 'proto', 'public', 'code', 'main', 'one@proton.me')`, resourceID).Error)
}
