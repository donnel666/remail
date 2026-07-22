package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type matchingRepoStub struct {
	Repository
	scopes           []OrderScope
	purchaseDelivery *domain.Message
}

func (r *matchingRepoStub) ListMatchingScopesByRecipient(context.Context, domain.ResourceType, uint, string, time.Time) ([]OrderScope, error) {
	return r.scopes, nil
}

func (r *matchingRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *matchingRepoStub) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	for i := range messages {
		messages[i].ID = uint(i + 1)
	}
	return messages, nil
}

func (r *matchingRepoStub) AdvancePurchaseOrderDelivery(_ context.Context, _ uint, message domain.Message) error {
	r.purchaseDelivery = &message
	return nil
}

type matchResultStub struct {
	results []MatchResult
}

type pickupBatchRepoStub struct {
	Repository
	scopes          map[string]OrderScope
	messages        map[uint][]domain.Message
	state           *domain.FetchState
	stateReads      int
	fetchRequestErr error
	fetchRequests   int
}

func (r *pickupBatchRepoStub) LoadPickupScope(_ context.Context, token string, email string) (*OrderScope, error) {
	scope, ok := r.scopes[token+"|"+email]
	if !ok {
		return nil, domain.ErrPickupCredentialInvalid
	}
	return &scope, nil
}

func (r *pickupBatchRepoStub) FindOrderDelivery(context.Context, uint) (*OrderDelivery, error) {
	return nil, nil
}

func (r *pickupBatchRepoStub) FindFetchStateForUpdate(context.Context, uint) (*domain.FetchState, error) {
	r.stateReads++
	return r.state, nil
}

func (r *pickupBatchRepoStub) LoadOrderScopeForServiceToken(_ context.Context, orderNo string) (*OrderScope, error) {
	for _, scope := range r.scopes {
		if scope.OrderNo == orderNo {
			return &scope, nil
		}
	}
	return nil, domain.ErrOrderNotFound
}

func (r *pickupBatchRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *pickupBatchRepoStub) EnsureFetchStates(context.Context, []uint) error {
	return nil
}

func (r *pickupBatchRepoStub) RequestFetch(context.Context, *domain.FetchJob, time.Time, time.Time) error {
	r.fetchRequests++
	return r.fetchRequestErr
}

func (r *pickupBatchRepoStub) ListOrderMessages(_ context.Context, scope OrderScope, _ int) ([]domain.Message, error) {
	return r.messages[scope.OrderID], nil
}

func (s *matchResultStub) NotifyMatchedCode(_ context.Context, result MatchResult) error {
	s.results = append(s.results, result)
	return nil
}

func TestListPickupMailBatchPreservesRequestOrderAndContinuesAfterFailure(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	cooldown := now.Add(time.Minute)
	repo := &pickupBatchRepoStub{
		scopes: map[string]OrderScope{
			"token-a|a@example.com": {OrderID: 1, OrderNo: "ORDER-A", EmailResourceID: 11, Recipient: "a@example.com", ServiceMode: "purchase", OrderStatus: "active"},
			"token-b|b@example.com": {OrderID: 2, OrderNo: "ORDER-B", EmailResourceID: 12, Recipient: "b@example.com", ServiceMode: "purchase", OrderStatus: "active"},
		},
		messages: map[uint][]domain.Message{
			1: {{ID: 101, Recipient: "a@example.com", ReceivedAt: now}},
			2: {{ID: 202, Recipient: "b@example.com", ReceivedAt: now}},
		},
		state: &domain.FetchState{CooldownUntil: &cooldown},
	}
	uc := NewUseCase(repo, nil, nil, nil)
	uc.now = func() time.Time { return now }

	results := uc.ListPickupMailBatch(context.Background(), []PickupCredential{
		{Email: "b@example.com", Token: "token-b"},
		{Email: "missing@example.com", Token: "missing-token"},
		{Email: "a@example.com", Token: "token-a"},
	})

	require.Len(t, results, 3)
	require.NoError(t, results[0].Err)
	require.Equal(t, uint(202), results[0].Items[0].ID)
	require.ErrorIs(t, results[1].Err, domain.ErrPickupCredentialInvalid)
	require.Empty(t, results[1].Items)
	require.NoError(t, results[2].Err)
	require.Equal(t, uint(101), results[2].Items[0].ID)
}

