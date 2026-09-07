package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestProtoMailScopesAndFetchFenceMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "PROTO-MAIL-1")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id) VALUES (101, 'proto', 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_resources(id, resource_type, owner_user_id, email_address, password, status, credential_revision)
		VALUES (101, 'proto', 1, 'one@example.com', 'secret', 'normal', 7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled,
		code_price, purchase_price, code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
		warranty_minutes, main_weight, dot_weight, plus_weight)
		VALUES (21, 10, 'proto', 'enabled', TRUE, TRUE, 1, 2, 0.5, 1, 10, 60, 60, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no,type) VALUES ('PROTO-MAIL-1','proto')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_allocations(order_no, project_id, product_id, resource_id, owner_user_id,
		guard_type, supply_scope, service_mode, mailbox, email)
		VALUES ('PROTO-MAIL-1',10,21,101,1,'proto','public','code','main','one@example.com')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO wallet_transactions(transaction_no,user_id,transaction_type,balance_bucket,direction,
		amount,balance_before,balance_after,biz_type,biz_id,idempotency_key)
		VALUES ('TX-PROTO-MAIL',2,'debit','consumer','out',-1,10,9,'order','PROTO-MAIL-1','TX-PROTO-MAIL')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = 'TX-PROTO-MAIL'").Scan(&debitID).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"project_product_id": 21, "product_type": "proto", "status": "active", "allocation_type": "proto",
		"debit_tx_id": debitID, "delivery_email": "one@example.com", "receive_started_at": now.Add(-time.Minute),
		"receive_until": now.Add(time.Hour),
	}).Error)
	require.NoError(t, db.Exec(`INSERT INTO order_tokens(token_prefix,token_plain,order_no,enabled)
		VALUES ('proto-prefix','proto-token','PROTO-MAIL-1',1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_mail_rules(project_id,rule_type,pattern,enabled)
		VALUES (10,'recipient','exact',TRUE),(10,'sender','service',TRUE)`).Error)

	ctx := context.Background()
	repo := NewRepo(db, nil)
	scope, err := repo.LoadOrderScope(ctx, "PROTO-MAIL-1", 2, false)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceTypeProto, scope.AllocationType)
	require.Equal(t, uint(101), scope.EmailResourceID)
	require.Equal(t, uint64(7), scope.CredentialRevision)
	require.Empty(t, scope.MicrosoftRT)
	_, err = repo.LoadOrderScope(ctx, "PROTO-MAIL-1", 3, false)
	require.ErrorIs(t, err, domain.ErrOrderForbidden)
	pickup, err := repo.LoadPickupScope(ctx, "proto-token", "one@example.com")
	require.NoError(t, err)
	require.Equal(t, scope.AllocationID, pickup.AllocationID)
	require.Equal(t, scope.CredentialRevision, pickup.CredentialRevision)
	_, err = repo.LoadPickupScope(ctx, "proto-token", "one+tag@example.com")
	require.ErrorIs(t, err, domain.ErrPickupCredentialInvalid)
	reads, err := repo.ReadPickupBatch(ctx, []app.PickupCredential{
		{Token: "proto-token", Email: "one@example.com"}, {Token: "proto-token", Email: "other@example.com"},
	}, now, 1, 30)
	require.NoError(t, err)
	require.NoError(t, reads[0].Err)
	require.Equal(t, uint(101), reads[0].Scope.EmailResourceID)
	require.ErrorIs(t, reads[1].Err, domain.ErrPickupCredentialInvalid)
	scopes, err := repo.ListMatchingScopesByRecipient(ctx, domain.ResourceTypeProto, 101, "one@example.com", now)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	scopes, err = repo.ListMatchingScopesByRecipient(ctx, domain.ResourceTypeProto, 101, "one+tag@example.com", now)
	require.NoError(t, err)
	require.Empty(t, scopes)

	fetchRepo := NewAdminResourceFetchRepo(db)
	job := domain.ResourceFetchJob{Kind: domain.ResourceFetchJobFetch, ResourceType: domain.ResourceTypeProto,
		ResourceID: 101, OperatorUserID: 1, IdempotencyKey: "proto-fetch-1"}
	_, err = fetchRepo.CreateOrReuseResourceFetch(ctx, &job, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(7), job.ExpectedCredentialRevision)
	claimed, err := fetchRepo.MarkResourceFetchProcessing(ctx, 101, job.Generation)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, fetchRepo.AssertProtoResourceFetchFence(ctx, 101, job.Generation, 7))
	require.NoError(t, db.Table("proto_resources").Where("id = 101").Update("credential_revision", 8).Error)
	require.ErrorIs(t, fetchRepo.AssertProtoResourceFetchFence(ctx, 101, job.Generation, 7), domain.ErrResourceFetchCredentialChanged)
	require.ErrorIs(t, repo.AssertProtoCredentialRevision(ctx, 100, 7), domain.ErrResourceFetchNotFound)
}
