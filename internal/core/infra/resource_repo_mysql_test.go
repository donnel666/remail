package infra

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newCoreMySQLTestDB(t *testing.T) *gorm.DB {
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

	require.NoError(t, platform.RunMigrations(sqlDB, coreMigrationsDir(t)))
	return db
}

func coreMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestResourceSchemaConstraintsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
		2,
		"other@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, status) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		200,
		1,
		"mail-owner.test.local",
		"online",
		201,
		2,
		"mail-other.test.local",
		"online",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?)",
		100,
		"domain",
		1,
	).Error)
	require.Error(t, db.Exec(
		"INSERT INTO microsoft_resources(id, email_address, password, status) VALUES (?, ?, ?, ?)",
		100,
		"wrong-child@test.local",
		"secret",
		"pending",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?)",
		101,
		"microsoft",
		1,
	).Error)
	require.Error(t, db.Exec(
		"INSERT INTO domain_resources(id, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?)",
		101,
		1,
		"wrong-child.example.com",
		200,
		"sale",
		"abnormal",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?)",
		102,
		"domain",
		1,
	).Error)
	require.Error(t, db.Exec(
		"INSERT INTO domain_resources(id, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?)",
		102,
		1,
		"cross-owner.example.com",
		201,
		"sale",
		"abnormal",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?)",
		103,
		"domain",
		1,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?)",
		103,
		1,
		"valid.example.com",
		200,
		"sale",
		"normal",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status) VALUES (?, ?, ?, ?)",
		103,
		1,
		"user@valid.example.com",
		"normal",
	).Error)
	require.Error(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status) VALUES (?, ?, ?, ?)",
		103,
		1,
		"user@valid.example.com",
		"normal",
	).Error)
}

func TestResourceListIndexesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)

	requireIndexExists(t, db, "resource_imports", "idx_resource_imports_owner_created")
	requireIndexExists(t, db, "email_resources", "idx_email_resources_owner_created")
	requireIndexExists(t, db, "email_resources", "idx_email_resources_owner_type_created")
	requireIndexExists(t, db, "email_resources", "idx_email_resources_type_created")
	requireIndexExists(t, db, "email_resources", "idx_email_resources_created")
	requireIndexExists(t, db, "mail_servers", "idx_mail_servers_owner_created")
	requireIndexExists(t, db, "mail_servers", "idx_mail_servers_created")
	requireIndexExists(t, db, "mail_servers", "idx_mail_servers_owner_address_mx")
	requireIndexExists(t, db, "domain_resources", "idx_domain_resources_owner_created")
	requireIndexExists(t, db, "domain_resources", "idx_domain_resources_owner_tld_private")
	requireIndexExists(t, db, "microsoft_resources", "idx_microsoft_bulk_domain")
	requireIndexExists(t, db, "generated_mailboxes", "idx_generated_mailboxes_resource_created")
}

func TestMailServerRepoGetOrCreateDefaultInboundConcurrentMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewMailServerRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		10,
	).Error)

	const workers = 8
	start := make(chan struct{})
	ids := make(chan uint, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			server, err := repo.GetOrCreateDefaultInbound(
				context.Background(),
				1,
				"Remail Inbound",
				"mx.aishop6.com",
				"mx.aishop6.com",
			)
			if err != nil {
				errs <- err
				return
			}
			ids <- server.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	seenIDs := make(map[uint]struct{})
	for id := range ids {
		seenIDs[id] = struct{}{}
	}
	require.Len(t, seenIDs, 1)

	var count int64
	require.NoError(t, db.Model(&MailServerModel{}).
		Where("owner_user_id = ? AND server_address = ? AND mx_record = ?", 1, "mx.aishop6.com", "mx.aishop6.com").
		Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCreateMicrosoftResourcesAndMarkImportSucceededRollsBackOnDuplicateMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	importRepo := NewResourceImportRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	importRecord := &domain.ResourceImport{
		OwnerUserID:     1,
		ResourceType:    domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/duplicate.txt",
		Status:          domain.ResourceImportProcessing,
	}
	require.NoError(t, importRepo.Create(context.Background(), importRecord))

	resources := []domain.EmailResource{
		{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1},
		{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1},
	}
	ms := []domain.MicrosoftResource{
		{EmailAddress: "dup@test.local", Password: "secret", ForSale: true, Status: domain.MicrosoftStatusPending},
		{EmailAddress: "dup@test.local", Password: "secret", ForSale: true, Status: domain.MicrosoftStatusPending},
	}

	require.ErrorIs(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms, "", ""), domain.ErrDuplicateEmail)

	storedImport, err := importRepo.FindByID(context.Background(), importRecord.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceImportProcessing, storedImport.Status)
	require.Zero(t, storedImport.ImportedCount)

	var rootCount int64
	require.NoError(t, db.Model(&EmailResourceModel{}).Count(&rootCount).Error)
	require.Zero(t, rootCount)

	var childCount int64
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).Count(&childCount).Error)
	require.Zero(t, childCount)
}

