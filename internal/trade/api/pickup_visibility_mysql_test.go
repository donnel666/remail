package api

import (
	"context"
	"strings"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestPickupRejectsLateDeliveryAfterAutomaticRefundMySQL(t *testing.T) {
	db := newTradeMySQLTestDB(t)
	seedTradeBase(t, db, "microsoft")
	seedTradeMicrosoftResources(t, db, 1, 1000, 1, true)
	creditBuyer(t, db, 2, "10.00")
	trade := newTradeUseCase(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := trade.Checkout(ctx, tradeapp.CheckoutRequest{
		UserID: 2, ProjectID: 10, ProductID: 20, ServiceMode: "code",
		SupplyPolicy: "public_only", ClientChannel: tradedomain.ClientChannelConsole,
		IdempotencyKey: "pickup-late-delivery", RequestID: "pickup-late-delivery",
	})
	require.NoError(t, err)
	repo := mailmatchinfra.NewRepo(db, nil)
	message := mailmatchdomain.Message{
		EmailResourceID: 1000, ResourceType: mailmatchdomain.ResourceTypeMicrosoft,
		MatchedOrderID: &result.Order.ID, Recipient: result.Order.DeliveryEmail,
		Sender: "sender@example.net", Subject: "Private code",
		RawBody: "Your code is 123456", BodyPreview: "Your code is 123456",
		VerificationCode: "123456", DedupeKey: strings.Repeat("e", 64),
		Status: mailmatchdomain.MessageStatusMatched, ReceivedAt: time.Now().UTC(),
	}
	// Matching reads active scopes before its projection/delivery transaction.
	scopes, err := repo.ListMatchingScopesByRecipient(ctx, message.ResourceType, 1000, message.Recipient, message.ReceivedAt)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	require.Equal(t, "active", scopes[0].OrderStatus)
	facts, _, err := repo.AppendMessages(ctx, []mailmatchdomain.Message{message})
	require.NoError(t, err)
	require.Len(t, facts, 1)
	message.ID = facts[0].ID

	loaded := make(chan struct{}, 2)
	resume := make(chan struct{})
	pickup := mailmatchapp.NewUseCase(&delayedPickupScopeRepo{Repo: repo, loaded: loaded, resume: resume}, nil, nil, nil)
	type readResult struct {
		kind  string
		items []mailmatchdomain.MailContent
		err   error
	}
	reads := make(chan readResult, 2)
	for _, kind := range []string{"list", "detail"} {
		go func() {
			out := readResult{kind: kind}
			if kind == "detail" {
				var content *mailmatchdomain.MailContent
				content, out.err = pickup.GetPickupMessage(ctx, result.ServiceToken, result.Order.DeliveryEmail, message.ID)
				if content != nil {
					out.items = []mailmatchdomain.MailContent{*content}
				}
			} else {
				out.items, _, out.err = pickup.ListPickupMail(ctx, result.ServiceToken, result.Order.DeliveryEmail)
			}
			reads <- out
		}()
	}
	for range 2 {
		select {
		case <-loaded:
		case <-ctx.Done():
			t.Fatal("pickup did not load its order scope")
		}
	}
	require.NoError(t, db.WithContext(ctx).Table("microsoft_resources").Where("id = ?", 1000).Update("status", "abnormal").Error)
	refundCount, err := trade.RefundUnavailableMicrosoftOrders(ctx, 1000, "pickup-late-delivery")
	require.NoError(t, err)
	require.Equal(t, 1, refundCount)

	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error {
		if _, _, err := repo.InsertMessageProjections(txCtx, []mailmatchdomain.Message{message}); err != nil {
			return err
		}
		return repo.CreateOrderDelivery(txCtx, result.Order.ID, message)
	}))
	require.NoError(t, trade.NotifyMatchedCode(ctx, tradeapp.MatchCodeResultRequest{
		OrderNo: result.Order.OrderNo, MatchedAt: message.ReceivedAt,
	}))
	var status string
	require.NoError(t, db.WithContext(ctx).Table("orders").Select("status").Where("id = ?", result.Order.ID).Scan(&status).Error)
	require.Equal(t, "refunded", status)

	close(resume)
	for range 2 {
		out := <-reads
		require.Empty(t, out.items, out.kind)
		if out.kind == "detail" {
			require.ErrorIs(t, out.err, mailmatchdomain.ErrMessageNotFound)
		} else {
			require.ErrorIs(t, out.err, mailmatchdomain.ErrOrderUnavailable)
		}
	}
}

type delayedPickupScopeRepo struct {
	*mailmatchinfra.Repo
	loaded chan<- struct{}
	resume <-chan struct{}
}

func (r *delayedPickupScopeRepo) LoadPickupScope(ctx context.Context, token, email string) (*mailmatchapp.OrderScope, error) {
	scope, err := r.Repo.LoadPickupScope(ctx, token, email)
	r.loaded <- struct{}{}
	select {
	case <-r.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return scope, err
}
