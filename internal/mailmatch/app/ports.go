package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

const (
	defaultFetchCooldown       = 10 * time.Second
	autoFetchCooldown          = 30 * time.Second
	fetchLookbackWindow        = 90 * 24 * time.Hour
	fetchOverlapWindow         = 5 * time.Minute
	readWindowSkew             = 2 * time.Minute
	codeReadLimit              = 1
	purchaseReadLimit          = 30
	messageScanLimit           = 120
	defaultFetchMaxAttempts    = 3
	staleFetchRunningThreshold = 90 * time.Second
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
	OrderNo           string
	UserID            uint
	ProjectID         uint
	ProductID         uint
	ServiceMode       string
	OrderStatus       string
	AllocationType    domain.ResourceType
	AllocationID      uint
	RecipientKind     string
	EmailResourceID   uint
	Recipient         string
	ReceiveStartedAt  *time.Time
	ReceiveUntil      *time.Time
	ActivatedAt       *time.Time
	AfterSaleUntil    *time.Time
	LooseMatch        bool
	Rules             []MailRule
	MicrosoftEmail    string
	MicrosoftClientID string
	MicrosoftRT       string
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
	Scope   OrderScope
	SinceAt time.Time
	UntilAt time.Time
}

type FetchMessagesResult struct {
	Messages     []FetchedMessage
	RefreshToken string
}

type MailTransportFetchPort interface {
	FetchMicrosoftMessages(ctx context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error)
}

type Repository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	LoadOrderScope(ctx context.Context, orderNo string, userID uint, isAdmin bool) (*OrderScope, error)
	LoadOrderScopeForServiceToken(ctx context.Context, orderNo string) (*OrderScope, error)
	ListOrderMessages(ctx context.Context, scope OrderScope, limit int) ([]domain.Message, error)
	FindOrderSnapshot(ctx context.Context, orderNo string) (*domain.OrderSnapshot, error)
	CreateOrderSnapshotOnce(ctx context.Context, snapshot domain.OrderSnapshot) error
	UpsertLatestOrderSnapshot(ctx context.Context, snapshot domain.OrderSnapshot) error
	ListMatchingScopesByRecipient(ctx context.Context, resourceType domain.ResourceType, emailResourceID uint, recipient string, receivedAt time.Time) ([]OrderScope, error)
	FindLatestReceivedAt(ctx context.Context, orderNo string) (*time.Time, error)
	FindActiveFetchJob(ctx context.Context, orderNo string) (*domain.FetchJob, error)
	FindFetchStateForUpdate(ctx context.Context, orderNo string) (*domain.FetchState, error)
	CreateFetchState(ctx context.Context, orderNo string) (*domain.FetchState, error)
	CreateFetchJob(ctx context.Context, job *domain.FetchJob) error
	MarkFetchJobQueued(ctx context.Context, jobID uint) error
	FindFetchJob(ctx context.Context, jobID uint) (*domain.FetchJob, error)
	ClaimFetchJobRunning(ctx context.Context, jobID uint, now time.Time) (bool, error)
	MarkFetchJobSucceeded(ctx context.Context, jobID uint, fetched int, stored int, matched int, lastReceivedAt *time.Time, now time.Time) error
	MarkFetchJobSkipped(ctx context.Context, jobID uint, safeError string, now time.Time) error
	MarkFetchJobFailed(ctx context.Context, jobID uint, safeError string, retry bool, now time.Time) error
	ClaimDispatchableFetchJobs(ctx context.Context, limit int, staleBefore time.Time) ([]domain.FetchJob, error)
	UpdateFetchStateSubmitted(ctx context.Context, orderNo string, jobID uint, status string, cooldownUntil time.Time, now time.Time) error
	UpdateFetchStateCompleted(ctx context.Context, orderNo string, jobID uint, status string, lastReceivedAt *time.Time, safeError string, now time.Time) error
	UpsertMessages(ctx context.Context, messages []domain.Message) (int, error)
	LoadDomainInboundMessages(ctx context.Context, scope OrderScope, sinceAt, untilAt time.Time, limit int) ([]FetchedMessage, error)
	UpdateMicrosoftRefreshToken(ctx context.Context, resourceID uint, refreshToken string) error
}

