package infra

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	twoFactorSetupPrefix           = "iam:2fa:setup:user:"
	twoFactorChallengePrefix       = "iam:2fa:login:"
	twoFactorActiveChallengePrefix = "iam:2fa:active:"
	twoFactorAttemptsPrefix        = "iam:2fa:attempts:"
	twoFactorMaxAttempts           = 5
)

var consumeTwoFactorChallenge = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return nil end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
if redis.call('GET', KEYS[3]) == ARGV[1] then
  redis.call('DEL', KEYS[3])
end
return value
`)

var beginTwoFactorChallenge = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
if previous then
  redis.call('DEL', ARGV[2] .. previous)
  redis.call('DEL', ARGV[3] .. previous)
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[4])
redis.call('SET', KEYS[2], ARGV[5], 'EX', ARGV[4])
return previous or ''
`)

var takeTwoFactorAttempt = redis.NewScript(`
local attempts = redis.call('INCR', KEYS[1])
if attempts == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return attempts
`)

type TwoFactorModel struct {
	UserID       uint      `gorm:"primaryKey;column:user_id"`
	Secret       string    `gorm:"type:varchar(255);not null"`
	LastUsedStep int64     `gorm:"not null;default:0"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"not null;autoUpdateTime"`
}

func (TwoFactorModel) TableName() string { return "user_two_factors" }

type twoFactorSetup struct {
	UserID       uint   `json:"userId"`
	TokenVersion int    `json:"tokenVersion"`
	Token        string `json:"token"`
	Secret       string `json:"secret"`
}

// TwoFactorService is the Redis/GORM implementation used by IAM. TOTP
// secrets are deliberately never returned from this package after enablement.
type TwoFactorService struct {
	db  *gorm.DB
	rdb redis.UniversalClient
}

func NewTwoFactorService(db *gorm.DB, rdb redis.UniversalClient) *TwoFactorService {
	return &TwoFactorService{db: db, rdb: rdb}
}

func twoFactorSetupKey(userID uint) string {
	return twoFactorSetupPrefix + strconv.FormatUint(uint64(userID), 10)
}

func twoFactorActiveChallengeKey(userID uint) string {
	return twoFactorActiveChallengePrefix + strconv.FormatUint(uint64(userID), 10)
}

func (s *TwoFactorService) Enabled(ctx context.Context, userID uint) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&TwoFactorModel{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check two-factor status: %w", err)
	}
	return count > 0, nil
}

func (s *TwoFactorService) EnabledForUsers(ctx context.Context, userIDs []uint) (map[uint]bool, error) {
	result := make(map[uint]bool)
	if len(userIDs) == 0 {
		return result, nil
	}
	var rows []TwoFactorModel
	if err := s.db.WithContext(ctx).Select("user_id").Where("user_id IN ?", userIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.UserID] = true
	}
	return result, nil
}

func (s *TwoFactorService) Setup(ctx context.Context, userID uint, account string, ttl time.Duration) (string, string, error) {
	if userID == 0 || strings.TrimSpace(account) == "" || ttl <= 0 {
		return "", "", domain.ErrTwoFactorSetupExpired
	}
	enabled, err := s.Enabled(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if enabled {
		return "", "", domain.ErrTwoFactorAlreadyEnabled
	}
	var user UserModel
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", userID, domain.UserStatusActive).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", domain.ErrAuthenticationRequired
		}
		return "", "", fmt.Errorf("load two-factor setup user: %w", err)
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Remail",
		AccountName: strings.TrimSpace(account),
		Period:      30,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", fmt.Errorf("generate two-factor secret: %w", err)
	}
	randomToken := make([]byte, 24)
	if _, err := rand.Read(randomToken); err != nil {
		return "", "", fmt.Errorf("generate two-factor setup token: %w", err)
	}
	token := hex.EncodeToString(randomToken)
	b, err := json.Marshal(twoFactorSetup{UserID: userID, TokenVersion: user.TokenVersion, Token: token, Secret: key.Secret()})
	if err != nil {
		return "", "", fmt.Errorf("marshal two-factor setup: %w", err)
	}
	if err := s.rdb.Set(ctx, twoFactorSetupKey(userID), b, ttl).Err(); err != nil {
		return "", "", fmt.Errorf("store two-factor setup: %w", err)
	}
	return key.URL(), token, nil
}

