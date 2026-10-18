package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type scanCacheStub struct {
	pickupMessageCacheStub
	progress *PickupScanProgress
	advances int
}

func (c *scanCacheStub) LoadScanProgress(context.Context, uint) (*PickupScanProgress, error) {
	if c.progress == nil {
		return nil, nil
	}
	value := *c.progress
	return &value, nil
}
func (c *scanCacheStub) StoreScanProgress(_ context.Context, _ uint, _ string, progress PickupScanProgress, _ time.Duration) (bool, error) {
	c.progress = &progress
	c.advances++
	return true, nil
}

type scanRepoStub struct {
	*appendFenceRepoStub
	scope      OrderScope
	fenceError error
	deliveries map[uint]domain.Message
}

func (r *scanRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*OrderScope, error) {
	value := r.scope
	return &value, nil
}
func (r *scanRepoStub) AssertDaumCredentialRevision(context.Context, uint, uint64) error {
	return r.fenceError
}
func (r *scanRepoStub) CreateOrderDelivery(_ context.Context, order uint, message domain.Message) error {
	r.deliveries[order] = message
	return nil
}
func (r *scanRepoStub) FindOrderDelivery(_ context.Context, order uint) (*OrderDelivery, error) {
	message, found := r.deliveries[order]
	if !found {
		return nil, nil
	}
	return &OrderDelivery{Message: &message, ReceivedAt: message.ReceivedAt}, nil
}

func TestPickupScanAdvancesPastCacheLimitAndKeepsAllRecipientMatches(t *testing.T) {
	runtimeconfig.Set("mail_stream_batch_size", "10")
	t.Cleanup(func() { runtimeconfig.Delete("mail_stream_batch_size") })
	now := time.Now().UTC().Truncate(time.Second)
	start, until := now.Add(-time.Hour), now.Add(time.Hour)
	scope := OrderScope{OrderID: 1, OrderNo: "one", EmailResourceID: 91, AllocationID: 1, AllocationType: domain.ResourceTypeDaum, CredentialRevision: 3,
		Recipient: "one@hanmail.net", ServiceMode: "code", OrderStatus: "active", ReceiveStartedAt: &start, ReceiveUntil: &until,
		Rules: []MailRule{{Type: MailRuleRecipient, Pattern: "exact", Enabled: true}, {Type: MailRuleSender, Pattern: "service", Enabled: true}, {Type: MailRuleSubject, Pattern: "code", Enabled: true}, {Type: MailRuleBody, Pattern: "([0-9]{6})", Enabled: true}}}
	second := scope
	second.OrderID, second.OrderNo, second.Recipient = 2, "two", "one@hanmail2.net"
	repo := &scanRepoStub{appendFenceRepoStub: &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{scopesByRecipient: map[string][]OrderScope{scope.Recipient: {scope}, second.Recipient: {second}}}}, scope: scope, deliveries: make(map[uint]domain.Message)}
	cache := &scanCacheStub{}
	uc := NewUseCase(repo, nil, nil, &matchResultStub{})
	uc.SetPickupMessageCachePort(cache)
	uc.SetPickupFetchStatePort(&pickupFetchStateStub{})
	clock := now
	uc.now = func() time.Time { return clock }
	var cursors []string
	uc.SetDaumMailFetchPort(daumFetchFunc(func(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
		require.Equal(t, 1, request.MaxMessages, "the order response limit remains one")
		require.NotNil(t, request.Page)
		if request.UntilAt.After(now) {
			return &FetchMessagesResult{Page: &mailbox.PageResult{Complete: true}}, nil
		}
		require.Equal(t, now, request.UntilAt, "the backfill snapshot must not move with the clock")
		cursors = append(cursors, request.Page.Cursor)
		page, _ := strconv.Atoi(request.Page.Cursor)
		var messages []FetchedMessage
		for index := page * 10; index < min((page+1)*10, 115); index++ {
			recipient := "one@daum.net"
			if index == 61 {
				recipient = scope.Recipient
			}
			if index == 111 {
				recipient = second.Recipient
			}
			messages = append(messages, FetchedMessage{EmailResourceID: 91, ResourceType: domain.ResourceTypeDaum, CredentialRevision: 3, Recipient: recipient, Sender: "service@example.test", Subject: "code", Body: "654321", MessageIDHeader: fmt.Sprint(index), ReceivedAt: now.Add(-time.Duration(index+1) * time.Second)})
		}
		return &FetchMessagesResult{Messages: messages, Page: &mailbox.PageResult{Complete: page == 11, NextCursor: strconv.Itoa(page + 1)}}, nil
	}))
	for round := 0; round < 24; round++ {
		require.NoError(t, uc.ProcessFetch(context.Background(), pickupFetchTask(91, "one", clock)))
		clock = clock.Add(11 * time.Second)
		cache.messages, cache.found = nil, false // Body-cache expiry must not erase scan progress.
	}
	require.Len(t, cursors, 12)
	require.Equal(t, "", cursors[0])
	require.Equal(t, "11", cursors[11])
	require.Len(t, repo.facts, 115)
	require.Equal(t, scope.Recipient, repo.deliveries[1].Recipient)
	require.Equal(t, second.Recipient, repo.deliveries[2].Recipient)
	require.True(t, cache.progress.BackfillComplete)
}

