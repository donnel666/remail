package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type protoQueueStub struct{ tasks []*asynq.Task }

func (q *protoQueueStub) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return nil, nil
}

type protoTestAllocation struct {
	ID         uint   `gorm:"primaryKey"`
	ResourceID uint   `gorm:"column:resource_id"`
	Status     string `gorm:"column:status"`
}

func (protoTestAllocation) TableName() string { return "proto_allocations" }

type protoMemoryFiles struct{ objects map[string][]byte }

func (f *protoMemoryFiles) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[file.ObjectKey] = append([]byte(nil), file.ContentBytes...)
	return &governancedomain.StoredPrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, Size: int64(len(file.ContentBytes))}, nil
}
func (f *protoMemoryFiles) SavePrivateStream(ctx context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	return f.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, ContentBytes: content})
}
func (f *protoMemoryFiles) ReadPrivate(_ context.Context, key string) (*governancedomain.PrivateFile, error) {
	return &governancedomain.PrivateFile{ObjectKey: key, ContentBytes: append([]byte(nil), f.objects[key]...)}, nil
}
func (f *protoMemoryFiles) DeletePrivate(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}
func (f *protoMemoryFiles) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

func newProtoAsyncTestService(t *testing.T) (*Service, *protoMemoryFiles) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	files := &protoMemoryFiles{objects: map[string][]byte{}}
	require.NoError(t, db.AutoMigrate(&resourceRoot{}, &Resource{}, &importRecord{}, &importItem{}, &protoTestAllocation{}, &MaintenanceRun{}, &commandReceipt{}, &sessionRecord{}))
	service := NewService(db, files)
	service.Protocol = protoValidationClientStub{}
	return service, files
}

type protoValidationClientStub struct {
	login func(context.Context, proton.LoginRequest) (proton.Session, error)
}

func (client protoValidationClientStub) Login(ctx context.Context, request proton.LoginRequest) (proton.Session, error) {
	if client.login != nil {
		return client.login(ctx, request)
	}
	return testProtoSession(request.Email), nil
}
func (protoValidationClientStub) Fetch(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error) {
	return proton.FetchResult{Complete: true}, nil
}
func testProtoSession(email string) proton.Session {
	return proton.Session{Version: 1, UID: "test-uid", AccessToken: "secret-access", RefreshToken: "secret-refresh", ExpiresAt: time.Now().Add(time.Hour), UserKeys: []string{"secret-user-key"}, Addresses: []proton.AddressKeys{{ID: "test-address", Email: email, PrivateKeys: []string{"secret-address-key"}}}}
}

func TestProtoImportTaskReadsPrivateArtifactAndQueuesOnlyImportedRows(t *testing.T) {
	s, files := newProtoAsyncTestService(t)
	ctx := context.Background()
	content := []byte("First@proto.test----pw\nfirst@proto.test----duplicate\ninvalid\n")
	stored, err := files.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: "proto/source/1.txt", ContentBytes: content})
	require.NoError(t, err)
	id, reused, err := s.CreateImportWithArtifact(ctx, 7, 7, domain.ErrorStrategySkip, "idem-1", stored.ObjectKey, "source.txt", "req-1", content)
	require.NoError(t, err)
	require.False(t, reused)
	ids, err := s.ProcessImportTask(ctx, id, 1)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	status, err := s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.ImportImported, status.Status)
	require.Equal(t, 1, status.ImportedCount)
	require.GreaterOrEqual(t, status.SkippedCount, 2)
	var resource Resource
	require.NoError(t, s.DB.First(&resource).Error)
	require.Equal(t, "first@proto.test", resource.EmailAddress)
	require.Equal(t, domain.StatusPending, resource.Status)
	require.Equal(t, "pw", resource.Password)
}

func TestProtoValidationRequiresUnlockedMailboxKeys(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) { return proton.Session{}, nil }}
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "todo@proto.test", Password: "pw"})
	require.NoError(t, err)
	generation, err := s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	_, err = s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(ctx, protoapp.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: generation, CredentialRevision: 1}))
	item, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusValidationFailed, item.Status)
	require.Contains(t, item.LastSafeError, "invalid_session")
	require.NotEqual(t, domain.StatusNormal, item.Status)
}

func TestProtoDispatchersRecoverFencedStatesWithoutSecrets(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	first, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "validating@proto.test", Password: "secret"})
	require.NoError(t, err)
	gen, err := s.ClaimForValidation(ctx, first, nil)
	require.NoError(t, err)
	second, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "identifying@proto.test", Password: "secret2"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", second).Updates(map[string]any{"status": domain.StatusIdentifying}).Error)
	q := &protoQueueStub{}
	count, err := s.DispatchPendingValidations(ctx, q, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = s.DispatchPendingHistory(ctx, q, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Len(t, q.tasks, 2)
	for _, task := range q.tasks {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(task.Payload(), &fields))
		require.NotContains(t, fields, "password")
		require.NotContains(t, fields, "accessToken")
	}
	var payload protoapp.ValidationTaskPayload
	require.NoError(t, json.Unmarshal(q.tasks[0].Payload(), &payload))
	require.Equal(t, first, payload.ResourceID)
	require.Equal(t, gen, payload.ValidationGeneration)
}

