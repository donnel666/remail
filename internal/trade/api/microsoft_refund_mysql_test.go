package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	tradeinfra "github.com/donnel666/remail/internal/trade/infra"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type microsoftRefundRaceRepo struct {
	*tradeinfra.Repo
	afterList func(context.Context, []tradeapp.MicrosoftUnavailableOrder) error
	afterLock func(context.Context, tradeapp.MicrosoftUnavailableOrder) error
}

func (r *microsoftRefundRaceRepo) ListUnavailableMicrosoftOrders(ctx context.Context, id uint, limit int) ([]tradeapp.MicrosoftUnavailableOrder, error) {
	proofs, err := r.Repo.ListUnavailableMicrosoftOrders(ctx, id, limit)
	if err == nil && r.afterList != nil {
		err = r.afterList(ctx, proofs)
	}
	return proofs, err
}
func (r *microsoftRefundRaceRepo) AssertMicrosoftRefundFailure(ctx context.Context, proof tradeapp.MicrosoftUnavailableOrder) error {
	if err := r.Repo.AssertMicrosoftRefundFailure(ctx, proof); err != nil {
		return err
	}
	if r.afterLock != nil {
		return r.afterLock(ctx, proof)
	}
	return nil
}
func seedMicrosoftRefundOrder(t *testing.T, db *gorm.DB, uc *tradeapp.UseCase, id uint) *tradeapp.CheckoutResult {
	t.Helper()
	seedTradeMicrosoftResources(t, db, 1, int(id), 1, true)
	require.NoError(t, db.Table("microsoft_resources").Where("id=?", id).Updates(map[string]any{"alloc_bucket": 1000, "validation_generation": 0}).Error)
	result, err := uc.Checkout(context.Background(), tradeapp.CheckoutRequest{UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "purchase", SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole, IdempotencyKey: fmt.Sprintf("microsoft-refund-%d", id)})
	require.NoError(t, err)
	require.NotNil(t, result.Order.DebitTxID)
	return result
}
func assertMicrosoftOrderStillActive(t *testing.T, db *gorm.DB, order *tradeapp.CheckoutResult) {
	t.Helper()
	current, err := tradeinfra.NewRepo(db).FindOrder(context.Background(), order.Order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusActive, current.Status)
	require.Nil(t, current.RefundTxID)
	var active int64
	require.NoError(t, db.Table("microsoft_allocations").Where("order_no = ? AND status = 'allocated'", current.OrderNo).Count(&active).Error)
	require.EqualValues(t, 1, active)
	require.NoError(t, db.Table("order_tokens").Where("order_no = ? AND enabled = TRUE", current.OrderNo).Count(&active).Error)
	require.EqualValues(t, 1, active)
	require.NoError(t, db.Table("wallet_transactions").Where("idempotency_key = ? AND transaction_type = 'refund'", "order:"+current.OrderNo+":refund").Count(&active).Error)
	require.Zero(t, active)
}

func TestMicrosoftRefundRechecksRecoveryAndNewCredentialProofMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "microsoft")
	creditBuyer(t, db, 2, "100.00")
	for i, change := range []string{"normal", "credential", "generation"} {
		t.Run(change, func(t *testing.T) {
			repo := &microsoftRefundRaceRepo{Repo: tradeinfra.NewRepo(db)}
			uc := resourceRefundUseCase(db, repo)
			id := uint(5100 + i)
			order := seedMicrosoftRefundOrder(t, db, uc, id)
			require.NoError(t, db.Table("microsoft_resources").Where("id = ?", id).Updates(map[string]any{"status": "abnormal"}).Error)
			selected, release := make(chan struct{}), make(chan struct{})
			repo.afterList = func(ctx context.Context, proofs []tradeapp.MicrosoftUnavailableOrder) error {
				if len(proofs) != 1 {
					return fmt.Errorf("expected one failure proof, got %d", len(proofs))
				}
				require.Zero(t, proofs[0].ValidationGeneration, "legacy generation zero remains a valid proof")
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
			go func() {
				count, err := uc.RefundUnavailableMicrosoftOrders(ctx, id, "race")
				finished <- outcome{count, err}
			}()
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
					updates["status"] = "normal"
				case "credential":
					updates["credential_revision"] = gorm.Expr("credential_revision + 1")
				case "generation":
					updates["validation_generation"] = gorm.Expr("validation_generation + 1")
				}
				return tx.Table("microsoft_resources").Where("id = ?", id).Updates(updates).Error
			}))
			close(release)
			got := <-finished
			require.NoError(t, got.err)
			require.Zero(t, got.count)
			assertMicrosoftOrderStillActive(t, db, order)
		})
	}
}

func TestMicrosoftRefundHoldsResourceFenceThroughWalletCreditMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	seedTradeBase(t, db, "microsoft")
	creditBuyer(t, db, 2, "100.00")
	repo := &microsoftRefundRaceRepo{Repo: tradeinfra.NewRepo(db)}
	uc := resourceRefundUseCase(db, repo)
	id := uint(5200)
	order := seedMicrosoftRefundOrder(t, db, uc, id)
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", id).Updates(map[string]any{"status": "abnormal"}).Error)
	checked := false
	repo.afterLock = func(_ context.Context, proof tradeapp.MicrosoftUnavailableOrder) error {
		checked = true
		require.Zero(t, proof.ValidationGeneration)
		err := db.Transaction(func(other *gorm.DB) error {
			var root struct{ ID uint }
			return other.Raw("SELECT id FROM email_resources WHERE id = ? FOR UPDATE NOWAIT", proof.ResourceID).Scan(&root).Error
		})
		var busy *mysql.MySQLError
		require.ErrorAs(t, err, &busy)
		require.EqualValues(t, 3572, busy.Number, "a concurrent credential update must wait for the refund decision")
		err = db.Transaction(func(other *gorm.DB) error {
			var allocation struct{ ResourceID uint }
			return other.Raw("SELECT resource_id FROM microsoft_allocations WHERE order_no=? FOR UPDATE NOWAIT", proof.OrderNo).Scan(&allocation).Error
		})
		require.ErrorAs(t, err, &busy)
		require.EqualValues(t, 3572, busy.Number, "allocation releases must wait for the refund decision")
		return nil
	}
	count, err := uc.RefundUnavailableMicrosoftOrders(context.Background(), id, "valid-proof")
	require.NoError(t, err)
	require.True(t, checked)
	require.Equal(t, 1, count)
	current, err := repo.FindOrder(context.Background(), order.Order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, tradedomain.OrderStatusRefunded, current.Status)
	require.NotNil(t, current.RefundTxID)
	count, err = uc.RefundUnavailableMicrosoftOrders(context.Background(), id, "replay")
	require.NoError(t, err)
	require.Zero(t, count)
	var enabled int64
	require.NoError(t, db.Table("order_tokens").Where("order_no = ? AND enabled = TRUE", current.OrderNo).Count(&enabled).Error)
	require.Zero(t, enabled)
}
