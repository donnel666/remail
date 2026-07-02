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
		"dns_abnormal",
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
		"dns_abnormal",
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
		"dns_normal",
	).Error)

	require.NoError(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, email, status) VALUES (?, ?, ?)",
		103,
		"user@valid.example.com",
		"normal",
	).Error)
	require.Error(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, email, status) VALUES (?, ?, ?)",
		103,
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
	requireIndexExists(t, db, "domain_resources", "idx_domain_resources_owner_created")
	requireIndexExists(t, db, "generated_mailboxes", "idx_generated_mailboxes_resource_created")
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

	require.ErrorIs(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms), domain.ErrDuplicateEmail)

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

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms))

	storedImport, err := importRepo.FindByID(context.Background(), importRecord.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceImportImported, storedImport.Status)
	require.Equal(t, 2, storedImport.ImportedCount)

	require.NoError(t, importRepo.CreateMicrosoftResourcesAndMarkSucceeded(context.Background(), importRecord.ID, resources, ms))

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
		"INSERT INTO microsoft_resources(id, email_address, password, status) VALUES (?, ?, ?, ?)",
		100,
		"ms@test.local",
		"secret",
		"pending",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, ?, ?, ?, ?, ?)",
		101,
		1,
		"valid.example.com",
		200,
		"sale",
		"dns_normal",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO generated_mailboxes(resource_id, email, status) VALUES (?, ?, ?)",
		101,
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
		"EXPLAIN SELECT * FROM generated_mailboxes WHERE resource_id = 101 ORDER BY created_at DESC LIMIT 20",
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

func TestResourceRepoPublishMicrosoftBatchWithLogIsConcurrentIdempotentMySQL(t *testing.T) {
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
			published, err := repo.PublishMicrosoftBatchWithLog(context.Background(), 1, []uint{ms.ID}, baseLog)
			if err != nil {
				errs <- err
				return
			}
			publishedResults <- published
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
		Key sql.NullString `gorm:"column:key"`
	}
	require.NoError(t, db.Raw(query).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	require.True(t, rows[0].Key.Valid, "expected query to use an index: %s", query)
	require.Equal(t, expectedKey, rows[0].Key.String, "unexpected index for %s", query)
}
