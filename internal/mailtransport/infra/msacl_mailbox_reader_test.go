package infra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

type failingMSACLFileStore struct{}

type countingMSACLFileStore struct {
	failingMSACLFileStore
	reads int
}

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

func (s *countingMSACLFileStore) ReadPrivate(context.Context, string) (*governancedomain.PrivateFile, error) {
	s.reads++
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

func TestParseMSACLInboundEmailRemovesHTMLMetadataFromPreview(t *testing.T) {
	raw := strings.Join([]string{
		"From: account-security-noreply@accountprotection.microsoft.com",
		"To: proof@example.com",
		"Subject: Microsoft security code",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<a href="https://tracker.example?id=123456">tracking</a>`,
		`<script>999999</script><span>Your security code is:</span>`,
		`<div class="long-formatting-wrapper"><b>654505</b></div>`,
	}, "\r\n")

	email := parseMSACLInboundEmail(InboundMailModel{Recipient: "proof@example.com"}, []byte(raw))

	require.Equal(t, "tracking Your security code is: 654505", email.Preview)
	require.NotContains(t, email.Preview, "123456")
	require.NotContains(t, email.Preview, "999999")
}

func TestParseMSACLInboundEmailKeepsEnvelopeRecipient(t *testing.T) {
	raw := strings.Join([]string{
		"From: account-security-noreply@accountprotection.microsoft.com",
		"To: forged@example.test",
		"Subject: Microsoft security code",
		"",
		"Your security code is 654505",
	}, "\r\n")

	email := parseMSACLInboundEmail(InboundMailModel{Recipient: "actual@recovery.test"}, []byte(raw))

	require.Equal(t, "actual@recovery.test", email.To)
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

func TestMSACLMailboxReaderRejectsMaskWithoutIndexedPrefix(t *testing.T) {
	emails, err := (&MSACLMailboxReader{}).ListMasked(context.Background(), "*****9@recovery.test", msacl.MaskedMailboxQuery{
		Since: time.Now().UTC().Add(-time.Minute),
		Limit: 50,
	})

	require.NoError(t, err)
	require.Empty(t, emails)
}

func TestMSACLMailboxReaderListsOnlyRecipientsMatchingMaskMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	parsedAt := now
	for resourceID := uint(9301); resourceID <= 9306; resourceID++ {
		createMicrosoftAliasTestResource(t, db, resourceID, "normal")
	}
	for i, row := range []struct {
		recipient string
		createdAt time.Time
	}{
		{recipient: "xalpha9@recovery.test", createdAt: now},
		{recipient: "xalpha9@recovery.test", createdAt: now.Add(-time.Minute)},
		{recipient: "xalpha8@recovery.test", createdAt: now},
		{recipient: "yalpha9@recovery.test", createdAt: now},
		{recipient: "xalpha9@other.test", createdAt: now},
		{recipient: "xalpha9@recovery.test", createdAt: now.Add(-2 * time.Hour)},
	} {
		require.NoError(t, db.Create(&InboundMailModel{
			EnvelopeFrom:    "account-security-noreply@accountprotection.microsoft.com",
			Recipient:       row.recipient,
			ParsedAt:        &parsedAt,
			ResourceID:      uint(9301 + i),
			ResourceType:    "microsoft",
			OwnerUserID:     uint(9301 + i),
			SourceObjectKey: row.recipient + time.Duration(i).String(),
			Status:          "stored",
			CreatedAt:       row.createdAt,
			UpdatedAt:       row.createdAt,
		}).Error)
	}

	emails, err := NewMSACLMailboxReaderWithContentWindow(db, failingMSACLFileStore{}, time.Hour).
		ListMasked(context.Background(), "x*****9@recovery.test", msacl.MaskedMailboxQuery{
			Since: now.Add(-time.Hour),
			Limit: 50,
		})

	require.NoError(t, err)
	require.Len(t, emails, 2)
	require.Equal(t, "xalpha9@recovery.test", emails[0].To)
	require.Equal(t, "xalpha9@recovery.test", emails[1].To)
}

func TestMSACLMailboxReaderMaskedSnapshotDoesNotReadRawMailMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	createMicrosoftAliasTestResource(t, db, 9310, "normal")
	row := InboundMailModel{
		EnvelopeFrom:    "account-security-noreply@accountprotection.microsoft.com",
		Recipient:       "metadata9@recovery.test",
		ResourceID:      9310,
		ResourceType:    "microsoft",
		OwnerUserID:     9310,
		SourceObjectKey: "inbound/metadata9.eml",
		Status:          "stored",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, db.Create(&row).Error)
	files := &countingMSACLFileStore{}
	reader := NewMSACLMailboxReader(db, files)
	query := msacl.MaskedMailboxQuery{Since: now.Add(-time.Minute), Limit: 50}

	emails, err := reader.ListMasked(context.Background(), "m*****9@recovery.test", query)
	require.NoError(t, err)
	require.Len(t, emails, 1)
	require.Zero(t, files.reads)

	query.AfterID = uint64(row.ID)
	query.LoadBody = true
	emails, err = reader.ListMasked(context.Background(), "m*****9@recovery.test", query)
	require.NoError(t, err)
	require.Empty(t, emails)
	require.Zero(t, files.reads)

	query.AfterID = 0
	emails, err = reader.ListMasked(context.Background(), "m*****9@recovery.test", query)
	require.NoError(t, err)
	require.Len(t, emails, 1)
	require.Equal(t, 1, files.reads)
}
