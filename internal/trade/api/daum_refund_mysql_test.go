package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type daumRefundRaceRepo struct {
	*tradeinfra.Repo
	afterList func(context.Context, []tradeapp.DaumUnavailableOrder) error
	afterLock func(context.Context, tradeapp.DaumUnavailableOrder) error
}

func (r *daumRefundRaceRepo) ListUnavailableDaumOrders(ctx context.Context, id uint, limit int) ([]tradeapp.DaumUnavailableOrder, error) {
	proofs, err := r.Repo.ListUnavailableDaumOrders(ctx, id, limit)
	if err == nil && r.afterList != nil {
		err = r.afterList(ctx, proofs)
	}
	return proofs, err
}
func (r *daumRefundRaceRepo) AssertDaumRefundFailure(ctx context.Context, proof tradeapp.DaumUnavailableOrder) error {
	if err := r.Repo.AssertDaumRefundFailure(ctx, proof); err != nil {
		return err
	}
	if r.afterLock != nil {
		return r.afterLock(ctx, proof)
	}
	return nil
}
func resourceRefundUseCase(db *gorm.DB, repo tradeapp.Repository) *tradeapp.UseCase {
	allocation := allocapp.NewUseCase(allocinfra.NewRepo(db))
	allocation.SetDaumProtocolReady(true)
	useCase := tradeapp.NewUseCase(repo, coreOrderingAdapter{projects: coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(db)), db: db}, billingWalletAdapter{wallet: billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))}, allocationAdapter{alloc: allocation}, orderTokenAdapter{tokens: openapiapp.NewUseCase(openapiinfra.NewRepo(db))})
	useCase.SetOrderDeliveryPort(orderDeliveryAdapter{db: db})
	return useCase
}
func seedDaumRefundOrder(t *testing.T, db *gorm.DB, uc *tradeapp.UseCase, id uint) *tradeapp.CheckoutResult {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id) VALUES(?,'daum',1)`, id).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,for_sale) VALUES(?,1,?,'fixture-app','normal',TRUE)`, id, fmt.Sprintf("refund%d@daum.net", id)).Error)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, EmailSuffix: "daum.net", ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("daum-refund-%d", id)})
	require.NoError(t, err)
	require.NotNil(t, result.Order.DebitTxID)
	return result
}
func assertDaumOrderStillActive(t *testing.T, db *gorm.DB, order *tradeapp.CheckoutResult) {
	t.Helper()
	current, err := tradeinfra.NewRepo(db).FindOrder(context.Background(), order.Order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, current.Status)
	require.Nil(t, current.RefundTxID)
	var active int64
	require.NoError(t, db.Table("daum_allocations").Where("order_no = ? AND status = 'allocated'", current.OrderNo).Count(&active).Error)
	require.EqualValues(t, 1, active)
	require.NoError(t, db.Table("order_tokens").Where("order_no = ? AND enabled = TRUE", current.OrderNo).Count(&active).Error)
	require.EqualValues(t, 1, active)
	require.NoError(t, db.Table("wallet_transactions").Where("idempotency_key = ? AND transaction_type = 'refund'", "order:"+current.OrderNo+":refund").Count(&active).Error)
	require.Zero(t, active)
}

func TestDaumRefundRechecksRecoveryAndNewCredentialProofMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "daum")
	creditBuyer(t, db, 2, "100.00")
	for i, change := range []string{"normal", "credential", "generation", "category"} {
		t.Run(change, func(t *testing.T) {
			repo := &daumRefundRaceRepo{Repo: tradeinfra.NewRepo(db)}
			uc := resourceRefundUseCase(db, repo)
			id := uint(5100 + i)
			order := seedDaumRefundOrder(t, db, uc, id)
			require.NoError(t, db.Table("daum_resources").Where("id = ?", id).Updates(map[string]any{"status": "abnormal", "failure_category": "invalid_credentials"}).Error)
			selected, release := make(chan struct{}), make(chan struct{})
			repo.afterList = func(ctx context.Context, proofs []tradeapp.DaumUnavailableOrder) error {
				if len(proofs) != 1 {
					return fmt.Errorf("expected one failure proof, got %d", len(proofs))
				}
				close(selected)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			type outcome struct {
				count int
				err   error
			}
			finished := make(chan outcome, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			go func() { count, err := uc.RefundUnavailableDaumOrders(ctx, id, "race"); finished <- outcome{count, err} }()
			select {
			case <-selected:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var root struct{ ID uint }
				if err := tx.Table("email_resources").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&root).Error; err != nil {
					return err
				}
				updates := map[string]any{}
				switch change {
				case "normal":
					updates["status"], updates["failure_category"] = "normal", ""
				case "credential":
					updates["credential_revision"] = gorm.Expr("credential_revision + 1")
				case "generation":
					updates["validation_generation"] = gorm.Expr("validation_generation + 1")
				case "category":
					updates["failure_category"] = "account_disabled"
				}
				return tx.Table("daum_resources").Where("id = ?", id).Updates(updates).Error
			}))
			close(release)
			got := <-finished
			require.NoError(t, got.err)
			if change == "category" {
				require.Equal(t, 1, got.count, "diagnostics cannot override the authoritative resource status")
				current, err := repo.FindOrder(ctx, order.Order.OrderNo)
				require.NoError(t, err)
				require.Equal(t, tradedomain.OrderStatusRefunded, current.Status)
			} else {
				require.Zero(t, got.count)
				assertDaumOrderStillActive(t, db, order)
			}
		})
	}
}

