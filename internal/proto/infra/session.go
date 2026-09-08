package infra

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const sessionOperationTimeout = 15 * time.Minute
const sessionLeaseDuration = 16 * time.Minute
const sessionRefreshTimeout = time.Minute
const sessionRefreshLeaseDuration = 90 * time.Second

var (
	ErrSessionUnavailable = errors.New("proto: session unavailable; validation is required")
	ErrSessionBusy        = errors.New("proto: session is in use")
)

type sessionRecord struct {
	ResourceID         uint `gorm:"primaryKey;autoIncrement:false"`
	CredentialRevision uint64
	Version            uint64
	Payload            []byte
	LeaseToken         string
	LeaseExpiresAt     *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (sessionRecord) TableName() string { return "proto_sessions" }

func (s *Service) sessionCipher() (cipher.AEAD, error) {
	if s == nil || s.SessionSecret == "" {
		return nil, domain.ErrDependency
	}
	key, err := hkdf.Key(sha256.New, []byte(s.SessionSecret), nil, "remail/proto/session/v1", 32)
	if err != nil {
		return nil, domain.ErrDependency
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, domain.ErrDependency
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func sessionAAD(id uint, revision uint64) []byte {
	return []byte(fmt.Sprintf("remail/proto/session/v1:%d:%d", id, revision))
}

func (s *Service) encryptSession(id uint, revision uint64, session proton.Session) ([]byte, error) {
	aead, err := s.sessionCipher()
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(session)
	if err != nil {
		return nil, ErrSessionUnavailable
	}
	return append([]byte{1}, aead.Seal(nil, nil, plain, sessionAAD(id, revision))...), nil
}

func (s *Service) decryptSession(row *sessionRecord) (*proton.Session, error) {
	aead, err := s.sessionCipher()
	if err != nil {
		return nil, err
	}
	if len(row.Payload) < 2 || row.Payload[0] != 1 {
		return nil, ErrSessionUnavailable
	}
	plain, err := aead.Open(nil, nil, row.Payload[1:], sessionAAD(row.ResourceID, row.CredentialRevision))
	if err != nil {
		return nil, ErrSessionUnavailable
	}
	var session proton.Session
	if json.Unmarshal(plain, &session) != nil {
		return nil, ErrSessionUnavailable
	}
	return &session, nil
}

func validSession(session proton.Session, email string) bool {
	if session.Version != 1 || session.UID == "" || session.AccessToken == "" || session.RefreshToken == "" {
		return false
	}
	for _, address := range session.Addresses {
		if address.ID != "" && strings.EqualFold(address.Email, email) && len(address.PrivateKeys) > 0 {
			for _, key := range address.PrivateKeys {
				if strings.TrimSpace(key) != "" {
					return true
				}
			}
		}
	}
	return false
}

func deleteSessionTx(tx *gorm.DB, id uint) error {
	return tx.Where("resource_id = ?", id).Delete(&sessionRecord{}).Error
}

func (s *Service) RequeueSessionValidation(ctx context.Context, id uint, revision, expectedGeneration uint64, expectedRefreshToken, reason string) error {
	return s.mutateResource(ctx, id, nil, func(_ context.Context, tx *gorm.DB, row *Resource) error {
		if row.CredentialRevision != revision || row.ValidationGeneration != expectedGeneration || (row.Status != domain.StatusNormal && row.Status != domain.StatusIdentifying) {
			return nil
		}
		stored, err := lockSessionTx(tx, id)
		if err != nil && !errors.Is(err, ErrSessionUnavailable) {
			return err
		}
		var current *proton.Session
		if stored != nil && stored.CredentialRevision == revision {
			current, err = s.decryptSession(stored)
			if err != nil && !errors.Is(err, ErrSessionUnavailable) {
				return err
			}
		}
		usable := current != nil && validSession(*current, row.EmailAddress)
		if expectedRefreshToken == "" {
			if usable {
				return nil
			}
		} else if !usable || current.RefreshToken != expectedRefreshToken {
			return nil
		}
		if err := deleteSessionTx(tx, id); err != nil {
			return err
		}
		invalidateResource(row, s.Now().UTC(), false)
		row.LastSafeError = safeSessionError(reason)
		return nil
	})
}

func lockSessionTx(tx *gorm.DB, id uint) (*sessionRecord, error) {
	var row sessionRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionUnavailable
		}
		return nil, err
	}
	return &row, nil
}

// ReadSession returns a credential-fenced snapshot without acquiring or waiting
// for the refresh lease. Reading mail must not serialize behind history scans.
func (s *Service) ReadSession(ctx context.Context, id uint, revision uint64) (*proton.Session, error) {
	if id == 0 || revision == 0 {
		return nil, domain.ErrInvalidResource
	}
	var session *proton.Session
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		resource, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		if resource.CredentialRevision != revision || resource.Status == domain.StatusDeleted {
			return domain.ErrInvalidClaim
		}
		row, err := lockSessionTx(tx, id)
		if err != nil {
			return err
		}
		if row.CredentialRevision != revision {
			return ErrSessionUnavailable
		}
		session, err = s.decryptSession(row)
		if err != nil {
			return err
		}
		if !validSession(*session, resource.EmailAddress) {
			return ErrSessionUnavailable
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// WithSession serializes only refresh-token rotation across workers. Normal
// reads use ReadSession and do not hold this lease during mailbox requests.
// ponytail: the 90s lease covers a 60s refresh; renew the lease if refresh work grows.
func (s *Service) WithSession(ctx context.Context, id uint, revision uint64, fn func(context.Context, *proton.Session, func(proton.Session) error) error) error {
	if id == 0 || revision == 0 || fn == nil {
		return domain.ErrInvalidResource
	}
	if _, inTransaction := platform.GormTxFromContext(ctx); inTransaction {
		return domain.ErrDependency
	}
	ctx, cancel := context.WithTimeout(ctx, sessionRefreshTimeout)
	defer cancel()
	token := platform.NewUUIDV7String()
	var session *proton.Session
	var version uint64
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		resource, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		if resource.CredentialRevision != revision || resource.Status == domain.StatusDeleted {
			return domain.ErrInvalidClaim
		}
		row, err := lockSessionTx(tx, id)
		if err != nil {
			return err
		}
		if row.CredentialRevision != revision {
			return ErrSessionUnavailable
		}
		now := s.Now().UTC()
		if row.LeaseToken != "" && row.LeaseExpiresAt != nil && row.LeaseExpiresAt.After(now) {
			return ErrSessionBusy
		}
		session, err = s.decryptSession(row)
		if err != nil {
			return err
		}
		if !validSession(*session, resource.EmailAddress) {
			return ErrSessionUnavailable
		}
		version = row.Version
		return tx.Model(row).Updates(map[string]any{"lease_token": token, "lease_expires_at": now.Add(sessionRefreshLeaseDuration), "updated_at": now}).Error
	})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = s.transaction(cleanup, func(_ context.Context, tx *gorm.DB) error {
			if _, err := lockResource(tx, id, nil); err != nil {
				return err
			}
			return tx.Model(&sessionRecord{}).Where("resource_id = ? AND credential_revision = ? AND lease_token = ?", id, revision, token).
				Updates(map[string]any{"lease_token": "", "lease_expires_at": nil}).Error
		})
	}()
	checkLease := func(tx *gorm.DB) (*Resource, error) {
		resource, err := lockResource(tx, id, nil)
		if err != nil {
			return nil, err
		}
		if resource.CredentialRevision != revision || resource.Status == domain.StatusDeleted {
			return nil, domain.ErrInvalidClaim
		}
		row, err := lockSessionTx(tx, id)
		if err != nil {
			return nil, err
		}
		if row.CredentialRevision != revision || row.Version != version || row.LeaseToken != token || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.After(s.Now().UTC()) {
			return nil, domain.ErrInvalidClaim
		}
		return resource, nil
	}
	save := func(next proton.Session) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, err := s.encryptSession(id, revision, next)
		if err != nil {
			return err
		}
		err = s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
			resource, err := checkLease(tx)
			if err != nil {
				return err
			}
			if !validSession(next, resource.EmailAddress) {
				return ErrSessionUnavailable
			}
			return tx.Model(&sessionRecord{}).Where("resource_id = ?", id).Updates(map[string]any{"payload": payload, "version": version + 1, "updated_at": s.Now().UTC()}).Error
		})
		if err == nil {
			version++
			*session = next
		}
		return err
	}
	if err := fn(ctx, session, save); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		if _, err := checkLease(tx); err != nil {
			return err
		}
		return tx.Model(&sessionRecord{}).Where("resource_id = ?", id).Updates(map[string]any{"lease_token": "", "lease_expires_at": nil}).Error
	})
}
