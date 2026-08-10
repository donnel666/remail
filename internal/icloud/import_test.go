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

type iCloudImportAllocationTestModel struct {
	ID         uint   `gorm:"column:id;primaryKey"`
	ResourceID uint   `gorm:"column:resource_id"`
	Status     string `gorm:"column:status"`
}

func (iCloudImportAllocationTestModel) TableName() string { return "icloud_allocations" }

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

func TestParseICloudImportLineKeepsSeparatorsInsideCookie(t *testing.T) {
	raw := "Main@icloud.com----p119-maildomainws.icloud.com----123----client----build----mastering----X-APPLE-DS-WEB-SESSION-TOKEN=a----inside; X-APPLE-WEBAUTH-USER=b; X-APPLE-WEBAUTH-TOKEN=c"
	line, failure := parseICloudImportLine(7, raw)
	if failure != nil {
		t.Fatalf("parse failure: %#v", failure)
	}
	if line.PrimaryEmail != "main@icloud.com" || line.Cookie != "X-APPLE-DS-WEB-SESSION-TOKEN=a----inside; X-APPLE-WEBAUTH-USER=b; X-APPLE-WEBAUTH-TOKEN=c" {
		t.Fatalf("unexpected parsed line: %#v", line)
	}
	if line.LangCode != "zh-tw" || line.Origin != "https://www.icloud.com" || line.Referer != "https://www.icloud.com/" {
		t.Fatalf("unexpected regional defaults: %#v", line)
	}
}

func TestICloudImportFailuresNeverContainCookie(t *testing.T) {
	const secret = "cookie-secret-value"
	content := "main@icloud.com----not-an-allowed-host----123----client----build----mastering----" + secret
	_, failures, fatal := parseICloudImport(content, coreDomain.ImportErrorStrategySkip)
	if fatal != nil || len(failures) != 1 {
		t.Fatalf("unexpected parse result: failures=%#v fatal=%#v", failures, fatal)
	}
	csv := iCloudImportFailuresCSV(failures)
	if strings.Contains(csv, secret) || strings.Contains(failures[0].SafeMessage, secret) {
		t.Fatalf("cookie leaked into failure output: %q", csv)
	}
}

