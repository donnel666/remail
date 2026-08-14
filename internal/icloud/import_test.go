package icloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coreDomain "github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type icloudImportFileStore struct {
	files map[string]governancedomain.PrivateFile
}

func (s *icloudImportFileStore) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	if s.files == nil {
		s.files = make(map[string]governancedomain.PrivateFile)
	}
	file.ContentBytes = append([]byte(nil), file.ContentBytes...)
	s.files[file.ObjectKey] = file
	return &governancedomain.StoredPrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, Size: int64(len(file.ContentBytes))}, nil
}

func (s *icloudImportFileStore) SavePrivateStream(ctx context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	return s.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, ContentBytes: content})
}

func (s *icloudImportFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file, ok := s.files[objectKey]
	if !ok {
		return nil, fmt.Errorf("private file %q not found", objectKey)
	}
	file.ContentBytes = append([]byte(nil), file.ContentBytes...)
	return &file, nil
}

func (s *icloudImportFileStore) DeletePrivate(_ context.Context, objectKey string) error {
	delete(s.files, objectKey)
	return nil
}

func (*icloudImportFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

func TestICloudImportFailuresNeverContainCredentials(t *testing.T) {
	const appPassword = "app-password-secret"
	const cookie = "cookie-secret-value"
	content := "main@icloud.com----" + appPassword + "----curl 'https://evil.example/account/manage/' -H 'Cookie: " + cookie + "'"
	_, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategySkip)
	if fatal != nil || len(failures) != 1 {
		t.Fatalf("unexpected parse result: failures=%#v fatal=%#v", failures, fatal)
	}
	csv := iCloudImportFailuresCSV(failures)
	if strings.Contains(csv, appPassword) || strings.Contains(csv, cookie) {
		t.Fatalf("credentials leaked into failure output: %q", csv)
	}
}

func TestParseICloudImportRejectsInvalidUTF8(t *testing.T) {
	content := string(append([]byte("main@icloud.com----app-password----"+testICloudOldCurl), 0xff))
	_, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategySkip)
	if len(failures) != 0 || fatal == nil || fatal.Category != "invalid_format" {
		t.Fatalf("unexpected invalid UTF-8 result: failures=%#v fatal=%#v", failures, fatal)
	}
}

func TestICloudImportDispatcherRecoversStaleLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-import-stale?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudImportModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	startedAt := now.Add(-iCloudImportRunningLease - time.Second)
	if err := db.Create(&iCloudImportModel{
		ID: 1, OwnerUserID: 7, OperatorUserID: 8, SourceObjectKey: "private/source.txt",
		Status: iCloudImportProcessing, DispatchStatus: "running", Generation: 2, MaxAttempts: iCloudImportMaxAttempts,
		ClaimToken: "stale-claim", StartedAt: &startedAt, UpdatedAt: startedAt,
	}).Error; err != nil {
		t.Fatalf("create stale import: %v", err)
	}
	service := NewService(db, nil, nil)
	if err := service.recoverStaleICloudImports(context.Background(), now); err != nil {
		t.Fatalf("recover stale import: %v", err)
	}
	var record iCloudImportModel
	if err := db.First(&record, 1).Error; err != nil {
		t.Fatalf("read import: %v", err)
	}
	if record.DispatchStatus != "pending" || record.Generation != 3 || record.ClaimToken != "" {
		t.Fatalf("unexpected recovered import state: %#v", record)
	}
}

