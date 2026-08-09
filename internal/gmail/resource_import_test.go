package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type gmailImportFileStore struct {
	files map[string]governancedomain.PrivateFile
}

func (s *gmailImportFileStore) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	if s.files == nil {
		s.files = make(map[string]governancedomain.PrivateFile)
	}
	file.ContentBytes = append([]byte(nil), file.ContentBytes...)
	s.files[file.ObjectKey] = file
	return &governancedomain.StoredPrivateFile{
		ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, Size: int64(len(file.ContentBytes)),
	}, nil
}

func (s *gmailImportFileStore) SavePrivateStream(ctx context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	return s.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, ContentBytes: content,
	})
}

func (s *gmailImportFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file, ok := s.files[objectKey]
	if !ok {
		return nil, fmt.Errorf("private file %q not found", objectKey)
	}
	file.ContentBytes = append([]byte(nil), file.ContentBytes...)
	return &file, nil
}

func (s *gmailImportFileStore) DeletePrivate(_ context.Context, objectKey string) error {
	delete(s.files, objectKey)
	return nil
}

func (*gmailImportFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

type gmailImportHarness struct {
	service   *Service
	db        *gorm.DB
	redis     *redis.Client
	inspector *asynq.Inspector
	files     *gmailImportFileStore
}

func newGmailImportHarness(t *testing.T) *gmailImportHarness {
	t.Helper()
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	queue := asynq.NewClient(redisOptions)
	inspector := asynq.NewInspector(redisOptions)
	databaseName := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &gmailMaintenanceRunModel{},
		&governanceinfra.OperationLogModel{}, &gmailAdminTestUser{}, &gmailAdminTestGroup{},
	))
	require.NoError(t, db.Create(&gmailAdminTestGroup{ID: 1, Name: "Suppliers"}).Error)
	require.NoError(t, db.Create(&gmailAdminTestUser{ID: 7, Email: "supplier@example.com", Nickname: "Supplier", Role: "supplier", Status: "active", UserGroupID: 1}).Error)
	files := &gmailImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	service := NewService(db, queue)
	service.SetResourceImportDependencies(redisClient, files)
	service.SetImportOwnerValidator(func(_ context.Context, ownerID uint) (bool, error) { return ownerID == 7, nil })
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, queue.Close())
		require.NoError(t, redisClient.Close())
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return &gmailImportHarness{service: service, db: db, redis: redisClient, inspector: inspector, files: files}
}

