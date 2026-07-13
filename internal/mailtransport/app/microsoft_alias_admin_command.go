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

var (
	ErrInvalidMicrosoftAliasExpedite     = errors.New("invalid microsoft alias expedite request")
	ErrMicrosoftAliasIdempotencyConflict = errors.New("microsoft alias expedite idempotency key conflict")
)

// MicrosoftAliasExpediteCommand is the administrator command boundary. It
// contains only audit and idempotency facts; alias candidates, credentials and
// schedule fencing tokens never cross this boundary.
type MicrosoftAliasExpediteCommand struct {
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

// MicrosoftAliasAdminCommandStore atomically advances the canonical schedule,
// records the idempotency receipt and writes the safe operation log. The
// boolean result is true when the command receipt already existed.
type MicrosoftAliasAdminCommandStore interface {
	AcceptAdminAliasExpedite(
		ctx context.Context,
		command MicrosoftAliasExpediteCommand,
		now time.Time,
		operationLog *governancedomain.OperationLog,
	) (*MicrosoftAliasExpediteResult, bool, error)
}

// AcceptAdminExpedite ensures an eligible resource has a canonical schedule,
// then accelerates it. It never creates an alias candidate or attempt, so
// quota, admission, reservation, fencing and reconciliation remain owned by
// the normal alias worker.
func (s *MicrosoftAliasService) AcceptAdminExpedite(
	ctx context.Context,
	command MicrosoftAliasExpediteCommand,
) (*AdminTaskAcceptedResult, error) {
	if s == nil {
		return nil, ErrMicrosoftAliasAdminUnavailable
	}
	store, ok := s.store.(MicrosoftAliasAdminCommandStore)
	if !ok || store == nil {
		return nil, ErrMicrosoftAliasAdminUnavailable
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.RequestID = strings.TrimSpace(command.RequestID)
	command.Path = strings.TrimSpace(command.Path)
	if command.ResourceID == 0 || command.OperatorUserID == 0 ||
		command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 {
		return nil, ErrInvalidMicrosoftAliasExpedite
	}
	if _, err := s.EnsureScheduleForResource(ctx, command.ResourceID); err != nil {
		return nil, ErrMicrosoftAliasAdminUnavailable
	}

	now := s.now().UTC()
	result, receiptReused, err := store.AcceptAdminAliasExpedite(ctx, command, now, &governancedomain.OperationLog{
		OperatorUserID: command.OperatorUserID,
		OperationType:  "mailtransport.microsoft_alias.expedite",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", command.ResourceID),
		Path:           command.Path,
		Result:         "success",
		SafeSummary:    "Microsoft explicit-alias schedule expedite accepted.",
		RequestID:      command.RequestID,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrMicrosoftAliasAdminUnavailable
	}
	result.ResourceID = command.ResourceID
	if result.TaskID == "" {
		result.TaskID = governanceapp.AdminTaskRef{
			Source: governanceapp.AdminTaskSourceAliasSchedule,
			ID:     uint64(command.ResourceID),
		}.String()
	}
	if result.WakeDispatcher {
		// The schedule, receipt and audit have committed before this call. A
		// transient enqueue failure is recovered by the periodic dispatcher.
		s.ScheduleDispatcher(ctx, 0)
	}
	return &AdminTaskAcceptedResult{
		Task:      microsoftAliasExpediteTaskView(*result, now),
		RequestID: command.RequestID,
		Reused:    receiptReused || result.Reused,
	}, nil
}

func microsoftAliasExpediteTaskView(result MicrosoftAliasExpediteResult, now time.Time) governanceapp.AdminTaskView {
	status := governanceapp.AdminTaskStatusQueued
	attempts := 0
	if result.Status == governanceapp.AdminTaskStatusRunning {
		status = governanceapp.AdminTaskStatusRunning
		attempts = 1
	}
	queuedAt := result.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = result.UpdatedAt
	}
	if queuedAt.IsZero() {
		queuedAt = now
	}
	updatedAt := result.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = queuedAt
	}
	return governanceapp.AdminTaskView{
		Ref: governanceapp.AdminTaskRef{
			Source: governanceapp.AdminTaskSourceAliasSchedule,
			ID:     uint64(result.ResourceID),
		},
		BizType:     governanceapp.AdminTaskBizMicrosoftResource,
		BizID:       uint64(result.ResourceID),
		Kind:        governanceapp.AdminTaskKindAlias,
		Status:      status,
		Attempts:    attempts,
		MaxAttempts: 1,
		QueuedAt:    queuedAt.UTC(),
		StartedAt:   utcAliasAdminTime(result.StartedAt),
		FinishedAt:  nil,
		UpdatedAt:   updatedAt.UTC(),
		Progress:    nil,
	}
}

func utcAliasAdminTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