func TestPickupScanDoesNotAdvanceOnFailedFetchOrFence(t *testing.T) {
	for _, failure := range []string{"fetch", "fence"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Now().UTC()
			repo := &scanRepoStub{appendFenceRepoStub: &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{}}, scope: OrderScope{OrderNo: "test", AllocationType: domain.ResourceTypeDaum, AllocationID: 1, EmailResourceID: 91, CredentialRevision: 1, Recipient: "one@daum.net", OrderStatus: "active", ServiceMode: "purchase"}}
			cache := &scanCacheStub{}
			uc := NewUseCase(repo, nil, nil, nil)
			uc.SetPickupMessageCachePort(cache)
			uc.SetPickupFetchStatePort(&pickupFetchStateStub{})
			uc.SetDaumMailFetchPort(daumFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
				if failure == "fetch" {
					return nil, errors.New("page interrupted")
				}
				repo.fenceError = domain.ErrResourceFetchCredentialChanged
				return &FetchMessagesResult{Page: &mailbox.PageResult{NextCursor: "next"}}, nil
			}))
			require.Error(t, uc.ProcessFetch(context.Background(), pickupFetchTask(91, "test", now)))
			require.Zero(t, cache.advances)
			require.Zero(t, cache.stores)
		})
	}
}

func TestPickupCacheCannotSatisfyOutsideOrderWindowOrUnknownTime(t *testing.T) {
	now := time.Now().UTC()
	start, end := now.Add(-time.Minute), now.Add(time.Minute)
	scope := OrderScope{EmailResourceID: 91, CredentialRevision: 1, AllocationType: domain.ResourceTypeDaum, ServiceMode: "code", Recipient: "one@hanmail.net", ReceiveStartedAt: &start, ReceiveUntil: &end, LooseMatch: true,
		Rules: []MailRule{{Type: MailRuleRecipient, Pattern: "exact", Enabled: true}, {Type: MailRuleSender, Pattern: "service", Enabled: true}}}
	message := FetchedMessage{EmailResourceID: 91, CredentialRevision: 1, ResourceType: domain.ResourceTypeDaum, Recipient: scope.Recipient, Sender: "service@example.test", Body: "Your code is 123456"}
	for _, received := range []time.Time{{}, now.Add(-time.Hour), now.Add(time.Hour)} {
		message.ReceivedAt = received
		messages, satisfied := cachedMessagesMatchingScopes([]FetchedMessage{message}, 91, []OrderScope{scope}, now)
		require.Empty(t, messages)
		require.False(t, satisfied)
	}
	message.ReceivedAt = now
	messages, satisfied := cachedMessagesMatchingScopes([]FetchedMessage{message}, 91, []OrderScope{scope}, now)
	require.Len(t, messages, 1)
	require.True(t, satisfied)
}

func TestPickupScanRestartsWhenAnEarlierOrderWindowAppears(t *testing.T) {
	now := time.Now().UTC()
	scope := OrderScope{AllocationType: domain.ResourceTypeDaum, CredentialRevision: 3}
	previous := PickupScanProgress{ResourceType: scope.AllocationType, CredentialRevision: 3, SinceAt: now.Add(-time.Hour), BackfillUntil: now, BackfillCursor: "already-filtered-headers", NextBackfill: true}
	page := nextPickupScanPage(scope, now.Add(-2*time.Hour), now.Add(time.Minute), &previous)
	require.Empty(t, page.request.Cursor)
	require.Equal(t, now.Add(-2*time.Hour), page.since)
	require.Equal(t, now.Add(time.Minute), page.until)
}

