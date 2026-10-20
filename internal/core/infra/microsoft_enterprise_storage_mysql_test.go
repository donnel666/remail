package infra

import (
	"context"
	"log"
	"strings"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestMicrosoftEnterpriseViewsUseOwnTablesAndOwnerScopeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec("INSERT INTO users(id,email,password_hash,role,status) VALUES(1,'owner@test.local','hash','supplier','active'),(2,'other@test.local','hash','supplier','active')").Error)
	repo := NewResourceRepo(db)
	for _, item := range []struct {
		owner uint
		email string
	}{
		{1, "own@tenant.onmicrosoft.com"}, {2, "other@private.onmicrosoft.com"}, {1, "personal@outlook.com"},
	} {
		root := &domain.EmailResource{OwnerUserID: item.owner}
		require.NoError(t, repo.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{EmailAddress: item.email, Status: domain.MicrosoftStatusNormal, Password: "test-secret", RefreshToken: "test-refresh", ClientID: "test-client"}))
	}
	for _, order := range [][]bool{{false, true, false}, {true, false, true}} {
		var sqlLog strings.Builder
		logged := db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&sqlLog, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})
		owned, admin := NewResourceRepo(logged), NewAdminResourceRepo(logged)
		for _, enterprise := range order {
			sqlLog.Reset()
			filter := coreapp.ResourceListFilter{ResourceType: domain.MicrosoftResourceType(enterprise)}
			rows, err := owned.List(ctx, 1, filter, 0, 20, 0)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, filter.ResourceType, rows[0].Type)
			count, err := owned.Count(ctx, 1, filter)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			facets, err := owned.Facets(ctx, 1, filter)
			require.NoError(t, err)
			require.EqualValues(t, 1, facets.Matched)
			wantSuffix, wantAdmin := "outlook.com", 1
			if enterprise {
				wantSuffix, wantAdmin = "tenant.onmicrosoft.com", 2
			}
			require.Equal(t, []coreapp.ResourceKeyFacet{{Key: wantSuffix, Count: 1}}, facets.Suffixes)
			adminFilter := coreapp.AdminMicrosoftListFilter{Enterprise: enterprise}
			adminRows, err := admin.ListAdminMicrosoft(ctx, adminFilter, 0, 20, 0, time.Now())
			require.NoError(t, err)
			require.Len(t, adminRows, wantAdmin)
			adminCount, err := admin.CountAdminMicrosoft(ctx, adminFilter, time.Now())
			require.NoError(t, err)
			require.EqualValues(t, wantAdmin, adminCount)
			adminFacets, err := admin.AdminMicrosoftFacets(ctx, adminFilter, time.Now())
			require.NoError(t, err)
			require.EqualValues(t, wantAdmin, adminFacets.Matched)
			if enterprise {
				require.Contains(t, sqlLog.String(), "microsoft_enterprise_resources")
				require.NotContains(t, sqlLog.String(), "microsoft_resources", "enterprise views must never scan the personal inventory")
				require.NotContains(t, sqlLog.String(), "explicit_aliases")
			}
		}
	}
	other, err := repo.FindMicrosoftByEmail(ctx, "other@private.onmicrosoft.com")
	require.NoError(t, err)
	_, err = repo.PublishMicrosoftWithLog(ctx, 1, other.ID, governancedomain.OperationLog{})
	require.Error(t, err)
	require.Error(t, repo.DeletePrivateMicrosoftWithLog(ctx, 1, other.ID, governancedomain.OperationLog{}))
	stored, err := repo.FindMicrosoftByID(ctx, other.ID)
	require.NoError(t, err)
	require.Equal(t, domain.MicrosoftStatusNormal, stored.Status)
	require.False(t, stored.ForSale)

	// Both automatic expiry scans read only their own resource table.
	for _, table := range []string{"microsoft_resources", "microsoft_enterprise_resources"} {
		require.NoError(t, db.Table(table).Where("status='normal'").Update("rt_expire_at", time.Now().Add(time.Hour)).Error)
	}
	for _, enterprise := range []bool{true, false} {
		candidates, err := repo.ListMicrosoftAutoRefreshCandidates(ctx, time.Now().Add(24*time.Hour), 20, enterprise)
		require.NoError(t, err)
		want := 1
		if enterprise {
			want = 2
		}
		require.Len(t, candidates, want)
		for _, candidate := range candidates {
			root, err := repo.FindByID(ctx, candidate.ResourceID)
			require.NoError(t, err)
			require.Equal(t, domain.MicrosoftResourceType(enterprise), root.Type)
		}
	}
	imports := NewResourceImportRepo(db)
	for _, enterprise := range []bool{false, true} {
		record := &domain.ResourceImport{OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft, Enterprise: enterprise, SourceObjectKey: "synthetic-import", Status: domain.ResourceImportProcessing}
		require.NoError(t, imports.Create(ctx, record))
	}
	for _, enterprise := range []bool{true, false} {
		items, err := imports.ClaimAdminImportDispatchable(ctx, 1, time.Time{}, time.Time{}, enterprise)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, enterprise, items[0].Enterprise)
	}
}

