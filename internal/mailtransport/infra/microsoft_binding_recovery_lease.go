package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type MicrosoftBindingRecoveryLeaseModel struct {
	NormalizedMask string     `gorm:"primaryKey;type:varchar(320);column:normalized_mask"`
	ClaimToken     string     `gorm:"type:char(32);not null;column:claim_token"`
	LeaseUntil     time.Time  `gorm:"not null;column:lease_until"`
	ResourceID     uint       `gorm:"not null;column:resource_id"`
	SentAt         *time.Time `gorm:"column:sent_at"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftBindingRecoveryLeaseModel) TableName() string {
	return "microsoft_binding_recovery_leases"
}

type MicrosoftBindingRecoveryLeaseStore struct {
	db *gorm.DB
}

func NewMicrosoftBindingRecoveryLeaseStore(db *gorm.DB) *MicrosoftBindingRecoveryLeaseStore {
	return &MicrosoftBindingRecoveryLeaseStore{db: db}
}

func (s *MicrosoftBindingRecoveryLeaseStore) Claim(ctx context.Context, normalizedMask string, resourceID uint, leaseUntil time.Time) (string, bool, error) {
	normalizedMask = normalizeBindingEmail(normalizedMask)
	if s == nil || s.db == nil || !isMaskedMicrosoftBindingAddress(normalizedMask) || resourceID == 0 || !leaseUntil.After(time.Now().UTC()) {
		return "", false, fmt.Errorf("claim microsoft binding recovery lease: invalid input")
	}
	claimToken, err := newMicrosoftAliasClaimToken()
	if err != nil {
		return "", false, err
	}
	claimed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Where("lease_until <= ?", now).
			Delete(&MicrosoftBindingRecoveryLeaseModel{}).Error; err != nil {
			return fmt.Errorf("clear expired microsoft binding recovery lease: %w", err)
		}
		model := &MicrosoftBindingRecoveryLeaseModel{
			NormalizedMask: normalizedMask,
			ClaimToken:     claimToken,
			LeaseUntil:     leaseUntil.UTC(),
			ResourceID:     resourceID,
		}
		if err := tx.Create(model).Error; err != nil {
			if isDuplicateKeyError(err) || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return nil
			}
			return fmt.Errorf("create microsoft binding recovery lease: %w", err)
		}
		claimed = true
		return nil
	})
	if !claimed {
		claimToken = ""
	}
	return claimToken, claimed, err
}

func (s *MicrosoftBindingRecoveryLeaseStore) MarkSent(ctx context.Context, normalizedMask, claimToken string, sentAt time.Time) error {
	result := s.db.WithContext(ctx).Model(&MicrosoftBindingRecoveryLeaseModel{}).
		Where("normalized_mask = ? AND claim_token = ? AND lease_until > ?", normalizeBindingEmail(normalizedMask), strings.TrimSpace(claimToken), time.Now().UTC()).
		Updates(map[string]any{"sent_at": sentAt.UTC(), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("mark microsoft binding recovery lease sent: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("mark microsoft binding recovery lease sent: lease is no longer owned")
	}
	return nil
}

func (s *MicrosoftBindingRecoveryLeaseStore) Release(ctx context.Context, normalizedMask, claimToken string) error {
	if err := s.db.WithContext(ctx).Where("normalized_mask = ? AND claim_token = ?", normalizeBindingEmail(normalizedMask), strings.TrimSpace(claimToken)).
		Delete(&MicrosoftBindingRecoveryLeaseModel{}).Error; err != nil {
		return fmt.Errorf("release microsoft binding recovery lease: %w", err)
	}
	return nil
}
