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
	config.FeeCapPoints = "3"
	config.Tiers = []RechargeTier{{Points: "100", BonusPoints: "10"}}
	quote, payment, err := rechargeAmounts(config, "100")
	require.NoError(t, err)
	require.Equal(t, &RechargeQuoteResult{Points: "100.00", BonusPoints: "10.00", FeePoints: "2.50", CreditedPoints: "110.00", PaymentAmount: "0.11", PaymentCurrency: "CNY"}, quote)
	require.Equal(t, "0.11", payment)
	quote, payment, err = rechargeAmounts(config, "200")
	require.NoError(t, err)
	require.Equal(t, &RechargeQuoteResult{Points: "200.00", BonusPoints: "0.00", FeePoints: "3.00", CreditedPoints: "200.00", PaymentAmount: "0.21", PaymentCurrency: "CNY"}, quote)
	require.Equal(t, "0.21", payment)
	config.FeeRate = "0.6"
	config.FeeCapPoints = "0"
	config.MinPoints = "0.01"
	_, _, err = rechargeAmounts(config, "0.01")
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
	quote, payment, err = rechargeAmounts(config, "1.00")
	require.NoError(t, err)
	require.Equal(t, &RechargeQuoteResult{Points: "1.00", BonusPoints: "0.00", FeePoints: "0.006", CreditedPoints: "1.00", PaymentAmount: "0.01", PaymentCurrency: "CNY"}, quote)
	require.Equal(t, "0.01", payment)
	config.FeeCapPoints = "0.0000001"
	_, _, err = rechargeAmounts(config, "1")
	require.ErrorIs(t, err, domain.ErrRechargeConfigUnavailable)
	config.FeeCapPoints = "0"

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
		{name: "provider auth rotates and retries", now: createdAt.Add(time.Minute), queryErr: domain.ErrRechargeGatewayAuthUnavailable, wantRecord: 1, wantQuery: 1},
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

func TestRechargeAmountsEpusdtUsesDedicatedRateWithoutFee(t *testing.T) {
	config := validRechargeConfig()
	config.PaymentMethod = domain.RechargePaymentMethodEpusdtUSDTTron
	config.EpusdtPointsPerUSDT = "333"
	config.FeeRate = "10"
	config.FeeCapPoints = "1"
	config.Tiers = []RechargeTier{{Points: "1000", BonusPoints: "50"}}

	quote, payment, err := rechargeAmounts(config, "1000")
	require.NoError(t, err)
	require.Equal(t, "0.00", quote.FeePoints)
	require.Equal(t, "1050.00", quote.CreditedPoints)
	require.Equal(t, "3.01", quote.PaymentAmount)
	require.Equal(t, "USDT", quote.PaymentCurrency)
	require.Equal(t, "3.01", payment)

	config.EpusdtPointsPerUSDT = "1000"
	quote, payment, err = rechargeAmounts(config, "1000")
	require.NoError(t, err)
	require.Equal(t, "0.00", quote.FeePoints)
	require.Equal(t, "1.00", payment)
}

func TestRechargeAmountsEpusdtRejectsInvalidRateAndProviderMinimum(t *testing.T) {
	config := validRechargeConfig()
	config.PaymentMethod = domain.RechargePaymentMethodEpusdtUSDTTron
	config.EpusdtPointsPerUSDT = "0"
	_, _, err := rechargeAmounts(config, "1000")
	require.ErrorIs(t, err, domain.ErrRechargeConfigUnavailable)

	config.EpusdtPointsPerUSDT = "100000"
	_, _, err = rechargeAmounts(config, "1000")
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
}

