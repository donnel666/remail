package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type domainMailboxFileStore struct {
	objects map[string][]byte
	failKey string
}

func (s *domainMailboxFileStore) SavePrivate(context.Context, governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	return nil, nil
}

func (s *domainMailboxFileStore) SavePrivateStream(context.Context, governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	return nil, nil
}

func (s *domainMailboxFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	if objectKey == s.failKey {
		return nil, errors.New("injected object read failure")
	}
	content, ok := s.objects[objectKey]
	if !ok {
		return nil, errors.New("object not found")
	}
	return &governancedomain.PrivateFile{ObjectKey: objectKey, ContentBytes: content}, nil
}

func (s *domainMailboxFileStore) DeletePrivate(context.Context, string) error { return nil }

func (s *domainMailboxFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

func TestListDomainMailboxMessagesIsolatesMailboxAndBoundsReadMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_DOMAIN_MAILBOX")
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
    (200, 'domain', 1),
    (201, 'domain', 1)`).Error)

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	type inbound struct {
		resourceID uint
		recipient  string
		mailboxKey string
		objectKey  string
		status     string
		createdAt  time.Time
	}
	rows := []inbound{
		{200, "user.name+old@example.com", "username@example.com", "old.eml", "stored", now.Add(-5 * time.Minute)},
		{200, "user.name+mid@example.com", "username@example.com", "mid.eml", "stored", now.Add(-3 * time.Minute)},
		{200, "user.name+new@example.com", "username@example.com", "new.eml", "stored", now.Add(-time.Minute)},
		{200, "user.name+pending@example.com", "username@example.com", "pending.eml", "pending", now},
		{200, "other@example.com", "other@example.com", "other-mailbox.eml", "stored", now},
		{201, "user.name@example.com", "username@example.com", "other-resource.eml", "stored", now},
		{200, "user.name@example.com", "username@example.com", "outside-window.eml", "stored", now.Add(-time.Hour)},
	}
	files := &domainMailboxFileStore{objects: make(map[string][]byte, len(rows))}
	for index, row := range rows {
		require.NoError(t, db.Exec(`
INSERT INTO inbound_mails(
    envelope_from, recipient, mailbox_key, resource_id, resource_type,
    owner_user_id, source_object_key, status, created_at, updated_at
) VALUES ('sender@example.net', ?, ?, ?, 'domain', 1, ?, ?, ?, ?)`,
			row.recipient, row.mailboxKey, row.resourceID, row.objectKey, row.status, row.createdAt, row.createdAt,
		).Error)
		files.objects[row.objectKey] = []byte(fmt.Sprintf(
			"From: Sender <sender@example.net>\r\nTo: forged@example.net\r\nDate: Sat, 01 Jan 2000 00:00:00 +0000\r\nMessage-ID: <domain-%d@example.net>\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody %d",
			index, row.objectKey, index,
		))
	}

	repo := NewRepo(db, files)
	messages, err := repo.ListDomainMailboxMessages(context.Background(), app.OrderScope{
		AllocationType:  domain.ResourceTypeDomain,
		EmailResourceID: 200,
		Recipient:       "USER.NAME+order@EXAMPLE.COM",
	}, now.Add(-10*time.Minute), now, 2)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, []string{"new.eml", "mid.eml"}, []string{messages[0].Subject, messages[1].Subject})
	require.Equal(t, []string{"user.name+new@example.com"}, messages[0].Recipients)
	require.Equal(t, now.Add(-time.Minute), messages[0].ReceivedAt)
	require.Equal(t, now.Add(-3*time.Minute), messages[1].ReceivedAt)

	files.failKey = "new.eml"
	_, err = repo.ListDomainMailboxMessages(context.Background(), app.OrderScope{
		AllocationType:  domain.ResourceTypeDomain,
		EmailResourceID: 200,
		Recipient:       "username@example.com",
	}, now.Add(-10*time.Minute), now, 2)
	require.ErrorContains(t, err, "read domain mailbox message")
	require.ErrorContains(t, err, "injected object read failure")
}
