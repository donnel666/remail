package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

type appendFenceRepoStub struct {
	*matchingRepoStub
	facts       map[string]domain.Message
	projections map[uint]domain.Message
	nextID      uint
	appended    int
	projected   int
	replayTypes []domain.ResourceType
}

func (r *appendFenceRepoStub) AppendMessages(_ context.Context, messages []domain.Message) ([]domain.Message, int, error) {
	if r.facts == nil {
		r.facts = make(map[string]domain.Message)
	}
	stored := make([]domain.Message, len(messages))
	inserted := 0
	for i := range messages {
		identity := mailMessageIdentity(messages[i])
		fact, ok := r.facts[identity]
		if !ok {
			r.nextID++
			fact = messages[i]
			fact.ID = r.nextID
			fact.MatchedOrderID = nil
			fact.Status = domain.MessageStatusReceived
			fact.VerificationCode = ""
			fact.MatchDiagnostic = ""
			r.facts[identity] = fact
			inserted++
		}
		stored[i] = fact
	}
	r.appended += inserted
	return stored, inserted, nil
}

func (r *appendFenceRepoStub) ListUnprojectedMessages(_ context.Context, resourceType domain.ResourceType, resourceIDs []uint, limit int) ([]domain.Message, error) {
	r.replayTypes = append(r.replayTypes, resourceType)
	resources := make(map[uint]struct{}, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		resources[resourceID] = struct{}{}
	}
	result := make([]domain.Message, 0)
	for _, fact := range r.facts {
		if fact.ResourceType != resourceType {
			continue
		}
		if _, requested := resources[fact.EmailResourceID]; !requested {
			continue
		}
		if _, projected := r.projections[fact.ID]; projected {
			continue
		}
		result = append(result, fact)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].ReceivedAt.Equal(result[j].ReceivedAt) {
			return result[i].ReceivedAt.After(result[j].ReceivedAt)
		}
		return result[i].ID > result[j].ID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *appendFenceRepoStub) InsertMessageProjections(_ context.Context, messages []domain.Message) ([]domain.Message, []uint, error) {
	if r.projections == nil {
		r.projections = make(map[uint]domain.Message)
	}
	projected := make([]domain.Message, len(messages))
	newlyMatched := make([]uint, 0, len(messages))
	for i := range messages {
		current, exists := r.projections[messages[i].ID]
		if !exists || current.Status != domain.MessageStatusMatched {
			current = messages[i]
			r.projections[messages[i].ID] = current
			if current.Status == domain.MessageStatusMatched {
				newlyMatched = append(newlyMatched, current.ID)
			}
		}
		projected[i] = current
	}
	r.projected += len(newlyMatched)
	return projected, newlyMatched, nil
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

func (r *matchingRepoStub) CreateOrderDelivery(_ context.Context, _ uint, message domain.Message) error {
	if r.purchaseDelivery != nil {
		return nil
	}
	r.purchaseDelivery = &message
	return nil
}

func (r *matchingRepoStub) FindOrderDelivery(context.Context, uint) (*OrderDelivery, error) {
	if r.purchaseDelivery == nil {
		return nil, nil
	}
	message := *r.purchaseDelivery
	return &OrderDelivery{Message: &message, ReceivedAt: message.ReceivedAt}, nil
}

type matchResultStub struct {
	results []MatchResult
}

type pickupBatchRepoStub struct {
	Repository
	scopes          map[string]OrderScope
	matchingScopes  []OrderScope
	messages        map[uint][]domain.Message
	delivery        *OrderDelivery
	state           *domain.FetchState
	stateReads      int
	fetchRequestErr error
	fetchRequests   int
	upsertErr       error
	upserts         int
}

func (r *pickupBatchRepoStub) LoadPickupScope(_ context.Context, token string, email string) (*OrderScope, error) {
	scope, ok := r.scopes[token+"|"+email]
	if !ok {
		return nil, domain.ErrPickupCredentialInvalid
	}
	return &scope, nil
}

func (r *pickupBatchRepoStub) FindOrderDelivery(context.Context, uint) (*OrderDelivery, error) {
	return r.delivery, nil
}

func (r *pickupBatchRepoStub) ListMatchingScopesByRecipient(context.Context, domain.ResourceType, uint, string, time.Time) ([]OrderScope, error) {
	return r.matchingScopes, nil
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

func (r *pickupBatchRepoStub) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	r.upserts++
	if r.upsertErr != nil {
		return nil, r.upsertErr
	}
	for i := range messages {
		messages[i].ID = uint(i + 1)
	}
	return messages, nil
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

func TestAppendOnlyIngestDoesNotProjectBeforeSecondFence(t *testing.T) {
	now := time.Now().UTC()
	repo := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{
		scopes: []OrderScope{{
			OrderID: 42, OrderNo: "OR_FENCE", EmailResourceID: 1,
			Recipient: "user@example.com", RecipientKind: "exact",
			ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
			Rules: []MailRule{
				{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
				{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
			},
		}},
	}}
	uc := NewUseCase(repo, nil, nil, &matchResultStub{})
	fenceCalls := 0

	_, _, _, err := uc.ingestFetchedMessagesWithFence(context.Background(), []FetchedMessage{{
		EmailResourceID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "user@example.com", Sender: "sender@example.net",
		Body: "Your code is 123456", ReceivedAt: now,
	}}, func(context.Context) error {
		fenceCalls++
		if fenceCalls == 2 {
			return domain.ErrFetchJobConflict
		}
		return nil
	})

	require.ErrorIs(t, err, domain.ErrFetchJobConflict)
	require.Equal(t, 2, fenceCalls)
	require.Equal(t, 1, repo.appended)
	require.Zero(t, repo.projected)
	require.Nil(t, repo.purchaseDelivery)

	// A later provider response may no longer contain the first message. The
	// append-only fact must still be replayed from MySQL and made visible.
	_, matched, _, err := uc.ingestFetchedMessagesForResourcesWithFence(context.Background(), nil, domain.ResourceTypeMicrosoft, []uint{1}, func(context.Context) error {
		fenceCalls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, matched)
	require.Equal(t, 1, repo.projected)
	require.Equal(t, []domain.ResourceType{domain.ResourceTypeMicrosoft, domain.ResourceTypeMicrosoft}, repo.replayTypes)
	require.NotNil(t, repo.purchaseDelivery)
	require.Equal(t, "123456", repo.purchaseDelivery.VerificationCode)
}

func TestPickupMessageCacheMatchesExplicitAliasOnLaterRequest(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	repo := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{
		scopes: []OrderScope{{
			OrderID: 43, OrderNo: "OR_ALIAS_CACHE", EmailResourceID: 100,
			AllocationType: domain.ResourceTypeMicrosoft,
			Recipient:      "alias@outlook.com", RecipientKind: "exact",
			ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
			Rules: []MailRule{
				{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
				{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
			},
		}},
	}}
	cache := &pickupMessageCacheStub{found: true, messages: []FetchedMessage{{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "alias@outlook.com", Recipients: []string{"alias@outlook.com"},
		Sender: "sender@example.net", Subject: "Cached code",
		Body: "Your code is 654321", ReceivedAt: now,
	}}}
	uc := NewUseCase(repo, nil, nil, &matchResultStub{})
	uc.SetPickupMessageCachePort(cache)

	result := uc.applyPickupMessageCache(context.Background(), 100, repo.scopes)

	require.True(t, result.satisfied)
	require.True(t, result.applied)
	require.NotNil(t, repo.purchaseDelivery)
	require.Equal(t, "654321", repo.purchaseDelivery.VerificationCode)
	require.Equal(t, "alias@outlook.com", repo.purchaseDelivery.Recipient)
}

func TestAppendOnlyIngestCountsOnlyNewFactsAndMatches(t *testing.T) {
	now := time.Now().UTC()
	repo := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{
		scopes: []OrderScope{{
			OrderID: 42, OrderNo: "OR_METRICS", EmailResourceID: 1,
			Recipient: "user@example.com", RecipientKind: "exact",
			ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
			Rules: []MailRule{
				{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
				{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
			},
		}},
	}}
	matches := &matchResultStub{}
	uc := NewUseCase(repo, nil, nil, matches)
	fetched := []FetchedMessage{{
		EmailResourceID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "user@example.com", Sender: "sender@example.net",
		Body: "Your code is 123456", ReceivedAt: now,
	}}

	stored, matched, _, err := uc.ingestFetchedMessages(context.Background(), fetched)
	require.NoError(t, err)
	require.Equal(t, 1, stored)
	require.Equal(t, 1, matched)
	stored, matched, _, err = uc.ingestFetchedMessages(context.Background(), fetched)
	require.NoError(t, err)
	require.Zero(t, stored)
	require.Zero(t, matched)
	require.Len(t, matches.results, 2, "the idempotent Trade notification must be replayed after its first post-commit attempt could have failed")
}

func TestMergePickupMessagesKeepsNewestThirty(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	messages := make([]FetchedMessage, 35)
	for i := range messages {
		messages[i] = FetchedMessage{
			EmailResourceID: 1, ResourceType: domain.ResourceTypeMicrosoft,
			ProviderMessageID: fmt.Sprintf("message-%02d", i),
			Protocol:          "graph", Folder: "Inbox", ReceivedAt: now.Add(time.Duration(i) * time.Second),
		}
	}

	merged := mergePickupMessages(messages[:20], messages[20:])

	require.Len(t, merged, pickupMessageCacheLimit)
	require.Equal(t, "message-34", merged[0].ProviderMessageID)
	require.Equal(t, "message-05", merged[len(merged)-1].ProviderMessageID)
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
	require.Len(t, queue.snapshot(), 1)
	acquired, released := state.snapshot()
	require.Empty(t, acquired)
	require.Zero(t, released)
}

func TestListPickupMailReturnsEmptyAndSchedulesRefreshWhenCacheDoesNotMatch(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	repo := &pickupBatchRepoStub{
		scopes: map[string]OrderScope{
			"token-a|alias@example.com": {
				OrderID: 1, OrderNo: "ORDER-ALIAS", ProjectID: 1,
				ServiceMode: "purchase", OrderStatus: "active",
				AllocationType: domain.ResourceTypeMicrosoft,
				AllocationID:   1, EmailResourceID: 100, Recipient: "alias@example.com",
			},
		},
	}
	queue := &pickupBatchQueueStub{}
	cache := &pickupMessageCacheStub{found: true, messages: []FetchedMessage{}}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupMessageCachePort(cache)
	uc.now = func() time.Time { return now }

	items, _, err := uc.ListPickupMail(context.Background(), "token-a", "alias@example.com")

	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 1, cache.loads)
	require.Len(t, queue.snapshot(), 1)
}

func TestListPickupMailSchedulesMicrosoftWhenCachedMatchingFails(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	scope := OrderScope{
		OrderID: 1, OrderNo: "ORDER-ALIAS", ProjectID: 1,
		ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
		AllocationType: domain.ResourceTypeMicrosoft,
		AllocationID:   1, EmailResourceID: 100, Recipient: "alias@outlook.com",
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}
	repo := &pickupBatchRepoStub{
		scopes:         map[string]OrderScope{"token-a|alias@outlook.com": scope},
		matchingScopes: []OrderScope{scope},
		upsertErr:      errors.New("forced cached matching failure"),
	}
	queue := &pickupBatchQueueStub{}
	cache := &pickupMessageCacheStub{found: true, messages: []FetchedMessage{{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "alias@outlook.com", Sender: "sender@example.net",
		Body: "Your code is 654321", ReceivedAt: now,
	}}}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupMessageCachePort(cache)
	uc.now = func() time.Time { return now }

	items, _, err := uc.ListPickupMail(context.Background(), "token-a", "alias@outlook.com")

	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 1, repo.upserts)
	require.Len(t, queue.snapshot(), 1)
}

func TestListPickupMailWithDeliveryStillUsesMatchingCachedMessages(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	scope := OrderScope{
		OrderID: 1, OrderNo: "ORDER-ALIAS", ProjectID: 1,
		ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
		AllocationType: domain.ResourceTypeMicrosoft,
		AllocationID:   1, EmailResourceID: 100, Recipient: "alias@outlook.com",
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}
	repo := &pickupBatchRepoStub{
		scopes:   map[string]OrderScope{"token-a|alias@outlook.com": scope},
		delivery: &OrderDelivery{Message: &domain.Message{ID: 1, ReceivedAt: now}},
		messages: map[uint][]domain.Message{1: {{ID: 1, ReceivedAt: now}}},
	}
	queue := &pickupBatchQueueStub{}
	cache := &pickupMessageCacheStub{found: true, messages: []FetchedMessage{{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "alias@outlook.com", Sender: "sender@example.net",
		Body: "cached", ReceivedAt: now,
	}}}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupMessageCachePort(cache)
	uc.now = func() time.Time { return now }

	items, _, err := uc.ListPickupMail(context.Background(), "token-a", "alias@outlook.com")

	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, 1, cache.loads)
	require.Equal(t, 1, repo.upserts)
	require.Empty(t, queue.snapshot())
}

func TestApplyPickupMessageCachesIncludesPurchaseScopesWithDelivery(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	scope := OrderScope{
		OrderID: 1, OrderNo: "ORDER-ALIAS", ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
		AllocationType: domain.ResourceTypeMicrosoft, EmailResourceID: 100, Recipient: "alias@outlook.com",
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}
	repo := &pickupBatchRepoStub{}
	cache := &pickupMessageCacheStub{found: true, messages: []FetchedMessage{{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "alias@outlook.com", Sender: "sender@example.net", ReceivedAt: now,
	}}}
	uc := NewUseCase(repo, nil, nil, nil)
	uc.SetPickupMessageCachePort(cache)

	hits, applied := uc.applyPickupMessageCaches(context.Background(), []PickupBatchRead{{
		Scope: &scope, Delivery: &OrderDelivery{Message: &domain.Message{ID: 1, ReceivedAt: now}},
	}})

	require.True(t, hits[100])
	require.True(t, applied)
	require.Equal(t, 1, repo.upserts)
}

func TestApplyPickupMessageCachesLoadsEachResourceOnceOnMiss(t *testing.T) {
	cache := &pickupMessageCacheStub{}
	uc := NewUseCase(nil, nil, nil, nil)
	uc.SetPickupMessageCachePort(cache)
	reads := []PickupBatchRead{
		{Scope: &OrderScope{AllocationType: domain.ResourceTypeMicrosoft, EmailResourceID: 100}},
		{Scope: &OrderScope{AllocationType: domain.ResourceTypeMicrosoft, EmailResourceID: 100}},
	}

	hits, applied := uc.applyPickupMessageCaches(context.Background(), reads)

	require.Empty(t, hits)
	require.False(t, applied)
	require.Equal(t, 1, cache.loads)
}

type pickupBatchReaderStub struct {
	Repository
	reads         []PickupBatchRead
	serviceScopes map[string]*OrderScope
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

func (r *pickupBatchReaderStub) LoadOrderScopeForServiceToken(_ context.Context, orderNo string) (*OrderScope, error) {
	scope := r.serviceScopes[orderNo]
	if scope == nil {
		return nil, domain.ErrOrderUnavailable
	}
	result := *scope
	return &result, nil
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
	requests []PickupRequestFetchTask
	fetchErr error
}

type pickupMessageCacheStub struct {
	messages      []FetchedMessage
	found         bool
	loads         int
	stores        int
	storeID       uint
	storeMessages []FetchedMessage
	storeTTL      time.Duration
}

func (c *pickupMessageCacheStub) Load(context.Context, uint) ([]FetchedMessage, bool, error) {
	c.loads++
	return c.messages, c.found, nil
}

func (c *pickupMessageCacheStub) LoadMany(_ context.Context, resourceIDs []uint) (map[uint][]FetchedMessage, error) {
	c.loads++
	result := make(map[uint][]FetchedMessage, len(resourceIDs))
	if c.found {
		for _, resourceID := range resourceIDs {
			result[resourceID] = c.messages
		}
	}
	return result, nil
}

func (c *pickupMessageCacheStub) Store(_ context.Context, resourceID uint, messages []FetchedMessage, ttl time.Duration) error {
	c.stores++
	c.storeID = resourceID
	c.storeMessages = append([]FetchedMessage(nil), messages...)
	c.storeTTL = ttl
	return nil
}

func (q *pickupBatchQueueStub) EnqueuePickupRequest(_ context.Context, task PickupRequestFetchTask) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requests = append(q.requests, task)
	return q.fetchErr == nil, q.fetchErr
}

func (q *pickupBatchQueueStub) snapshot() []PickupRequestFetchTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]PickupRequestFetchTask(nil), q.requests...)
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
	requests := queue.snapshot()
	acquired, _ := state.snapshot()
	require.Empty(t, acquired)
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Scopes, 2)
	require.Equal(t, "ORDER-A", requests[0].Scopes[0].OrderNo)
	require.Equal(t, "ORDER-B", requests[0].Scopes[1].OrderNo)
	require.NoError(t, results[0].Err)
	require.Equal(t, uint(101), results[0].Items[0].ID)
	require.NoError(t, results[1].Err)
	require.Equal(t, uint(202), results[1].Items[0].ID)
	require.NoError(t, results[2].Err)
	require.Equal(t, uint(303), results[2].Items[0].ID)
	require.ErrorIs(t, results[3].Err, domain.ErrPickupCredentialInvalid)
}

