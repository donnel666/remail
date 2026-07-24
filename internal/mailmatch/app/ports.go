package app

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	fetchLookbackWindow     = 90 * 24 * time.Hour
	readWindowSkew          = 2 * time.Minute
	codeReadLimit           = 1
	purchaseReadLimit       = 30
	messageScanLimit        = 40
	projectionReplayLimit   = 100
	pickupFetchReserveTTL   = 2 * time.Minute
	pickupFetchReturnSlack  = 2 * time.Second
	pickupFetchLeaseTTL     = 2 * time.Minute
	pickupMessageCacheTTL   = 10 * time.Second
	pickupMessageCacheLimit = 30
	pickupMessageCacheIO    = time.Second
	pickupFetchHeartbeat    = 30 * time.Second

	maxFetchLookbackWindow     = 3650 * 24 * time.Hour
	maxReadWindowSkew          = 24 * time.Hour
	maxCodeReadLimit           = 100
	maxPurchaseReadLimit       = 500
	maxMessageScanLimit        = 500
	maxProjectionReplayLimit   = 1000
	maxPickupFetchReserveTTL   = 30 * time.Minute
	maxPickupFetchLeaseTTL     = 10 * time.Minute
	maxPickupMessageCacheTTL   = 5 * time.Minute
	maxPickupMessageCacheLimit = 100
	maxPickupFetchHeartbeat    = 5 * time.Minute
	maxVerificationPatternSize = 4096
)

func boundedRuntimeInt(key string, fallback, maximum int) int {
	return min(runtimeconfig.Int(key, fallback, 1), maximum)
}

func boundedRuntimeDuration(key string, fallback, unit, maximum time.Duration) time.Duration {
	return min(runtimeconfig.Duration(key, fallback, unit, 1), maximum)
}

type MailRuleType string

const (
	MailRuleSender    MailRuleType = "sender"
	MailRuleRecipient MailRuleType = "recipient"
	MailRuleSubject   MailRuleType = "subject"
	MailRuleBody      MailRuleType = "body"
)

type MailRule struct {
	Type    MailRuleType
	Pattern string
	Enabled bool
}

type OrderScope struct {
	OrderID            uint
	OrderNo            string
	UserID             uint
	ProjectID          uint
	ProductID          uint
	ServiceMode        string
	OrderStatus        string
	AllocationType     domain.ResourceType
	AllocationID       uint
	RecipientKind      string
	EmailResourceID    uint
	Recipient          string
	ReceiveStartedAt   *time.Time
	ReceiveUntil       *time.Time
	ActivatedAt        *time.Time
	AfterSaleUntil     *time.Time
	LooseMatch         bool
	Rules              []MailRule
	MicrosoftEmail     string
	MicrosoftClientID  string
	MicrosoftRT        string
	CredentialRevision uint64
}

type OrderDelivery struct {
	Message    *domain.Message
	ReceivedAt time.Time
}

type FetchedMessage struct {
	EmailResourceID   uint
	ResourceType      domain.ResourceType
	Recipient         string
	Recipients        []string
	Sender            string
	Subject           string
	Body              string
	RawSource         string
	ProviderPayload   string
	BodyPreview       string
	VerificationCode  string
	MessageIDHeader   string
	ProviderMessageID string
	Protocol          string
	Folder            string
	ReceivedAt        time.Time
}

type FetchMessagesRequest struct {
	Scope           OrderScope
	SinceAt         time.Time
	UntilAt         time.Time
	RequestID       string
	Realtime        bool
	FullHistory     bool
	KnownMessageIDs []string
	OnMessages      func([]FetchedMessage)
	OnReset         func()
}

type FetchMessagesResult struct {
	Messages     []FetchedMessage
	RefreshToken string
}

type InboundMailRequest struct {
	EmailResourceID uint
	ResourceType    domain.ResourceType
	Recipient       string
	EnvelopeFrom    string
	Raw             []byte
	ReceivedAt      time.Time
}

type MailTransportFetchPort interface {
	FetchMicrosoftMessages(ctx context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error)
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	LoadOrderScope(ctx context.Context, orderNo string, userID uint, isAdmin bool) (*OrderScope, error)
	LoadOrderScopeForServiceToken(ctx context.Context, orderNo string) (*OrderScope, error)
	LoadPickupScope(ctx context.Context, token string, email string) (*OrderScope, error)
	ListOrderMessages(ctx context.Context, scope OrderScope, limit int) ([]domain.Message, error)
	FindOrderMessage(ctx context.Context, orderID uint, messageID uint) (*domain.Message, error)
	FindOrderDelivery(ctx context.Context, orderID uint) (*OrderDelivery, error)
	CreateOrderDelivery(ctx context.Context, orderID uint, message domain.Message) error
	ListMatchingScopesByRecipient(ctx context.Context, resourceType domain.ResourceType, emailResourceID uint, recipient string, receivedAt time.Time) ([]OrderScope, error)
	UpsertMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error)
}

type MessageAppendRepository interface {
	AppendMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, int, error)
	ListUnprojectedMessages(ctx context.Context, resourceType domain.ResourceType, emailResourceIDs []uint, limit int) ([]domain.Message, error)
	InsertMessageProjections(ctx context.Context, messages []domain.Message) ([]domain.Message, []uint, error)
}

type FetchQueue interface {
	EnqueuePickupRequest(ctx context.Context, task PickupRequestFetchTask) (bool, error)
}

type PickupFetchStatePort interface {
	Acquire(ctx context.Context, emailResourceID uint, token string, ttl time.Duration) (bool, error)
	Owns(ctx context.Context, emailResourceID uint, token string) (bool, error)
	Extend(ctx context.Context, emailResourceID uint, token string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, emailResourceID uint, token string) error
}

type PickupMessageCachePort interface {
	Load(ctx context.Context, emailResourceID uint) ([]FetchedMessage, bool, error)
	LoadMany(ctx context.Context, emailResourceIDs []uint) (map[uint][]FetchedMessage, error)
	Store(ctx context.Context, emailResourceID uint, messages []FetchedMessage, ttl time.Duration) error
}

type MatchResultPort interface {
	NotifyMatchedCode(ctx context.Context, result MatchResult) error
}

type MatchResult struct {
	OrderNo          string
	VerificationCode string
	MatchedAt        time.Time
}

type FetchTask struct {
	OrderNo         string    `json:"orderNo"`
	EmailResourceID uint      `json:"emailResourceId"`
	Generation      uint64    `json:"generation,omitempty"`
	LeaseToken      string    `json:"leaseToken"`
	SinceAt         time.Time `json:"sinceAt"`
	UntilAt         time.Time `json:"untilAt"`
	RequestedAt     time.Time `json:"requestedAt"`
}

type PickupRequestFetchScope struct {
	// OrderNo keeps queued payloads compatible across rolling deployments.
	OrderNo         string   `json:"orderNo,omitempty"`
	OrderNos        []string `json:"orderNos,omitempty"`
	EmailResourceID uint     `json:"emailResourceId"`
}

type PickupRequestFetchTask struct {
	RequestedAt time.Time                 `json:"requestedAt"`
	ExpiresAt   time.Time                 `json:"expiresAt,omitempty"`
	Scopes      []PickupRequestFetchScope `json:"scopes"`
}

func (task PickupRequestFetchTask) EffectiveExpiresAt() time.Time {
	if !task.ExpiresAt.IsZero() {
		return task.ExpiresAt
	}
	if task.RequestedAt.IsZero() {
		return time.Time{}
	}
	return task.RequestedAt.Add(2 * time.Minute)
}

type PickupRequestFetchOutcome struct {
	Requested int
	Succeeded int
	NoWork    int
	Failed    int
	Expired   int
}

type PickupCredential struct {
	Email string
	Token string
}

type PickupMailResult struct {
	Items []domain.MailContent
	Fetch *domain.FetchState
	Err   error
}

// PickupBatchRead contains the database snapshot needed to render one pickup
// item. Repositories that implement PickupBatchReader can load all items in a
// single transaction; older repository implementations use the single-item
// fallback below.
type PickupBatchRead struct {
	Scope    *OrderScope
	Delivery *OrderDelivery
	Fetch    *domain.FetchState
	Messages []domain.Message
	Err      error
}

type PickupBatchReader interface {
	ReadPickupBatch(ctx context.Context, credentials []PickupCredential, now time.Time, limit int) ([]PickupBatchRead, error)
}

type UseCase struct {
	repo           Repository
	queue          FetchQueue
	transport      MailTransportFetchPort
	matches        MatchResultPort
	credentials    coreapp.MicrosoftCredentialPort
	pickupFetch    PickupFetchStatePort
	pickupMessages PickupMessageCachePort
	now            func() time.Time
}

func (uc *UseCase) SetPickupFetchStatePort(state PickupFetchStatePort) {
	if uc != nil {
		uc.pickupFetch = state
	}
}

func (uc *UseCase) SetPickupMessageCachePort(cache PickupMessageCachePort) {
	if uc != nil {
		uc.pickupMessages = cache
	}
}

