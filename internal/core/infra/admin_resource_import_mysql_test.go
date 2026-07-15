package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

func TestResourceImportWithoutAdministratorIsDurablyDispatchableMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES (1, 'durable-user-import@test.local', 'hash', 'supplier', TRUE)`).Error)

	repo := NewResourceImportRepo(db)
	item := &domain.ResourceImport{
		OwnerUserID:     1,
		ResourceType:    domain.ResourceTypeMicrosoft,
		LongLived:       true,
		ErrorStrategy:   domain.ImportErrorStrategyAbort,
		SourceObjectKey: "imports/microsoft/source/user-durable.txt",
		Status:          domain.ResourceImportProcessing,
		DispatchStatus:  "queued",
		MaxAttempts:     3,
		RequestID:       "req-user-durable-import",
	}
	require.NoError(t, repo.Create(context.Background(), item))

	var row ResourceImportModel
	require.NoError(t, db.First(&row, item.ID).Error)
	require.Nil(t, row.OperatorUserID)
	require.Equal(t, "queued", row.DispatchStatus)
	require.True(t, row.LongLived)
	require.Equal(t, string(domain.ImportErrorStrategyAbort), row.ErrorStrategy)
	require.Equal(t, item.RequestID, row.RequestID)

	now := time.Now().UTC()
	dispatchable, err := repo.ClaimAdminImportDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	require.Equal(t, item.ID, dispatchable[0].ImportID)
	require.True(t, dispatchable[0].LongLived)
	require.Equal(t, domain.ImportErrorStrategyAbort, dispatchable[0].ErrorStrategy)
	require.Equal(t, item.RequestID, dispatchable[0].RequestID)
	require.NotEmpty(t, dispatchable[0].DispatchToken)

	claimToken, claimed, err := repo.MarkAdminImportRunning(context.Background(), item.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.SetAdminImportCounts(context.Background(), item.ID, claimToken, 2, 1))
}

func TestAdminResourceImportSerializedQueueTaskHydratesDurableObjectKeyMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES
    (1, 'serialized-import-owner@test.local', 'hash', 'supplier', TRUE),
    (9, 'serialized-import-operator@test.local', 'hash', 'admin', TRUE)`).Error)

	const (
		sourceObjectKey = "imports/microsoft/source/private-object-key-canary.txt"
		passwordCanary  = "private-import-password-canary"
	)
	repo := NewResourceImportRepo(db)
	stored, created, err := repo.CreateAdminWithLog(context.Background(), &domain.ResourceImport{
		OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: sourceObjectKey, Status: domain.ResourceImportProcessing,
	}, coreapp.AdminResourceImportMetadata{
		OperatorUserID: 9, LongLived: true, ErrorStrategy: domain.ImportErrorStrategyAbort,
		RequestID: "req-serialized-import", Path: "/v1/admin/resources/imports",
		IdempotencyKey: "admin-import-serialized", RequestFingerprint: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}, nil)
	require.NoError(t, err)
	require.True(t, created)

	now := time.Now().UTC()
	dispatchable, err := repo.ClaimAdminImportDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)

	// SourceObjectKey is deliberately populated to model an older in-memory
	// caller. JSON serialization must still strip it before the Asynq boundary.
	queuedTask := coreapp.MicrosoftImportTask{
		ImportID: stored.ID, OwnerUserID: dispatchable[0].OwnerUserID,
		SourceObjectKey: sourceObjectKey, LongLived: dispatchable[0].LongLived,
		ErrorStrategy: dispatchable[0].ErrorStrategy, RequestID: dispatchable[0].RequestID,
		DispatchToken: dispatchable[0].DispatchToken,
	}
	payload, err := json.Marshal(queuedTask)
	require.NoError(t, err)
	require.NotContains(t, string(payload), sourceObjectKey)
	require.NotContains(t, string(payload), passwordCanary)
	require.NotContains(t, string(payload), "sourceObjectKey")
	require.Contains(t, string(payload), dispatchable[0].DispatchToken)

	var decoded coreapp.MicrosoftImportTask
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Empty(t, decoded.SourceObjectKey)

	files := &serializedImportFileStore{
		objectKey: sourceObjectKey,
		content:   []byte("serialized-queue@outlook.com----" + passwordCanary),
	}
	useCase := coreapp.NewImportUseCase(NewResourceRepo(db), repo, NewTXTParser(), files, nil)
	result, err := useCase.ProcessMicrosoftImport(context.Background(), decoded)
	require.NoError(t, err)
	require.Len(t, result.ImportedResourceIDs, 1)
	require.Equal(t, sourceObjectKey, files.readObjectKey)
	require.Equal(t, 1, files.readCount)

	var row ResourceImportModel
	require.NoError(t, db.First(&row, stored.ID).Error)
	require.Equal(t, string(domain.ResourceImportImported), row.Status)
	require.Equal(t, "succeeded", row.DispatchStatus)
	require.Equal(t, 1, row.Attempts)
	require.Empty(t, row.ClaimToken)
	require.Empty(t, row.DispatchToken)

	// Replaying the consumed opaque dispatch token must remain fenced and must
	// not read the private object a second time.
	replayed, err := useCase.ProcessMicrosoftImport(context.Background(), decoded)
	require.NoError(t, err)
	require.Empty(t, replayed.ImportedResourceIDs)
	require.Equal(t, 1, files.readCount)
}