func TestParseICloudImportRejectsInvalidUTF8(t *testing.T) {
	content := string([]byte("main@icloud.com----p119-maildomainws.icloud.com----123----client----build----mastering----cookie\xff"))
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
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudImportModel{
		ID: 1, OwnerUserID: 7, OperatorUserID: 8, SourceObjectKey: "private/source.txt",
		Status: iCloudImportProcessing, DispatchStatus: "running", Generation: 2, MaxAttempts: iCloudImportMaxAttempts,
		ClaimToken: "stale-claim", StartedAt: ptrICloudTime(now.Add(-iCloudImportRunningLease - time.Second)), UpdatedAt: now.Add(-iCloudImportRunningLease - time.Second),
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

func TestICloudImportAcceptsAndPersistsPrivateSessionWithoutQueueSecrets(t *testing.T) {
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
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudImportModel{}, &iCloudImportItemModel{}, &governanceinfra.OperationLogModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	files := &icloudImportFileStore{}
	service := NewService(db, queue, files)
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	secret := "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	content := []byte("main@icloud.com----p119-maildomainws.icloud.com----123----client----build----mastering----" + secret)
	accepted, reused, err := service.AcceptAdminICloudTXTFile(context.Background(), 9, 7, "icloud.txt", content, coreDomain.ImportErrorStrategySkip, "icloud-flow", "request-icloud", "/v1/admin/icloud/resources/imports")
	if err != nil || reused || accepted == nil {
		t.Fatalf("accept import: view=%#v reused=%v err=%v", accepted, reused, err)
	}
	tasks, err := inspector.ListScheduledTasks(platform.QueueDefault)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("scheduled import task: tasks=%d err=%v", len(tasks), err)
	}
	if strings.Contains(string(tasks[0].Payload), secret) || strings.Contains(string(tasks[0].Payload), "icloud.txt") {
		t.Fatalf("private session material entered task payload: %s", tasks[0].Payload)
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
		t.Fatalf("read iCloud resource: %v", err)
	}
	if resource.Cookie != secret || resource.Status != iCloudResourcePending || !resource.ExpireAt.After(resource.CreatedAt) {
		t.Fatalf("unexpected persisted resource: %#v", resource)
	}
	status, err := service.GetAdminICloudResourceImport(context.Background(), accepted.ImportID)
	if err != nil || status.Status != iCloudImportImported || status.Imported != 1 || status.Accepted != 1 {
		t.Fatalf("unexpected import status: %#v err=%v", status, err)
	}
}

func TestICloudImportRecoversInvalidSessionWithoutExtendingResourceLifetime(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-import-recover?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAliasModel{}, &iCloudImportAllocationTestModel{}, &iCloudImportModel{}, &iCloudImportItemModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	expiresAt := now.Add(12 * time.Hour)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 3, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Host: "p119-maildomainws.icloud.com", DSID: "123",
		ClientID: "old-client", ClientBuildNumber: "old-build", ClientMasteringNumber: "old-mastering", Cookie: "old-cookie",
		SelectedForwardTo: "icloud@aishop6.com",
		ExpireAt:          expiresAt, Status: iCloudResourceAbnormal, SessionStatus: iCloudSessionInvalid,
		CredentialRevision: 4, CredentialUpdatedAt: now.Add(-time.Hour), ValidationGeneration: 8, ValidationFailures: 3,
		DeliveryProbeToken: "old-probe", DeliveryProbeAlias: "old@icloud.com", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create invalid resource: %v", err)
	}
	if err := db.Create(&iCloudAliasModel{
		ID: 21, ResourceID: 1, AnonymousID: "anonymous-21", RecipientMailID: "recipient-21",
		Email: "assigned@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: iCloudResourceNormal,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create assigned alias: %v", err)
	}
	if err := db.Create(&iCloudImportAllocationTestModel{ID: 31, ResourceID: 1, Status: "allocated"}).Error; err != nil {
		t.Fatalf("create active allocation: %v", err)
	}
	files := &icloudImportFileStore{}
	newCookie := "X-APPLE-DS-WEB-SESSION-TOKEN=new-session; X-APPLE-WEBAUTH-USER=new-user; X-APPLE-WEBAUTH-TOKEN=new-token"
	content := []byte("main@icloud.com----p119-maildomainws.icloud.com----123----new-client----new-build----new-mastering----" + newCookie)
	stored, err := files.SavePrivate(context.Background(), governancedomain.PrivateFile{ObjectKey: "recover.txt", ContentBytes: content})
	if err != nil || stored == nil {
		t.Fatalf("store import: %v", err)
	}
	importRow := iCloudImportModel{
		ID: 5, OwnerUserID: 7, OperatorUserID: 9, SourceObjectKey: stored.ObjectKey,
		Status: iCloudImportProcessing, ErrorStrategy: string(coreDomain.ImportErrorStrategyAbort), DispatchStatus: "running",
		Generation: 1, MaxAttempts: 3, ClaimToken: "claim", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&importRow).Error; err != nil {
		t.Fatalf("create import row: %v", err)
	}
	service := NewService(db, nil, files)
	service.now = func() time.Time { return now.Add(time.Hour) }
	if err := service.processICloudImportClaimed(context.Background(), &importRow); err != nil {
		t.Fatalf("recover import: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read recovered resource: %v", err)
	}
	if resource.Cookie != newCookie || resource.ClientID != "new-client" || resource.Status != iCloudResourcePending ||
		resource.SessionStatus != iCloudSessionUnchecked || resource.CredentialRevision != 5 || resource.ValidationGeneration != 9 ||
		!resource.ExpireAt.Equal(expiresAt) || resource.DeliveryProbeToken != "" || resource.SelectedForwardTo != "icloud@aishop6.com" {
		t.Fatalf("unexpected recovered resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.First(&alias, 21).Error; err != nil || alias.RecipientMailID != "recipient-21" || alias.ForwardToEmail != "icloud@aishop6.com" {
		t.Fatalf("credential recovery changed the assigned alias routing fact: alias=%#v err=%v", alias, err)
	}
	var allocation iCloudImportAllocationTestModel
	if err := db.First(&allocation, 31).Error; err != nil || allocation.Status != "allocated" {
		t.Fatalf("credential recovery changed the active allocation: allocation=%#v err=%v", allocation, err)
	}
	var roots int64
	if err := db.Model(&iCloudRootModel{}).Count(&roots).Error; err != nil || roots != 1 {
		t.Fatalf("reimport must reuse root: roots=%d err=%v", roots, err)
	}
}

func TestICloudImportResumesFromImportedLineItems(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-import-resume?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudImportModel{}, &iCloudImportItemModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create processed root: %v", err)
	}
	cookie := "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "first@icloud.com", Host: "p119-maildomainws.icloud.com", DSID: "first-dsid",
		ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
		ExpireAt: now.AddDate(0, 1, 0), Status: iCloudResourcePending, SessionStatus: iCloudSessionUnchecked,
		CredentialRevision: 1, CredentialUpdatedAt: now, ValidationGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create processed resource: %v", err)
	}
	content := strings.Join([]string{
		"first@icloud.com----p119-maildomainws.icloud.com----first-dsid----client----build----mastering----" + cookie,
		"second@icloud.com----p119-maildomainws.icloud.com----second-dsid----client----build----mastering----" + cookie,
	}, "\n")
	files := &icloudImportFileStore{}
	stored, err := files.SavePrivate(context.Background(), governancedomain.PrivateFile{ObjectKey: "resume.txt", ContentBytes: []byte(content)})
	if err != nil {
		t.Fatalf("store source: %v", err)
	}
	record := iCloudImportModel{
		ID: 5, OwnerUserID: 7, OperatorUserID: 9, SourceObjectKey: stored.ObjectKey,
		Status: iCloudImportProcessing, ErrorStrategy: string(coreDomain.ImportErrorStrategySkip), DispatchStatus: "running",
		Generation: 2, ImportedCount: 1, MaxAttempts: 3, ClaimToken: "resume-claim", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create import: %v", err)
	}
	resourceID := uint(1)
	if err := db.Create(&iCloudImportItemModel{ImportID: record.ID, ResourceID: &resourceID, LineNumber: 1, Outcome: "imported", CreatedAt: now}).Error; err != nil {
		t.Fatalf("create processed item: %v", err)
	}
	service := NewService(db, nil, files)
	service.now = func() time.Time { return now.Add(time.Minute) }
	if err := service.processICloudImportClaimed(context.Background(), &record); err != nil {
		t.Fatalf("resume import: %v", err)
	}
	var resources int64
	if err := db.Model(&iCloudResourceModel{}).Count(&resources).Error; err != nil {
		t.Fatalf("count resources: %v", err)
	}
	var finished iCloudImportModel
	if err := db.First(&finished, record.ID).Error; err != nil {
		t.Fatalf("read import: %v", err)
	}
	if resources != 2 || finished.Status != iCloudImportImported || finished.ImportedCount != 2 || finished.SkippedCount != 0 {
		t.Fatalf("unexpected resumed import: resources=%d import=%#v", resources, finished)
	}
}

func ptrICloudTime(value time.Time) *time.Time { return &value }
