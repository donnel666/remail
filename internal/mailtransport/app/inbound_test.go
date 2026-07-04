package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inboundRepoStub struct {
	mu     sync.Mutex
	nextID uint
	mails  map[uint]*domain.InboundMail
}

func newInboundRepoStub() *inboundRepoStub {
	return &inboundRepoStub{nextID: 1, mails: make(map[uint]*domain.InboundMail)}
}

func (r *inboundRepoStub) CreateMany(_ context.Context, mails []domain.InboundMail) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range mails {
		mails[i].ID = r.nextID
		r.nextID++
		clone := mails[i]
		r.mails[clone.ID] = &clone
	}
	return nil
}

func (r *inboundRepoStub) FindByID(_ context.Context, id uint) (*domain.InboundMail, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mail, ok := r.mails[id]; ok {
		clone := *mail
		return &clone, nil
	}
	return nil, nil
}

func (r *inboundRepoStub) ClaimProcessing(_ context.Context, id uint) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mail, ok := r.mails[id]
	if !ok {
		return false, errors.New("not found")
	}
	if mail.Status != domain.InboundStatusPending {
		return false, nil
	}
	mail.Status = domain.InboundStatusProcessing
	mail.FailureReason = ""
	return true, nil
}

func (r *inboundRepoStub) ClaimDispatchable(_ context.Context, limit int, staleBefore time.Time) ([]domain.InboundMail, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	mails := make([]domain.InboundMail, 0, len(r.mails))
	for _, mail := range r.mails {
		if mail.Status == domain.InboundStatusPending ||
			(mail.Status == domain.InboundStatusProcessing && mail.UpdatedAt.Before(staleBefore)) {
			mails = append(mails, *mail)
		}
	}
	if len(mails) > limit {
		mails = mails[:limit]
	}
	return mails, nil
}

func (r *inboundRepoStub) MarkPending(_ context.Context, id uint, safeError string) error {
	return r.update(id, domain.InboundStatusPending, safeError)
}

func (r *inboundRepoStub) MarkStored(_ context.Context, id uint) error {
	return r.update(id, domain.InboundStatusStored, "")
}

func (r *inboundRepoStub) MarkFailed(_ context.Context, id uint, safeError string) error {
	return r.update(id, domain.InboundStatusFailed, safeError)
}

func (r *inboundRepoStub) update(id uint, status domain.InboundStatus, safeError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	mail, ok := r.mails[id]
	if !ok {
		return errors.New("not found")
	}
	mail.Status = status
	mail.FailureReason = safeError
	return nil
}

type inboundResolverStub struct {
	recipient *domain.InboundRecipient
	err       error
}

func (r inboundResolverStub) ResolveInboundRecipient(_ context.Context, _ string) (*domain.InboundRecipient, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.recipient, nil
}

type fileStoreStub struct {
	files map[string]governancedomain.PrivateFile
}

func newFileStoreStub() *fileStoreStub {
	return &fileStoreStub{files: make(map[string]governancedomain.PrivateFile)}
}

func (s *fileStoreStub) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	s.files[file.ObjectKey] = file
	return &governancedomain.StoredPrivateFile{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Size:        int64(len(file.ContentBytes)),
	}, nil
}

func (s *fileStoreStub) SavePrivateStream(_ context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	s.files[file.ObjectKey] = governancedomain.PrivateFile{
		ObjectKey:    file.ObjectKey,
		FileName:     file.FileName,
		ContentType:  file.ContentType,
		ContentBytes: content,
	}
	return &governancedomain.StoredPrivateFile{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Size:        int64(len(content)),
	}, nil
}

func (s *fileStoreStub) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	file, ok := s.files[objectKey]
	if !ok {
		return nil, errors.New("missing object")
	}
	return &file, nil
}

type inboundQueueStub struct {
	tasks      []InboundProcessTask
	dispatches []time.Duration
	err        error
}

func (q *inboundQueueStub) EnqueueInboundProcess(_ context.Context, task InboundProcessTask) error {
	if q.err != nil {
		return q.err
	}
	q.tasks = append(q.tasks, task)
	return nil
}

func (q *inboundQueueStub) EnqueueInboundDispatch(_ context.Context, delay time.Duration) error {
	if q.err != nil {
		return q.err
	}
	q.dispatches = append(q.dispatches, delay)
	return nil
}

