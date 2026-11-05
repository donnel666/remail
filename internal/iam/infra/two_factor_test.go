package infra

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTwoFactorTestService(t *testing.T) (*TwoFactorService, *miniredis.Miniredis) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT NOT NULL, token_version INTEGER NOT NULL DEFAULT 0)").Error)
	require.NoError(t, db.AutoMigrate(&TwoFactorModel{}))
	require.NoError(t, db.Exec("INSERT INTO users (id,status,token_version) VALUES (7,'active',0)").Error)
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	return NewTwoFactorService(db, rdb), server
}

type failingTwoFactorEvalRedis struct{ redis.UniversalClient }

func (failingTwoFactorEvalRedis) EvalSha(context.Context, string, []string, ...interface{}) *redis.Cmd {
	return redis.NewCmdResult(nil, fmt.Errorf("redis consume failed"))
}

func TestTwoFactorSetupOnlyEnablesAfterValidCodeAndGuardsOldSetup(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	uri, token, err := service.Setup(ctx, 7, "user@example.test", time.Minute)
	require.NoError(t, err)
	parsed, err := url.Parse(uri)
	require.NoError(t, err)
	secret := parsed.Query().Get("secret")
	require.NotEmpty(t, secret)
	enabled, err := service.Enabled(ctx, 7)
	require.NoError(t, err)
	require.False(t, enabled)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, service.Enable(ctx, 7, token, code))
	require.False(t, server.Exists(twoFactorSetupKey(7)))
	require.ErrorIs(t, service.Enable(ctx, 7, token, code), domain.ErrTwoFactorSetupExpired)
	enabled, err = service.Enabled(ctx, 7)
	require.NoError(t, err)
	require.True(t, enabled)
	var version int
	require.NoError(t, service.db.Table("users").Select("token_version").Where("id = 7").Scan(&version).Error)
	require.Equal(t, 1, version)
}

func TestTwoFactorSetupKeepsOnePendingSecretPerUser(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	firstURI, firstToken, err := service.Setup(ctx, 7, "user@example.test", time.Minute)
	require.NoError(t, err)
	secondURI, secondToken, err := service.Setup(ctx, 7, "user@example.test", time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, firstURI, secondURI)
	require.NotEqual(t, firstToken, secondToken)
	require.Len(t, server.Keys(), 1)
	firstKey, err := url.Parse(firstURI)
	require.NoError(t, err)
	firstCode, err := totp.GenerateCode(firstKey.Query().Get("secret"), time.Now())
	require.NoError(t, err)
	require.ErrorIs(t, service.Enable(ctx, 7, firstToken, firstCode), domain.ErrTwoFactorSetupExpired)
	secondKey, err := url.Parse(secondURI)
	require.NoError(t, err)
	secondCode, err := totp.GenerateCode(secondKey.Query().Get("secret"), time.Now())
	require.NoError(t, err)
	require.NoError(t, service.Enable(ctx, 7, secondToken, secondCode))
}

func TestTwoFactorLoginChallengeIsSingleUseAndRejectsReplayedStep(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, service.db.Create(&TwoFactorModel{UserID: 7, Secret: secret}).Error)
	pending := domain.LoginChallenge{UserID: 7, TokenVersion: 0}
	require.NoError(t, service.BeginLogin(ctx, "one", pending, time.Minute))
	require.NoError(t, service.BeginLogin(ctx, "two", pending, time.Minute))
	// A user has one live challenge; starting a second login invalidates the first.
	require.False(t, server.Exists(twoFactorChallengePrefix+"one"))
	require.True(t, server.Exists(twoFactorChallengePrefix+"two"))
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	_, err = service.ConsumeLogin(ctx, "one", code)
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	got, err := service.ConsumeLogin(ctx, "two", code)
	require.NoError(t, err)
	require.Equal(t, &pending, got)
	_, err = service.ConsumeLogin(ctx, "two", code)
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
}