func TestListPickupMailReturnsEmptyWhenFetchSchedulingFails(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	repo := &pickupBatchRepoStub{
		scopes: map[string]OrderScope{
			"token-a|a@example.com": {
				OrderID: 1, OrderNo: "ORDER-A", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
				AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1, EmailResourceID: 1, Recipient: "a@example.com",
			},
		},
	}
	queue := &pickupBatchQueueStub{fetchErr: domain.ErrFetchQueueUnavailable}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	items, _, err := uc.ListPickupMail(context.Background(), "token-a", "a@example.com")

	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, repo.stateReads)
	require.Eventually(t, func() bool {
		acquired, released := state.snapshot()
		return len(acquired) == 1 && acquired[0] == 1 && released == 1
	}, time.Second, time.Millisecond)
}

type pickupBatchReaderStub struct {
	Repository
	reads         []PickupBatchRead
	calls         int
	txCalls       int
	ensuredIDs    []uint
	lockCalls     int
	requestCalls  int
	blockRequest  bool
	requestedJobs []*domain.FetchJob
}

func (r *pickupBatchReaderStub) ReadPickupBatch(context.Context, []PickupCredential, time.Time, int) ([]PickupBatchRead, error) {
	r.calls++
	return r.reads, nil
}

func (r *pickupBatchReaderStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	r.txCalls++
	return fn(ctx)
}

func (r *pickupBatchReaderStub) EnsureFetchStates(_ context.Context, ids []uint) error {
	r.ensuredIDs = append([]uint(nil), ids...)
	return nil
}

func (r *pickupBatchReaderStub) FindFetchStatesForUpdate(_ context.Context, ids []uint) (map[uint]*domain.FetchState, error) {
	r.lockCalls++
	return make(map[uint]*domain.FetchState, len(ids)), nil
}

func (r *pickupBatchReaderStub) RequestFetchBatch(ctx context.Context, jobs []*domain.FetchJob, _ time.Time, _ time.Time) error {
	r.requestCalls++
	if r.blockRequest {
		<-ctx.Done()
		return ctx.Err()
	}
	r.requestedJobs = append([]*domain.FetchJob(nil), jobs...)
	return nil
}

type pickupBatchQueueStub struct {
	mu       sync.Mutex
	fetches  []FetchTask
	fetchErr error
}

func (q *pickupBatchQueueStub) EnqueueFetch(_ context.Context, task FetchTask) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fetches = append(q.fetches, task)
	return q.fetchErr == nil, q.fetchErr
}

func (q *pickupBatchQueueStub) snapshot() []FetchTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]FetchTask(nil), q.fetches...)
}

type pickupFetchStateStub struct {
	mu         sync.Mutex
	acquired   []uint
	released   int
	releaseErr error
	block      <-chan struct{}
	started    chan<- struct{}
}

func (s *pickupFetchStateStub) Acquire(ctx context.Context, emailResourceID uint, _ string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	s.acquired = append(s.acquired, emailResourceID)
	block := s.block
	started := s.started
	s.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

func (*pickupFetchStateStub) Owns(context.Context, uint, string) (bool, error) { return true, nil }
func (*pickupFetchStateStub) Extend(context.Context, uint, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *pickupFetchStateStub) Release(context.Context, uint, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released++
	return s.releaseErr
}

func (s *pickupFetchStateStub) snapshot() ([]uint, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint(nil), s.acquired...), s.released
}

