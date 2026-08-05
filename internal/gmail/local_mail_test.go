package gmail

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/emersion/go-imap/v2"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newLocalCodeSession(t *testing.T, name string, clock *time.Time, codeWindowMinutes ...int) (*gorm.DB, *Service, sessionModel, uint) {
	t.Helper()
	windowMinutes := 10
	if len(codeWindowMinutes) > 0 {
		windowMinutes = codeWindowMinutes[0]
	}
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}, &sessionModel{},
	))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_local_allocation_order ON gmail_allocations(order_no)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_local_session_order ON gmail_code_sessions(order_no)").Error)

	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "local@gmail.com", Identity: "local@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", Status: LocalResourceNormal,
	}).Error)

	service := NewService(db, nil)
	service.now = func() time.Time { return clock.UTC() }
	orderNo := "ORDER-" + name
	resourceID := root.ID
	require.NoError(t, db.Create(&localAllocationGuardModel{OrderNo: orderNo, Type: "gmail"}).Error)
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: orderNo, GuardType: "gmail", ProjectID: 7, ProductID: 71,
		Source: SourceLocal, ServiceMode: string(tradedomain.ServiceModeCode), ResourceID: &resourceID,
		SupplyScope: AllocationSupplyPublic, Mailbox: GmailMailboxPlus,
		Email: "local+project@gmail.com", Status: AllocationStatusAllocated,
		CostPointsSnapshot: "1",
	}).Error)
	sessionID, err := service.CreateSession(context.Background(), tradeapp.GmailSessionCommand{
		OrderNo: orderNo, ProjectID: 7, ProductID: 71, CodeWindowMinutes: windowMinutes,
		Quote: tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "1"},
	})
	require.NoError(t, err)
	var session sessionModel
	require.NoError(t, db.First(&session, sessionID).Error)
	return db, service, session, root.ID
}

func TestLocalGmailSessionUsesProjectCodeWindow(t *testing.T) {
	clock := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	_, _, session, _ := newLocalCodeSession(t, "gmail-local-project-window", &clock, 17)

	require.NotNil(t, session.StartedAt)
	require.NotNil(t, session.ExpiresAt)
	require.Equal(t, 17*time.Minute, session.ExpiresAt.Sub(*session.StartedAt))
}

func loadGmailSession(t *testing.T, db *gorm.DB, sessionID uint) sessionModel {
	t.Helper()
	var session sessionModel
	require.NoError(t, db.First(&session, sessionID).Error)
	return session
}

func TestLocalGmailRecordsThreeCodesAndDeduplicatesReplayedMail(t *testing.T) {
	clock := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	db, service, session, _ := newLocalCodeSession(t, "gmail-local-three-codes", &clock)
	trade := &gmailTradeSpy{}
	service.SetTrade(trade)

	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "too-early", clock.Add(-time.Second)))
	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "too-late", clock.Add(10*time.Minute)))
	firstReceivedAt := clock.Add(time.Minute)
	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "111111", firstReceivedAt))
	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "111111", firstReceivedAt))

	session = loadGmailSession(t, db, session.ID)
	require.EqualValues(t, 1, session.ReceivedCount)
	require.Equal(t, SessionActive, session.Status)
	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "111111", firstReceivedAt.Add(time.Minute)))
	session = loadGmailSession(t, db, session.ID)
	require.EqualValues(t, 2, session.ReceivedCount, "the same code from a different mail still counts")
	require.Equal(t, SessionActive, session.Status)

	require.NoError(t, service.RecordMatchedCode(context.Background(), session.OrderNo, "333333", firstReceivedAt.Add(2*time.Minute)))
	session = loadGmailSession(t, db, session.ID)
	require.EqualValues(t, MaxCodes, session.ReceivedCount)
	require.Equal(t, SessionCompleted, session.Status)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{session.OrderNo}, trade.completed)
	require.Empty(t, trade.failed)
	var allocation allocationModel
	require.NoError(t, db.Where("order_no = ?", session.OrderNo).Take(&allocation).Error)
	require.Equal(t, AllocationStatusReleased, allocation.Status)
	require.NotNil(t, allocation.ReleasedAt)
}

