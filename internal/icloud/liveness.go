package icloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	stdmail "net/mail"
	"net/textproto"
	"slices"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudMaintenanceLiveness      = "liveness"
	typeICloudLiveness             = "icloud:resource_liveness"
	iCloudLivenessSendTimeout      = time.Minute
	iCloudLivenessBounceWait       = 2 * time.Minute
	iCloudLivenessCheckInterval    = 5 * time.Minute
	iCloudLivenessLateBounceWindow = 24 * time.Hour
)

var (
	ErrICloudLivenessNoAlias     = errors.New("icloud: no usable alias for liveness check")
	ErrICloudLivenessUnavailable = errors.New("icloud: liveness mail transport unavailable")
)

func (s *Service) SetLivenessMail(sender mailapp.SenderPort, inbox *mailapp.InboundService, from string) {
	s.livenessSender, s.livenessInbox, s.livenessFrom = sender, inbox, from
}

func (s *Service) livenessProbeSender() string {
	from, err := stdmail.ParseAddress(s.livenessFrom)
	if err != nil || strings.ContainsAny(from.Address, "\r\n") {
		return ""
	}
	_, domain, ok := strings.Cut(from.Address, "@")
	if !ok || domain == "" {
		return ""
	}
	return "icloud-check-" + platform.NewUUIDV7String() + "@" + strings.ToLower(domain)
}

func (s *Service) checkICloudLivenessMail(ctx context.Context) error {
	if s.livenessSender == nil || s.livenessInbox == nil || s.files == nil {
		return ErrICloudLivenessUnavailable
	}
	from := s.livenessProbeSender()
	if from == "" {
		return ErrICloudLivenessUnavailable
	}
	recipient, err := s.livenessInbox.ResolveRecipient(ctx, from)
	if err != nil || recipient == nil || recipient.ResourceType != maildomain.InboundResourceDomain {
		return ErrICloudLivenessUnavailable
	}
	return nil
}

func queueAdminICloudLivenessTx(ctx context.Context, tx *gorm.DB, root iCloudRootModel, resource iCloudResourceModel, now time.Time) (*AdminICloudMutationResult, bool, error) {
	if resource.Status == iCloudResourceDeleted {
		return nil, false, ErrICloudResourceNotFound
	}
	if resource.Status == iCloudResourceDisabled || resource.Status == iCloudResourceValidating {
		return nil, false, ErrICloudResourceStatus
	}
	var active int64
	if err := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("resource_id = ? AND kind = ? AND status IN ?", resource.ID, iCloudMaintenanceLiveness, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning}).Count(&active).Error; err != nil {
		return nil, false, err
	}
	if active > 0 {
		return adminICloudMutationResult(root, resource), false, nil
	}
	var alias iCloudAliasModel
	if err := tx.WithContext(ctx).Where("resource_id = ? AND status = ?", resource.ID, iCloudResourceNormal).Order("id ASC").Take(&alias).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudLivenessNoAlias
		}
		return nil, false, err
	}
	address, err := stdmail.ParseAddress(alias.Email)
	if err != nil || address.Address != alias.Email || strings.ContainsAny(alias.Email, "\r\n") {
		return nil, false, ErrICloudLivenessNoAlias
	}
	var aliasCount int64
	if err := tx.WithContext(ctx).Model(&iCloudAliasModel{}).
		Where("resource_id = ? AND status = ?", resource.ID, iCloudResourceNormal).Count(&aliasCount).Error; err != nil {
		return nil, false, err
	}
	generation := resource.ValidationGeneration + 1
	if err := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("resource_id = ? AND kind = ? AND status IN ?", resource.ID, iCloudMaintenanceValidation, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning}).
		Updates(map[string]any{"status": iCloudMaintenanceCanceled, "finished_at": now, "updated_at": now}).Error; err != nil {
		return nil, false, err
	}
	run := iCloudMaintenanceRunModel{
		ResourceID: resource.ID, Kind: iCloudMaintenanceLiveness, Status: iCloudMaintenanceQueued,
		ValidationGeneration: generation, CredentialRevision: resource.CredentialRevision,
		MaxAttempts: int(aliasCount), ProbeRecipient: alias.Email, NextCheckAt: &now,
		QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, false, err
	}
	if err := tx.WithContext(ctx).Model(&resource).Updates(map[string]any{"validation_generation": generation, "updated_at": now}).Error; err != nil {
		return nil, false, err
	}
	if err := tx.WithContext(ctx).Model(&root).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
		return nil, false, err
	}
	root.Version++
	return adminICloudMutationResult(root, resource), true, nil
}

