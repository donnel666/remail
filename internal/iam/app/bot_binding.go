package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/iam/domain"
)

type BotBindingRepository interface {
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByThirdPartyIdentity(ctx context.Context, provider, providerUserID string) (*domain.User, error)
	FindThirdPartyProviderUserID(ctx context.Context, userID uint, provider string) (string, error)
	BindThirdPartyIdentityWithPasswordSnapshot(ctx context.Context, userID uint, expectedPasswordHash, provider, providerUserID string) error
	DeleteThirdPartyIdentity(ctx context.Context, provider, providerUserID string) error
}

type BotBindingUseCase struct {
	repo   BotBindingRepository
	hasher Hasher
}

type BotBindingInfo struct {
	Bound       bool
	Available   bool
	MaskedEmail string
}

type BotBindingContext struct {
	UserID             uint
	Role               domain.Role
	UserGroup          domain.UserGroup
	PriceDiscountRatio string
}

type BotBindingResolution struct {
	Bound     bool
	Available bool
	User      BotBindingContext
}

func NewBotBindingUseCase(repo BotBindingRepository, hasher Hasher) *BotBindingUseCase {
	return &BotBindingUseCase{repo: repo, hasher: hasher}
}

// Bind verifies the supplied local credential without creating a login
// session, updating last-login state, or sending a login notification.
func (uc *BotBindingUseCase) Bind(ctx context.Context, platform, namespace, subject, email, password string) (BotBindingInfo, error) {
	provider, subject, ok := normalizeBotIdentity(platform, namespace, subject)
	email = normalizeEmail(email)
	if uc == nil || uc.repo == nil || uc.hasher == nil || !ok || validateEmailAddress(email) != nil || password == "" || len(password) > 1024 {
		return BotBindingInfo{}, domain.ErrAccountOrPasswordIncorrect
	}
	user, err := uc.repo.FindByEmail(ctx, email)
	if err != nil {
		return BotBindingInfo{}, fmt.Errorf("bot binding find account: %w", err)
	}
	if user == nil || !uc.hasher.Verify(password, user.PasswordHash) || !user.IsActive() {
		return BotBindingInfo{}, domain.ErrAccountOrPasswordIncorrect
	}
	if err := uc.repo.BindThirdPartyIdentityWithPasswordSnapshot(ctx, user.ID, user.PasswordHash, provider, subject); err != nil {
		return BotBindingInfo{}, fmt.Errorf("bot binding commit: %w", err)
	}
	return BotBindingInfo{Bound: true, Available: true, MaskedEmail: maskBotBindingEmail(user.Email)}, nil
}

func (uc *BotBindingUseCase) Get(ctx context.Context, platform, namespace, subject string) (BotBindingInfo, error) {
	user, err := uc.find(ctx, platform, namespace, subject)
	if err != nil || user == nil {
		return BotBindingInfo{}, err
	}
	if !user.IsActive() {
		return BotBindingInfo{Bound: true}, nil
	}
	return BotBindingInfo{Bound: true, Available: true, MaskedEmail: maskBotBindingEmail(user.Email)}, nil
}

func (uc *BotBindingUseCase) Unbind(ctx context.Context, platform, namespace, subject string) error {
	provider, subject, ok := normalizeBotIdentity(platform, namespace, subject)
	if uc == nil || uc.repo == nil || !ok {
		return domain.ErrThirdPartyIdentityUnavailable
	}
	if err := uc.repo.DeleteThirdPartyIdentity(ctx, provider, subject); err != nil {
		return fmt.Errorf("bot binding delete: %w", err)
	}
	return nil
}

// QQNumber returns the plaintext QQ number bound through the current QQ Bot scope.
func (uc *BotBindingUseCase) QQNumber(ctx context.Context, userID uint) (string, error) {
	if uc == nil || uc.repo == nil || userID == 0 {
		return "", domain.ErrThirdPartyIdentityUnavailable
	}
	provider := botQQProvider()
	value, err := uc.repo.FindThirdPartyProviderUserID(ctx, userID, provider)
	if err != nil {
		return "", fmt.Errorf("find QQ bot identity: %w", err)
	}
	value = strings.TrimSpace(value)
	if !isPositiveDecimalBotSubject(value) {
		return "", nil
	}
	return value, nil
}

