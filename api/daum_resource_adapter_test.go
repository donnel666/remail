package api

import (
	"context"
	"errors"
	"testing"
	"time"

	dauminfra "github.com/donnel666/remail/internal/daum/infra"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDaumFetchedMessagesPreserveRecipientsInsteadOfRequestedAlias(t *testing.T) {
	req := mailmatchapp.FetchMessagesRequest{Scope: mailmatchapp.OrderScope{
		AllocationType: mailmatchdomain.ResourceTypeDaum, EmailResourceID: 7,
		CredentialRevision: 3, Recipient: "one@hanmail2.net",
	}}
	rows := daumFetchedMessages(req, []protocol.Message{
		{ID: "imap:INBOX:1:2", ExternalID: "<id@example.test>", OriginalToCount: 1,
			ToList: []protocol.Address{{Address: "one@hanmail.net"}}, CCList: []protocol.Address{{Address: "two@daum.net"}}},
		{ID: "pop3:3", RawHash: "abcdef"},
	})
	require.Len(t, rows, 2)
	require.Equal(t, "one@hanmail.net", rows[0].Recipient)
	require.Equal(t, []string{"one@hanmail.net", "two@daum.net"}, rows[0].Recipients)
	require.NotContains(t, rows[0].Recipients, req.Scope.Recipient)
	require.Equal(t, "id@example.test", rows[0].MessageIDHeader)
	require.Equal(t, uint64(3), rows[0].CredentialRevision)
	require.Empty(t, rows[1].Recipient)
	require.Empty(t, rows[1].Recipients)
	require.Equal(t, "sha256:abcdef", rows[1].ProviderMessageID)
	require.Equal(t, "pop3", rows[1].Protocol)
}

type daumPickupScopeFunc func(context.Context, string, string) (*mailmatchapp.OrderScope, error)

func (f daumPickupScopeFunc) LoadPickupScope(ctx context.Context, token, email string) (*mailmatchapp.OrderScope, error) {
	return f(ctx, token, email)
}

func TestDaumAuxiliaryBindingUsesExistingPickupAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE domain_resources (id INTEGER PRIMARY KEY, domain TEXT, status TEXT, owner_user_id INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO domain_resources VALUES (42,'relay.example','normal',1)").Error)
	require.NoError(t, db.Exec("CREATE TABLE email_resources (id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources VALUES(42,'domain',1)").Error)
	require.NoError(t, db.Exec("CREATE TABLE domain_allocations (resource_id INTEGER,order_no TEXT,email TEXT,status TEXT)").Error)
	require.NoError(t, db.Exec("INSERT INTO domain_allocations VALUES(42,'existing-order','aux@relay.example','allocated')").Error)
	called := 0
	pickupUsedCallerTx := false
	resolver := daumBindingResolver{db: db, pickup: daumPickupScopeFunc(func(ctx context.Context, token, email string) (*mailmatchapp.OrderScope, error) {
		called++
		_, pickupUsedCallerTx = platform.GormTxFromContext(ctx)
		require.Equal(t, "aux@relay.example", email)
		if token != "valid" {
			return nil, mailmatchdomain.ErrPickupCredentialInvalid
		}
		return &mailmatchapp.OrderScope{OrderNo: "existing-order", UserID: 1, AllocationType: mailmatchdomain.ResourceTypeDomain,
			EmailResourceID: 42, Recipient: email, OrderStatus: "active", ServiceMode: "purchase"}, nil
	})}
	input := dauminfra.BindingInput{OwnerUserID: 1, BindingAddress: "aux@relay.example", PickupURL: "https://remail.example/pickup?email=aux%40relay.example&token=valid"}
	result, err := resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "linked", result.Status)
	require.Equal(t, uint(42), *result.DomainResourceID)
	require.Equal(t, "existing-order", result.OrderNo)
	require.Equal(t, 1, called)
	input.PickupURL = "https://remail.example/pickup?email=aux%40relay.example&token=invalid"
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "unavailable", result.Status)
	require.NotContains(t, result.LastSafeError, "token=")
	input.PickupURL = "https://remail.example/pickup?email=other%40relay.example&token=valid"
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.Status)
	require.Equal(t, 2, called)
	input.PickupURL = ""
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "linked", result.Status)
	input.OwnerUserID = 2
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "unlinked", result.Status)
	input.PickupURL = "https://remail.example/pickup?email=aux%40relay.example&token=valid"
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.Status, "a valid bearer link cannot associate another user's mailbox")
	require.Nil(t, result.DomainResourceID)
	require.Empty(t, result.OrderNo)

	input.OwnerUserID = 1
	rollback := errors.New("rollback fixture")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE domain_resources SET status='disabled' WHERE id=42").Error)
		result, err := resolver.Resolve(platform.WithGormTx(ctx, tx), input)
		require.NoError(t, err)
		require.True(t, pickupUsedCallerTx)
		require.Equal(t, "unlinked", result.Status, "resolution must see the caller's uncommitted changes")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	result, err = resolver.Resolve(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "linked", result.Status)
}

