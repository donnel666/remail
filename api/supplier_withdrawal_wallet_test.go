package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	aftersaleapi "github.com/donnel666/remail/internal/aftersale/api"
	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	aftersaledomain "github.com/donnel666/remail/internal/aftersale/domain"
	aftersaleinfra "github.com/donnel666/remail/internal/aftersale/infra"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type withdrawalTestMail struct{}

func (withdrawalTestMail) SendTicketMail(context.Context, aftersaleapp.TicketMailCommand) error {
	return nil
}

type withdrawalTestFiles struct{}

func (withdrawalTestFiles) Save(context.Context, string, string, string, []byte) error { return nil }
func (withdrawalTestFiles) Read(context.Context, string) (string, []byte, error) {
	return "image/png", []byte("qr"), nil
}

type withdrawalTestPermissions map[string]bool

func (p withdrawalTestPermissions) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	return p[resource+":"+action], nil
}

type withdrawalUnavailableOwners struct{}

func (withdrawalUnavailableOwners) GetByIDs(context.Context, []uint) (map[uint]aftersaleapp.RequesterSummary, error) {
	return nil, errors.New("requester directory unavailable")
}

func (withdrawalUnavailableOwners) ListActiveSuperAdmins(context.Context) ([]aftersaleapp.RequesterSummary, error) {
	return nil, nil
}

