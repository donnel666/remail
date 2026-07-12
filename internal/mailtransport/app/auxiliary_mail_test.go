package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auxiliaryMailRepoStub struct {
	exists bool
	items  []domain.InboundMail
	total  int64
	detail *domain.InboundMail
	err    error
}

func (r *auxiliaryMailRepoStub) MicrosoftResourceExists(context.Context, uint) (bool, error) {
	return r.exists, r.err
}

func (r *auxiliaryMailRepoStub) ListByMicrosoftResource(context.Context, AuxiliaryMailFilter) ([]domain.InboundMail, int64, error) {
	return append([]domain.InboundMail(nil), r.items...), r.total, r.err
}

func (r *auxiliaryMailRepoStub) FindByMicrosoftResource(_ context.Context, resourceID, messageID uint) (*domain.InboundMail, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.detail == nil || r.detail.ResourceID != resourceID || r.detail.ID != messageID {
		return nil, nil
	}
	clone := *r.detail
	return &clone, nil
}

type bindingQueryRepoStub struct {
	bindings map[uint]domain.MicrosoftBindingMailbox
	err      error
}

func (r bindingQueryRepoStub) FindByResourceIDs(context.Context, []uint) (map[uint]domain.MicrosoftBindingMailbox, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[uint]domain.MicrosoftBindingMailbox, len(r.bindings))
	for id, binding := range r.bindings {
		result[id] = binding
	}
	return result, nil
}

type auxiliaryOperationLogStub struct {
	logs []*governancedomain.OperationLog
	err  error
}

func (s *auxiliaryOperationLogStub) Create(_ context.Context, log *governancedomain.OperationLog) error {
	if s.err != nil {
		return s.err
	}
	clone := *log
	s.logs = append(s.logs, &clone)
	return nil
}

func TestAuxiliaryMailListUsesPersistedSummaryWithoutReadingObject(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 12, 10, 30, 0, 0, time.UTC)
	repo := &auxiliaryMailRepoStub{
		exists: true,
		total:  1,
		items: []domain.InboundMail{{
			ID:               44,
			ResourceID:       9,
			ResourceType:     domain.InboundResourceMicrosoft,
			Recipient:        "proof@example.com",
			EnvelopeFrom:     "untrusted-envelope@example.net",
			HeaderFrom:       "account-security-noreply@accountprotection.microsoft.com",
			Subject:          "Microsoft account security code",
			BodyPreview:      "Your security code is 654321.",
			VerificationCode: "654321",
			SourceObjectKey:  "private/do-not-read.eml",
			Status:           domain.InboundStatusStored,
			ReceivedAt:       &receivedAt,
		}},
	}
	files := newFileStoreStub()
	service := NewAuxiliaryMailQueryService(
		repo,
		bindingQueryRepoStub{bindings: map[uint]domain.MicrosoftBindingMailbox{9: {
			ID:             7,
			ResourceID:     9,
			BindingAddress: "proof@example.com",
			Status:         domain.MicrosoftBindingVerified,
			UpdatedAt:      receivedAt.Add(-time.Hour),
		}}},
		files,
		&auxiliaryOperationLogStub{},
		nil,
	)

	page, err := service.List(context.Background(), AuxiliaryMailFilter{ResourceID: 9, Limit: 20})
	require.NoError(t, err)
	require.NotNil(t, page.Binding)
	assert.Equal(t, "proof@example.com", page.Binding.EmailAddress)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "account-security-noreply@accountprotection.microsoft.com", page.Items[0].Sender)
	assert.Equal(t, "654321", *page.Items[0].VerificationCode)
	assert.Equal(t, 0, files.readCount)
}

func TestAuxiliaryMailListReturnsConfiguredEmptyState(t *testing.T) {
	service := NewAuxiliaryMailQueryService(
		&auxiliaryMailRepoStub{exists: true, items: nil},
		bindingQueryRepoStub{bindings: map[uint]domain.MicrosoftBindingMailbox{}},
		newFileStoreStub(),
		&auxiliaryOperationLogStub{},
		nil,
	)

	page, err := service.List(context.Background(), AuxiliaryMailFilter{ResourceID: 11, Limit: 20})
	require.NoError(t, err)
	assert.Nil(t, page.Binding)
	assert.NotNil(t, page.Items)
	assert.Empty(t, page.Items)
}