func TestListPickupMailBatchUsesBulkReader(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	repo := &pickupBatchReaderStub{reads: []PickupBatchRead{
		{
			Scope: &OrderScope{
				OrderID: 1, OrderNo: "ORDER-A", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
				AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 21, EmailResourceID: 11, Recipient: "a@example.com",
				CredentialRevision: 31,
			},
			Fetch:    &domain.FetchState{OperationKind: "resource_history", LastStatus: string(domain.FetchJobPending)},
			Messages: []domain.Message{{ID: 101, Recipient: "a@example.com", ReceivedAt: now}},
		},
		{
			Scope: &OrderScope{
				OrderID: 2, OrderNo: "ORDER-B", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
				AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 22, EmailResourceID: 12, Recipient: "b@example.com",
				CredentialRevision: 32,
			},
			Messages: []domain.Message{{ID: 202, Recipient: "b@example.com", ReceivedAt: now}},
		},
		{
			Scope: &OrderScope{
				OrderID: 3, OrderNo: "ORDER-C", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
				AllocationType: domain.ResourceTypeDomain, AllocationID: 23, EmailResourceID: 13, Recipient: "c@example.com",
			},
			Messages: []domain.Message{{ID: 303, Recipient: "c@example.com", ReceivedAt: now}},
		},
		{Err: domain.ErrPickupCredentialInvalid},
	}}
	queue := &pickupBatchQueueStub{}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	results := uc.ListPickupMailBatch(context.Background(), []PickupCredential{
		{Email: "a@example.com", Token: "token-a"},
		{Email: "b@example.com", Token: "token-b"},
		{Email: "c@example.com", Token: "token-c"},
		{Email: "missing@example.com", Token: "missing-token"},
	})

	require.Equal(t, 1, repo.calls)
	require.Zero(t, repo.txCalls)
	require.Zero(t, repo.lockCalls)
	require.Zero(t, repo.requestCalls)
	require.Eventually(t, func() bool {
		acquired, _ := state.snapshot()
		fetches := queue.snapshot()
		return len(acquired) == 2 && len(fetches) == 2
	}, time.Second, time.Millisecond)
	acquired, _ := state.snapshot()
	fetches := queue.snapshot()
	require.Equal(t, []uint{11, 12}, acquired)
	require.Equal(t, "ORDER-A", fetches[0].OrderNo)
	require.Equal(t, "ORDER-B", fetches[1].OrderNo)
	require.NoError(t, results[0].Err)
	require.Equal(t, uint(101), results[0].Items[0].ID)
	require.NoError(t, results[1].Err)
	require.Equal(t, uint(202), results[1].Items[0].ID)
	require.NoError(t, results[2].Err)
	require.Equal(t, uint(303), results[2].Items[0].ID)
	require.ErrorIs(t, results[3].Err, domain.ErrPickupCredentialInvalid)
}

func TestListPickupMailBatchHundredItemsUsesOneBulkReadAndWriteUnderBudget(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	reads := make([]PickupBatchRead, 100)
	credentials := make([]PickupCredential, len(reads))
	for i := range reads {
		resourceID := uint(i + 1)
		reads[i].Scope = &OrderScope{
			OrderID: resourceID, OrderNo: fmt.Sprintf("ORDER-%d", resourceID), ProjectID: 1,
			ServiceMode: "purchase", OrderStatus: "active", AllocationType: domain.ResourceTypeMicrosoft,
			AllocationID: resourceID, EmailResourceID: resourceID, Recipient: fmt.Sprintf("user-%d@example.com", i),
		}
		credentials[i] = PickupCredential{Email: reads[i].Scope.Recipient, Token: fmt.Sprintf("token-%d", i)}
	}
	repo := &pickupBatchReaderStub{reads: reads}
	queue := &pickupBatchQueueStub{}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	started := time.Now()
	results := uc.ListPickupMailBatch(context.Background(), credentials)

	require.Less(t, time.Since(started), 10*time.Second)
	require.Len(t, results, 100)
	require.Equal(t, 1, repo.calls)
	require.Zero(t, repo.requestCalls)
	require.Eventually(t, func() bool {
		acquired, _ := state.snapshot()
		fetches := queue.snapshot()
		return len(acquired) == 100 && len(fetches) == 100
	}, time.Second, time.Millisecond)
}

