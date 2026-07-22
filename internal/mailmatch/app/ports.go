package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
)

const (
	defaultFetchCooldown     = 5 * time.Second
	autoFetchCooldown        = 5 * time.Second
	fetchLookbackWindow      = 90 * 24 * time.Hour
	fetchOverlapWindow       = 5 * time.Minute
	readWindowSkew           = 2 * time.Minute
	codeReadLimit            = 1
	purchaseReadLimit        = 30
	messageScanLimit         = 40
	defaultFetchMaxAttempts  = 3
	readFetchScheduleTimeout = 500 * time.Millisecond
)

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
	Scope       OrderScope
	SinceAt     time.Time
	UntilAt     time.Time
	RequestID   string
	FullHistory bool
	OnMessages  func([]FetchedMessage)
	OnReset     func()
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
	CreateCodeOrderDelivery(ctx context.Context, orderID uint, message domain.Message) error
	AdvancePurchaseOrderDelivery(ctx context.Context, orderID uint, message domain.Message) error
	ListMatchingScopesByRecipient(ctx context.Context, resourceType domain.ResourceType, emailResourceID uint, recipient string, receivedAt time.Time) ([]OrderScope, error)
	FindFetchStateForUpdate(ctx context.Context, emailResourceID uint) (*domain.FetchState, error)
	EnsureFetchStates(ctx context.Context, emailResourceIDs []uint) error
	FindFetchStatesForUpdate(ctx context.Context, emailResourceIDs []uint) (map[uint]*domain.FetchState, error)
	RequestFetch(ctx context.Context, job *domain.FetchJob, cooldownUntil time.Time, now time.Time) error
	RequestFetchBatch(ctx context.Context, jobs []*domain.FetchJob, cooldownUntil time.Time, now time.Time) error
	ListPendingFetches(ctx context.Context, limit int) ([]domain.FetchJob, error)
	MarkFetchProcessing(ctx context.Context, emailResourceID uint, generation uint64, now time.Time) (bool, error)
	FindFetch(ctx context.Context, emailResourceID uint, generation uint64) (*domain.FetchJob, error)
	AssertFetchFence(ctx context.Context, emailResourceID uint, generation uint64) error
	CompleteFetch(ctx context.Context, emailResourceID uint, generation uint64, fetched int, stored int, matched int, lastReceivedAt *time.Time, now time.Time) (bool, error)
	SkipFetch(ctx context.Context, emailResourceID uint, generation uint64, safeError string, now time.Time) (bool, error)
	ReleaseFetchInfrastructureFailure(ctx context.Context, emailResourceID uint, generation uint64, safeError string) (bool, error)
	RecordFetchFailure(ctx context.Context, emailResourceID uint, generation uint64, safeError string, retryable bool, now time.Time) (recorded bool, abnormal bool, err error)
	UpsertMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error)
}

type FetchQueue interface {
	EnqueueFetch(ctx context.Context, task FetchTask) (bool, error)
	EnqueueFetchDispatcher(ctx context.Context, delay time.Duration) error
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
	EmailResourceID uint   `json:"emailResourceId"`
	Generation      uint64 `json:"generation"`
}

type FetchSubmitRequest struct {
	OrderNo      string
	UserID       uint
	IsAdmin      bool
	Purpose      domain.FetchPurpose
	RequestID    string
	ServiceToken bool
	ServiceEmail string
}

type FetchSubmitResult struct {
	Accepted           bool
	Reason             string
	JobID              uint
	Status             string
	NextFetchAllowedAt *time.Time
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
	repo        Repository
	queue       FetchQueue
	transport   MailTransportFetchPort
	matches     MatchResultPort
	credentials coreapp.MicrosoftCredentialPort
	now         func() time.Time
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
	items, state, hasDelivery, err := uc.listOrderMailByScope(ctx, *scope)
	if err != nil {
		return nil, nil, err
	}
	if shouldScheduleReadFetch(*scope, hasDelivery) && fetchDue(state, uc.now()) {
		uc.scheduleReadFetch(ctx, FetchSubmitRequest{
			OrderNo: scope.OrderNo,
			UserID:  userID,
			IsAdmin: isAdmin,
			Purpose: domain.FetchPurposeAutoRefresh,
		})
	}
	return items, state, nil
}

