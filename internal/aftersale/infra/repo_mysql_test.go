package infra

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var aftersaleMySQLTestServer = testmysql.New("remail_aftersale_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = aftersaleMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newAftersaleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return aftersaleMySQLTestServer.Database(t, aftersaleMigrationsDir(t))
}

func aftersaleMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func userMessage(content string, attachments ...aftersaleapp.AttachmentInsert) aftersaleapp.MessageInsert {
	return aftersaleapp.MessageInsert{
		SenderType:   domain.SenderTypeUser,
		SenderUserID: 7,
		SenderEmail:  "u@example.com",
		Content:      content,
		Attachments:  attachments,
	}
}

func TestRepoCreateAndGetMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()

	ticket, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo:        "AS1",
		TicketType:      domain.TicketTypeGeneral,
		Title:           "help me",
		RequesterUserID: 7,
		ReplyToken:      "tok123abc",
		FirstMessage:    userMessage("hi", aftersaleapp.AttachmentInsert{AttachmentNo: "AA1", ObjectKey: "k1", Mime: "image/png", Size: 10}),
	})
	require.NoError(t, err)
	require.Equal(t, domain.TicketStatusOpen, ticket.Status)
	require.Equal(t, 1, ticket.PlatformUnreadCount)
	require.Equal(t, 0, ticket.RequesterUnreadCount)
	require.Equal(t, "tok123abc", ticket.ReplyToken)
	require.Equal(t, "hi", ticket.LastMessagePreview)
	require.Len(t, ticket.Messages, 1)
	require.Equal(t, domain.SenderTypeUser, ticket.Messages[0].SenderType)
	require.Len(t, ticket.Messages[0].Attachments, 1)
	require.Equal(t, "AA1", ticket.Messages[0].Attachments[0].AttachmentNo)

	_, err = repo.Get(ctx, "MISSING", false)
	require.ErrorIs(t, err, domain.ErrTicketNotFound)
}

func TestRepoOrderSnapshotMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()

	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo:        "AS2",
		TicketType:      domain.TicketTypeOrder,
		Title:           "order issue",
		RequesterUserID: 7,
		Order: &domain.OrderSnapshot{
			OrderNo: "OR1", ProjectName: "Telegram", DeliveryEmail: "a@b.com",
			PayAmount: "6.800000", ServiceMode: "code",
		},
		FirstMessage: userMessage("m"),
	})
	require.NoError(t, err)

	got, err := repo.Get(ctx, "AS2", false)
	require.NoError(t, err)
	require.NotNil(t, got.Order)
	require.Equal(t, "OR1", got.Order.OrderNo)
	require.Equal(t, "Telegram", got.Order.ProjectName)
	require.Equal(t, "6.800000", got.Order.PayAmount)
	require.Equal(t, "code", got.Order.ServiceMode)
}

func TestRepoReplyTransitionsMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()
	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo: "AS3", TicketType: domain.TicketTypeGeneral, Title: "t", RequesterUserID: 7, FirstMessage: userMessage("hi"),
	})
	require.NoError(t, err)

	// Platform reply: open -> processing, requester gets an unread, platform cleared.
	updated, err := repo.Reply(ctx, aftersaleapp.ReplyParams{
		TicketNo: "AS3",
		Message:  aftersaleapp.MessageInsert{SenderType: domain.SenderTypePlatform, SenderName: "客服", Content: "hello"},
	})
	require.NoError(t, err)
	require.Equal(t, domain.TicketStatusProcessing, updated.Status)
	require.Equal(t, 1, updated.RequesterUnreadCount)
	require.Equal(t, 0, updated.PlatformUnreadCount)

	// User reply: platform gets an unread, requester cleared, status stays.
	updated, err = repo.Reply(ctx, aftersaleapp.ReplyParams{TicketNo: "AS3", Message: userMessage("ok")})
	require.NoError(t, err)
	require.Equal(t, domain.TicketStatusProcessing, updated.Status)
	require.Equal(t, 0, updated.RequesterUnreadCount)
	require.Equal(t, 1, updated.PlatformUnreadCount)
	require.Len(t, updated.Messages, 3)
}