// BotQQNumber projects only the current QQ Bot identity from an already loaded
// identity collection. LinuxDO, Telegram and other providers never match.
func BotQQNumber(identities []domain.ThirdPartyIdentity) string {
	provider := botQQProvider()
	for _, identity := range identities {
		value := strings.TrimSpace(identity.ProviderUserID)
		if identity.Provider == provider && isPositiveDecimalBotSubject(value) {
			return value
		}
	}
	return ""
}

func botQQProvider() string {
	// ponytail: the settings UI currently owns one QQ scope; accept a provider
	// set here only when multiple QQ namespaces become a real requirement.
	provider, _, _ := normalizeBotIdentity("qq", "qq:main", "1")
	return provider
}

// ResolveActiveUserID is the internal bridge used by other remail Bot
// capabilities. The ID is never serialized by this use case's HTTP handlers.
func (uc *BotBindingUseCase) ResolveActiveUserID(ctx context.Context, platform, namespace, subject string) (uint, bool, error) {
	resolved, found, err := uc.ResolveActiveUser(ctx, platform, namespace, subject)
	return resolved.UserID, found, err
}

func (uc *BotBindingUseCase) ResolveActiveUser(ctx context.Context, platform, namespace, subject string) (BotBindingContext, bool, error) {
	resolution, err := uc.ResolveBinding(ctx, platform, namespace, subject)
	return resolution.User, resolution.Bound && resolution.Available, err
}

func (uc *BotBindingUseCase) ResolveBinding(ctx context.Context, platform, namespace, subject string) (BotBindingResolution, error) {
	user, err := uc.find(ctx, platform, namespace, subject)
	if err != nil || user == nil {
		return BotBindingResolution{}, err
	}
	if !user.IsActive() {
		return BotBindingResolution{Bound: true}, nil
	}
	ratio := strings.TrimSpace(user.UserGroup.PriceDiscountRatio)
	if ratio == "" {
		ratio = "1"
	}
	return BotBindingResolution{
		Bound: true, Available: true,
		User: BotBindingContext{
			UserID: user.ID, Role: user.Role, UserGroup: user.UserGroup,
			PriceDiscountRatio: ratio,
		},
	}, nil
}

func (uc *BotBindingUseCase) find(ctx context.Context, platform, namespace, subject string) (*domain.User, error) {
	provider, subject, ok := normalizeBotIdentity(platform, namespace, subject)
	if uc == nil || uc.repo == nil || !ok {
		return nil, domain.ErrThirdPartyIdentityUnavailable
	}
	user, err := uc.repo.FindByThirdPartyIdentity(ctx, provider, subject)
	if err != nil {
		return nil, fmt.Errorf("bot binding find identity: %w", err)
	}
	return user, nil
}

func normalizeBotIdentity(platform, namespace, subject string) (string, string, bool) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	subject = strings.TrimSpace(subject)
	if platform == "" || len(platform) > 32 || namespace == "" || len(namespace) > 50 || subject == "" || len(subject) > 255 || !utf8.ValidString(subject) {
		return "", "", false
	}
	for _, char := range subject {
		if unicode.IsControl(char) {
			return "", "", false
		}
	}
	if (platform == "aiocqhttp" || strings.HasPrefix(platform, "qq") || strings.HasPrefix(platform, "onebot") ||
		platform == "telegram" || namespace == "qq" || strings.HasPrefix(namespace, "qq:") ||
		namespace == "telegram" || strings.HasPrefix(namespace, "telegram:")) && !isPositiveDecimalBotSubject(subject) {
		return "", "", false
	}
	scopeHash := sha256.Sum256([]byte(platform + "\x00" + namespace))
	// "bot:" plus 23 hash bytes in hex fits the existing provider VARCHAR(50).
	// The provider stays scoped, while provider_user_id is the trusted plaintext
	// identity supplied by the adapter (for QQ, the QQ number itself).
	return "bot:" + hex.EncodeToString(scopeHash[:23]), subject, true
}

func isPositiveDecimalBotSubject(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func maskBotBindingEmail(email string) string {
	local, domainName, ok := strings.Cut(normalizeEmail(email), "@")
	if !ok || local == "" || domainName == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domainName
}
