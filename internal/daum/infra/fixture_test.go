package infra

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type daumQueueStub struct{ tasks []*asynq.Task }

func (q *daumQueueStub) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return nil, nil
}

type daumTestAllocation struct {
	ID         uint   `gorm:"primaryKey"`
	ResourceID uint   `gorm:"column:resource_id"`
	Status     string `gorm:"column:status"`
}

func (daumTestAllocation) TableName() string { return "daum_allocations" }

type daumMemoryFiles struct{ objects map[string][]byte }

func (f *daumMemoryFiles) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[file.ObjectKey] = append([]byte(nil), file.ContentBytes...)
	return &governancedomain.StoredPrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, Size: int64(len(file.ContentBytes))}, nil
}
func (f *daumMemoryFiles) SavePrivateStream(ctx context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	return f.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, ContentBytes: content})
}
func (f *daumMemoryFiles) ReadPrivate(_ context.Context, key string) (*governancedomain.PrivateFile, error) {
	return &governancedomain.PrivateFile{ObjectKey: key, ContentBytes: append([]byte(nil), f.objects[key]...)}, nil
}
func (f *daumMemoryFiles) DeletePrivate(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}
func (f *daumMemoryFiles) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

func newDaumAsyncTestService(t *testing.T) (*Service, *daumMemoryFiles) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	files := &daumMemoryFiles{objects: map[string][]byte{}}
	require.NoError(t, db.AutoMigrate(&resourceRoot{}, &Resource{}, &importRecord{}, &importItem{}, &daumTestAllocation{}, &MaintenanceRun{}, &commandReceipt{}, &ExplicitAlias{}, &BindingMailbox{}))
	service := NewService(db, files)
	service.Protocol = daumValidationClientStub{}
	service.Proxies = daumTestProxyProvider{}
	return service, files
}

type daumValidationClientStub struct {
	validate func(context.Context, protocol.Credentials) error
	fetch    func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error)
}

func (c daumValidationClientStub) Validate(ctx context.Context, credentials protocol.Credentials) error {
	if c.validate != nil {
		return c.validate(ctx, credentials)
	}
	return nil
}
func (c daumValidationClientStub) Fetch(ctx context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
	if c.fetch != nil {
		return c.fetch(ctx, credentials, request)
	}
	return protocol.FetchResult{Complete: true}, nil
}

type daumTestProxyProvider struct{}

func (daumTestProxyProvider) Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	return &proxyapp.ProxyConfig{ID: 1, ProxyServerID: 1, URL: "http://proxy.fixture:3128"}, nil
}
func (daumTestProxyProvider) ReportSuccess(context.Context, uint) error         { return nil }
func (daumTestProxyProvider) ReportFailure(context.Context, uint, string) error { return nil }
