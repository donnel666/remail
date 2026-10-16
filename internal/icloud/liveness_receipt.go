package icloud

import (
	"context"
	"errors"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm/clause"
)

type LivenessReceiptStore interface {
	StoreLivenessReceipt(context.Context, mailmatchapp.InboundMailRequest, func(context.Context) error) error
}

const iCloudLivenessReceiptStoragePending = "Liveness email was received; retrying resource inbox storage."

func (s *Service) SetLivenessReceiptStore(store LivenessReceiptStore) { s.livenessReceipts = store }

func (s *Service) storeICloudLivenessReceipt(ctx context.Context, run iCloudMaintenanceRunModel, message MailFetchedMessage) error {
	if s.livenessReceipts == nil {
		return ErrICloudMailUnavailable
	}
	return s.livenessReceipts.StoreLivenessReceipt(ctx, mailmatchapp.InboundMailRequest{
		EmailResourceID: run.ResourceID, ResourceType: mailmatchdomain.ResourceTypeICloud,
		Recipient: message.Recipient, EnvelopeFrom: message.Sender, Raw: message.Raw,
		ReceivedAt: message.ReceivedAt, ProviderMessageID: message.ProviderMessageID, Protocol: "smtp", Folder: "inbound",
	}, func(txCtx context.Context) error {
		tx, ok := platform.GormTxFromContext(txCtx)
		if !ok {
			return ErrICloudValidationTemp
		}
		var root iCloudRootModel
		if err := tx.WithContext(txCtx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&root, run.ResourceID).Error; err != nil {
			return err
		}
		var resource iCloudResourceModel
		if err := tx.WithContext(txCtx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, run.ResourceID).Error; err != nil {
			return err
		}
		if !currentICloudLivenessRun(resource, run) {
			return errICloudValidationStale
		}
		var current iCloudMaintenanceRunModel
		if err := tx.WithContext(txCtx).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current, run.ID).Error; err != nil {
			return err
		}
		if current.Attempts != run.Attempts || current.StartedAt == nil || run.StartedAt == nil || !current.StartedAt.Equal(*run.StartedAt) ||
			(current.Status != iCloudMaintenanceRunning && current.Status != iCloudMaintenanceSucceeded) {
			return errICloudValidationStale
		}
		return nil
	})
}

func (s *Service) retryICloudLivenessReceiptStorage(ctx context.Context, run iCloudMaintenanceRunModel, err error) error {
	if errors.Is(err, errICloudValidationStale) {
		return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceCanceled, "Liveness check canceled because the resource changed.", nil)
	}
	now := s.now().UTC()
	window := iCloudLivenessReceiptWait
	if run.Kind != iCloudMaintenanceLivenessSMTP && run.ProbeSentAt != nil {
		window = iCloudLivenessLateBounceWindow
	}
	if run.StartedAt == nil || !now.Before(run.StartedAt.Add(window)) {
		return s.finishICloudLivenessCheck(ctx, run, iCloudResourceNormal, iCloudMaintenanceSucceeded,
			"Liveness succeeded; the received email could not be saved to the resource inbox.", nil)
	}
	next := now.Add(iCloudLivenessReceiptPoll)
	if deadline := run.StartedAt.Add(window); next.After(deadline) {
		next = deadline
	}
	return s.finishICloudLivenessCheck(ctx, run, iCloudResourceNormal, iCloudMaintenanceSucceeded, iCloudLivenessReceiptStoragePending, &next)
}