func TestLocalGmailExpirySettlesByCodeCount(t *testing.T) {
	for _, test := range []struct {
		count        int
		wantStatus   string
		wantComplete bool
	}{
		{count: 0, wantStatus: SessionCancelled},
		{count: 1, wantStatus: SessionCompleted, wantComplete: true},
		{count: 2, wantStatus: SessionCompleted, wantComplete: true},
	} {
		t.Run(fmt.Sprintf("%d_codes", test.count), func(t *testing.T) {
			startedAt := time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC)
			clock := startedAt
			db, service, session, _ := newLocalCodeSession(t, fmt.Sprintf("gmail-local-expiry-%d", test.count), &clock)
			trade := &gmailTradeSpy{}
			service.SetTrade(trade)
			for i := 0; i < test.count; i++ {
				require.NoError(t, service.RecordMatchedCode(
					context.Background(), session.OrderNo, fmt.Sprintf("code-%d", i+1), startedAt.Add(time.Duration(i+1)*time.Minute),
				))
			}
			clock = startedAt.Add(10 * time.Minute)
			require.NoError(t, service.Poll(context.Background(), session.ID))

			session = loadGmailSession(t, db, session.ID)
			require.Equal(t, test.wantStatus, session.Status)
			require.Nil(t, session.NextPollAt)
			require.Len(t, trade.activations, 1)
			if test.wantComplete {
				require.Equal(t, []string{session.OrderNo}, trade.completed)
				require.Empty(t, trade.failed)
			} else {
				require.Equal(t, []string{session.OrderNo}, trade.failed)
				require.Empty(t, trade.completed)
			}
			var allocation allocationModel
			require.NoError(t, db.Where("order_no = ?", session.OrderNo).Take(&allocation).Error)
			require.Equal(t, AllocationStatusReleased, allocation.Status)
		})
	}
}

type retryGmailTradeSpy struct {
	gmailTradeSpy
	completeFailures int
}

func (s *retryGmailTradeSpy) CompleteGmailOrder(_ context.Context, orderNo, _ string) error {
	s.completed = append(s.completed, orderNo)
	if s.completeFailures > 0 {
		s.completeFailures--
		return errors.New("trade temporarily unavailable")
	}
	return nil
}

func TestLocalGmailTerminalCallbackRetriesAfterAllocationRelease(t *testing.T) {
	clock := time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC)
	db, service, session, _ := newLocalCodeSession(t, "gmail-local-callback-retry", &clock)
	trade := &retryGmailTradeSpy{completeFailures: 1}
	service.SetTrade(trade)
	for i := 0; i < MaxCodes; i++ {
		err := service.RecordMatchedCode(
			context.Background(), session.OrderNo, fmt.Sprintf("code-%d", i+1), clock.Add(time.Duration(i+1)*time.Minute),
		)
		if i < MaxCodes-1 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}

	session = loadGmailSession(t, db, session.ID)
	require.Equal(t, SessionCompleted, session.Status)
	require.NotNil(t, session.NextPollAt)
	var allocation allocationModel
	require.NoError(t, db.Where("order_no = ?", session.OrderNo).Take(&allocation).Error)
	require.Equal(t, AllocationStatusReleased, allocation.Status)

	require.NoError(t, service.Poll(context.Background(), session.ID))
	session = loadGmailSession(t, db, session.ID)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{session.OrderNo, session.OrderNo}, trade.completed)
}

type localMailIngestSpy struct {
	messageIDs []string
	recipients []string
	folders    []string
	failAt     int
}

