package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

const validTestRechargeNo = "RC00000000000000000000000000000001"

func TestRechargeAmountsAndActiveReconciliation(t *testing.T) {
	config := validRechargeConfig()
	config.FeeRate = "2.5"
	config.FeeCap = "3"
	config.Tiers = []RechargeTier{{Amount: "100", Bonus: "10"}}
	quota, payment, err := rechargeAmounts(config, "100")
	require.NoError(t, err)
	require.Equal(t, "110.00", quota)
	require.Equal(t, "102.50", payment)
	quota, payment, err = rechargeAmounts(config, "200")
	require.NoError(t, err)
	require.Equal(t, "200.00", quota)
	require.Equal(t, "203.00", payment)

	createdAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		now        time.Time
		query      RechargeGatewayQuery
		queryErr   error
		creditErr  error
		wantCredit int
		wantRecord int
		wantFail   string
		wantQuery  int
	}{
		{name: "pending does not credit", now: createdAt.Add(time.Minute), wantRecord: 1, wantQuery: 1},
		{name: "paid credits", now: createdAt.Add(time.Minute), query: RechargeGatewayQuery{Paid: true, GatewayTrade: "GW1"}, wantCredit: 1, wantQuery: 1},
		{name: "mismatch fails", now: createdAt.Add(time.Minute), queryErr: domain.ErrRechargeQueryMismatch, wantFail: "query_mismatch", wantQuery: 1},
		{name: "gateway rejection retries", now: createdAt.Add(time.Minute), queryErr: ErrRechargeGatewayRejected, wantRecord: 1, wantQuery: 1},
		{name: "duplicate gateway trade fails", now: createdAt.Add(time.Minute), query: RechargeGatewayQuery{Paid: true, GatewayTrade: "GW1"}, creditErr: domain.ErrRechargeQueryMismatch, wantCredit: 1, wantFail: "query_mismatch", wantQuery: 1},
		{name: "timeout race is already terminal", now: createdAt.Add(time.Minute), query: RechargeGatewayQuery{Paid: true, GatewayTrade: "GW1"}, creditErr: domain.ErrRechargeExpired, wantCredit: 1, wantQuery: 1},
		{name: "five minute deadline fails without query", now: createdAt.Add(5 * time.Minute), wantFail: "query_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &rechargeRepoStub{creditErr: test.creditErr, recharge: domain.Recharge{
				RechargeNo: "RC1", UserID: 1, PaymentAmount: "100.00", RechargeQuota: "100.00",
				Status: domain.RechargeStatusPaying, GatewayConfigHash: rechargeGatewayConfigHash(config), CreatedAt: createdAt,
			}}
			gateway := &rechargeGatewayStub{query: test.query, err: test.queryErr}
			useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{})
			useCase.now = func() time.Time { return test.now }

			require.NoError(t, useCase.Reconcile(context.Background(), RechargeTask{RechargeNo: "RC1"}))
			require.Equal(t, test.wantCredit, repo.creditCalls)
			require.Equal(t, test.wantRecord, repo.recordCalls)
			require.Equal(t, test.wantFail, repo.failReason)
			require.Equal(t, test.wantQuery, gateway.calls)
		})
	}

	t.Run("query completing after deadline does not credit", func(t *testing.T) {
		repo := &rechargeRepoStub{recharge: domain.Recharge{
			RechargeNo: "RC1", UserID: 1, PaymentAmount: "100.00", RechargeQuota: "100.00",
			Status: domain.RechargeStatusPaying, GatewayConfigHash: rechargeGatewayConfigHash(config), CreatedAt: createdAt,
		}}
		gateway := &rechargeGatewayStub{query: RechargeGatewayQuery{Paid: true, GatewayTrade: "GW1"}}
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{})
		times := []time.Time{createdAt.Add(4*time.Minute + 59*time.Second), createdAt.Add(5*time.Minute + time.Second)}
		useCase.now = func() time.Time {
			now := times[0]
			times = times[1:]
			return now
		}

		require.NoError(t, useCase.Reconcile(context.Background(), RechargeTask{RechargeNo: "RC1"}))
		require.Equal(t, 0, repo.creditCalls)
		require.Equal(t, "query_timeout", repo.failReason)
		require.Equal(t, 1, gateway.calls)
	})
}