func TestAdminResourceImportIdempotencyMetadataAndAuditMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES
    (1, 'import-owner@test.local', 'hash', 'supplier', TRUE),
    (9, 'import-operator@test.local', 'hash', 'admin', TRUE)`).Error)

	repo := NewResourceImportRepo(db)
	item := &domain.ResourceImport{
		OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/admin-idempotent.txt",
		Status:          domain.ResourceImportProcessing,
	}
	metadata := coreapp.AdminResourceImportMetadata{
		OperatorUserID: 9, LongLived: true, ErrorStrategy: domain.ImportErrorStrategyAbort,
		RequestID: "req-admin-import", Path: "/v1/admin/resources/imports",
		IdempotencyKey: "admin-import-key", RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	stored, created, err := repo.CreateAdminWithLog(context.Background(), item, metadata, &governancedomain.OperationLog{
		OperatorUserID: 9, OperationType: "core.admin_resource.import",
		ResourceType: "microsoft_resource_import", ResourceID: "pending",
		Path: metadata.Path, Result: "success", SafeSummary: "Microsoft resource import accepted.", RequestID: metadata.RequestID,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotZero(t, stored.ID)
	require.Equal(t, metadata.RequestID, stored.RequestID)

	var row ResourceImportModel
	require.NoError(t, db.First(&row, stored.ID).Error)
	require.NotNil(t, row.OperatorUserID)
	require.Equal(t, uint(9), *row.OperatorUserID)
	require.True(t, row.LongLived)
	require.Equal(t, string(domain.ImportErrorStrategyAbort), row.ErrorStrategy)
	require.Equal(t, "queued", row.DispatchStatus)
	require.Equal(t, metadata.IdempotencyKey, row.IdempotencyKey)
	require.Equal(t, metadata.RequestFingerprint, row.RequestFingerprint)
	now := time.Now().UTC()
	dispatchable, err := repo.ClaimAdminImportDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	require.Equal(t, stored.ID, dispatchable[0].ImportID)
	require.True(t, dispatchable[0].LongLived)
	require.NotEmpty(t, dispatchable[0].DispatchToken)
	claimToken, claimed, err := repo.MarkAdminImportRunning(context.Background(), stored.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, claimToken)
	require.NoError(t, db.First(&row, stored.ID).Error)
	require.Equal(t, "running", row.DispatchStatus)
	require.Equal(t, 1, row.Attempts)
	require.NotNil(t, row.StartedAt)
	require.Equal(t, claimToken, row.ClaimToken)

	replayed, created, err := repo.CreateAdminWithLog(context.Background(), &domain.ResourceImport{
		OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/unused-replay.txt", Status: domain.ResourceImportProcessing,
	}, metadata, nil)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, stored.ID, replayed.ID)
	require.Equal(t, metadata.RequestID, replayed.RequestID)
	reloaded, err := repo.FindByID(context.Background(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, metadata.RequestID, reloaded.RequestID)

	conflicting := metadata
	conflicting.RequestFingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, _, err = repo.CreateAdminWithLog(context.Background(), item, conflicting, nil)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)

	var imports, logs int64
	require.NoError(t, db.Model(&ResourceImportModel{}).Count(&imports).Error)
	require.NoError(t, db.Table("operation_logs").Where("request_id = ?", metadata.RequestID).Count(&logs).Error)
	require.EqualValues(t, 1, imports)
	require.EqualValues(t, 1, logs)
}

func TestAdminResourceImportSerializesConcurrentIdempotentRequestsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES
    (1, 'import-concurrent-owner@test.local', 'hash', 'supplier', TRUE),
    (9, 'import-concurrent-operator@test.local', 'hash', 'admin', TRUE)`).Error)
	repo := NewResourceImportRepo(db)

	const workerCount = 10
	start := make(chan struct{})
	storedByWorker := make([]*domain.ResourceImport, workerCount)
	createdByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			requestID := fmt.Sprintf("req-admin-import-concurrent-%03d", worker)
			metadata := coreapp.AdminResourceImportMetadata{
				OperatorUserID:     9,
				LongLived:          true,
				ErrorStrategy:      domain.ImportErrorStrategyAbort,
				RequestID:          requestID,
				Path:               "/v1/admin/resources/imports",
				IdempotencyKey:     "admin-import-concurrent-key",
				RequestFingerprint: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			}
			storedByWorker[worker], createdByWorker[worker], errorsByWorker[worker] = repo.CreateAdminWithLog(
				context.Background(),
				&domain.ResourceImport{
					OwnerUserID:     1,
					ResourceType:    domain.ResourceTypeMicrosoft,
					SourceObjectKey: "imports/microsoft/source/admin-concurrent.txt",
					Status:          domain.ResourceImportProcessing,
				},
				metadata,
				&governancedomain.OperationLog{
					OperatorUserID: 9,
					OperationType:  "core.admin_resource.import",
					ResourceType:   "microsoft_resource_import",
					ResourceID:     "pending",
					Path:           metadata.Path,
					Result:         "success",
					SafeSummary:    "Microsoft resource import accepted.",
					RequestID:      requestID,
				},
			)
		}(index)
	}
	close(start)
	workers.Wait()

	createdCount := 0
	importIDs := make(map[uint]struct{}, workerCount)
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if createdByWorker[index] {
			createdCount++
		}
		require.NotNil(t, storedByWorker[index])
		importIDs[storedByWorker[index].ID] = struct{}{}
	}
	require.Equal(t, 1, createdCount)
	require.Len(t, importIDs, 1)

	var imports, logs int64
	require.NoError(t, db.Model(&ResourceImportModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9, "admin-import-concurrent-key").
		Count(&imports).Error)
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ?", "core.admin_resource.import").
		Count(&logs).Error)
	require.EqualValues(t, 1, imports)
	require.EqualValues(t, 1, logs)
}