func TestSchedulePickupRequestKeepsAllOrdersForSharedResource(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	queue := &pickupBatchQueueStub{}
	uc := NewUseCase(nil, queue, nil, nil)
	uc.now = func() time.Time { return now }
	scope := func(orderNo string) OrderScope {
		return OrderScope{
			OrderNo: orderNo, OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 11,
			EmailResourceID: 21, Recipient: "shared@example.com",
		}
	}

	uc.scheduleScopeFetches(context.Background(), []OrderScope{
		scope("ORDER-B"), scope("ORDER-A"), scope("ORDER-B"),
	})

	requests := queue.snapshot()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Scopes, 1)
	require.Equal(t, uint(21), requests[0].Scopes[0].EmailResourceID)
	require.Equal(t, "ORDER-B", requests[0].Scopes[0].OrderNo)
	require.Equal(t, []string{"ORDER-B", "ORDER-A"}, requests[0].Scopes[0].OrderNos)
}

func TestListPickupMailBatchKeepsValidFallbackOrderForSharedResource(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	scope := func(orderNo, recipient string) *OrderScope {
		return &OrderScope{
			OrderNo: orderNo, OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 11,
			EmailResourceID: 21, Recipient: recipient,
		}
	}
	stale := scope("ORDER-STALE", "stale@example.com")
	valid := scope("ORDER-VALID", "valid@example.com")
	repo := &pickupBatchReaderStub{
		reads:         []PickupBatchRead{{Scope: stale}, {Scope: valid}},
		serviceScopes: map[string]*OrderScope{"ORDER-VALID": valid},
	}
	queue := &pickupBatchQueueStub{}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.now = func() time.Time { return now }

	results := uc.ListPickupMailBatch(context.Background(), []PickupCredential{
		{Email: stale.Recipient, Token: "token-stale"},
		{Email: valid.Recipient, Token: "token-valid"},
	})

	require.Len(t, results, 2)
	require.NoError(t, results[0].Err)
	require.NoError(t, results[1].Err)
	requests := queue.snapshot()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Scopes, 1)
	require.Equal(t, []string{"ORDER-STALE", "ORDER-VALID"}, requests[0].Scopes[0].OrderNos)
	selected, err := uc.selectPickupRequestOrder(context.Background(), requests[0].Scopes[0])
	require.NoError(t, err)
	require.Equal(t, "ORDER-VALID", selected)
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
	requests := queue.snapshot()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Scopes, 100)
	acquired, _ := state.snapshot()
	require.Empty(t, acquired)
}

