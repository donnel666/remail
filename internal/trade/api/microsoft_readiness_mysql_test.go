package api

import (
	"context"
	"strings"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	"github.com/donnel666/remail/internal/platform/testmysql"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMicrosoftPaidRecoveryRefundsUnavailableAllocationBeforeIssuingTokenMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")
	repo := tradeinfra.NewRepo(db)
	ctx := context.Background()
	order, created, err := repo.LoadOrCreatePendingOrder(ctx, tradeapp.CreatePendingOrderCommand{
		OrderNo: testmysql.AllocationOrderNo("PAID-UNREADY-MS", 10, "main", 1000, allocapp.MicrosoftBucketCount),
		UserID:  2, ProjectID: 10, ProjectProductID: 20, ProductType: tradedomain.ProductTypeMicrosoft,
		ServiceMode: tradedomain.ServiceModeCode, SupplyPolicy: tradedomain.SupplyPolicyPublicOnly, PayAmount: "1.00",
		CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 1440, ClientChannel: tradedomain.ClientChannelConsole,
		IdempotencyKey: "paid-unready-ms", RequestFingerprint: strings.Repeat("b", 64), Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = allocapp.NewUseCase(allocinfra.NewRepo(db)).Allocate(ctx, allocapp.AllocateCommand{
		OrderNo: order.OrderNo, BuyerUserID: order.UserID, ProjectProductID: order.ProjectProductID,
		SupplyScope: allocdomain.SupplyScopePublic, FulfillExistingOrder: true,
	})
	require.NoError(t, err)
	wallet := billingWalletAdapter{wallet: billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))}
	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := wallet.LockConsumer(txCtx, order.UserID); err != nil {
			return err
		}
		debit, err := wallet.DebitConsumer(txCtx, tradeapp.WalletCommand{UserID: order.UserID, Amount: order.PayAmount, Reason: "order:" + order.OrderNo, IdempotencyKey: "order:" + order.OrderNo + ":debit"})
		if err != nil {
			return err
		}
		_, err = repo.MarkPaid(txCtx, tradeapp.MarkPaidCommand{OrderNo: order.OrderNo, DebitTxID: debit.ID, PayAmount: order.PayAmount})
		return err
	}))
	require.NoError(t, db.Table("microsoft_resources").Where("id=1000").Update("status", "abnormal").Error)
	require.NoError(t, db.Table("orders").Where("order_no=?", order.OrderNo).Update("updated_at", time.Now().UTC().Add(-16*time.Minute)).Error)
	uc := newTradeUseCase(db)
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	current, err := repo.FindOrder(ctx, order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusFailed, current.Status)
	require.NotNil(t, current.RefundTxID)
	var active int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no=? AND status='allocated'", order.OrderNo).Count(&active).Error)
	require.Zero(t, active)
	require.NoError(t, db.Table("order_tokens").Where("order_no=? AND enabled=TRUE", order.OrderNo).Count(&active).Error)
	require.Zero(t, active)
	var buyer billinginfra.WalletModel
	require.NoError(t, db.First(&buyer, "user_id=2").Error)
	require.Equal(t, "10.000000", buyer.ConsumerBalance)
	_, err = uc.ExpireDueOrders(ctx, 200)
	require.NoError(t, err)
	var refunds int64
	require.NoError(t, db.Table("wallet_transactions").Where("idempotency_key=?", "order:"+order.OrderNo+":refund").Count(&refunds).Error)
	require.EqualValues(t, 1, refunds)
}
