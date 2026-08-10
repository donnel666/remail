package icloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailtransportdomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudValidationMaxFailures    = 3
	iCloudKeepaliveInterval        = 10 * time.Minute
	iCloudValidationRetryInterval  = 30 * time.Second
	iCloudAliasProvisionInterval   = time.Second
	iCloudProvisionFailureInterval = 5 * time.Minute
	iCloudRateLimitRetryInterval   = 30 * time.Minute
	iCloudDeliveryProbeTimeout     = 10 * time.Minute
	iCloudRecipientProbeBatchLimit = 16
	iCloudRecipientProbeReadLimit  = 2000
	iCloudRecipientProbeRetryAfter = 2 * time.Minute
	iCloudValidationBatchLimit     = 128
	iCloudValidationRunningLease   = 5 * time.Minute
)

var (
	errICloudValidationStale = errors.New("icloud: validation result is stale")
	errICloudAliasConflict   = errors.New("icloud: alias belongs to another resource")
)

// iCloudValidationResult contains only provider-safe validation facts. The
// session Cookie is returned only as a replacement value for the locked
// resource row and is never sent to a queue, API response, or log.
type iCloudValidationResult struct {
	Valid              bool
	Retryable          bool
	Deferred           bool
	Category           string
	SafeMessage        string
	SessionKnown       bool
	SessionValid       bool
	SelectedForwardTo  string
	Aliases            []hmeAlias
	SnapshotComplete   bool
	AliasCount         int
	NextValidationAt   *time.Time
	ProvisionCandidate *string
	ProvisionReconcile *bool
	ProbeToken         string
	ProbeAlias         string
	ProbeStartedAt     *time.Time
	ProbeVerifiedAt    *time.Time
	ClearProbe         bool
	UpdatedCookie      string
}