func TestGmailResourceImportAcceptsSupportedCredentialFormats(t *testing.T) {
	harness := newGmailImportHarness(t)
	accepted, _, err := harness.service.AcceptAdminGmailTXTFile(
		context.Background(), 9, 7, "gmail.txt", []byte(strings.Join([]string{
			"two.fields@gmail.com----password-2",
			"three.fields@gmail.com----password-3----JBSW Y3DP EHPK 3PXP",
			"binding.only@gmail.com----password-binding-only----Backup.Only@Example.NET",
			"four.fields@gmail.com----password-4----JBSWY3DPEHPK3PXP----abcd efgh ijkl mnop",
			"binding.fields@gmail.com----password-binding----Backup.Address@Example.COM----JBSW Y3DP EHPK 3PXP",
		}, "\n")), "skip", "gmail-import-formats", "request-formats", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.NoError(t, harness.service.ProcessGmailResourceImport(context.Background(), gmailResourceImportTask{
		ImportID: accepted.ImportID, Generation: 1,
	}))

	var resources []localResourceModel
	require.NoError(t, harness.db.Order("email ASC").Find(&resources).Error)
	require.Len(t, resources, 5)
	byEmail := make(map[string]localResourceModel, len(resources))
	for _, resource := range resources {
		byEmail[resource.Email] = resource
		require.Equal(t, LocalResourcePending, resource.Status)
	}
	require.Empty(t, byEmail["two.fields@gmail.com"].TwoFactorSecret)
	require.Empty(t, byEmail["two.fields@gmail.com"].AppPassword)
	require.Equal(t, "JBSWY3DPEHPK3PXP", byEmail["three.fields@gmail.com"].TwoFactorSecret)
	require.Empty(t, byEmail["three.fields@gmail.com"].AppPassword)
	require.Equal(t, "backup.only@example.net", byEmail["binding.only@gmail.com"].BindingEmail)
	require.Empty(t, byEmail["binding.only@gmail.com"].TwoFactorSecret)
	require.Empty(t, byEmail["binding.only@gmail.com"].AppPassword)
	require.Equal(t, "JBSWY3DPEHPK3PXP", byEmail["four.fields@gmail.com"].TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", byEmail["four.fields@gmail.com"].AppPassword)
	require.Equal(t, "backup.address@example.com", byEmail["binding.fields@gmail.com"].BindingEmail)
	require.Equal(t, "JBSWY3DPEHPK3PXP", byEmail["binding.fields@gmail.com"].TwoFactorSecret)
	require.Empty(t, byEmail["binding.fields@gmail.com"].AppPassword)
}

func TestGmailResourceImportUsesRedisReferenceTaskAndSafeLineResults(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	content := strings.Join([]string{
		"first.last@gmail.com----login password----JBSW Y3DP EHPK 3PXP----abcd efgh ijkl mnop",
		"firstlast@googlemail.com----duplicate password----JBSWY3DPEHPK3PXP----ponmlkjihgfedcba",
		"firstlast+tag@gmail.com----invalid password----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
	}, "\n")

	accepted, reused, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt", []byte(content), "skip", "gmail-import-key", "request-1", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, "processing", accepted.Status)
	require.Equal(t, "queued", accepted.TaskStatus)
	require.Len(t, harness.files.files, 1)
	var sourceObjectKey string
	for key := range harness.files.files {
		sourceObjectKey = key
	}

	replayed, reused, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt", []byte(content), "skip", "gmail-import-key", "request-2", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, accepted.ImportID, replayed.ImportID)
	require.Len(t, harness.files.files, 1)
	_, _, err = harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt", []byte("other@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "gmail-import-key", "request-3", "/v1/admin/gmail/resources/imports",
	)
	require.ErrorIs(t, err, ErrGmailImportConflict)

	scheduled, err := harness.inspector.ListScheduledTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, typeGmailResourceImport, scheduled[0].Type)
	var task gmailResourceImportTask
	require.NoError(t, json.Unmarshal(scheduled[0].Payload, &task))
	require.Equal(t, gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}, task)
	require.JSONEq(t, fmt.Sprintf(`{"importId":%d,"generation":1}`, accepted.ImportID), string(scheduled[0].Payload))
	for _, privateValue := range []string{
		"first.last@gmail.com", "login password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop", sourceObjectKey,
	} {
		require.NotContains(t, string(scheduled[0].Payload), privateValue)
	}

	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, task))
	status, err := harness.service.GetAdminGmailResourceImport(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.Equal(t, "imported", status.Status)
	require.Equal(t, "succeeded", status.TaskStatus)
	require.Equal(t, 1, status.Accepted)
	require.Equal(t, 1, status.Imported)
	require.Equal(t, 2, status.Skipped)
	require.Zero(t, status.Attempts)
	require.Equal(t, gmailResourceImportMaxAttempts, status.MaxAttempts)
	require.NotNil(t, status.StartedAt)
	require.NotNil(t, status.FinishedAt)
	require.Len(t, harness.files.files, 2)
	require.Contains(t, harness.files.files, sourceObjectKey)
	var failureObjectKey string
	for key, file := range harness.files.files {
		if file.FileName == "gmail-import-failures.csv" {
			failureObjectKey = key
			require.Equal(t, "text/csv; charset=utf-8", file.ContentType)
			require.Equal(t, strings.Join([]string{
				"line,email,category,message",
				`3,"firstlast+tag@gmail.com","invalid_format","Invalid import format."`,
				`2,"firstlast@googlemail.com","duplicate_email","Duplicate email address in import file; first occurrence is line 1."`,
				"",
			}, "\n"), string(file.ContentBytes))
		}
	}
	require.NotEmpty(t, failureObjectKey)
	rawState, err := harness.redis.HGetAll(ctx, gmailResourceImportStatusKey(accepted.ImportID)).Result()
	require.NoError(t, err)
	require.Equal(t, sourceObjectKey, rawState["source_object_key"])
	require.Equal(t, failureObjectKey, rawState["failure_object_key"])
	require.Equal(t, "", rawState["claim_token"])
	require.Equal(t, time.Duration(-1), harness.redis.TTL(ctx, gmailResourceImportStatusKey(accepted.ImportID)).Val())

	var resource localResourceModel
	require.NoError(t, harness.db.First(&resource).Error)
	require.EqualValues(t, 7, resource.OwnerUserID)
	require.Equal(t, "first.last@gmail.com", resource.Email)
	require.Equal(t, "firstlast@gmail.com", resource.Identity)
	require.Equal(t, "login password", resource.Password)
	require.Equal(t, "JBSWY3DPEHPK3PXP", resource.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", resource.AppPassword)
	require.Equal(t, LocalResourcePending, resource.Status)
	var root resourceRootModel
	require.NoError(t, harness.db.First(&root, resource.ID).Error)
	require.Equal(t, "gmail", root.Type)
	require.EqualValues(t, 7, root.OwnerUserID)

	rawItems, err := harness.redis.HGetAll(ctx, gmailResourceImportItemsKey(accepted.ImportID)).Result()
	require.NoError(t, err)
	require.Len(t, rawItems, 3)
	items := make(map[int]gmailResourceImportItem, len(rawItems))
	for _, raw := range rawItems {
		var item gmailResourceImportItem
		require.NoError(t, json.Unmarshal([]byte(raw), &item))
		items[item.LineNumber] = item
		for _, privateValue := range []string{"@gmail.com", "@googlemail.com", "password", "JBSWY3DPEHPK3PXP", sourceObjectKey} {
			require.NotContains(t, raw, privateValue)
		}
	}
	require.Equal(t, "imported", items[1].Outcome)
	require.Equal(t, resource.ID, items[1].ResourceID)
	require.Equal(t, "duplicate_email", items[2].Category)
	require.Equal(t, "invalid_format", items[3].Category)

	dispatchTasks, err := harness.inspector.ListPendingTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Len(t, dispatchTasks, 1)
	require.Equal(t, typeGmailValidationDispatcher, dispatchTasks[0].Type)
	require.NoError(t, harness.service.DispatchLocalResourceValidations(ctx, localGmailValidationBatchMax))
	validationTasks, err := harness.inspector.ListScheduledTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Len(t, validationTasks, 1)
	var validationTask localResourceValidationTask
	require.NoError(t, json.Unmarshal(validationTasks[0].Payload, &validationTask))
	require.Equal(t, resource.ID, validationTask.ResourceID)
	require.EqualValues(t, 7, validationTask.OwnerUserID)
	require.EqualValues(t, 1, validationTask.ValidationGeneration)
	require.EqualValues(t, 1, validationTask.ExpectedCredentialRevision)
	require.Equal(t, "request-1", validationTask.RequestID)

	var audit governanceinfra.OperationLogModel
	require.NoError(t, harness.db.First(&audit).Error)
	require.EqualValues(t, 9, audit.OperatorUserID)
	require.Equal(t, "gmail.admin_resource.import", audit.OperationType)
	require.Equal(t, "gmail_resource_import", audit.ResourceType)
	require.Equal(t, "request-1", audit.RequestID)
	for _, privateValue := range []string{"first.last@gmail.com", "login password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop", sourceObjectKey} {
		require.NotContains(t, audit.SafeSummary, privateValue)
	}
}

func TestGmailResourceImportNeverUpdatesExistingCredentials(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7}
	require.NoError(t, harness.db.Create(&root).Error)
	require.NoError(t, harness.db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "existing@gmail.com", Identity: "existing@gmail.com", Password: "original-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "original-app-password", Status: LocalResourceNormal,
	}).Error)

	skipView, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("e.xisting@googlemail.com----replacement-password----KRSXG5DSNFXGOIDB----replacement-app-password"),
		"skip", "skip-existing", "request-skip", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: skipView.ImportID, Generation: 1}))
	skipStatus, err := harness.service.GetAdminGmailResourceImport(ctx, skipView.ImportID)
	require.NoError(t, err)
	require.Equal(t, "imported", skipStatus.Status)
	require.Zero(t, skipStatus.Imported)
	require.Equal(t, 1, skipStatus.Skipped)

	abortView, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt", []byte(strings.Join([]string{
			"new@gmail.com----new-password----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
			"existing@gmail.com----must-not-replace----KRSXG5DSNFXGOIDB----ponmlkjihgfedcba",
		}, "\n")), "abort", "abort-existing", "request-abort", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: abortView.ImportID, Generation: 1}))
	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: abortView.ImportID, Generation: 2}))
	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: abortView.ImportID, Generation: 3}))
	abortStatus, err := harness.service.GetAdminGmailResourceImport(ctx, abortView.ImportID)
	require.NoError(t, err)
	require.Equal(t, "failed", abortStatus.Status)
	require.Equal(t, "failed", abortStatus.TaskStatus)
	require.Equal(t, gmailResourceImportMaxAttempts, abortStatus.Attempts)
	require.Zero(t, abortStatus.Imported)
	abortRecord, err := harness.service.gmailResourceImportRecord(ctx, abortView.ImportID)
	require.NoError(t, err)
	require.NotEmpty(t, abortRecord.SourceObjectKey)
	require.NotEmpty(t, abortRecord.FailureObjectKey)
	require.Contains(t, harness.files.files, abortRecord.SourceObjectKey)
	require.Contains(t, harness.files.files, abortRecord.FailureObjectKey)

	var stored localResourceModel
	require.NoError(t, harness.db.First(&stored, root.ID).Error)
	require.Equal(t, "original-password", stored.Password)
	require.Equal(t, "JBSWY3DPEHPK3PXP", stored.TwoFactorSecret)
	require.Equal(t, "original-app-password", stored.AppPassword)
	var resourceCount, rootCount int64
	require.NoError(t, harness.db.Model(&localResourceModel{}).Count(&resourceCount).Error)
	require.NoError(t, harness.db.Model(&resourceRootModel{}).Count(&rootCount).Error)
	require.EqualValues(t, 1, resourceCount)
	require.EqualValues(t, 1, rootCount)
	require.Len(t, harness.files.files, 4)
	for _, file := range harness.files.files {
		require.Contains(t, []string{"gmail.txt", "gmail-import-failures.csv"}, file.FileName)
	}
}