func (uc *UseCase) ListPickupMail(ctx context.Context, token string, email string) ([]domain.MailContent, *domain.FetchState, error) {
	scope, err := uc.repo.LoadPickupScope(ctx, strings.TrimSpace(token), normalizeEmail(email))
	if err != nil {
		return nil, nil, err
	}
	items, state, hasDelivery, err := uc.listOrderMailByScope(ctx, *scope)
	if err != nil {
		return nil, nil, err
	}
	if shouldScheduleReadFetch(*scope, hasDelivery) && fetchDue(state, uc.now()) {
		uc.scheduleReadFetch(ctx, FetchSubmitRequest{
			OrderNo:      scope.OrderNo,
			Purpose:      domain.FetchPurposeAutoRefresh,
			ServiceToken: true,
			ServiceEmail: email,
		})
	}
	return items, state, nil
}

func (uc *UseCase) ListPickupMailBatch(ctx context.Context, credentials []PickupCredential) []PickupMailResult {
	if reader, ok := uc.repo.(PickupBatchReader); ok {
		return uc.listPickupMailBatchBulk(ctx, credentials, reader)
	}

	results := make([]PickupMailResult, len(credentials))
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
			items, state, err := uc.ListPickupMail(ctx, credentials[index].Token, credentials[index].Email)
			results[index] = PickupMailResult{Items: items, Fetch: state, Err: err}
		}(i)
	}
	wg.Wait()
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
	reads, err := reader.ReadPickupBatch(ctx, credentials, now, messageScanLimit)
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

	fetchScopes := make([]OrderScope, 0, len(reads))
	seenResources := make(map[uint]struct{}, len(reads))
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
		if read.Scope.AllocationType == domain.ResourceTypeMicrosoft &&
			shouldScheduleReadFetch(*read.Scope, hasDelivery) && fetchDue(state, now) {
			if _, exists := seenResources[read.Scope.EmailResourceID]; exists {
				continue
			}
			seenResources[read.Scope.EmailResourceID] = struct{}{}
			fetchScopes = append(fetchScopes, *read.Scope)
		}
	}
	uc.scheduleReadFetchBatch(ctx, fetchScopes)
	return results
}

func (uc *UseCase) scheduleReadFetchBatch(ctx context.Context, scopes []OrderScope) {
	if uc == nil || uc.queue == nil || len(scopes) == 0 {
		return
	}
	scheduleCtx, cancel := context.WithTimeout(ctx, readFetchScheduleTimeout)
	defer cancel()
	sort.Slice(scopes, func(i, j int) bool {
		return scopes[i].EmailResourceID < scopes[j].EmailResourceID
	})
	now := uc.now()
	accepted := 0
	err := uc.repo.WithTx(scheduleCtx, func(txCtx context.Context) error {
		resourceIDs := make([]uint, len(scopes))
		for i := range scopes {
			resourceIDs[i] = scopes[i].EmailResourceID
		}
		if err := uc.repo.EnsureFetchStates(txCtx, resourceIDs); err != nil {
			return err
		}
		states, err := uc.repo.FindFetchStatesForUpdate(txCtx, resourceIDs)
		if err != nil {
			return err
		}
		jobs := make([]*domain.FetchJob, 0, len(scopes))
		for i := range scopes {
			scope := scopes[i]
			if scope.AllocationType != domain.ResourceTypeMicrosoft || !scopeFetchable(scope, func() time.Time { return now }) {
				continue
			}
			state := states[scope.EmailResourceID]
			if !fetchDue(state, now) {
				continue
			}
			sinceAt := fetchSinceAt(scope, state, now)
			untilAt := now
			if readUntil := scopeReadUntil(scope); readUntil != nil && readUntil.Before(untilAt) {
				untilAt = *readUntil
			}
			jobs = append(jobs, &domain.FetchJob{
				ID:                         scope.EmailResourceID,
				ExpectedCredentialRevision: scope.CredentialRevision,
				OrderNo:                    scope.OrderNo,
				Purpose:                    domain.FetchPurposeAutoRefresh,
				AllocationType:             scope.AllocationType,
				AllocationID:               scope.AllocationID,
				ProjectID:                  scope.ProjectID,
				EmailResourceID:            scope.EmailResourceID,
				Recipient:                  scope.Recipient,
				Status:                     domain.FetchJobPending,
				MaxAttempts:                defaultFetchMaxAttempts,
				SinceAt:                    &sinceAt,
				UntilAt:                    &untilAt,
			})
		}
		if len(jobs) == 0 {
			return nil
		}
		if err := uc.repo.RequestFetchBatch(txCtx, jobs, now.Add(autoFetchCooldown), now); err != nil {
			return err
		}
		accepted = len(jobs)
		return nil
	})
	if err == nil && accepted > 0 {
		uc.ScheduleFetchDispatcher(scheduleCtx, 0)
	}
}

