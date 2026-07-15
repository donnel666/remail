package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var iamMySQLTestServer = testmysql.New("remail_iam_test")

type iamExplainRow struct {
	Type sql.NullString `gorm:"column:type"`
	Key  sql.NullString `gorm:"column:key"`
}

func TestMain(m *testing.M) {
	code := m.Run()
	_ = iamMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	return iamMySQLTestServer.Database(t, migrationsDir(t))
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
				Role:         domain.RoleSuperAdmin,
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

func TestUserRepoRoleCheckMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	err := db.Exec(
		"INSERT INTO users(email, password_hash, role) VALUES (?, ?, ?)",
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
		Role:         domain.RoleUser,
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

func TestUserRepoUpdateNonSuperAdminAccessWithOperationLogProtectsCurrentRoleMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	user := &domain.User{
		Email:        "stale-admin-update@test.local",
		PasswordHash: "hash",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), user))
	enabled := false

	require.NoError(t, db.Model(&UserModel{}).
		Where("id = ?", user.ID).
		Update("role", domain.RoleSuperAdmin.String()).Error)

	_, err := repo.UpdateNonSuperAdminAccessWithOperationLog(context.Background(), user.ID, &enabled, nil, nil, false, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "iam.user.update",
		ResourceType:   "user",
		ResourceID:     fmt.Sprintf("%d", user.ID),
		Path:           fmt.Sprintf("/v1/admin/users/%d", user.ID),
		Result:         "success",
		SafeSummary:    "User access settings updated.",
		RequestID:      "req-stale-user-update",
	})
	require.ErrorIs(t, err, domain.ErrPermissionDenied)

	var stored UserModel
	require.NoError(t, db.First(&stored, user.ID).Error)
	require.Equal(t, domain.RoleSuperAdmin.String(), stored.Role)
	require.True(t, stored.Enabled)

	var logCount int64
	require.NoError(t, db.Table("operation_logs").
		Where("request_id = ?", "req-stale-user-update").
		Count(&logCount).Error)
	require.Zero(t, logCount)
}

func TestUserRepoUpdateNonSuperAdminAccessPreservesUnrelatedConcurrentFieldsMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	user := &domain.User{
		Email:        "partial-admin-update@test.local",
		PasswordHash: "hash",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), user))
	require.NoError(t, db.Model(&UserModel{}).
		Where("id = ?", user.ID).
		Update("role", domain.RoleAdmin.String()).Error)

	enabled := false
	updated, err := repo.UpdateNonSuperAdminAccessWithOperationLog(context.Background(), user.ID, &enabled, nil, nil, false, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "iam.user.update",
		ResourceType:   "user",
		ResourceID:     fmt.Sprintf("%d", user.ID),
		Path:           fmt.Sprintf("/v1/admin/users/%d", user.ID),
		Result:         "success",
		SafeSummary:    "User access settings updated.",
		RequestID:      "req-partial-user-update",
	})
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	require.Equal(t, domain.RoleAdmin, updated.Role)

	var stored UserModel
	require.NoError(t, db.First(&stored, user.ID).Error)
	require.False(t, stored.Enabled)
	require.Equal(t, domain.RoleAdmin.String(), stored.Role)

	updated, err = repo.UpdateNonSuperAdminAccessWithOperationLog(context.Background(), user.ID, nil, nil, nil, true, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "iam.user.sessions.revoke",
		ResourceType:   "user",
		ResourceID:     fmt.Sprintf("%d", user.ID),
		Path:           fmt.Sprintf("/v1/admin/users/%d/sessions/revoke", user.ID),
		Result:         "success",
		SafeSummary:    "User sessions revoked.",
		RequestID:      "req-partial-token-bump",
	})
	require.NoError(t, err)
	require.Equal(t, domain.RoleAdmin, updated.Role)
	require.False(t, updated.Enabled)
	require.Equal(t, 1, updated.TokenVersion)
}

func TestUserRepoListByFilterSearchUsesFullTextIndexMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	alpha := &domain.User{
		Email:        "alpha-search@example.com",
		PasswordHash: "hash",
		Nickname:     "Project Alpha",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), alpha))
	beta := &domain.User{
		Email:        "beta-search@example.com",
		PasswordHash: "hash",
		Nickname:     "Beta User",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), beta))

	users, err := repo.ListByFilter(context.Background(), domain.UserListFilter{Search: "alpha-search@example.com"}, 0, 20)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, alpha.Email, users[0].Email)

	users, err = repo.ListByFilter(context.Background(), domain.UserListFilter{Search: "Project"}, 0, 20)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, alpha.Email, users[0].Email)

	users, err = repo.ListByFilter(context.Background(), domain.UserListFilter{Search: fmt.Sprintf("%d", beta.ID)}, 0, 20)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, beta.Email, users[0].Email)

	var plan []iamExplainRow
	require.NoError(t, db.Raw(
		"EXPLAIN SELECT id FROM users WHERE MATCH(email, nickname) AGAINST (? IN BOOLEAN MODE) LIMIT 20",
		userSearchBooleanQuery("alpha-search@example.com"),
	).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.Equal(t, "fulltext", plan[0].Type.String)
	require.Equal(t, "idx_users_search", plan[0].Key.String)
}