func TestGmailResourceImportRestoresDeletedResourceWithImportedCredentials(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	checkedAt := time.Now().UTC().Add(-time.Hour)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 6, Version: 4}
	require.NoError(t, harness.db.Create(&root).Error)
	require.NoError(t, harness.db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 6,
		Email: "restore@gmail.com", Identity: "restore@gmail.com", Password: "old-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "old-app-password",
		Status: LocalResourceDeleted, LastSafeError: "Deleted Gmail resource.", LastCheckedAt: &checkedAt,
	}).Error)
	listed, err := harness.service.ListLocalResources(ctx, LocalResourceListFilter{})
	require.NoError(t, err)
	require.Zero(t, listed.Total, "deleted resources stay out of the normal Gmail inventory view")
	require.Zero(t, listed.Facets.All)

	accepted, reused, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("r.e.s.t.o.r.e@googlemail.com----new-password----KRSXG5DSNFXGOIDB----new-app-password"),
		"skip", "restore-deleted", "request-restore", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	require.False(t, reused)
	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}))

	status, err := harness.service.GetAdminGmailResourceImport(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.Equal(t, "imported", status.Status)
	require.Equal(t, 1, status.Accepted)
	require.Equal(t, 1, status.Imported)
	require.Zero(t, status.Skipped)

	var restored localResourceModel
	require.NoError(t, harness.db.First(&restored, root.ID).Error)
	require.Equal(t, root.ID, restored.ID)
	require.EqualValues(t, 7, restored.OwnerUserID)
	require.Equal(t, "r.e.s.t.o.r.e@googlemail.com", restored.Email)
	require.Equal(t, "restore@gmail.com", restored.Identity)
	require.Equal(t, "new-password", restored.Password)
	require.Equal(t, "KRSXG5DSNFXGOIDB", restored.TwoFactorSecret)
	require.Equal(t, "new-app-password", restored.AppPassword)
	require.Equal(t, LocalResourcePending, restored.Status)
	require.Empty(t, restored.LastSafeError)
	require.Nil(t, restored.LastCheckedAt)

	var restoredRoot resourceRootModel
	require.NoError(t, harness.db.First(&restoredRoot, root.ID).Error)
	require.EqualValues(t, 7, restoredRoot.OwnerUserID)
	require.EqualValues(t, 5, restoredRoot.Version)

	rawItems, err := harness.redis.HGetAll(ctx, gmailResourceImportItemsKey(accepted.ImportID)).Result()
	require.NoError(t, err)
	require.Len(t, rawItems, 1)
	var item gmailResourceImportItem
	for _, raw := range rawItems {
		require.NoError(t, json.Unmarshal([]byte(raw), &item))
	}
	require.Equal(t, "restored", item.Outcome)
	require.Equal(t, root.ID, item.ResourceID)
	require.Len(t, harness.files.files, 1, "the private source TXT is retained even without failures")
}