type FetchQueue interface {
	EnqueueFetch(ctx context.Context, task FetchTask) error
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
	JobID uint `json:"jobId"`
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

type UseCase struct {
	repo      Repository
	queue     FetchQueue
	transport MailTransportFetchPort
	matches   MatchResultPort
	now       func() time.Time
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

func (uc *UseCase) ListOrderMail(ctx context.Context, orderNo string, userID uint, isAdmin bool) ([]domain.MailContent, *domain.FetchState, error) {
	scope, err := uc.repo.LoadOrderScope(ctx, strings.TrimSpace(orderNo), userID, isAdmin)
	if err != nil {
		return nil, nil, err
	}
	items, state, hasSnapshot, err := uc.listOrderMailByScope(ctx, *scope)
	if err != nil {
		return nil, nil, err
	}
	if shouldScheduleReadFetch(*scope, hasSnapshot) {
		uc.scheduleReadFetch(ctx, FetchSubmitRequest{
			OrderNo: scope.OrderNo,
			UserID:  userID,
			IsAdmin: isAdmin,
			Purpose: domain.FetchPurposeAutoRefresh,
		})
	}
	return items, state, nil
}

func (uc *UseCase) ListOrderMailByServiceToken(ctx context.Context, orderNo string, email string) ([]domain.MailContent, *domain.FetchState, error) {
	scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, strings.TrimSpace(orderNo))
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(email) != "" && normalizeEmail(email) != normalizeEmail(scope.Recipient) {
		return nil, nil, domain.ErrOrderForbidden
	}
	items, state, hasSnapshot, err := uc.listOrderMailByScope(ctx, *scope)
	if err != nil {
		return nil, nil, err
	}
	if shouldScheduleReadFetch(*scope, hasSnapshot) {
		uc.scheduleReadFetch(ctx, FetchSubmitRequest{
			OrderNo:      scope.OrderNo,
			Purpose:      domain.FetchPurposeAutoRefresh,
			ServiceToken: true,
			ServiceEmail: email,
		})
	}
	return items, state, nil
}

func shouldScheduleReadFetch(scope OrderScope, hasSnapshot bool) bool {
	return !(scope.ServiceMode == "code" && hasSnapshot)
}

func (uc *UseCase) listOrderMailByScope(ctx context.Context, scope OrderScope) ([]domain.MailContent, *domain.FetchState, bool, error) {
	if !scopeReadable(scope, uc.now) {
		return nil, nil, false, domain.ErrOrderUnavailable
	}
	snapshot, err := uc.repo.FindOrderSnapshot(ctx, scope.OrderNo)
	if err != nil {
		return nil, nil, false, err
	}
	state, err := uc.repo.FindFetchStateForUpdate(ctx, scope.OrderNo)
	if err != nil {
		return nil, nil, false, err
	}
	if snapshot != nil && scope.ServiceMode == "code" {
		return []domain.MailContent{mailContentFromSnapshot(*snapshot)}, state, true, nil
	}
	limit := purchaseReadLimit
	if scope.ServiceMode == "code" {
		limit = codeReadLimit
	}
	messages, err := uc.repo.ListOrderMessages(ctx, scope, messageScanLimit)
	if err != nil {
		return nil, nil, false, err
	}
	messages = filterMessagesForScope(messages, scope, limit)
	items := make([]domain.MailContent, len(messages))
	for i := range messages {
		items[i] = domain.MailContent{
			Sender:           messages[i].Sender,
			Recipient:        messages[i].Recipient,
			ReceivedAt:       messages[i].ReceivedAt,
			Subject:          messages[i].Subject,
			Body:             messages[i].RawBody,
			VerificationCode: messages[i].VerificationCode,
		}
	}
	if snapshot != nil {
		items = prependSnapshotMail(items, mailContentFromSnapshot(*snapshot), limit)
	}
	return items, state, snapshot != nil, nil
}

func (uc *UseCase) saveOrderSnapshot(ctx context.Context, scope OrderScope, message domain.Message) error {
	code := strings.TrimSpace(message.VerificationCode)
	if code == "" || strings.TrimSpace(scope.OrderNo) == "" {
		return nil
	}
	snapshot := domain.OrderSnapshot{
		OrderNo:          scope.OrderNo,
		Sender:           message.Sender,
		Recipient:        message.Recipient,
		ReceivedAt:       message.ReceivedAt,
		Subject:          message.Subject,
		Body:             message.RawBody,
		VerificationCode: code,
	}
	if scope.ServiceMode == "code" {
		return uc.repo.CreateOrderSnapshotOnce(ctx, snapshot)
	}
	return uc.repo.UpsertLatestOrderSnapshot(ctx, snapshot)
}

func mailContentFromSnapshot(snapshot domain.OrderSnapshot) domain.MailContent {
	return domain.MailContent{
		Sender:           snapshot.Sender,
		Recipient:        snapshot.Recipient,
		ReceivedAt:       snapshot.ReceivedAt,
		Subject:          snapshot.Subject,
		Body:             snapshot.Body,
		VerificationCode: snapshot.VerificationCode,
	}
}

func prependSnapshotMail(items []domain.MailContent, snapshot domain.MailContent, limit int) []domain.MailContent {
	for i := range items {
		if sameMailContent(items[i], snapshot) {
			return items
		}
	}
	out := make([]domain.MailContent, 0, len(items)+1)
	out = append(out, snapshot)
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
		active, err := uc.repo.FindActiveFetchJob(txCtx, orderNo)
		if err != nil {
			return err
		}
		if active != nil {
			result = &FetchSubmitResult{
				Accepted: false,
				Reason:   "active_job_exists",
				JobID:    active.ID,
				Status:   string(active.Status),
			}
			return nil
		}
		state, err := uc.repo.FindFetchStateForUpdate(txCtx, orderNo)
		if err != nil {
			return err
		}
		if state == nil {
			state, err = uc.repo.CreateFetchState(txCtx, orderNo)
			if err != nil {
				return err
			}
		}
		now := uc.now()
		if state.CooldownUntil != nil && state.CooldownUntil.After(now) {
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
			OrderNo:         scope.OrderNo,
			Purpose:         purpose,
			AllocationType:  scope.AllocationType,
			AllocationID:    scope.AllocationID,
			ProjectID:       scope.ProjectID,
			EmailResourceID: scope.EmailResourceID,
			Recipient:       scope.Recipient,
			Status:          domain.FetchJobPending,
			MaxAttempts:     defaultFetchMaxAttempts,
			SinceAt:         &sinceAt,
			UntilAt:         &untilAt,
			RequestID:       strings.TrimSpace(req.RequestID),
		}
		if err := uc.repo.CreateFetchJob(txCtx, job); err != nil {
			if errors.Is(err, domain.ErrFetchJobConflict) {
				active, findErr := uc.repo.FindActiveFetchJob(txCtx, orderNo)
				if findErr != nil {
					return findErr
				}
				if active != nil {
					result = &FetchSubmitResult{
						Accepted: false,
						Reason:   "active_job_exists",
						JobID:    active.ID,
						Status:   string(active.Status),
					}
					return nil
				}
			}
			return err
		}
		cooldownUntil := now.Add(fetchCooldown(purpose))
		if err := uc.repo.UpdateFetchStateSubmitted(txCtx, orderNo, job.ID, string(job.Status), cooldownUntil, now); err != nil {
			return err
		}
		result = &FetchSubmitResult{
			Accepted:           true,
			Reason:             "created",
			JobID:              job.ID,
			Status:             string(job.Status),
			NextFetchAllowedAt: &cooldownUntil,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result != nil && result.Accepted {
		_ = uc.enqueueFetch(ctx, FetchTask{JobID: result.JobID})
	}
	return result, nil
}

func (uc *UseCase) ProcessFetch(ctx context.Context, task FetchTask) error {
	if task.JobID == 0 {
		return domain.ErrInvalidRequest
	}
	now := uc.now()
	claimed, err := uc.repo.ClaimFetchJobRunning(ctx, task.JobID, now)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	job, err := uc.repo.FindFetchJob(ctx, task.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return domain.ErrFetchJobNotFound
	}
	scope, err := uc.repo.LoadOrderScopeForServiceToken(ctx, job.OrderNo)
	if err != nil {
		_ = uc.repo.MarkFetchJobSkipped(ctx, task.JobID, "Order is not available for mail fetch.", uc.now())
		_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobSkipped), nil, "Order is not available for mail fetch.", uc.now())
		return nil
	}
	if !scopeFetchable(*scope, uc.now) {
		_ = uc.repo.MarkFetchJobSkipped(ctx, task.JobID, "Order is not available for mail fetch.", uc.now())
		_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobSkipped), nil, "Order is not available for mail fetch.", uc.now())
		return nil
	}
	fetched, fetchErr := uc.fetchMessages(ctx, *scope, *job)
	if fetchErr != nil {
		retry := job.Attempts < job.MaxAttempts
		_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Mail service is temporarily unavailable.", retry, uc.now())
		if !retry {
			_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), nil, "Mail service is temporarily unavailable.", uc.now())
		}
		return fetchErr
	}
	if strings.TrimSpace(fetched.RefreshToken) != "" &&
		strings.TrimSpace(fetched.RefreshToken) != strings.TrimSpace(scope.MicrosoftRT) &&
		scope.AllocationType == domain.ResourceTypeMicrosoft {
		if err := uc.repo.UpdateMicrosoftRefreshToken(ctx, scope.EmailResourceID, fetched.RefreshToken); err != nil {
			_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Microsoft credential refresh failed.", false, uc.now())
			_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), nil, "Microsoft credential refresh failed.", uc.now())
			return err
		}
	}
	messages := make([]domain.Message, 0, len(fetched.Messages))
	matchResults := make([]MatchResult, 0)
	matchSnapshots := make([]struct {
		scope   OrderScope
		message domain.Message
	}, 0)
	for _, item := range fetched.Messages {
		message, matchedScope, err := uc.fetchedMessageToDomain(ctx, item)
		if err != nil {
			_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Mail message matching failed.", false, uc.now())
			_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), nil, "Mail message matching failed.", uc.now())
			return err
		}
		messages = append(messages, message)
		if matchedScope != nil && strings.TrimSpace(message.VerificationCode) != "" {
			matchResults = append(matchResults, MatchResult{
				OrderNo:          matchedScope.OrderNo,
				VerificationCode: message.VerificationCode,
				MatchedAt:        message.ReceivedAt,
			})
			matchSnapshots = append(matchSnapshots, struct {
				scope   OrderScope
				message domain.Message
			}{scope: *matchedScope, message: message})
		}
	}
	stored, err := uc.repo.UpsertMessages(ctx, messages)
	if err != nil {
		_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Mail message storage failed.", false, uc.now())
		_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), nil, "Mail message storage failed.", uc.now())
		return err
	}
	lastReceivedAt := latestReceivedAt(messages)
	matched := countMatched(messages)
	for _, snapshot := range matchSnapshots {
		if err := uc.saveOrderSnapshot(ctx, snapshot.scope, snapshot.message); err != nil {
			_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Mail match snapshot storage failed.", false, uc.now())
			_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), lastReceivedAt, "Mail match snapshot storage failed.", uc.now())
			return err
		}
	}
	for _, result := range matchResults {
		if err := uc.matches.NotifyMatchedCode(ctx, result); err != nil {
			retry := job.Attempts < job.MaxAttempts
			_ = uc.repo.MarkFetchJobFailed(ctx, task.JobID, "Mail match result notification failed.", retry, uc.now())
			if !retry {
				_ = uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobFailed), lastReceivedAt, "Mail match result notification failed.", uc.now())
			}
			return err
		}
	}
	if err := uc.repo.MarkFetchJobSucceeded(ctx, task.JobID, len(messages), stored, matched, lastReceivedAt, uc.now()); err != nil {
		return err
	}
	if err := uc.repo.UpdateFetchStateCompleted(ctx, job.OrderNo, job.ID, string(domain.FetchJobSucceeded), lastReceivedAt, "", uc.now()); err != nil {
		return err
	}
	return nil
}