func TestProtoUpdateResourceFencesIdentityAndRejectsBusyAllocation(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "edit@proto.test", Password: "pw"})
	require.NoError(t, err)
	newEmail := "edited@proto.test"
	newOwner := uint(8)
	require.NoError(t, s.UpdateResource(ctx, id, &newEmail, &newOwner))
	item, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, newEmail, item.EmailAddress)
	require.Equal(t, newOwner, item.OwnerUserID)
	require.Equal(t, domain.StatusPending, item.Status)
	root := resourceRoot{}
	require.NoError(t, s.DB.First(&root, id).Error)
	require.Equal(t, newOwner, root.OwnerUserID)
	require.NoError(t, s.DB.Create(&protoTestAllocation{ResourceID: id, Status: "allocated"}).Error)
	require.ErrorIs(t, s.UpdateResource(ctx, id, &newEmail, nil), domain.ErrResourceBusy)
}

func TestProtoRealAdapterFenceStopsAtHistoryBeforeInventory(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "real-fence@proto.test", Password: "pw"})
	require.NoError(t, err)
	generation, err := s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	_, err = s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
	require.NoError(t, err)
	item, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(ctx, protoapp.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: generation, CredentialRevision: item.CredentialRevision, RequestID: "req-real"}))
	item, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, item.Status)
	require.NoError(t, s.CompleteHistorySuccess(ctx, id, generation))
	item, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusNormal, item.Status)
	require.False(t, item.ForSale)
}

func TestProtoMaintenanceRunUsesPortableStateTransitions(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "maintenance@proto.test", Password: "pw"})
	require.NoError(t, err)
	generation, err := s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	_, err = s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
	require.NoError(t, err)
	item, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	run, err := s.FindMaintenanceRun(ctx, id, generation, "validation")
	require.NoError(t, err)
	require.Equal(t, "queued", run.Status)
	require.NoError(t, s.StartMaintenanceRun(ctx, run.ID, id, generation, "validation"))
	require.NoError(t, s.FinishMaintenanceRun(ctx, run.ID, "uncertain", "verification_uncertain"))
	var stored MaintenanceRun
	require.NoError(t, s.DB.First(&stored, run.ID).Error)
	require.Equal(t, 1, stored.Attempts)
	require.Equal(t, "uncertain", stored.Status)
	require.Equal(t, item.CredentialRevision, stored.CredentialRevision)
}

func TestProtoQueriesRequireMatchingUnifiedResourceRoot(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	// A malformed child row with a non-Proto root must never be visible through
	// the Proto API, even though the numeric ID is otherwise valid.
	root := resourceRoot{ID: 900, Type: "microsoft", OwnerUserID: 7, Version: 1}
	require.NoError(t, s.DB.Create(&root).Error)
	require.NoError(t, s.DB.Create(&Resource{ID: 900, ResourceType: "proto", OwnerUserID: 7, EmailAddress: "wrong-root@proto.test", Password: "pw", Status: domain.StatusPending, Version: 1, ValidationGeneration: 1, CredentialRevision: 1}).Error)
	_, err := s.GetResource(ctx, 900, nil)
	require.ErrorIs(t, err, domain.ErrResourceMissing)
	page, err := s.ListResources(ctx, ResourceFilter{Limit: 20})
	require.NoError(t, err)
	for _, item := range page.Items {
		require.NotEqual(t, uint(900), item.ID)
	}
}

func TestProtoCommandPayloadIdempotencyRejectsChangedSecretRequest(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "receipt@proto.test", Password: "old"})
	require.NoError(t, err)
	password := "first"
	cmd := Command{ResourceID: id, Version: 1, Action: "credentials", Password: &password, OperatorUserID: 7, IdempotencyKey: "same-key"}
	result, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Reused)
	result, err = s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Reused)
	password = "second"
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrImportConflict)
	cmd.Action = "publish"
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrImportConflict)
	cmd.ResourceID = id + 1
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrImportConflict)
}

func TestProtoImportItemsExposeOnlySafeOutcomeFields(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.CreateImport(ctx, 7, 7, domain.ErrorStrategySkip, "items-key", []byte("ok@proto.test----pw\ninvalid"))
	require.NoError(t, err)
	ids, err := func() ([]uint, error) {
		if err := s.ProcessImport(ctx, id, 1, 7, domain.ErrorStrategySkip, []byte("ok@proto.test----pw\ninvalid")); err != nil {
			return nil, err
		}
		return s.ListImportResourceIDs(ctx, id)
	}()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	page, err := s.ListImportItems(ctx, id, 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), page.Total)
	require.Len(t, page.Items, 2)
	for _, item := range page.Items {
		require.NotContains(t, item.LastSafeError, "pw")
	}
}