func TestRechargeCreateInputAndConfigReplaySafety(t *testing.T) {
	config := validRechargeConfig()

	t.Run("successful create waits for callback or fallback", func(t *testing.T) {
		createdAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
		repo := &rechargeRepoStub{recharge: domain.Recharge{
			RechargeNo: "RC1", UserID: 1, PaymentAmount: "10.00", RechargeQuota: "10.00",
			Status: domain.RechargeStatusPaying, GatewayConfigHash: rechargeGatewayConfigHash(config), CreatedAt: createdAt,
		}}
		queueCalls := 0
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, &rechargeGatewayStub{}, &rechargeQueueStub{calls: &queueCalls})
		useCase.now = func() time.Time { return createdAt }

		result, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Amount: "10", IdempotencyKey: "create"})
		require.NoError(t, err)
		require.Equal(t, "RC1", result.Recharge.RechargeNo)
		require.Zero(t, queueCalls)
	})

	t.Run("rejects oversized idempotency key", func(t *testing.T) {
		useCase := NewRechargeUseCase(&rechargeRepoStub{}, rechargeConfigStub{config}, &rechargeGatewayStub{}, &rechargeQueueStub{})
		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Amount: "10", IdempotencyKey: strings.Repeat("a", 129)})
		require.ErrorIs(t, err, domain.ErrInvalidIdempotencyKey)
	})

	t.Run("does not regenerate payment URL after gateway config changes", func(t *testing.T) {
		repo := &rechargeRepoStub{recharge: domain.Recharge{
			RechargeNo: "RC-OLD", UserID: 1, PaymentAmount: "10.00", RechargeQuota: "10.00",
			Status: domain.RechargeStatusPaying, GatewayConfigHash: "old-config-hash", CreatedAt: time.Now().UTC(),
		}}
		gateway := &rechargeGatewayStub{}
		queueCalls := 0
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{calls: &queueCalls})

		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Amount: "10", IdempotencyKey: "replay"})
		require.ErrorIs(t, err, domain.ErrRechargeExpired)
		require.Zero(t, gateway.paymentCalls)
		require.Zero(t, queueCalls)
	})

	t.Run("does not regenerate an expired idempotent payment URL", func(t *testing.T) {
		createdAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
		repo := &rechargeRepoStub{recharge: domain.Recharge{
			RechargeNo: validTestRechargeNo, UserID: 1, PaymentAmount: "10.00", RechargeQuota: "10.00",
			Status: domain.RechargeStatusPaying, GatewayConfigHash: rechargeGatewayConfigHash(config), CreatedAt: createdAt,
		}}
		gateway := &rechargeGatewayStub{}
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{})
		useCase.now = func() time.Time { return createdAt.Add(domain.RechargeReconciliationWindow) }

		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Amount: "10", IdempotencyKey: "expired-replay"})
		require.ErrorIs(t, err, domain.ErrRechargeExpired)
		require.Zero(t, gateway.paymentCalls)
	})
}

func TestRechargeReconcileUsesClaimedConfigAndSkipsUnclaimedTasks(t *testing.T) {
	createdAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	snapshot := validRechargeConfig()
	snapshot.MerchantKey = "order-secret"
	recharge := domain.Recharge{
		RechargeNo: validTestRechargeNo, UserID: 1, PaymentAmount: "10.00", RechargeQuota: "10.00",
		Status: domain.RechargeStatusCallback, GatewayConfigHash: rechargeGatewayConfigHash(snapshot), CreatedAt: createdAt,
	}

	t.Run("uses immutable order snapshot", func(t *testing.T) {
		current := validRechargeConfig()
		current.MerchantKey = "rotated-secret"
		repo := &rechargeRepoStub{recharge: recharge, claimConfig: snapshot}
		gateway := &rechargeGatewayStub{}
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{current}, gateway, &rechargeQueueStub{})
		useCase.now = func() time.Time { return createdAt.Add(time.Minute) }

		require.NoError(t, useCase.Reconcile(context.Background(), RechargeTask{RechargeNo: validTestRechargeNo}))
		require.Equal(t, "order-secret", gateway.queryConfig.MerchantKey)
		require.Equal(t, 1, repo.recordCalls)
	})

	t.Run("stale task without a claim does nothing", func(t *testing.T) {
		repo := &rechargeRepoStub{recharge: recharge, claimDisabled: true}
		gateway := &rechargeGatewayStub{}
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{snapshot}, gateway, &rechargeQueueStub{})
		useCase.now = func() time.Time { return createdAt.Add(time.Minute) }

		require.NoError(t, useCase.Reconcile(context.Background(), RechargeTask{RechargeNo: validTestRechargeNo}))
		require.Zero(t, gateway.calls)
		require.Zero(t, repo.recordCalls)
	})
}

func TestRechargeCallbackAndDispatchScheduling(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	t.Run("first callback enqueues once", func(t *testing.T) {
		repo := &rechargeRepoStub{markCallback: true}
		queueCalls := 0
		useCase := NewRechargeUseCase(repo, nil, nil, &rechargeQueueStub{calls: &queueCalls})
		useCase.now = func() time.Time { return now }

		require.NoError(t, useCase.NotifyCallback(context.Background(), " "+validTestRechargeNo+" "))
		require.Equal(t, validTestRechargeNo, repo.markedRechargeNo)
		require.Equal(t, now, repo.markedAt)
		require.Equal(t, 1, queueCalls)

		repo.markCallback = false
		require.NoError(t, useCase.NotifyCallback(context.Background(), validTestRechargeNo))
		require.Equal(t, 1, queueCalls)
	})

	t.Run("dispatcher retries a persisted callback after enqueue failure", func(t *testing.T) {
		repo := &rechargeRepoStub{
			markCallback: true,
			due:          []domain.Recharge{{RechargeNo: validTestRechargeNo, QueryAttempts: 3}},
		}
		queueCalls := 0
		queue := &rechargeQueueStub{calls: &queueCalls, err: errors.New("redis unavailable")}
		useCase := NewRechargeUseCase(repo, nil, nil, queue)
		useCase.now = func() time.Time { return now }

		require.NoError(t, useCase.NotifyCallback(context.Background(), validTestRechargeNo))
		queue.err = nil
		require.NoError(t, useCase.Dispatch(context.Background()))
		require.Equal(t, 2, queueCalls)
		require.Equal(t, now, repo.listedAt)
		require.Equal(t, now.Add(-domain.RechargeReconciliationWindow), repo.expiredBefore)
	})

	t.Run("invalid callback is an opaque no-op", func(t *testing.T) {
		repo := &rechargeRepoStub{markCallback: true}
		useCase := NewRechargeUseCase(repo, nil, nil, &rechargeQueueStub{})
		require.NoError(t, useCase.NotifyCallback(context.Background(), strings.Repeat("x", 65)))
		require.Empty(t, repo.markedRechargeNo)
	})
}