func (s *localMailIngestSpy) IngestGmailMail(_ context.Context, _ uint, recipient string, _ []byte, _ time.Time, messageID, folder string) error {
	s.messageIDs = append(s.messageIDs, messageID)
	s.recipients = append(s.recipients, recipient)
	s.folders = append(s.folders, folder)
	if s.failAt > 0 && len(s.messageIDs) == s.failAt {
		return errors.New("mailmatch temporarily unavailable")
	}
	return nil
}

func TestLocalGmailCursorAdvancesOnlyAfterWholeFetchIsIngested(t *testing.T) {
	clock := time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC)
	db, service, session, _ := newLocalCodeSession(t, "gmail-local-cursor", &clock)
	service.SetTrade(&gmailTradeSpy{})
	cursors := make([]localGmailFolderCursors, 0, 2)
	service.fetch = func(_ context.Context, email, _ string, cursor localGmailFolderCursors, _ time.Time, fullHistory bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, "local@gmail.com", email)
		require.False(t, fullHistory)
		cursors = append(cursors, cursor)
		return []localGmailFetchedMessage{
			{UID: 11, Folder: localGmailInboxFolder, Recipient: "local+one@gmail.com", ProviderMessageID: "inbox:77:11", Raw: []byte("first"), ReceivedAt: clock},
			{UID: 12, Folder: localGmailSpamFolder, Recipient: "local+two@gmail.com", ProviderMessageID: "spam:88:12", Raw: []byte("second"), ReceivedAt: clock.Add(time.Second)},
		}, localGmailFolderCursors{Inbox: joinLocalGmailCursor(77, 11), Spam: joinLocalGmailCursor(88, 12)}, nil
	}
	mail := &localMailIngestSpy{failAt: 2}
	service.SetMailIngest(mail)
	require.Error(t, service.Poll(context.Background(), session.ID))
	session = loadGmailSession(t, db, session.ID)
	require.Zero(t, session.ProviderCursor)
	require.Zero(t, session.ProviderSpamCursor)

	mail.failAt = 0
	mail.messageIDs = nil
	mail.recipients = nil
	mail.folders = nil
	require.NoError(t, service.Poll(context.Background(), session.ID))
	session = loadGmailSession(t, db, session.ID)
	require.EqualValues(t, joinLocalGmailCursor(77, 11), session.ProviderCursor)
	require.EqualValues(t, joinLocalGmailCursor(88, 12), session.ProviderSpamCursor)
	require.Equal(t, []localGmailFolderCursors{{}, {}}, cursors)
	require.Equal(t, []string{"local+one@gmail.com", "local+two@gmail.com"}, mail.recipients)
	require.Equal(t, []string{localGmailInboxFolder, localGmailSpamFolder}, mail.folders)
}

func TestLocalGmailFetchKeepsOldestHundredUIDs(t *testing.T) {
	uids := make([]imap.UID, localGmailFetchLimit+5)
	for i := range uids {
		uids[i] = imap.UID(i + 1)
	}
	oldest := oldestLocalGmailUIDs(uids, localGmailFetchLimit)
	require.Len(t, oldest, localGmailFetchLimit)
	require.Equal(t, imap.UID(1), oldest[0])
	require.Equal(t, imap.UID(localGmailFetchLimit), oldest[len(oldest)-1])
}

func TestLocalGmailCursorIncludesUIDValidity(t *testing.T) {
	cursor := joinLocalGmailCursor(123, 456)
	uidValidity, uid := splitLocalGmailCursor(cursor)

	require.EqualValues(t, 123, uidValidity)
	require.EqualValues(t, 456, uid)
	require.Equal(t, "inbox:123:456", localGmailProviderMessageID(localGmailInboxFolder, uidValidity, uid))
	require.NotEqual(t, localGmailProviderMessageID(localGmailInboxFolder, 122, uid), localGmailProviderMessageID(localGmailInboxFolder, uidValidity, uid))
	require.NotEqual(t, localGmailProviderMessageID(localGmailSpamFolder, uidValidity, uid), localGmailProviderMessageID(localGmailInboxFolder, uidValidity, uid))
	require.EqualValues(t, 456, localGmailCursorUID(cursor, 123))
	require.Zero(t, localGmailCursorUID(cursor, 124), "a new UIDVALIDITY must reset the mailbox cursor")
}