func TestPickupFreshHeadIncludesDelayedMessagesWithOldProviderDates(t *testing.T) {
	now := time.Now().UTC()
	scope := OrderScope{AllocationType: domain.ResourceTypeDaum, CredentialRevision: 3}
	previous := PickupScanProgress{ResourceType: scope.AllocationType, CredentialRevision: 3, SinceAt: now.Add(-time.Hour), BackfillUntil: now, BackfillCursor: "deep-page", NextBackfill: false}
	page := nextPickupScanPage(scope, previous.SinceAt, now.Add(time.Minute), &previous)
	require.False(t, page.backfill)
	require.Equal(t, previous.SinceAt, page.since)
	require.Empty(t, page.request.Cursor)
	progress, err := page.completed(&mailbox.PageResult{NextCursor: "head-page", Complete: false})
	require.NoError(t, err)
	require.Equal(t, "deep-page", progress.BackfillCursor)
	next := nextPickupScanPage(scope, previous.SinceAt, now.Add(2*time.Minute), &progress)
	require.True(t, next.backfill)
	require.Equal(t, "deep-page", next.request.Cursor)
	require.Equal(t, now, next.until)
}

type scanMicrosoftFetchFunc func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error)

func (f scanMicrosoftFetchFunc) FetchMicrosoftMessages(ctx context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	return f(ctx, request)
}

type scanMicrosoftCredentials struct {
	coreapp.MicrosoftCredentialPort
	repo    *scanRepoStub
	replace bool
}

func (c *scanMicrosoftCredentials) ApplyMicrosoftFetchRefreshToken(_ context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	if c.repo.scope.CredentialRevision != update.ExpectedCredentialRevision {
		return coreapp.ErrMicrosoftCredentialChanged
	}
	c.repo.scope.MicrosoftRT = update.RefreshToken
	c.repo.scope.CredentialRevision++
	if c.replace {
		c.repo.scope.MicrosoftRT = "unrelated-credential"
		c.repo.scope.CredentialRevision++
	}
	return nil
}
func (c *scanMicrosoftCredentials) LockMicrosoftCredentialScope(context.Context, uint) (*coreapp.MicrosoftCredentialScope, error) {
	s := c.repo.scope
	return &coreapp.MicrosoftCredentialScope{ResourceID: s.EmailResourceID, EmailAddress: s.MicrosoftEmail, ClientID: s.MicrosoftClientID, RefreshToken: s.MicrosoftRT, CredentialRevision: s.CredentialRevision}, nil
}

func TestPickupScanFollowsControlledMicrosoftTokenRotationOnly(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			now := time.Now().UTC()
			repo := &scanRepoStub{appendFenceRepoStub: &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{}}, scope: OrderScope{OrderNo: "ms-scan", AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1, EmailResourceID: 91, CredentialRevision: 3, Recipient: "user@outlook.com", MicrosoftEmail: "user@outlook.com", MicrosoftClientID: "client", MicrosoftRT: "initial", OrderStatus: "active", ServiceMode: "purchase"}}
			cache := &scanCacheStub{}
			var cursors []string
			var requests int
			transport := scanMicrosoftFetchFunc(func(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
				requests++
				if requests%2 == 1 {
					cursors = append(cursors, request.Page.Cursor)
				}
				page, _ := strconv.Atoi(request.Page.Cursor)
				return &FetchMessagesResult{RefreshToken: fmt.Sprintf("rotated%d", requests), Page: &mailbox.PageResult{NextCursor: strconv.Itoa(page + 1)}}, nil
			})
			uc := NewUseCase(repo, nil, transport, nil)
			uc.SetPickupMessageCachePort(cache)
			uc.SetPickupFetchStatePort(&pickupFetchStateStub{})
			uc.SetMicrosoftCredentialPort(&scanMicrosoftCredentials{repo: repo, replace: replace})
			for request := 0; request < 5; request++ {
				err := uc.ProcessFetch(context.Background(), pickupFetchTask(91, "ms-scan", now))
				if replace {
					require.ErrorIs(t, err, domain.ErrResourceFetchCredentialChanged)
					break
				}
				require.NoError(t, err)
				require.Equal(t, repo.scope.CredentialRevision, cache.progress.CredentialRevision)
			}
			if replace {
				require.Zero(t, cache.advances)
			} else {
				require.Equal(t, []string{"", "1", "2"}, cursors)
			}
		})
	}
}

