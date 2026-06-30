package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8.0",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "remail_test",
			"MYSQL_USER":          "remail",
			"MYSQL_PASSWORD":      "remail",
		},
		WaitingFor: wait.ForListeningPort("3306/tcp").WithStartupTimeout(2 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)

	dsn := fmt.Sprintf("remail:remail@tcp(%s:%s)/remail_test?charset=utf8mb4&parseTime=True&loc=Local", host, port.Port())
	var db *gorm.DB
	var sqlDB *sql.DB
	var lastErr error
	require.Eventually(t, func() bool {
		if sqlDB != nil {
			_ = sqlDB.Close()
		}

		db, lastErr = gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
		if lastErr != nil {
			return false
		}

		sqlDB, lastErr = db.DB()
		if lastErr != nil {
			return false
		}
		lastErr = sqlDB.PingContext(ctx)
		return lastErr == nil
	}, 30*time.Second, 500*time.Millisecond, "mysql did not become ready: %v", lastErr)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, platform.RunMigrations(sqlDB, migrationsDir(t)))
	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestUserRepoCreateFirstUserConcurrentMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- repo.CreateFirstUser(context.Background(), &domain.User{
				Email:        fmt.Sprintf("admin-%d@test.local", i),
				PasswordHash: "hash",
				Enabled:      true,
				RoleLevel:    domain.RoleSuperAdmin,
			})
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrActivationAlreadyDone):
			conflicts++
		default:
			t.Fatalf("unexpected activation error: %v", err)
		}
	}

	require.Equal(t, 1, successes)
	require.Equal(t, workers-1, conflicts)

	var count int64
	require.NoError(t, db.Table("users").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestUserRepoRoleLevelCheckMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	err := db.Exec(
		"INSERT INTO users(email, password_hash, role_level) VALUES (?, ?, ?)",
		"bad-role@test.local",
		"hash",
		30,
	).Error

	require.Error(t, err)
}

func TestUserRepoUpdateWithOperationLogMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	user := &domain.User{
		Email:        "user@test.local",
		PasswordHash: "hash",
		Enabled:      true,
		RoleLevel:    domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), user))

	user.Enabled = false
	user.TokenVersion++
	require.NoError(t, repo.UpdateWithOperationLog(context.Background(), user, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "iam.user.update",
		ResourceType:   "user",
		ResourceID:     fmt.Sprintf("%d", user.ID),
		Path:           fmt.Sprintf("/v1/admin/users/%d", user.ID),
		Result:         "success",
		SafeSummary:    "User access settings updated.",
		RequestID:      "req-user-update",
	}))

	var model UserModel
	require.NoError(t, db.First(&model, user.ID).Error)
	require.False(t, model.Enabled)
	require.Equal(t, 1, model.TokenVersion)

	var logCount int64
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_type = ? AND resource_id = ? AND request_id = ?", "iam.user.update", "user", fmt.Sprintf("%d", user.ID), "req-user-update").
		Count(&logCount).Error)
	require.Equal(t, int64(1), logCount)
}

func TestPermissionServiceBaselineMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)

	allowed, err := permissions.Check(context.Background(), 1, domain.RoleAdmin, "iam:user", "read")
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = permissions.Check(context.Background(), 1, domain.RoleSuperAdmin, "iam:user", "operate")
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = permissions.Check(context.Background(), 1, domain.RoleUser, "iam:user", "read")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestPermissionServiceUserDenyOverridesRoleMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	require.NoError(t, db.Exec(
		"INSERT INTO casbin_rule(ptype, v0, v1, v2, v3) VALUES (?, ?, ?, ?, ?)",
		"p",
		"user:99",
		"iam:user",
		"read",
		"deny",
	).Error)

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)

	allowed, err := permissions.Check(context.Background(), 99, domain.RoleAdmin, "iam:user", "read")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestPermissionServiceUserAllowExtendsRoleMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	require.NoError(t, db.Exec(
		"INSERT INTO casbin_rule(ptype, v0, v1, v2, v3) VALUES (?, ?, ?, ?, ?)",
		"p",
		"user:99",
		"iam:permission",
		"read",
		"allow",
	).Error)

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)

	allowed, err := permissions.Check(context.Background(), 99, domain.RoleUser, "iam:permission", "read")
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestUserRepoCreateWithInviteConcurrentMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO invites(code, enabled, max_use) VALUES (?, ?, ?)",
		"INVITE-1",
		true,
		1,
	).Error)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- repo.CreateWithInvite(context.Background(), &domain.User{
				Email:        fmt.Sprintf("invite-user-%d@test.local", i),
				PasswordHash: "hash",
				Enabled:      true,
				RoleLevel:    domain.RoleUser,
			}, "INVITE-1")
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	rejected := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrInviteInvalid):
			rejected++
		default:
			t.Fatalf("unexpected invite consume error: %v", err)
		}
	}

	require.Equal(t, 1, successes)
	require.Equal(t, workers-1, rejected)

	var invite InviteModel
	require.NoError(t, db.Where("code = ?", "INVITE-1").First(&invite).Error)
	require.Equal(t, 1, invite.Used)

	var inviteUseCount int64
	require.NoError(t, db.Table("invite_uses").Where("invite_code = ?", "INVITE-1").Count(&inviteUseCount).Error)
	require.Equal(t, int64(1), inviteUseCount)
}

func TestPermissionServiceReplacePoliciesReloadsMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)

	err = permissions.ReplaceUserPermissionPolicies(context.Background(), 99, []domain.PermissionPolicy{
		{Resource: "iam:user", Action: "read", Effect: "deny"},
	})
	require.NoError(t, err)

	stored, err := permissions.ListUserPermissionPolicies(context.Background(), 99)
	require.NoError(t, err)
	require.Equal(t, []domain.PermissionPolicy{{Resource: "iam:user", Action: "read", Effect: "deny"}}, stored)

	require.NoError(t, permissions.Reload(context.Background()))
	allowed, err := permissions.Check(context.Background(), 99, domain.RoleAdmin, "iam:user", "read")
	require.NoError(t, err)
	require.False(t, allowed)
}
