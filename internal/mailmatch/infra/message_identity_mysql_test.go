package infra

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestMessageIDCollisionsKeepLegacyFactsAndCrossProtocolReplaysMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_IDENTITY_COLLISION")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	first := domain.Message{EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft, Recipient: "first@example.com", Sender: "sender@example.net", Subject: strings.Repeat("s", 600), RawBody: "original body", DedupeKey: fmt.Sprintf("%064x", 100), MessageIDHeader: "same@example.test", Protocol: "imap", ReceivedAt: time.Now().UTC()}
	first.Recipients = []string{"other@example.com"}
	stored, inserted, err := repo.AppendMessages(ctx, []domain.Message{first})
	require.NoError(t, err)
	require.Equal(t, 1, inserted)
	require.Len(t, stored[0].Subject, 500)
	require.ElementsMatch(t, []string{"first@example.com", "other@example.com"}, stored[0].Recipients)
	legacyID, legacyKey := stored[0].ID, stored[0].DedupeKey
	replayed := first
	replayed.Protocol, replayed.Folder, replayed.ProviderMessageID = "pop3", "INBOX", "pop3:another-uid"
	replayed.ReceivedAt = first.ReceivedAt.Add(-time.Minute)
	stored, inserted, err = repo.AppendMessages(ctx, []domain.Message{replayed})
	require.NoError(t, err)
	require.Zero(t, inserted)
	require.Equal(t, legacyID, stored[0].ID)
	require.Equal(t, legacyKey, stored[0].DedupeKey)
	collision := first
	collision.Recipient, collision.Recipients, collision.RawBody = "second@example.com", nil, "different body"
	stored, inserted, err = repo.AppendMessages(ctx, []domain.Message{collision})
	require.NoError(t, err)
	require.Equal(t, 1, inserted)
	collisionID := stored[0].ID
	require.NotEqual(t, legacyID, collisionID)
	stored, inserted, err = repo.AppendMessages(ctx, []domain.Message{first, collision})
	require.NoError(t, err)
	require.Zero(t, inserted)
	require.Equal(t, legacyID, stored[0].ID)
	require.Equal(t, collisionID, stored[1].ID)
}

func TestConcurrentFirstMessageIDCollisionsVerifyReturnedFactsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_CONCURRENT_IDENTITY_COLLISION")
	repo := NewRepo(db, nil)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			message := domain.Message{EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft, Recipient: fmt.Sprintf("address%d@example.com", index%2), Sender: "sender@example.net", Subject: "same id", RawBody: fmt.Sprintf("body%d", index%2), DedupeKey: fmt.Sprintf("%064x", 101), ReceivedAt: time.Now().UTC()}
			stored, _, err := repo.AppendMessages(context.Background(), []domain.Message{message})
			if err == nil && (len(stored) != 1 || domain.MessageFactFingerprint(stored[0]) != domain.MessageFactFingerprint(message)) {
				err = fmt.Errorf("returned a different original message")
			}
			errors <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, db.Model(&MessageModel{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
