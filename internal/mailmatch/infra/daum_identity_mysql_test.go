package infra

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type collisionDaumFetcher struct{ message app.FetchedMessage }

func (f *collisionDaumFetcher) FetchDaumMessages(context.Context, app.FetchMessagesRequest) (*app.FetchMessagesResult, error) {
	return &app.FetchMessagesResult{Messages: []app.FetchedMessage{f.message}}, nil
}

type collisionCodeNotify struct{ results []app.MatchResult }

func (n *collisionCodeNotify) NotifyMatchedCode(_ context.Context, r app.MatchResult) error {
	n.results = append(n.results, r)
	return nil
}
func TestDaumMessageIDCollisionCannotDeliverAnotherRecipientFactMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "REVIEW-DAUM-DUPE")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(101,'daum',1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,resource_type,owner_user_id,email_address,email_domain,app_password,status,credential_revision) VALUES(101,'daum',1,'one@daum.net','daum.net','synthetic','normal',7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(id,project_id,type,status,code_enabled,purchase_enabled,code_price,purchase_price,code_supplier_price,purchase_supplier_price,code_window_minutes,activation_window_minutes,warranty_minutes,main_weight,dot_weight,plus_weight) VALUES(21,10,'daum','enabled',TRUE,TRUE,1,2,0.5,1,10,60,60,1,0,0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no,type) VALUES('REVIEW-DAUM-DUPE','daum')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_explicit_aliases(id,resource_id,owner_user_id,email_address,email_domain,status) VALUES(301,101,1,'one@hanmail2.net','hanmail2.net','normal')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_allocations(order_no,project_id,product_id,resource_id,owner_user_id,guard_type,supply_scope,service_mode,mailbox,explicit_alias_id,email) VALUES('REVIEW-DAUM-DUPE',10,21,101,1,'daum','public','code','alias',301,'one@hanmail2.net')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO wallet_transactions(transaction_no,user_id,transaction_type,balance_bucket,direction,amount,balance_before,balance_after,biz_type,biz_id,idempotency_key) VALUES('REVIEW-DAUM-TX',2,'debit','consumer','out',-1,10,9,'order','REVIEW-DAUM-DUPE','REVIEW-DAUM-TX')`).Error)
	var debit uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no='REVIEW-DAUM-TX'").Scan(&debit).Error)
	require.NoError(t, db.Table("orders").Where("id=?", orderID).Updates(map[string]any{"project_product_id": 21, "product_type": "daum", "status": "active", "allocation_type": "daum", "debit_tx_id": debit, "delivery_email": "one@hanmail2.net", "receive_started_at": now.Add(-time.Minute), "receive_until": now.Add(time.Hour)}).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_mail_rules(project_id,rule_type,pattern,enabled) VALUES(10,'recipient','exact',TRUE),(10,'sender','service',TRUE),(10,'body','([0-9]{6})',TRUE)`).Error)
	repo := NewRepo(db, nil)
	notify := &collisionCodeNotify{}
	fetch := &collisionDaumFetcher{message: app.FetchedMessage{EmailResourceID: 101, ResourceType: domain.ResourceTypeDaum, CredentialRevision: 7, Recipient: "one@daum.net", Sender: "private@example.test", Subject: "main secret", Body: "PRIVATE MAIN BODY", MessageIDHeader: "repeated@example.test", ProviderMessageID: "imap:INBOX:1:1", Protocol: "imap", Folder: "INBOX", ReceivedAt: now}}
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	state := NewPickupFetchState(redisClient)
	uc := app.NewUseCase(repo, nil, nil, notify)
	uc.SetDaumMailFetchPort(fetch)
	uc.SetPickupFetchStatePort(state)
	for round := 0; round < 2; round++ {
		if round == 1 {
			fetch.message.Recipient = "one@hanmail2.net"
			fetch.message.Sender = "service@example.test"
			fetch.message.Subject = "code"
			fetch.message.Body = "New alias code 654321"
			fetch.message.ProviderMessageID = "imap:INBOX:1:2"
			fetch.message.ReceivedAt = now.Add(time.Second)
		}
		acquired, err := state.Acquire(context.Background(), 101, "review", time.Minute)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NoError(t, uc.ProcessFetch(context.Background(), app.FetchTask{OrderNo: "REVIEW-DAUM-DUPE", EmailResourceID: 101, LeaseToken: "review", RequestedAt: time.Now()}))
	}
	delivery, err := repo.FindOrderDelivery(context.Background(), orderID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.Message)
	message, err := repo.FindOrderMessage(context.Background(), orderID, delivery.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "one@hanmail2.net", message.Recipient)
	require.Equal(t, "New alias code 654321", message.RawBody)
	require.Equal(t, "654321", message.VerificationCode)
	require.Len(t, notify.results, 1)
	var count int64
	require.NoError(t, db.Model(&MessageModel{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