func TestListPickupMailBatchDoesNotWaitForFetchScheduling(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	repo := &pickupBatchReaderStub{
		reads: []PickupBatchRead{{Scope: &OrderScope{
			OrderID: 1, OrderNo: "ORDER-A", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1, EmailResourceID: 1, Recipient: "a@example.com",
		}}},
	}
	block := make(chan struct{})
	startedScheduling := make(chan struct{}, 1)
	state := &pickupFetchStateStub{block: block, started: startedScheduling}
	uc := NewUseCase(repo, &pickupBatchQueueStub{}, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	started := time.Now()
	results := uc.ListPickupMailBatch(context.Background(), []PickupCredential{{Email: "a@example.com", Token: "token-a"}})

	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Empty(t, results[0].Items)
	select {
	case <-startedScheduling:
	case <-time.After(time.Second):
		t.Fatal("pickup scheduling did not start")
	}
	close(block)
}

func TestMatchAndExtractAnyRecipientUsesAliasCandidate(t *testing.T) {
	scope := OrderScope{
		Recipient:     "alias+login@example.com",
		RecipientKind: "plus",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "plus", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}
	message := FetchedMessage{
		Recipient:  "main@example.com",
		Recipients: []string{"main@example.com", "alias+login@example.com"},
		Sender:     "sender@example.net",
		Body:       "Your code is 123456.",
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "123456", code)
	require.Empty(t, diagnostic)
}

func TestRecipientRulesNormalizePlusAndDotAliases(t *testing.T) {
	tests := []struct {
		name      string
		recipient string
		target    string
		patterns  []string
		wantMatch bool
	}{
		{name: "plus", recipient: "firstname+tag@example.com", target: "firstname@example.com", patterns: []string{"plus"}, wantMatch: true},
		{name: "dot", recipient: "first.name@example.com", target: "firstname@example.com", patterns: []string{"dot"}, wantMatch: true},
		{name: "combined", recipient: "first.name+tag@example.com", target: "firstname@example.com", patterns: []string{"plus", "dot"}, wantMatch: true},
		{name: "combined requires both", recipient: "first.name+tag@example.com", target: "firstname@example.com", patterns: []string{"plus"}, wantMatch: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := []MailRule{{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true}}
			for _, pattern := range test.patterns {
				rules = append(rules, MailRule{Type: MailRuleRecipient, Pattern: pattern, Enabled: true})
			}
			matched, code, _ := matchAndExtract(FetchedMessage{
				Recipient: test.recipient,
				Sender:    "sender@example.net",
				Body:      "Code: 654321",
			}, OrderScope{
				Recipient:     test.target,
				RecipientKind: "exact",
				LooseMatch:    true,
				Rules:         rules,
			})
			require.Equal(t, test.wantMatch, matched)
			if test.wantMatch {
				require.Equal(t, "654321", code)
			}
		})
	}
}

func TestRecipientBuiltInStrategyMustMatchAllocationKind(t *testing.T) {
	message := FetchedMessage{
		Recipient: "name.tag@example.com",
		Sender:    "sender@example.net",
		Body:      "Code: 654321",
	}
	scope := OrderScope{
		Recipient:     "name.tag@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "dot", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}

	matched, _, _ := matchAndExtractAnyRecipient(message, scope)
	require.False(t, matched)

	scope.RecipientKind = "dot"
	matched, code, _ := matchAndExtractAnyRecipient(message, scope)
	require.True(t, matched)
	require.Equal(t, "654321", code)
}

func TestRecipientRuleRejectsRegexPattern(t *testing.T) {
	message := FetchedMessage{Recipient: "user@example.com", Sender: "sender@example.net"}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: `.*`, Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}

	matched, _, _ := matchAndExtract(message, scope)
	require.False(t, matched)
}

func TestStrictBodyRuleExtractsCaptureGroup(t *testing.T) {
	message := FetchedMessage{
		Recipient: "user@example.com",
		Sender:    "notify@example.net",
		Subject:   "Login verification",
		Body:      "Use token ABC-135790 to continue.",
	}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    false,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `notify@example\.net`, Enabled: true},
			{Type: MailRuleSubject, Pattern: `Login`, Enabled: true},
			{Type: MailRuleBody, Pattern: `token\s+([A-Z]+-\d{6})`, Enabled: true},
		},
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "ABC-135790", code)
	require.Empty(t, diagnostic)
}