func TestTwoFactorLoginLimitsAcrossFreshChallengesAndExpires(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, service.db.Create(&TwoFactorModel{UserID: 7, Secret: secret}).Error)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	wrongCode := string('0'+(code[0]-'0'+1)%10) + code[1:]
	for i := 0; i < 10; i++ {
		require.NoError(t, service.BeginLogin(ctx, fmt.Sprintf("ticket-%d", i), domain.LoginChallenge{UserID: 7}, time.Minute))
		_, err := service.ConsumeLogin(ctx, fmt.Sprintf("ticket-%d", i), wrongCode)
		require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	}
	require.NoError(t, service.BeginLogin(ctx, "ticket-new", domain.LoginChallenge{UserID: 7}, time.Minute))
	_, err = service.ConsumeLogin(ctx, "ticket-new", code)
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	require.True(t, server.Exists(twoFactorAttemptsPrefix+"user:7"))
	server.FastForward(5 * time.Minute)
	require.False(t, server.Exists(twoFactorAttemptsPrefix+"user:7"))
	_, err = service.ConsumeLogin(ctx, "ticket-new", "000000")
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	require.NoError(t, service.BeginLogin(ctx, "after-cooldown", domain.LoginChallenge{UserID: 7}, time.Minute))
	_, err = service.ConsumeLogin(ctx, "after-cooldown", code)
	require.NoError(t, err)
}

func TestTwoFactorDisableLimitsCodeGuessing(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, service.db.Create(&TwoFactorModel{UserID: 7, Secret: secret}).Error)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	wrongCode := string('0'+(code[0]-'0'+1)%10) + code[1:]
	for i := 0; i < 10; i++ {
		require.ErrorIs(t, service.Disable(ctx, 7, wrongCode), domain.ErrTwoFactorCodeIncorrect)
	}
	require.ErrorIs(t, service.Disable(ctx, 7, code), domain.ErrTwoFactorCodeIncorrect)
	server.FastForward(5 * time.Minute)
	require.NoError(t, service.Disable(ctx, 7, code))
	enabled, err := service.Enabled(ctx, 7)
	require.NoError(t, err)
	require.False(t, enabled)
}

func TestTwoFactorEnableDoesNotOverwriteAnEnabledSecret(t *testing.T) {
	service, _ := newTwoFactorTestService(t)
	ctx := context.Background()
	uri, token, err := service.Setup(ctx, 7, "user@example.test", time.Minute)
	require.NoError(t, err)
	parsed, err := url.Parse(uri)
	require.NoError(t, err)
	code, err := totp.GenerateCode(parsed.Query().Get("secret"), time.Now())
	require.NoError(t, err)
	require.NoError(t, service.db.Create(&TwoFactorModel{UserID: 7, Secret: "existing"}).Error)
	require.ErrorIs(t, service.Enable(ctx, 7, token, code), domain.ErrTwoFactorAlreadyEnabled)
	var row TwoFactorModel
	require.NoError(t, service.db.First(&row, "user_id = 7").Error)
	require.Equal(t, "existing", row.Secret)
}

func TestTwoFactorChallengeAtomicConsumption(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	ctx := context.Background()
	require.NoError(t, service.BeginLogin(ctx, "atomic", domain.LoginChallenge{UserID: 7}, time.Minute))
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := consumeTwoFactorChallenge.Run(ctx, service.rdb, []string{twoFactorChallengePrefix + "atomic", twoFactorAttemptsPrefix + "atomic", twoFactorActiveChallengeKey(7)}, "atomic").Text()
			if err == nil && value != "" {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, success)
	require.False(t, server.Exists(twoFactorChallengePrefix+"atomic"))
}

func TestTwoFactorUnknownChallengeDoesNotCreateRedisAttemptKeys(t *testing.T) {
	service, server := newTwoFactorTestService(t)
	_, err := service.ConsumeLogin(context.Background(), "unknown-ticket", "123456")
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	require.Empty(t, server.Keys())
}

func TestTwoFactorChallengeDependencyFailureIsNotAnInvalidCode(t *testing.T) {
	service, _ := newTwoFactorTestService(t)
	ctx := context.Background()
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, service.db.Create(&TwoFactorModel{UserID: 7, Secret: secret}).Error)
	require.NoError(t, service.BeginLogin(ctx, "redis-failure", domain.LoginChallenge{UserID: 7}, time.Minute))
	service.rdb = failingTwoFactorEvalRedis{service.rdb}
	_, err := service.ConsumeLogin(ctx, "redis-failure", "123456")
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	var row TwoFactorModel
	require.NoError(t, service.db.First(&row, "user_id = 7").Error)
	require.Zero(t, row.LastUsedStep)
}
