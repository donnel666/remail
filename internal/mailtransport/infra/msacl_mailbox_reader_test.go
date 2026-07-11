package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

type failingMSACLFileStore struct{}

var _ governanceapp.FilePort = failingMSACLFileStore{}

func (failingMSACLFileStore) SavePrivate(context.Context, governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	return nil, errors.New("unavailable")
}

func (failingMSACLFileStore) SavePrivateStream(context.Context, governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	return nil, errors.New("unavailable")
}

func (failingMSACLFileStore) ReadPrivate(context.Context, string) (*governancedomain.PrivateFile, error) {
	return nil, errors.New("unavailable")
}

func (failingMSACLFileStore) DeletePrivate(context.Context, string) error {
	return errors.New("unavailable")
}

func (failingMSACLFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, errors.New("unavailable")
}

func TestMSACLMailboxReaderPreservesRowIdentityWhenObjectIsUnreadable(t *testing.T) {
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	reader := &MSACLMailboxReader{files: failingMSACLFileStore{}}

	emails, err := reader.rowsToEmailObjects(context.Background(), []InboundMailModel{{
		ID:              42,
		EnvelopeFrom:    "account-security-noreply@accountprotection.microsoft.com",
		Recipient:       "proof@example.com",
		SourceObjectKey: "inbound/42.eml",
		Status:          "stored",
		CreatedAt:       createdAt,
	}})

	require.NoError(t, err)
	require.Len(t, emails, 1)
	require.EqualValues(t, 42, emails[0].ID)
	require.Equal(t, "proof@example.com", emails[0].To)
	require.Empty(t, emails[0].Preview)
}
