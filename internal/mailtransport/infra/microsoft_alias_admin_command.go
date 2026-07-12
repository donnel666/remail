package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

var (
	_ mailapp.MicrosoftAliasAdminCommandStore = (*MicrosoftAliasStore)(nil)

	errMicrosoftAliasExpediteReceiptRace = errors.New("microsoft alias expedite receipt raced")
)

type MicrosoftAliasExpediteRequestModel struct {
	OperatorUserID uint      `gorm:"primaryKey;column:operator_user_id"`
	IdempotencyKey string    `gorm:"primaryKey;type:varchar(128);column:idempotency_key"`
	ResourceID     uint      `gorm:"not null;column:resource_id"`
	Reused         bool      `gorm:"not null;column:reused"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (MicrosoftAliasExpediteRequestModel) TableName() string {
	return "microsoft_alias_expedite_requests"
}

func (s *MicrosoftAliasStore) AcceptAdminAliasExpedite(
	ctx context.Context,
	command mailapp.MicrosoftAliasExpediteCommand,
	now time.Time,
	operationLog *governancedomain.OperationLog,
) (*mailapp.MicrosoftAliasExpediteResult, bool, error) {
	return s.acceptAdminAliasExpedite(ctx, command, now.UTC(), operationLog, true)
}

func (s *MicrosoftAliasStore) acceptAdminAliasExpedite(
	ctx context.Context,
	command mailapp.MicrosoftAliasExpediteCommand,
	now time.Time,
	operationLog *governancedomain.OperationLog,
	retryReceiptRace bool,
) (*mailapp.MicrosoftAliasExpediteResult, bool, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if s == nil || s.db == nil || command.ResourceID == 0 || command.OperatorUserID == 0 ||
		command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 || operationLog == nil {
		return nil, false, mailapp.ErrInvalidMicrosoftAliasExpedite
	}

	var result *mailapp.MicrosoftAliasExpediteResult
	receiptReused := false
	err := s.withAdminAliasTx(ctx, func(tx *gorm.DB) error {
		receipt, err := findMicrosoftAliasExpediteReceipt(tx, command.OperatorUserID, command.IdempotencyKey)
		if err != nil {
			return err
		}
		if receipt != nil {
			if receipt.ResourceID != command.ResourceID {
				return mailapp.ErrMicrosoftAliasIdempotencyConflict
			}
			result, err = loadMicrosoftAliasExpediteReplay(tx, command.ResourceID, receipt.CreatedAt)
			if err != nil {
				return err
			}
			receiptReused = true
			return s.createMicrosoftAliasExpediteOperationLog(ctx, tx, operationLog, command.ResourceID, true)
		}

		result, err = s.expediteAdminScheduleInTx(tx, command.ResourceID, now)
		if err != nil {
			return err
		}
		receipt = &MicrosoftAliasExpediteRequestModel{
			OperatorUserID: command.OperatorUserID,
			IdempotencyKey: command.IdempotencyKey,
			ResourceID:     command.ResourceID,
			Reused:         result.Reused,
			CreatedAt:      now,
		}
		if err := tx.Create(receipt).Error; err != nil {
			if isDuplicateKeyError(err) {
				return errMicrosoftAliasExpediteReceiptRace
			}
			return aliasAdminUnavailable("create expedite receipt", err)
		}
		return s.createMicrosoftAliasExpediteOperationLog(
			ctx,
			tx,
			operationLog,
			command.ResourceID,
			result.Reused,
		)
	})
	if errors.Is(err, errMicrosoftAliasExpediteReceiptRace) && retryReceiptRace {
		// A duplicate receipt rolls back the schedule change and success audit.
		// Retry through the authoritative receipt only when this method owns the
		// transaction; a caller-owned transaction cannot be safely restarted.
		if _, callerOwnsTx := platform.GormTxFromContext(ctx); !callerOwnsTx {
			return s.acceptAdminAliasExpedite(ctx, command, now, operationLog, false)
		}
	}
	if err != nil {
		if errors.Is(err, errMicrosoftAliasExpediteReceiptRace) {
			return nil, false, aliasAdminUnavailable("resolve expedite receipt", err)
		}
		return nil, false, err
	}
	return result, receiptReused, nil
}

func findMicrosoftAliasExpediteReceipt(
	tx *gorm.DB,
	operatorUserID uint,
	idempotencyKey string,
) (*MicrosoftAliasExpediteRequestModel, error) {
	var receipt MicrosoftAliasExpediteRequestModel
	err := tx.Where(
		"operator_user_id = ? AND idempotency_key = ?",
		operatorUserID,
		idempotencyKey,
	).Take(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, aliasAdminUnavailable("find expedite receipt", err)
	}
	return &receipt, nil
}

func loadMicrosoftAliasExpediteReplay(
	tx *gorm.DB,
	resourceID uint,
	receiptCreatedAt time.Time,
) (*mailapp.MicrosoftAliasExpediteResult, error) {
	var schedule MicrosoftAliasScheduleModel
	err := tx.Where("resource_id = ?", resourceID).Take(&schedule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, mailapp.ErrMicrosoftAliasScheduleNotFound
	}
	if err != nil {
		return nil, aliasAdminUnavailable("load idempotent expedite schedule", err)
	}
	status := "queued"
	if schedule.Status == "running" {
		status = "running"
	}
	nextRunAt := schedule.NextRunAt.UTC()
	queuedAt := receiptCreatedAt.UTC()
	if queuedAt.IsZero() {
		queuedAt = aliasAdminQueuedAt(schedule)
	}
	return &mailapp.MicrosoftAliasExpediteResult{
		Status:    status,
		Reused:    true,
		NextRunAt: &nextRunAt,
		QueuedAt:  queuedAt,
		StartedAt: aliasAdminStartedAt(schedule),
		UpdatedAt: schedule.UpdatedAt.UTC(),
	}, nil
}

func (s *MicrosoftAliasStore) createMicrosoftAliasExpediteOperationLog(
	ctx context.Context,
	tx *gorm.DB,
	operationLog *governancedomain.OperationLog,
	resourceID uint,
	reused bool,
) error {
	operationLog.SafeSummary = fmt.Sprintf(
		"Microsoft explicit-alias schedule expedite accepted; task=alias_schedule:%d; reused=%t.",
		resourceID,
		reused,
	)
	operationLogs := s.operationLogs
	if operationLogs == nil {
		operationLogs = governanceinfra.NewOperationLogRepo(s.db)
	}
	if err := operationLogs.CreateInTx(ctx, tx, operationLog); err != nil {
		return aliasAdminUnavailable("write expedite operation audit", err)
	}
	return nil
}