func validRechargeConfig() RechargeConfig {
	return RechargeConfig{
		Enabled: true, Version: "v1", GatewayURL: "https://pay.example.com",
		MerchantID: "1000", MerchantKey: "secret",
		NotifyURL: "https://app.example.com/v1/payments/webhooks/epay/v1",
		ReturnURL: "https://app.example.com/wallet",
		MinAmount: "10", FeeRate: "0", FeeCap: "0",
		RequestTimeout: 5 * time.Second,
	}
}

type rechargeConfigStub struct{ config RechargeConfig }

func (stub rechargeConfigStub) Current() (RechargeConfig, error) { return stub.config, nil }

type rechargeGatewayStub struct {
	query        RechargeGatewayQuery
	err          error
	calls        int
	paymentCalls int
	queryConfig  RechargeConfig
}

func (stub *rechargeGatewayStub) PaymentURL(RechargeConfig, domain.Recharge) (string, error) {
	stub.paymentCalls++
	return "https://pay.example.com/submit.php", nil
}

func (stub *rechargeGatewayStub) Query(_ context.Context, config RechargeConfig, _ domain.Recharge) (RechargeGatewayQuery, error) {
	stub.calls++
	stub.queryConfig = config
	return stub.query, stub.err
}

type rechargeQueueStub struct {
	calls *int
	err   error
}

func (stub *rechargeQueueStub) Enqueue(context.Context, RechargeTask) error {
	if stub.calls != nil {
		*stub.calls++
	}
	return stub.err
}

type rechargeRepoStub struct {
	recharge         domain.Recharge
	claimConfig      RechargeConfig
	claimDisabled    bool
	creditErr        error
	creditCalls      int
	recordCalls      int
	failReason       string
	markCallback     bool
	markErr          error
	markedRechargeNo string
	markedAt         time.Time
	due              []domain.Recharge
	listedAt         time.Time
	expiredBefore    time.Time
}

func (stub *rechargeRepoStub) CreateRecharge(context.Context, CreateRechargeCommand) (*domain.Recharge, error) {
	return &stub.recharge, nil
}

func (stub *rechargeRepoStub) GetRechargeByNo(context.Context, string) (*domain.Recharge, error) {
	recharge := stub.recharge
	return &recharge, nil
}

func (stub *rechargeRepoStub) MarkRechargeCallback(_ context.Context, rechargeNo string, callbackAt time.Time) (bool, error) {
	stub.markedRechargeNo = rechargeNo
	stub.markedAt = callbackAt
	return stub.markCallback, stub.markErr
}

func (stub *rechargeRepoStub) ListDueRecharges(_ context.Context, now time.Time, _ int) ([]domain.Recharge, error) {
	stub.listedAt = now
	return stub.due, nil
}

func (stub *rechargeRepoStub) ExpirePendingRecharges(_ context.Context, createdBefore, _ time.Time) (int64, error) {
	stub.expiredBefore = createdBefore
	return 0, nil
}

func (stub *rechargeRepoStub) ClaimRechargeQuery(context.Context, string, time.Time, time.Time) (*domain.Recharge, RechargeConfig, int, bool, error) {
	if stub.claimDisabled {
		return nil, RechargeConfig{}, 0, false, nil
	}
	config := stub.claimConfig
	if config.Version == "" {
		config = validRechargeConfig()
	}
	recharge := stub.recharge
	return &recharge, config, 1, true, nil
}

func (stub *rechargeRepoStub) RecordRechargeQuery(context.Context, string, int, time.Time) error {
	stub.recordCalls++
	return nil
}

func (stub *rechargeRepoStub) FailRecharge(_ context.Context, _ string, _ int, reason string, _ time.Time) error {
	stub.failReason = reason
	return nil
}

func (stub *rechargeRepoStub) CreditRecharge(_ context.Context, command CreditRechargeCommand) (*domain.Recharge, error) {
	if command.GatewayTradeNo == "" {
		return nil, errors.New("missing gateway trade")
	}
	stub.creditCalls++
	if stub.creditErr != nil {
		return nil, stub.creditErr
	}
	stub.recharge.Status = domain.RechargeStatusCredited
	return &stub.recharge, nil
}