func TestAdminResourceImportClaimFencingAndResumeItemsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role, enabled)
VALUES
    (1, 'resume-owner@test.local', 'hash', 'supplier', TRUE),
    (9, 'resume-operator@test.local', 'hash', 'admin', TRUE)`).Error)

	repo := NewResourceImportRepo(db)
	stored, created, err := repo.CreateAdminWithLog(context.Background(), &domain.ResourceImport{
		OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft,
		SourceObjectKey: "imports/microsoft/source/resume.txt", Status: domain.ResourceImportProcessing,
	}, coreapp.AdminResourceImportMetadata{
		OperatorUserID: 9, ErrorStrategy: domain.ImportErrorStrategySkip,
		RequestID: "req-resume", Path: "/v1/admin/resources/imports",
		IdempotencyKey: "admin-import-resume", RequestFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}, nil)
	require.NoError(t, err)
	require.True(t, created)

	now := time.Now().UTC()
	dispatchable, err := repo.ClaimAdminImportDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	firstClaim, claimed, err := repo.MarkAdminImportRunning(context.Background(), stored.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)

	exhausted, err := repo.MarkAdminImportRetryableFailure(context.Background(), stored.ID, firstClaim, "Temporary import failure.")
	require.NoError(t, err)
	require.False(t, exhausted)
	require.ErrorIs(t, repo.SetAdminImportCounts(context.Background(), stored.ID, firstClaim, 2, 1), domain.ErrResourceImportInvalidClaim)

	dispatchable, err = repo.ClaimAdminImportDispatchable(context.Background(), 10, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	secondClaim, claimed, err := repo.MarkAdminImportRunning(context.Background(), stored.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEqual(t, firstClaim, secondClaim)
	require.NoError(t, repo.SetAdminImportCounts(context.Background(), stored.ID, secondClaim, 2, 1))

	resourceRepo := NewResourceRepo(db)
	previousRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	previousMicrosoft := &domain.MicrosoftResource{
		EmailAddress: "resume-first@outlook.com", Password: "safe-test-password",
		Status: domain.MicrosoftStatusPending,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), previousRoot, previousMicrosoft))
	previousResourceID := previousRoot.ID
	require.NoError(t, db.Create(&ResourceImportItemModel{
		ImportID: stored.ID, ResourceID: &previousResourceID, LineNumber: 1, Outcome: "imported",
	}).Error)

	lines := []domain.MicrosoftImportLine{{LineNumber: 2, Email: "resume-second@outlook.com"}}
	resources := []domain.EmailResource{{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}}
	microsoft := []domain.MicrosoftResource{{
		EmailAddress: "resume-second@outlook.com", Password: "safe-test-password",
		Status: domain.MicrosoftStatusPending,
	}}
	importedIDs, err := repo.CreateMicrosoftResourcesAndMarkSucceeded(
		context.Background(), stored.ID, secondClaim, lines, resources, microsoft,
		[]coreapp.AdminResourceImportSkippedItem{{LineNumber: 3, Category: "invalid_format", SafeError: "Invalid import entry."}},
		"imports/microsoft/failures/resume.csv", "Skipped 1 import entry.", nil,
	)
	require.NoError(t, err)
	require.Len(t, importedIDs, 1)

	var row ResourceImportModel
	require.NoError(t, db.First(&row, stored.ID).Error)
	require.Equal(t, string(domain.ResourceImportImported), row.Status)
	require.Equal(t, "succeeded", row.DispatchStatus)
	require.Equal(t, 2, row.ImportedCount)
	require.Equal(t, 2, row.AcceptedCount)
	require.Equal(t, 1, row.SkippedCount)
	require.Empty(t, row.ClaimToken)
	require.Equal(t, 2, row.Attempts)

	var items []ResourceImportItemModel
	require.NoError(t, db.Where("import_id = ?", stored.ID).Order("line_number ASC").Find(&items).Error)
	require.Len(t, items, 3)
	require.Equal(t, []int{1, 2, 3}, []int{items[0].LineNumber, items[1].LineNumber, items[2].LineNumber})
	require.Equal(t, []string{"imported", "imported", "skipped"}, []string{items[0].Outcome, items[1].Outcome, items[2].Outcome})

	_, claimed, err = repo.MarkAdminImportRunning(context.Background(), stored.ID, dispatchable[0].DispatchToken)
	require.NoError(t, err)
	require.False(t, claimed, "a consumed dispatch token must not re-enter a terminal import")
}

type serializedImportFileStore struct {
	objectKey     string
	content       []byte
	readObjectKey string
	readCount     int
}

func (*serializedImportFileStore) SavePrivate(context.Context, governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	return nil, fmt.Errorf("unexpected SavePrivate call")
}

func (*serializedImportFileStore) SavePrivateStream(context.Context, governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	return nil, fmt.Errorf("unexpected SavePrivateStream call")
}

func (s *serializedImportFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	s.readObjectKey = objectKey
	s.readCount++
	if objectKey != s.objectKey {
		return nil, fmt.Errorf("unexpected private object key")
	}
	return &governancedomain.PrivateFile{
		ObjectKey:    s.objectKey,
		FileName:     "microsoft-import.txt",
		ContentType:  "text/plain; charset=utf-8",
		ContentBytes: append([]byte(nil), s.content...),
	}, nil
}

func (*serializedImportFileStore) DeletePrivate(context.Context, string) error {
	return fmt.Errorf("unexpected DeletePrivate call")
}

func (*serializedImportFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, fmt.Errorf("unexpected ListPrivate call")
}