func TestLocalGmailCandidateFoldersPreferSpecialUseSpam(t *testing.T) {
	folders := localGmailCandidateFoldersFromList([]*imap.ListData{
		{Mailbox: "INBOX"},
		{Mailbox: "Spam Archive"},
		{Mailbox: "垃圾邮件", Attrs: []imap.MailboxAttr{imap.MailboxAttrJunk}},
	})

	firstSpam := ""
	for _, folder := range folders {
		if folder.Label == localGmailSpamFolder {
			firstSpam = folder.ID
			break
		}
	}
	require.Equal(t, "垃圾邮件", firstSpam)
	require.Contains(t, folders, localGmailFolder{ID: "INBOX", Label: localGmailInboxFolder})
	require.Contains(t, folders, localGmailFolder{ID: "[Gmail]/Spam", Label: localGmailSpamFolder})
}

func TestLocalGmailOriginalRecipientUsesProviderDeliveryHeaders(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "delivered-to wins",
			raw:  "Delivered-To: first.name+actual@googlemail.com\r\nTo: firstname+wrong@gmail.com\r\n\r\nbody",
			want: "first.name+actual@googlemail.com",
		},
		{
			name: "x-original-to fallback",
			raw:  "X-Original-To: first.name@gmail.com\r\nTo: unrelated@example.com\r\n\r\nbody",
			want: "first.name@gmail.com",
		},
		{
			name: "envelope-to fallback",
			raw:  "Envelope-To: firstname+legacy@gmail.com\r\n\r\nbody",
			want: "firstname+legacy@gmail.com",
		},
		{
			name: "ambiguous same account aliases",
			raw:  "To: firstname+one@gmail.com, firstname+two@gmail.com\r\n\r\nbody",
		},
		{
			name: "different Gmail account",
			raw:  "To: coworker@gmail.com\r\n\r\nbody",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, localGmailOriginalRecipient("firstname@gmail.com", []byte(test.raw)))
		})
	}
}

func TestLocalGmailFetchLimitsBodyAndBatchMemory(t *testing.T) {
	setGmailRuntime(t, map[string]string{"max_inbound_body_bytes": "10485760"})

	messageLimit, bodySection := localGmailFetchOptions(false)

	require.Equal(t, 10, messageLimit)
	require.True(t, bodySection.Peek)
	require.NotNil(t, bodySection.Partial)
	require.EqualValues(t, 10<<20, bodySection.Partial.Size)
	_, historyBodySection := localGmailFetchOptions(true)
	require.Nil(t, historyBodySection.Partial)
}