func (uc *UseCase) listOrderMailFromBatch(
	read PickupBatchRead,
	now time.Time,
) ([]domain.MailContent, *domain.FetchState, bool, error) {
	scope := *read.Scope
	if !scopeReadable(scope, func() time.Time { return now }) {
		return nil, nil, false, domain.ErrOrderUnavailable
	}
	limit := purchaseReadLimit
	if scope.ServiceMode == "code" {
		limit = codeReadLimit
	}
	messages := filterPickupMessages(scope, read.Messages, now)
	if len(messages) > messageScanLimit {
		messages = messages[:messageScanLimit]
	}
	if len(messages) > limit {
		messages = messages[:limit]
	}
	items := make([]domain.MailContent, len(messages))
	for i := range messages {
		items[i] = mailContentFromMessage(messages[i])
	}
	if read.Delivery != nil && scope.ServiceMode == "code" {
		if read.Delivery.Message == nil {
			return nil, read.Fetch, true, nil
		}
		return []domain.MailContent{mailContentFromMessage(*read.Delivery.Message)}, read.Fetch, true, nil
	}
	if read.Delivery != nil && read.Delivery.Message != nil {
		items = prependDeliveryMail(items, mailContentFromMessage(*read.Delivery.Message), limit)
	}
	return items, read.Fetch, read.Delivery != nil, nil
}