func NewUseCase(repo Repository, queue FetchQueue, transport MailTransportFetchPort, matches MatchResultPort) *UseCase {
	return &UseCase{
		repo:      repo,
		queue:     queue,
		transport: transport,
		matches:   matches,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (uc *UseCase) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if uc != nil {
		uc.credentials = credentials
	}
}

func (uc *UseCase) ListOrderMail(ctx context.Context, orderNo string, userID uint, isAdmin bool) ([]domain.MailContent, *domain.FetchState, error) {
	scope, err := uc.repo.LoadOrderScope(ctx, strings.TrimSpace(orderNo), userID, isAdmin)
	if err != nil {
		return nil, nil, err
	}
	items, state, hasDelivery, cacheSatisfied, err := uc.listOrderMailWithPickupCache(ctx, *scope)
	if err != nil {
		return nil, nil, err
	}
	if !cacheSatisfied && shouldScheduleReadFetch(*scope, hasDelivery) {
		uc.scheduleScopeFetch(ctx, *scope)
	}
	return items, state, nil
}

func (uc *UseCase) ListPickupMail(ctx context.Context, token string, email string) ([]domain.MailContent, *domain.FetchState, error) {
	items, state, scope, hasDelivery, cacheSatisfied, err := uc.readPickupMail(ctx, token, email)
	if err != nil {
		return nil, nil, err
	}
	if !cacheSatisfied && shouldScheduleReadFetch(*scope, hasDelivery) {
		uc.scheduleScopeFetch(ctx, *scope)
	}
	return items, state, nil
}

func (uc *UseCase) readPickupMail(ctx context.Context, token string, email string) ([]domain.MailContent, *domain.FetchState, *OrderScope, bool, bool, error) {
	scope, err := uc.repo.LoadPickupScope(ctx, strings.TrimSpace(token), normalizeEmail(email))
	if err != nil {
		return nil, nil, nil, false, false, err
	}
	items, state, hasDelivery, cacheSatisfied, err := uc.listOrderMailWithPickupCache(ctx, *scope)
	if err != nil {
		return nil, nil, nil, false, cacheSatisfied, err
	}
	return items, state, scope, hasDelivery, cacheSatisfied, nil
}

func (uc *UseCase) listOrderMailWithPickupCache(ctx context.Context, scope OrderScope) ([]domain.MailContent, *domain.FetchState, bool, bool, error) {
	items, state, hasDelivery, err := uc.listOrderMailByScope(ctx, scope)
	if err != nil || !shouldScheduleReadFetch(scope, hasDelivery) {
		return items, state, hasDelivery, false, err
	}
	cache := uc.applyPickupMessageCache(ctx, scope.EmailResourceID, []OrderScope{scope})
	if !cache.applied {
		return items, state, hasDelivery, cache.satisfied, nil
	}
	items, state, hasDelivery, err = uc.listOrderMailByScope(ctx, scope)
	return items, state, hasDelivery, cache.satisfied, err
}

func (uc *UseCase) ListPickupMailBatch(ctx context.Context, credentials []PickupCredential) []PickupMailResult {
	if reader, ok := uc.repo.(PickupBatchReader); ok {
		return uc.listPickupMailBatchBulk(ctx, credentials, reader)
	}

	results := make([]PickupMailResult, len(credentials))
	fetchScopes := make([]*OrderScope, len(credentials))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	// ponytail: eight concurrent single-pickup flows bound database pressure;
	// replace them with bulk repository reads only if profiles still require it.
	for i := range credentials {
		sem <- struct{}{}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			items, state, scope, hasDelivery, cacheSatisfied, err := uc.readPickupMail(ctx, credentials[index].Token, credentials[index].Email)
			results[index] = PickupMailResult{Items: items, Fetch: state, Err: err}
			if err == nil && !cacheSatisfied && shouldScheduleReadFetch(*scope, hasDelivery) {
				fetchScopes[index] = scope
			}
		}(i)
	}
	wg.Wait()
	scopes := make([]OrderScope, 0, len(fetchScopes))
	for _, scope := range fetchScopes {
		if scope != nil {
			scopes = append(scopes, *scope)
		}
	}
	uc.scheduleReadFetchBatch(ctx, scopes)
	return results
}

func (uc *UseCase) listPickupMailBatchBulk(
	ctx context.Context,
	credentials []PickupCredential,
	reader PickupBatchReader,
) []PickupMailResult {
	results := make([]PickupMailResult, len(credentials))
	if len(credentials) == 0 {
		return results
	}

	now := uc.now()
	scanLimit := boundedRuntimeInt("message_scan_limit", messageScanLimit, maxMessageScanLimit)
	reads, err := reader.ReadPickupBatch(ctx, credentials, now, scanLimit)
	if err != nil {
		for i := range results {
			results[i].Err = err
		}
		return results
	}
	if len(reads) != len(results) {
		err := fmt.Errorf("pickup batch repository returned %d items for %d credentials", len(reads), len(results))
		for i := range results {
			results[i].Err = err
		}
		return results
	}
	cacheSatisfied, cacheApplied := uc.applyPickupMessageCaches(ctx, reads)
	if cacheApplied {
		refreshed, refreshErr := reader.ReadPickupBatch(ctx, credentials, now, scanLimit)
		if refreshErr != nil {
			slog.Warn("pickup batch cache refresh read failed", "error", refreshErr)
		} else if len(refreshed) == len(reads) {
			reads = refreshed
		} else {
			slog.Warn("pickup batch cache refresh returned unexpected item count", "expected", len(reads), "actual", len(refreshed))
		}
	}

	fetchScopes := make([]OrderScope, 0, len(reads))
	for i := range reads {
		read := reads[i]
		if read.Err != nil {
			results[i].Err = read.Err
			continue
		}
		if read.Scope == nil {
			results[i].Err = domain.ErrPickupCredentialInvalid
			continue
		}
		items, state, hasDelivery, err := uc.listOrderMailFromBatch(read, now)
		if err != nil {
			results[i].Err = err
			continue
		}
		results[i] = PickupMailResult{Items: items, Fetch: state}
		if read.Scope.AllocationType == domain.ResourceTypeMicrosoft && !cacheSatisfied[read.Scope.EmailResourceID] && shouldScheduleReadFetch(*read.Scope, hasDelivery) {
			fetchScopes = append(fetchScopes, *read.Scope)
		}
	}
	uc.scheduleReadFetchBatch(ctx, fetchScopes)
	return results
}

func (uc *UseCase) applyPickupMessageCaches(ctx context.Context, reads []PickupBatchRead) (map[uint]bool, bool) {
	satisfied := make(map[uint]bool)
	applied := false
	scopesByResource := make(map[uint][]OrderScope)
	for _, read := range reads {
		if read.Err != nil || read.Scope == nil || read.Scope.AllocationType != domain.ResourceTypeMicrosoft {
			continue
		}
		hasDelivery := read.Delivery != nil
		if !shouldScheduleReadFetch(*read.Scope, hasDelivery) {
			continue
		}
		resourceID := read.Scope.EmailResourceID
		if _, exists := scopesByResource[resourceID]; !exists {
			scopesByResource[resourceID] = nil
		}
		scopesByResource[resourceID] = append(scopesByResource[resourceID], *read.Scope)
	}
	resourceIDs := make([]uint, 0, len(scopesByResource))
	for resourceID := range scopesByResource {
		resourceIDs = append(resourceIDs, resourceID)
	}
	sort.Slice(resourceIDs, func(i, j int) bool { return resourceIDs[i] < resourceIDs[j] })
	if uc == nil || uc.pickupMessages == nil || len(resourceIDs) == 0 {
		return satisfied, false
	}
	cachedByResource, err := uc.pickupMessages.LoadMany(ctx, resourceIDs)
	if err != nil {
		slog.Warn("pickup message cache batch read failed", "resources", len(resourceIDs), "error", err)
		return satisfied, false
	}
	for _, resourceID := range resourceIDs {
		messages, found := cachedByResource[resourceID]
		result := uc.applyLoadedPickupMessageCache(ctx, resourceID, scopesByResource[resourceID], messages, found)
		if result.satisfied {
			satisfied[resourceID] = true
		}
		applied = applied || result.applied
	}
	return satisfied, applied
}

type pickupMessageCacheMatch struct {
	satisfied bool
	applied   bool
}

func (uc *UseCase) applyPickupMessageCache(ctx context.Context, emailResourceID uint, scopes []OrderScope) pickupMessageCacheMatch {
	if uc == nil || uc.pickupMessages == nil || emailResourceID == 0 {
		return pickupMessageCacheMatch{}
	}
	messages, found, err := uc.pickupMessages.Load(ctx, emailResourceID)
	if err != nil {
		slog.Warn("pickup message cache read failed", "resource_id", emailResourceID, "error", err)
		return pickupMessageCacheMatch{}
	}
	if !found {
		return pickupMessageCacheMatch{}
	}
	return uc.applyLoadedPickupMessageCache(ctx, emailResourceID, scopes, messages, true)
}

