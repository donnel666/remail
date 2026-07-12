package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

const (
	MicrosoftTokenRefreshQueued    = "queued"
	MicrosoftTokenRefreshRunning   = "running"
	MicrosoftTokenRefreshSucceeded = "succeeded"
	MicrosoftTokenRefreshFailed    = "failed"
	MicrosoftTokenRefreshCanceled  = "canceled"

	MicrosoftTokenRefreshDefaultMaxAttempts = 3

	microsoftTokenRefreshRunningStaleAfter = 20 * time.Minute
	microsoftTokenRefreshDispatchLease     = 4 * time.Hour
)

var (
	ErrInvalidMicrosoftTokenRefresh      = errors.New("invalid microsoft token refresh request")
	ErrMicrosoftTokenRefreshNotFound     = errors.New("microsoft token refresh resource not found")
	ErrMicrosoftTokenRefreshConflict     = errors.New("microsoft token refresh resource state conflict")
	ErrMicrosoftTokenCredentialsMissing  = errors.New("microsoft token refresh credentials are missing")
	ErrMicrosoftAdminIdempotencyConflict = errors.New("administrator idempotency key conflict")
	ErrMicrosoftTokenRefreshUnavailable  = errors.New("microsoft token refresh is temporarily unavailable")
	ErrMicrosoftTokenRefreshStale        = errors.New("microsoft token refresh result is stale")
)

