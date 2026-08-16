package app

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/systemsettings/domain"
)

const (
	systemKeyPrefix                = "sk_"
	systemKeyRandomBytes           = 32
	systemKeyLastUsedTouchInterval = time.Minute
)

type SystemKeyRepository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	CreateSystemKey(ctx context.Context, key domain.SystemKey, keyHash string) (*domain.SystemKey, error)
	ListSystemKeys(ctx context.Context) ([]domain.SystemKey, error)
	FindSystemKeyByHash(ctx context.Context, keyHash string) (*domain.SystemKey, error)
	DeleteSystemKey(ctx context.Context, keyID uint, deletedAt time.Time) error
	TouchSystemKey(ctx context.Context, keyID uint, usedAt time.Time) error
}

type SystemKeyUseCase struct {
	repo SystemKeyRepository
	logs governanceapp.OperationLogPort
	now  func() time.Time
}

func NewSystemKeyUseCase(repo SystemKeyRepository, logs governanceapp.OperationLogPort) *SystemKeyUseCase {
	return &SystemKeyUseCase{repo: repo, logs: logs, now: func() time.Time { return time.Now().UTC() }}
}

func (uc *SystemKeyUseCase) Create(ctx context.Context, name string, meta MutationMeta) (*domain.SystemKey, error) {
	name = strings.TrimSpace(name)
	if uc == nil || uc.repo == nil || uc.logs == nil || meta.OperatorUserID == 0 || name == "" || utf8.RuneCountInString(name) > 120 {
		return nil, domain.ErrInvalidSystemKey
	}
	plain, hash, err := newSystemKeyCredential()
	if err != nil {
		return nil, fmt.Errorf("generate system key: %w", err)
	}
	key := domain.SystemKey{
		Name: name, KeyPrefix: credentialPrefix(plain), KeyPlain: plain,
		CreatedAt: uc.now(),
	}
	var created *domain.SystemKey
	err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		var createErr error
		created, createErr = uc.repo.CreateSystemKey(txCtx, key, hash)
		if createErr != nil {
			return createErr
		}
		return uc.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: meta.OperatorUserID,
			OperationType:  "system_key.create",
			ResourceType:   "system_key",
			ResourceID:     fmt.Sprintf("system-key:%d", created.ID),
			Path:           meta.Path,
			Result:         "success",
			SafeSummary:    "System key created.",
			RequestID:      meta.RequestID,
		})
	})
	return created, err
}

func (uc *SystemKeyUseCase) List(ctx context.Context) ([]domain.SystemKey, error) {
	if uc == nil || uc.repo == nil {
		return nil, domain.ErrInvalidSystemKey
	}
	return uc.repo.ListSystemKeys(ctx)
}

func (uc *SystemKeyUseCase) Delete(ctx context.Context, keyID uint, meta MutationMeta) error {
	if uc == nil || uc.repo == nil || uc.logs == nil || keyID == 0 || meta.OperatorUserID == 0 {
		return domain.ErrInvalidSystemKey
	}
	return uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := uc.repo.DeleteSystemKey(txCtx, keyID, uc.now()); err != nil {
			return err
		}
		return uc.logs.Create(txCtx, &governancedomain.OperationLog{
			OperatorUserID: meta.OperatorUserID,
			OperationType:  "system_key.delete",
			ResourceType:   "system_key",
			ResourceID:     fmt.Sprintf("system-key:%d", keyID),
			Path:           meta.Path,
			Result:         "success",
			SafeSummary:    "System key revoked.",
			RequestID:      meta.RequestID,
		})
	})
}

func (uc *SystemKeyUseCase) AuthenticateSystemKey(ctx context.Context, plain string) (uint, error) {
	plain = strings.TrimSpace(plain)
	if uc == nil || uc.repo == nil || !validSystemKeyCredential(plain) {
		return 0, domain.ErrInvalidSystemKey
	}
	key, err := uc.repo.FindSystemKeyByHash(ctx, systemKeyHash(plain))
	if err != nil {
		return 0, err
	}
	if key == nil || key.ID == 0 {
		return 0, domain.ErrInvalidSystemKey
	}
	now := uc.now()
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= systemKeyLastUsedTouchInterval {
		if err := uc.repo.TouchSystemKey(ctx, key.ID, now); err != nil {
			return 0, err
		}
	}
	return key.ID, nil
}

func newSystemKeyCredential() (plain, hash string, err error) {
	random := make([]byte, systemKeyRandomBytes)
	if _, err = cryptorand.Read(random); err != nil {
		return "", "", err
	}
	plain = systemKeyPrefix + base64.RawURLEncoding.EncodeToString(random)
	return plain, systemKeyHash(plain), nil
}

func validSystemKeyCredential(plain string) bool {
	if !strings.HasPrefix(plain, systemKeyPrefix) {
		return false
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, systemKeyPrefix))
	return err == nil && len(random) == systemKeyRandomBytes
}

func systemKeyHash(plain string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plain)))
	return hex.EncodeToString(sum[:])
}

func credentialPrefix(plain string) string {
	if len(plain) <= 15 {
		return plain
	}
	return plain[:15]
}