func (uc *UseCase) applyLoadedPickupMessageCache(ctx context.Context, emailResourceID uint, scopes []OrderScope, messages []FetchedMessage, found bool) pickupMessageCacheMatch {
	if !found {
		return pickupMessageCacheMatch{}
	}
	messages, allMatched := cachedMessagesMatchingScopes(messages, emailResourceID, scopes)
	if len(messages) == 0 {
		return pickupMessageCacheMatch{satisfied: allMatched}
	}
	if _, _, _, err := uc.ingestFetchedMessagesForResourcesWithFence(
		ctx, messages, domain.ResourceTypeMicrosoft, []uint{emailResourceID}, nil,
	); err != nil {
		slog.Warn("pickup cached message matching failed", "resource_id", emailResourceID, "error", err)
		return pickupMessageCacheMatch{}
	}
	platform.AddWorkUnits("mailmatch_fetch", "all", "cache_match", 1)
	return pickupMessageCacheMatch{satisfied: allMatched, applied: true}
}

func cachedMessagesMatchingScopes(messages []FetchedMessage, emailResourceID uint, scopes []OrderScope) ([]FetchedMessage, bool) {
	if len(scopes) == 0 {
		return nil, true
	}
	matched := make([]FetchedMessage, 0, len(messages))
	matchedScopes := make([]bool, len(scopes))
	for _, message := range messages {
		if message.EmailResourceID != emailResourceID || message.ResourceType != domain.ResourceTypeMicrosoft {
			continue
		}
		messageMatched := false
		for index, scope := range scopes {
			if scope.EmailResourceID != emailResourceID || scope.AllocationType != domain.ResourceTypeMicrosoft {
				continue
			}
			if ok, _, _ := matchAndExtractAnyRecipient(message, scope); ok {
				matchedScopes[index] = true
				messageMatched = true
			}
		}
		if messageMatched {
			matched = append(matched, message)
		}
	}
	allMatched := true
	for _, scopeMatched := range matchedScopes {
		allMatched = allMatched && scopeMatched
	}
	return matched, allMatched
}

func (uc *UseCase) scheduleReadFetchBatch(ctx context.Context, scopes []OrderScope) {
	uc.scheduleScopeFetches(ctx, scopes)
}

func (uc *UseCase) listOrderMailFromBatch(
	read PickupBatchRead,
	now time.Time,
) ([]domain.MailContent, *domain.FetchState, bool, error) {
	scope := *read.Scope
	if !scopeReadable(scope, func() time.Time { return now }) {
		return nil, nil, false, domain.ErrOrderUnavailable
	}
	limit := boundedRuntimeInt("purchase_read_limit", purchaseReadLimit, maxPurchaseReadLimit)
	if scope.ServiceMode == "code" && read.Delivery == nil {
		limit = boundedRuntimeInt("code_read_limit", codeReadLimit, maxCodeReadLimit)
	}
	messages := filterPickupMessages(scope, read.Messages, now)
	scanLimit := boundedRuntimeInt("message_scan_limit", messageScanLimit, maxMessageScanLimit)
	if len(messages) > scanLimit {
		messages = messages[:scanLimit]
	}
	if len(messages) > limit {
		messages = messages[:limit]
	}
	items := make([]domain.MailContent, len(messages))
	for i := range messages {
		items[i] = mailContentFromMessage(messages[i])
	}
	if read.Delivery != nil && read.Delivery.Message != nil {
		items = prependDeliveryMail(items, mailContentFromMessage(*read.Delivery.Message), limit)
	}
	return items, nil, read.Delivery != nil, nil
}