func (s *Service) DispatchICloudLivenessChecks(ctx context.Context) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	now := s.now().UTC()
	var runs []iCloudMaintenanceRunModel
	if err := s.db.WithContext(ctx).Where("kind = ? AND next_check_at <= ? AND status IN ?",
		iCloudMaintenanceLiveness, now, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning, iCloudMaintenanceSucceeded}).
		Order("next_check_at ASC, id ASC").Limit(100).Find(&runs).Error; err != nil {
		return err
	}
	var joined error
	for _, run := range runs {
		payload, err := json.Marshal(struct {
			RunID uint64 `json:"runId"`
		}{run.ID})
		if err != nil {
			return err
		}
		_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudLiveness, payload),
			asynq.Queue(platform.QueueBackgroundICloudValidation), asynq.MaxRetry(0),
			asynq.Timeout(2*time.Minute), asynq.Unique(2*time.Minute), asynq.Retention(0))
		if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (s *Service) ProcessICloudLivenessCheck(ctx context.Context, runID uint64) error {
	var run iCloudMaintenanceRunModel
	if err := s.db.WithContext(ctx).Where("id = ? AND kind = ?", runID, iCloudMaintenanceLiveness).Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if run.Status != iCloudMaintenanceQueued && run.Status != iCloudMaintenanceRunning && run.Status != iCloudMaintenanceSucceeded {
		return nil
	}
	var resource iCloudResourceModel
	if err := s.db.WithContext(ctx).Take(&resource, run.ResourceID).Error; err != nil {
		return err
	}
	if !currentICloudLivenessRun(resource, run) {
		return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceCanceled, "Liveness check canceled because the resource changed.")
	}
	now := s.now().UTC()
	if run.NextCheckAt != nil && run.NextCheckAt.After(now) {
		return nil
	}
	if run.Status == iCloudMaintenanceQueued {
		if err := s.checkICloudLivenessMail(ctx); err != nil {
			return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "SMTP liveness mail transport is unavailable.")
		}
		run.ProbeSender = s.livenessProbeSender()
		lease := now.Add(2 * time.Minute)
		claimed := s.db.WithContext(ctx).Model(&run).
			Where("status = ? AND probe_recipient = ? AND attempts = ? AND attempts < max_attempts", iCloudMaintenanceQueued, run.ProbeRecipient, run.Attempts).
			Updates(map[string]any{"status": iCloudMaintenanceRunning, "attempts": run.Attempts + 1, "started_at": now, "probe_sender": run.ProbeSender, "next_check_at": lease, "updated_at": now})
		if claimed.Error != nil || claimed.RowsAffected == 0 {
			return claimed.Error
		}
		sendCtx, cancel := context.WithTimeout(ctx, iCloudLivenessSendTimeout)
		err := s.livenessSender.Send(sendCtx, maildomain.OutboundMessage{
			Purpose: maildomain.PurposeSystemNotice, From: run.ProbeSender, To: run.ProbeRecipient,
			Subject: "ReMail iCloud 邮箱测活", TextBody: "这是一封 iCloud 别名邮箱测活邮件，无需回复。",
		})
		cancel()
		if err != nil {
			var failure *mailapp.OutboundSendFailure
			if errors.As(err, &failure) {
				if failure.RecipientRejected {
					return s.finishICloudLivenessCheck(ctx, run, iCloudResourceAbnormal, iCloudMaintenanceFailed, "iCloud alias rejected the liveness email.")
				}
				if failure.Retryable && ctx.Err() == nil {
					if retrying, retryErr := s.retryICloudLivenessAlias(ctx, run); retryErr != nil || retrying {
						return retryErr
					}
				}
			}
			return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "Liveness email could not be sent; account health is unchanged.")
		}
		now = s.now().UTC()
		return s.db.WithContext(ctx).Model(&run).Where("status = ?", iCloudMaintenanceRunning).
			Updates(map[string]any{"probe_sent_at": now, "next_check_at": now.Add(iCloudLivenessBounceWait), "updated_at": now}).Error
	}
	if run.NextCheckAt == nil || run.NextCheckAt.After(now) {
		return nil
	}
	if run.ProbeSentAt == nil {
		return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "Liveness sending worker expired; account health is unchanged.")
	}
	bounced, err := s.iCloudLivenessBounced(ctx, run)
	if err != nil {
		var failure *mailapp.OutboundSendFailure
		if errors.As(err, &failure) {
			return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, failure.SafeMessage)
		}
		now = s.now().UTC()
		deadline := run.ProbeSentAt.Add(iCloudLivenessLateBounceWindow)
		if !now.Before(deadline) {
			return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "Liveness bounce inspection expired; account health is unchanged.")
		}
		next := now.Add(iCloudLivenessCheckInterval)
		if next.After(deadline) {
			next = deadline
		}
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
		defer cancel()
		if updateErr := s.db.WithContext(retryCtx).Model(&run).
			Where("status = ? AND next_check_at = ?", run.Status, *run.NextCheckAt).
			Updates(map[string]any{"next_check_at": next, "last_safe_error": "Liveness bounce inspection is temporarily unavailable; retrying.", "updated_at": now}).Error; updateErr != nil {
			return updateErr
		}
		return err
	}
	if bounced {
		return s.finishICloudLivenessCheck(ctx, run, iCloudResourceAbnormal, iCloudMaintenanceFailed, "iCloud alias bounced the liveness email.")
	}
	if run.Status == iCloudMaintenanceRunning {
		// shortcut: SMTP acceptance without a bounce counts as healthy, require a received probe when inbox delivery must be confirmed.
		return s.finishICloudLivenessCheck(ctx, run, iCloudResourceNormal, iCloudMaintenanceSucceeded, "")
	}
	var nextCheck *time.Time
	if now.Before(run.ProbeSentAt.Add(iCloudLivenessLateBounceWindow)) {
		next := now.Add(iCloudLivenessCheckInterval)
		nextCheck = &next
	}
	return s.db.WithContext(ctx).Model(&run).Where("status = ?", iCloudMaintenanceSucceeded).
		Update("next_check_at", nextCheck).Error
}