type MicrosoftTokenRefreshJob struct {
	ID                         uint64
	ResourceID                 uint
	OperatorUserID             uint
	ExpectedCredentialRevision uint64
	Status                     string
	Attempts                   int
	MaxAttempts                int
	ClaimToken                 string
	DispatchToken              string
	LastSafeError              string
	RequestID                  string
	Path                       string
	DispatchedAt               *time.Time
	StartedAt                  *time.Time
	FinishedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type MicrosoftTokenRefreshCommand struct {
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type MicrosoftTokenRefreshTask struct {
	JobID         uint64 `json:"jobId"`
	ResourceID    uint   `json:"resourceId"`
	DispatchToken string `json:"dispatchToken"`
	RequestID     string `json:"requestId"`
}

type MicrosoftTokenRefreshExecution struct {
	Job          MicrosoftTokenRefreshJob
	EmailAddress string
	ClientID     string
	RefreshToken string
}

type MicrosoftTokenRefreshProtocolRequest struct {
	ResourceID   uint
	EmailAddress string
	ClientID     string
	RefreshToken string
	RequestID    string
}

type MicrosoftTokenRefreshProtocolResult struct {
	Valid        bool
	ClientID     string
	RefreshToken string
	Category     string
	SafeMessage  string
}

type MicrosoftTokenRefreshRepository interface {
	CreateOrReuse(ctx context.Context, command MicrosoftTokenRefreshCommand, operationLog *governancedomain.OperationLog) (*MicrosoftTokenRefreshJob, bool, error)
	ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore, dispatchStaleBefore time.Time) ([]MicrosoftTokenRefreshJob, error)
	MarkDispatchFailed(ctx context.Context, id uint64, dispatchToken, safeError string) error
	ReleaseDispatch(ctx context.Context, id uint64, dispatchToken string) error
	ClaimExecution(ctx context.Context, id uint64, dispatchToken string, runningStaleBefore time.Time) (*MicrosoftTokenRefreshExecution, bool, error)
	MarkRetryableFailure(ctx context.Context, id uint64, claimToken, safeError string) (bool, error)
	ApplyResult(ctx context.Context, id uint64, claimToken string, result MicrosoftTokenRefreshProtocolResult) error
}

type MicrosoftTokenRefreshQueue interface {
	EnqueueMicrosoftTokenRefresh(ctx context.Context, task MicrosoftTokenRefreshTask) error
	EnqueueMicrosoftTokenRefreshDispatcher(ctx context.Context, delay time.Duration) error
}

type MicrosoftTokenRefresher interface {
	RefreshMicrosoftToken(ctx context.Context, request MicrosoftTokenRefreshProtocolRequest) (MicrosoftTokenRefreshProtocolResult, error)
}

type MicrosoftTokenRefreshService struct {
	repo      MicrosoftTokenRefreshRepository
	queue     MicrosoftTokenRefreshQueue
	refresher MicrosoftTokenRefresher
	now       func() time.Time
}

func NewMicrosoftTokenRefreshService(repo MicrosoftTokenRefreshRepository, queue MicrosoftTokenRefreshQueue, refresher MicrosoftTokenRefresher) *MicrosoftTokenRefreshService {
	return &MicrosoftTokenRefreshService{
		repo:      repo,
		queue:     queue,
		refresher: refresher,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (s *MicrosoftTokenRefreshService) Accept(ctx context.Context, command MicrosoftTokenRefreshCommand) (*AdminTaskAcceptedResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrMicrosoftTokenRefreshUnavailable
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.RequestID = strings.TrimSpace(command.RequestID)
	command.Path = strings.TrimSpace(command.Path)
	if command.ResourceID == 0 || command.OperatorUserID == 0 || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 {
		return nil, ErrInvalidMicrosoftTokenRefresh
	}
	job, reused, err := s.repo.CreateOrReuse(ctx, command, &governancedomain.OperationLog{
		OperatorUserID: command.OperatorUserID,
		OperationType:  "mailtransport.microsoft_token_refresh.accept",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", command.ResourceID),
		Path:           command.Path,
		Result:         "success",
		SafeSummary:    "Microsoft refresh-token diagnostic accepted.",
		RequestID:      command.RequestID,
	})
	if err != nil {
		return nil, err
	}
	s.ScheduleDispatcher(ctx, 0)
	return &AdminTaskAcceptedResult{
		Task: microsoftTokenRefreshTaskView(*job), RequestID: job.RequestID, Reused: reused,
	}, nil
}

type MicrosoftTokenRefreshDispatchResult struct {
	Attempted int
	Queued    int
	Failed    int
}

func (s *MicrosoftTokenRefreshService) DispatchPending(ctx context.Context, limit int) (*MicrosoftTokenRefreshDispatchResult, error) {
	if s == nil || s.repo == nil || s.queue == nil {
		return nil, ErrMicrosoftTokenRefreshUnavailable
	}
	if limit <= 0 {
		limit = 32
	}
	now := s.now().UTC()
	jobs, err := s.repo.ClaimDispatchable(
		ctx,
		limit,
		now.Add(-microsoftTokenRefreshRunningStaleAfter),
		now.Add(-microsoftTokenRefreshDispatchLease),
	)
	if err != nil {
		return nil, err
	}
	result := &MicrosoftTokenRefreshDispatchResult{Attempted: len(jobs)}
	var dispatchErr error
	for i := range jobs {
		job := jobs[i]
		if err := s.queue.EnqueueMicrosoftTokenRefresh(ctx, MicrosoftTokenRefreshTask{
			JobID:         job.ID,
			ResourceID:    job.ResourceID,
			DispatchToken: job.DispatchToken,
			RequestID:     job.RequestID,
		}); err != nil {
			result.Failed++
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("enqueue microsoft token refresh %d: %w", job.ID, err))
			if releaseErr := s.repo.MarkDispatchFailed(ctx, job.ID, job.DispatchToken, "Microsoft token refresh queue is unavailable; dispatcher will retry."); releaseErr != nil {
				dispatchErr = errors.Join(dispatchErr, fmt.Errorf("release microsoft token refresh %d: %w", job.ID, releaseErr))
			}
			continue
		}
		result.Queued++
	}
	return result, dispatchErr
}

func (s *MicrosoftTokenRefreshService) Process(ctx context.Context, task MicrosoftTokenRefreshTask) error {
	if s == nil || s.repo == nil || task.JobID == 0 {
		return ErrMicrosoftTokenRefreshNotFound
	}
	if strings.TrimSpace(task.DispatchToken) == "" {
		return nil
	}
	execution, claimed, err := s.repo.ClaimExecution(
		ctx,
		task.JobID,
		task.DispatchToken,
		s.now().UTC().Add(-microsoftTokenRefreshRunningStaleAfter),
	)
	if err != nil || !claimed || execution == nil {
		return err
	}
	result := MicrosoftTokenRefreshProtocolResult{
		Category:    "request",
		SafeMessage: "Microsoft mail service is temporarily unavailable.",
	}
	if s.refresher != nil {
		result, err = s.refresher.RefreshMicrosoftToken(ctx, MicrosoftTokenRefreshProtocolRequest{
			ResourceID:   execution.Job.ResourceID,
			EmailAddress: execution.EmailAddress,
			ClientID:     execution.ClientID,
			RefreshToken: execution.RefreshToken,
			RequestID:    execution.Job.RequestID,
		})
		if err != nil {
			result = MicrosoftTokenRefreshProtocolResult{
				Category:    "request",
				SafeMessage: "Microsoft mail service is temporarily unavailable.",
			}
		}
	}
	result.Category = normalizeMicrosoftTokenRefreshCategory(result.Category)
	result.SafeMessage = microsoftTokenRefreshSafeMessage(result.Valid, result.Category)
	if !result.Valid && isRetryableMicrosoftTokenRefreshCategory(result.Category) {
		exhausted, retryErr := s.repo.MarkRetryableFailure(ctx, execution.Job.ID, execution.Job.ClaimToken, result.SafeMessage)
		if retryErr != nil {
			return retryErr
		}
		if !exhausted {
			s.ScheduleDispatcher(ctx, time.Second)
		}
		return nil
	}
	err = s.repo.ApplyResult(ctx, execution.Job.ID, execution.Job.ClaimToken, result)
	if errors.Is(err, ErrMicrosoftTokenRefreshStale) {
		return nil
	}
	return err
}

func (s *MicrosoftTokenRefreshService) ReleaseDispatch(ctx context.Context, task MicrosoftTokenRefreshTask) error {
	if s == nil || s.repo == nil || task.JobID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return nil
	}
	return s.repo.ReleaseDispatch(ctx, task.JobID, task.DispatchToken)
}

func (s *MicrosoftTokenRefreshService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	_ = s.queue.EnqueueMicrosoftTokenRefreshDispatcher(ctx, delay)
}

func microsoftTokenRefreshTaskView(job MicrosoftTokenRefreshJob) governanceapp.AdminTaskView {
	revision := job.ExpectedCredentialRevision
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = MicrosoftTokenRefreshDefaultMaxAttempts
	}
	return governanceapp.AdminTaskView{
		Ref:                governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceToken, ID: job.ID},
		BizType:            governanceapp.AdminTaskBizMicrosoftResource,
		BizID:              uint64(job.ResourceID),
		Kind:               governanceapp.AdminTaskKindToken,
		Status:             job.Status,
		Attempts:           job.Attempts,
		MaxAttempts:        maxAttempts,
		CredentialRevision: &revision,
		QueuedAt:           job.CreatedAt,
		StartedAt:          job.StartedAt,
		FinishedAt:         job.FinishedAt,
		UpdatedAt:          job.UpdatedAt,
	}
}

