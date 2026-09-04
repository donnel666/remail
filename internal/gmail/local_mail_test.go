package gmail

import (
	"context"
	"errors"
	"testing"
	"time"

	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/emersion/go-imap/v2"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newLocalMailResource(t *testing.T, name string, clock *time.Time) (*gorm.DB, *Service, string, uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{},
	))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_local_allocation_order ON gmail_allocations(order_no)").Error)

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
	return db, service, orderNo, root.ID
}

type localMailIngestSpy struct {
	resourceIDs []uint
	messageIDs  []string
	recipients  []string
	folders     []string
	fences      int
	failAt      int
}

func (s *localMailIngestSpy) IngestGmailMail(ctx context.Context, resourceID uint, recipient string, _ []byte, _ time.Time, messageID, folder string, fence func(context.Context) error) (int, int, error) {
	if fence != nil {
		s.fences++
		if err := fence(ctx); err != nil {
			return 0, 0, err
		}
	}
	s.resourceIDs = append(s.resourceIDs, resourceID)
	s.messageIDs = append(s.messageIDs, messageID)
	s.recipients = append(s.recipients, recipient)
	s.folders = append(s.folders, folder)
	if s.failAt > 0 && len(s.messageIDs) == s.failAt {
		return 0, 0, errors.New("mailmatch temporarily unavailable")
	}
	return 1, 0, nil
}

func TestLocalGmailCursorAdvancesOnlyAfterWholeFetchIsIngested(t *testing.T) {
	clock := time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC)
	db, service, orderNo, resourceID := newLocalMailResource(t, "gmail-local-cursor", &clock)
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
	require.Error(t, service.FetchLocalOrderMail(context.Background(), orderNo))
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Zero(t, resource.ProviderCursor)
	require.Zero(t, resource.ProviderSpamCursor)

	mail.failAt = 0
	mail.messageIDs = nil
	mail.recipients = nil
	mail.folders = nil
	require.NoError(t, service.FetchLocalOrderMail(context.Background(), orderNo))
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.EqualValues(t, joinLocalGmailCursor(77, 11), resource.ProviderCursor)
	require.EqualValues(t, joinLocalGmailCursor(88, 12), resource.ProviderSpamCursor)
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

func TestLocalGmailIncrementalCursorIgnoresMessageAge(t *testing.T) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	require.True(t, localGmailSearchSince(7, since).IsZero())
	require.Equal(t, since.Add(-2*time.Minute), localGmailSearchSince(0, since))
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

func TestFetchLocalOrderMailUsesSharedResourceCursorForPurchases(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase-mail?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &allocationModel{}))
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
	now := allocatedAt.Add(time.Hour)
	service.now = func() time.Time { return now }
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

	require.NoError(t, service.FetchLocalOrderMail(context.Background(), allocation.OrderNo))
	require.NoError(t, service.FetchLocalOrderMail(context.Background(), allocation.OrderNo))
	require.Equal(t, []localGmailFolderCursors{{}, {Inbox: providerCursor}}, cursors)
	require.Len(t, sinceValues, 2)
	require.True(t, sinceValues[0].Equal(now.Add(-90*24*time.Hour)))
	require.True(t, sinceValues[1].Equal(now.Add(-90*24*time.Hour)))
	require.Equal(t, []string{"inbox:91:101", "inbox:91:102"}, mail.messageIDs)
	require.Equal(t, []string{"purchase.mail+order@gmail.com", "purchase.mail+order@gmail.com"}, mail.recipients)

	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusAllocated, allocation.Status)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Equal(t, providerCursor, resource.ProviderCursor)

	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}
	require.Error(t, service.FetchLocalOrderMail(context.Background(), allocation.OrderNo))
	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusAllocated, allocation.Status, "purchase mail errors must not release or refund the purchase")
}

func TestFetchLocalResourceMailResumesLatestAllocationCursor(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, _, resourceID := newLocalMailResource(t, "gmail-admin-resource-fetch", &clock)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Update("credential_revision", 3).Error)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
		"provider_cursor": joinLocalGmailCursor(11, 20), "provider_spam_cursor": joinLocalGmailCursor(12, 7),
	}).Error)
	newOrder := allocationModel{
		OrderNo: "GMAIL-NEW-CODE", GuardType: "gmail", ProjectID: 7, ProductID: 71,
		Source: SourceLocal, ServiceMode: string(tradedomain.ServiceModeCode), ResourceID: &resourceID,
		SupplyScope: AllocationSupplyPublic, Mailbox: GmailMailboxPlus,
		Email: "local+new@gmail.com", Status: AllocationStatusAllocated, CostPointsSnapshot: "1",
	}
	require.NoError(t, db.Create(&newOrder).Error)
	mail := &localMailIngestSpy{}
	service.SetMailIngest(mail)
	var seenCursors []localGmailFolderCursors
	service.fetch = func(_ context.Context, email, appPassword string, cursor localGmailFolderCursors, since time.Time, fullHistory bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, "local@gmail.com", email)
		require.Equal(t, "app-password", appPassword)
		require.Equal(t, clock.Add(-90*24*time.Hour), since)
		require.False(t, fullHistory)
		seenCursors = append(seenCursors, cursor)
		if len(seenCursors) > 1 {
			return nil, cursor, nil
		}
		return []localGmailFetchedMessage{{
			UID: 21, Folder: localGmailInboxFolder, Recipient: "local+manual@gmail.com",
			ProviderMessageID: "inbox:11:21", Raw: []byte("message"), ReceivedAt: clock,
		}}, localGmailFolderCursors{Inbox: joinLocalGmailCursor(11, 21), Spam: joinLocalGmailCursor(12, 7)}, nil
	}

	fetched, stored, matched, err := service.FetchLocalResourceMail(context.Background(), resourceID, 3)
	require.NoError(t, err)
	require.Equal(t, 1, fetched)
	require.Equal(t, 1, stored)
	require.Zero(t, matched)
	require.NoError(t, service.FetchLocalOrderMail(context.Background(), newOrder.OrderNo))
	require.Equal(t, []localGmailFolderCursors{
		{Inbox: joinLocalGmailCursor(11, 20), Spam: joinLocalGmailCursor(12, 7)},
		{Inbox: joinLocalGmailCursor(11, 21), Spam: joinLocalGmailCursor(12, 7)},
	}, seenCursors)
	require.Equal(t, []string{"local+manual@gmail.com"}, mail.recipients)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.EqualValues(t, joinLocalGmailCursor(11, 21), resource.ProviderCursor)
	require.EqualValues(t, joinLocalGmailCursor(12, 7), resource.ProviderSpamCursor)

	_, _, _, err = service.FetchLocalResourceMail(context.Background(), resourceID, 4)
	require.ErrorIs(t, err, ErrLocalValidationConflict)
}