func TestGmailResourceImportFencesDuplicateWorkersAndReleasesGeneration(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	accepted, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("fence@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "claim-fence", "request-fence", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	task := gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}
	claimToken, claimed, err := harness.service.markGmailResourceImportRunning(ctx, task, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, claimToken)

	require.ErrorIs(t, harness.service.ProcessGmailResourceImport(ctx, task), ErrGmailImportTemporary)
	require.ErrorIs(t, harness.service.finishPreparedGmailResourceImport(
		ctx, accepted.ImportID, 1, "stale-claim", time.Now().UTC(),
	), ErrGmailImportInvalidClaim)
	require.NoError(t, harness.service.ReleaseGmailResourceImport(ctx, task, "temporary infrastructure failure"))

	record, err := harness.service.gmailResourceImportRecord(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.Equal(t, "processing", record.Status)
	require.Equal(t, "pending", record.TaskStatus)
	require.EqualValues(t, 2, record.Generation)
	require.Zero(t, record.Attempts)
	require.Empty(t, record.ClaimToken)
	require.NotEmpty(t, record.SourceObjectKey)
}

func TestGmailResourceImportFinalizesPreparedRedisResultWithoutRecreatingResources(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	accepted, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("prepared@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "prepared-result", "request-prepared", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	task := gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}
	claimToken, claimed, err := harness.service.markGmailResourceImportRunning(ctx, task, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, claimed)
	line, valid := parseLocalResourceImportLine("prepared@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop")
	require.True(t, valid)
	line.lineNumber = 1
	writes, err := harness.service.createGmailResourcesForImport(ctx, 7, "request-prepared", []localResourceImportLine{line}, func(created []gmailResourceImportWrite) error {
		return harness.service.prepareGmailResourceImportResult(
			ctx, accepted.ImportID, 1, claimToken,
			gmailResourceImportResultItems([]localResourceImportLine{line}, created, nil),
			1, 1, 0, "", "",
		)
	})
	require.NoError(t, err)
	require.Len(t, writes, 1)
	prepared, err := harness.service.gmailResourceImportRecord(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	require.Equal(t, "running", prepared.TaskStatus)

	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, task))
	status, err := harness.service.GetAdminGmailResourceImport(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.Equal(t, "imported", status.Status)
	require.Equal(t, 1, status.Imported)
	var resourceCount int64
	require.NoError(t, harness.db.Model(&localResourceModel{}).Count(&resourceCount).Error)
	require.EqualValues(t, 1, resourceCount)
	dispatchTasks, err := harness.inspector.ListPendingTasks(platform.QueueBackgroundGmailValidation)
	require.NoError(t, err)
	require.Len(t, dispatchTasks, 1)
	require.Equal(t, typeGmailValidationDispatcher, dispatchTasks[0].Type)
}

func TestGmailResourceImportClearsPreparedResultAfterDatabaseRollback(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	accepted, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("rollback@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "prepared-rollback", "request-rollback", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	task := gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}
	claimToken, claimed, err := harness.service.markGmailResourceImportRunning(ctx, task, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, claimed)
	line, valid := parseLocalResourceImportLine("rollback@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop")
	require.True(t, valid)
	line.lineNumber = 1
	_, err = harness.service.createGmailResourcesForImport(ctx, 7, "request-rollback", []localResourceImportLine{line}, func(created []gmailResourceImportWrite) error {
		if err := harness.service.prepareGmailResourceImportResult(
			ctx, accepted.ImportID, 1, claimToken,
			gmailResourceImportResultItems([]localResourceImportLine{line}, created, nil),
			1, 1, 0, "", "",
		); err != nil {
			return err
		}
		return errors.New("force database rollback after Redis prepare")
	})
	require.EqualError(t, err, "force database rollback after Redis prepare")
	var rolledBackCount int64
	require.NoError(t, harness.db.Model(&localResourceModel{}).Count(&rolledBackCount).Error)
	require.Zero(t, rolledBackCount)
	require.NoError(t, harness.service.ReleaseGmailResourceImport(ctx, task, "database commit failed"))

	require.NoError(t, harness.service.ProcessGmailResourceImport(ctx, gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 2}))
	status, err := harness.service.GetAdminGmailResourceImport(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.Equal(t, "imported", status.Status)
	require.Equal(t, 1, status.Imported)
	var committedCount int64
	require.NoError(t, harness.db.Model(&localResourceModel{}).Count(&committedCount).Error)
	require.EqualValues(t, 1, committedCount)
}

func TestGmailResourceImportDispatcherRecoversStaleRunningLease(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	accepted, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("stale@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "stale-running", "request-stale", "/v1/admin/gmail/resources/imports",
	)
	require.NoError(t, err)
	now := time.Now().UTC()
	task := gmailResourceImportTask{ImportID: accepted.ImportID, Generation: 1}
	_, claimed, err := harness.service.markGmailResourceImportRunning(ctx, task, now.Add(-gmailResourceImportRunningLease-time.Second))
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, harness.service.DispatchGmailResourceImports(ctx, 100))
	record, err := harness.service.gmailResourceImportRecord(ctx, accepted.ImportID)
	require.NoError(t, err)
	require.EqualValues(t, 2, record.Generation)
	require.Equal(t, "queued", record.TaskStatus)
	require.Empty(t, record.ClaimToken)
	require.Zero(t, record.Attempts)

	scheduled, err := harness.inspector.ListScheduledTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, scheduled, 2)
	var generations []uint64
	for _, queued := range scheduled {
		var payload gmailResourceImportTask
		require.NoError(t, json.Unmarshal(queued.Payload, &payload))
		generations = append(generations, payload.Generation)
	}
	require.ElementsMatch(t, []uint64{1, 2}, generations)
}

func TestGmailResourceImportAuditFailureRollsBackRedisAndSource(t *testing.T) {
	harness := newGmailImportHarness(t)
	ctx := context.Background()
	require.NoError(t, harness.db.Migrator().DropTable(&governanceinfra.OperationLogModel{}))

	_, _, err := harness.service.AcceptAdminGmailTXTFile(
		ctx, 9, 7, "gmail.txt",
		[]byte("audit@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "audit-failure", "request-audit", "/v1/admin/gmail/resources/imports",
	)
	require.ErrorIs(t, err, ErrGmailImportDependency)
	require.Empty(t, harness.files.files)
	keys, err := harness.redis.Keys(ctx, gmailResourceImportRedisPrefix+"status:*").Result()
	require.NoError(t, err)
	require.Empty(t, keys)
	require.Zero(t, harness.redis.Exists(ctx, gmailResourceImportIdempotencyKey(9, "audit-failure")).Val())
	require.Zero(t, harness.redis.ZCard(ctx, gmailResourceImportDispatchKey).Val())
}

func TestGmailResourceImportValidatesOwnerBeforePersisting(t *testing.T) {
	harness := newGmailImportHarness(t)
	_, _, err := harness.service.AcceptAdminGmailTXTFile(
		context.Background(), 9, 8, "gmail.txt",
		[]byte("owner@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop"),
		"skip", "invalid-owner", "request-owner", "/v1/admin/gmail/resources/imports",
	)
	require.ErrorIs(t, err, ErrGmailImportInvalidOwner)
	require.Empty(t, harness.files.files)
	require.False(t, harness.db.Migrator().HasTable("resource_imports"))
	require.False(t, harness.db.Migrator().HasTable("resource_import_items"))
}

func TestAdminGmailResourceImportAPIResponds202AndSupportsStatusPolling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	harness := newGmailImportHarness(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "gmail.txt")
	require.NoError(t, err)
	_, err = io.WriteString(file, "api@gmail.com----api-password----JBSWY3DPEHPK3PXP----abcdefghijklmnop")
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("ownerId", "7"))
	require.NoError(t, writer.WriteField("errorStrategy", "skip"))
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/gmail/resources/imports", &body)
	ginContext.Request.Header.Set("Content-Type", writer.FormDataContentType())
	ginContext.Request.Header.Set("Idempotency-Key", "api-import-key")
	ginContext.Set("request_id", "api-request")
	middleware.SetCurrentUser(ginContext, 9, "admin", "admin@example.com", "session")
	(&handler{service: harness.service}).importLocalResources(ginContext)

	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "api-password")
	var accepted gmailResourceImportResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	require.Positive(t, accepted.ImportID)
	require.Equal(t, "processing", accepted.Status)
	require.Equal(t, "gmail_resource_import", accepted.Task.BizType)
	require.Equal(t, "queued", accepted.Task.Status)
	for objectKey := range harness.files.files {
		require.NotContains(t, recorder.Body.String(), objectKey)
	}

	statusRecorder := httptest.NewRecorder()
	statusContext, _ := gin.CreateTestContext(statusRecorder)
	statusContext.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/gmail/resources/imports/"+strconv.FormatUint(accepted.ImportID, 10), nil)
	statusContext.Params = gin.Params{{Key: "importId", Value: strconv.FormatUint(accepted.ImportID, 10)}}
	statusContext.Set("request_id", "api-status-request")
	(&handler{service: harness.service}).localResourceImport(statusContext)
	require.Equal(t, http.StatusOK, statusRecorder.Code)
	require.Contains(t, statusRecorder.Body.String(), `"importId":`+strconv.FormatUint(accepted.ImportID, 10))
	require.NotContains(t, statusRecorder.Body.String(), "api-password")
	for objectKey := range harness.files.files {
		require.NotContains(t, statusRecorder.Body.String(), objectKey)
	}
}