func TestListPickupMailBatchDoesNotAcquireResourceLeasesInHTTP(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	repo := &pickupBatchReaderStub{
		reads: []PickupBatchRead{{Scope: &OrderScope{
			OrderID: 1, OrderNo: "ORDER-A", ProjectID: 1, ServiceMode: "purchase", OrderStatus: "active",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1, EmailResourceID: 1, Recipient: "a@example.com",
		}}},
	}
	state := &pickupFetchStateStub{}
	queue := &pickupBatchQueueStub{}
	uc := NewUseCase(repo, queue, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	started := time.Now()
	results := uc.ListPickupMailBatch(context.Background(), []PickupCredential{{Email: "a@example.com", Token: "token-a"}})

	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Empty(t, results[0].Items)
	require.Len(t, queue.snapshot(), 1)
	acquired, _ := state.snapshot()
	require.Empty(t, acquired)
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

func TestEarliestOrderDeliveriesKeepsOldestMessagePerOrder(t *testing.T) {
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	deliveries := earliestOrderDeliveries([]matchedDelivery{
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
	require.Equal(t, "older", byOrder[1].message.DedupeKey)
	require.Equal(t, "other", byOrder[2].message.DedupeKey)
}

func TestLaterPullNotifiesWithImmutableDeliveryHead(t *testing.T) {
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	repo := &matchingRepoStub{scopes: []OrderScope{{
		OrderID: 1, OrderNo: "OR_FIXED", EmailResourceID: 1,
		Recipient: "user@example.com", ServiceMode: "purchase", OrderStatus: "active", LooseMatch: true,
		Rules: []MailRule{
			{Type: MailRuleRecipient, Pattern: "exact", Enabled: true},
			{Type: MailRuleSender, Pattern: `sender@example\.net`, Enabled: true},
		},
	}}}
	matches := &matchResultStub{}
	uc := NewUseCase(repo, nil, nil, matches)

	_, _, _, err := uc.ingestFetchedMessages(context.Background(), []FetchedMessage{{
		EmailResourceID: 1, ResourceType: domain.ResourceTypeDomain,
		Recipient: "user@example.com", Sender: "sender@example.net", ReceivedAt: base,
	}})
	require.NoError(t, err)
	_, _, _, err = uc.ingestFetchedMessages(context.Background(), []FetchedMessage{{
		EmailResourceID: 1, ResourceType: domain.ResourceTypeDomain,
		Recipient: "user@example.com", Sender: "sender@example.net", ReceivedAt: base.Add(time.Minute),
	}})
	require.NoError(t, err)

	require.Len(t, matches.results, 2)
	require.Equal(t, base, matches.results[0].MatchedAt)
	require.Equal(t, base, matches.results[1].MatchedAt)
}

func TestCodePickupIncludesOtherStoredMessagesAfterDelivery(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	scope := OrderScope{
		OrderID: 1, OrderNo: "OR_CODE", EmailResourceID: 1,
		Recipient: "user@example.com", ServiceMode: "code", OrderStatus: "active",
	}
	delivered := domain.Message{ID: 1, ReceivedAt: now.Add(-2 * time.Minute), VerificationCode: "111111"}
	other := domain.Message{ID: 2, ReceivedAt: now.Add(-time.Minute), VerificationCode: "222222"}
	items, _, hasDelivery, err := (&UseCase{}).listOrderMailFromBatch(
		PickupBatchRead{
			Scope:    &scope,
			Delivery: &OrderDelivery{Message: &delivered, ReceivedAt: delivered.ReceivedAt},
			Messages: []domain.Message{other},
		},
		now,
	)

	require.NoError(t, err)
	require.True(t, hasDelivery)
	require.Len(t, items, 2)
	require.Equal(t, uint(delivered.ID), items[0].ID)
	require.Equal(t, uint(other.ID), items[1].ID)
}