func (s *Service) retryICloudLivenessAlias(ctx context.Context, run iCloudMaintenanceRunModel) (bool, error) {
	if run.Attempts >= run.MaxAttempts {
		return false, nil
	}
	var alias iCloudAliasModel
	// shortcut: alias IDs are the retry cursor, restart the check if its current alias is removed.
	err := s.db.WithContext(ctx).Where(`resource_id = ? AND status = ? AND id >
		(SELECT id FROM icloud_aliases WHERE resource_id = ? AND email = ?)`,
		run.ResourceID, iCloudResourceNormal, run.ResourceID, run.ProbeRecipient).Order("id ASC").Take(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	address, err := stdmail.ParseAddress(alias.Email)
	if err != nil || address.Address != alias.Email || strings.ContainsAny(alias.Email, "\r\n") {
		return false, nil
	}
	now := s.now().UTC()
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	err = s.db.WithContext(retryCtx).Model(&iCloudMaintenanceRunModel{}).
		Where("id = ? AND status = ? AND attempts = ? AND probe_recipient = ? AND probe_sent_at IS NULL",
			run.ID, iCloudMaintenanceRunning, run.Attempts, run.ProbeRecipient).
		Updates(map[string]any{
			"status": iCloudMaintenanceQueued, "probe_recipient": alias.Email, "probe_sender": "",
			"started_at": nil, "finished_at": nil, "next_check_at": now.Add(iCloudValidationRetryInterval),
			"last_safe_error": "Temporary SMTP rejection; retrying another iCloud alias.", "updated_at": now,
		}).Error
	return true, err
}

func currentICloudLivenessRun(resource iCloudResourceModel, run iCloudMaintenanceRunModel) bool {
	return resource.ValidationGeneration == run.ValidationGeneration && resource.CredentialRevision == run.CredentialRevision &&
		resource.Status != iCloudResourceDisabled && resource.Status != iCloudResourceDeleted
}

func (s *Service) finishICloudLivenessCheck(ctx context.Context, snapshot iCloudMaintenanceRunModel, health, status, safeError string) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	return s.db.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&root, snapshot.ResourceID).Error; err != nil {
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, snapshot.ResourceID).Error; err != nil {
			return err
		}
		var run iCloudMaintenanceRunModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&run, snapshot.ID).Error; err != nil {
			return err
		}
		if run.Status != iCloudMaintenanceQueued && run.Status != iCloudMaintenanceRunning && run.Status != iCloudMaintenanceSucceeded {
			return nil
		}
		if !currentICloudLivenessRun(resource, run) {
			health, status, safeError = "", iCloudMaintenanceCanceled, "Liveness check canceled because the resource changed."
		}
		now := s.now().UTC()
		var nextCheck *time.Time
		if status == iCloudMaintenanceSucceeded {
			// shortcut: observe late bounces for 24 hours, extend this window if the provider delays delivery reports longer.
			next := now.Add(iCloudLivenessCheckInterval)
			nextCheck = &next
		}
		if err := tx.Model(&run).Updates(map[string]any{"status": status, "last_safe_error": safeError, "finished_at": now, "next_check_at": nextCheck, "updated_at": now}).Error; err != nil {
			return err
		}
		if status == iCloudMaintenanceCanceled {
			return nil
		}
		updates := map[string]any{"last_safe_error": safeError, "updated_at": now}
		if health != "" {
			updates["status"], updates["last_checked_at"] = health, now
			if health == iCloudResourceNormal {
				updates["last_valid_at"] = now
				updates["validation_failures"] = 0
			} else {
				updates["next_provision_at"] = nil
			}
		}
		if err := tx.Model(&resource).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&root).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
	})
}