func TestFetchLocalResourceMailUsesSharedLookbackBeforeFirstCursor(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, _, resourceID := newLocalMailResource(t, "gmail-resource-lookback", &clock)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Update("credential_revision", 1).Error)
	service.SetMailIngest(&localMailIngestSpy{})
	service.fetch = func(_ context.Context, _, _ string, cursor localGmailFolderCursors, since time.Time, _ bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, localGmailFolderCursors{}, cursor)
		require.Equal(t, clock.Add(-90*24*time.Hour), since)
		return nil, cursor, nil
	}

	_, _, _, err := service.FetchLocalResourceMail(context.Background(), resourceID, 1)
	require.NoError(t, err)
}

func TestFetchLocalResourceMailDoesNotOverwriteConcurrentCursor(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, _, resourceID := newLocalMailResource(t, "gmail-resource-cursor-cas", &clock)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Update("credential_revision", 1).Error)
	service.SetMailIngest(&localMailIngestSpy{})
	newer := localGmailFolderCursors{Inbox: joinLocalGmailCursor(11, 30), Spam: joinLocalGmailCursor(12, 9)}
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
			"provider_cursor": newer.Inbox, "provider_spam_cursor": newer.Spam,
		}).Error)
		return nil, localGmailFolderCursors{Inbox: joinLocalGmailCursor(11, 21), Spam: joinLocalGmailCursor(12, 8)}, nil
	}

	_, _, _, err := service.FetchLocalResourceMail(context.Background(), resourceID, 1)
	require.NoError(t, err)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Equal(t, newer.Inbox, resource.ProviderCursor)
	require.Equal(t, newer.Spam, resource.ProviderSpamCursor)
}

func TestFetchLocalResourceMailFenceStopsIngestAndCursor(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, _, resourceID := newLocalMailResource(t, "gmail-resource-fetch-fence", &clock)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Update("credential_revision", 1).Error)
	mail := &localMailIngestSpy{}
	service.SetMailIngest(mail)
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return []localGmailFetchedMessage{{
			UID: 1, Folder: localGmailInboxFolder, Recipient: "local+fenced@gmail.com",
			ProviderMessageID: "inbox:11:1", Raw: []byte("message"), ReceivedAt: clock,
		}}, localGmailFolderCursors{Inbox: joinLocalGmailCursor(11, 1)}, nil
	}
	wantErr := errors.New("fetch claim replaced")
	calls := 0
	fence := func(context.Context) error {
		calls++
		if calls > 1 {
			return wantErr
		}
		return nil
	}

	_, _, _, err := service.FetchLocalResourceMailWithFence(context.Background(), resourceID, 1, fence)
	require.ErrorIs(t, err, wantErr)
	require.Empty(t, mail.messageIDs)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Zero(t, resource.ProviderCursor)
}

func TestFetchLocalOrderMailAuthenticationFailureMarksResourceAbnormal(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, orderNo, resourceID := newLocalMailResource(t, "gmail-order-auth-failure", &clock)
	service.SetMailIngest(&localMailIngestSpy{})
	trade := &gmailTradeSpy{}
	service.SetTrade(trade)
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}

	err := service.FetchLocalOrderMail(context.Background(), orderNo)
	require.ErrorIs(t, err, errLocalGmailAuthentication)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Equal(t, LocalResourceAbnormal, resource.Status)
	require.Equal(t, []uint{resourceID}, trade.refundedResourceIDs)
}

func TestDisabledGmailAuthenticationFailureKeepsAdministratorState(t *testing.T) {
	clock := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, service, _, resourceID := newLocalMailResource(t, "gmail-disabled-auth-failure", &clock)
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
		"status": LocalResourceDisabled, "credential_revision": 1,
	}).Error)
	service.SetMailIngest(&localMailIngestSpy{})
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		return nil, localGmailFolderCursors{}, errLocalGmailAuthentication
	}

	_, _, _, err := service.FetchLocalResourceMail(context.Background(), resourceID, 1)
	require.ErrorIs(t, err, errLocalGmailAuthentication)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, resourceID).Error)
	require.Equal(t, LocalResourceDisabled, resource.Status)
}