func TestCreateMicrosoftResourcesAndMarkImportSucceededRestoresDeletedMicrosoftMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	importRepo := NewResourceImportRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
		2,
		"new-owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "restore-deleted@test.local",
		Password:     "old-secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	require.NoError(t, repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, ms.ID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d", ms.ID),
		Result:         "success",
		SafeSummary:    "Private Microsoft resource deleted.",
		RequestID:      "req-restore-delete",
	}))

	existing, err := repo.FindExistingMicrosoftEmails(context.Background(), []string{ms.EmailAddress})
	require.NoError(t, err)
	require.Empty(t, existing)

	importRecord := &domain.ResourceImport{
		OwnerUserID:     2,
		ResourceType:    domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/restore-deleted.txt",
		Status:          domain.ResourceImportProcessing,
	}
	require.NoError(t, importRepo.Create(context.Background(), importRecord))

	resources := []domain.EmailResource{{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 2}}
	msResources := []domain.MicrosoftResource{{
		EmailAddress:  ms.EmailAddress,
		Password:      "new-secret",
		ClientID:      "new-client",
		RefreshToken:  "new-refresh",
		LongLived:     true,
		ForSale:       true,
		Status:        domain.MicrosoftStatusPending,
		QualityScore:  7,
		LastSafeError: "",
	}}

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, msResources, "", ""))

	var rootCount int64
	require.NoError(t, db.Model(&EmailResourceModel{}).Count(&rootCount).Error)
	require.EqualValues(t, 1, rootCount)

	var childCount int64
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).Count(&childCount).Error)
	require.EqualValues(t, 1, childCount)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, ms.ID).Error)
	require.Equal(t, "new-secret", stored.Password)
	require.Equal(t, "new-client", stored.ClientID)
	require.Equal(t, "new-refresh", stored.RefreshToken)
	require.True(t, stored.LongLived)
	require.False(t, stored.ForSale)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
	require.Equal(t, 7, stored.QualityScore)
	require.Empty(t, stored.LastSafeError)
	require.Nil(t, stored.LastAllocatedAt)

	var ownerID uint
	require.NoError(t, db.Raw("SELECT owner_user_id FROM email_resources WHERE id = ?", ms.ID).Scan(&ownerID).Error)
	require.EqualValues(t, 2, ownerID)

	storedImport, err := importRepo.FindByID(context.Background(), importRecord.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceImportImported, storedImport.Status)
	require.Equal(t, 1, storedImport.ImportedCount)
}

func TestCreateMicrosoftResourcesAndMarkImportSucceededRestoresDeletedMicrosoftCaseInsensitiveMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)
	importRepo := NewResourceImportRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "case-restore@test.local",
		Password:     "old-secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))
	require.NoError(t, repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, ms.ID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d", ms.ID),
		Result:         "success",
		RequestID:      "req-delete-case-restore",
	}))

	importRecord := &domain.ResourceImport{
		OwnerUserID:     1,
		ResourceType:    domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/restore-deleted-case.txt",
		Status:          domain.ResourceImportProcessing,
	}
	require.NoError(t, importRepo.Create(context.Background(), importRecord))

	resources := []domain.EmailResource{{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}}
	msResources := []domain.MicrosoftResource{{
		EmailAddress: "Case-Restore@Test.Local",
		Password:     "new-secret",
		LongLived:    true,
		ForSale:      true,
		Status:       domain.MicrosoftStatusPending,
	}}

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, msResources, "", ""))

	var rootCount int64
	require.NoError(t, db.Model(&EmailResourceModel{}).Count(&rootCount).Error)
	require.EqualValues(t, 1, rootCount)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, ms.ID).Error)
	require.Equal(t, "Case-Restore@Test.Local", stored.EmailAddress)
	require.Equal(t, "new-secret", stored.Password)
	require.True(t, stored.LongLived)
	require.False(t, stored.ForSale)
	require.Equal(t, string(domain.MicrosoftStatusPending), stored.Status)
}

func TestCreateMicrosoftResourcesAndMarkImportSucceededIsIdempotentMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	importRepo := NewResourceImportRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	importRecord := &domain.ResourceImport{
		OwnerUserID:     1,
		ResourceType:    domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/test.txt",
		Status:          domain.ResourceImportProcessing,
	}
	require.NoError(t, importRepo.Create(context.Background(), importRecord))

	resources := []domain.EmailResource{
		{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1},
		{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1},
	}
	ms := []domain.MicrosoftResource{
		{EmailAddress: "one@test.local", Password: "secret", Status: domain.MicrosoftStatusPending},
		{EmailAddress: "two@test.local", Password: "secret", Status: domain.MicrosoftStatusPending},
	}

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms, "", ""))

	storedImport, err := importRepo.FindByID(context.Background(), importRecord.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceImportImported, storedImport.Status)
	require.Equal(t, 2, storedImport.ImportedCount)

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms, "", ""))

	var rootCount int64
	require.NoError(t, db.Model(&EmailResourceModel{}).Count(&rootCount).Error)
	require.EqualValues(t, 2, rootCount)

	var childCount int64
	require.NoError(t, db.Model(&MicrosoftResourceModel{}).Count(&childCount).Error)
	require.EqualValues(t, 2, childCount)
}

func TestCoreListQueriesUseIndexesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, status) VALUES (?, ?, ?, ?)",
		200,
		1,
		"mail-owner.test.local",
		"online",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?), (?, ?, ?)",
		100,
		"microsoft",
		1,
		101,
		"domain",
		1,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO microsoft_resources(id, email_address, email_domain, password, status) VALUES (?, ?, ?, ?, ?)",
		100,
		"ms@test.local",
		"test.local",
		"secret",
		"normal",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, owner_user_id, domain, domain_tld, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?, ?)",
		101,
		1,
		"valid.example.com",
		".com",
		200,
		"not_sale",
		"normal",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status) VALUES (?, ?, ?, ?)",
		101,
		1,
		"user@valid.example.com",
		"normal",
	).Error)

	requireExplainUsesIndex(t, db,
		"idx_email_resources_owner_created",
		"EXPLAIN SELECT * FROM email_resources WHERE owner_user_id = 1 ORDER BY created_at DESC LIMIT 20",
	)
	requireExplainUsesIndex(t, db,
		"idx_email_resources_owner_type_created",
		"EXPLAIN SELECT * FROM email_resources WHERE owner_user_id = 1 AND type = 'microsoft' ORDER BY created_at DESC LIMIT 20",
	)
	requireExplainUsesIndex(t, db,
		"idx_mail_servers_owner_created",
		"EXPLAIN SELECT * FROM mail_servers WHERE owner_user_id = 1 ORDER BY created_at DESC LIMIT 20",
	)
	requireExplainUsesIndex(t, db,
		"idx_generated_mailboxes_resource_created",
		"EXPLAIN SELECT * FROM generated_mailboxes WHERE resource_id = 101 AND owner_user_id = 1 ORDER BY created_at DESC LIMIT 20",
	)
	requireExplainUsesIndex(t, db,
		"idx_microsoft_bulk_domain",
		"EXPLAIN SELECT er.id FROM microsoft_resources AS ms STRAIGHT_JOIN email_resources AS er ON er.id = ms.id WHERE er.owner_user_id = 1 AND er.type = 'microsoft' AND ms.for_sale = 0 AND ms.status <> 'deleted' AND ms.email_domain = 'test.local' ORDER BY er.id ASC LIMIT 1000",
	)
	for i := 0; i < 50; i++ {
		resourceID := 1000 + i
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, ?, ?)",
			resourceID,
			"domain",
			1,
		).Error)
		require.NoError(t, db.Exec(
			"INSERT INTO domain_resources(id, owner_user_id, domain, domain_tld, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?, ?)",
			resourceID,
			1,
			fmt.Sprintf("bulk-%d.example.net", i),
			".net",
			200,
			"not_sale",
			"normal",
		).Error)
	}
	require.NoError(t, db.Exec("ANALYZE TABLE domain_resources").Error)
	requireExplainUsesIndex(t, db,
		"idx_domain_resources_owner_tld_private",
		"EXPLAIN SELECT er.id FROM domain_resources AS dr STRAIGHT_JOIN email_resources AS er ON er.id = dr.id WHERE er.owner_user_id = 1 AND er.type = 'domain' AND dr.owner_user_id = 1 AND dr.purpose = 'not_sale' AND dr.status <> 'deleted' AND dr.domain_tld = '.com' ORDER BY er.id ASC LIMIT 1000",
	)
}

func TestResourceRepoUpdateMicrosoftWithLogPreservesCredentialsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{
		Type:        domain.ResourceTypeMicrosoft,
		OwnerUserID: 1,
	}
	ms := &domain.MicrosoftResource{
		EmailAddress:  "ms@test.local",
		Password:      "original-password",
		ClientID:      "original-client",
		RefreshToken:  "original-refresh",
		ForSale:       true,
		Status:        domain.MicrosoftStatusPending,
		QualityScore:  10,
		LastSafeError: "",
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	update := &domain.MicrosoftResource{
		ID:            ms.ID,
		ForSale:       false,
		Status:        domain.MicrosoftStatusDisabled,
		QualityScore:  3,
		LastSafeError: "safe diagnostic",
	}
	require.NoError(t, repo.UpdateMicrosoftWithLog(context.Background(), update, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.resource.update",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/admin/resources/%d", ms.ID),
		Result:         "success",
		SafeSummary:    "Microsoft resource metadata updated.",
		RequestID:      "req-ms-update",
	}))

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, ms.ID).Error)
	require.Equal(t, "original-password", stored.Password)
	require.Equal(t, "original-client", stored.ClientID)
	require.Equal(t, "original-refresh", stored.RefreshToken)
	require.False(t, stored.ForSale)
	require.Equal(t, string(domain.MicrosoftStatusDisabled), stored.Status)
	require.Equal(t, 3, stored.QualityScore)
	require.Equal(t, "safe diagnostic", stored.LastSafeError)
}