func (s *Service) iCloudLivenessBounced(ctx context.Context, run iCloudMaintenanceRunModel) (bool, error) {
	if s.files == nil {
		return false, ErrICloudMailUnavailable
	}
	var rows []struct {
		SourceObjectKey string `gorm:"column:source_object_key"`
	}
	if err := s.db.WithContext(ctx).Table("inbound_mails").Select("source_object_key").
		Where("recipient = ? AND created_at >= ? AND status = ?", run.ProbeSender, run.QueuedAt, maildomain.InboundStatusStored).
		Where("created_at <= ?", run.ProbeSentAt.Add(iCloudLivenessLateBounceWindow)).
		Order("id ASC").Limit(101).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) > 100 {
		return false, ErrICloudMailUnavailable
	}
	var deliveryErr error
	for _, row := range rows {
		file, err := s.files.ReadPrivate(ctx, row.SourceObjectKey)
		if err != nil || file == nil || len(file.ContentBytes) == 0 {
			return false, ErrICloudMailUnavailable
		}
		if failure := iCloudLivenessDeliveryFailure(file.ContentBytes, run.ProbeRecipient); failure != nil {
			verified, err := s.verifyICloudLivenessBounce(ctx, file.ContentBytes, run.ProbeSender)
			if err != nil {
				return false, err
			}
			if !verified {
				deliveryErr = &mailapp.OutboundSendFailure{SafeMessage: "Liveness delivery report could not be authenticated; account health is unchanged."}
				continue
			}
			if failure.RecipientRejected {
				return true, nil
			}
			deliveryErr = failure
		}
	}
	return false, deliveryErr
}