func TestRechargeQuoteRejectsUnavailablePaymentMethods(t *testing.T) {
	config := validRechargeConfig()
	config.Enabled = false
	useCase := NewRechargeUseCase(nil, rechargeConfigStub{config}, nil, nil)

	_, err := useCase.Quote("100")
	require.ErrorIs(t, err, domain.ErrRechargeConfigUnavailable)

	config.Enabled = true
	config.PaymentMethod = domain.RechargePaymentMethodEpusdtUSDTTron
	config.EpusdtEnabled = true
	config.EpusdtPointsPerUSDT = "500"
	useCase = NewRechargeUseCase(nil, rechargeConfigStub{config}, nil, nil)
	_, err = useCase.Quote("100")
	require.ErrorIs(t, err, domain.ErrRechargeConfigUnavailable)

	config.EpusdtCurrency = "USDT"
	config.EpusdtGatewayURL = "https://epusdt.example.com"
	config.EpusdtPID = "1000"
	config.EpusdtAPISecret = "secret"
	config.EpusdtToken = "USDT"
	config.EpusdtNetwork = "tron"
	config.EpusdtNotifyURL = "https://app.example.com/notify"
	config.EpusdtReturnURL = "https://app.example.com/return"
	config.FeeRate = "not-an-epay-rate"
	config.FeeCapPoints = "not-an-epay-cap"
	useCase = NewRechargeUseCase(nil, rechargeConfigStub{config}, nil, nil)
	quote, err := useCase.Quote("100", domain.RechargePaymentMethodEpusdtUSDTTron)
	require.NoError(t, err)
	require.Equal(t, "USDT", quote.PaymentCurrency)
}

func TestRechargeConfigReturnsDisabledWhenNoGatewayIsAvailable(t *testing.T) {
	config := validRechargeConfig()
	config.Enabled = false
	config.Tiers = []RechargeTier{{Points: "100", BonusPoints: "10"}}
	useCase := NewRechargeUseCase(nil, rechargeConfigStub{config}, nil, nil)

	result, err := useCase.Config()
	require.NoError(t, err)
	require.False(t, result.Enabled)
	require.Empty(t, result.PaymentMethods)
}

func TestRechargeConfigSkipsLegacyFractionalTiers(t *testing.T) {
	config := validRechargeConfig()
	config.Tiers = []RechargeTier{
		{Points: "10", BonusPoints: "1"},
		{Points: "20.5", BonusPoints: "2"},
		{Points: "30", BonusPoints: "3"},
	}
	useCase := NewRechargeUseCase(nil, rechargeConfigStub{config}, nil, nil)

	result, err := useCase.Config()
	require.NoError(t, err)
	require.Len(t, result.Tiers, 2)
	require.Equal(t, "10.00", result.Tiers[0].Points)
	require.Equal(t, "30.00", result.Tiers[1].Points)

	quote, err := useCase.Quote("20")
	require.NoError(t, err)
	require.Equal(t, "20.00", quote.Points)
	require.Equal(t, "0.00", quote.BonusPoints)
}

func TestRechargeCreateInputAndConfigReplaySafety(t *testing.T) {
	config := validRechargeConfig()

	t.Run("successful create waits for callback or fallback", func(t *testing.T) {
		createdAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
		startedAt := time.Now()
		repo := &rechargeRepoStub{recharge: domain.Recharge{
			RechargeNo: "RC1", UserID: 1, PaymentAmount: "10.00", RechargeQuota: "10.00",
			Status: domain.RechargeStatusPaying, GatewayConfigHash: rechargeGatewayConfigHash(config), CreatedAt: createdAt,
		}}
		queueCalls := 0
		gateway := &rechargeGatewayStub{}
		useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{calls: &queueCalls})
		useCase.now = func() time.Time { return createdAt }

		result, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Points: "10", IdempotencyKey: "create"})
		require.NoError(t, err)
		require.Equal(t, "RC1", result.Recharge.RechargeNo)
		require.WithinDuration(t, startedAt.Add(rechargePaymentCreateTimeout), gateway.paymentDeadline, time.Second)
		require.Zero(t, queueCalls)
	})

	t.Run("rejects oversized idempotency key", func(t *testing.T) {
		useCase := NewRechargeUseCase(&rechargeRepoStub{}, rechargeConfigStub{config}, &rechargeGatewayStub{}, &rechargeQueueStub{})
		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Points: "10", IdempotencyKey: strings.Repeat("a", 129)})
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

		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Points: "10", IdempotencyKey: "replay"})
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

		_, err := useCase.Create(context.Background(), CreateRechargeRequest{UserID: 1, Points: "10", IdempotencyKey: "expired-replay"})
		require.ErrorIs(t, err, domain.ErrRechargeExpired)
		require.Zero(t, gateway.paymentCalls)
	})
}