func TestResourceRepoPublishMicrosoftWithLogIsIdempotentMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "publish-once@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	log := governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.publish",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d/publish", ms.ID),
		Result:         "success",
		SafeSummary:    "Microsoft resource published for sale.",
		RequestID:      "req-publish-once",
	}

	published, err := repo.PublishMicrosoftWithLog(context.Background(), 1, ms.ID, log)
	require.NoError(t, err)
	require.True(t, published)

	published, err = repo.PublishMicrosoftWithLog(context.Background(), 1, ms.ID, log)
	require.NoError(t, err)
	require.False(t, published)

	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, ms.ID).Error)
	require.True(t, stored.ForSale)

	var logCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE operation_type = ? AND resource_id = ?",
		"core.microsoft_resource.publish",
		fmt.Sprintf("%d", ms.ID),
	).Scan(&logCount).Error)
	require.Equal(t, int64(1), logCount)
}

func TestResourceRepoPublishResourcesBatchWithLogIsConcurrentIdempotentMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "publish-batch@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	baseLog := governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.publish_batch",
		ResourceType:   "microsoft_resource",
		Path:           "/v1/resources/publish",
		Result:         "success",
		SafeSummary:    "Microsoft resources published for sale.",
		RequestID:      "req-publish-batch",
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	publishedResults := make(chan int, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			publishedIDs, err := repo.PublishResourcesBatchWithLog(context.Background(), 1, []uint{ms.ID}, baseLog, governancedomain.OperationLog{})
			if err != nil {
				errs <- err
				return
			}
			publishedResults <- len(publishedIDs)
		}()
	}
	close(start)
	wg.Wait()
	close(publishedResults)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	totalPublished := 0
	for published := range publishedResults {
		totalPublished += published
	}
	require.Equal(t, 1, totalPublished)

	var logCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE operation_type = ? AND resource_id = ?",
		"core.microsoft_resource.publish_batch",
		fmt.Sprintf("%d", ms.ID),
	).Scan(&logCount).Error)
	require.Equal(t, int64(1), logCount)
}

func TestResourceRepoPublishResourcesBatchWithLogPublishesMixedResourcesAndRollsBackBindingMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status) VALUES (?, ?, ?, ?, ?)",
		200,
		1,
		"mx.aishop6.com",
		"mx.aishop6.com",
		"online",
	).Error)

	msRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "mixed-publish@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), msRoot, ms))

	domainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &domain.MailDomainResource{
		Domain:       "mixed-publish.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusAbnormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), domainRoot, dr))

	microsoftLog := governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.publish_batch",
		ResourceType:   "microsoft_resource",
		Path:           "/v1/resources/publish",
		Result:         "success",
		SafeSummary:    "Microsoft resources published for sale.",
		RequestID:      "req-mixed-publish",
	}
	domainLog := governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.domain_resource.publish_batch",
		ResourceType:   "domain_resource",
		Path:           "/v1/resources/publish",
		Result:         "success",
		SafeSummary:    "Domain resources published for sale.",
		RequestID:      "req-mixed-publish",
	}

	publishedIDs, err := repo.PublishResourcesBatchWithLog(context.Background(), 1, []uint{ms.ID, dr.ID}, microsoftLog, domainLog)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint{ms.ID, dr.ID}, publishedIDs)

	var storedMS MicrosoftResourceModel
	require.NoError(t, db.First(&storedMS, ms.ID).Error)
	require.True(t, storedMS.ForSale)
	var storedDomain DomainResourceModel
	require.NoError(t, db.First(&storedDomain, dr.ID).Error)
	require.Equal(t, string(domain.PurposeSale), storedDomain.Purpose)

	rollbackMSRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	rollbackMS := &domain.MicrosoftResource{
		EmailAddress: "mixed-rollback@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), rollbackMSRoot, rollbackMS))
	rollbackDomainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	rollbackDomain := &domain.MailDomainResource{
		Domain:       "mixed-binding.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeBinding,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), rollbackDomainRoot, rollbackDomain))

	publishedIDs, err = repo.PublishResourcesBatchWithLog(context.Background(), 1, []uint{rollbackMS.ID, rollbackDomain.ID}, microsoftLog, domainLog)
	require.ErrorIs(t, err, domain.ErrResourceNotPrivate)
	require.Empty(t, publishedIDs)

	var rollbackStoredMS MicrosoftResourceModel
	require.NoError(t, db.First(&rollbackStoredMS, rollbackMS.ID).Error)
	require.False(t, rollbackStoredMS.ForSale)
	var rollbackStoredDomain DomainResourceModel
	require.NoError(t, db.First(&rollbackStoredDomain, rollbackDomain.ID).Error)
	require.Equal(t, string(domain.PurposeBinding), rollbackStoredDomain.Purpose)

	var rollbackLogCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE operation_type = ? AND resource_id = ?",
		"core.microsoft_resource.publish_batch",
		fmt.Sprintf("%d", rollbackMS.ID),
	).Scan(&rollbackLogCount).Error)
	require.Zero(t, rollbackLogCount)
}