func filterPickupMessages(scope OrderScope, messages []domain.Message, now time.Time) []domain.Message {
	start := now.Add(-30 * 24 * time.Hour)
	if scope.AllocationType == domain.ResourceTypeMicrosoft {
		start = now.Add(-3 * 24 * time.Hour)
	}
	if scope.ReceiveStartedAt != nil {
		serviceStart := scope.ReceiveStartedAt.Add(-readWindowSkew)
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

func fetchDue(state *domain.FetchState, now time.Time) bool {
	if state == nil {
		return true
	}
	if state.LastStatus == string(domain.FetchJobPending) || state.LastStatus == string(domain.FetchJobRunning) {
		return false
	}
	if state.CooldownUntil == nil {
		return true
	}
	return !state.CooldownUntil.After(now)
}

func (uc *UseCase) listOrderMailByScope(ctx context.Context, scope OrderScope) ([]domain.MailContent, *domain.FetchState, bool, error) {
	if !scopeReadable(scope, uc.now) {
		return nil, nil, false, domain.ErrOrderUnavailable
	}
	delivery, err := uc.repo.FindOrderDelivery(ctx, scope.OrderID)
	if err != nil {
		return nil, nil, false, err
	}
	state, err := uc.repo.FindFetchStateForUpdate(ctx, scope.EmailResourceID)
	if err != nil {
		return nil, nil, false, err
	}
	if delivery != nil && scope.ServiceMode == "code" {
		if delivery.Message == nil {
			return nil, state, true, nil
		}
		return []domain.MailContent{mailContentFromMessage(*delivery.Message)}, state, true, nil
	}
	limit := purchaseReadLimit
	if scope.ServiceMode == "code" {
		limit = codeReadLimit
	}
	messages, err := uc.repo.ListOrderMessages(ctx, scope, messageScanLimit)
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
	return items, state, delivery != nil, nil
}

func (uc *UseCase) saveOrderDelivery(ctx context.Context, scope OrderScope, message domain.Message) error {
	if scope.OrderID == 0 || message.ID == 0 {
		return nil
	}
	if scope.ServiceMode == "code" {
		if strings.TrimSpace(message.VerificationCode) == "" {
			return nil
		}
		return uc.repo.CreateCodeOrderDelivery(ctx, scope.OrderID, message)
	}
	return uc.repo.AdvancePurchaseOrderDelivery(ctx, scope.OrderID, message)
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

func (uc *UseCase) SubmitFetch(ctx context.Context, req FetchSubmitRequest) (*FetchSubmitResult, error) {
	orderNo := strings.TrimSpace(req.OrderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidRequest
	}
	var result *FetchSubmitResult
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		scope, err := uc.loadScopeForSubmit(txCtx, req)
		if err != nil {
			return err
		}
		if !scopeFetchable(*scope, uc.now) {
			return domain.ErrOrderUnavailable
		}
		if scope.AllocationType == domain.ResourceTypeDomain {
			result = &FetchSubmitResult{
				Accepted: false,
				Reason:   "push_only",
				Status:   "push_only",
			}
			return nil
		}
		if err := uc.repo.EnsureFetchStates(txCtx, []uint{scope.EmailResourceID}); err != nil {
			return err
		}
		state, err := uc.repo.FindFetchStateForUpdate(txCtx, scope.EmailResourceID)
		if err != nil {
			return err
		}
		now := uc.now()
		if state != nil && state.CooldownUntil != nil && state.CooldownUntil.After(now) {
			result = &FetchSubmitResult{
				Accepted:           false,
				Reason:             "cooldown",
				Status:             "cooldown",
				NextFetchAllowedAt: state.CooldownUntil,
			}
			return nil
		}
		sinceAt := fetchSinceAt(*scope, state, now)
		untilAt := now
		if readUntil := scopeReadUntil(*scope); readUntil != nil && readUntil.Before(untilAt) {
			untilAt = *readUntil
		}
		purpose := req.Purpose
		if purpose == "" {
			purpose = domain.FetchPurposeOrder
		}
		job := &domain.FetchJob{
			ID:                         scope.EmailResourceID,
			ExpectedCredentialRevision: scope.CredentialRevision,
			OrderNo:                    scope.OrderNo,
			Purpose:                    purpose,
			AllocationType:             scope.AllocationType,
			AllocationID:               scope.AllocationID,
			ProjectID:                  scope.ProjectID,
			EmailResourceID:            scope.EmailResourceID,
			Recipient:                  scope.Recipient,
			Status:                     domain.FetchJobPending,
			MaxAttempts:                defaultFetchMaxAttempts,
			SinceAt:                    &sinceAt,
			UntilAt:                    &untilAt,
			RequestID:                  strings.TrimSpace(req.RequestID),
		}
		cooldownUntil := now.Add(fetchCooldown(purpose))
		if err := uc.repo.RequestFetch(txCtx, job, cooldownUntil, now); err != nil {
			return err
		}
		result = &FetchSubmitResult{
			Accepted:           true,
			Reason:             "created",
			JobID:              scope.EmailResourceID,
			Status:             string(job.Status),
			NextFetchAllowedAt: &cooldownUntil,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result != nil && result.Accepted {
		uc.ScheduleFetchDispatcher(ctx, 0)
	}
	return result, nil
}

func (uc *UseCase) ProcessFetch(ctx context.Context, task FetchTask) error {
	if task.EmailResourceID == 0 || task.Generation == 0 {
		return domain.ErrInvalidRequest
	}
	now := uc.now()
	claimed, err := uc.repo.MarkFetchProcessing(ctx, task.EmailResourceID, task.Generation, now)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	serviceStarted := time.Now()
	defer platform.ObserveTaskService("mailmatch_fetch", serviceStarted)
	job, err := uc.repo.FindFetch(ctx, task.EmailResourceID, task.Generation)
	if err != nil {
		return uc.releaseFetchInfrastructure(ctx, task, err)
	}
	if job == nil {
		return nil
	}
	platform.ObserveQueueWait("mailmatch_fetch", job.CreatedAt)
	scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, job.OrderNo)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) || errors.Is(err, domain.ErrOrderUnavailable) || errors.Is(err, domain.ErrOrderForbidden) {
			_, _ = uc.repo.SkipFetch(ctx, task.EmailResourceID, task.Generation, "Order is not available for mail fetch.", uc.now())
			return nil
		}
		return uc.releaseFetchInfrastructure(ctx, task, err)
	}
	if !scopeFetchable(*scope, uc.now) {
		_, _ = uc.repo.SkipFetch(ctx, task.EmailResourceID, task.Generation, "Order is not available for mail fetch.", uc.now())
		return nil
	}
	fetched, fetchErr := uc.fetchMessages(ctx, *scope, *job)
	if fetchErr != nil {
		return uc.finishFetchFailure(ctx, task, "Mail service is temporarily unavailable.", true, fetchErr)
	}
	if fetched == nil {
		return uc.finishFetchFailure(ctx, task, "Mail service is temporarily unavailable.", true, domain.ErrMailServiceUnavailable)
	}
	if strings.TrimSpace(fetched.RefreshToken) != "" &&
		strings.TrimSpace(fetched.RefreshToken) != strings.TrimSpace(scope.MicrosoftRT) &&
		scope.AllocationType == domain.ResourceTypeMicrosoft {
		if uc.credentials == nil {
			return uc.releaseFetchInfrastructure(ctx, task, errors.New("microsoft credential service is unavailable"))
		}
		err := uc.credentials.ApplyMicrosoftFetchRefreshToken(ctx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID:                 scope.EmailResourceID,
			ExpectedCredentialRevision: scope.CredentialRevision,
			RefreshToken:               fetched.RefreshToken,
			Now:                        uc.now(),
		})
		if errors.Is(err, coreapp.ErrMicrosoftCredentialChanged) || errors.Is(err, coreapp.ErrMicrosoftCredentialDeleted) || errors.Is(err, coreapp.ErrMicrosoftCredentialNotFound) {
			_, _ = uc.repo.SkipFetch(ctx, task.EmailResourceID, task.Generation, "Microsoft credentials changed while mail was being fetched.", uc.now())
			return nil
		}
		if err != nil {
			return uc.releaseFetchInfrastructure(ctx, task, err)
		}
	}
	stored, matched, lastReceivedAt, err := uc.ingestFetchedMessagesWithFence(ctx, fetched.Messages, func(txCtx context.Context) error {
		return uc.repo.AssertFetchFence(txCtx, task.EmailResourceID, task.Generation)
	})
	if err != nil {
		safeError := "Mail message ingestion failed."
		if stageErr := (*mailIngestError)(nil); errors.As(err, &stageErr) {
			safeError = stageErr.safe
		}
		if safeError == "Mail match result notification failed." {
			return uc.finishFetchFailure(ctx, task, safeError, true, err)
		}
		return uc.releaseFetchInfrastructure(ctx, task, err)
	}
	if _, err := uc.repo.CompleteFetch(ctx, task.EmailResourceID, task.Generation, len(fetched.Messages), stored, matched, lastReceivedAt, uc.now()); err != nil {
		return uc.releaseFetchInfrastructure(ctx, task, err)
	}
	return nil
}