func TestRechargeCreateSelectsEpusdtWithoutTrustingCreateResponse(t *testing.T) {
	config := validRechargeConfig()
	config.Enabled = false
	config.EpusdtEnabled = true
	config.EpusdtGatewayURL = "https://epusdt.example.com"
	config.EpusdtPID = "1000"
	config.EpusdtAPISecret = "secret"
	config.EpusdtToken = "USDT"
	config.EpusdtNetwork = "tron"
	config.EpusdtCurrency = "USDT"
	config.EpusdtPointsPerUSDT = "500"
	config.EpusdtNotifyURL = "https://app.example.com/v1/payments/webhooks/epusdt/v1"
	config.EpusdtReturnURL = "https://app.example.com/payment/return"
	config.PointsPerYuan = "500"
	config.PaymentMethod = domain.RechargePaymentMethodEpusdtUSDTTron
	config.Provider = "epusdt"
	created := domain.Recharge{
		RechargeNo: "RC00000000000000000000000000000001", UserID: 1,
		PaymentAmount: "0.02", RechargeQuota: "10.00", Status: domain.RechargeStatusPaying,
		PaymentMethod: domain.RechargePaymentMethodEpusdtUSDTTron,
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	created.GatewayConfigHash = rechargeGatewayConfigHash(config)
	repo := &rechargeRepoStub{recharge: created}
	gateway := &rechargeGatewayStub{}
	useCase := NewRechargeUseCase(repo, rechargeConfigStub{config}, gateway, &rechargeQueueStub{})
	useCase.now = func() time.Time { return created.CreatedAt }

	result, err := useCase.Create(context.Background(), CreateRechargeRequest{
		UserID: 1, Points: "10", PaymentMethod: domain.RechargePaymentMethodEpusdtUSDTTron, IdempotencyKey: "epusdt-create",
	})
	require.NoError(t, err)
	require.Equal(t, domain.RechargePaymentMethodEpusdtUSDTTron, result.Recharge.PaymentMethod)
	require.Equal(t, domain.RechargePaymentMethodEpusdtUSDTTron, gateway.queryConfig.PaymentMethod)
	require.Equal(t, "epusdt", gateway.queryConfig.Provider)
}

func TestValidateEpusdtRejectsGatewayURLQueryOrFragment(t *testing.T) {
	config := validRechargeConfig()
	config.Enabled = false
	config.EpusdtEnabled = true
	config.PaymentMethod = domain.RechargePaymentMethodEpusdtUSDTTron
	config.EpusdtGatewayURL = "https://epusdt.example.com/?tenant=wallet"
	config.EpusdtPID = "1000"
	config.EpusdtAPISecret = "secret"
	config.EpusdtToken = "USDT"
	config.EpusdtNetwork = "tron"
	config.EpusdtNotifyURL = "https://app.example.com/v1/payments/webhooks/epusdt/v1"
	config.EpusdtReturnURL = "https://app.example.com/payment/return"

	require.ErrorIs(t, validateRechargeGatewayConfig(config), domain.ErrRechargeConfigUnavailable)

	config.EpusdtGatewayURL = "https://epusdt.example.com"
	config.EpusdtCurrency = "EUR"
	require.ErrorIs(t, validateRechargeGatewayConfig(config), domain.ErrRechargeConfigUnavailable)
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
		NotifyURL:     "https://app.example.com/v1/payments/webhooks/epay/v1",
		ReturnURL:     "https://app.example.com/wallet",
		PointsPerYuan: "1000", MinPoints: "10", FeeRate: "0", FeeCapPoints: "0",
		MaxPendingOrders: 10,
		RequestTimeout:   5 * time.Second,
	}
}

type rechargeConfigStub struct{ config RechargeConfig }

func (stub rechargeConfigStub) Current() (RechargeConfig, error) { return stub.config, nil }

type rechargeGatewayStub struct {
	query           RechargeGatewayQuery
	err             error
	calls           int
	paymentCalls    int
	paymentDeadline time.Time
	queryConfig     RechargeConfig
}

func (stub *rechargeGatewayStub) PaymentURL(ctx context.Context, config RechargeConfig, _ domain.Recharge, _ string) (string, error) {
	stub.paymentCalls++
	stub.queryConfig = config
	stub.paymentDeadline, _ = ctx.Deadline()
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