func TestLooseModeUsesGenericNumericExtraction(t *testing.T) {
	message := FetchedMessage{
		Recipient: "user@example.com",
		Sender:    "sender@example.net",
		Body:      "OTP: 87654321",
	}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
			{Type: MailRuleBody, Pattern: `never-match-(\d+)`, Enabled: true},
		},
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "87654321", code)
	require.Empty(t, diagnostic)
}

func TestLooseModePrefersBodyRuleExtraction(t *testing.T) {
	message := FetchedMessage{
		Recipient: "user@example.com",
		Sender:    "sender@example.net",
		Body:      "您的验证码是 1GO-6KT；备用数字 87654321",
	}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
			{Type: MailRuleBody, Pattern: `(?:^|[^A-Za-z0-9])([A-Za-z0-9]{3}-[A-Za-z0-9]{3})(?:$|[^A-Za-z0-9])`, Enabled: true},
		},
	}

	matched, code, diagnostic := matchAndExtractAnyRecipient(message, scope)

	require.True(t, matched)
	require.Equal(t, "1GO-6KT", code)
	require.Empty(t, diagnostic)
}

func TestLooseModeRequiresSenderRule(t *testing.T) {
	message := FetchedMessage{Recipient: "user@example.com", Sender: "sender@example.net"}
	scope := OrderScope{
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		LooseMatch:    true,
		Rules:         []MailRule{{Type: MailRuleRecipient, Pattern: "exact", Enabled: true}},
	}

	matched, _, _ := matchAndExtract(message, scope)
	require.False(t, matched)
}

func TestLooseModeKeepsOrderMailWithoutExtractableCode(t *testing.T) {
	const orderID = 42
	repo := &matchingRepoStub{scopes: []OrderScope{{
		OrderID:       orderID,
		OrderNo:       "OR_LOOSE",
		Recipient:     "user@example.com",
		RecipientKind: "exact",
		ServiceMode:   "purchase",
		LooseMatch:    true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}}}
	matches := &matchResultStub{}
	uc := NewUseCase(repo, nil, nil, matches)

	stored, matched, _, err := uc.ingestFetchedMessages(context.Background(), []FetchedMessage{{
		EmailResourceID: 1,
		ResourceType:    domain.ResourceTypeDomain,
		Recipient:       "user@example.com",
		Sender:          "sender@example.net",
		Body:            "验证码是：123456789",
		ReceivedAt:      time.Now().UTC(),
	}})

	require.NoError(t, err)
	require.Equal(t, 1, stored)
	require.Equal(t, 1, matched)
	require.NotNil(t, repo.purchaseDelivery)
	require.Equal(t, domain.MessageStatusMatched, repo.purchaseDelivery.Status)
	require.Equal(t, uint(orderID), *repo.purchaseDelivery.MatchedOrderID)
	require.Empty(t, repo.purchaseDelivery.VerificationCode)
	require.Len(t, matches.results, 1)
	require.Equal(t, "OR_LOOSE", matches.results[0].OrderNo)
	require.Empty(t, matches.results[0].VerificationCode)
}

func TestBodyPreviewDoesNotSplitUTF8(t *testing.T) {
	preview := bodyPreview(strings.Repeat("a", 999) + "中")

	require.True(t, utf8.ValidString(preview))
	require.LessOrEqual(t, len(preview), 1000)
}

