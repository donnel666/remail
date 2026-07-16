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

func TestNewMSACLMailboxReaderWithContentWindow(t *testing.T) {
	reader := NewMSACLMailboxReaderWithContentWindow(nil, failingMSACLFileStore{}, 90*24*time.Hour)
	require.Equal(t, 90*24*time.Hour, reader.contentSearchWindow)

	defaulted := NewMSACLMailboxReaderWithContentWindow(nil, failingMSACLFileStore{}, 0)
	require.Equal(t, msaclContentSearchWindow, defaulted.contentSearchWindow)
}

func TestEscapeMSACLLikeTreatsAccountMaskLiterally(t *testing.T) {
	require.Equal(t, `qa!%!_**8\@example.test!!`, escapeMSACLLike(`qa%_**8\@example.test!`))
}

func TestMSACLMailboxReaderListsOnlyRecipientsMatchingMaskMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	parsedAt := now
	for resourceID := uint(9301); resourceID <= 9305; resourceID++ {
		createMicrosoftAliasTestResource(t, db, resourceID, "normal")
	}
	for i, recipient := range []string{
		"xalpha9@recovery.test",
		"xalpha9@recovery.test",
		"xalpha8@recovery.test",
		"yalpha9@recovery.test",
		"xalpha9@other.test",
	} {
		require.NoError(t, db.Create(&InboundMailModel{
			EnvelopeFrom:    "account-security-noreply@accountprotection.microsoft.com",
			Recipient:       recipient,
			ParsedAt:        &parsedAt,
			ResourceID:      uint(9301 + i),
			ResourceType:    "microsoft",
			OwnerUserID:     uint(9301 + i),
			SourceObjectKey: recipient + time.Duration(i).String(),
			Status:          "stored",
			CreatedAt:       now.Add(time.Duration(i) * time.Second),
			UpdatedAt:       now.Add(time.Duration(i) * time.Second),
		}).Error)
	}

	emails, err := NewMSACLMailboxReader(db, failingMSACLFileStore{}).
		ListMasked(context.Background(), "x*****9@recovery.test", 50)

	require.NoError(t, err)
	require.Len(t, emails, 2)
	require.Equal(t, "xalpha9@recovery.test", emails[0].To)
	require.Equal(t, "xalpha9@recovery.test", emails[1].To)
}
