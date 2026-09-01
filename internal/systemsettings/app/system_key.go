package app

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
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

func (uc *SystemKeyUseCase) Create(ctx context.Context, name string, purpose domain.SystemKeyPurpose, meta MutationMeta) (*domain.SystemKey, error) {
	return uc.CreateWithScope(ctx, name, purpose, "", "", meta)
}

func (uc *SystemKeyUseCase) CreateWithScope(ctx context.Context, name string, purpose domain.SystemKeyPurpose, platform, subjectNamespace string, meta MutationMeta, allowedGroupIDs ...string) (*domain.SystemKey, error) {
	name = strings.TrimSpace(name)
	platform, subjectNamespace, allowedGroupIDs, scopeOK := normalizeSystemKeyScope(purpose, platform, subjectNamespace, allowedGroupIDs)
	if uc == nil || uc.repo == nil || uc.logs == nil || meta.OperatorUserID == 0 || name == "" || utf8.RuneCountInString(name) > 120 || !validSystemKeyPurpose(purpose) || !scopeOK {
		return nil, domain.ErrInvalidSystemKey
	}
	plain, hash, err := newSystemKeyCredential()
	if err != nil {
		return nil, fmt.Errorf("generate system key: %w", err)
	}
	key := domain.SystemKey{
		Name: name, Purpose: purpose, Platform: platform, SubjectNamespace: subjectNamespace,
		AllowedGroupIDs: allowedGroupIDs,
		KeyPrefix:       credentialPrefix(plain), KeyPlain: plain,
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
	key, err := uc.authenticateSystemKey(ctx, plain, domain.SystemKeyPurposeICloudForwarding)
	if err != nil {
		return 0, err
	}
	return key.ID, nil
}

func (uc *SystemKeyUseCase) AuthenticateSMTPSubmissionKey(ctx context.Context, plain string) (uint, error) {
	key, err := uc.authenticateSystemKey(ctx, plain, domain.SystemKeyPurposeSMTPSubmission)
	if err != nil {
		return 0, err
	}
	return key.ID, nil
}

// AuthenticateBotSystemKey validates a bot-scoped key and returns only its
// safe integration metadata. The plaintext and stored hash are never present
// on a loaded SystemKey.
func (uc *SystemKeyUseCase) AuthenticateBotSystemKey(ctx context.Context, plain string) (*domain.SystemKey, error) {
	return uc.authenticateSystemKey(ctx, plain, domain.SystemKeyPurposeBot)
}

func (uc *SystemKeyUseCase) authenticateSystemKey(ctx context.Context, plain string, purpose domain.SystemKeyPurpose) (*domain.SystemKey, error) {
	plain = strings.TrimSpace(plain)
	if uc == nil || uc.repo == nil || !validSystemKeyCredential(plain) || !validSystemKeyPurpose(purpose) {
		return nil, domain.ErrInvalidSystemKey
	}
	key, err := uc.repo.FindSystemKeyByHash(ctx, systemKeyHash(plain))
	if err != nil {
		return nil, err
	}
	if key == nil || key.ID == 0 || key.Purpose != purpose {
		return nil, domain.ErrInvalidSystemKey
	}
	platform, subjectNamespace, allowedGroupIDs, ok := normalizeSystemKeyScope(key.Purpose, key.Platform, key.SubjectNamespace, key.AllowedGroupIDs)
	if !ok {
		return nil, domain.ErrInvalidSystemKey
	}
	key.Platform, key.SubjectNamespace, key.AllowedGroupIDs = platform, subjectNamespace, allowedGroupIDs
	now := uc.now()
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= systemKeyLastUsedTouchInterval {
		if err := uc.repo.TouchSystemKey(ctx, key.ID, now); err != nil {
			return nil, err
		}
	}
	return key, nil
}

func validSystemKeyPurpose(purpose domain.SystemKeyPurpose) bool {
	return purpose == domain.SystemKeyPurposeICloudForwarding || purpose == domain.SystemKeyPurposeSMTPSubmission || purpose == domain.SystemKeyPurposeBot
}

func normalizeSystemKeyScope(purpose domain.SystemKeyPurpose, platform, subjectNamespace string, allowedGroupIDs []string) (string, string, []string, bool) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	subjectNamespace = strings.ToLower(strings.TrimSpace(subjectNamespace))
	if purpose != domain.SystemKeyPurposeBot {
		return platform, subjectNamespace, nil, platform == "" && subjectNamespace == "" && len(allowedGroupIDs) == 0
	}
	groups, groupsOK := normalizeBotGroupIDs(subjectNamespace, allowedGroupIDs)
	return platform, subjectNamespace, groups,
		validScopeToken(platform, 32, false) && validScopeToken(subjectNamespace, 50, true) &&
			validBotKeyScope(platform, subjectNamespace) && groupsOK
}

func validBotKeyScope(platform, namespace string) bool {
	return platform == "qq" && namespace == "qq:main" ||
		platform == "telegram" && namespace == "telegram:main"
}

func normalizeBotGroupIDs(namespace string, values []string) ([]string, bool) {
	if len(values) == 0 || len(values) > 100 {
		return nil, false
	}
	groups := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	qq := namespace == "qq" || strings.HasPrefix(namespace, "qq:")
	telegram := namespace == "telegram" || strings.HasPrefix(namespace, "telegram:")
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || !utf8.ValidString(value) {
			return nil, false
		}
		for _, char := range value {
			if unicode.IsControl(char) {
				return nil, false
			}
		}
		if qq {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil || parsed == 0 {
				return nil, false
			}
			value = strconv.FormatUint(parsed, 10)
		} else if telegram {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed == 0 {
				return nil, false
			}
			value = strconv.FormatInt(parsed, 10)
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			groups = append(groups, value)
		}
	}
	sort.Strings(groups)
	return groups, len(groups) > 0
}

func validScopeToken(value string, maxLength int, allowColon bool) bool {
	if value == "" || len(value) > maxLength {
		return false
	}
	for i, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || i > 0 && (char == '_' || char == '-' || char == '.' || allowColon && char == ':') {
			continue
		}
		return false
	}
	return true
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