func TestScopeReadableKeepsPurchaseServiceAfterWarranty(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	receiveUntil := now.Add(time.Hour)
	afterSaleUntil := now.Add(24 * time.Hour)
	activatedAt := now.Add(5 * time.Minute)

	preActivation := OrderScope{
		OrderNo:          "OR_PURCHASE_PRE",
		EmailResourceID:  1,
		Recipient:        "user@example.com",
		ServiceMode:      "purchase",
		OrderStatus:      "active",
		ReceiveUntil:     &receiveUntil,
		AfterSaleUntil:   nil,
		AllocationID:     1,
		AllocationType:   domain.ResourceTypeMicrosoft,
		ReceiveStartedAt: &now,
	}
	require.True(t, scopeReadable(preActivation, func() time.Time { return now }))
	require.True(t, scopeReadable(preActivation, func() time.Time { return receiveUntil.Add(time.Second) }))

	activated := preActivation
	activated.OrderNo = "OR_PURCHASE_ACTIVE"
	activated.ActivatedAt = &activatedAt
	activated.AfterSaleUntil = &afterSaleUntil
	require.True(t, scopeReadable(activated, func() time.Time { return receiveUntil.Add(time.Second) }))
	activated.OrderStatus = "completed"
	require.True(t, scopeReadable(activated, func() time.Time { return afterSaleUntil.Add(time.Second) }))
}

func TestShouldScheduleReadFetchOnlySuppressesDeliveredCodeOrders(t *testing.T) {
	require.False(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "code"}, true))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "code"}, false))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "purchase"}, true))
	require.True(t, shouldScheduleReadFetch(OrderScope{ServiceMode: "purchase"}, false))
}

func TestMessageDedupeKeyMatchesAcrossProvidersWithoutMessageID(t *testing.T) {
	receivedAt := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	graph := FetchedMessage{
		Recipient:         "user@example.com",
		Sender:            "noreply@example.net",
		Subject:           "Verification",
		Body:              "<p>Your code is <b>123456</b></p>",
		ProviderMessageID: "graph-id",
		Protocol:          "graph",
		ReceivedAt:        receivedAt,
	}
	imap := graph
	imap.ProviderMessageID = "imap-uid"
	imap.Protocol = "imap"

	require.Equal(t, messageDedupeKey(graph), messageDedupeKey(imap))
}

func TestHistoricalMessageMatchesProjectWithoutVerificationCode(t *testing.T) {
	scope := HistoricalProjectScope{
		ProjectID:  10,
		LooseMatch: true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `noreply@github\.com`, Enabled: true},
		},
	}
	message := HistoricalProjectMessage{
		Recipients: []string{"main@example.com"},
		Sender:     "noreply@github.com",
		Subject:    "Welcome to GitHub",
		Body:       "Your account is ready.",
		ReceivedAt: time.Now().UTC(),
	}

	require.True(t, historicalMessageMatchesProject(message, "main@example.com", scope))
	require.Equal(t, []string{"main@example.com"}, historicalRecipientCandidates("main@example.com", []string{
		"main@example.com", "coworker@another-domain.test",
	}))
	require.Equal(t, []string{"custom-alias@example.com"}, historicalRecipientCandidates("main@example.com", []string{
		"custom-alias@example.com",
	}))
	require.Equal(t, "plus", historicalRecipientKind("main@example.com", "main+github@example.com"))
	require.Equal(t, "dot", historicalRecipientKind("firstname@example.com", "first.name@example.com"))
}

func TestLatestOrderDeliveriesKeepsNewestMessagePerOrder(t *testing.T) {
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	deliveries := latestOrderDeliveries([]matchedDelivery{
		{scope: OrderScope{OrderID: 1}, message: domain.Message{DedupeKey: "older", ReceivedAt: base}},
		{scope: OrderScope{OrderID: 2}, message: domain.Message{DedupeKey: "other", ReceivedAt: base}},
		{scope: OrderScope{OrderID: 1}, message: domain.Message{DedupeKey: "newer", ReceivedAt: base.Add(time.Minute)}},
	})
	require.Len(t, deliveries, 2)
	require.Equal(t, uint(1), deliveries[0].scope.OrderID)
	require.Equal(t, uint(2), deliveries[1].scope.OrderID)
	byOrder := make(map[uint]matchedDelivery, len(deliveries))
	for _, delivery := range deliveries {
		byOrder[delivery.scope.OrderID] = delivery
	}
	require.Equal(t, "newer", byOrder[1].message.DedupeKey)
	require.Equal(t, "other", byOrder[2].message.DedupeKey)
}