func (s *TwoFactorService) Enable(ctx context.Context, userID uint, token, code string) error {
	if userID == 0 || strings.TrimSpace(token) == "" {
		return domain.ErrTwoFactorSetupExpired
	}
	if err := s.takeAttempt(ctx, fmt.Sprintf("setup:%d", userID), 10); err != nil {
		return err
	}
	b, err := s.rdb.Get(ctx, twoFactorSetupKey(userID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return domain.ErrTwoFactorSetupExpired
	}
	if err != nil {
		return fmt.Errorf("get two-factor setup: %w", err)
	}
	var pending twoFactorSetup
	if err := json.Unmarshal(b, &pending); err != nil || pending.UserID != userID || pending.Token != token {
		return domain.ErrTwoFactorSetupExpired
	}
	step, err := validTwoFactorStep(code, pending.Secret)
	if err != nil {
		return domain.ErrTwoFactorCodeIncorrect
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&UserModel{}).Where("id = ? AND status = ? AND token_version = ?", userID, domain.UserStatusActive, pending.TokenVersion).
			UpdateColumn("token_version", gorm.Expr("token_version + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return domain.ErrTwoFactorSetupExpired
		}
		var existing int64
		if err := tx.Model(&TwoFactorModel{}).Where("user_id = ?", userID).Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return domain.ErrTwoFactorAlreadyEnabled
		}
		// SQL error logs must not include the plaintext TOTP secret.
		return tx.Session(&gorm.Session{Logger: logger.Discard}).Create(&TwoFactorModel{UserID: userID, Secret: pending.Secret, LastUsedStep: step}).Error
	}); err != nil {
		return fmt.Errorf("enable two-factor: %w", err)
	}
	if err := s.rdb.Del(ctx, twoFactorSetupKey(userID)).Err(); err != nil {
		// The database row and token version are authoritative. The key is
		// short-lived and will expire if Redis is temporarily unavailable.
		slog.WarnContext(ctx, "delete two-factor setup key failed", "user_id", userID, "error", err)
	}
	return nil
}

func (s *TwoFactorService) Disable(ctx context.Context, userID uint, code string) error {
	if err := s.takeAttempt(ctx, fmt.Sprintf("user:%d", userID), 10); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user UserModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", userID, domain.UserStatusActive).Take(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrAuthenticationRequired
			}
			return err
		}
		var row TwoFactorModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrTwoFactorNotEnabled
		} else if err != nil {
			return err
		}
		step, err := validTwoFactorStep(code, row.Secret)
		if err != nil || step <= row.LastUsedStep {
			return domain.ErrTwoFactorCodeIncorrect
		}
		if err := tx.Model(&UserModel{}).Where("id = ?", userID).UpdateColumn("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", userID).Delete(&TwoFactorModel{}).Error
	})
}

func (s *TwoFactorService) ForceDisable(ctx context.Context, userID uint, audit *governancedomain.OperationLog) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&UserModel{}).Where("id = ? AND status <> ?", userID, domain.UserStatusDeleted).UpdateColumn("token_version", gorm.Expr("token_version + 1"))
		if result.Error != nil {
			return fmt.Errorf("revoke two-factor sessions: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrUserNotFound
		}
		if err := tx.Where("user_id = ?", userID).Delete(&TwoFactorModel{}).Error; err != nil {
			return fmt.Errorf("disable two-factor: %w", err)
		}
		if audit != nil {
			return governanceinfra.NewOperationLogRepo(s.db).CreateInTx(ctx, tx, audit)
		}
		return nil
	})
}