func filterPickupMessages(scope OrderScope, messages []domain.Message, now time.Time) []domain.Message {
	start := now.Add(-30 * 24 * time.Hour)
	if scope.AllocationType == domain.ResourceTypeMicrosoft {
		start = now.Add(-3 * 24 * time.Hour)
	}
	if scope.ReceiveStartedAt != nil {
		serviceStart := scope.ReceiveStartedAt.Add(-boundedRuntimeDuration("read_window_skew_minutes", readWindowSkew, time.Minute, maxReadWindowSkew))
		if serviceStart.After(start) {
			start = serviceStart
		}
	}
	end := now
	if scope.ServiceMode == "code" && scope.ReceiveUntil != nil {
		end = *scope.ReceiveUntil
	}
	filtered := make([]domain.Message, 0, len(messages))
	for _, message := range messages {
		if message.ReceivedAt.Before(start) || message.ReceivedAt.After(end) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func (uc *UseCase) GetPickupMessage(ctx context.Context, token string, email string, messageID uint) (*domain.MailContent, error) {
	if messageID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	scope, err := uc.repo.LoadPickupScope(ctx, strings.TrimSpace(token), normalizeEmail(email))
	if err != nil {
		return nil, err
	}
	if !scopeReadable(*scope, uc.now) {
		return nil, domain.ErrOrderUnavailable
	}
	message, err := uc.repo.FindOrderMessage(ctx, scope.OrderID, messageID)
	if err != nil {
		return nil, err
	}
	content := mailContentFromMessage(*message)
	return &content, nil
}

func shouldScheduleReadFetch(scope OrderScope, hasDelivery bool) bool {
	return scope.ServiceMode != "code" || !hasDelivery
}

func (uc *UseCase) listOrderMailByScope(ctx context.Context, scope OrderScope) ([]domain.MailContent, *domain.FetchState, bool, error) {
	if !scopeReadable(scope, uc.now) {
		return nil, nil, false, domain.ErrOrderUnavailable
	}
	delivery, err := uc.repo.FindOrderDelivery(ctx, scope.OrderID)
	if err != nil {
		return nil, nil, false, err
	}
	limit := boundedRuntimeInt("purchase_read_limit", purchaseReadLimit, maxPurchaseReadLimit)
	if scope.ServiceMode == "code" && delivery == nil {
		limit = boundedRuntimeInt("code_read_limit", codeReadLimit, maxCodeReadLimit)
	}
	messages, err := uc.repo.ListOrderMessages(ctx, scope, boundedRuntimeInt("message_scan_limit", messageScanLimit, maxMessageScanLimit))
	if err != nil {
		return nil, nil, false, err
	}
	if len(messages) > limit {
		messages = messages[:limit]
	}
	items := make([]domain.MailContent, len(messages))
	for i := range messages {
		items[i] = mailContentFromMessage(messages[i])
	}
	if delivery != nil && delivery.Message != nil {
		items = prependDeliveryMail(items, mailContentFromMessage(*delivery.Message), limit)
	}
	return items, nil, delivery != nil, nil
}

func (uc *UseCase) saveOrderDelivery(ctx context.Context, scope OrderScope, message domain.Message) error {
	if scope.OrderID == 0 || message.ID == 0 {
		return nil
	}
	if scope.ServiceMode == "code" && strings.TrimSpace(message.VerificationCode) == "" {
		return nil
	}
	return uc.repo.CreateOrderDelivery(ctx, scope.OrderID, message)
}

func mailContentFromMessage(message domain.Message) domain.MailContent {
	return domain.MailContent{
		ID:               message.ID,
		Sender:           message.Sender,
		Recipient:        message.Recipient,
		ReceivedAt:       message.ReceivedAt,
		Subject:          message.Subject,
		Body:             message.RawBody,
		BodyPreview:      message.BodyPreview,
		VerificationCode: message.VerificationCode,
	}
}

func prependDeliveryMail(items []domain.MailContent, delivery domain.MailContent, limit int) []domain.MailContent {
	for i := range items {
		if sameMailContent(items[i], delivery) {
			return items
		}
	}
	out := make([]domain.MailContent, 0, len(items)+1)
	out = append(out, delivery)
	out = append(out, items...)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func sameMailContent(a, b domain.MailContent) bool {
	return strings.EqualFold(strings.TrimSpace(a.Recipient), strings.TrimSpace(b.Recipient)) &&
		strings.TrimSpace(a.Sender) == strings.TrimSpace(b.Sender) &&
		strings.TrimSpace(a.Subject) == strings.TrimSpace(b.Subject) &&
		strings.TrimSpace(a.VerificationCode) == strings.TrimSpace(b.VerificationCode) &&
		a.ReceivedAt.Equal(b.ReceivedAt)
}

func (uc *UseCase) scheduleScopeFetch(ctx context.Context, scope OrderScope) {
	uc.scheduleScopeFetches(ctx, []OrderScope{scope})
}

func (uc *UseCase) scheduleScopeFetches(ctx context.Context, scopes []OrderScope) {
	if uc == nil || uc.queue == nil || len(scopes) == 0 {
		return
	}
	positions := make(map[uint]int, len(scopes))
	requestScopes := make([]PickupRequestFetchScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.AllocationType != domain.ResourceTypeMicrosoft || !scopeFetchable(scope, uc.now) {
			continue
		}
		orderNo := strings.TrimSpace(scope.OrderNo)
		if orderNo == "" {
			continue
		}
		if index, exists := positions[scope.EmailResourceID]; exists {
			requestScopes[index].OrderNos = appendUniqueString(requestScopes[index].OrderNos, orderNo)
			continue
		}
		positions[scope.EmailResourceID] = len(requestScopes)
		requestScopes = append(requestScopes, PickupRequestFetchScope{
			OrderNo: orderNo, OrderNos: []string{orderNo}, EmailResourceID: scope.EmailResourceID,
		})
	}
	if len(requestScopes) == 0 {
		return
	}
	sort.Slice(requestScopes, func(i, j int) bool {
		return requestScopes[i].EmailResourceID < requestScopes[j].EmailResourceID
	})
	task := PickupRequestFetchTask{RequestedAt: uc.now(), Scopes: requestScopes}
	platform.RecordTaskEvent("pickup_request_fetch", "requested")
	accepted, err := uc.queue.EnqueuePickupRequest(ctx, task)
	if err != nil {
		platform.RecordTaskEvent("pickup_request_fetch", "enqueue_failed")
		slog.Warn("pickup request fetch scheduling failed", "resources", len(requestScopes), "error", err)
		return
	}
	if !accepted {
		platform.RecordTaskEvent("pickup_request_fetch", "deduplicated")
		slog.Debug("pickup request fetch already scheduled", "resources", len(requestScopes))
		return
	}
	platform.RecordTaskEvent("pickup_request_fetch", "enqueued")
}

func (uc *UseCase) ProcessPickupRequestFetch(ctx context.Context, task PickupRequestFetchTask) error {
	_, err := uc.ProcessPickupRequestFetchWithOutcome(ctx, task)
	return err
}

func (uc *UseCase) ProcessPickupRequestFetchWithOutcome(ctx context.Context, task PickupRequestFetchTask) (outcome PickupRequestFetchOutcome, resultErr error) {
	if uc == nil || uc.pickupFetch == nil || task.RequestedAt.IsZero() || len(task.Scopes) == 0 {
		return outcome, domain.ErrInvalidRequest
	}
	outcome.Requested = len(task.Scopes)
	defer func() {
		platform.AddWorkUnits("pickup_fetch", "all", "requested", outcome.Requested)
		platform.AddWorkUnits("pickup_fetch", "all", "succeeded", outcome.Succeeded)
		platform.AddWorkUnits("pickup_fetch", "all", "no_work", outcome.NoWork)
		platform.AddWorkUnits("pickup_fetch", "all", "system_failed", outcome.Failed)
		platform.AddWorkUnits("pickup_fetch", "all", "expired", outcome.Expired)
	}()
	timing := configuredPickupFetchTiming()
	// Asynq owns the hard timeout. Stop scope work slightly earlier
	// so partial outcomes and lease cleanup can finish before that outer timer.
	deadline := task.RequestedAt.Add(timing.reserveTTL - pickupFetchReturnSlack)
	if outerDeadline := task.EffectiveExpiresAt().Add(-pickupFetchReturnSlack); outerDeadline.Before(deadline) {
		deadline = outerDeadline
	}
	if !uc.now().Before(deadline) {
		outcome.Expired = outcome.Requested
		return outcome, nil
	}
	taskCtx, cancel := context.WithTimeout(ctx, deadline.Sub(uc.now()))
	defer cancel()
	for _, scope := range task.Scopes {
		if taskCtx.Err() != nil || !uc.now().Before(deadline) {
			break
		}
		if scope.EmailResourceID == 0 || len(pickupRequestOrderNos(scope)) == 0 {
			err := fmt.Errorf("invalid pickup request fetch scope: %w", domain.ErrInvalidRequest)
			slog.Warn("pickup request fetch scope is invalid", "resource_id", scope.EmailResourceID)
			outcome.Failed++
			resultErr = errors.Join(resultErr, err)
			continue
		}
		executed, err := uc.processPickupRequestScope(taskCtx, task.RequestedAt, scope, timing)
		if err != nil {
			outcome.Failed++
			resultErr = errors.Join(resultErr, err)
			slog.Warn(
				"pickup request fetch scope failed",
				"resource_id", scope.EmailResourceID,
				"order_nos", pickupRequestOrderNos(scope),
				"error", err,
			)
			continue
		}
		if executed {
			outcome.Succeeded++
		} else {
			outcome.NoWork++
		}
	}
	outcome.Expired = outcome.Requested - outcome.Succeeded - outcome.NoWork - outcome.Failed
	return outcome, resultErr
}

func (uc *UseCase) processPickupRequestScope(ctx context.Context, requestedAt time.Time, scope PickupRequestFetchScope, timing pickupFetchTiming) (bool, error) {
	orderNo, err := uc.selectPickupRequestOrder(ctx, scope)
	if err != nil || orderNo == "" {
		return false, err
	}
	token, err := newPickupFetchToken()
	if err != nil {
		return false, err
	}
	ttl := requestedAt.Add(timing.reserveTTL).Sub(uc.now())
	if ttl <= 0 {
		return false, nil
	}
	acquired, err := uc.pickupFetch.Acquire(ctx, scope.EmailResourceID, token, min(ttl, timing.reserveTTL))
	if err != nil || !acquired {
		return false, err
	}
	err = uc.processFetch(ctx, FetchTask{
		OrderNo: orderNo, EmailResourceID: scope.EmailResourceID,
		LeaseToken: token, RequestedAt: requestedAt,
	}, timing)
	return true, err
}

func (uc *UseCase) selectPickupRequestOrder(ctx context.Context, request PickupRequestFetchScope) (string, error) {
	for _, orderNo := range pickupRequestOrderNos(request) {
		scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, orderNo)
		if errors.Is(err, domain.ErrOrderNotFound) || errors.Is(err, domain.ErrOrderUnavailable) || errors.Is(err, domain.ErrOrderForbidden) {
			continue
		}
		if err != nil {
			return "", err
		}
		if scope == nil || scope.EmailResourceID != request.EmailResourceID ||
			scope.AllocationType != domain.ResourceTypeMicrosoft || !scopeFetchable(*scope, uc.now) {
			continue
		}
		return strings.TrimSpace(scope.OrderNo), nil
	}
	return "", nil
}

func pickupRequestOrderNos(scope PickupRequestFetchScope) []string {
	result := make([]string, 0, len(scope.OrderNos)+1)
	result = appendUniqueString(result, strings.TrimSpace(scope.OrderNo))
	for _, orderNo := range scope.OrderNos {
		result = appendUniqueString(result, strings.TrimSpace(orderNo))
	}
	return result
}

func appendUniqueString(items []string, value string) []string {
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func newPickupFetchToken() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create pickup fetch lease token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (uc *UseCase) ProcessFetch(ctx context.Context, task FetchTask) (resultErr error) {
	return uc.processFetch(ctx, task, configuredPickupFetchTiming())
}

func (uc *UseCase) processFetch(ctx context.Context, task FetchTask, timing pickupFetchTiming) (resultErr error) {
	if task.Generation > 0 && strings.TrimSpace(task.OrderNo) == "" && strings.TrimSpace(task.LeaseToken) == "" {
		return nil
	}
	if uc == nil || uc.pickupFetch == nil || task.EmailResourceID == 0 || strings.TrimSpace(task.OrderNo) == "" || strings.TrimSpace(task.LeaseToken) == "" {
		return domain.ErrInvalidRequest
	}
	leaseTTL := pickupFetchTTL(task.RequestedAt, uc.now(), timing)
	if leaseTTL <= 0 {
		return uc.pickupFetch.Release(context.WithoutCancel(ctx), task.EmailResourceID, task.LeaseToken)
	}
	extended, err := uc.pickupFetch.Extend(ctx, task.EmailResourceID, task.LeaseToken, leaseTTL)
	if err != nil {
		return errors.Join(err, uc.pickupFetch.Release(context.WithoutCancel(ctx), task.EmailResourceID, task.LeaseToken))
	}
	if !extended {
		return nil
	}
	stopLease := uc.keepPickupFetchLease(ctx, task, timing)
	defer func() {
		stopLease()
		resultErr = errors.Join(resultErr, uc.pickupFetch.Release(
			context.WithoutCancel(ctx), task.EmailResourceID, task.LeaseToken,
		))
	}()
	serviceStarted := time.Now()
	defer platform.ObserveTaskService("mailmatch_fetch", serviceStarted)
	scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, task.OrderNo)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) || errors.Is(err, domain.ErrOrderUnavailable) || errors.Is(err, domain.ErrOrderForbidden) {
			return nil
		}
		return err
	}
	if scope.EmailResourceID != task.EmailResourceID || !scopeFetchable(*scope, uc.now) {
		return nil
	}
	var cachedMessages []FetchedMessage
	if uc.pickupMessages != nil {
		messages, found, cacheErr := uc.pickupMessages.Load(ctx, task.EmailResourceID)
		if cacheErr != nil {
			slog.Warn("pickup message cache read failed", "resource_id", task.EmailResourceID, "error", cacheErr)
		} else if found {
			cachedMessages = pickupMessagesForResource(messages, task.EmailResourceID)
		}
	}
	job := domain.FetchJob{SinceAt: &task.SinceAt, UntilAt: &task.UntilAt}
	fetched, fetchErr := uc.fetchMessages(ctx, *scope, job, pickupMessageIdentityKeys(cachedMessages))
	if fetchErr != nil {
		return fetchErr
	}
	if fetched == nil {
		return domain.ErrMailServiceUnavailable
	}
	leaseTTL = pickupFetchTTL(task.RequestedAt, uc.now(), timing)
	if leaseTTL <= 0 {
		return nil
	}
	current, err := uc.pickupFetch.Extend(ctx, task.EmailResourceID, task.LeaseToken, leaseTTL)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	if strings.TrimSpace(fetched.RefreshToken) != "" &&
		strings.TrimSpace(fetched.RefreshToken) != strings.TrimSpace(scope.MicrosoftRT) &&
		scope.AllocationType == domain.ResourceTypeMicrosoft {
		if uc.credentials == nil {
			return errors.New("microsoft credential service is unavailable")
		}
		err := uc.credentials.ApplyMicrosoftFetchRefreshToken(ctx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID:                 scope.EmailResourceID,
			ExpectedCredentialRevision: scope.CredentialRevision,
			RefreshToken:               fetched.RefreshToken,
			Now:                        uc.now(),
		})
		if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) || errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	stored := 0
	matched := 0
	if len(fetched.Messages) == 0 {
		current, err := uc.pickupFetch.Owns(ctx, task.EmailResourceID, task.LeaseToken)
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
	} else {
		var lastReceivedAt *time.Time
		stored, matched, lastReceivedAt, err = uc.ingestFetchedMessagesForResourcesWithFence(ctx, fetched.Messages, domain.ResourceTypeMicrosoft, []uint{task.EmailResourceID}, func(txCtx context.Context) error {
			current, err := uc.pickupFetch.Owns(txCtx, task.EmailResourceID, task.LeaseToken)
			if err != nil {
				return err
			}
			if !current {
				return domain.ErrFetchJobConflict
			}
			return nil
		})
		if err != nil {
			return err
		}
		_ = lastReceivedAt
	}
	platform.AddWorkUnits("mailmatch_fetch", "all", "fetched", len(fetched.Messages))
	platform.AddWorkUnits("mailmatch_fetch", "all", "stored", stored)
	platform.AddWorkUnits("mailmatch_fetch", "all", "matched", matched)
	if uc.pickupMessages != nil {
		mergedMessages := mergePickupMessages(fetched.Messages, cachedMessages)
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pickupMessageCacheIO)
		err := uc.pickupMessages.Store(cacheCtx, task.EmailResourceID, mergedMessages, boundedRuntimeDuration("pickup_message_cache_ttl_seconds", pickupMessageCacheTTL, time.Second, maxPickupMessageCacheTTL))
		cancel()
		if err != nil {
			slog.Warn("pickup message cache write failed", "resource_id", task.EmailResourceID, "error", err)
		} else {
			platform.AddWorkUnits("mailmatch_fetch", "all", "cache_refresh", 1)
		}
	}
	return nil
}