func (uc *UseCase) finishFetchFailure(ctx context.Context, task FetchTask, safeError string, retryable bool, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return uc.releaseFetchInfrastructure(ctx, task, cause)
	}
	var failure *MailFetchFailure
	if errors.As(cause, &failure) && failure != nil {
		retryable = failure.Retryable
		if strings.TrimSpace(failure.SafeMessage) != "" {
			safeError = failure.SafeMessage
		}
	}
	recorded, abnormal, err := uc.repo.RecordFetchFailure(
		context.WithoutCancel(ctx), task.EmailResourceID, task.Generation, safeError, retryable, uc.now(),
	)
	if err != nil {
		return errors.Join(cause, err)
	}
	if recorded && !abnormal {
		uc.ScheduleFetchDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
}

func (uc *UseCase) releaseFetchInfrastructure(ctx context.Context, task FetchTask, cause error) error {
	released, err := uc.repo.ReleaseFetchInfrastructureFailure(
		context.WithoutCancel(ctx), task.EmailResourceID, task.Generation, "Mail fetch infrastructure is temporarily unavailable.",
	)
	if err != nil {
		return errors.Join(cause, err)
	}
	if released {
		uc.ScheduleFetchDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
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
	_, _, _, err := uc.ingestFetchedMessages(ctx, []FetchedMessage{inboundFetchedMessage(req)})
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
	matchDeliveries = latestOrderDeliveries(matchDeliveries)
	lastReceivedAt := latestReceivedAt(messages)
	storedDeliveries := make([]matchedDelivery, 0, len(matchDeliveries))
	stored := 0
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if fence != nil {
			if err := fence(txCtx); err != nil {
				return err
			}
		}
		storedMessages, err := uc.repo.UpsertMessages(txCtx, messages)
		if err != nil {
			return &mailIngestError{safe: "Mail message storage failed.", err: err}
		}
		messages = storedMessages
		stored = len(storedMessages)
		storedByIdentity := make(map[string]domain.Message, len(storedMessages))
		for _, message := range storedMessages {
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
		if message.Status == domain.MessageStatusMatched {
			platform.ObserveMailVisible(string(message.ResourceType), message.ReceivedAt)
		}
	}
	for _, delivery := range storedDeliveries {
		result := MatchResult{
			OrderNo:          delivery.scope.OrderNo,
			VerificationCode: delivery.message.VerificationCode,
			MatchedAt:        delivery.message.ReceivedAt,
		}
		if err := uc.matches.NotifyMatchedCode(ctx, result); err != nil {
			return stored, countMatched(messages), lastReceivedAt, &mailIngestError{safe: "Mail match result notification failed.", err: err}
		}
	}
	return stored, countMatched(messages), lastReceivedAt, nil
}

func mailMessageIdentity(message domain.Message) string {
	return fmt.Sprintf("%d:%s", message.EmailResourceID, strings.TrimSpace(message.DedupeKey))
}

func latestOrderDeliveries(deliveries []matchedDelivery) []matchedDelivery {
	if len(deliveries) < 2 {
		return deliveries
	}
	latestByOrder := make(map[uint]matchedDelivery, len(deliveries))
	for _, delivery := range deliveries {
		current, exists := latestByOrder[delivery.scope.OrderID]
		if !exists || delivery.message.ReceivedAt.After(current.message.ReceivedAt) ||
			(delivery.message.ReceivedAt.Equal(current.message.ReceivedAt) && delivery.message.DedupeKey > current.message.DedupeKey) {
			latestByOrder[delivery.scope.OrderID] = delivery
		}
	}
	result := make([]matchedDelivery, 0, len(latestByOrder))
	for _, delivery := range latestByOrder {
		result = append(result, delivery)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].scope.OrderID < result[j].scope.OrderID
	})
	return result
}