func TestSupplierWithdrawalTicketMySQL(t *testing.T) {
	server := testmysql.New("remail_ticket_withdrawal_test")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	migrations, err := filepath.Abs("../migrations")
	require.NoError(t, err)
	db := server.Database(t, migrations)
	require.False(t, db.Migrator().HasColumn("wallet_transactions", "withdrawal_ticket_no"))
	require.False(t, db.Migrator().HasIndex("wallet_transactions", "uq_wallet_transactions_withdrawal_ticket"))
	ctx := context.Background()
	repo := aftersaleinfra.NewRepo(db)
	wallets := billingapp.NewWalletUseCase(billinginfra.NewBillingRepo(db))
	uc, err := aftersaleapp.NewUseCase(repo, nil, nil, withdrawalTestFiles{}, withdrawalTestMail{}, aftersaleapp.TicketMailConfig{ReplyLocalPart: "support", ReplyDomain: "example.com", ReplySecret: "test"})
	require.NoError(t, err)
	uc.SetSupplierWalletPort(supplierWithdrawalWalletAdapter{wallets: wallets})

	sequence := 0
	createTicket := func(t *testing.T, userID uint) string {
		view, err := uc.CreateSupplierWithdrawal(ctx, aftersaleapp.CreateSupplierWithdrawalRequest{RequesterUserID: userID, SupplierAccess: true, Amount: "260000", PaymentQRCode: "data:image/png;base64,cXI="})
		require.NoError(t, err)
		require.Equal(t, "260000", view.WithdrawalAmount)
		return view.Ticket.TicketNo
	}
	fixture := func(t *testing.T) (uint, string) {
		sequence++
		user := struct {
			ID                                  uint
			Email, PasswordHash, Nickname, Role string
		}{Email: fmt.Sprintf("withdraw-%d@example.com", sequence), PasswordHash: "test", Nickname: "Supplier", Role: "supplier"}
		require.NoError(t, db.Table("users").Create(&user).Error)
		require.NoError(t, db.Create(&billinginfra.WalletModel{UserID: user.ID, ConsumerBalance: "123.50", SupplierAvailable: "500000", SupplierFrozen: "50"}).Error)
		return user.ID, createTicket(t, user.ID)
	}
	newRouter := func(permissions withdrawalTestPermissions) *gin.Engine {
		r := gin.New()
		fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 42, iamdomain.RoleAdmin, "admin@example.com", true
		})
		aftersaleapi.RegisterRoutes(r.Group("/v1"), &aftersaleapi.Module{UseCase: uc}, fetcher, permissions, nil)
		return r
	}
	router := newRouter(withdrawalTestPermissions{"iam:user:operate": true, "billing:wallet:operate": true})
	request := func(r *gin.Engine, ticketNo string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/tickets/"+ticketNo+"/withdraw", strings.NewReader(`{"amount":"1","userId":9999}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middleware.CSRFHeaderName, "csrf")
		req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	assertWallet := func(t *testing.T, userID uint, available string, ledgerCount int64) {
		wallet, err := wallets.GetWallet(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, available, wallet.Wallet.SupplierAvailable)
		require.Equal(t, "123.50", wallet.Wallet.ConsumerBalance)
		require.Equal(t, "50.00", wallet.Wallet.SupplierFrozen)
		var count int64
		require.NoError(t, db.Model(&billinginfra.WalletTransactionModel{}).Where("user_id = ?", userID).Count(&count).Error)
		require.Equal(t, ledgerCount, count)
		require.NoError(t, db.Table("operation_logs").Where("resource_id = ? AND operation_type = ?", fmt.Sprint(userID), "billing.wallet.withdraw").Count(&count).Error)
		require.Equal(t, ledgerCount, count)
	}

	t.Run("requires both permissions", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		for _, permissions := range []withdrawalTestPermissions{{}, {"iam:user:operate": true}, {"billing:wallet:operate": true}} {
			require.Equal(t, http.StatusForbidden, request(newRouter(permissions), ticketNo).Code)
		}
		assertWallet(t, userID, "500000.00", 0)
	})
	t.Run("concurrent confirmations debit once and record the original amount", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		start, results := make(chan struct{}), make(chan int, 8)
		for range 8 {
			go func() { <-start; results <- request(router, ticketNo).Code }()
		}
		close(start)
		counts := map[int]int{}
		for range 8 {
			counts[<-results]++
		}
		require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: 7}, counts)
		assertWallet(t, userID, "240000.00", 1)
		ticket, err := repo.Get(ctx, ticketNo, true)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.ResolutionWithdrawn, ticket.Resolution.Kind)
		require.Equal(t, aftersaledomain.TicketStatusClosed, ticket.Status)
		require.Len(t, ticket.Messages, 2)
		var transaction billinginfra.WalletTransactionModel
		require.NoError(t, db.Where("user_id = ?", userID).First(&transaction).Error)
		require.Equal(t, "supplier_available", transaction.BalanceBucket)
		require.Equal(t, "-260000.000000", transaction.Amount)
		require.Equal(t, ticketNo, transaction.BizID)
		require.Equal(t, "withdrawal", transaction.TransactionType)
		require.Equal(t, billingdomain.BizTypeTicketWithdrawal, transaction.BizType)
		repeat := request(router, ticketNo)
		require.Equal(t, http.StatusConflict, repeat.Code)
		require.Contains(t, repeat.Body.String(), "withdrawal_already_processed")
	})
	t.Run("insufficient balance leaves ticket open", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", userID).Update("supplier_available", "1").Error)
		w := request(router, ticketNo)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "Withdrawal exceeds withdrawable balance.")
		assertWallet(t, userID, "1.00", 0)
		ticket, err := repo.Get(ctx, ticketNo, true)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.TicketStatusOpen, ticket.Status)
		require.Len(t, ticket.Messages, 1)
	})
	t.Run("close failure rolls back wallet ledger and audit then allows retry", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		callback := "test:fail_withdrawal_ticket_close"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "aftersale_tickets" {
				_ = tx.AddError(errors.New("forced close failure"))
			}
		}))
		w := request(router, ticketNo)
		require.NoError(t, db.Callback().Update().Remove(callback))
		require.Equal(t, http.StatusInternalServerError, w.Code)
		assertWallet(t, userID, "500000.00", 0)
		ticket, err := repo.Get(ctx, ticketNo, true)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.TicketStatusOpen, ticket.Status)
		require.Len(t, ticket.Messages, 1)
		w = request(router, ticketNo)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assertWallet(t, userID, "240000.00", 1)
	})
	t.Run("plain close does not withdraw", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		_, err := uc.CloseTicket(ctx, aftersaleapp.CloseTicketRequest{TicketNo: ticketNo, UserID: 42, AsPlatform: true})
		require.NoError(t, err)
		require.Equal(t, http.StatusConflict, request(router, ticketNo).Code)
		assertWallet(t, userID, "500000.00", 0)
	})
	t.Run("competing applications cannot overdraw the same wallet", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		second := createTicket(t, userID)
		start, results := make(chan struct{}), make(chan int, 2)
		for _, number := range []string{ticketNo, second} {
			go func() { <-start; results <- request(router, number).Code }()
		}
		close(start)
		codes := []int{<-results, <-results}
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusUnprocessableEntity}, codes)
		assertWallet(t, userID, "240000.00", 1)
	})
	t.Run("ticket withdrawal cannot be reversed independently", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.Equal(t, http.StatusOK, request(router, ticketNo).Code)
		var transaction billinginfra.WalletTransactionModel
		require.NoError(t, db.Where("user_id = ?", userID).First(&transaction).Error)
		_, err := wallets.ReverseTransaction(ctx, billingapp.ReverseTransactionRequest{TransactionID: transaction.ID, IdempotencyKey: "reverse-" + ticketNo})
		require.ErrorIs(t, err, billingdomain.ErrTransactionNotReversible)
		billingRepo := billinginfra.NewBillingRepo(db)
		original, err := billingRepo.GetAdminTransaction(ctx, transaction.ID)
		require.NoError(t, err)
		_, err = billingRepo.ReverseTransaction(ctx, billingapp.ReverseTransactionCommand{Original: original.Transaction, IdempotencyKey: "direct-reverse-" + ticketNo, RequestFingerprint: strings.Repeat("a", 64)})
		require.ErrorIs(t, err, billingdomain.ErrTransactionNotReversible)
		assertWallet(t, userID, "240000.00", 1)
	})
	t.Run("ticket withdrawal appears in the all ledger category", func(t *testing.T) {
		_, ticketNo := fixture(t)
		require.Equal(t, http.StatusOK, request(router, ticketNo).Code)
		listed, err := wallets.ListAdminTransactions(ctx, billingapp.AdminTransactionFilter{Category: billingapp.AdminTransactionCategoryAll, Search: ticketNo}, 0, 20)
		require.NoError(t, err)
		require.EqualValues(t, 1, listed.Total)
		require.Len(t, listed.Items, 1)
		require.Equal(t, ticketNo, listed.Items[0].Transaction.BizID)
	})
	t.Run("requester lookup failure does not follow a committed debit", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		uc.SetOwnerLookupPort(withdrawalUnavailableOwners{})
		defer uc.SetOwnerLookupPort(nil)
		require.Equal(t, http.StatusInternalServerError, request(router, ticketNo).Code)
		assertWallet(t, userID, "500000.00", 0)
		ticket, err := repo.Get(ctx, ticketNo, true)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.TicketStatusOpen, ticket.Status)
	})
	t.Run("manual entry cannot debit a processed ticket again", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", userID).Update("supplier_available", "1000000").Error)
		require.Equal(t, http.StatusOK, request(router, ticketNo).Code)
		_, err := wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{UserID: userID, Amount: "260000", Note: ticketNo, IdempotencyKey: "manual-" + ticketNo})
		require.Error(t, err)
		assertWallet(t, userID, "740000.00", 1)
	})
	t.Run("existing manual withdrawal blocks the ticket shortcut", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		_, err := wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{UserID: userID, Amount: "260000", Note: ticketNo, IdempotencyKey: "manual-" + ticketNo})
		require.NoError(t, err)
		w := request(router, ticketNo)
		require.Equal(t, http.StatusConflict, w.Code)
		require.Contains(t, w.Body.String(), "already recorded")
		wallet, err := wallets.GetWallet(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, "240000.00", wallet.Wallet.SupplierAvailable)
		ticket, err := repo.Get(ctx, ticketNo, false)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.TicketStatusOpen, ticket.Status)
	})
	t.Run("wallet lock and ledger check prevent repeat debit even without idempotency receipts", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.Equal(t, http.StatusOK, request(router, ticketNo).Code)
		require.NoError(t, db.Where("owner_user_id = ?", userID).Delete(&billinginfra.IdempotencyKeyModel{}).Error)
		_, err := wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{TicketNo: ticketNo, UserID: userID, Amount: "260000", IdempotencyKey: "another-key-" + ticketNo})
		require.ErrorIs(t, err, billingdomain.ErrWithdrawalAlreadyRecorded)
		require.Equal(t, http.StatusConflict, request(router, ticketNo).Code)
		assertWallet(t, userID, "240000.00", 1)
	})
	t.Run("existing idempotency table replays the original withdrawal receipt", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.Equal(t, http.StatusOK, request(router, ticketNo).Code)
		var original billinginfra.WalletTransactionModel
		require.NoError(t, db.Where("user_id = ?", userID).First(&original).Error)
		replayed, err := wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{
			TicketNo: ticketNo, UserID: userID, Amount: "260000", IdempotencyKey: "astw-" + ticketNo,
		})
		require.NoError(t, err)
		require.Equal(t, original.TransactionNo, replayed.Transaction.TransactionNo)
		require.Equal(t, "240000.00", replayed.Wallet.SupplierAvailable)
		assertWallet(t, userID, "240000.00", 1)
	})
	t.Run("audit and detail read failures roll back all financial writes", func(t *testing.T) {
		for _, table := range []string{"operation_logs", "aftersale_ticket_messages"} {
			t.Run(table, func(t *testing.T) {
				userID, ticketNo := fixture(t)
				callback := "test:withdrawal_failure_" + table
				debited := false
				require.NoError(t, db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "wallets" {
						debited = true
					}
				}))
				fail := func(tx *gorm.DB) {
					if debited && tx.Statement.Table == table {
						_ = tx.AddError(errors.New("forced " + table + " failure"))
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
					if table == "operation_logs" {
						fail(tx)
					}
				}))
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, fail))
				w := request(router, ticketNo)
				require.NoError(t, db.Callback().Update().Remove(callback))
				require.NoError(t, db.Callback().Create().Remove(callback))
				require.NoError(t, db.Callback().Query().Remove(callback))
				require.True(t, debited)
				require.Equal(t, http.StatusInternalServerError, w.Code)
				assertWallet(t, userID, "500000.00", 0)
				var receipts int64
				require.NoError(t, db.Model(&billinginfra.IdempotencyKeyModel{}).Where("owner_user_id = ?", userID).Count(&receipts).Error)
				require.Zero(t, receipts)
				ticket, err := repo.Get(ctx, ticketNo, true)
				require.NoError(t, err)
				require.Equal(t, aftersaledomain.TicketStatusOpen, ticket.Status)
				require.Len(t, ticket.Messages, 1)
			})
		}
	})
	t.Run("concurrent wallet transfer cannot spend withdrawn points", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		start, transferDone, withdrawalDone := make(chan struct{}), make(chan error, 1), make(chan int, 1)
		go func() {
			<-start
			_, err := wallets.TransferSupplierBalance(ctx, billingapp.TransferSupplierBalanceRequest{UserID: userID, Amount: "260000", IdempotencyKey: "transfer-" + ticketNo})
			transferDone <- err
		}()
		go func() { <-start; withdrawalDone <- request(router, ticketNo).Code }()
		close(start)
		transferErr, status := <-transferDone, <-withdrawalDone
		wallet, err := wallets.GetWallet(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, "240000.00", wallet.Wallet.SupplierAvailable)
		if status == http.StatusOK {
			require.ErrorIs(t, transferErr, billingdomain.ErrInsufficientBalance)
			require.Equal(t, "123.50", wallet.Wallet.ConsumerBalance)
		} else {
			require.Equal(t, http.StatusUnprocessableEntity, status)
			require.NoError(t, transferErr)
			require.Equal(t, "260123.50", wallet.Wallet.ConsumerBalance)
		}
	})
	t.Run("maximum integer points remain exact in the ledger", func(t *testing.T) {
		userID, _ := fixture(t)
		require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", userID).Update("supplier_available", "999999999999.999999").Error)
		view, err := uc.CreateSupplierWithdrawal(ctx, aftersaleapp.CreateSupplierWithdrawalRequest{RequesterUserID: userID, SupplierAccess: true, Amount: "999999999999", PaymentQRCode: "data:image/png;base64,cXI="})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, request(router, view.Ticket.TicketNo).Code)
		wallet, err := wallets.GetWallet(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, "0.999999", wallet.Wallet.SupplierAvailable)
		var transaction billinginfra.WalletTransactionModel
		require.NoError(t, db.Where("user_id = ?", userID).First(&transaction).Error)
		require.Equal(t, "-999999999999.000000", transaction.Amount)
		require.Equal(t, "999999999999.999999", transaction.BalanceBefore)
		require.Equal(t, "0.999999", transaction.BalanceAfter)
	})
	t.Run("concurrent user close and withdrawal have one terminal outcome", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		start, closeDone, withdrawalDone := make(chan struct{}), make(chan error, 1), make(chan int, 1)
		go func() {
			<-start
			_, err := uc.CloseTicket(ctx, aftersaleapp.CloseTicketRequest{TicketNo: ticketNo, UserID: userID})
			closeDone <- err
		}()
		go func() { <-start; withdrawalDone <- request(router, ticketNo).Code }()
		close(start)
		closeErr, status := <-closeDone, <-withdrawalDone
		ticket, err := repo.Get(ctx, ticketNo, false)
		require.NoError(t, err)
		require.Equal(t, aftersaledomain.TicketStatusClosed, ticket.Status)
		if status == http.StatusOK {
			require.ErrorIs(t, closeErr, aftersaledomain.ErrTicketStateConflict)
			require.Equal(t, aftersaledomain.ResolutionWithdrawn, ticket.Resolution.Kind)
			assertWallet(t, userID, "240000.00", 1)
		} else {
			require.Equal(t, http.StatusConflict, status)
			require.NoError(t, closeErr)
			require.Equal(t, aftersaledomain.ResolutionClosed, ticket.Resolution.Kind)
			assertWallet(t, userID, "500000.00", 0)
		}
	})
	t.Run("independent wallets do not deadlock on absent withdrawal records", func(t *testing.T) {
		firstID, first := fixture(t)
		secondID, second := fixture(t)
		arrived, release, results := make(chan struct{}, 2), make(chan struct{}), make(chan int, 2)
		callback := "test:parallel_absent_withdrawals"
		require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "wallet_transactions" && errors.Is(tx.Error, gorm.ErrRecordNotFound) && strings.Contains(tx.Statement.SQL.String(), "biz_type IN") {
				arrived <- struct{}{}
				<-release
			}
		}))
		for _, ticketNo := range []string{first, second} {
			go func() { results <- request(router, ticketNo).Code }()
		}
		for range 2 {
			select {
			case <-arrived:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("withdrawal did not reach the concurrent read")
			}
		}
		close(release)
		codes := []int{<-results, <-results}
		require.NoError(t, db.Callback().Query().Remove(callback))
		require.Equal(t, []int{http.StatusOK, http.StatusOK}, codes)
		assertWallet(t, firstID, "240000.00", 1)
		assertWallet(t, secondID, "240000.00", 1)
	})
	t.Run("manual and ticket entry points serialize even after simultaneous balance reads", func(t *testing.T) {
		userID, ticketNo := fixture(t)
		require.NoError(t, db.Model(&billinginfra.WalletModel{}).Where("user_id = ?", userID).Update("supplier_available", "1000000").Error)
		arrived, release := make(chan struct{}, 2), make(chan struct{})
		callback := "test:concurrent_withdrawal_wallet_reads"
		require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "wallets" && !strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") {
				arrived <- struct{}{}
				<-release
			}
		}))
		manualDone, ticketDone := make(chan error, 1), make(chan int, 1)
		go func() {
			_, err := wallets.WithdrawSupplier(ctx, billingapp.WithdrawSupplierRequest{UserID: userID, Amount: "260000", Note: ticketNo, IdempotencyKey: "manual-race-" + ticketNo})
			manualDone <- err
		}()
		go func() { ticketDone <- request(router, ticketNo).Code }()
		for range 2 {
			select {
			case <-arrived:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("withdrawal did not reach the initial wallet read")
			}
		}
		close(release)
		manualErr, status := <-manualDone, <-ticketDone
		require.NoError(t, db.Callback().Query().Remove(callback))
		wallet, err := wallets.GetWallet(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, "740000.00", wallet.Wallet.SupplierAvailable)
		var entries int64
		require.NoError(t, db.Model(&billinginfra.WalletTransactionModel{}).Where("user_id = ?", userID).Count(&entries).Error)
		require.EqualValues(t, 1, entries)
		if status == http.StatusOK {
			require.ErrorIs(t, manualErr, billingdomain.ErrWithdrawalAlreadyRecorded)
		} else {
			require.Equal(t, http.StatusConflict, status)
			require.NoError(t, manualErr)
		}
	})
}