func pickupMessagesForResource(messages []FetchedMessage, emailResourceID uint) []FetchedMessage {
	filtered := make([]FetchedMessage, 0, len(messages))
	for _, message := range messages {
		if message.EmailResourceID == emailResourceID && message.ResourceType == domain.ResourceTypeMicrosoft {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func pickupMessageIdentityKeys(messages []FetchedMessage) []string {
	keys := make([]string, 0, len(messages)*2)
	seen := make(map[string]struct{}, len(messages)*2)
	for _, message := range messages {
		messageID := strings.ToLower(strings.Trim(strings.TrimSpace(message.MessageIDHeader), "<>"))
		if messageID != "" {
			keys = appendUniqueIdentityKey(keys, seen, "internet:"+messageID)
		}
		providerID := strings.ToLower(strings.TrimSpace(message.ProviderMessageID))
		if providerID != "" {
			keys = appendUniqueIdentityKey(keys, seen, strings.Join([]string{
				"provider",
				strings.ToLower(strings.TrimSpace(message.Protocol)),
				strings.ToLower(strings.TrimSpace(message.Folder)),
				providerID,
			}, ":"))
		}
	}
	return keys
}

func appendUniqueIdentityKey(keys []string, seen map[string]struct{}, key string) []string {
	if key == "" {
		return keys
	}
	if _, exists := seen[key]; exists {
		return keys
	}
	seen[key] = struct{}{}
	return append(keys, key)
}

func mergePickupMessages(fetched, cached []FetchedMessage) []FetchedMessage {
	merged := make([]FetchedMessage, 0, len(fetched)+len(cached))
	seen := make(map[string]struct{}, len(fetched)+len(cached))
	for _, messages := range [][]FetchedMessage{fetched, cached} {
		for _, message := range messages {
			key := fmt.Sprintf("%d:%s", message.EmailResourceID, messageDedupeKey(message))
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, message)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ReceivedAt.After(merged[j].ReceivedAt)
	})
	limit := boundedRuntimeInt("pickup_message_cache_limit", pickupMessageCacheLimit, maxPickupMessageCacheLimit)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func (uc *UseCase) keepPickupFetchLease(ctx context.Context, task FetchTask, timing pickupFetchTiming) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(timing.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				leaseTTL := pickupFetchTTL(task.RequestedAt, uc.now(), timing)
				if leaseTTL <= 0 {
					return
				}
				extended, err := uc.pickupFetch.Extend(heartbeatCtx, task.EmailResourceID, task.LeaseToken, leaseTTL)
				if err != nil {
					continue
				}
				if !extended {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

type pickupFetchTiming struct {
	reserveTTL time.Duration
	leaseTTL   time.Duration
	heartbeat  time.Duration
}

func configuredPickupFetchTiming() pickupFetchTiming {
	settings := runtimeconfig.Snapshot()
	timing := pickupFetchTiming{
		reserveTTL: min(settings.Duration("pickup_fetch_reserve_ttl_minutes", pickupFetchReserveTTL, time.Minute, 1), maxPickupFetchReserveTTL),
		leaseTTL:   min(settings.Duration("pickup_fetch_lease_ttl_minutes", pickupFetchLeaseTTL, time.Minute, 1), maxPickupFetchLeaseTTL),
		heartbeat:  min(settings.Duration("pickup_fetch_heartbeat_seconds", pickupFetchHeartbeat, time.Second, 1), maxPickupFetchHeartbeat),
	}
	if timing.heartbeat >= timing.leaseTTL {
		timing.heartbeat = timing.leaseTTL / 2
	}
	if timing.heartbeat <= 0 {
		timing.heartbeat = time.Second
	}
	return timing
}

func pickupFetchTTL(requestedAt, now time.Time, timing pickupFetchTiming) time.Duration {
	remaining := requestedAt.Add(timing.reserveTTL).Sub(now)
	if remaining <= 0 {
		return 0
	}
	return min(remaining, timing.leaseTTL)
}

func (uc *UseCase) IngestInboundMail(ctx context.Context, req InboundMailRequest) error {
	if req.EmailResourceID == 0 || strings.TrimSpace(req.Recipient) == "" || len(req.Raw) == 0 {
		return domain.ErrInvalidRequest
	}
	if req.ResourceType == "" {
		req.ResourceType = domain.ResourceTypeDomain
	}
	if req.ResourceType != domain.ResourceTypeDomain {
		return nil
	}
	_, _, _, err := uc.ingestFetchedMessagesForResourcesWithFence(
		ctx,
		[]FetchedMessage{inboundFetchedMessage(req)},
		domain.ResourceTypeDomain,
		[]uint{req.EmailResourceID},
		nil,
	)
	return err
}

type mailIngestError struct {
	safe string
	err  error
}

type matchedDelivery struct {
	scope   OrderScope
	message domain.Message
}

func (e *mailIngestError) Error() string { return e.err.Error() }
func (e *mailIngestError) Unwrap() error { return e.err }

func (uc *UseCase) ingestFetchedMessages(ctx context.Context, fetched []FetchedMessage) (int, int, *time.Time, error) {
	return uc.ingestFetchedMessagesWithFence(ctx, fetched, nil)
}

func (uc *UseCase) ingestFetchedMessagesWithFence(
	ctx context.Context,
	fetched []FetchedMessage,
	fence func(context.Context) error,
) (int, int, *time.Time, error) {
	return uc.ingestFetchedMessagesForResourcesWithFence(ctx, fetched, "", nil, fence)
}

func (uc *UseCase) ingestFetchedMessagesForResourcesWithFence(
	ctx context.Context,
	fetched []FetchedMessage,
	replayResourceType domain.ResourceType,
	replayResourceIDs []uint,
	fence func(context.Context) error,
) (int, int, *time.Time, error) {
	messages := make([]domain.Message, 0, len(fetched))
	matchDeliveries := make([]matchedDelivery, 0)
	for _, item := range fetched {
		message, matchedScope, err := uc.fetchedMessageToDomain(ctx, item)
		if err != nil {
			return 0, 0, latestReceivedAt(messages), &mailIngestError{safe: "Mail message matching failed.", err: err}
		}
		messages = append(messages, message)
		if matchedScope != nil && (strings.TrimSpace(message.VerificationCode) != "" || matchedScope.ServiceMode == "purchase") {
			matchDeliveries = append(matchDeliveries, matchedDelivery{scope: *matchedScope, message: message})
		}
	}
	lastReceivedAt := latestReceivedAt(messages)
	storedDeliveries := make([]matchedDelivery, 0, len(matchDeliveries))
	stored := 0
	matched := 0
	newlyMatched := make(map[uint]struct{})
	legacyMatched := make(map[uint]struct{})
	appendRepo, appendOnly := uc.repo.(MessageAppendRepository)
	if appendOnly {
		if fence != nil {
			if err := fence(ctx); err != nil {
				return 0, 0, lastReceivedAt, err
			}
		}
		storedMessages, inserted, err := appendRepo.AppendMessages(ctx, messages)
		if err != nil {
			return 0, 0, lastReceivedAt, &mailIngestError{safe: "Mail message storage failed.", err: err}
		}
		pendingMessages, err := listUnprojectedMessages(
			ctx, appendRepo, replayResourceType, replayResourceIDs, storedMessages,
		)
		if err != nil {
			return 0, 0, lastReceivedAt, &mailIngestError{safe: "Mail message recovery failed.", err: err}
		}
		storedMessages = appendUniqueMessages(storedMessages, pendingMessages)
		decisions := append([]domain.Message(nil), messages...)
		knownDecisions := make(map[string]struct{}, len(decisions))
		for _, decision := range decisions {
			knownDecisions[mailMessageIdentity(decision)] = struct{}{}
		}
		for _, fact := range pendingMessages {
			if _, exists := knownDecisions[mailMessageIdentity(fact)]; exists {
				continue
			}
			decision, matchedScope, matchErr := uc.fetchedMessageToDomain(ctx, fetchedMessageFromDomain(fact))
			if matchErr != nil {
				return 0, 0, lastReceivedAt, &mailIngestError{safe: "Mail message recovery failed.", err: matchErr}
			}
			decisions = append(decisions, decision)
			knownDecisions[mailMessageIdentity(decision)] = struct{}{}
			if matchedScope != nil && (strings.TrimSpace(decision.VerificationCode) != "" || matchedScope.ServiceMode == "purchase") {
				matchDeliveries = append(matchDeliveries, matchedDelivery{scope: *matchedScope, message: decision})
			}
		}
		for _, fact := range storedMessages {
			if hasLegacyMatchedDecision(fact) {
				legacyMatched[fact.ID] = struct{}{}
			}
		}
		messages, err = mergeStoredMessageDecisions(storedMessages, decisions)
		if err != nil {
			return 0, 0, lastReceivedAt, &mailIngestError{safe: "Mail message resolution failed.", err: err}
		}
		stored = inserted
	}
	matchDeliveries = earliestOrderDeliveries(matchDeliveries)
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if fence != nil {
			if err := fence(txCtx); err != nil {
				return err
			}
		}
		if !appendOnly {
			storedMessages, err := uc.repo.UpsertMessages(txCtx, messages)
			if err != nil {
				return &mailIngestError{safe: "Mail message storage failed.", err: err}
			}
			messages = storedMessages
			stored = len(storedMessages)
			matched = countMatched(storedMessages)
		} else {
			projectedMessages, newlyMatchedIDs, err := appendRepo.InsertMessageProjections(txCtx, messages)
			if err != nil {
				return &mailIngestError{safe: "Mail message projection failed.", err: err}
			}
			messages = projectedMessages
			matched = 0
			for _, id := range newlyMatchedIDs {
				if _, existedAsLegacyMatch := legacyMatched[id]; existedAsLegacyMatch {
					continue
				}
				newlyMatched[id] = struct{}{}
				matched++
			}
		}
		storedByIdentity := make(map[string]domain.Message, len(messages))
		for _, message := range messages {
			storedByIdentity[mailMessageIdentity(message)] = message
		}
		for _, delivery := range matchDeliveries {
			message, ok := storedByIdentity[mailMessageIdentity(delivery.message)]
			if !ok {
				return &mailIngestError{safe: "Mail delivery message resolution failed.", err: domain.ErrMessageNotFound}
			}
			if message.MatchedOrderID == nil || *message.MatchedOrderID != delivery.scope.OrderID {
				continue
			}
			if err := uc.saveOrderDelivery(txCtx, delivery.scope, message); err != nil {
				return &mailIngestError{safe: "Mail delivery storage failed.", err: err}
			}
			delivery.message = message
			storedDeliveries = append(storedDeliveries, delivery)
		}
		return nil
	})
	if err != nil {
		return 0, 0, lastReceivedAt, err
	}
	for _, message := range messages {
		if message.Status == domain.MessageStatusMatched && (!appendOnly || containsMessageID(newlyMatched, message.ID)) {
			platform.ObserveMailVisible(string(message.ResourceType), message.ReceivedAt)
		}
	}
	for _, delivery := range storedDeliveries {
		// Notify from the immutable head, not from a later matching pull.
		canonical, findErr := uc.repo.FindOrderDelivery(ctx, delivery.scope.OrderID)
		if findErr != nil {
			return stored, matched, lastReceivedAt, &mailIngestError{safe: "Mail delivery lookup failed.", err: findErr}
		}
		if canonical != nil {
			delivery.message = domain.Message{ReceivedAt: canonical.ReceivedAt}
			if canonical.Message != nil {
				delivery.message = *canonical.Message
			}
		}
		result := MatchResult{
			OrderNo:          delivery.scope.OrderNo,
			VerificationCode: delivery.message.VerificationCode,
			MatchedAt:        delivery.message.ReceivedAt,
		}
		if err := uc.matches.NotifyMatchedCode(ctx, result); err != nil {
			return stored, matched, lastReceivedAt, &mailIngestError{safe: "Mail match result notification failed.", err: err}
		}
	}
	return stored, matched, lastReceivedAt, nil
}

func mergeResourceIDs(first, second []uint) []uint {
	ids := make([]uint, 0, len(first)+len(second))
	seen := make(map[uint]struct{}, len(first)+len(second))
	for _, resourceID := range append(append([]uint(nil), first...), second...) {
		if resourceID == 0 {
			continue
		}
		if _, exists := seen[resourceID]; exists {
			continue
		}
		seen[resourceID] = struct{}{}
		ids = append(ids, resourceID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func listUnprojectedMessages(
	ctx context.Context,
	repo MessageAppendRepository,
	replayResourceType domain.ResourceType,
	replayResourceIDs []uint,
	storedMessages []domain.Message,
) ([]domain.Message, error) {
	resourceIDsByType := map[domain.ResourceType][]uint{}
	if replayResourceType == domain.ResourceTypeMicrosoft || replayResourceType == domain.ResourceTypeDomain {
		resourceIDsByType[replayResourceType] = append(resourceIDsByType[replayResourceType], replayResourceIDs...)
	}
	for _, message := range storedMessages {
		if message.ResourceType != domain.ResourceTypeMicrosoft && message.ResourceType != domain.ResourceTypeDomain {
			continue
		}
		resourceIDsByType[message.ResourceType] = append(resourceIDsByType[message.ResourceType], message.EmailResourceID)
	}

	limit := boundedRuntimeInt("projection_replay_limit", projectionReplayLimit, maxProjectionReplayLimit)
	pending := make([]domain.Message, 0, limit)
	for _, resourceType := range []domain.ResourceType{domain.ResourceTypeMicrosoft, domain.ResourceTypeDomain} {
		resourceIDs := mergeResourceIDs(resourceIDsByType[resourceType], nil)
		if len(resourceIDs) == 0 {
			continue
		}
		messages, err := repo.ListUnprojectedMessages(ctx, resourceType, resourceIDs, limit)
		if err != nil {
			return nil, err
		}
		pending = appendUniqueMessages(pending, messages)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].ReceivedAt.Equal(pending[j].ReceivedAt) {
			return pending[i].ID > pending[j].ID
		}
		return pending[i].ReceivedAt.After(pending[j].ReceivedAt)
	})
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func appendUniqueMessages(messages, extra []domain.Message) []domain.Message {
	seen := make(map[string]struct{}, len(messages)+len(extra))
	for _, message := range messages {
		seen[mailMessageIdentity(message)] = struct{}{}
	}
	for _, message := range extra {
		identity := mailMessageIdentity(message)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		messages = append(messages, message)
	}
	return messages
}

func containsMessageID(ids map[uint]struct{}, id uint) bool {
	_, ok := ids[id]
	return ok
}

func mergeStoredMessageDecisions(stored, decisions []domain.Message) ([]domain.Message, error) {
	decisionsByIdentity := make(map[string]domain.Message, len(decisions))
	for _, decision := range decisions {
		decisionsByIdentity[mailMessageIdentity(decision)] = decision
	}
	merged := make([]domain.Message, len(stored))
	for i, fact := range stored {
		decision, ok := decisionsByIdentity[mailMessageIdentity(fact)]
		if !ok {
			return nil, domain.ErrMessageNotFound
		}
		// Existing legacy rows may already contain the authoritative first
		// decision. New append-only facts contain none of these derived fields.
		if !hasLegacyMatchedDecision(fact) {
			fact.MatchedOrderID = decision.MatchedOrderID
			fact.Status = decision.Status
			fact.VerificationCode = decision.VerificationCode
			fact.MatchDiagnostic = decision.MatchDiagnostic
		}
		merged[i] = fact
	}
	return merged, nil
}

func hasLegacyMatchedDecision(message domain.Message) bool {
	return message.MatchedOrderID != nil || message.Status == domain.MessageStatusMatched
}

func mailMessageIdentity(message domain.Message) string {
	return fmt.Sprintf("%d:%s", message.EmailResourceID, strings.TrimSpace(message.DedupeKey))
}

func earliestOrderDeliveries(deliveries []matchedDelivery) []matchedDelivery {
	if len(deliveries) < 2 {
		return deliveries
	}
	earliestByOrder := make(map[uint]matchedDelivery, len(deliveries))
	for _, delivery := range deliveries {
		current, exists := earliestByOrder[delivery.scope.OrderID]
		if !exists || delivery.message.ReceivedAt.Before(current.message.ReceivedAt) ||
			(delivery.message.ReceivedAt.Equal(current.message.ReceivedAt) && delivery.message.DedupeKey < current.message.DedupeKey) {
			earliestByOrder[delivery.scope.OrderID] = delivery
		}
	}
	result := make([]matchedDelivery, 0, len(earliestByOrder))
	for _, delivery := range earliestByOrder {
		result = append(result, delivery)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].scope.OrderID < result[j].scope.OrderID
	})
	return result
}

func (uc *UseCase) fetchMessages(ctx context.Context, scope OrderScope, job domain.FetchJob, knownMessageIDs []string) (*FetchMessagesResult, error) {
	sinceAt := uc.now().Add(-boundedRuntimeDuration("fetch_lookback_window_days", fetchLookbackWindow, 24*time.Hour, maxFetchLookbackWindow))
	if job.SinceAt != nil && !job.SinceAt.IsZero() {
		sinceAt = *job.SinceAt
	}
	untilAt := uc.now()
	if job.UntilAt != nil && !job.UntilAt.IsZero() {
		untilAt = *job.UntilAt
	}
	switch scope.AllocationType {
	case domain.ResourceTypeMicrosoft:
		if uc.transport == nil {
			return nil, domain.ErrMailServiceUnavailable
		}
		return uc.transport.FetchMicrosoftMessages(ctx, FetchMessagesRequest{
			Scope: scope, SinceAt: sinceAt, UntilAt: untilAt, Realtime: true, KnownMessageIDs: knownMessageIDs,
		})
	case domain.ResourceTypeDomain:
		return &FetchMessagesResult{Messages: nil}, nil
	default:
		return nil, domain.ErrInvalidRequest
	}
}

func scopeReadable(scope OrderScope, now func() time.Time) bool {
	if scope.OrderNo == "" || scope.EmailResourceID == 0 || scope.Recipient == "" {
		return false
	}
	switch scope.OrderStatus {
	case "active", "completed":
	default:
		return false
	}
	readUntil := scopeReadUntil(scope)
	return readUntil == nil || readUntil.After(now())
}

func scopeReadUntil(scope OrderScope) *time.Time {
	if scope.ServiceMode == "purchase" {
		return nil
	}
	return scope.ReceiveUntil
}

func scopeFetchable(scope OrderScope, now func() time.Time) bool {
	if !scopeReadable(scope, now) {
		return false
	}
	return scope.AllocationID > 0 && scope.EmailResourceID > 0
}

func (uc *UseCase) fetchedMessageToDomain(ctx context.Context, item FetchedMessage) (domain.Message, *OrderScope, error) {
	message := baseMessageFromFetched(item)
	matches := make([]struct {
		scope     OrderScope
		code      string
		recipient string
	}, 0)
	seenOrders := make(map[string]struct{})
	for _, recipient := range fetchedRecipientCandidates(item) {
		scopes, err := uc.repo.ListMatchingScopesByRecipient(ctx, message.ResourceType, message.EmailResourceID, recipient, message.ReceivedAt)
		if err != nil {
			return message, nil, err
		}
		for _, scope := range scopes {
			if _, ok := seenOrders[scope.OrderNo]; ok {
				continue
			}
			candidateMessage := message
			candidateMessage.Recipient = recipient
			matched, code, _ := matchAndExtract(fetchedMessageFromDomain(candidateMessage), scope)
			if matched {
				seenOrders[scope.OrderNo] = struct{}{}
				matches = append(matches, struct {
					scope     OrderScope
					code      string
					recipient string
				}{scope: scope, code: code, recipient: recipient})
			}
		}
	}
	switch len(matches) {
	case 0:
		message.Status = domain.MessageStatusIgnored
		message.MatchDiagnostic = "Message did not match any active order service."
	case 1:
		message.Status = domain.MessageStatusMatched
		message.Recipient = matches[0].recipient
		message.VerificationCode = matches[0].code
		matchedOrderID := matches[0].scope.OrderID
		message.MatchedOrderID = &matchedOrderID
		platform.RecordBusinessEvent("mail_match", "matched")
		return message, &matches[0].scope, nil
	default:
		message.Status = domain.MessageStatusReceived
		message.MatchDiagnostic = "Message matched multiple active order services."
		platform.RecordBusinessEvent("mail_match", "ambiguous")
	}
	return message, nil, nil
}

func baseMessageFromFetched(item FetchedMessage) domain.Message {
	body := strings.TrimSpace(item.Body)
	recipient := ""
	candidates := fetchedRecipientCandidates(item)
	if len(candidates) > 0 {
		recipient = candidates[0]
	}
	if item.BodyPreview == "" {
		item.BodyPreview = bodyPreview(body)
	}
	if item.ReceivedAt.IsZero() {
		item.ReceivedAt = time.Now().UTC()
	}
	return domain.Message{
		EmailResourceID:   item.EmailResourceID,
		ResourceType:      item.ResourceType,
		Recipient:         recipient,
		Recipients:        candidates,
		Sender:            strings.TrimSpace(item.Sender),
		Subject:           strings.TrimSpace(item.Subject),
		RawBody:           body,
		BodyPreview:       bodyPreview(item.BodyPreview),
		VerificationCode:  strings.TrimSpace(item.VerificationCode),
		MessageIDHeader:   strings.TrimSpace(item.MessageIDHeader),
		ProviderMessageID: strings.TrimSpace(item.ProviderMessageID),
		DedupeKey:         messageDedupeKey(item),
		Protocol:          strings.TrimSpace(item.Protocol),
		Folder:            strings.TrimSpace(item.Folder),
		Status:            domain.MessageStatusReceived,
		ReceivedAt:        item.ReceivedAt.UTC(),
	}
}

func fetchedMessageFromDomain(message domain.Message) FetchedMessage {
	return FetchedMessage{
		EmailResourceID:   message.EmailResourceID,
		ResourceType:      message.ResourceType,
		Recipient:         message.Recipient,
		Recipients:        message.Recipients,
		Sender:            message.Sender,
		Subject:           message.Subject,
		Body:              message.RawBody,
		BodyPreview:       message.BodyPreview,
		VerificationCode:  message.VerificationCode,
		MessageIDHeader:   message.MessageIDHeader,
		ProviderMessageID: message.ProviderMessageID,
		Protocol:          message.Protocol,
		Folder:            message.Folder,
		ReceivedAt:        message.ReceivedAt,
	}
}

func matchAndExtractAnyRecipient(message FetchedMessage, scope OrderScope) (bool, string, string) {
	for _, recipient := range fetchedRecipientCandidates(message) {
		candidate := message
		candidate.Recipient = recipient
		if matched, code, diagnostic := matchAndExtract(candidate, scope); matched {
			return true, code, diagnostic
		}
	}
	return false, "", "Message did not match recipient project mail rules."
}

func fetchedRecipientCandidates(item FetchedMessage) []string {
	values := make([]string, 0, len(item.Recipients)+1)
	values = append(values, item.Recipients...)
	values = append(values, item.Recipient)
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := normalizeEmail(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func inboundFetchedMessage(req InboundMailRequest) FetchedMessage {
	recipient := normalizeEmail(req.Recipient)
	receivedAt := req.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	body := strings.TrimSpace(string(req.Raw))
	item := FetchedMessage{
		EmailResourceID: req.EmailResourceID,
		ResourceType:    req.ResourceType,
		Recipient:       recipient,
		Recipients:      []string{recipient},
		Sender:          strings.TrimSpace(req.EnvelopeFrom),
		Body:            body,
		BodyPreview:     bodyPreview(body),
		MessageIDHeader: hashParts("inbound-raw", recipient, fmt.Sprintf("%d", receivedAt.UnixNano()), body),
		Protocol:        "smtp",
		Folder:          "inbound",
		ReceivedAt:      receivedAt.UTC(),
	}
	msg, err := stdmail.ReadMessage(bytes.NewReader(req.Raw))
	if err != nil {
		return item
	}
	decoder := new(mime.WordDecoder)
	if subject := decodeMIMEHeader(decoder, msg.Header.Get("Subject")); subject != "" {
		item.Subject = subject
	}
	if from := decodeMIMEHeader(decoder, msg.Header.Get("From")); from != "" {
		item.Sender = from
	}
	item.Recipients = normalizeRecipientCandidates(append(item.Recipients, mailAddressCandidates(msg.Header.Get("To"))...))
	item.Recipients = normalizeRecipientCandidates(append(item.Recipients, mailAddressCandidates(msg.Header.Get("Cc"))...))
	if messageID := strings.Trim(strings.TrimSpace(msg.Header.Get("Message-Id")), "<>"); messageID != "" {
		item.MessageIDHeader = messageID
	}
	if date, err := stdmail.ParseDate(msg.Header.Get("Date")); err == nil {
		item.ReceivedAt = date.UTC()
	}
	if parsedBody, _ := readMIMEBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body); strings.TrimSpace(parsedBody) != "" {
		item.Body = strings.TrimSpace(parsedBody)
		item.BodyPreview = bodyPreview(parsedBody)
	}
	return item
}

func normalizeRecipientCandidates(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := normalizeEmail(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

var mailAddressCandidateRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func mailAddressCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	values := make([]string, 0)
	if list, err := stdmail.ParseAddressList(raw); err == nil {
		for _, address := range list {
			values = append(values, address.Address)
		}
	} else {
		values = append(values, mailAddressCandidateRe.FindAllString(raw, -1)...)
	}
	return values
}

func matchAndExtract(message FetchedMessage, scope OrderScope) (bool, string, string) {
	enabled := enabledRules(scope.Rules)
	if !matchRequiredRule(MailRuleRecipient, enabled, message, scope) {
		return false, "", "Message did not match recipient project mail rules."
	}
	if scope.LooseMatch {
		if !matchRequiredRule(MailRuleSender, enabled, message, scope) {
			return false, "", "Message did not match sender project mail rules."
		}
		if code := extractByBodyRules(message.Body, enabled[MailRuleBody]); code != "" {
			return true, code, ""
		}
		return true, extractVerificationCode(message.Body), ""
	}
	for _, ruleType := range []MailRuleType{MailRuleSender, MailRuleSubject} {
		if !matchRequiredRule(ruleType, enabled, message, scope) {
			return false, "", "Message did not match strict project mail rules."
		}
	}
	code := extractByBodyRules(message.Body, enabled[MailRuleBody])
	if code == "" {
		return false, "", "Strict body rule did not extract a verification code."
	}
	return true, code, ""
}

func enabledRules(rules []MailRule) map[MailRuleType][]string {
	enabled := make(map[MailRuleType][]string)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			continue
		}
		enabled[rule.Type] = append(enabled[rule.Type], pattern)
	}
	return enabled
}

func matchRequiredRule(ruleType MailRuleType, enabled map[MailRuleType][]string, message FetchedMessage, scope OrderScope) bool {
	patterns := enabled[ruleType]
	return len(patterns) > 0 && matchAnyPattern(ruleType, patterns, message, scope)
}

func matchAnyPattern(ruleType MailRuleType, patterns []string, message FetchedMessage, scope OrderScope) bool {
	if ruleType == MailRuleRecipient {
		return matchRecipientPatterns(patterns, message, scope)
	}
	for _, pattern := range patterns {
		if matchPattern(ruleType, pattern, message, scope) {
			return true
		}
	}
	return false
}

func matchPattern(ruleType MailRuleType, pattern string, message FetchedMessage, scope OrderScope) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	switch ruleType {
	case MailRuleRecipient:
		return matchRecipientPatterns([]string{pattern}, message, scope)
	case MailRuleSender:
		return regexMatch(pattern, message.Sender)
	case MailRuleSubject:
		return regexMatch(pattern, message.Subject)
	case MailRuleBody:
		return regexMatch(pattern, message.Body)
	default:
		return false
	}
}

func matchRecipientPatterns(patterns []string, message FetchedMessage, scope OrderScope) bool {
	allowed := make(map[string]bool, len(patterns))
	for _, pattern := range patterns {
		switch pattern = strings.ToLower(strings.TrimSpace(pattern)); pattern {
		case "exact", "dot", "plus":
			allowed[pattern] = true
		}
	}
	recipient, recipientPlus, recipientDots, ok := domain.RecipientAliasForms(message.Recipient)
	if !ok {
		return false
	}
	target, targetPlus, targetDots, ok := domain.RecipientAliasForms(scope.Recipient)
	if !ok {
		return false
	}
	if recipient == target {
		return allowed[recipientKind(scope)]
	}
	if recipientDots != targetDots {
		return false
	}
	requiresPlus := recipient != recipientPlus
	requiresDot := recipientPlus != targetPlus
	if !requiresPlus && !requiresDot {
		return false
	}
	return (!requiresPlus || allowed["plus"]) && (!requiresDot || allowed["dot"])
}

func recipientKind(scope OrderScope) string {
	switch strings.ToLower(strings.TrimSpace(scope.RecipientKind)) {
	case "dot":
		return "dot"
	case "plus":
		return "plus"
	default:
		return "exact"
	}
}

type cachedRegex struct {
	re *regexp.Regexp
}

var regexCache sync.Map

func compileCachedRegex(pattern string) *regexp.Regexp {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(cachedRegex).re
	}
	re, err := regexp.Compile(pattern)
	item := cachedRegex{re: re}
	if err != nil {
		item.re = nil
	}
	actual, _ := regexCache.LoadOrStore(pattern, item)
	return actual.(cachedRegex).re
}

func regexMatch(pattern string, value string) bool {
	re := compileCachedRegex(pattern)
	if re == nil {
		return false
	}
	return re.MatchString(value)
}

func extractByBodyRules(body string, patterns []string) string {
	body = strings.TrimSpace(body)
	if body == "" || len(patterns) == 0 {
		return ""
	}
	for _, pattern := range patterns {
		re := compileCachedRegex(pattern)
		if re == nil {
			continue
		}
		matches := re.FindStringSubmatch(body)
		if len(matches) == 0 {
			continue
		}
		for _, value := range matches[1:] {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
		if value := strings.TrimSpace(matches[0]); value != "" {
			return value
		}
	}
	return ""
}

const verificationCodePattern = `(^|[^\d])(\d{6,8})([^\d]|$)`

var verificationCodeRe = regexp.MustCompile(verificationCodePattern)

func extractVerificationCode(body string) string {
	pattern := runtimeconfig.String("verification_code_pattern", verificationCodePattern)
	if len(pattern) > maxVerificationPatternSize {
		pattern = verificationCodePattern
	}
	re := compileCachedRegex(pattern)
	if re == nil {
		re = verificationCodeRe
	}
	matches := re.FindStringSubmatch(body)
	if len(matches) == 0 {
		return ""
	}
	for _, match := range matches[1:] {
		if isDigits(match) {
			return match
		}
	}
	if isDigits(matches[0]) {
		return matches[0]
	}
	return longestDigitRun(matches[0])
}

func longestDigitRun(value string) string {
	start, bestStart, bestLength := -1, 0, 0
	for i := 0; i <= len(value); i++ {
		if i < len(value) && value[i] >= '0' && value[i] <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start > bestLength {
			bestStart, bestLength = start, i-start
		}
		start = -1
	}
	if bestLength == 0 {
		return ""
	}
	return value[bestStart : bestStart+bestLength]
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func bodyPreview(value string) string {
	value = strings.ToValidUTF8(strings.Join(strings.Fields(strings.TrimSpace(value)), " "), "")
	if len(value) <= 1000 {
		return value
	}
	return strings.ToValidUTF8(value[:1000], "")
}

func decodeMIMEHeader(decoder *mime.WordDecoder, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func readMIMEBody(contentType string, transferEncoding string, body io.Reader) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		var htmlFallback string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			partBody, err := readMIMEBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil {
				continue
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			switch strings.ToLower(partType) {
			case "text/plain":
				if strings.TrimSpace(partBody) != "" {
					return partBody, nil
				}
			case "text/html":
				if htmlFallback == "" {
					htmlFallback = stripHTML(partBody)
				}
			}
		}
		return htmlFallback, nil
	}

	reader := decodeTransferReader(body, transferEncoding)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	text := string(data)
	if strings.EqualFold(mediaType, "text/html") {
		text = stripHTML(text)
	}
	return text, nil
}

func decodeTransferReader(body io.Reader, transferEncoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

var (
	htmlScriptRe = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	htmlStyleRe  = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	htmlTagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
)

func stripHTML(value string) string {
	value = htmlScriptRe.ReplaceAllString(value, " ")
	value = htmlStyleRe.ReplaceAllString(value, " ")
	value = htmlTagRe.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func messageDedupeKey(item FetchedMessage) string {
	if messageID := strings.ToLower(strings.Trim(strings.TrimSpace(item.MessageIDHeader), "<>")); messageID != "" {
		return hashParts("message-id", messageID)
	}
	recipients := strings.Join(fetchedRecipientCandidates(item), ",")
	sender := strings.ToLower(strings.TrimSpace(item.Sender))
	subject := strings.TrimSpace(item.Subject)
	normalizedBody := stripHTML(item.Body)
	if strings.TrimSpace(recipients+sender+subject+normalizedBody) == "" {
		if providerMessageID := strings.ToLower(strings.TrimSpace(item.ProviderMessageID)); providerMessageID != "" {
			return hashParts(
				"provider",
				strings.ToLower(strings.TrimSpace(item.Protocol)),
				strings.ToLower(strings.TrimSpace(item.Folder)),
				providerMessageID,
			)
		}
	}
	parts := []string{
		"content",
		recipients,
		sender,
		subject,
		item.ReceivedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
		bodyHash(normalizedBody),
	}
	return hashParts(parts...)
}

func hashParts(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprint(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func bodyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func latestReceivedAt(messages []domain.Message) *time.Time {
	var latest *time.Time
	for _, message := range messages {
		receivedAt := message.ReceivedAt
		if receivedAt.IsZero() {
			continue
		}
		if latest == nil || receivedAt.After(*latest) {
			latest = &receivedAt
		}
	}
	return latest
}

func countMatched(messages []domain.Message) int {
	count := 0
	for _, message := range messages {
		if message.Status == domain.MessageStatusMatched {
			count++
		}
	}
	return count
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