func TestResourceRepoDeletePrivateMicrosoftWithLogMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		10,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "delete-private@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	log := governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d", ms.ID),
		Result:         "success",
		SafeSummary:    "Private Microsoft resource deleted.",
		RequestID:      "req-delete-private",
	}
	require.NoError(t, repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, ms.ID, log))

	var rootCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM email_resources WHERE id = ?", ms.ID).Scan(&rootCount).Error)
	require.Equal(t, int64(1), rootCount)

	var status string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_resources WHERE id = ?", ms.ID).Scan(&status).Error)
	require.Equal(t, string(domain.MicrosoftStatusDeleted), status)

	listed, err := repo.List(context.Background(), 1, string(domain.ResourceTypeMicrosoft), 0, 20)
	require.NoError(t, err)
	require.Empty(t, listed)

	visibleCount, err := repo.Count(context.Background(), 1, string(domain.ResourceTypeMicrosoft))
	require.NoError(t, err)
	require.Zero(t, visibleCount)

	var logCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE operation_type = ? AND resource_id = ?",
		"core.microsoft_resource.delete_private",
		fmt.Sprintf("%d", ms.ID),
	).Scan(&logCount).Error)
	require.Equal(t, int64(1), logCount)
}

func TestResourceRepoDeletePublishedMicrosoftDeniedMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		10,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "delete-public@test.local",
		Password:     "secret",
		ForSale:      true,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))

	err := repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, ms.ID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d", ms.ID),
		Result:         "success",
		SafeSummary:    "Private Microsoft resource deleted.",
		RequestID:      "req-delete-public",
	})
	require.ErrorIs(t, err, domain.ErrResourceNotPrivate)

	var rootCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM email_resources WHERE id = ?", ms.ID).Scan(&rootCount).Error)
	require.Equal(t, int64(1), rootCount)

	var logCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE operation_type = ? AND resource_id = ?",
		"core.microsoft_resource.delete_private",
		fmt.Sprintf("%d", ms.ID),
	).Scan(&logCount).Error)
	require.Zero(t, logCount)
}

func TestResourceRepoPublishDeletedMicrosoftDeniedMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	ms := &domain.MicrosoftResource{
		EmailAddress: "deleted-publish@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusPending,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), root, ms))
	require.NoError(t, repo.DeletePrivateMicrosoftWithLog(context.Background(), 1, ms.ID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.delete_private",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d", ms.ID),
		Result:         "success",
		RequestID:      "req-delete-before-publish",
	}))

	published, err := repo.PublishMicrosoftWithLog(context.Background(), 1, ms.ID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.publish",
		ResourceType:   "microsoft_resource",
		ResourceID:     fmt.Sprintf("%d", ms.ID),
		Path:           fmt.Sprintf("/v1/resources/%d/publish", ms.ID),
		Result:         "success",
		RequestID:      "req-publish-deleted",
	})
	require.ErrorIs(t, err, domain.ErrResourceNotFound)
	require.False(t, published)

	publishedIDs, err := repo.PublishResourcesBatchWithLog(context.Background(), 1, []uint{ms.ID}, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.microsoft_resource.publish_batch",
		ResourceType:   "microsoft_resource",
		Path:           "/v1/resources/publish",
		Result:         "success",
		RequestID:      "req-publish-deleted-batch",
	}, governancedomain.OperationLog{})
	require.ErrorIs(t, err, domain.ErrResourceNotFound)
	require.Empty(t, publishedIDs)
}

