package infra

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestDaumMigrationUpgradeReentryPreservesCredentialsAndLegacyFacts(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Join(filepath.Dir(file), "../../../migrations")
	baseline := testmysql.MigrationsThrough(t, source, 149)
	beforeCorrection := testmysql.MigrationsThrough(t, source, 150)
	target := testmysql.MigrationsThrough(t, source, 152)
	server := testmysql.New("remail_daum_migration")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	db := server.Database(t, baseline)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO users(id,email,password_hash,nickname,status,role) VALUES (1,'daum-admin@example.test','hash','admin','active','super_admin'),(7,'daum-owner@example.test','hash','owner','active','supplier')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id,version) VALUES(100,'proto',1,9),(101,'microsoft',1,7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO proto_resources(id,owner_user_id,email_address,password,status,version) VALUES(100,1,'keep@proton.me','preserved-proto','normal',9)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO microsoft_resources(id,email_address,password,refresh_token,status) VALUES(101,'keep@outlook.com','preserved-ms','preserved-rt','normal')`).Error)
	require.NoError(t, platform.RunMigrations(sqlDB, beforeCorrection))
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id,type,owner_user_id,version) VALUES(102,'daum',7,4)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO daum_resources(id,owner_user_id,email_address,app_password,status,last_safe_error) VALUES(102,7,'legacy@daum.net','legacy-fixture-app','abnormal','invalid_credentials: old free text is not an authoritative failure category')`).Error)
	require.NoError(t, platform.RunMigrations(sqlDB, target))
	var legacy Resource
	require.NoError(t, db.First(&legacy, 102).Error)
	require.Equal(t, domain.StatusAbnormal, legacy.Status)
	require.Empty(t, legacy.FailureCategory, "migration must not promote old abnormal records or diagnostic strings to permanent failure evidence")
	s := NewService(db)
	id, outcome, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: "account@hanmail2.net", AppPassword: "private-app", Password: "private-login", KakaoEmail: "account@kakao.com", BindingAddress: "aux@example.test", PickupURL: "https://remail.test/pickup?email=aux%40example.test&token=private-token"})
	require.NoError(t, err)
	require.Equal(t, "imported", outcome)
	require.NoError(t, db.Model(&Resource{}).Where("id=?", id).Updates(map[string]any{"status": domain.StatusAbnormal, "failure_category": "invalid_credentials"}).Error)
	// Simulate committed DDL with a missing migration marker.
	require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id IN (150,151,152)").Error)
	require.NoError(t, platform.RunMigrations(sqlDB, target))
	var row Resource
	require.NoError(t, db.First(&row, id).Error)
	require.Equal(t, "private-app", row.AppPassword)
	require.Equal(t, "private-login", row.Password)
	require.Equal(t, "account@kakao.com", row.KakaoEmail)
	require.Equal(t, "invalid_credentials", row.FailureCategory)
	require.NoError(t, db.First(&legacy, 102).Error)
	require.Empty(t, legacy.FailureCategory, "reentry must also preserve the absence of authoritative evidence")
	aliases, err := s.ListAliases(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, aliases, 2)
	var old struct{ Password string }
	require.NoError(t, db.Table("proto_resources").Select("password").Where("id = 100").Take(&old).Error)
	require.Equal(t, "preserved-proto", old.Password)
	require.NoError(t, db.Table("microsoft_resources").Select("password").Where("id = 101").Take(&old).Error)
	require.Equal(t, "preserved-ms", old.Password)

	t.Run("identity correction preserves alias history and permits reuse", func(t *testing.T) {
		ctx := context.Background()
		corrected, fixtureApp := "corrected@daum.net", "corrected-fixture"
		_, err := s.ExecuteCommand(ctx, Command{ResourceID: id, Version: row.Version, Action: "edit", Email: &corrected, AppPassword: &fixtureApp, OperatorUserID: 1, IdempotencyKey: "correct-no-history"})
		require.NoError(t, err)
		current, err := s.ListAliases(ctx, id)
		require.NoError(t, err)
		require.Len(t, current, 2)
		require.Equal(t, aliases[0].ID, current[0].ID, "an unused typo does not create a permanent historical alias")
		require.Equal(t, "corrected@hanmail.net", current[0].EmailAddress)
		_, _, err = s.ImportLine(ctx, 7, domain.ImportLine{Email: "account@daum.net", AppPassword: "original-reused"})
		require.NoError(t, err)
		require.NoError(t, db.Exec(`INSERT INTO projects(id,name,target_platform,status) VALUES(10,'Daum alias history','fixture','listed')`).Error)
		require.NoError(t, db.Exec(`INSERT INTO project_products(id,project_id,type,main_weight) VALUES(20,10,'daum',1)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no,type,owner_user_id) VALUES('daum-alias-history','daum',7)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO daum_allocations(order_no,project_id,product_id,resource_id,owner_user_id,mailbox,explicit_alias_id,email,status,released_at) VALUES('daum-alias-history',10,20,?,7,'alias',?,'corrected@hanmail.net','released',NOW())`, id, current[0].ID).Error)
		finalEmail := "final@daum.net"
		stored, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Version: stored.Version, Action: "edit", Email: &finalEmail, OperatorUserID: 1, IdempotencyKey: "correct-with-history"})
		require.NoError(t, err)
		var historical ExplicitAlias
		require.NoError(t, db.First(&historical, current[0].ID).Error)
		require.Equal(t, "corrected@hanmail.net", historical.EmailAddress)
		require.Equal(t, "disabled", historical.Status)
		var allocation struct {
			Email           string
			ExplicitAliasID uint
		}
		require.NoError(t, db.Table("daum_allocations").Where("order_no = 'daum-alias-history'").Take(&allocation).Error)
		require.Equal(t, historical.ID, allocation.ExplicitAliasID)
		require.Equal(t, historical.EmailAddress, allocation.Email)
		current, err = s.ListAliases(ctx, id)
		require.NoError(t, err)
		require.Len(t, current, 2)
		require.Equal(t, "final@hanmail.net", current[0].EmailAddress)
		_, _, err = s.ImportLine(ctx, 7, domain.ImportLine{Email: corrected, AppPassword: "reused-again"})
		require.NoError(t, err, "historical aliases must not reserve the old active identity")
		stored, err = s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Version: stored.Version, Action: "edit", Email: &corrected, OperatorUserID: 1, IdempotencyKey: "reject-current-conflict"})
		require.ErrorIs(t, err, domain.ErrResourceConflict)
		current, err = s.ListAliases(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "final@hanmail.net", current[0].EmailAddress, "conflicting correction rolls back disabled aliases too")
		require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
		restored, outcome, err := s.ImportLine(ctx, 1, domain.ImportLine{Email: finalEmail, AppPassword: "owner-transfer"})
		require.NoError(t, err)
		require.Equal(t, id, restored)
		require.Equal(t, "restored", outcome)
		stored, err = s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.EqualValues(t, 1, stored.OwnerUserID)
		require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 152").Error)
		require.NoError(t, platform.RunMigrations(sqlDB, target))
		require.NoError(t, db.First(&historical, historical.ID).Error)
		require.Equal(t, "disabled", historical.Status)
	})
	require.Error(t, goose.DownTo(sqlDB, target, 149))
	require.True(t, db.Migrator().HasTable(&Resource{}))
	require.NoError(t, db.First(&row, id).Error)
}
