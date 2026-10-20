package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	mailapp "github.com/donnel666/remail/internal/mailmatch/app"
	maildomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftEnterpriseRefundSweepHasOwnBudgetMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft_enterprise")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(3048,'microsoft_enterprise',1)").Error)
	require.NoError(t, db.Exec("INSERT INTO microsoft_enterprise_resources(id,email_address,email_domain,status,for_sale,alloc_bucket) VALUES(3048,'refund@tenant.onmicrosoft.com','tenant.onmicrosoft.com','normal',TRUE,1000)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id,project_id,type,status,code_enabled,code_price,code_supplier_price,code_window_minutes,main_weight) VALUES(21,10,'microsoft','enabled',TRUE,1,0.5,10,1)").Error)
	ctx := context.Background()
	uc := newTradeUseCase(db)
	for _, id := range []uint{20, 21} {
		_, err := uc.Checkout(ctx, tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: id, ServiceMode: "code", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: map[uint]string{20: "enterprise-sweep", 21: "personal-sweep"}[id]})
		require.NoError(t, err)
	}
	for _, table := range []string{"microsoft_resources", "microsoft_enterprise_resources"} {
		require.NoError(t, db.Table(table).Where("id IN ?", []uint{1000, 3048}).Update("status", "abnormal").Error)
	}
	repo := tradeinfra.NewRepo(db)
	personal, err := repo.ListUnavailableMicrosoftOrders(ctx, 0, 1)
	require.NoError(t, err)
	require.Len(t, personal, 1)
	require.EqualValues(t, 1000, personal[0].ResourceID)
	enterprise, err := repo.ListUnavailableMicrosoftEnterpriseOrders(ctx, 1)
	require.NoError(t, err)
	require.Len(t, enterprise, 1)
	require.EqualValues(t, 3048, enterprise[0].ResourceID)
	result, err := uc.ExpireDueOrders(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 2, result.ResourceUnavailableRefunded)
}

type enterpriseCommerceMail struct{ uc *tradeapp.UseCase }

func (f enterpriseCommerceMail) FetchMicrosoftMessages(_ context.Context, req mailapp.FetchMessagesRequest) (*mailapp.FetchMessagesResult, error) {
	return &mailapp.FetchMessagesResult{Messages: []mailapp.FetchedMessage{{
		EmailResourceID: req.Scope.EmailResourceID, ResourceType: maildomain.ResourceTypeMicrosoftEnterprise,
		Recipient: req.Scope.Recipient, Sender: "service@example.test", Subject: "Verification code",
		Body: "Your code is 654321", ProviderMessageID: "enterprise-message", Protocol: "graph", Folder: "INBOX", ReceivedAt: time.Now().UTC(),
	}}}, nil
}

func (f enterpriseCommerceMail) NotifyMatchedCode(ctx context.Context, result mailapp.MatchResult) error {
	return f.uc.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{OrderNo: result.OrderNo, MatchedAt: result.MatchedAt})
}