type daumMailboxFunc func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error)

func (f daumMailboxFunc) FetchMailbox(ctx context.Context, id uint, revision uint64, req protocol.FetchRequest) (protocol.FetchResult, error) {
	return f(ctx, id, revision, req)
}

func TestDaumFetchAdapterRequiresRealCompleteHistory(t *testing.T) {
	request := mailmatchapp.FetchMessagesRequest{FullHistory: true, Scope: mailmatchapp.OrderScope{
		AllocationType: mailmatchdomain.ResourceTypeDaum, EmailResourceID: 8, CredentialRevision: 3,
	}}
	adapter := daumMailFetchAdapter{resources: daumMailboxFunc(func(_ context.Context, id uint, _ uint64, req protocol.FetchRequest) (protocol.FetchResult, error) {
		require.Equal(t, uint(8), id)
		require.Empty(t, req.Recipient)
		return protocol.FetchResult{Complete: false, Messages: []protocol.Message{{ReceivedAt: time.Now()}}}, nil
	})}
	_, err := adapter.FetchDaumMessages(context.Background(), request)
	var failure *mailmatchapp.MailFetchFailure
	require.True(t, errors.As(err, &failure))
	require.Equal(t, "incomplete_history", failure.Category)
}

func TestDaumAuxiliaryResolverAcceptsExistingMicrosoftOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	for _, statement := range []string{`CREATE TABLE email_resources(id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)`, `CREATE TABLE microsoft_resources(id INTEGER PRIMARY KEY,status TEXT)`, `CREATE TABLE microsoft_allocations(resource_id INTEGER,order_no TEXT,email TEXT,status TEXT)`, `INSERT INTO email_resources VALUES(55,'microsoft',9)`, `INSERT INTO microsoft_resources VALUES(55,'normal')`, `INSERT INTO microsoft_allocations VALUES(55,'source-order','helper@outlook.com','allocated')`} {
		require.NoError(t, db.Exec(statement).Error)
	}
	resolver := daumBindingResolver{db: db, pickup: daumPickupScopeFunc(func(context.Context, string, string) (*mailmatchapp.OrderScope, error) {
		return &mailmatchapp.OrderScope{UserID: 7, OrderNo: "source-order", AllocationType: mailmatchdomain.ResourceTypeMicrosoft, EmailResourceID: 55, Recipient: "helper@outlook.com", OrderStatus: "active", ServiceMode: "purchase"}, nil
	})}
	result, err := resolver.Resolve(context.Background(), dauminfra.BindingInput{OwnerUserID: 7, BindingAddress: "helper@outlook.com", PickupURL: "https://remail.test/v1/pickup?email=helper@outlook.com&token=fixture-only"})
	require.NoError(t, err)
	require.Equal(t, "linked", result.Status)
	require.EqualValues(t, 55, *result.MailboxResourceID)
	require.Nil(t, result.DomainResourceID)
	require.Equal(t, "source-order", result.OrderNo)
}