func TestCachedMessageIDCollisionRetainsBothOriginalMessages(t *testing.T) {
	now := time.Now()
	first := FetchedMessage{EmailResourceID: 91, ResourceType: domain.ResourceTypeDaum, Recipient: "one@daum.net", Body: "private main mail", MessageIDHeader: "duplicate", ProviderMessageID: "imap:INBOX:1:1", Protocol: "imap", Folder: "INBOX", ReceivedAt: now}
	second := first
	second.Recipient, second.Body, second.ProviderMessageID = "one@hanmail2.net", "654321", "imap:INBOX:1:2"
	merged := mergePickupMessages([]FetchedMessage{second}, []FetchedMessage{first})
	require.Len(t, merged, 2)
	keys := pickupMessageIdentityKeys(merged)
	require.NotContains(t, keys, "internet:duplicate")
	require.Len(t, keys, 2)
}

func TestPickupScanWindowIncludesAllRequestedOrdersOnAccount(t *testing.T) {
	now := time.Now().UTC()
	newStart, oldStart := now.Add(-time.Minute), now.Add(-time.Hour)
	shortEnd, longEnd := now.Add(time.Minute), now.Add(2*time.Minute)
	first := OrderScope{OrderNo: "new", AllocationType: domain.ResourceTypeDaum, AllocationID: 1, EmailResourceID: 91, Recipient: "one@daum.net", ServiceMode: "code", OrderStatus: "active", ReceiveStartedAt: &newStart, ReceiveUntil: &shortEnd}
	second := first
	second.OrderNo, second.Recipient, second.ReceiveStartedAt, second.ReceiveUntil = "old", "one@hanmail2.net", &oldStart, &longEnd
	uc := NewUseCase(&pickupRequestRepoStub{scopes: map[string]OrderScope{first.OrderNo: first, second.OrderNo: second}}, nil, nil, nil)
	uc.now = func() time.Time { return now }
	selected, since, until, err := uc.selectPickupRequestOrder(context.Background(), PickupRequestFetchScope{OrderNos: []string{"new", "old"}, EmailResourceID: 91})
	require.NoError(t, err)
	require.Equal(t, "new", selected)
	expectedSince, _ := OrderReadWindow(second, now)
	require.Equal(t, expectedSince, since)
	require.Equal(t, now, until)
}

type unavailableScanMicrosoftCredentials struct {
	*scanMicrosoftCredentials
	deleted bool
}

func (c unavailableScanMicrosoftCredentials) LockMicrosoftCredentialScope(ctx context.Context, id uint) (*coreapp.MicrosoftCredentialScope, error) {
	if !c.deleted {
		return nil, nil
	}
	scope, err := c.scanMicrosoftCredentials.LockMicrosoftCredentialScope(ctx, id)
	if scope != nil {
		scope.Status = "deleted"
	}
	return scope, err
}

func TestPickupScanRejectsDeletedOrMissingMicrosoftScopeBeforeProgress(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			now := time.Now().UTC()
			repo := &scanRepoStub{appendFenceRepoStub: &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{}}, scope: OrderScope{OrderNo: "ms-deleted", AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1, EmailResourceID: 91, CredentialRevision: 3, Recipient: "user@outlook.com", MicrosoftEmail: "user@outlook.com", MicrosoftClientID: "client", MicrosoftRT: "initial", OrderStatus: "active", ServiceMode: "purchase"}}
			cache := &scanCacheStub{}
			uc := NewUseCase(repo, nil, scanMicrosoftFetchFunc(func(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
				return &FetchMessagesResult{Page: &mailbox.PageResult{Complete: true}}, nil
			}), nil)
			uc.SetPickupMessageCachePort(cache)
			uc.SetPickupFetchStatePort(&pickupFetchStateStub{})
			uc.SetMicrosoftCredentialPort(unavailableScanMicrosoftCredentials{scanMicrosoftCredentials: &scanMicrosoftCredentials{repo: repo}, deleted: deleted})
			err := uc.ProcessFetch(context.Background(), pickupFetchTask(91, "ms-deleted", now))
			if deleted {
				require.ErrorIs(t, err, domain.ErrResourceFetchDeleted)
			} else {
				require.ErrorIs(t, err, domain.ErrResourceFetchCredentialChanged)
			}
			require.Zero(t, cache.advances)
			require.Zero(t, cache.stores)
			require.Zero(t, repo.appended)
		})
	}
}
