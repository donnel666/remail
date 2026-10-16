package icloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AcquireStandalonePrivateEmailReplacement serializes CMDs even when they use different checkpoint files.
func (s *Service) AcquireStandalonePrivateEmailReplacement(ctx context.Context, account string) (func(), error) {
	if s == nil || s.deviceRedis == nil || !validStandaloneICloudEmail(account) {
		return nil, ErrICloudImportDependency
	}
	ttl := 15 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		ttl = time.Until(deadline) + time.Minute
	}
	key := fmt.Sprintf("icloud:private-email:replacement:%x", sha256.Sum256([]byte(strings.ToLower(account))))
	token := rand.Text()
	acquired, err := s.deviceRedis.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, errors.New("another private email replacement is running for this account")
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = releaseFamilyLoginScript.Run(cleanupCtx, s.deviceRedis, []string{key}, token).Err()
	}, nil
}

// PrepareStandalonePrivateEmailReplacement removes the selected alias from allocation before remote deletion.
func (s *Service) PrepareStandalonePrivateEmailReplacement(ctx context.Context, ownerID uint, account, target string) error {
	if !validICloudHMEEmail(target) {
		return ErrICloudImportInvalid
	}
	return s.withStandalonePrivateEmail(ctx, ownerID, account, func(tx *gorm.DB, resource iCloudResourceModel) error {
		var alias iCloudAliasModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id=? AND email=?", resource.ID, target).Take(&alias).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var allocated int64
		if err := tx.Table("icloud_allocations").Where("alias_id=? AND status=?", alias.ID, "allocated").Count(&allocated).Error; err != nil {
			return err
		}
		if allocated != 0 {
			return ErrICloudResourceAllocation
		}
		return tx.Model(&alias).Where("status NOT IN ?", []string{iCloudResourceDeleted, "missing"}).
			Updates(map[string]any{"status": iCloudResourceDisabled, "updated_at": s.now().UTC()}).Error
	})
}

func (s *Service) SyncStandalonePrivateEmails(ctx context.Context, ownerID uint, account, removed string, aliases []ApplePrivateEmail, channel *AppleOnboardingChannel) error {
	if !validICloudHMEEmail(removed) {
		return ErrICloudImportInvalid
	}
	return s.withStandalonePrivateEmail(ctx, ownerID, account, func(tx *gorm.DB, resource iCloudResourceModel) error {
		if channel == nil || standaloneChannel(*channel, iCloudChannelAppleAccount) == nil {
			return ErrICloudImportInvalid
		}
		snapshot := make([]hmeAlias, 0, len(aliases))
		removedPresent := false
		for _, alias := range aliases {
			if !validApplePrivateEmailID(alias.ID) || !validICloudHMEEmail(alias.EmailAddress) || !validICloudHMEEmail(alias.ForwardToEmail) || alias.Active == nil {
				return ErrICloudImportInvalid
			}
			removedPresent = removedPresent || strings.EqualFold(alias.EmailAddress, removed)
			snapshot = append(snapshot, hmeAlias{AnonymousID: alias.ID, Email: alias.EmailAddress, Label: alias.Label, Note: alias.Note,
				ForwardToEmail: alias.ForwardToEmail, Active: *alias.Active, Origin: "APPLE_ACCOUNT"})
		}
		now := s.now().UTC()
		if err := syncICloudAliasesTx(tx, resource.ID, snapshot, true, now); err != nil {
			return err
		}
		if !removedPresent {
			if err := tx.Model(&iCloudAliasModel{}).Where("resource_id=? AND email=?", resource.ID, removed).
				Updates(map[string]any{"status": iCloudResourceDeleted, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := upsertICloudImportChannelsTx(tx, resource.ID, []iCloudImportChannel{appleOnboardingImportChannel(*channel)}, false, now); err != nil {
			return err
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id=?", resource.ID).
			Updates(map[string]any{"alias_count": len(aliases), "last_alias_sync_at": now, "updated_at": now,
				"credential_revision": resource.CredentialRevision + 1, "credential_updated_at": now,
				"validation_generation": resource.ValidationGeneration + 1}).Error; err != nil {
			return err
		}
		if err := tx.Model(&iCloudRootModel{}).Where("id=?", resource.ID).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		return s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: ownerID, OperationType: "icloud.standalone.private_email_sync", ResourceType: "icloud_resource",
			ResourceID: strconv.FormatUint(uint64(resource.ID), 10), Path: "/cmd/icloudvalidate/replace-private-email", Result: "success",
			SafeSummary: "Synchronized the remote private email snapshot after explicit alias replacement.",
		})
	})
}

func (s *Service) withStandalonePrivateEmail(ctx context.Context, ownerID uint, account string, action func(*gorm.DB, iCloudResourceModel) error) error {
	if s == nil || s.db == nil || s.operationLogs == nil || ownerID == 0 || !validStandaloneICloudEmail(account) {
		return ErrICloudImportInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource iCloudResourceModel
		err := tx.Where("LOWER(primary_email)=?", strings.ToLower(account)).Take(&resource).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND type=?", resource.ID, "icloud").Take(&root).Error; err != nil {
			return err
		}
		if root.OwnerUserID != ownerID {
			return ErrICloudResourceOwner
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, resource.ID).Error; err != nil {
			return err
		}
		if !strings.EqualFold(resource.PrimaryEmail, account) {
			return ErrICloudResourceIdentity
		}
		if isICloudAccountLocked(resource) {
			return ErrICloudResourceStatus
		}
		if resource.Status != iCloudResourceDisabled || resource.OnboardingStatus == iCloudOnboardingProcessing || resource.OnboardingStatus == iCloudOnboardingWaiting {
			return errors.New("disable the imported resource and stop its active workflow before replacing a private email")
		}
		return action(tx, resource)
	})
}
