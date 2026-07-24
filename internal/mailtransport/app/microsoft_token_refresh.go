package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	MicrosoftTokenRefreshPending    = "pending"
	MicrosoftTokenRefreshProcessing = "processing"
	MicrosoftTokenRefreshNormal     = "normal"
	MicrosoftTokenRefreshAbnormal   = "abnormal"

	MicrosoftTokenRefreshDefaultMaxAttempts = 3
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

type MicrosoftTokenRefreshState struct {
	ResourceID                 uint
	Generation                 uint64
	OperatorUserID             uint
	ExpectedCredentialRevision uint64
	Status                     string
	Failures                   int
	LastSafeError              string
	IdempotencyKey             string
	RequestID                  string
	Path                       string
	RequestedAt                *time.Time
	StartedAt                  *time.Time
	FinishedAt                 *time.Time
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
	ResourceID                 uint   `json:"resourceId"`
	Generation                 uint64 `json:"generation"`
	ExpectedCredentialRevision uint64 `json:"expectedCredentialRevision"`
	RequestID                  string `json:"requestId"`
}

type MicrosoftTokenRefreshExecution struct {
	State        MicrosoftTokenRefreshState
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
	Request(ctx context.Context, command MicrosoftTokenRefreshCommand, operationLog *governancedomain.OperationLog) (*MicrosoftTokenRefreshState, bool, error)
	ListPending(ctx context.Context, limit int) ([]MicrosoftTokenRefreshState, error)
	MarkProcessing(ctx context.Context, resourceID uint, generation uint64) (bool, error)
	ReleaseInfrastructureFailure(ctx context.Context, resourceID uint, generation uint64, safeError string) (bool, error)
	LoadExecution(ctx context.Context, task MicrosoftTokenRefreshTask) (*MicrosoftTokenRefreshExecution, bool, error)
	RecordRetryableFailure(ctx context.Context, task MicrosoftTokenRefreshTask, safeError string) (abnormal bool, err error)
	ApplyResult(ctx context.Context, task MicrosoftTokenRefreshTask, result MicrosoftTokenRefreshProtocolResult) error
}

type MicrosoftTokenRefreshQueue interface {
	EnqueueMicrosoftTokenRefresh(ctx context.Context, task MicrosoftTokenRefreshTask) (bool, error)
	EnqueueMicrosoftTokenRefreshDispatcher(ctx context.Context, delay time.Duration) error
}

type MicrosoftTokenRefresher interface {
	RefreshMicrosoftToken(ctx context.Context, request MicrosoftTokenRefreshProtocolRequest) (MicrosoftTokenRefreshProtocolResult, error)
}

type MicrosoftTokenRefreshService struct {
	repo      MicrosoftTokenRefreshRepository
	queue     MicrosoftTokenRefreshQueue
	refresher MicrosoftTokenRefresher
}