func TestFetchLocalPurchaseMailUsesTypedAllocationAndStableUIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase-mail?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &allocationModel{}, &sessionModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "purchase-mail@gmail.com", Identity: "purchase-mail@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "purchase-app-password", Status: LocalResourceNormal,
	}).Error)
	allocatedAt := time.Date(2026, 8, 3, 4, 30, 0, 0, time.UTC)
	resourceID := root.ID
	allocation := allocationModel{
		OrderNo: "PURCHASE-MAIL", Source: SourceLocal, ServiceMode: string(tradedomain.ServiceModePurchase),
		ResourceID: &resourceID, Mailbox: GmailMailboxPlus,
		Email: "purchase.mail+order@gmail.com", Status: AllocationStatusAllocated,
		CostPointsSnapshot: "1", CreatedAt: allocatedAt,
	}
	require.NoError(t, db.Create(&allocation).Error)

	service := NewService(db, nil)
	mail := &localMailIngestSpy{}
	service.SetMailIngest(mail)
	const uidValidity = 91
	providerCursor := joinLocalGmailCursor(uidValidity, 102)
	var cursors []localGmailFolderCursors
	var sinceValues []time.Time
	service.fetch = func(_ context.Context, email, appPassword string, cursor localGmailFolderCursors, since time.Time, fullHistory bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, "purchase-mail@gmail.com", email)
		require.Equal(t, "purchase-app-password", appPassword)
		require.False(t, fullHistory)
		cursors = append(cursors, cursor)
		sinceValues = append(sinceValues, since)
		if cursor.Inbox == providerCursor {
			return nil, cursor, nil
		}
		return []localGmailFetchedMessage{
			{UID: 101, Folder: localGmailInboxFolder, Recipient: "purchase.mail+order@gmail.com", ProviderMessageID: "inbox:91:101", Raw: []byte("first"), ReceivedAt: allocatedAt.Add(time.Minute)},
			{UID: 102, Folder: localGmailInboxFolder, Recipient: "purchase.mail+order@gmail.com", ProviderMessageID: "inbox:91:102", Raw: []byte("second"), ReceivedAt: allocatedAt.Add(2 * time.Minute)},
		}, localGmailFolderCursors{Inbox: providerCursor}, nil
	}

	require.NoError(t, service.FetchLocalPurchaseMail(context.Background(), allocation.OrderNo))
	require.NoError(t, service.FetchLocalPurchaseMail(context.Background(), allocation.OrderNo))
	require.Equal(t, []localGmailFolderCursors{{}, {Inbox: providerCursor}}, cursors)
	require.Len(t, sinceValues, 2)
	require.True(t, sinceValues[0].Equal(allocatedAt))
	require.True(t, sinceValues[1].Equal(allocatedAt))
	require.Equal(t, []string{"inbox:91:101", "inbox:91:102"}, mail.messageIDs)
	require.Equal(t, []string{"purchase.mail+order@gmail.com", "purchase.mail+order@gmail.com"}, mail.recipients)

	var sessionCount int64
	require.NoError(t, db.Model(&sessionModel{}).Count(&sessionCount).Error)
	require.Zero(t, sessionCount)
	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusAllocated, allocation.Status)
	require.Equal(t, providerCursor, allocation.ProviderCursor)

	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}
	require.Error(t, service.FetchLocalPurchaseMail(context.Background(), allocation.OrderNo))
	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusAllocated, allocation.Status, "purchase mail errors must not release or refund the purchase")
}

func TestLocalGmailAuthenticationFailureDisablesSupplyAndRefunds(t *testing.T) {
	clock := time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC)
	db, service, session, resourceID := newLocalCodeSession(t, "gmail-local-auth-failure", &clock)
	trade := &gmailTradeSpy{}
	service.SetTrade(trade)
	service.SetMailIngest(&localMailIngestSpy{})
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}

	require.NoError(t, service.Poll(context.Background(), session.ID))
	session = loadGmailSession(t, db, session.ID)
	require.Equal(t, SessionFailed, session.Status)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{session.OrderNo}, trade.failed)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Equal(t, LocalResourceAbnormal, resource.Status)
	var allocation allocationModel
	require.NoError(t, db.Where("order_no = ?", session.OrderNo).Take(&allocation).Error)
	require.Equal(t, AllocationStatusReleased, allocation.Status)
}

func TestCancelLocalGmailReleasesWithoutRemoteAction(t *testing.T) {
	clock := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	db, service, session, _ := newLocalCodeSession(t, "gmail-local-cancel", &clock)
	trade := &gmailTradeSpy{}
	service.SetTrade(trade)

	require.NoError(t, service.CancelGmailOrder(context.Background(), session.OrderNo))
	session = loadGmailSession(t, db, session.ID)
	require.Equal(t, SessionCancelled, session.Status)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{session.OrderNo}, trade.failed)
	var allocation allocationModel
	require.NoError(t, db.Where("order_no = ?", session.OrderNo).Take(&allocation).Error)
	require.Equal(t, AllocationStatusReleased, allocation.Status)
}