func TestResourceRepoDeletePrivateDomainAndRestoreMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
		2,
		"new-owner@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)",
		200,
		1,
		"mx.aishop6.com",
		"mx.aishop6.com",
		"online",
		201,
		2,
		"mx.aishop6.com",
		"mx.aishop6.com",
		"online",
	).Error)

	root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	dr := &domain.MailDomainResource{
		Domain:       "restore-domain.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusAbnormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), root, dr))
	originalID := dr.ID
	require.NoError(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status) VALUES (?, ?, ?, ?)",
		originalID,
		1,
		"old-owner@restore-domain.example.com",
		"normal",
	).Error)

	require.NoError(t, repo.DeletePrivateDomainWithLog(context.Background(), 1, originalID, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.domain_resource.delete_private",
		ResourceType:   "domain_resource",
		ResourceID:     fmt.Sprintf("%d", originalID),
		Path:           fmt.Sprintf("/v1/resources/%d", originalID),
		Result:         "success",
		RequestID:      "req-delete-domain",
	}))

	var status string
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", originalID).Scan(&status).Error)
	require.Equal(t, string(domain.DomainStatusDeleted), status)

	listed, err := repo.List(context.Background(), 1, string(domain.ResourceTypeDomain), 0, 20)
	require.NoError(t, err)
	require.Empty(t, listed)

	visibleCount, err := repo.Count(context.Background(), 1, string(domain.ResourceTypeDomain))
	require.NoError(t, err)
	require.Zero(t, visibleCount)

	publishedIDs, err := repo.PublishResourcesBatchWithLog(context.Background(), 1, []uint{originalID}, governancedomain.OperationLog{}, governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "core.domain_resource.publish_batch",
		ResourceType:   "domain_resource",
		Path:           "/v1/resources/publish",
		Result:         "success",
		RequestID:      "req-publish-deleted-domain",
	})
	require.ErrorIs(t, err, domain.ErrResourceNotFound)
	require.Empty(t, publishedIDs)

	restoredRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 2}
	restoredDomain := &domain.MailDomainResource{
		Domain:       "restore-domain.example.com",
		MailServerID: 201,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusAbnormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), restoredRoot, restoredDomain))
	require.Equal(t, originalID, restoredDomain.ID)
	require.Equal(t, originalID, restoredRoot.ID)

	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", originalID).Scan(&status).Error)
	require.Equal(t, string(domain.DomainStatusAbnormal), status)

	var rootOwnerID, domainOwnerID, mailServerID uint
	require.NoError(t, db.Raw("SELECT owner_user_id FROM email_resources WHERE id = ?", originalID).Scan(&rootOwnerID).Error)
	require.NoError(t, db.Raw("SELECT owner_user_id, mail_server_id FROM domain_resources WHERE id = ?", originalID).Row().Scan(&domainOwnerID, &mailServerID))
	require.EqualValues(t, 2, rootOwnerID)
	require.EqualValues(t, 2, domainOwnerID)
	require.EqualValues(t, 201, mailServerID)

	var mailboxCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM generated_mailboxes WHERE resource_id = ?", originalID).Scan(&mailboxCount).Error)
	require.Zero(t, mailboxCount)

	mailboxes, err := NewGeneratedMailboxRepo(db).List(context.Background(), originalID, 2, 0, 20)
	require.NoError(t, err)
	require.Empty(t, mailboxes)

	listed, err = repo.List(context.Background(), 2, string(domain.ResourceTypeDomain), 0, 20)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

func TestResourceRepoDeleteResourcesBatchWithLogDeletesMixedPrivateAndSkipsPublicMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status) VALUES (?, ?, ?, ?, ?)",
		200,
		1,
		"mx.aishop6.com",
		"mx.aishop6.com",
		"online",
	).Error)

	privateMSRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	privateMS := &domain.MicrosoftResource{
		EmailAddress: "batch-delete-private@test.local",
		Password:     "secret",
		ForSale:      false,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), privateMSRoot, privateMS))

	publicMSRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	publicMS := &domain.MicrosoftResource{
		EmailAddress: "batch-delete-public@test.local",
		Password:     "secret",
		ForSale:      true,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), publicMSRoot, publicMS))

	privateDomainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	privateDomain := &domain.MailDomainResource{
		Domain:       "batch-delete-private.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), privateDomainRoot, privateDomain))

	saleDomainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	saleDomain := &domain.MailDomainResource{
		Domain:       "batch-delete-sale.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), saleDomainRoot, saleDomain))

	deletedIDs, err := repo.DeleteResourcesBatchWithLog(
		context.Background(),
		1,
		[]uint{privateMS.ID, publicMS.ID, privateDomain.ID, saleDomain.ID},
		governancedomain.OperationLog{
			OperatorUserID: 1,
			OperationType:  "core.microsoft_resource.delete_batch",
			ResourceType:   "microsoft_resource",
			Path:           "/v1/resources/delete",
			Result:         "success",
			RequestID:      "req-delete-batch",
		},
		governancedomain.OperationLog{
			OperatorUserID: 1,
			OperationType:  "core.domain_resource.delete_batch",
			ResourceType:   "domain_resource",
			Path:           "/v1/resources/delete",
			Result:         "success",
			RequestID:      "req-delete-batch",
		},
	)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint{privateMS.ID, privateDomain.ID}, deletedIDs)

	var privateMSStatus string
	require.NoError(t, db.Raw("SELECT status FROM microsoft_resources WHERE id = ?", privateMS.ID).Scan(&privateMSStatus).Error)
	require.Equal(t, string(domain.MicrosoftStatusDeleted), privateMSStatus)

	var publicMSStatus string
	var publicMSForSale bool
	require.NoError(t, db.Raw("SELECT status, for_sale FROM microsoft_resources WHERE id = ?", publicMS.ID).Row().Scan(&publicMSStatus, &publicMSForSale))
	require.Equal(t, string(domain.MicrosoftStatusNormal), publicMSStatus)
	require.True(t, publicMSForSale)

	var privateDomainStatus string
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", privateDomain.ID).Scan(&privateDomainStatus).Error)
	require.Equal(t, string(domain.DomainStatusDeleted), privateDomainStatus)

	var saleDomainStatus string
	var saleDomainPurpose string
	require.NoError(t, db.Raw("SELECT status, purpose FROM domain_resources WHERE id = ?", saleDomain.ID).Row().Scan(&saleDomainStatus, &saleDomainPurpose))
	require.Equal(t, string(domain.DomainStatusNormal), saleDomainStatus)
	require.Equal(t, string(domain.PurposeSale), saleDomainPurpose)

	var logCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM operation_logs WHERE request_id = ?",
		"req-delete-batch",
	).Scan(&logCount).Error)
	require.EqualValues(t, 2, logCount)
}

func TestResourceRepoBulkFilterMutationsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	repo := NewResourceRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"owner@test.local",
		"hash",
		20,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status) VALUES (?, ?, ?, ?, ?)",
		200,
		1,
		"mx.aishop6.com",
		"mx.aishop6.com",
		"online",
	).Error)

	matchingMS := &domain.MicrosoftResource{
		EmailAddress: "matching-filter@outlook.com",
		Password:     "secret",
		LongLived:    true,
		ForSale:      false,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}, matchingMS))
	shortMS := &domain.MicrosoftResource{
		EmailAddress: "short-filter@outlook.com",
		Password:     "secret",
		LongLived:    false,
		ForSale:      false,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}, shortMS))
	otherSuffixMS := &domain.MicrosoftResource{
		EmailAddress: "other-filter@gmail.com",
		Password:     "secret",
		LongLived:    true,
		ForSale:      false,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}, otherSuffixMS))
	abnormalMS := &domain.MicrosoftResource{
		EmailAddress: "abnormal-filter@outlook.com",
		Password:     "secret",
		LongLived:    true,
		ForSale:      false,
		Status:       domain.MicrosoftStatusAbnormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}, abnormalMS))
	oldMatchingMS := &domain.MicrosoftResource{
		EmailAddress: "old-filter@outlook.com",
		Password:     "secret",
		LongLived:    true,
		ForSale:      false,
		Status:       domain.MicrosoftStatusNormal,
	}
	require.NoError(t, repo.CreateMicrosoft(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}, oldMatchingMS))
	require.NoError(t, db.Exec(
		"UPDATE email_resources SET created_at = ? WHERE id = ?",
		time.Now().Add(-48*time.Hour),
		oldMatchingMS.ID,
	).Error)

	longLived := true
	createdFrom := time.Now().Add(-time.Hour)
	createdTo := time.Now().Add(time.Hour)
	published, err := repo.PublishResourcesByFilterWithLog(
		context.Background(),
		1,
		coreapp.ResourceBulkFilter{
			ResourceType: domain.ResourceTypeMicrosoft,
			Suffix:       "@outlook.com",
			Status:       string(domain.MicrosoftStatusNormal),
			LongLived:    &longLived,
			CreatedFrom:  &createdFrom,
			CreatedTo:    &createdTo,
		},
		governancedomain.OperationLog{
			OperatorUserID: 1,
			OperationType:  "core.microsoft_resource.publish_batch",
			ResourceType:   "microsoft_resource",
			Path:           "/v1/resources/publish",
			Result:         "success",
			RequestID:      "req-filter-publish",
		},
		governancedomain.OperationLog{},
	)
	require.NoError(t, err)
	require.Equal(t, 1, published)

	var matchingForSale, shortForSale, otherSuffixForSale, abnormalForSale, oldMatchingForSale bool
	require.NoError(t, db.Raw("SELECT for_sale FROM microsoft_resources WHERE id = ?", matchingMS.ID).Scan(&matchingForSale).Error)
	require.NoError(t, db.Raw("SELECT for_sale FROM microsoft_resources WHERE id = ?", shortMS.ID).Scan(&shortForSale).Error)
	require.NoError(t, db.Raw("SELECT for_sale FROM microsoft_resources WHERE id = ?", otherSuffixMS.ID).Scan(&otherSuffixForSale).Error)
	require.NoError(t, db.Raw("SELECT for_sale FROM microsoft_resources WHERE id = ?", abnormalMS.ID).Scan(&abnormalForSale).Error)
	require.NoError(t, db.Raw("SELECT for_sale FROM microsoft_resources WHERE id = ?", oldMatchingMS.ID).Scan(&oldMatchingForSale).Error)
	require.True(t, matchingForSale)
	require.False(t, shortForSale)
	require.False(t, otherSuffixForSale)
	require.False(t, abnormalForSale)
	require.False(t, oldMatchingForSale)

	matchingDomain := &domain.MailDomainResource{
		Domain:       "matching-filter.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, matchingDomain))
	otherTLDDomain := &domain.MailDomainResource{
		Domain:       "other-filter.example.net",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, otherTLDDomain))
	disabledDomain := &domain.MailDomainResource{
		Domain:       "disabled-filter.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusDisabled,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, disabledDomain))
	saleDomain := &domain.MailDomainResource{
		Domain:       "sale-filter.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, saleDomain))
	bindingDomain := &domain.MailDomainResource{
		Domain:       "binding-filter.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeBinding,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, bindingDomain))
	oldMatchingDomain := &domain.MailDomainResource{
		Domain:       "old-filter.example.com",
		MailServerID: 200,
		Purpose:      domain.PurposeNotSale,
		Status:       domain.DomainStatusNormal,
	}
	require.NoError(t, repo.CreateDomain(context.Background(), &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}, oldMatchingDomain))
	require.NoError(t, db.Exec(
		"UPDATE email_resources SET created_at = ? WHERE id = ?",
		time.Now().Add(-48*time.Hour),
		oldMatchingDomain.ID,
	).Error)

	deleted, err := repo.DeleteResourcesByFilterWithLog(
		context.Background(),
		1,
		coreapp.ResourceBulkFilter{
			ResourceType: domain.ResourceTypeDomain,
			TLD:          ".com",
			Status:       string(domain.DomainStatusNormal),
			CreatedFrom:  &createdFrom,
			CreatedTo:    &createdTo,
		},
		governancedomain.OperationLog{},
		governancedomain.OperationLog{
			OperatorUserID: 1,
			OperationType:  "core.domain_resource.delete_batch",
			ResourceType:   "domain_resource",
			Path:           "/v1/resources/delete",
			Result:         "success",
			RequestID:      "req-filter-delete",
		},
	)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	var matchingDomainStatus, otherTLDStatus, disabledStatus, saleStatus, bindingStatus, oldMatchingStatus string
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", matchingDomain.ID).Scan(&matchingDomainStatus).Error)
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", otherTLDDomain.ID).Scan(&otherTLDStatus).Error)
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", disabledDomain.ID).Scan(&disabledStatus).Error)
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", saleDomain.ID).Scan(&saleStatus).Error)
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", bindingDomain.ID).Scan(&bindingStatus).Error)
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id = ?", oldMatchingDomain.ID).Scan(&oldMatchingStatus).Error)
	require.Equal(t, string(domain.DomainStatusDeleted), matchingDomainStatus)
	require.Equal(t, string(domain.DomainStatusNormal), otherTLDStatus)
	require.Equal(t, string(domain.DomainStatusDisabled), disabledStatus)
	require.Equal(t, string(domain.DomainStatusNormal), saleStatus)
	require.Equal(t, string(domain.DomainStatusNormal), bindingStatus)
	require.Equal(t, string(domain.DomainStatusNormal), oldMatchingStatus)

	var filterDeleteLog struct {
		Count       int64
		ResourceID  string
		SafeSummary string
	}
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) AS count, COALESCE(MAX(resource_id), '') AS resource_id, COALESCE(MAX(safe_summary), '') AS safe_summary FROM operation_logs WHERE request_id = ?",
		"req-filter-delete",
	).Scan(&filterDeleteLog).Error)
	require.EqualValues(t, 1, filterDeleteLog.Count)
	require.Equal(t, "filter", filterDeleteLog.ResourceID)
	require.Contains(t, filterDeleteLog.SafeSummary, "Count: 1")
}

func requireIndexExists(t *testing.T, db *gorm.DB, tableName string, indexName string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		tableName,
		indexName,
	).Scan(&count).Error)
	require.Positive(t, count, "expected index %s on %s", indexName, tableName)
}

func requireExplainUsesIndex(t *testing.T, db *gorm.DB, expectedKey string, query string) {
	t.Helper()

	var rows []struct {
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
	}
	require.NoError(t, db.Raw(query).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	usedExpectedKey := false
	seenKeys := make([]string, 0, len(rows))
	for _, row := range rows {
		require.True(t, row.Key.Valid, "expected query to use an index: %s", query)
		seenKeys = append(seenKeys, row.Key.String)
		require.True(t, row.Rows.Valid, "expected query to expose row estimate: %s", query)
		require.LessOrEqual(t, row.Rows.Int64, int64(10), "unexpected row estimate for %s using %s", query, row.Key.String)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan for %s", query)
		if row.Key.String == expectedKey {
			usedExpectedKey = true
		}
	}
	require.True(t, usedExpectedKey, "expected query to use index %s, saw %v: %s", expectedKey, seenKeys, query)
}