func (s *Service) ProcessICloudValidation(ctx context.Context, task iCloudValidationTask) error {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 || task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrICloudValidationTemp
	}
	resource, found, err := s.iCloudValidationResource(ctx, task)
	if err != nil {
		return err
	}
	if !found {
		if task.MaintenanceRunID > 0 {
			_ = s.db.WithContext(context.WithoutCancel(ctx)).Transaction(func(tx *gorm.DB) error {
				return finishICloudMaintenanceRunTx(
					context.WithoutCancel(ctx), tx, task.MaintenanceRunID,
					iCloudMaintenanceCanceled, "iCloud resource changed before maintenance started.", s.now().UTC(),
				)
			})
		}
		return nil
	}
	now := s.now().UTC()
	if !resource.ExpireAt.After(now) {
		return s.applyICloudValidationResult(ctx, task, iCloudValidationResult{
			Category: "resource_expired", SafeMessage: "iCloud resource has expired.",
		})
	}

	client := s.hme
	if client == nil {
		client = NewHMEClient(nil)
	}
	list, err := client.list(ctx, resource.hmeConfig())
	if err != nil {
		if providerErr, ok := err.(*hmeError); ok {
			result := iCloudValidationResult{
				Retryable: providerErr.Retryable, Category: providerErr.Category, SafeMessage: providerErr.SafeMessage,
				SessionKnown: providerErr.SessionKnown, SessionValid: providerErr.SessionValid,
				UpdatedCookie: providerErr.UpdatedCookie,
			}
			if providerErr.Retryable {
				result.Deferred = true
				result.NextValidationAt = iCloudTimePointer(iCloudProviderRetryAt(now, providerErr))
			}
			return s.applyICloudValidationResult(ctx, task, result)
		}
		return s.applyICloudValidationResult(ctx, task, iCloudValidationResult{
			Retryable: true, Deferred: true, Category: "provider_unavailable", SafeMessage: "iCloud HME service is temporarily unavailable.",
			NextValidationAt: iCloudTimePointer(now.Add(iCloudProvisionFailureInterval)),
		})
	}
	if !list.Complete {
		return s.applyICloudValidationResult(ctx, task, iCloudValidationResult{
			Retryable: true, Category: "snapshot_incomplete", SafeMessage: "iCloud alias snapshot is incomplete.",
			SessionKnown: true, SessionValid: true, SelectedForwardTo: list.SelectedForwardTo, UpdatedCookie: list.UpdatedCookie,
		})
	}
	resultBase := iCloudValidationResult{
		SessionKnown: true, SessionValid: true, SelectedForwardTo: list.SelectedForwardTo,
		Aliases: list.Aliases, SnapshotComplete: true, AliasCount: len(list.Aliases), UpdatedCookie: list.UpdatedCookie,
	}
	mutationConfig := resource.hmeConfig()
	mutationConfig.Cookie = list.UpdatedCookie
	forwardingMailboxes := runtimeconfig.EmailList(
		runtimeconfig.ICloudForwardingMailboxesKey,
		runtimeconfig.DefaultICloudForwardingMailbox,
	)
	if !containsICloudEmail(forwardingMailboxes, list.SelectedForwardTo) {
		resultBase.Category = "forward_target_mismatch"
		resultBase.SafeMessage = "iCloud forwarding target is not configured as an Apple mailbox."
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if len(list.Aliases) > iCloudMaxAliases {
		resultBase.Category = "alias_limit_exceeded"
		resultBase.SafeMessage = "iCloud resource has more than 750 aliases and is not sellable."
		resultBase.ProvisionCandidate = iCloudStringPointer("")
		resultBase.ProvisionReconcile = iCloudBoolPointer(false)
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	for _, alias := range list.Aliases {
		if !strings.EqualFold(strings.TrimSpace(alias.ForwardToEmail), strings.TrimSpace(list.SelectedForwardTo)) {
			resultBase.Category = "alias_forward_target_mismatch"
			resultBase.SafeMessage = "An iCloud alias does not forward to the selected Apple mailbox."
			return s.applyICloudValidationResult(ctx, task, resultBase)
		}
	}
	for _, alias := range list.Aliases {
		if alias.Active {
			continue
		}
		updatedCookie, activateErr := client.Activate(ctx, mutationConfig, alias.AnonymousID)
		if updatedCookie != "" {
			resultBase.UpdatedCookie = updatedCookie
		}
		if activateErr != nil {
			resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudProvisionFailureInterval))
			return s.applyICloudProviderError(ctx, task, resultBase, activateErr, true)
		}
		resultBase.Deferred = true
		resultBase.Retryable = true
		resultBase.Category = "alias_activation"
		resultBase.SafeMessage = "iCloud alias activation is in progress."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudAliasProvisionInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}

	// Reconcile a generated/reserved candidate before creating another one.
	candidate := strings.TrimSpace(resource.AliasProvisionCandidate)
	if candidate != "" {
		if findICloudAlias(list.Aliases, candidate) != nil {
			resultBase.ProvisionCandidate = iCloudStringPointer("")
			resultBase.ProvisionReconcile = iCloudBoolPointer(false)
			if len(list.Aliases) < iCloudMaxAliases {
				resultBase.Deferred, resultBase.Retryable = true, true
				resultBase.Category = "alias_provisioning"
				resultBase.SafeMessage = "iCloud alias provisioning is in progress."
				resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudAliasProvisionInterval))
				return s.applyICloudValidationResult(ctx, task, resultBase)
			}
		} else if len(list.Aliases) < iCloudMaxAliases {
			markErr := s.markICloudAliasReserveAttempt(ctx, task)
			if errors.Is(markErr, errICloudValidationStale) {
				return nil
			}
			if markErr != nil {
				return ErrICloudValidationTemp
			}
			reservedAlias, updatedCookie, reserveErr := client.reserve(ctx, mutationConfig, candidate, "ReMail", "")
			if updatedCookie != "" {
				resultBase.UpdatedCookie = updatedCookie
			}
			resultBase.ProvisionCandidate = iCloudStringPointer(candidate)
			resultBase.ProvisionReconcile = iCloudBoolPointer(true)
			if reserveErr != nil {
				resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudProvisionFailureInterval))
				if providerErr, ok := reserveErr.(*hmeError); ok && providerErr.Category == "invalid_candidate" {
					resultBase.ProvisionCandidate = iCloudStringPointer("")
					resultBase.ProvisionReconcile = iCloudBoolPointer(false)
					return s.applyICloudProviderError(ctx, task, resultBase, reserveErr, false)
				}
				if providerErr, ok := reserveErr.(*hmeError); ok && providerErr.Category == "rate_limited" {
					resultBase.ProvisionReconcile = iCloudBoolPointer(false)
				}
				return s.applyICloudProviderError(ctx, task, resultBase, reserveErr, true)
			}
			// The reserve response is already an authoritative alias fact. Persist
			// its anonymous and recipient IDs immediately, while retaining the
			// reconciliation marker until the next list confirms visibility.
			if strings.TrimSpace(reservedAlias.ForwardToEmail) == "" {
				reservedAlias.ForwardToEmail = list.SelectedForwardTo
			}
			resultBase.Aliases = append(resultBase.Aliases, reservedAlias)
			resultBase.AliasCount = len(resultBase.Aliases)
			resultBase.Deferred = true
			resultBase.Retryable = true
			resultBase.Category = "alias_provisioning"
			resultBase.SafeMessage = "iCloud alias provisioning is in progress."
			resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudAliasProvisionInterval))
			return s.applyICloudValidationResult(ctx, task, resultBase)
		}
		// A complete 750-alias snapshot without the candidate is authoritative;
		// never reserve it and risk creating alias 751.
		resultBase.ProvisionCandidate = iCloudStringPointer("")
		resultBase.ProvisionReconcile = iCloudBoolPointer(false)
	}
	if len(list.Aliases) < iCloudMaxAliases {
		generated, updatedCookie, generateErr := client.Generate(ctx, mutationConfig)
		if updatedCookie != "" {
			resultBase.UpdatedCookie = updatedCookie
		}
		if generateErr != nil {
			resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudProvisionFailureInterval))
			return s.applyICloudProviderError(ctx, task, resultBase, generateErr, true)
		}
		resultBase.ProvisionCandidate = iCloudStringPointer(generated)
		resultBase.ProvisionReconcile = iCloudBoolPointer(false)
		resultBase.Deferred = true
		resultBase.Retryable = true
		resultBase.Category = "alias_provisioning"
		resultBase.SafeMessage = "iCloud alias provisioning is in progress."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudAliasProvisionInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	// Apple commonly omits recipientMailId from the HME list response.  Merge
	// facts learned by an earlier validation before deciding whether the 750
	// aliases are routable; otherwise a valid account can never leave pending.
	if err := s.mergeICloudAliasFacts(ctx, resource.ID, list.Aliases); err != nil {
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "alias_route_state_unavailable"
		resultBase.SafeMessage = "iCloud alias routing state is temporarily unavailable."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if ready, err := s.discoverICloudRecipientIDs(ctx, task, list.SelectedForwardTo, list.Aliases, now); err != nil {
		if errors.Is(err, errICloudValidationStale) {
			return nil
		}
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "recipient_probe_unavailable"
		resultBase.SafeMessage = "Waiting for Apple relay routing identifiers."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	} else if !ready {
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "recipient_probe_pending"
		resultBase.SafeMessage = "Waiting for Apple relay routing identifiers."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if !iCloudAliasesReadyForForwarding(list.Aliases, list.SelectedForwardTo) {
		resultBase.Category = "alias_not_ready"
		resultBase.SafeMessage = "Not every iCloud alias is active, routable, and forwarding to the selected Apple mailbox."
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}

	if resource.DeliveryProbeVerifiedAt != nil && strings.TrimSpace(resource.DeliveryProbeAlias) != "" &&
		findICloudAlias(list.Aliases, resource.DeliveryProbeAlias) != nil {
		resultBase.Valid = true
		resultBase.ProbeToken = resource.DeliveryProbeToken
		resultBase.ProbeAlias = resource.DeliveryProbeAlias
		resultBase.ProbeVerifiedAt = resource.DeliveryProbeVerifiedAt
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if s.delivery == nil || s.files == nil {
		resultBase.Retryable = true
		resultBase.Category = "delivery_probe_unavailable"
		resultBase.SafeMessage = "Apple mailbox delivery probe is unavailable."
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	probeAlias := strings.TrimSpace(resource.DeliveryProbeAlias)
	probeToken := strings.TrimSpace(resource.DeliveryProbeToken)
	probeStartedAt := resource.DeliveryProbeStartedAt
	probeAliasItem := findICloudAlias(list.Aliases, probeAlias)
	if probeAlias == "" || probeAliasItem == nil || strings.TrimSpace(probeAliasItem.RecipientMailID) == "" {
		probeAlias = list.Aliases[0].Email
		probeAliasItem = &list.Aliases[0]
		probeToken = ""
		probeStartedAt = nil
		resultBase.ClearProbe = true
	}
	if probeToken == "" || probeStartedAt == nil {
		probeToken = iCloudDeliveryProbeToken(resource.ID, task.ValidationGeneration, probeAlias)
		probeStartedAt = iCloudTimePointer(now)
		// Persist the random token in the result before the outbound side effect.
		// If enqueueing fails, the next attempt reuses the same idempotency key.
		resultBase.ProbeToken, resultBase.ProbeAlias, resultBase.ProbeStartedAt = probeToken, probeAlias, probeStartedAt
		if err := s.delivery.Send(ctx, mailtransportdomain.OutboundMessage{
			IdempotencyKey: "icloud-delivery-probe:" + probeToken,
			Purpose:        mailtransportdomain.PurposeSystemNotice,
			To:             probeAlias,
			Subject:        "ReMail iCloud delivery probe",
			TextBody:       "ReMail iCloud delivery probe token: " + probeToken,
		}); err != nil {
			resultBase.Retryable = true
			resultBase.Category = "delivery_probe_send_failed"
			resultBase.SafeMessage = "Unable to send the Apple mailbox delivery probe."
			return s.applyICloudValidationResult(ctx, task, resultBase)
		}
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "delivery_probe_pending"
		resultBase.SafeMessage = "Waiting for the iCloud alias delivery probe."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	foundDelivery, probeErr := s.findICloudDeliveryProbe(
		ctx,
		list.SelectedForwardTo,
		probeAliasItem.RecipientMailID,
		probeToken,
		probeStartedAt.Add(-time.Minute),
	)
	if probeErr != nil {
		resultBase.ProbeToken, resultBase.ProbeAlias, resultBase.ProbeStartedAt = probeToken, probeAlias, probeStartedAt
		if !now.Before(probeStartedAt.Add(iCloudDeliveryProbeTimeout)) {
			resultBase.Category = "delivery_probe_failed"
			resultBase.SafeMessage = "Apple mailbox delivery probe could not be completed."
			return s.applyICloudValidationResult(ctx, task, resultBase)
		}
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "delivery_probe_unavailable"
		resultBase.SafeMessage = "Apple mailbox delivery probe is temporarily unavailable."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if foundDelivery {
		resultBase.Valid = true
		resultBase.ProbeToken, resultBase.ProbeAlias, resultBase.ProbeStartedAt = probeToken, probeAlias, probeStartedAt
		resultBase.ProbeVerifiedAt = iCloudTimePointer(now)
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	if now.Before(probeStartedAt.Add(iCloudDeliveryProbeTimeout)) {
		resultBase.ProbeToken, resultBase.ProbeAlias, resultBase.ProbeStartedAt = probeToken, probeAlias, probeStartedAt
		resultBase.Deferred, resultBase.Retryable = true, true
		resultBase.Category = "delivery_probe_pending"
		resultBase.SafeMessage = "Waiting for the iCloud alias delivery probe."
		resultBase.NextValidationAt = iCloudTimePointer(now.Add(iCloudValidationRetryInterval))
		return s.applyICloudValidationResult(ctx, task, resultBase)
	}
	resultBase.ProbeToken, resultBase.ProbeAlias, resultBase.ProbeStartedAt = probeToken, probeAlias, probeStartedAt
	resultBase.Category = "delivery_probe_timeout"
	resultBase.SafeMessage = "The configured Apple mailbox did not receive the delivery probe in time."
	return s.applyICloudValidationResult(ctx, task, resultBase)
}

func (s *Service) RequestAdminICloudValidation(ctx context.Context, operatorUserID, resourceID uint, requestID, path string) error {
	if s == nil || s.db == nil || s.operationLogs == nil || operatorUserID == 0 || resourceID == 0 {
		return ErrICloudValidationTemp
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ?", resourceID, "icloud").First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, resourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		switch resource.Status {
		case iCloudResourceDeleted:
			return ErrICloudResourceNotFound
		case iCloudResourceDisabled:
			return ErrICloudResourceStatus
		}
		nextGeneration := resource.ValidationGeneration + 1
		updated := tx.Model(&iCloudResourceModel{}).Where("id = ? AND validation_generation = ?", resource.ID, resource.ValidationGeneration).
			Updates(map[string]any{
				"status": iCloudResourcePending, "validation_generation": nextGeneration,
				"validation_failures": 0, "next_validation_at": now, "last_safe_error": "", "updated_at": now,
				"delivery_probe_token": "", "delivery_probe_alias": "",
				"delivery_probe_started_at": nil, "delivery_probe_verified_at": nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errICloudValidationStale
		}
		if _, err := ensureICloudMaintenanceRunTx(
			ctx, tx, resource.ID, nextGeneration, iCloudMaintenanceValidation,
			resource.CredentialRevision, 0, now,
		); err != nil {
			return err
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_resource.validate",
			ResourceType: "icloud_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
			Path: strings.TrimSpace(path), Result: "success",
			SafeSummary: "iCloud resource validation marked pending for asynchronous execution.",
			RequestID:   strings.TrimSpace(requestID),
		})
	})
	if errors.Is(err, ErrICloudResourceNotFound) || errors.Is(err, ErrICloudResourceStatus) {
		return err
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) applyICloudProviderError(ctx context.Context, task iCloudValidationTask, result iCloudValidationResult, err error, deferred bool) error {
	if providerErr, ok := err.(*hmeError); ok {
		result.Retryable = providerErr.Retryable
		result.SessionKnown = providerErr.SessionKnown
		result.SessionValid = providerErr.SessionValid
		result.Category = providerErr.Category
		result.SafeMessage = providerErr.SafeMessage
		if providerErr.UpdatedCookie != "" {
			result.UpdatedCookie = providerErr.UpdatedCookie
		}
	} else {
		result.Retryable = true
		result.Category = "provider_unavailable"
		result.SafeMessage = "iCloud HME service is temporarily unavailable."
	}
	if result.Category == "rate_limited" {
		result.Deferred = true
		if providerErr, ok := err.(*hmeError); ok {
			result.NextValidationAt = iCloudTimePointer(iCloudProviderRetryAt(s.now().UTC(), providerErr))
		}
	}
	if deferred && result.Retryable {
		result.Deferred = true
		if result.NextValidationAt == nil {
			result.NextValidationAt = iCloudTimePointer(s.now().UTC().Add(iCloudValidationRetryInterval))
		}
	}
	return s.applyICloudValidationResult(ctx, task, result)
}

func iCloudStringPointer(value string) *string { return &value }

func iCloudBoolPointer(value bool) *bool { return &value }

func iCloudTimePointer(value time.Time) *time.Time { return &value }

func iCloudProviderRetryAt(now time.Time, providerErr *hmeError) time.Time {
	if providerErr != nil && providerErr.Category == "rate_limited" {
		if providerErr.RetryAfter > 0 {
			return now.Add(providerErr.RetryAfter)
		}
		return now.Add(iCloudRateLimitRetryInterval)
	}
	return now.Add(iCloudProvisionFailureInterval)
}

// markICloudAliasReserveAttempt persists the uncertain candidate before the
// provider side effect so a crash retries by listing instead of creating alias 751.
func (s *Service) markICloudAliasReserveAttempt(ctx context.Context, task iCloudValidationTask) error {
	if s == nil || s.db == nil {
		return ErrICloudValidationTemp
	}
	updated := s.db.WithContext(ctx).Model(&iCloudResourceModel{}).
		Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
			task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
		Updates(map[string]any{
			"alias_provision_reconcile": true,
			"updated_at":                s.now().UTC(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errICloudValidationStale
	}
	return nil
}

func iCloudDeliveryProbeToken(resourceID uint, generation uint64, alias string) string {
	// The token is persisted before the outbound side effect is considered
	// complete. Randomness prevents another message from guessing a probe by
	// knowing a resource ID, validation generation, and alias address.
	return newICloudProbeToken("remail-icloud-delivery")
}

func newICloudProbeToken(prefix string) string {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		// crypto/rand failures are exceptionally rare. Keep the state machine
		// usable while still avoiding the old predictable resource-derived token.
		digest := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		return prefix + "-" + hex.EncodeToString(digest[:12])
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}

// mergeICloudAliasFacts overlays durable routing/probe facts onto Apple's
// latest snapshot. Apple may omit recipientMailId after the first observation.
func (s *Service) mergeICloudAliasFacts(ctx context.Context, resourceID uint, aliases []hmeAlias) error {
	if s == nil || s.db == nil || resourceID == 0 {
		return ErrICloudValidationTemp
	}
	var rows []iCloudAliasModel
	if err := s.db.WithContext(ctx).Where("resource_id = ?", resourceID).Find(&rows).Error; err != nil {
		return err
	}
	byID := make(map[string]iCloudAliasModel, len(rows))
	for _, row := range rows {
		byID[strings.TrimSpace(row.AnonymousID)] = row
	}
	for i := range aliases {
		row, ok := byID[strings.TrimSpace(aliases[i].AnonymousID)]
		if !ok {
			continue
		}
		forwardTo := strings.TrimSpace(aliases[i].ForwardToEmail)
		if forwardTo == "" {
			aliases[i].ForwardToEmail = strings.TrimSpace(row.ForwardToEmail)
			forwardTo = strings.TrimSpace(aliases[i].ForwardToEmail)
		}
		sameRoute := forwardTo != "" && strings.EqualFold(forwardTo, strings.TrimSpace(row.ForwardToEmail))
		if strings.TrimSpace(aliases[i].RecipientMailID) == "" && sameRoute {
			aliases[i].RecipientMailID = strings.TrimSpace(row.RecipientMailID)
		}
		if sameRoute {
			aliases[i].RecipientProbeToken = strings.TrimSpace(row.RecipientProbeToken)
			aliases[i].RecipientProbeStartedAt = row.RecipientProbeStartedAt
			aliases[i].RecipientProbeLastSentAt = row.RecipientProbeLastSentAt
		} else {
			// A forwarding mailbox change makes the old probe token and relay
			// suffix unusable. The result sync clears the current route first;
			// the next validation generation discovers the new suffix.
			aliases[i].RecipientProbeToken = ""
			aliases[i].RecipientProbeStartedAt = nil
			aliases[i].RecipientProbeLastSentAt = nil
		}
	}
	return nil
}

// discoverICloudRecipientIDs learns the opaque Apple relay suffix by sending a
// token through each alias and inspecting the configured domain mailbox.
func (s *Service) discoverICloudRecipientIDs(
	ctx context.Context,
	task iCloudValidationTask,
	forwardTo string,
	aliases []hmeAlias,
	now time.Time,
) (bool, error) {
	if len(aliases) != iCloudMaxAliases {
		return false, nil
	}
	allKnown := true
	for _, alias := range aliases {
		if strings.TrimSpace(alias.RecipientMailID) == "" ||
			!strings.EqualFold(strings.TrimSpace(alias.ForwardToEmail), strings.TrimSpace(forwardTo)) {
			allKnown = false
			break
		}
	}
	if allKnown {
		return true, nil
	}
	rows := make(map[string]iCloudAliasModel, len(aliases))
	var stored []iCloudAliasModel
	if err := s.db.WithContext(ctx).Where("resource_id = ?", task.ResourceID).Find(&stored).Error; err != nil {
		return false, err
	}
	for _, row := range stored {
		rows[strings.TrimSpace(row.AnonymousID)] = row
	}
	missingRows := false
	targetChanged := false
	tokens := make(map[string]time.Time)
	toSend := make([]int, 0, iCloudRecipientProbeBatchLimit)
	for i := range aliases {
		if strings.TrimSpace(aliases[i].RecipientMailID) != "" {
			continue
		}
		row, ok := rows[strings.TrimSpace(aliases[i].AnonymousID)]
		if !ok {
			missingRows = true
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(row.ForwardToEmail), strings.TrimSpace(aliases[i].ForwardToEmail)) {
			// Do not probe against a row that still belongs to the previous
			// forwarding mailbox. The normal result transaction must first
			// archive that pair and clear the current recipient ID.
			targetChanged = true
			continue
		}
		if strings.TrimSpace(row.RecipientMailID) != "" {
			aliases[i].RecipientMailID = strings.TrimSpace(row.RecipientMailID)
			continue
		}
		token := strings.TrimSpace(row.RecipientProbeToken)
		startedAt := row.RecipientProbeStartedAt
		if token == "" || startedAt == nil {
			if len(tokens) >= iCloudRecipientProbeBatchLimit {
				continue
			}
			token = newICloudProbeToken("remail-icloud-recipient")
			started := now
			if err := s.persistICloudRecipientProbe(ctx, task, row.ID, token, started); err != nil {
				return false, err
			}
			startedAt = &started
			aliases[i].RecipientProbeToken = token
			aliases[i].RecipientProbeStartedAt = startedAt
			aliases[i].RecipientProbeLastSentAt = nil
			toSend = append(toSend, i)
		}
		aliases[i].RecipientProbeToken = token
		aliases[i].RecipientProbeStartedAt = startedAt
		if (row.RecipientProbeLastSentAt == nil || !now.Before(row.RecipientProbeLastSentAt.Add(iCloudRecipientProbeRetryAfter))) &&
			!slices.Contains(toSend, i) {
			if len(toSend) < iCloudRecipientProbeBatchLimit {
				toSend = append(toSend, i)
			}
		}
		if startedAt != nil {
			tokens[token] = startedAt.UTC()
		}
	}
	if missingRows || targetChanged {
		// The first complete provider snapshot is persisted by the normal result
		// transaction. Probe work starts on the following validation generation.
		return false, nil
	}
	if len(toSend) > 0 {
		if s.delivery == nil {
			return false, ErrICloudMailUnavailable
		}
		for _, index := range toSend {
			alias := aliases[index]
			token := strings.TrimSpace(alias.RecipientProbeToken)
			if err := s.delivery.Send(ctx, mailtransportdomain.OutboundMessage{
				IdempotencyKey: "icloud-recipient-probe:" + token,
				Purpose:        mailtransportdomain.PurposeSystemNotice,
				To:             alias.Email,
				Subject:        "ReMail iCloud recipient probe",
				TextBody:       "ReMail iCloud recipient probe token: " + token,
			}); err != nil {
				return false, err
			}
			if err := s.markICloudRecipientProbeSent(ctx, task, rows[strings.TrimSpace(alias.AnonymousID)].ID, now); err != nil {
				return false, err
			}
			sentAt := now
			aliases[index].RecipientProbeLastSentAt = &sentAt
		}
	}
	found := map[string]string{}
	if len(tokens) > 0 {
		var err error
		found, err = s.findICloudRecipientProbes(ctx, forwardTo, tokens)
		if err != nil {
			return false, err
		}
	}
	for i := range aliases {
		if strings.TrimSpace(aliases[i].RecipientMailID) != "" {
			continue
		}
		token := strings.TrimSpace(aliases[i].RecipientProbeToken)
		recipientID := strings.TrimSpace(found[token])
		if recipientID == "" {
			continue
		}
		row, ok := rows[strings.TrimSpace(aliases[i].AnonymousID)]
		if !ok {
			continue
		}
		if err := s.persistICloudRecipientID(ctx, task, row.ID, recipientID); err != nil {
			return false, err
		}
		aliases[i].RecipientMailID = recipientID
		aliases[i].RecipientProbeToken = ""
		aliases[i].RecipientProbeStartedAt = nil
		aliases[i].RecipientProbeLastSentAt = nil
	}
	for _, alias := range aliases {
		if strings.TrimSpace(alias.RecipientMailID) == "" {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) withICloudValidationFence(ctx context.Context, task iCloudValidationTask, fn func(*gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			First(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errICloudValidationStale
			}
			return err
		}
		return fn(tx)
	})
}

func (s *Service) persistICloudRecipientProbe(ctx context.Context, task iCloudValidationTask, aliasID uint, token string, startedAt time.Time) error {
	return s.withICloudValidationFence(ctx, task, func(tx *gorm.DB) error {
		result := tx.Model(&iCloudAliasModel{}).Where("id = ? AND resource_id = ? AND recipient_mail_id = ''", aliasID, task.ResourceID).
			Updates(map[string]any{"recipient_probe_token": token, "recipient_probe_started_at": startedAt, "recipient_probe_last_sent_at": nil, "updated_at": startedAt})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
}

func (s *Service) markICloudRecipientProbeSent(ctx context.Context, task iCloudValidationTask, aliasID uint, sentAt time.Time) error {
	return s.withICloudValidationFence(ctx, task, func(tx *gorm.DB) error {
		result := tx.Model(&iCloudAliasModel{}).Where("id = ? AND resource_id = ?", aliasID, task.ResourceID).
			Updates(map[string]any{"recipient_probe_last_sent_at": sentAt, "updated_at": sentAt})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
}

func (s *Service) persistICloudRecipientID(ctx context.Context, task iCloudValidationTask, aliasID uint, recipientID string) error {
	return s.withICloudValidationFence(ctx, task, func(tx *gorm.DB) error {
		result := tx.Model(&iCloudAliasModel{}).Where("id = ? AND resource_id = ? AND recipient_mail_id = ''", aliasID, task.ResourceID).
			Updates(map[string]any{"recipient_mail_id": recipientID, "recipient_probe_token": "", "recipient_probe_started_at": nil, "recipient_probe_last_sent_at": nil, "updated_at": s.now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
}

func findICloudAlias(aliases []hmeAlias, email string) *hmeAlias {
	email = strings.ToLower(strings.TrimSpace(email))
	for index := range aliases {
		if strings.EqualFold(strings.TrimSpace(aliases[index].Email), email) {
			return &aliases[index]
		}
	}
	return nil
}

func iCloudAliasesReadyForForwarding(aliases []hmeAlias, forwardTo string) bool {
	forwardTo = strings.ToLower(strings.TrimSpace(forwardTo))
	if len(aliases) != iCloudMaxAliases || forwardTo == "" {
		return false
	}
	seen := make(map[string]struct{}, len(aliases))
	seenRecipientIDs := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		email := strings.ToLower(strings.TrimSpace(alias.Email))
		recipientMailID := strings.ToLower(strings.TrimSpace(alias.RecipientMailID))
		if !alias.Active || email == "" || recipientMailID == "" ||
			strings.ToLower(strings.TrimSpace(alias.ForwardToEmail)) != forwardTo {
			return false
		}
		if _, exists := seen[email]; exists {
			return false
		}
		if _, exists := seenRecipientIDs[recipientMailID]; exists {
			return false
		}
		seen[email] = struct{}{}
		seenRecipientIDs[recipientMailID] = struct{}{}
	}
	return len(seen) == iCloudMaxAliases
}

func containsICloudEmail(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

// ApplyForwardingMailboxSettings fences resources whose Apple-selected target
// is no longer approved for new provisioning. Existing alias delivery facts
// and allocations remain intact; only new allocation eligibility is removed.
func (s *Service) ApplyForwardingMailboxSettings(ctx context.Context, mailboxes []string) error {
	if s == nil || s.db == nil || len(mailboxes) == 0 {
		return ErrICloudValidationTemp
	}
	allowed := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		mailbox = strings.ToLower(strings.TrimSpace(mailbox))
		if mailbox != "" {
			allowed = append(allowed, mailbox)
		}
	}
	if len(allowed) == 0 {
		return ErrICloudValidationTemp
	}
	if s.validateForwardingMailbox != nil {
		for _, mailbox := range allowed {
			if err := s.validateForwardingMailbox(ctx, mailbox); err != nil {
				return ErrICloudForwardingMailbox
			}
		}
	}
	now := s.now().UTC()
	result := s.db.WithContext(ctx).Model(&iCloudResourceModel{}).
		Where("status NOT IN ?", []string{iCloudResourceDisabled, iCloudResourceDeleted}).
		Where("LOWER(TRIM(selected_forward_to)) NOT IN ?", allowed).
		Updates(map[string]any{
			"for_sale":                   false,
			"status":                     iCloudResourcePending,
			"validation_generation":      gorm.Expr("validation_generation + 1"),
			"validation_failures":        0,
			"next_validation_at":         now,
			"delivery_probe_token":       "",
			"delivery_probe_alias":       "",
			"delivery_probe_started_at":  nil,
			"delivery_probe_verified_at": nil,
			"last_safe_error":            "Configured Apple mailbox changed.",
			"updated_at":                 now,
		})
	if result.Error != nil {
		return ErrICloudValidationTemp
	}
	if result.RowsAffected > 0 {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) iCloudValidationResource(ctx context.Context, task iCloudValidationTask) (*iCloudResourceModel, bool, error) {
	var root iCloudRootModel
	if err := s.db.WithContext(ctx).Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).First(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, ErrICloudValidationTemp
	}
	var resource iCloudResourceModel
	if err := s.db.WithContext(ctx).First(&resource, task.ResourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, ErrICloudValidationTemp
	}
	if resource.Status != iCloudResourceValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
		return nil, false, nil
	}
	return &resource, true, nil
}

// DispatchICloudValidations claims both first-time pending validations and
// due keepalives. It uses the same pending -> validating fence as Microsoft,
// but stores that fence in the iCloud resource itself so Core stays unchanged.
func (s *Service) DispatchICloudValidations(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	if limit <= 0 || limit > iCloudValidationBatchLimit {
		limit = iCloudValidationBatchLimit
	}
	if err := s.recoverStaleICloudValidations(ctx, s.now().UTC()); err != nil {
		return err
	}
	tasks, err := s.iCloudValidationCandidates(ctx, limit)
	if err != nil {
		return err
	}
	var result error
	for _, task := range tasks {
		claimedTask, claimed, claimErr := s.markICloudValidationDispatched(ctx, task)
		if claimErr != nil {
			result = errors.Join(result, claimErr)
			continue
		}
		if !claimed {
			continue
		}
		// A duplicate queue task already carries this durable validation fence.
		_, enqueueErr := s.enqueueICloudValidation(ctx, claimedTask)
		if enqueueErr != nil {
			_ = s.releaseICloudValidation(ctx, claimedTask, "iCloud validation is temporarily unavailable; dispatcher will retry.")
			result = errors.Join(result, enqueueErr)
			continue
		}
	}
	return result
}

// A validating row is the durable validation lease. If its worker disappears,
// the next dispatcher advances the generation and returns it to pending; an
// old task then becomes harmless through the generation/credential fence.
func (s *Service) recoverStaleICloudValidations(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrICloudValidationTemp
	}
	var tasks []iCloudValidationTask
	if err := s.db.WithContext(ctx).Table("icloud_resources AS ir").
		Select("ir.id AS resource_id, ir.validation_generation, ir.credential_revision AS expected_credential_revision, COALESCE(run.id, 0) AS maintenance_run_id, COALESCE(run.kind, 'validation') AS maintenance_kind").
		Joins("LEFT JOIN icloud_maintenance_runs AS run ON run.resource_id = ir.id AND run.validation_generation = ir.validation_generation").
		Where("ir.status = ? AND ir.updated_at <= ?", iCloudResourceValidating, now.Add(-iCloudValidationRunningLease)).
		Order("ir.id ASC").Scan(&tasks).Error; err != nil {
		return ErrICloudValidationTemp
	}
	for _, task := range tasks {
		if err := s.releaseICloudValidation(ctx, task, "iCloud validation lease expired; dispatcher will retry."); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) iCloudValidationCandidates(ctx context.Context, limit int) ([]iCloudValidationTask, error) {
	now := s.now().UTC()
	var rows []struct {
		ID                   uint   `gorm:"column:id"`
		OwnerUserID          uint   `gorm:"column:owner_user_id"`
		Status               string `gorm:"column:status"`
		CredentialRevision   uint64 `gorm:"column:credential_revision"`
		ValidationGeneration uint64 `gorm:"column:validation_generation"`
	}
	err := s.db.WithContext(ctx).Table("icloud_resources AS ir").
		Select("ir.id, er.owner_user_id, ir.status, ir.credential_revision, ir.validation_generation").
		Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
		Where(`(ir.status = ? AND (ir.next_validation_at IS NULL OR ir.next_validation_at <= ?)) OR
			(ir.status = ? AND ir.expire_at <= ?) OR
			(ir.status = ? AND ir.session_status = ? AND ir.next_keepalive_at IS NOT NULL AND ir.next_keepalive_at <= ? AND ir.expire_at > ?)`,
			iCloudResourcePending, now, iCloudResourceNormal, now, iCloudResourceNormal, iCloudSessionValid, now, now).
		Order("ir.id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, ErrICloudValidationTemp
	}
	tasks := make([]iCloudValidationTask, 0, len(rows))
	for _, row := range rows {
		if row.ID == 0 || row.OwnerUserID == 0 || row.CredentialRevision == 0 || row.ValidationGeneration == 0 {
			continue
		}
		generation := row.ValidationGeneration
		if row.Status == iCloudResourceNormal {
			generation++
		}
		tasks = append(tasks, iCloudValidationTask{
			ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: generation,
			ExpectedCredentialRevision: row.CredentialRevision,
		})
	}
	return tasks, nil
}

func (s *Service) markICloudValidationDispatched(ctx context.Context, task iCloudValidationTask) (iCloudValidationTask, bool, error) {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 || task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return task, false, ErrICloudValidationTemp
	}
	now := s.now().UTC()
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).
			Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if resource.CredentialRevision != task.ExpectedCredentialRevision {
			return nil
		}
		pendingDue := resource.Status == iCloudResourcePending &&
			resource.ValidationGeneration == task.ValidationGeneration &&
			(resource.NextValidationAt == nil || !resource.NextValidationAt.After(now))
		normalDue := resource.Status == iCloudResourceNormal &&
			resource.ValidationGeneration+1 == task.ValidationGeneration &&
			(!resource.ExpireAt.After(now) ||
				(resource.SessionStatus == iCloudSessionValid && resource.NextKeepaliveAt != nil &&
					!resource.NextKeepaliveAt.After(now) && resource.ExpireAt.After(now)))
		if !pendingDue && !normalDue {
			return nil
		}
		run, err := ensureICloudMaintenanceRunTx(
			ctx, tx, resource.ID, task.ValidationGeneration, iCloudMaintenanceValidation,
			resource.CredentialRevision, int(resource.ValidationFailures), now,
		)
		if err != nil {
			return err
		}
		result := tx.Model(&iCloudMaintenanceRunModel{}).
			Where("id = ? AND status = ?", run.ID, iCloudMaintenanceQueued).
			Updates(map[string]any{
				"status":          iCloudMaintenanceRunning,
				"attempts":        min(run.Attempts+1, run.MaxAttempts),
				"started_at":      now,
				"finished_at":     nil,
				"last_safe_error": "",
				"updated_at":      now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		updates := map[string]any{"status": iCloudResourceValidating, "last_safe_error": "", "updated_at": now}
		if normalDue {
			updates["validation_generation"] = task.ValidationGeneration
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(updates).Error; err != nil {
			return err
		}
		task.MaintenanceRunID = run.ID
		task.MaintenanceKind = run.Kind
		claimed = true
		return nil
	})
	if err != nil {
		return task, false, ErrICloudValidationTemp
	}
	return task, claimed, nil
}

func (s *Service) applyICloudValidationResult(ctx context.Context, task iCloudValidationTask, result iCloudValidationResult) error {
	err := s.applyICloudValidationResultOnce(ctx, task, result)
	if errors.Is(err, errICloudAliasConflict) {
		fallback := result
		fallback.Valid = false
		fallback.Retryable = false
		fallback.Deferred = false
		fallback.SnapshotComplete = false
		fallback.Aliases = nil
		fallback.Category = "alias_conflict"
		fallback.SafeMessage = "iCloud alias is already linked to another resource."
		err = s.applyICloudValidationResultOnce(ctx, task, fallback)
	}
	if errors.Is(err, errICloudValidationStale) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func (s *Service) applyICloudValidationResultOnce(ctx context.Context, task iCloudValidationTask, result iCloudValidationResult) error {
	stale := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				stale = true
				return nil
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				stale = true
				return nil
			}
			return err
		}
		if resource.Status != iCloudResourceValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.ExpectedCredentialRevision {
			stale = true
			return nil
		}
		maintenanceRun, runErr := findICloudMaintenanceRunTx(ctx, tx, task)
		if runErr != nil {
			return runErr
		}
		if task.MaintenanceRunID > 0 && maintenanceRun == nil {
			stale = true
			return nil
		}

		now := s.now().UTC()
		if !resource.ExpireAt.After(now) {
			result = iCloudValidationResult{Category: "resource_expired", SafeMessage: "iCloud resource has expired."}
		}
		updates := map[string]any{}
		if result.SnapshotComplete {
			if err := syncICloudAliasesTx(tx, resource.ID, result.Aliases, result.SelectedForwardTo, true, now); err != nil {
				return err
			}
			updates["alias_count"] = result.AliasCount
		}
		nextStatus := iCloudResourceAbnormal
		nextFailures := minICloudValidationFailures(resource.ValidationFailures + 1)
		safeMessage := safeICloudValidationMessage(result.SafeMessage)
		if result.Valid {
			nextStatus = iCloudResourceNormal
			nextFailures = 0
			safeMessage = ""
		} else if result.Deferred {
			nextStatus = iCloudResourcePending
			nextFailures = 0
		} else if result.Retryable && nextFailures < iCloudValidationMaxFailures {
			nextStatus = iCloudResourcePending
		}
		updates["status"] = nextStatus
		updates["validation_failures"] = nextFailures
		updates["last_safe_error"] = safeMessage
		updates["last_checked_at"] = now
		updates["updated_at"] = now
		if nextStatus == iCloudResourcePending {
			updates["validation_generation"] = resource.ValidationGeneration + 1
			if result.NextValidationAt != nil {
				updates["next_validation_at"] = result.NextValidationAt
			} else {
				updates["next_validation_at"] = now.Add(iCloudValidationRetryInterval)
			}
		} else {
			updates["next_validation_at"] = nil
		}
		if result.SessionKnown {
			if result.SessionValid {
				updates["session_status"] = iCloudSessionValid
				updates["session_failures"] = 0
				updates["last_valid_at"] = now
				updates["next_keepalive_at"] = now.Add(iCloudKeepaliveInterval)
			} else {
				updates["session_status"] = iCloudSessionInvalid
				updates["session_failures"] = minICloudSessionFailures(resource.SessionFailures + 1)
				updates["next_keepalive_at"] = nil
			}
		}
		if result.SessionKnown && result.SessionValid {
			updates["selected_forward_to"] = strings.TrimSpace(result.SelectedForwardTo)
		}
		if result.SnapshotComplete {
			updates["last_alias_sync_at"] = now
		}
		if result.ProvisionCandidate != nil {
			updates["alias_provision_candidate"] = strings.TrimSpace(*result.ProvisionCandidate)
		}
		if result.ProvisionReconcile != nil {
			updates["alias_provision_reconcile"] = *result.ProvisionReconcile
		}
		if result.ClearProbe {
			updates["delivery_probe_token"] = ""
			updates["delivery_probe_alias"] = ""
			// Clear the old nullable timestamps in the same statement that starts
			// the replacement probe.
			updates["delivery_probe_started_at"] = gorm.Expr("NULL")
			updates["delivery_probe_verified_at"] = gorm.Expr("NULL")
		}
		if result.ProbeToken != "" {
			updates["delivery_probe_token"] = result.ProbeToken
		}
		if result.ProbeAlias != "" {
			updates["delivery_probe_alias"] = strings.ToLower(strings.TrimSpace(result.ProbeAlias))
		}
		if result.ProbeStartedAt != nil {
			updates["delivery_probe_started_at"] = result.ProbeStartedAt
		}
		if result.ProbeVerifiedAt != nil {
			updates["delivery_probe_verified_at"] = result.ProbeVerifiedAt
		}
		updatedCookie := strings.TrimSpace(result.UpdatedCookie)
		if updatedCookie != "" && updatedCookie != resource.Cookie {
			updates["cookie"] = updatedCookie
			updates["credential_revision"] = resource.CredentialRevision + 1
			updates["credential_updated_at"] = now
		}
		updated := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			stale = true
			return nil
		}
		rootUpdated := tx.Model(&iCloudRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
		if rootUpdated.Error != nil {
			return rootUpdated.Error
		}
		if rootUpdated.RowsAffected != 1 {
			return errICloudValidationStale
		}
		if maintenanceRun != nil {
			runStatus := iCloudMaintenanceSucceeded
			if nextStatus == iCloudResourceAbnormal || (!result.Valid && !result.Deferred) {
				runStatus = iCloudMaintenanceFailed
			}
			if err := finishICloudMaintenanceRunTx(ctx, tx, maintenanceRun.ID, runStatus, safeMessage, now); err != nil {
				return err
			}
			if nextStatus == iCloudResourcePending {
				nextCredentialRevision := resource.CredentialRevision
				if updatedCookie != "" && updatedCookie != resource.Cookie {
					nextCredentialRevision++
				}
				if _, err := ensureICloudMaintenanceRunTx(
					ctx, tx, resource.ID, resource.ValidationGeneration+1, maintenanceRun.Kind,
					nextCredentialRevision, int(nextFailures), now,
				); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stale {
		return errICloudValidationStale
	}
	return nil
}

func (s *Service) releaseICloudValidation(ctx context.Context, task iCloudValidationTask, safeError string) error {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrICloudValidationTemp
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		run, err := findICloudMaintenanceRunTx(ctx, tx, task)
		if err != nil {
			return err
		}
		if run == nil {
			run, err = ensureICloudMaintenanceRunTx(
				ctx, tx, resource.ID, resource.ValidationGeneration, task.MaintenanceKind,
				resource.CredentialRevision, 0, now,
			)
			if err != nil {
				return err
			}
		}
		if err := finishICloudMaintenanceRunTx(ctx, tx, run.ID, iCloudMaintenanceFailed, safeError, now); err != nil {
			return err
		}
		retry := run.Attempts < run.MaxAttempts
		updates := map[string]any{
			"status": iCloudResourceAbnormal, "validation_failures": min(run.Attempts, iCloudValidationMaxFailures),
			"next_validation_at": nil, "last_safe_error": safeICloudValidationMessage(safeError), "updated_at": now,
		}
		if retry {
			updates["status"] = iCloudResourcePending
			updates["validation_generation"] = resource.ValidationGeneration + 1
			updates["next_validation_at"] = now
		}
		result := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				resource.ID, iCloudResourceValidating, resource.ValidationGeneration, resource.CredentialRevision).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errICloudValidationStale
		}
		if retry {
			_, err = ensureICloudMaintenanceRunTx(
				ctx, tx, resource.ID, resource.ValidationGeneration+1, run.Kind,
				resource.CredentialRevision, run.Attempts, now,
			)
		}
		return err
	})
	if err != nil && !errors.Is(err, errICloudValidationStale) {
		return ErrICloudValidationTemp
	}
	return nil
}

func syncICloudAliasesTx(tx *gorm.DB, resourceID uint, aliases []hmeAlias, expectedForwardTo string, complete bool, now time.Time) error {
	if resourceID == 0 {
		return errICloudAliasConflict
	}
	aliasByAnonymousID := make(map[string]hmeAlias, len(aliases))
	aliasByEmail := make(map[string]hmeAlias, len(aliases))
	anonymousIDs := make([]string, 0, len(aliases))
	emails := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		anonymousID := strings.TrimSpace(alias.AnonymousID)
		email := strings.ToLower(strings.TrimSpace(alias.Email))
		if anonymousID == "" || email == "" {
			return errICloudAliasConflict
		}
		if _, exists := aliasByAnonymousID[anonymousID]; exists {
			return errICloudAliasConflict
		}
		if _, exists := aliasByEmail[email]; exists {
			return errICloudAliasConflict
		}
		alias.AnonymousID = anonymousID
		alias.Email = email
		aliasByAnonymousID[anonymousID] = alias
		aliasByEmail[email] = alias
		anonymousIDs = append(anonymousIDs, anonymousID)
		emails = append(emails, email)
	}
	if len(emails) > 0 {
		var foreign []iCloudAliasModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email IN ? AND resource_id <> ?", emails, resourceID).Find(&foreign).Error; err != nil {
			return err
		}
		if len(foreign) > 0 {
			return errICloudAliasConflict
		}
	}
	var current []iCloudAliasModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id = ?", resourceID).Find(&current).Error; err != nil {
		return err
	}
	currentByAnonymousID := make(map[string]iCloudAliasModel, len(current))
	currentByEmail := make(map[string]iCloudAliasModel, len(current))
	for _, item := range current {
		currentByAnonymousID[item.AnonymousID] = item
		currentByEmail[strings.ToLower(item.Email)] = item
	}
	for _, alias := range aliases {
		if existing, found := currentByEmail[alias.Email]; found && existing.AnonymousID != alias.AnonymousID {
			return errICloudAliasConflict
		}
		status := iCloudResourceNormal
		if !alias.Active || strings.TrimSpace(expectedForwardTo) == "" ||
			!strings.EqualFold(strings.TrimSpace(alias.ForwardToEmail), strings.TrimSpace(expectedForwardTo)) {
			status = iCloudResourceDisabled
		}
		if existing, found := currentByAnonymousID[alias.AnonymousID]; found {
			forwardChanged := !strings.EqualFold(strings.TrimSpace(alias.ForwardToEmail), strings.TrimSpace(existing.ForwardToEmail))
			if err := persistICloudAliasRouteTx(tx, existing.ID, resourceID, existing.ForwardToEmail, existing.RecipientMailID, now); err != nil {
				return err
			}
			updates := map[string]any{
				"email": alias.Email, "label": alias.Label, "note": alias.Note, "forward_to_email": alias.ForwardToEmail,
				"origin": alias.Origin, "provider_domain": alias.ProviderDomain,
				"status": status, "provider_created_at": alias.ProviderCreatedAt, "last_seen_at": now, "updated_at": now,
			}
			// Apple sometimes omits recipientMailId from later snapshots. Once
			// learned, it remains the durable routing key until the forwarding
			// mailbox changes.
			if strings.TrimSpace(alias.RecipientMailID) != "" {
				updates["recipient_mail_id"] = alias.RecipientMailID
			} else if forwardChanged {
				updates["recipient_mail_id"] = ""
			}
			if forwardChanged {
				updates["recipient_probe_token"] = ""
				updates["recipient_probe_started_at"] = nil
				updates["recipient_probe_last_sent_at"] = nil
			} else if strings.TrimSpace(alias.RecipientProbeToken) != "" {
				updates["recipient_probe_token"] = alias.RecipientProbeToken
				updates["recipient_probe_started_at"] = alias.RecipientProbeStartedAt
				updates["recipient_probe_last_sent_at"] = alias.RecipientProbeLastSentAt
			} else if strings.TrimSpace(alias.RecipientMailID) != "" {
				updates["recipient_probe_token"] = ""
				updates["recipient_probe_started_at"] = nil
				updates["recipient_probe_last_sent_at"] = nil
			}
			updated := tx.Model(&iCloudAliasModel{}).Where("id = ? AND resource_id = ?", existing.ID, resourceID).Updates(updates)
			if updated.Error != nil || updated.RowsAffected != 1 {
				if updated.Error != nil {
					return updated.Error
				}
				return errICloudAliasConflict
			}
			routeRecipient := strings.TrimSpace(alias.RecipientMailID)
			if routeRecipient == "" && !forwardChanged {
				routeRecipient = strings.TrimSpace(existing.RecipientMailID)
			}
			if err := persistICloudAliasRouteTx(tx, existing.ID, resourceID, alias.ForwardToEmail, routeRecipient, now); err != nil {
				return err
			}
			continue
		}
		item := iCloudAliasModel{
			ResourceID: resourceID, AnonymousID: alias.AnonymousID, Email: alias.Email, Label: alias.Label, Note: alias.Note,
			ForwardToEmail: alias.ForwardToEmail, Origin: alias.Origin, ProviderDomain: alias.ProviderDomain,
			RecipientMailID: alias.RecipientMailID, Status: status, ProviderCreatedAt: alias.ProviderCreatedAt,
			LastSeenAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&item).Error; err != nil {
			if isICloudDuplicateError(err) {
				return errICloudAliasConflict
			}
			return err
		}
		if err := persistICloudAliasRouteTx(tx, item.ID, resourceID, item.ForwardToEmail, item.RecipientMailID, now); err != nil {
			return err
		}
	}
	if !complete {
		return nil
	}
	missing := tx.Model(&iCloudAliasModel{}).Where("resource_id = ? AND status <> ?", resourceID, iCloudResourceDeleted)
	if len(anonymousIDs) > 0 {
		missing = missing.Where("anonymous_id NOT IN ?", anonymousIDs)
	}
	if err := missing.Updates(map[string]any{"status": "missing", "updated_at": now}).Error; err != nil {
		return err
	}
	return nil
}

func persistICloudAliasRouteTx(tx *gorm.DB, aliasID, resourceID uint, forwardToEmail, recipientMailID string, now time.Time) error {
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	recipientMailID = strings.ToLower(strings.TrimSpace(recipientMailID))
	if forwardToEmail == "" || recipientMailID == "" || tx == nil || !tx.Migrator().HasTable("icloud_alias_routes") {
		return nil
	}
	var route iCloudAliasRouteModel
	err := tx.Where("forward_to_email = ? AND recipient_mail_id = ?", forwardToEmail, recipientMailID).First(&route).Error
	if err == nil {
		if route.ResourceID != resourceID || route.AliasID != aliasID {
			return errICloudAliasConflict
		}
		return tx.Model(&iCloudAliasRouteModel{}).Where("id = ?", route.ID).Updates(map[string]any{"last_seen_at": now}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := tx.Create(&iCloudAliasRouteModel{
		ResourceID: resourceID, AliasID: aliasID, ForwardToEmail: forwardToEmail,
		RecipientMailID: recipientMailID, FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		if isICloudDuplicateError(err) {
			return errICloudAliasConflict
		}
		return err
	}
	return nil
}

func minICloudValidationFailures(value uint8) uint8 {
	if value > iCloudValidationMaxFailures {
		return iCloudValidationMaxFailures
	}
	return value
}

func minICloudSessionFailures(value uint8) uint8 {
	// SessionFailures+1 wraps from 255 to 0; keep the counter saturated.
	if value == 0 {
		return 255
	}
	return value
}

func safeICloudValidationMessage(value string) string {
	value = safeICloudImportMessage(value)
	if value == "" {
		return "iCloud validation failed."
	}
	return value
}