func (s *Service) verifyICloudLivenessBounce(ctx context.Context, raw []byte, probeSender string) (bool, error) {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil || len(message.Header["From"]) != 1 || len(message.Header["To"]) != 1 || len(message.Header["Content-Type"]) != 1 {
		return false, nil
	}
	to, err := stdmail.ParseAddress(message.Header.Get("To"))
	if err != nil || !strings.EqualFold(to.Address, probeSender) {
		return false, nil
	}
	from, err := stdmail.ParseAddress(message.Header.Get("From"))
	if err != nil {
		return false, nil
	}
	local, domain, ok := strings.Cut(strings.ToLower(from.Address), "@")
	if !ok || (local != "mailer-daemon" && local != "postmaster") || !iCloudLivenessAppleDomain(domain) {
		return false, nil
	}
	lookup := s.livenessLookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	verifications, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		MaxVerifications: 3,
		LookupTXT:        func(name string) ([]string, error) { return lookup(verifyCtx, name) },
	})
	if errors.Is(err, dkim.ErrTooManySignatures) {
		return false, nil
	}
	if err != nil || verifyCtx.Err() != nil {
		return false, ErrICloudMailUnavailable
	}
	for _, verification := range verifications {
		// A provider signature on an ordinary user's mail does not prove it is a DSN.
		if verification.Err == nil && iCloudLivenessAppleDomain(verification.Domain) &&
			slices.ContainsFunc(verification.HeaderKeys, func(key string) bool { return strings.EqualFold(key, "From") }) &&
			slices.ContainsFunc(verification.HeaderKeys, func(key string) bool { return strings.EqualFold(key, "To") }) &&
			slices.ContainsFunc(verification.HeaderKeys, func(key string) bool { return strings.EqualFold(key, "Content-Type") }) {
			return true, nil
		}
	}
	if slices.ContainsFunc(verifications, func(verification *dkim.Verification) bool { return dkim.IsTempFail(verification.Err) }) {
		return false, ErrICloudMailUnavailable
	}
	return false, nil
}

func iCloudLivenessAppleDomain(domain string) bool {
	domain = strings.ToLower(domain)
	for _, provider := range []string{"icloud.com", "me.com", "mac.com"} {
		if domain == provider || strings.HasSuffix(domain, "."+provider) {
			return true
		}
	}
	return false
}

func iCloudLivenessDeliveryFailure(raw []byte, recipient string) *mailapp.OutboundSendFailure {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return iCloudLivenessFailurePart(message.Header.Get("Content-Type"), message.Body, recipient, 0)
}

func iCloudLivenessFailurePart(contentType string, body io.Reader, recipient string, depth int) *mailapp.OutboundSendFailure {
	if depth > 5 {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil
	}
	var failure *mailapp.OutboundSendFailure
	if strings.HasPrefix(mediaType, "multipart/") {
		parts := multipart.NewReader(body, params["boundary"])
		for {
			part, err := parts.NextPart()
			if err != nil {
				return failure
			}
			partFailure := iCloudLivenessFailurePart(part.Header.Get("Content-Type"), part, recipient, depth+1)
			part.Close()
			if partFailure != nil {
				if partFailure.RecipientRejected {
					return partFailure
				}
				failure = partFailure
			}
		}
	}
	if mediaType != "message/delivery-status" {
		return nil
	}
	reader := textproto.NewReader(bufio.NewReader(io.LimitReader(body, 64<<10)))
	for {
		header, err := reader.ReadMIMEHeader()
		_, address, ok := strings.Cut(header.Get("Final-Recipient"), ";")
		if ok && strings.EqualFold(strings.Trim(strings.TrimSpace(address), "<>"), recipient) &&
			strings.EqualFold(header.Get("Action"), "failed") && strings.HasPrefix(header.Get("Status"), "5.") {
			failure = &mailapp.OutboundSendFailure{
				SafeMessage:       "Liveness email could not be delivered; account health is unchanged.",
				RecipientRejected: maildomain.IsRecipientRejectionStatus(header.Get("Status")),
			}
			if failure.RecipientRejected {
				return failure
			}
		}
		if err != nil {
			return failure
		}
	}
}