func (uc *UseCase) DispatchFetchJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	jobs, err := uc.repo.ListPendingFetches(ctx, limit)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, job := range jobs {
		accepted, err := uc.enqueueFetch(ctx, FetchTask{EmailResourceID: job.EmailResourceID, Generation: job.Generation})
		if err != nil || !accepted {
			continue
		}
		queued++
	}
	return queued, nil
}

func (uc *UseCase) ScheduleFetchDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueFetchDispatcher(ctx, delay)
}

func (uc *UseCase) scheduleReadFetch(ctx context.Context, req FetchSubmitRequest) {
	if uc == nil || uc.queue == nil {
		return
	}
	scheduleCtx, cancel := context.WithTimeout(ctx, readFetchScheduleTimeout)
	defer cancel()
	_, _ = uc.SubmitFetch(scheduleCtx, req)
}

func (uc *UseCase) loadScopeForSubmit(ctx context.Context, req FetchSubmitRequest) (*OrderScope, error) {
	if req.ServiceToken {
		scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, strings.TrimSpace(req.OrderNo))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.ServiceEmail) != "" && normalizeEmail(req.ServiceEmail) != normalizeEmail(scope.Recipient) {
			return nil, domain.ErrOrderForbidden
		}
		return scope, nil
	}
	return uc.repo.LoadOrderScope(ctx, strings.TrimSpace(req.OrderNo), req.UserID, req.IsAdmin)
}