func TestDaumRefundHoldsResourceFenceThroughWalletCreditMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "daum")
	creditBuyer(t, db, 2, "100.00")
	repo := &daumRefundRaceRepo{Repo: tradeinfra.NewRepo(db)}
	uc := resourceRefundUseCase(db, repo)
	id := uint(5200)
	order := seedDaumRefundOrder(t, db, uc, id)
	require.NoError(t, db.Table("daum_resources").Where("id = ?", id).Updates(map[string]any{"status": "abnormal", "failure_category": "invalid_credentials"}).Error)
	checked := false
	repo.afterLock = func(_ context.Context, proof tradeapp.DaumUnavailableOrder) error {
		checked = true
		err := db.Transaction(func(other *gorm.DB) error {
			var root struct{ ID uint }
			return other.Raw("SELECT id FROM email_resources WHERE id = ? FOR UPDATE NOWAIT", proof.ResourceID).Scan(&root).Error
		})
		var busy *mysql.MySQLError
		require.ErrorAs(t, err, &busy)
		require.EqualValues(t, 3572, busy.Number, "a concurrent credential update must wait for the refund decision")
		err = db.Transaction(func(other *gorm.DB) error {
			var allocation struct{ ResourceID uint }
			return other.Raw("SELECT resource_id FROM daum_allocations WHERE order_no=? FOR UPDATE NOWAIT", proof.OrderNo).Scan(&allocation).Error
		})
		require.ErrorAs(t, err, &busy)
		require.EqualValues(t, 3572, busy.Number, "allocation releases must wait for the refund decision")
		return nil
	}
	count, err := uc.RefundUnavailableDaumOrders(context.Background(), id, "valid-proof")
	require.NoError(t, err)
	require.True(t, checked)
	require.Equal(t, 1, count)
	current, err := repo.FindOrder(context.Background(), order.Order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusRefunded, current.Status)
	require.NotNil(t, current.RefundTxID)
	count, err = uc.RefundUnavailableDaumOrders(context.Background(), id, "replay")
	require.NoError(t, err)
	require.Zero(t, count)
	var enabled int64
	require.NoError(t, db.Table("order_tokens").Where("order_no = ? AND enabled = TRUE", current.OrderNo).Count(&enabled).Error)
	require.Zero(t, enabled)
}

func TestDaumAbnormalResourcesRefundWithoutDiagnosticWhitelistMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "daum")
	creditBuyer(t, db, 2, "100.00")
	repo := tradeinfra.NewRepo(db)
	uc := resourceRefundUseCase(db, repo)
	for i, category := range []string{"", "invalid_credentials"} {
		t.Run(fmt.Sprintf("category-%d", i), func(t *testing.T) {
			id := uint(5300 + i)
			order := seedDaumRefundOrder(t, db, uc, id)
			require.NoError(t, db.Table("daum_resources").Where("id = ?", id).Updates(map[string]any{"status": "abnormal", "failure_category": category, "last_safe_error": "transport: retry budget exhausted"}).Error)
			proofs, err := repo.ListUnavailableDaumOrders(context.Background(), id, 200)
			require.NoError(t, err)
			require.Len(t, proofs, 1)
			swept, err := uc.ExpireDueOrders(context.Background(), 200)
			require.NoError(t, err)
			require.Equal(t, 1, swept.ResourceUnavailableRefunded)
			current, err := repo.FindOrder(context.Background(), order.Order.OrderNo)
			require.NoError(t, err)
			require.Equal(t, tradedomain.OrderStatusRefunded, current.Status)
			require.NotNil(t, current.RefundTxID)
		})
	}
}