func TestInboundServiceAcceptStoresRawMailAndEnqueuesPerRecipient(t *testing.T) {
	repo := newInboundRepoStub()
	files := newFileStoreStub()
	queue := &inboundQueueStub{}
	service := NewInboundService(repo, inboundResolverStub{}, files, queue, nil)
	service.now = fixedClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))

	mails, err := service.Accept(context.Background(), InboundRawMessage{
		EnvelopeFrom: "Sender@Example.COM",
		Recipients: []domain.InboundRecipient{
			{Email: "a@test.com", ResourceID: 10, ResourceType: domain.InboundResourceDomain, OwnerUserID: 1},
			{Email: "b@test.com", ResourceID: 10, ResourceType: domain.InboundResourceDomain, OwnerUserID: 1},
		},
		ContentBytes: []byte("Subject: hi\r\n\r\nbody"),
	})
	require.NoError(t, err)
	require.Len(t, mails, 2)
	require.Len(t, queue.tasks, 2)
	assert.Equal(t, "sender@example.com", mails[0].EnvelopeFrom)
	assert.Equal(t, mails[0].SourceObjectKey, mails[1].SourceObjectKey)
	assert.Contains(t, mails[0].SourceObjectKey, "mailtransport/inbound/2026/07/03/")
	_, ok := files.files[mails[0].SourceObjectKey]
	assert.True(t, ok)
}

func TestInboundServiceProcessMarksStoredWhenObjectReadable(t *testing.T) {
	repo := newInboundRepoStub()
	files := newFileStoreStub()
	queue := &inboundQueueStub{}
	service := NewInboundService(repo, inboundResolverStub{}, files, queue, nil)

	mails, err := service.Accept(context.Background(), InboundRawMessage{
		EnvelopeFrom: "sender@example.com",
		Recipients:   []domain.InboundRecipient{{Email: "a@test.com", ResourceID: 10, ResourceType: domain.InboundResourceDomain, OwnerUserID: 1}},
		ContentBytes: []byte("Subject: hi\r\n\r\nbody"),
	})
	require.NoError(t, err)

	require.NoError(t, service.Process(context.Background(), queue.tasks[0], false))

	stored, err := repo.FindByID(context.Background(), mails[0].ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, domain.InboundStatusStored, stored.Status)
}

func TestInboundServiceRejectsUnknownRecipient(t *testing.T) {
	service := NewInboundService(
		newInboundRepoStub(),
		inboundResolverStub{err: domain.ErrInboundRecipientRejected},
		newFileStoreStub(),
		&inboundQueueStub{},
		nil,
	)

	_, err := service.ResolveRecipient(context.Background(), "nobody@example.com")
	require.ErrorIs(t, err, domain.ErrInboundRecipientRejected)
}

func TestInboundServiceAcceptRejectsInvalidResolvedRecipient(t *testing.T) {
	service := NewInboundService(
		newInboundRepoStub(),
		inboundResolverStub{},
		newFileStoreStub(),
		&inboundQueueStub{},
		nil,
	)

	_, err := service.Accept(context.Background(), InboundRawMessage{
		EnvelopeFrom: "sender@example.com",
		Recipients:   []domain.InboundRecipient{{Email: "a@test.com", ResourceID: 10, OwnerUserID: 1}},
		ContentBytes: []byte("Subject: hi\r\n\r\nbody"),
	})
	require.ErrorIs(t, err, domain.ErrInboundRecipientRejected)
}

func TestInboundServiceAcceptKeepsPendingAndLogsWhenQueueUnavailable(t *testing.T) {
	repo := newInboundRepoStub()
	logs := &systemLogStub{}
	service := NewInboundService(
		repo,
		inboundResolverStub{},
		newFileStoreStub(),
		&inboundQueueStub{err: errors.New("redis unavailable")},
		logs,
	)

	mails, err := service.Accept(context.Background(), InboundRawMessage{
		EnvelopeFrom: "sender@example.com",
		Recipients:   []domain.InboundRecipient{{Email: "a@test.com", ResourceID: 10, ResourceType: domain.InboundResourceDomain, OwnerUserID: 1}},
		ContentBytes: []byte("Subject: hi\r\n\r\nbody"),
	})
	require.NoError(t, err)
	require.Len(t, mails, 1)
	require.Len(t, logs.logs, 1)
	assert.Equal(t, "mail.inbound_enqueue_failed", logs.logs[0].EventType)

	stored, err := repo.FindByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, domain.InboundStatusPending, stored.Status)
}