func (uc *UseCase) fetchMessages(ctx context.Context, scope OrderScope, job domain.FetchJob) (*FetchMessagesResult, error) {
	sinceAt := time.Now().UTC().Add(-fetchLookbackWindow)
	if job.SinceAt != nil {
		sinceAt = *job.SinceAt
	}
	untilAt := uc.now()
	if job.UntilAt != nil {
		untilAt = *job.UntilAt
	}
	switch scope.AllocationType {
	case domain.ResourceTypeMicrosoft:
		if uc.transport == nil {
			return nil, domain.ErrMailServiceUnavailable
		}
		return uc.transport.FetchMicrosoftMessages(ctx, FetchMessagesRequest{Scope: scope, SinceAt: sinceAt, UntilAt: untilAt})
	case domain.ResourceTypeDomain:
		return &FetchMessagesResult{Messages: nil}, nil
	default:
		return nil, domain.ErrInvalidRequest
	}
}

func (uc *UseCase) enqueueFetch(ctx context.Context, task FetchTask) (bool, error) {
	if uc.queue == nil {
		return false, domain.ErrFetchQueueUnavailable
	}
	accepted, err := uc.queue.EnqueueFetch(ctx, task)
	if err != nil || !accepted {
		return false, err
	}
	processing, err := uc.repo.MarkFetchProcessing(ctx, task.EmailResourceID, task.Generation, uc.now())
	if err != nil {
		return false, err
	}
	return processing, nil
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

func fetchSinceAt(scope OrderScope, state *domain.FetchState, now time.Time) time.Time {
	candidates := []time.Time{now.Add(-fetchLookbackWindow)}
	if scope.ReceiveStartedAt != nil {
		candidates = append(candidates, scope.ReceiveStartedAt.Add(-readWindowSkew))
	}
	if state != nil && state.LastReceivedAt != nil {
		candidates = append(candidates, state.LastReceivedAt.Add(-fetchOverlapWindow))
	}
	result := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.After(result) {
			result = candidate
		}
	}
	return result
}

func fetchCooldown(purpose domain.FetchPurpose) time.Duration {
	if purpose == domain.FetchPurposeAutoRefresh {
		return autoFetchCooldown
	}
	return defaultFetchCooldown
}

func (uc *UseCase) fetchedMessageToDomain(ctx context.Context, item FetchedMessage) (domain.Message, *OrderScope, error) {
	message := baseMessageFromFetched(item)
	matches := make([]struct {
		scope OrderScope
		code  string
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
					scope OrderScope
					code  string
				}{scope: scope, code: code})
			}
		}
	}
	switch len(matches) {
	case 0:
		message.Status = domain.MessageStatusIgnored
		message.MatchDiagnostic = "Message did not match any active order service."
	case 1:
		message.Status = domain.MessageStatusMatched
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

var verificationCodeRe = regexp.MustCompile(`(^|[^\d])(\d{6,8})([^\d]|$)`)

func extractVerificationCode(body string) string {
	matches := verificationCodeRe.FindStringSubmatch(body)
	if len(matches) == 0 {
		return ""
	}
	if len(matches) > 2 && isDigits(matches[2]) {
		return matches[2]
	}
	return ""
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
