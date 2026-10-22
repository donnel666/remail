package infra

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMicrosoftEnterpriseSupplierQueryStartsWithEnterpriseAllocationsMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ownerID := createBillingTestUser(t, db, "query-owner@example.com")
	otherID := createBillingTestUser(t, db, "query-other@example.com")
	require.NoError(t, db.Exec(`INSERT INTO projects(id,name,target_platform,status) VALUES(9701,'Query fixture','test','listed')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(id,project_id,type,main_weight) VALUES(9701,9701,'microsoft_enterprise',1)`).Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(9701,'microsoft_enterprise',?)", ownerID).Error)
	require.NoError(t, db.Exec(`INSERT INTO microsoft_enterprise_resources(id,email_address,email_domain,password)
VALUES(9701,'query@fixture.onmicrosoft.com','fixture.onmicrosoft.com','fixture')`).Error)
	guards := make([]map[string]any, 512)
	for i := range guards {
		guards[i] = map[string]any{"order_no": fmt.Sprintf("OTHER-GUARD-%04d", i), "type": "microsoft", "owner_user_id": otherID}
	}
	require.NoError(t, db.Table("allocation_order_guards").CreateInBatches(guards, 100).Error)
	for i, snapshot := range []any{ownerID, nil} {
		name := fmt.Sprintf("SUPPLIER-QUERY-%d", i)
		require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no,type,owner_user_id) VALUES(?,'microsoft_enterprise',?)", name, snapshot).Error)
		require.NoError(t, db.Exec(`INSERT INTO microsoft_enterprise_allocations(order_no,project_id,product_id,resource_id,supply_scope,mailbox,email,status,released_at)
VALUES(?,9701,9701,9701,'public','main','query@fixture.onmicrosoft.com','released',NOW())`, name).Error)
	}
	query := supplierMicrosoftEnterpriseAllocationsSQL
	var names []string
	require.NoError(t, db.Raw(query, ownerID).Scan(&names).Error)
	require.ElementsMatch(t, []string{"SUPPLIER-QUERY-0", "SUPPLIER-QUERY-1"}, names)
	var plans []struct {
		ID    int
		Table string
		Type  string
		Key   string
		Rows  int
	}
	require.NoError(t, db.Raw("EXPLAIN "+query, ownerID).Scan(&plans).Error)
	require.Len(t, plans, 3)
	require.Equal(t, "ma", plans[0].Table, "enterprise allocations must drive this branch instead of shared guards")
	var seenGuard, seenResource bool
	for _, row := range plans {
		if row.Table == "guard" {
			seenGuard = true
			require.Equal(t, "eq_ref", row.Type)
			require.Equal(t, "PRIMARY", row.Key)
			require.Equal(t, 1, row.Rows)
		}
		if row.Table == "er" {
			seenResource = true
			require.Equal(t, "eq_ref", row.Type)
			require.Equal(t, "PRIMARY", row.Key)
			require.Equal(t, 1, row.Rows)
		}
	}
	require.True(t, seenGuard)
	require.True(t, seenResource)
}