func NewMicrosoftTokenRefreshService(repo MicrosoftTokenRefreshRepository, queue MicrosoftTokenRefreshQueue, refresher MicrosoftTokenRefresher) *MicrosoftTokenRefreshService {
	return &MicrosoftTokenRefreshService{repo: repo, queue: queue, refresher: refresher}
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
	state, reused, err := s.repo.Request(ctx, command, &governancedomain.OperationLog{
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
		Task: microsoftTokenRefreshTaskView(*state), RequestID: state.RequestID, Reused: reused,
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
	states, err := s.repo.ListPending(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &MicrosoftTokenRefreshDispatchResult{Attempted: len(states)}
	var dispatchErr error
	for _, state := range states {
		task := MicrosoftTokenRefreshTask{
			ResourceID:                 state.ResourceID,
			Generation:                 state.Generation,
			ExpectedCredentialRevision: state.ExpectedCredentialRevision,
			RequestID:                  state.RequestID,
		}
		accepted, enqueueErr := s.queue.EnqueueMicrosoftTokenRefresh(ctx, task)
		if enqueueErr != nil {
			result.Failed++
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("enqueue microsoft token refresh %d: %w", state.ResourceID, enqueueErr))
			continue
		}
		if !accepted {
			continue
		}
		processing, processingErr := s.repo.MarkProcessing(ctx, state.ResourceID, state.Generation)
		if processingErr != nil {
			result.Failed++
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("activate microsoft token refresh %d: %w", state.ResourceID, processingErr))
			continue
		}
		if processing {
			result.Queued++
		}
	}
	return result, dispatchErr
}

func (s *MicrosoftTokenRefreshService) Process(ctx context.Context, task MicrosoftTokenRefreshTask) error {
	if s == nil || s.repo == nil || task.ResourceID == 0 || task.Generation == 0 {
		return ErrMicrosoftTokenRefreshNotFound
	}
	if _, err := s.repo.MarkProcessing(ctx, task.ResourceID, task.Generation); err != nil {
		return err
	}
	execution, current, err := s.repo.LoadExecution(ctx, task)
	if err != nil {
		s.releaseInfrastructureFailure(ctx, task)
		return err
	}
	if !current || execution == nil {
		return nil
	}
	if s.refresher == nil {
		s.releaseInfrastructureFailure(ctx, task)
		return ErrMicrosoftTokenRefreshUnavailable
	}
	result, err := s.refresher.RefreshMicrosoftToken(ctx, MicrosoftTokenRefreshProtocolRequest{
		ResourceID:   execution.State.ResourceID,
		EmailAddress: execution.EmailAddress,
		ClientID:     execution.ClientID,
		RefreshToken: execution.RefreshToken,
		RequestID:    execution.State.RequestID,
	})
	if err != nil {
		s.releaseInfrastructureFailure(ctx, task)
		return err
	}
	result.Category = normalizeMicrosoftTokenRefreshCategory(result.Category)
	result.SafeMessage = microsoftTokenRefreshSafeMessage(result.Valid, result.Category)
	if !result.Valid && isRetryableMicrosoftTokenRefreshCategory(result.Category) {
		abnormal, retryErr := s.repo.RecordRetryableFailure(ctx, task, result.SafeMessage)
		if retryErr != nil {
			if errors.Is(retryErr, ErrMicrosoftTokenRefreshUnavailable) {
				s.releaseInfrastructureFailure(ctx, task)
			}
			return retryErr
		}
		if !abnormal {
			s.ScheduleDispatcher(ctx, time.Second)
		}
		return nil
	}
	err = s.repo.ApplyResult(ctx, task, result)
	if errors.Is(err, ErrMicrosoftTokenRefreshStale) {
		return nil
	}
	if errors.Is(err, ErrMicrosoftTokenRefreshUnavailable) {
		s.releaseInfrastructureFailure(ctx, task)
	}
	return err
}

func (s *MicrosoftTokenRefreshService) ReleaseDispatch(ctx context.Context, task MicrosoftTokenRefreshTask) error {
	if s == nil || s.repo == nil || task.ResourceID == 0 || task.Generation == 0 {
		return nil
	}
	_, err := s.repo.ReleaseInfrastructureFailure(ctx, task.ResourceID, task.Generation, "")
	return err
}

func (s *MicrosoftTokenRefreshService) releaseInfrastructureFailure(ctx context.Context, task MicrosoftTokenRefreshTask) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.repo.ReleaseInfrastructureFailure(
		releaseCtx,
		task.ResourceID,
		task.Generation,
		"Microsoft token refresh infrastructure failed; dispatcher will retry.",
	)
}

func (s *MicrosoftTokenRefreshService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	_ = s.queue.EnqueueMicrosoftTokenRefreshDispatcher(ctx, delay)
}

func microsoftTokenRefreshTaskView(state MicrosoftTokenRefreshState) governanceapp.AdminTaskView {
	revision := state.ExpectedCredentialRevision
	queuedAt := state.UpdatedAt
	if state.RequestedAt != nil {
		queuedAt = *state.RequestedAt
	}
	return governanceapp.AdminTaskView{
		Ref:                governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceToken, ID: uint64(state.ResourceID)},
		BizType:            governanceapp.AdminTaskBizMicrosoftResource,
		BizID:              uint64(state.ResourceID),
		Kind:               governanceapp.AdminTaskKindToken,
		Status:             microsoftTokenRefreshAdminStatus(state.Status),
		Attempts:           state.Failures,
		MaxAttempts:        runtimeconfig.Int("token_refresh_max_attempts", MicrosoftTokenRefreshDefaultMaxAttempts, 1),
		CredentialRevision: &revision,
		QueuedAt:           queuedAt,
		StartedAt:          state.StartedAt,
		FinishedAt:         state.FinishedAt,
		UpdatedAt:          state.UpdatedAt,
	}
}

func microsoftTokenRefreshAdminStatus(status string) string {
	switch status {
	case MicrosoftTokenRefreshPending:
		return governanceapp.AdminTaskStatusQueued
	case MicrosoftTokenRefreshProcessing:
		return governanceapp.AdminTaskStatusRunning
	case MicrosoftTokenRefreshAbnormal:
		return governanceapp.AdminTaskStatusFailed
	default:
		return governanceapp.AdminTaskStatusSucceeded
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
	case "request", "auth_timeout", "rate_limited", "oauth_invalid_grant", "oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password", "unknown_mailbox", "locked", "success":
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
