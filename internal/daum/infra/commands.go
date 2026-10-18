package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/daum/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errIneligibleOwner = errors.New("daum: owner is not eligible")

type commandReceipt struct {
	ID                 uint64 `gorm:"primaryKey"`
	OperatorUserID     uint   `gorm:"uniqueIndex:uq_daum_command_key"`
	ResourceID         uint
	Command            string
	IdempotencyKey     string `gorm:"uniqueIndex:uq_daum_command_key"`
	RequestFingerprint string
	ReservationToken   string
	Status             string
	ResultJSON         *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (commandReceipt) TableName() string { return "daum_command_receipts" }

type Command struct {
	ResourceID        uint    `json:"resourceId"`
	Version           uint64  `json:"version"`
	Action            string  `json:"action"`
	Email             *string `json:"email,omitempty"`
	OwnerID           *uint   `json:"ownerId,omitempty"`
	Password          *string `json:"password,omitempty"`
	AppPassword       *string `json:"appPassword,omitempty"`
	Protocol          *string `json:"protocol,omitempty"`
	KakaoEmail        *string `json:"kakaoEmail,omitempty"`
	BindingAddress    *string `json:"bindingAddress,omitempty"`
	BindingAddressSet bool    `json:"bindingAddressSet,omitempty"`
	PickupURL         *string `json:"pickupUrl,omitempty"`
	PickupURLSet      bool    `json:"pickupUrlSet,omitempty"`
	ForSale           *bool   `json:"forSale,omitempty"`
	QualityScore      *int    `json:"qualityScore,omitempty"`
	LongLived         *bool   `json:"longLived,omitempty"`
	OperatorUserID    uint    `json:"-"`
	ScopeOwnerID      *uint   `json:"-"`
	IdempotencyKey    string  `json:"-"`
	RequestID         string  `json:"-"`
	Path              string  `json:"-"`
	Bulk              bool    `json:"-"`
}
type CommandResult struct {
	ResourceID           uint   `json:"resourceId"`
	Version              uint64 `json:"version"`
	Status               string `json:"status"`
	ForSale              bool   `json:"forSale"`
	ValidationGeneration uint64 `json:"validationGeneration"`
	Queued               bool   `json:"queued"`
	Todo                 bool   `json:"todo"`
	Reused               bool   `json:"reused"`
	Changed              bool   `json:"changed"`
}

// Receipt, resource, and audit commit together. A failed command leaves no accepted receipt.
func (s *Service) ExecuteCommand(ctx context.Context, cmd Command) (*CommandResult, error) {
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	versionless := cmd.Action == "validate" || cmd.Action == "history"
	cmd.BindingAddressSet = cmd.BindingAddressSet || cmd.BindingAddress != nil
	cmd.PickupURLSet = cmd.PickupURLSet || cmd.PickupURL != nil
	if cmd.ResourceID == 0 || cmd.OperatorUserID == 0 || cmd.IdempotencyKey == "" || len(cmd.IdempotencyKey) > 128 || (!cmd.Bulk && !versionless && cmd.Version == 0) {
		return nil, domain.ErrInvalidResource
	}
	if cmd.QualityScore != nil && (*cmd.QualityScore < 0 || *cmd.QualityScore > 100) {
		return nil, domain.ErrInvalidResource
	}
	if cmd.ScopeOwnerID != nil && *cmd.ScopeOwnerID != cmd.OperatorUserID {
		return nil, domain.ErrInvalidResource
	}
	if (cmd.Password != nil && !validPassword(*cmd.Password)) || (cmd.AppPassword != nil && !validPassword(*cmd.AppPassword)) || (cmd.Protocol != nil && !validProtocol(*cmd.Protocol)) {
		return nil, domain.ErrInvalidResource
	}
	if cmd.KakaoEmail != nil {
		*cmd.KakaoEmail = strings.ToLower(strings.TrimSpace(*cmd.KakaoEmail))
		if *cmd.KakaoEmail != "" && !validEmail(*cmd.KakaoEmail) {
			return nil, domain.ErrInvalidResource
		}
	}
	if cmd.Action == "credentials" && (cmd.KakaoEmail != nil || cmd.BindingAddressSet || cmd.PickupURLSet || cmd.ForSale != nil || cmd.OwnerID != nil || cmd.Email != nil || cmd.QualityScore != nil || cmd.LongLived != nil) {
		return nil, domain.ErrInvalidResource
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	fingerprint := Fingerprint(cmd.OperatorUserID, fmt.Sprintf("%s:%v", cmd.Action, ownerValue(cmd.ScopeOwnerID)), payload)
	result := &CommandResult{}
	err = s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		token := platform.NewUUIDV7String()
		receipt := commandReceipt{OperatorUserID: cmd.OperatorUserID, ResourceID: cmd.ResourceID, Command: cmd.Action, IdempotencyKey: cmd.IdempotencyKey, RequestFingerprint: fingerprint, ReservationToken: token, Status: "processing"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		var stored commandReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operator_user_id = ? AND idempotency_key = ?", cmd.OperatorUserID, cmd.IdempotencyKey).Take(&stored).Error; err != nil {
			return err
		}
		if stored.ResourceID != cmd.ResourceID || stored.Command != cmd.Action || stored.RequestFingerprint != fingerprint {
			return domain.ErrImportConflict
		}
		if stored.ReservationToken != token {
			if cmd.ScopeOwnerID != nil {
				if _, err := lockResource(tx, cmd.ResourceID, cmd.ScopeOwnerID); err != nil {
					return err
				}
			}
			if stored.Status != "succeeded" || stored.ResultJSON == nil {
				return domain.ErrImportConflict
			}
			if err := json.Unmarshal([]byte(*stored.ResultJSON), result); err != nil {
				return err
			}
			result.Reused = true
			return nil
		}
		row, err := lockResource(tx, cmd.ResourceID, cmd.ScopeOwnerID)
		if err != nil {
			return err
		}
		if !cmd.Bulk && !versionless && row.Version != cmd.Version {
			return domain.ErrVersionConflict
		}
		if cmd.ScopeOwnerID != nil && cmd.Action != "validate" && cmd.Action != "publish" && cmd.Action != "delete" {
			return domain.ErrInvalidResource
		}
		effectiveSale := row.ForSale
		if cmd.ForSale != nil {
			effectiveSale = *cmd.ForSale
		}
		if cmd.Action == "publish" || (cmd.OwnerID != nil && *cmd.OwnerID != row.OwnerUserID) || (cmd.Action == "edit" && effectiveSale) {
			owner := row.OwnerUserID
			if cmd.OwnerID != nil {
				owner = *cmd.OwnerID
			}
			if s.ValidateOwner != nil {
				valid, err := s.ValidateOwner(ctx, owner)
				if err != nil {
					return err
				}
				if !valid {
					return errors.Join(domain.ErrInvalidResource, errIneligibleOwner)
				}
			}
			if (cmd.Action == "publish" || effectiveSale) && s.ValidateSupplierOwner != nil {
				valid, err := s.ValidateSupplierOwner(ctx, owner)
				if err != nil {
					return err
				}
				if !valid {
					return errors.Join(domain.ErrInvalidResource, errIneligibleOwner)
				}
			}
		}
		switch cmd.Action {
		case "publish", "unpublish":
			err = s.SetForSale(ctx, row.ID, cmd.ScopeOwnerID, cmd.Action == "publish")
		case "disable", "delete":
			status := domain.StatusDisabled
			if cmd.Action == "delete" {
				status = domain.StatusDeleted
			}
			err = s.SetStatus(ctx, row.ID, cmd.ScopeOwnerID, status)
		case "enable", "recover":
			if (cmd.Action == "enable" && row.Status != domain.StatusDisabled) || (cmd.Action == "recover" && row.Status != domain.StatusDeleted) {
				return domain.ErrInvalidResource
			}
			err = s.SetStatus(ctx, row.ID, nil, domain.StatusPending)
		case "validate":
			_, err = s.ClaimForValidationWithRequest(ctx, row.ID, cmd.ScopeOwnerID, cmd.RequestID)
		case "history":
			err = s.requestHistory(ctx, row.ID, cmd.RequestID)
		case "credentials":
			if cmd.Password == nil && cmd.AppPassword == nil && cmd.Protocol == nil {
				return domain.ErrInvalidResource
			}
			err = s.ReplaceCredentials(ctx, row.ID, nil, cmd.Password, cmd.AppPassword, cmd.Protocol)
		case "edit":
			if cmd.KakaoEmail == nil && cmd.Email == nil && cmd.OwnerID == nil && cmd.QualityScore == nil && cmd.LongLived == nil && cmd.Password == nil && cmd.AppPassword == nil && cmd.Protocol == nil && !cmd.BindingAddressSet && !cmd.PickupURLSet && cmd.ForSale == nil {
				return domain.ErrInvalidResource
			}
			if err == nil && (cmd.KakaoEmail != nil || cmd.QualityScore != nil || cmd.LongLived != nil) {
				err = s.mutateResource(ctx, row.ID, nil, func(_ context.Context, _ *gorm.DB, r *Resource) error {
					if r.Status == domain.StatusDeleted {
						return domain.ErrResourceMissing
					}
					if cmd.KakaoEmail != nil {
						r.KakaoEmail = *cmd.KakaoEmail
					}
					if cmd.QualityScore != nil {
						r.QualityScore = *cmd.QualityScore
					}
					if cmd.LongLived != nil {
						r.LongLived = *cmd.LongLived
					}
					return nil
				})
			}
			if err == nil && (cmd.Email != nil || cmd.OwnerID != nil) {
				err = s.UpdateResource(ctx, row.ID, cmd.Email, cmd.OwnerID)
			}
			if err == nil && (cmd.Password != nil || cmd.AppPassword != nil || cmd.Protocol != nil) {
				err = s.ReplaceCredentials(ctx, row.ID, nil, cmd.Password, cmd.AppPassword, cmd.Protocol)
			}
			if err == nil && (cmd.BindingAddressSet || cmd.PickupURLSet) {
				err = s.updateNullableBinding(ctx, row.ID, cmd.BindingAddress, cmd.BindingAddressSet, cmd.PickupURL, cmd.PickupURLSet)
			}
			if err == nil && cmd.ForSale != nil {
				err = s.SetForSale(ctx, row.ID, nil, *cmd.ForSale)
			}
		default:
			return domain.ErrInvalidResource
		}
		if err != nil {
			return err
		}
		updated, err := s.GetResource(ctx, row.ID, cmd.ScopeOwnerID)
		if err != nil {
			return err
		}
		*result = CommandResult{ResourceID: updated.ID, Version: updated.Version, Status: updated.Status, ForSale: updated.ForSale, ValidationGeneration: updated.ValidationGeneration, Queued: cmd.Action == "validate" || cmd.Action == "history", Changed: updated.Version != row.Version}
		if s.OperationLogs != nil {
			if err := s.OperationLogs.Create(ctx, &governancedomain.OperationLog{OperatorUserID: cmd.OperatorUserID, OperationType: "daum.resource." + cmd.Action, ResourceType: "daum_resource", ResourceID: fmt.Sprint(row.ID), Path: cmd.Path, Result: "success", SafeSummary: "Daum resource command completed.", RequestID: cmd.RequestID}); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Model(&commandReceipt{}).Where("id = ? AND reservation_token = ? AND status = 'processing'", stored.ID, token).Updates(map[string]any{"status": "succeeded", "result_json": string(encoded), "updated_at": s.Now().UTC()}).Error
	})
	return result, err
}
func ownerValue(owner *uint) uint {
	if owner != nil {
		return *owner
	}
	return 0
}
func (s *Service) requestHistory(ctx context.Context, id uint, requestID string) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		if row.Status != domain.StatusNormal && row.Status != domain.StatusIdentifying {
			if row.Status == domain.StatusDeleted {
				return domain.ErrResourceMissing
			}
			return domain.ErrInvalidResource
		}
		now := s.Now().UTC()
		if err := cancelMaintenanceRunsTx(tx, id, "Superseded by explicit history retry.", now); err != nil {
			return err
		}
		generation := row.ValidationGeneration + 1
		if err := tx.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{"status": domain.StatusIdentifying, "validation_generation": generation, "validation_request_id": requestID, "last_safe_error": "", "version": row.Version + 1, "updated_at": now}).Error; err != nil {
			return err
		}
		if _, err := ensureMaintenanceRunTx(ctx, tx, id, generation, row.CredentialRevision, maintenanceKindHistory, requestID, now); err != nil {
			return err
		}
		return bumpRoot(tx, id, now)
	})
}