func TestPermissionServiceBaselineMySQL(t *testing.T) {
	db := newMySQLTestDB(t)

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)

	checks := []struct {
		role     domain.Role
		resource string
		action   string
		allowed  bool
	}{
		{role: domain.RoleAdmin, resource: "iam:user", action: "read", allowed: true},
		{role: domain.RoleAdmin, resource: "billing:wallet", action: "read", allowed: true},
		{role: domain.RoleAdmin, resource: "billing:wallet", action: "operate", allowed: true},
		{role: domain.RoleAdmin, resource: "billing:card", action: "read", allowed: true},
		{role: domain.RoleAdmin, resource: "trade:order", action: "read", allowed: true},
		{role: domain.RoleAdmin, resource: "trade:order", action: "write", allowed: true},
		{role: domain.RoleAdmin, resource: "iam:permission", action: "sensitive", allowed: false},
		{role: domain.RoleSuperAdmin, resource: "iam:user", action: "operate", allowed: true},
		{role: domain.RoleSuperAdmin, resource: "iam:permission", action: "sensitive", allowed: true},
		{role: domain.RoleSuperAdmin, resource: "billing:wallet", action: "sensitive", allowed: true},
		{role: domain.RoleSuperAdmin, resource: "billing:card", action: "sensitive", allowed: true},
		{role: domain.RoleUser, resource: "iam:user", action: "read", allowed: false},
	}
	for _, check := range checks {
		allowed, checkErr := permissions.Check(context.Background(), 1, check.role, check.resource, check.action)
		require.NoError(t, checkErr)
		require.Equal(t, check.allowed, allowed, "%s %s:%s", check.role, check.resource, check.action)
	}
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
				Role:         domain.RoleUser,
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

func TestUserRepoReferralInviteConstraintsMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)

	owner := &domain.User{
		Email:        "referral-owner@test.local",
		PasswordHash: "hash",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), owner))

	err := db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFF-NO-OWNER",
		domain.InviteKindReferral,
		true,
		100,
		0,
		owner.ID,
		nil,
	).Error
	require.Error(t, err)

	err = db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFF-MISSING-OWNER",
		domain.InviteKindReferral,
		true,
		100,
		0,
		owner.ID,
		99999999,
	).Error
	require.Error(t, err)

	err = db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"ADMIN-WITH-OWNER",
		domain.InviteKindAdmin,
		true,
		100,
		0,
		owner.ID,
		owner.ID,
	).Error
	require.Error(t, err)

	require.NoError(t, db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFF-OWNER-1",
		domain.InviteKindReferral,
		true,
		100,
		0,
		owner.ID,
		owner.ID,
	).Error)

	err = db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFF-OWNER-2",
		domain.InviteKindReferral,
		true,
		100,
		0,
		owner.ID,
		owner.ID,
	).Error
	require.Error(t, err)
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

func TestPermissionServiceGuardedReplaceProtectsSensitiveAndSuperAdminMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)
	user := &domain.User{
		Email:        "guarded-permissions@test.local",
		PasswordHash: "hash",
		Enabled:      true,
		Role:         domain.RoleUser,
	}
	require.NoError(t, repo.Create(context.Background(), user))

	permissions, err := NewPermissionService(db)
	require.NoError(t, err)
	sensitive := []domain.PermissionPolicy{{
		Resource: "iam:permission",
		Action:   "sensitive",
		Effect:   "allow",
	}}
	require.NoError(t, permissions.ReplaceUserPermissionPolicies(context.Background(), user.ID, sensitive))

	_, err = permissions.ReplaceUserPermissionPoliciesGuarded(context.Background(), user.ID, nil, false)
	require.ErrorIs(t, err, domain.ErrPermissionDenied)
	stored, err := permissions.ListUserPermissionPolicies(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, sensitive, stored)

	previous, err := permissions.ReplaceUserPermissionPoliciesGuarded(context.Background(), user.ID, nil, true)
	require.NoError(t, err)
	require.Equal(t, sensitive, previous)

	require.NoError(t, db.Model(&UserModel{}).
		Where("id = ?", user.ID).
		Update("role", domain.RoleSuperAdmin.String()).Error)
	_, err = permissions.ReplaceUserPermissionPoliciesGuarded(context.Background(), user.ID, sensitive, true)
	require.ErrorIs(t, err, domain.ErrPermissionDenied)
}