func TestAuxiliaryMailDetailReadsOneObjectAndWritesSafeAudit(t *testing.T) {
	createdAt := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	repo := &auxiliaryMailRepoStub{detail: &domain.InboundMail{
		ID:              52,
		ResourceID:      9,
		ResourceType:    domain.InboundResourceMicrosoft,
		Recipient:       "proof@example.com",
		SourceObjectKey: "private/secret-object-key.eml",
		Status:          domain.InboundStatusStored,
		CreatedAt:       createdAt,
	}}
	files := newFileStoreStub()
	files.files[repo.detail.SourceObjectKey] = governancedomain.PrivateFile{
		ObjectKey: repo.detail.SourceObjectKey,
		ContentBytes: []byte("From: Microsoft Security <account-security-noreply@accountprotection.microsoft.com>\r\n" +
			"Subject: Microsoft account security code\r\n" +
			"Content-Type: text/html; charset=utf-8\r\n\r\n" +
			"<html><script>steal()</script><body>Your security code is <b>123456</b>.</body></html>"),
	}
	logs := &auxiliaryOperationLogStub{}
	service := NewAuxiliaryMailQueryService(repo, bindingQueryRepoStub{}, files, logs, nil)

	detail, err := service.Get(context.Background(), AuxiliaryMailDetailRequest{
		ResourceID:     9,
		MessageID:      52,
		OperatorUserID: 3,
		RequestID:      "request-52",
		Path:           "/v1/admin/bindings/messages/:messageId",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, files.readCount)
	assert.Equal(t, "Your security code is 123456 .", detail.Body)
	assert.Equal(t, "123456", *detail.VerificationCode)
	assert.NotContains(t, detail.Body, "steal")
	require.Len(t, logs.logs, 1)
	assert.Equal(t, "mailtransport.auxiliary_message.read", logs.logs[0].OperationType)
	assert.Equal(t, "9:52", logs.logs[0].ResourceID)
	assert.NotContains(t, logs.logs[0].SafeSummary, "123456")
	assert.NotContains(t, logs.logs[0].SafeSummary, "secret-object-key")
}

func TestAuxiliaryMailDetailCrossResourceReturnsSameSafeNotFoundWithoutObjectRead(t *testing.T) {
	repo := &auxiliaryMailRepoStub{detail: &domain.InboundMail{
		ID:              52,
		ResourceID:      9,
		SourceObjectKey: "private/secret-object-key.eml",
	}}
	files := newFileStoreStub()
	logs := &auxiliaryOperationLogStub{}
	service := NewAuxiliaryMailQueryService(repo, bindingQueryRepoStub{}, files, logs, nil)

	_, err := service.Get(context.Background(), AuxiliaryMailDetailRequest{
		ResourceID:     10,
		MessageID:      52,
		OperatorUserID: 3,
	})
	require.ErrorIs(t, err, domain.ErrAuxiliaryMessageNotFound)
	assert.Zero(t, files.readCount)
	assert.Empty(t, logs.logs)
}

func TestAuxiliaryMailDetailNeverReturnsMalformedRawMessage(t *testing.T) {
	repo := &auxiliaryMailRepoStub{detail: &domain.InboundMail{
		ID:              53,
		ResourceID:      9,
		Recipient:       "proof@example.com",
		SourceObjectKey: "private/malformed.eml",
		Status:          domain.InboundStatusStored,
		CreatedAt:       time.Now().UTC(),
	}}
	files := newFileStoreStub()
	files.files[repo.detail.SourceObjectKey] = governancedomain.PrivateFile{
		ObjectKey:    repo.detail.SourceObjectKey,
		ContentBytes: []byte("not-rfc822 raw-password=super-secret refresh-token=do-not-return"),
	}
	service := NewAuxiliaryMailQueryService(repo, bindingQueryRepoStub{}, files, &auxiliaryOperationLogStub{}, nil)

	detail, err := service.Get(context.Background(), AuxiliaryMailDetailRequest{
		ResourceID:     9,
		MessageID:      53,
		OperatorUserID: 3,
	})
	require.NoError(t, err)
	assert.Empty(t, detail.Body)
	require.NotNil(t, detail.MatchDiagnostic)
	assert.Equal(t, "Message content could not be parsed.", *detail.MatchDiagnostic)
	assert.NotContains(t, detail.Body, "super-secret")
}

func TestAuxiliaryMailDetailDoesNotReturnBodyWhenAuditFails(t *testing.T) {
	repo := &auxiliaryMailRepoStub{detail: &domain.InboundMail{
		ID:              54,
		ResourceID:      9,
		Recipient:       "proof@example.com",
		SourceObjectKey: "private/message.eml",
		Status:          domain.InboundStatusStored,
		CreatedAt:       time.Now().UTC(),
	}}
	files := newFileStoreStub()
	files.files[repo.detail.SourceObjectKey] = governancedomain.PrivateFile{
		ObjectKey:    repo.detail.SourceObjectKey,
		ContentBytes: []byte("Subject: security code\r\n\r\nSecurity code 112233"),
	}
	service := NewAuxiliaryMailQueryService(
		repo,
		bindingQueryRepoStub{},
		files,
		&auxiliaryOperationLogStub{err: errors.New("database unavailable")},
		nil,
	)

	_, err := service.Get(context.Background(), AuxiliaryMailDetailRequest{
		ResourceID:     9,
		MessageID:      54,
		OperatorUserID: 3,
	})
	require.ErrorIs(t, err, domain.ErrAuxiliaryMailUnavailable)
}

func TestAuxiliaryMailListRejectsUnboundedSearch(t *testing.T) {
	service := NewAuxiliaryMailQueryService(
		&auxiliaryMailRepoStub{exists: true},
		bindingQueryRepoStub{},
		newFileStoreStub(),
		&auxiliaryOperationLogStub{},
		nil,
	)

	_, err := service.List(context.Background(), AuxiliaryMailFilter{
		ResourceID: 9,
		Search:     strings.Repeat("界", AuxiliaryMailMaxSearch+1),
		Limit:      20,
	})
	require.ErrorIs(t, err, domain.ErrInvalidAuxiliaryMailQuery)
}