func TestMicrosoftEnterpriseStorageMigrationMySQL(t *testing.T) {
	server := testmysql.New("remail_enterprise_migration")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	legacy := testmysql.MigrationsThrough(t, coreMigrationsDir(t), 153)
	for _, dependency := range []bool{false, true} {
		name := "preserves_data_and_replays"
		if dependency {
			name = "dependency_rolls_back"
		}
		t.Run(name, func(t *testing.T) {
			db := server.Database(t, legacy)
			require.NoError(t, db.Exec("INSERT INTO users(id,email,password_hash,role,status) VALUES(1,'migration@test.local','hash','supplier','active')").Error)
			require.NoError(t, db.Exec("INSERT INTO projects(id,name,target_platform,status) VALUES(10,'History','test','listed')").Error)
			require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(101,'microsoft',1),(102,'microsoft',1)").Error)
			require.NoError(t, db.Exec(`INSERT INTO microsoft_resources(id,email_address,email_domain,password,client_id,refresh_token,status,credential_revision,validation_generation,token_refresh_status,token_refresh_generation,token_refresh_expected_credential_revision)
VALUES(101,'move@tenant.onmicrosoft.com','tenant.onmicrosoft.com','password','client',?,'validating',7,3,'processing',4,7),
(102,'keep@outlook.com','outlook.com','personal-password','personal-client','personal-rt','normal',1,1,'normal',0,0)`, strings.Repeat("r", 1800)).Error)
			require.NoError(t, db.Exec("INSERT INTO microsoft_resource_project_matches(resource_id,project_id,first_matched_at,last_matched_at,last_scanned_at) VALUES(101,10,NOW(),NOW(),NOW())").Error)
			require.NoError(t, db.Exec("INSERT INTO mailmatch_resource_fetch_states(email_resource_id,operation_kind,status,generation) VALUES(101,'resource_history','processing',3)").Error)
			require.NoError(t, db.Exec("INSERT INTO mailmatch_admin_resource_fetch_states(email_resource_id,status,generation) VALUES(101,'processing',2)").Error)
			if dependency {
				require.NoError(t, db.Exec("INSERT INTO explicit_aliases(resource_id,owner_user_id,email,status) VALUES(101,1,'legacy@outlook.com','normal')").Error)
			}
			var before MicrosoftResourceModel
			require.NoError(t, db.First(&before, 102).Error)
			var tableName, schemaBefore string
			require.NoError(t, db.Raw("SHOW CREATE TABLE microsoft_resources").Row().Scan(&tableName, &schemaBefore))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, goose.SetDialect("mysql"))
			err = goose.UpTo(sqlDB, coreMigrationsDir(t), 154)
			if dependency {
				require.ErrorContains(t, err, "refusing an unsafe automatic move")
				var root EmailResourceModel
				require.NoError(t, db.First(&root, 101).Error)
				require.Equal(t, "microsoft", root.Type)
				require.EqualValues(t, 1, root.Version)
				var old MicrosoftResourceModel
				require.NoError(t, db.First(&old, 101).Error)
				require.Equal(t, "validating", old.Status)
				var count int64
				require.NoError(t, db.Table("microsoft_enterprise_resources").Count(&count).Error)
				require.Zero(t, count)
			} else {
				require.NoError(t, err)
				var root EmailResourceModel
				require.NoError(t, db.First(&root, 101).Error)
				require.Equal(t, "microsoft_enterprise", root.Type)
				require.EqualValues(t, 1, root.OwnerUserID)
				require.EqualValues(t, 2, root.Version)
				var moved MicrosoftResourceModel
				require.NoError(t, db.Table("microsoft_enterprise_resources").First(&moved, 101).Error)
				require.Equal(t, "password", moved.Password)
				require.Equal(t, strings.Repeat("r", 1800), moved.RefreshToken)
				require.EqualValues(t, 7, moved.CredentialRevision)
				require.Equal(t, "pending", moved.Status)
				require.EqualValues(t, 4, moved.ValidationGeneration)
				var token struct {
					Status     string
					Generation uint64
				}
				require.NoError(t, db.Table("microsoft_enterprise_resources").Select("token_refresh_status AS status,token_refresh_generation AS generation").Where("id=101").Scan(&token).Error)
				require.Equal(t, "pending", token.Status)
				require.EqualValues(t, 5, token.Generation)
				var history int64
				require.NoError(t, db.Table("microsoft_enterprise_project_matches").Where("resource_id=101 AND project_id=10").Count(&history).Error)
				require.EqualValues(t, 1, history)
				for table, operation := range map[string]string{"mailmatch_resource_fetch_states": "enterprise_history", "mailmatch_admin_resource_fetch_states": "enterprise_fetch"} {
					var state struct{ Status, OperationKind string }
					require.NoError(t, db.Table(table).Where("email_resource_id=101").Scan(&state).Error)
					require.Equal(t, "pending", state.Status)
					require.Equal(t, operation, state.OperationKind)
				}
				require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id=154").Error)
				require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 154))
				var replay MicrosoftResourceModel
				require.NoError(t, db.Table("microsoft_enterprise_resources").First(&replay, 101).Error)
				require.Equal(t, moved, replay)
			}
			var after MicrosoftResourceModel
			require.NoError(t, db.First(&after, 102).Error)
			require.Equal(t, before, after, "personal resource data must remain unchanged")
			var schemaAfter string
			require.NoError(t, db.Raw("SHOW CREATE TABLE microsoft_resources").Row().Scan(&tableName, &schemaAfter))
			require.Equal(t, schemaBefore, schemaAfter, "personal table must not be altered")
		})
	}
}