func isRetryableMicrosoftTokenRefreshCategory(category string) bool {
	switch strings.TrimSpace(category) {
	case "request", "auth_timeout", "rate_limited":
		return true
	default:
		return false
	}
}

func normalizeMicrosoftTokenRefreshCategory(category string) string {
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "request", "auth_timeout", "rate_limited", "oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password", "unknown_mailbox", "locked":
		return category
	default:
		return "request"
	}
}

func microsoftTokenRefreshSafeMessage(valid bool, category string) string {
	if valid {
		return "Microsoft refresh-token diagnostic succeeded."
	}
	switch category {
	case "oauth_invalid_grant":
		return "Microsoft refresh token is invalid or expired."
	case "oauth_client":
		return "Microsoft OAuth client is invalid or not allowed."
	case "oauth_permission":
		return "Microsoft OAuth permission is not available."
	case "mfa":
		return "Microsoft account requires authenticator verification."
	case "passkey":
		return "Microsoft account requires passkey verification."
	case "phone":
		return "Microsoft account requires phone verification."
	case "password":
		return "Microsoft account password is incorrect."
	case "unknown_mailbox":
		return "Microsoft account does not exist or recovery mailbox is not supported."
	case "locked":
		return "Microsoft account is locked."
	case "rate_limited":
		return "Microsoft mail service is rate limited."
	default:
		return "Microsoft mail service is temporarily unavailable."
	}
}