func (s *TwoFactorService) BeginLogin(ctx context.Context, challenge string, pending domain.LoginChallenge, ttl time.Duration) error {
	if strings.TrimSpace(challenge) == "" || pending.UserID == 0 || ttl <= 0 {
		return errors.New("invalid two-factor challenge")
	}
	b, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal two-factor challenge: %w", err)
	}
	_, err = beginTwoFactorChallenge.Run(ctx, s.rdb,
		[]string{twoFactorActiveChallengeKey(pending.UserID), twoFactorChallengePrefix + challenge},
		challenge,
		twoFactorChallengePrefix,
		twoFactorAttemptsPrefix,
		strconv.FormatInt(int64(ttl/time.Second), 10),
		string(b),
	).Text()
	return err
}

func (s *TwoFactorService) ConsumeLogin(ctx context.Context, challenge, code string) (*domain.LoginChallenge, error) {
	challenge = strings.TrimSpace(challenge)
	if challenge == "" || len(challenge) > 128 {
		return nil, domain.ErrTwoFactorCodeIncorrect
	}
	key := twoFactorChallengePrefix + challenge
	attemptKey := twoFactorAttemptsPrefix + challenge
	b, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.ErrTwoFactorCodeIncorrect
	}
	if err != nil {
		return nil, fmt.Errorf("get two-factor challenge: %w", err)
	}
	attempts, err := s.rdb.Incr(ctx, attemptKey).Result()
	if err != nil {
		return nil, fmt.Errorf("count two-factor attempts: %w", err)
	}
	if attempts == 1 {
		if err := s.rdb.Expire(ctx, attemptKey, 5*time.Minute).Err(); err != nil {
			return nil, fmt.Errorf("expire two-factor attempts: %w", err)
		}
	}
	if attempts > twoFactorMaxAttempts {
		if err := s.rdb.Del(ctx, key, attemptKey).Err(); err != nil {
			return nil, fmt.Errorf("expire two-factor challenge: %w", err)
		}
		return nil, domain.ErrTwoFactorCodeIncorrect
	}
	var pending domain.LoginChallenge
	if err := json.Unmarshal(b, &pending); err != nil {
		return nil, domain.ErrTwoFactorCodeIncorrect
	}
	if err := s.takeAttempt(ctx, fmt.Sprintf("user:%d", pending.UserID), 10); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row TwoFactorModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", pending.UserID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrTwoFactorCodeIncorrect
			}
			return err
		}
		step, err := validTwoFactorStep(code, row.Secret)
		if err != nil || step <= row.LastUsedStep {
			return domain.ErrTwoFactorCodeIncorrect
		}
		value, err := consumeTwoFactorChallenge.Run(ctx, s.rdb,
			[]string{key, attemptKey, twoFactorActiveChallengeKey(pending.UserID)}, challenge,
		).Text()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return domain.ErrTwoFactorCodeIncorrect
			}
			return fmt.Errorf("consume two-factor challenge: %w", err)
		}
		if value == "" {
			return domain.ErrTwoFactorCodeIncorrect
		}
		return tx.Model(&TwoFactorModel{}).Where("user_id = ?", pending.UserID).UpdateColumn("last_used_step", step).Error
	}); err != nil {
		return nil, err
	}
	return &pending, nil
}

func validTwoFactorStep(code, secret string) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, domain.ErrTwoFactorCodeIncorrect
	}
	if _, err := strconv.ParseUint(code, 10, 32); err != nil {
		return 0, domain.ErrTwoFactorCodeIncorrect
	}
	step := time.Now().UTC().Unix() / 30
	for _, candidate := range []int64{step, step - 1, step + 1} {
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidate, nil
		}
	}
	return 0, domain.ErrTwoFactorCodeIncorrect
}

func (s *TwoFactorService) takeAttempt(ctx context.Context, scope string, limit int64) error {
	attempts, err := takeTwoFactorAttempt.Run(ctx, s.rdb, []string{twoFactorAttemptsPrefix + scope}, 5*60).Int64()
	if err != nil {
		return fmt.Errorf("count two-factor user attempts: %w", err)
	}
	if attempts > limit {
		return domain.ErrTwoFactorCodeIncorrect
	}
	return nil
}