func TestMicrosoftEnterpriseCheckoutPickupRefundAndOwnershipMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft_enterprise")
	seedTradeMicrosoftResources(t, db, 1, 1001, 1, true)
	creditBuyer(t, db, 2, "10.00")
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(1000,'microsoft_enterprise',1),(2024,'microsoft_enterprise',1)").Error)
	require.NoError(t, db.Exec(`INSERT INTO microsoft_enterprise_resources(id,email_address,email_domain,password,client_id,refresh_token,status,for_sale,alloc_bucket) VALUES
(1000,'one@tenant.onmicrosoft.com','tenant.onmicrosoft.com','enterprise-password','client','enterprise-refresh','normal',TRUE,1000),
(2024,'two@tenant.onmicrosoft.com','tenant.onmicrosoft.com','enterprise-password','client','enterprise-refresh','normal',TRUE,1000)`).Error)
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(project_id,rule_type,pattern,enabled) VALUES(10,'body','([0-9]{6})',TRUE)").Error)
	ctx := context.Background()
	uc := newTradeUseCase(db)
	request := tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, EmailSuffix: "tenant.onmicrosoft.com", ServiceMode: "code", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: "enterprise-checkout"}
	first, err := uc.Checkout(ctx, request)
	require.NoError(t, err)
	require.Equal(t, tradedomain.AllocationTypeMicrosoftEnterprise, *first.Order.AllocationType)
	require.Equal(t, "1.00", first.Order.PayAmount)
	require.Equal(t, "0.50", first.Order.SupplierAmount)
	require.NotEmpty(t, first.ServiceToken)
	replay, err := uc.Checkout(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first.Order.OrderNo, replay.Order.OrderNo)
	request.IdempotencyKey = "enterprise-refund"
	second, err := uc.Checkout(ctx, request)
	require.NoError(t, err)
	_, err = uc.GetOrder(ctx, first.Order.OrderNo, 3, false)
	require.ErrorIs(t, err, tradedomain.ErrOrderForbidden)
	_, err = uc.GetOrder(ctx, first.Order.OrderNo, 1, true)
	require.NoError(t, err)
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "enterprise-password")
	require.NotContains(t, string(encoded), "enterprise-refresh")
	allocations := allocinfra.NewRepo(db)
	active, err := allocations.ListActiveByRecipient(ctx, first.Order.DeliveryEmail)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, allocdomain.AllocationTypeMicrosoftEnterprise, active[0].Type)

	mailRepo := mailinfra.NewRepo(db, nil)
	_, err = mailRepo.LoadOrderScope(ctx, first.Order.OrderNo, 3, false)
	require.ErrorIs(t, err, maildomain.ErrOrderForbidden)
	_, err = mailRepo.LoadPickupScope(ctx, first.ServiceToken, second.Order.DeliveryEmail)
	require.ErrorIs(t, err, maildomain.ErrPickupCredentialInvalid)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	lease := mailinfra.NewPickupFetchState(redisClient)
	fetcher := enterpriseCommerceMail{uc: uc}
	mail := mailapp.NewUseCase(mailRepo, nil, fetcher, fetcher)
	mail.SetPickupFetchStatePort(lease)
	acquired, err := lease.Acquire(ctx, active[0].ResourceID, "enterprise-test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, mail.ProcessFetch(ctx, mailapp.FetchTask{OrderNo: first.Order.OrderNo, EmailResourceID: active[0].ResourceID, LeaseToken: "enterprise-test", RequestedAt: time.Now()}))
	read, err := mailRepo.ReadPickupBatch(ctx, []mailapp.PickupCredential{{Token: first.ServiceToken, Email: first.Order.DeliveryEmail}}, time.Now(), 10, 10)
	require.NoError(t, err)
	require.Len(t, read, 1)
	require.NotNil(t, read[0].Delivery)
	require.Equal(t, "654321", read[0].Delivery.Message.VerificationCode)

	secondAllocation, err := allocations.FindAllocationByOrder(ctx, second.Order.OrderNo)
	require.NoError(t, err)
	require.NoError(t, db.Table("microsoft_enterprise_resources").Where("id=?", secondAllocation.ResourceID).Update("status", "abnormal").Error)
	refunded, err := uc.RefundUnavailableMicrosoftOrders(ctx, secondAllocation.ResourceID, "enterprise-refund")
	require.NoError(t, err)
	require.Equal(t, 1, refunded)
	refunded, err = uc.RefundUnavailableMicrosoftOrders(ctx, secondAllocation.ResourceID, "enterprise-refund-replay")
	require.NoError(t, err)
	require.Zero(t, refunded)
	released, err := allocations.FindAllocationByOrder(ctx, second.Order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, allocdomain.AllocationStatusReleased, released.Status)
	_, err = mailRepo.LoadPickupScope(ctx, second.ServiceToken, second.Order.DeliveryEmail)
	require.Error(t, err)
	wallet, err := billinginfra.NewBillingRepo(db).GetOrCreateWalletSummary(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, "9.00", wallet.Wallet.ConsumerBalance)
	supplier, err := billinginfra.NewBillingRepo(db).GetOrCreateWalletSummary(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, supplier.SupplierAllocationCount)
	var personalStatus string
	require.NoError(t, db.Table("microsoft_resources").Select("status").Where("id=1001").Scan(&personalStatus).Error)
	require.Equal(t, "normal", personalStatus)
	var personalAllocations int64
	require.NoError(t, db.Table("microsoft_allocations").Count(&personalAllocations).Error)
	require.Zero(t, personalAllocations)
}