func (uc *UseCase) DispatchFetchJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	jobs, err := uc.repo.ClaimDispatchableFetchJobs(ctx, limit, uc.now().Add(-staleFetchRunningThreshold))
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, job := range jobs {
		if err := uc.enqueueFetch(ctx, FetchTask{JobID: job.ID}); err != nil {
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
	_, _ = uc.SubmitFetch(ctx, req)
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
		items, err := uc.repo.LoadDomainInboundMessages(ctx, scope, sinceAt, untilAt, purchaseReadLimit)
		if err != nil {
			return nil, err
		}
		return &FetchMessagesResult{Messages: items}, nil
	default:
		return nil, domain.ErrInvalidRequest
	}
}

func (uc *UseCase) enqueueFetch(ctx context.Context, task FetchTask) error {
	if uc.queue == nil {
		return domain.ErrFetchQueueUnavailable
	}
	if err := uc.queue.EnqueueFetch(ctx, task); err != nil {
		return err
	}
	_ = uc.repo.MarkFetchJobQueued(ctx, task.JobID)
	return nil
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
	if scope.ServiceMode == "code" {
		return scope.ReceiveUntil
	}
	if scope.ActivatedAt == nil {
		return scope.ReceiveUntil
	}
	return scope.AfterSaleUntil
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
			if matched && strings.TrimSpace(code) != "" {
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
		return message, &matches[0].scope, nil
	default:
		message.Status = domain.MessageStatusReceived
		message.MatchDiagnostic = "Message matched multiple active order services."
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
		RawSource:         strings.TrimSpace(item.RawSource),
		ProviderPayload:   strings.TrimSpace(item.ProviderPayload),
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

func filterMessagesForScope(messages []domain.Message, scope OrderScope, limit int) []domain.Message {
	if limit <= 0 {
		limit = purchaseReadLimit
	}
	out := make([]domain.Message, 0, minInt(limit, len(messages)))
	for _, message := range messages {
		matched, code, _ := matchAndExtractAnyRecipient(fetchedMessageFromDomain(message), scope)
		if !matched {
			continue
		}
		message.VerificationCode = code
		if scope.ServiceMode == "code" && strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, message)
		if len(out) >= limit {
			break
		}
	}
	return out
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
		RawSource:         message.RawSource,
		ProviderPayload:   message.ProviderPayload,
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func matchAndExtract(message FetchedMessage, scope OrderScope) (bool, string, string) {
	enabled := enabledRules(scope.Rules)
	if !matchRequiredRule(MailRuleRecipient, enabled, message, scope) {
		return false, "", "Message did not match recipient project mail rules."
	}
	if scope.LooseMatch {
		if !matchOptionalRule(MailRuleSender, enabled, message, scope) {
			return false, "", "Message did not match sender project mail rules."
		}
		if !matchOptionalRule(MailRuleSubject, enabled, message, scope) {
			return false, "", "Message did not match subject project mail rules."
		}
		code := extractVerificationCode(message.Body)
		if code == "" {
			return false, "", "Message did not contain an extractable verification code."
		}
		return true, code, ""
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

func matchOptionalRule(ruleType MailRuleType, enabled map[MailRuleType][]string, message FetchedMessage, scope OrderScope) bool {
	patterns := enabled[ruleType]
	return len(patterns) == 0 || matchAnyPattern(ruleType, patterns, message, scope)
}

func matchAnyPattern(ruleType MailRuleType, patterns []string, message FetchedMessage, scope OrderScope) bool {
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
		recipient := normalizeEmail(message.Recipient)
		switch strings.ToLower(pattern) {
		case "exact":
			return recipient != "" && recipient == normalizeEmail(scope.Recipient) && recipientKind(scope) == "exact"
		case "dot":
			return recipient != "" && recipient == normalizeEmail(scope.Recipient) && recipientKind(scope) == "dot"
		case "plus":
			return recipient != "" && recipient == normalizeEmail(scope.Recipient) && recipientKind(scope) == "plus"
		default:
			return regexMatch(pattern, recipient)
		}
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
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) <= 1000 {
		return value
	}
	return value[:1000]
}

func messageDedupeKey(item FetchedMessage) string {
	if messageID := strings.ToLower(strings.Trim(strings.TrimSpace(item.MessageIDHeader), "<>")); messageID != "" {
		return hashParts("message-id", messageID)
	}
	if providerMessageID := strings.ToLower(strings.TrimSpace(item.ProviderMessageID)); providerMessageID != "" {
		return hashParts(
			"provider",
			strings.ToLower(strings.TrimSpace(item.Protocol)),
			strings.ToLower(strings.TrimSpace(item.Folder)),
			providerMessageID,
		)
	}
	parts := []string{
		"fallback",
		strings.Join(fetchedRecipientCandidates(item), ","),
		strings.ToLower(strings.TrimSpace(item.Sender)),
		strings.TrimSpace(item.Subject),
		item.ReceivedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
		bodyHash(item.Body),
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