func TestICloudImportPersistsIMAPAndBothChannelsWithoutQueueSecrets(t *testing.T) {
	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = inspector.Close()
		_ = queue.Close()
	})
	db, err := gorm.Open(sqlite.Open("file:icloud-import-flow?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{},
		&iCloudImportModel{}, &iCloudImportItemModel{}, &governanceinfra.OperationLogModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	files := &icloudImportFileStore{}
	service := NewService(db, queue, files)
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	const appPassword = "app-password-secret"
	content := []byte("main@icloud.com----" + appPassword + "----" + testICloudNewCurl + "----" + testICloudOldCurl)
	expireAt := now.AddDate(0, 2, 0)
	accepted, reused, err := service.AcceptAdminICloudTXTFile(
		context.Background(), 9, 7, "icloud.txt", content, coreDomain.ImportErrorStrategySkip,
		expireAt, "icloud-flow", "request-icloud", "/v1/admin/icloud/resources/imports",
	)
	if err != nil || reused || accepted == nil {
		t.Fatalf("accept import: view=%#v reused=%v err=%v", accepted, reused, err)
	}
	tasks, err := inspector.ListScheduledTasks(platform.QueueDefault)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("scheduled import task: tasks=%d err=%v", len(tasks), err)
	}
	for _, secret := range []string{appPassword, "myacinfo=secret", testICloudOldCookie, testICloudFDClientInfo, "icloud.txt"} {
		if strings.Contains(string(tasks[0].Payload), secret) {
			t.Fatalf("private credential entered task payload: %s", tasks[0].Payload)
		}
	}
	var task iCloudImportTask
	if err := json.Unmarshal(tasks[0].Payload, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if err := service.ProcessICloudImport(context.Background(), task); err != nil {
		t.Fatalf("process import: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.IMAPAppPassword != appPassword || resource.Status != iCloudResourcePending ||
		!resource.ExpireAt.Equal(expireAt) || resource.NextValidationAt == nil || resource.NextProvisionAt != nil {
		t.Fatalf("unexpected persisted resource: %#v", resource)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", resource.ID).Order("kind").Find(&channels).Error; err != nil {
		t.Fatalf("read channels: %v", err)
	}
	if len(channels) != 2 || channels[0].Kind != iCloudChannelAppleAccount || channels[1].Kind != iCloudChannelWeb ||
		channels[0].FDClientInfo != testICloudFDClientInfo || channels[0].SessionStatus != iCloudSessionUnchecked ||
		channels[1].SessionStatus != iCloudSessionUnchecked {
		t.Fatalf("unexpected channels: %#v", channels)
	}
	status, err := service.GetAdminICloudResourceImport(context.Background(), accepted.ImportID)
	if err != nil || status.Status != iCloudImportImported || status.Imported != 1 || status.Accepted != 1 {
		t.Fatalf("unexpected import status: %#v err=%v", status, err)
	}
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"status": iCloudResourceDisabled, "alias_provision_candidate": "stale@icloud.com", "alias_provision_reconcile": true,
	}).Error; err != nil {
		t.Fatalf("seed stale provision candidate: %v", err)
	}
	now = now.Add(time.Minute)
	rotatedContent := []byte("main@icloud.com----rotated-app-password----" + strings.Replace(testICloudNewCurl, "myacinfo=secret", "myacinfo=rotated", 1))
	rotatedExpireAt := expireAt.AddDate(0, 1, 0)
	rotated, reused, err := service.AcceptAdminICloudTXTFile(
		context.Background(), 9, 7, "icloud-rotated.txt", rotatedContent, coreDomain.ImportErrorStrategySkip,
		rotatedExpireAt, "icloud-flow-rotated", "request-icloud-rotated", "/v1/admin/icloud/resources/imports",
	)
	if err != nil || reused || rotated == nil {
		t.Fatalf("accept rotated import: view=%#v reused=%v err=%v", rotated, reused, err)
	}
	var rotatedRecord iCloudImportModel
	if err := db.First(&rotatedRecord, rotated.ImportID).Error; err != nil {
		t.Fatalf("read rotated import: %v", err)
	}
	if err := service.ProcessICloudImport(context.Background(), iCloudImportTask{ImportID: rotatedRecord.ID, Generation: rotatedRecord.Generation}); err != nil {
		t.Fatalf("process rotated import: %v", err)
	}
	resourceID := resource.ID
	resource = iCloudResourceModel{}
	if err := db.First(&resource, resourceID).Error; err != nil {
		t.Fatalf("read rotated resource: %v", err)
	}
	if resource.IMAPAppPassword != "rotated-app-password" || resource.Status != iCloudResourceDisabled ||
		resource.NextValidationAt != nil || resource.AliasProvisionCandidate != "" || resource.AliasProvisionReconcile ||
		!resource.ExpireAt.Equal(rotatedExpireAt) {
		t.Fatalf("rotated import retained stale provisioning state: %#v", resource)
	}
	channels = nil
	if err := db.Where("resource_id = ?", resource.ID).Find(&channels).Error; err != nil {
		t.Fatalf("read rotated channels: %v", err)
	}
	if len(channels) != 1 || channels[0].Kind != iCloudChannelAppleAccount || !strings.Contains(channels[0].Cookie, "rotated") {
		t.Fatalf("rotated import did not replace channels: %#v", channels)
	}

	now = expireAt.Add(time.Hour)
	replayed, reused, err := service.AcceptAdminICloudTXTFile(
		context.Background(), 9, 7, "icloud.txt", content, coreDomain.ImportErrorStrategySkip,
		expireAt, "icloud-flow", "request-icloud-retry", "/v1/admin/icloud/resources/imports",
	)
	if err != nil || !reused || replayed.ImportID != accepted.ImportID {
		t.Fatalf("replay import: view=%#v reused=%v err=%v", replayed, reused, err)
	}
}
