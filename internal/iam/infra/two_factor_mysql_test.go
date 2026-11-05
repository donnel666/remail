package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestTwoFactorConcurrentEnrollmentMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	ctx := context.Background()
	user := &domain.User{Email: "enroll@test.local", PasswordHash: "hash", Status: domain.UserStatusActive}
	require.NoError(t, NewUserRepo(db).Create(ctx, user))
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	svc := NewTwoFactorService(db, rdb)
	enabled, err := svc.Enabled(ctx, user.ID)
	require.NoError(t, err)
	require.False(t, enabled)

	type enrollment struct{ token, code, secret string }
	var pending [2]enrollment
	for i := range pending {
		uri, token, err := svc.Setup(ctx, user.ID, user.Email, 10*time.Minute)
		require.NoError(t, err)
		key, err := otp.NewKeyFromURL(uri)
		require.NoError(t, err)
		code, err := totp.GenerateCode(key.Secret(), time.Now())
		require.NoError(t, err)
		pending[i] = enrollment{token: token, code: code, secret: key.Secret()}
	}
	start := make(chan struct{})
	type outcome struct {
		index int
		err   error
	}
	results := make(chan outcome, len(pending))
	for i, setup := range pending {
		go func(i int, setup enrollment) {
			<-start
			results <- outcome{i, svc.Enable(ctx, user.ID, setup.token, setup.code)}
		}(i, setup)
	}
	close(start)
	winner := -1
	for range pending {
		result := <-results
		if result.err == nil {
			require.Equal(t, -1, winner, "only one enrollment may commit")
			winner = result.index
		} else {
			require.True(t, errors.Is(result.err, domain.ErrTwoFactorSetupExpired) || errors.Is(result.err, domain.ErrTwoFactorAlreadyEnabled), "%v", result.err)
		}
	}
	require.NotEqual(t, -1, winner)
	var row TwoFactorModel
	require.NoError(t, db.First(&row, "user_id = ?", user.ID).Error)
	require.Equal(t, pending[winner].secret, row.Secret)
	current, err := NewUserRepo(db).FindByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.TokenVersion+1, current.TokenVersion)
}

func TestTwoFactorResetAuditRollbackMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	ctx := context.Background()
	user := &domain.User{Email: "recover@test.local", PasswordHash: "hash", Status: domain.UserStatusActive}
	repo := NewUserRepo(db)
	require.NoError(t, repo.Create(ctx, user))
	require.NoError(t, db.Create(&TwoFactorModel{UserID: user.ID, Secret: "JBSWY3DPEHPK3PXP"}).Error)
	svc := NewTwoFactorService(db, nil)
	audit := &governancedomain.OperationLog{
		OperatorUserID: user.ID, OperationType: strings.Repeat("x", 101),
		ResourceType: "iam:user", ResourceID: strconv.FormatUint(uint64(user.ID), 10),
		Path: "/v1/admin/users/1/2fa", Result: "success", SafeSummary: "Reset two-factor authentication.",
	}
	require.Error(t, svc.ForceDisable(ctx, user.ID, audit), "an audit failure must roll back recovery")
	enabled, err := svc.Enabled(ctx, user.ID)
	require.NoError(t, err)
	require.True(t, enabled)
	current, err := repo.FindByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.TokenVersion, current.TokenVersion)

	audit.OperationType = "iam.user.two_factor.reset"
	require.NoError(t, svc.ForceDisable(ctx, user.ID, audit))
	enabled, err = svc.Enabled(ctx, user.ID)
	require.NoError(t, err)
	require.False(t, enabled)
	current, err = repo.FindByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.TokenVersion+1, current.TokenVersion)
	var logs int64
	require.NoError(t, db.Table("operation_logs").Where("operation_type = ? AND resource_id = ?", audit.OperationType, audit.ResourceID).Count(&logs).Error)
	require.EqualValues(t, 1, logs)
}