func TestRepoInboundReplyReceiptIsIdempotentMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()
	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo: "ASINBOUND", TicketType: domain.TicketTypeGeneral, Title: "t", RequesterUserID: 7,
		FirstMessage: userMessage("hi"),
	})
	require.NoError(t, err)

	params := aftersaleapp.ReplyParams{TicketNo: "ASINBOUND", Message: userMessage("email reply")}
	updated, created, err := repo.ReplyInbound(ctx, 501, params)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, updated.Messages, 2)

	updated, created, err = repo.ReplyInbound(ctx, 501, params)
	require.NoError(t, err)
	require.False(t, created)
	require.Nil(t, updated)

	require.NoError(t, repo.IgnoreInboundReply(ctx, 502, "ASINBOUND"))
	require.NoError(t, repo.IgnoreInboundReply(ctx, 502, "ASINBOUND"))
	updated, created, err = repo.ReplyInbound(ctx, 502, params)
	require.NoError(t, err)
	require.False(t, created)
	require.Nil(t, updated)

	var receiptCount int64
	require.NoError(t, repo.db.Model(&InboundReceiptModel{}).Count(&receiptCount).Error)
	require.Equal(t, int64(2), receiptCount)
	ticket, err := repo.Get(ctx, "ASINBOUND", true)
	require.NoError(t, err)
	require.Len(t, ticket.Messages, 2)
}

type inboundMailboxFileStore struct {
	content map[string][]byte
}

func (*inboundMailboxFileStore) Save(context.Context, string, string, string, []byte) error {
	return nil
}

func (s *inboundMailboxFileStore) Read(_ context.Context, objectKey string) (string, []byte, error) {
	return "message/rfc822", s.content[objectKey], nil
}

func TestInboundMailboxScopesByTicketRecipientMySQL(t *testing.T) {
	db := newAftersaleTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Exec(`INSERT INTO users(id, email, password_hash) VALUES (1, 'mailbox-owner@example.com', 'test')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id) VALUES (1, 'domain', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO inbound_mails(
    envelope_from, recipient, mailbox_key, resource_id, resource_type,
    owner_user_id, source_object_key, status, created_at, updated_at
) VALUES
    ('customer@example.com', 'support+asmail-tok@tickets.example.com', 'support@tickets.example.com', 1, 'domain', 1, 'mail/asmail.eml', 'stored', ?, ?),
    ('other@example.com', 'support+asother-tok@tickets.example.com', 'support@tickets.example.com', 1, 'domain', 1, 'mail/other.eml', 'stored', ?, ?)`,
		now, now, now, now).Error)
	files := &inboundMailboxFileStore{content: map[string][]byte{
		"mail/asmail.eml": []byte("From: customer@example.com\r\n\r\nreply"),
		"mail/other.eml":  []byte("From: other@example.com\r\n\r\nother"),
	}}
	mailbox := NewInboundMailbox(db, files, aftersaleapp.TicketMailConfig{
		ReplyLocalPart: "support", ReplyDomain: "tickets.example.com", ReplySecret: "secret",
	})

	messages, err := mailbox.ListTicketReplies(context.Background(), "ASMAIL", now.Add(-time.Minute), 30)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "support+asmail-tok@tickets.example.com", messages[0].Recipient)
	require.Equal(t, files.content["mail/asmail.eml"], messages[0].Raw)

	require.NoError(t, NewRepo(db).IgnoreInboundReply(context.Background(), messages[0].ID, "ASMAIL"))
	messages, err = mailbox.ListTicketReplies(context.Background(), "ASMAIL", now.Add(-time.Minute), 30)
	require.NoError(t, err)
	require.Empty(t, messages)
}

func TestRepoCloseMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()
	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo: "AS4", TicketType: domain.TicketTypeOrder, Title: "t", RequesterUserID: 7,
		Order: &domain.OrderSnapshot{OrderNo: "OR1", PayAmount: "6.8"}, FirstMessage: userMessage("hi"),
	})
	require.NoError(t, err)

	updated, err := repo.Close(ctx, aftersaleapp.CloseParams{
		TicketNo:      "AS4",
		By:            domain.SenderTypePlatform,
		Resolution:    domain.Resolution{Kind: domain.ResolutionRefunded, RefundAmount: "6.800000"},
		SystemMessage: "平台已退款 6.8 积分并关闭工单。",
	})
	require.NoError(t, err)
	require.Equal(t, domain.TicketStatusClosed, updated.Status)
	require.NotNil(t, updated.Resolution)
	require.Equal(t, domain.ResolutionRefunded, updated.Resolution.Kind)
	require.Equal(t, "6.800000", updated.Resolution.RefundAmount)
	require.Equal(t, domain.SenderTypeSystem, updated.Messages[len(updated.Messages)-1].SenderType)

	// A second close conflicts (terminal state).
	_, err = repo.Close(ctx, aftersaleapp.CloseParams{
		TicketNo: "AS4", By: domain.SenderTypePlatform,
		Resolution: domain.Resolution{Kind: domain.ResolutionClosed}, SystemMessage: "x",
	})
	require.ErrorIs(t, err, domain.ErrTicketStateConflict)
}

func TestRepoListFacetsCursorMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		ticketType := domain.TicketTypeOrder
		var order *domain.OrderSnapshot
		if i >= 3 {
			ticketType = domain.TicketTypeGeneral
		} else {
			order = &domain.OrderSnapshot{OrderNo: fmt.Sprintf("OR%d", i), PayAmount: "1"}
		}
		_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
			TicketNo: fmt.Sprintf("AS10%d", i), TicketType: ticketType, Title: "t",
			RequesterUserID: 7, Order: order, FirstMessage: userMessage("hi"),
		})
		require.NoError(t, err)
	}
	// A different user's ticket is excluded from scope=mine.
	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo: "AS999", TicketType: domain.TicketTypeGeneral, Title: "t", RequesterUserID: 99, FirstMessage: userMessage("hi"),
	})
	require.NoError(t, err)

	mine := aftersaleapp.ListFilter{RequesterUserID: 7, Scope: "mine"}

	page1, next, err := repo.List(ctx, mine, 0, 0, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotNil(t, next)
	require.Greater(t, page1[0].ID, page1[1].ID) // id DESC

	page2, _, err := repo.List(ctx, mine, 0, *next, 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.Less(t, page2[0].ID, page1[1].ID) // cursor continues below the previous block

	total, err := repo.Count(ctx, mine)
	require.NoError(t, err)
	require.Equal(t, int64(5), total)

	facets, err := repo.Facets(ctx, mine)
	require.NoError(t, err)
	require.Equal(t, int64(5), facets.TicketType.All)
	require.Equal(t, int64(3), facets.TicketType.Order)
	require.Equal(t, int64(2), facets.TicketType.General)
	require.Equal(t, int64(5), facets.Status.Open)

	// Type filter still reports full facets (each dimension excludes itself).
	filtered := mine
	filtered.TicketType = domain.TicketTypeOrder
	filteredFacets, err := repo.Facets(ctx, filtered)
	require.NoError(t, err)
	require.Equal(t, int64(5), filteredFacets.TicketType.All)

	// Admin scope=all sees every requester's tickets.
	adminItems, _, err := repo.List(ctx, aftersaleapp.ListFilter{IsAdmin: true, Scope: "all"}, 0, 0, 100)
	require.NoError(t, err)
	require.Len(t, adminItems, 6)
}

func TestRepoFindAttachmentMySQL(t *testing.T) {
	repo := NewRepo(newAftersaleTestDB(t))
	ctx := context.Background()
	_, err := repo.Create(ctx, aftersaleapp.CreateTicketParams{
		TicketNo: "AS5", TicketType: domain.TicketTypeGeneral, Title: "t", RequesterUserID: 7,
		FirstMessage: userMessage("hi", aftersaleapp.AttachmentInsert{AttachmentNo: "AA9", ObjectKey: "obj/9", Mime: "image/png", Size: 5}),
	})
	require.NoError(t, err)

	attachment, err := repo.FindAttachment(ctx, "AS5", "AA9")
	require.NoError(t, err)
	require.Equal(t, "obj/9", attachment.ObjectKey)

	// Wrong ticket scoping is a not-found, never a cross-ticket leak.
	_, err = repo.FindAttachment(ctx, "WRONG", "AA9")
	require.ErrorIs(t, err, domain.ErrAttachmentNotFound)
}
