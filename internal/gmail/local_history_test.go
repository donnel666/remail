package gmail

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidatedLocalGmailHistoryIdentifiesMainDotAndPlusUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-validated-history?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{},
		&governanceinfra.SystemLogModel{},
	))
	prepareLocalGmailHistorySchema(t, db)

	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "firstname@gmail.com", Identity: "firstname@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 3, ValidationGeneration: 4, Status: LocalResourceIdentifying,
		ForSale: true,
	}).Error)

	base := time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)
	service := NewService(db, nil)
	trade := newGmailHistoryTradeSpy(db)
	service.SetTrade(trade)
	service.now = func() time.Time { return base.Add(time.Hour) }
	var cursors []localGmailFolderCursors
	service.fetch = func(_ context.Context, email, appPassword string, cursor localGmailFolderCursors, since time.Time, fullHistory bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, "firstname@gmail.com", email)
		require.Equal(t, "app-password", appPassword)
		require.True(t, since.IsZero())
		require.True(t, fullHistory)
		cursors = append(cursors, cursor)
		if cursor.Inbox != 0 {
			return nil, cursor, nil
		}
		recipients := []string{
			"firstname@gmail.com",
			"first.name@gmail.com",
			"first.name+legacy@googlemail.com",
		}
		messages := make([]localGmailFetchedMessage, len(recipients))
		for i, recipient := range recipients {
			messages[i] = localGmailFetchedMessage{
				UID: uint64(i + 1), Folder: localGmailInboxFolder, Recipient: recipient,
				ProviderMessageID: fmt.Sprintf("inbox:1:%d", i+1),
				Raw:               []byte("To: " + recipient + "\r\nFrom: noreply@example.com\r\nSubject: Legacy sign-in\r\n\r\nYour old project code is 123456."),
				ReceivedAt:        base.Add(time.Duration(i) * time.Minute),
			}
		}
		return messages, localGmailFolderCursors{Inbox: 3}, nil
	}
	task := localGmailHistoryTask{
		ResourceID: root.ID, OwnerUserID: 7, ValidationGeneration: 4,
		ExpectedCredentialRevision: 3, RequestID: "history-request",
	}
	require.NoError(t, service.ProcessValidatedLocalGmailHistory(context.Background(), task))
	require.Equal(t, []localGmailFolderCursors{{}, {Inbox: 3}}, cursors)

	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, localResourceRollbackNormal, resource.Status)
	require.Zero(t, resource.ValidationFailures)
	require.Empty(t, resource.LastSafeError)
	require.NoError(t, db.First(&root, root.ID).Error)
	require.EqualValues(t, 2, root.Version)

	var allocations []allocationModel
	require.NoError(t, db.Order("mailbox ASC").Find(&allocations).Error)
	require.Len(t, allocations, 3)
	require.Len(t, trade.history, 3)
	byMailbox := make(map[string]allocationModel, len(allocations))
	for _, allocation := range allocations {
		byMailbox[allocation.Mailbox] = allocation
		require.True(t, strings.HasPrefix(allocation.OrderNo, "HIST-GMAIL-"))
		require.Equal(t, AllocationStatusReleased, allocation.Status)
		require.Empty(t, allocation.SourceRef)
		require.NotNil(t, allocation.ReleasedAt)
	}
	require.Equal(t, "firstname@gmail.com", byMailbox[GmailMailboxMain].Email)
	require.Equal(t, "first.name@gmail.com", byMailbox[GmailMailboxDot].Email)
	require.Equal(t, "first.name+legacy@googlemail.com", byMailbox[GmailMailboxPlus].Email)

	available, err := allocinfra.NewRepo(db).IsGmailMailboxAvailable(
		context.Background(), root.ID, 11, allocdomain.GmailMailboxMain, "firstname@gmail.com",
	)
	require.NoError(t, err)
	require.False(t, available, "identified main-mailbox history must prevent reuse by the old project")

	var guardCount, logCount int64
	require.NoError(t, db.Model(&localAllocationGuardModel{}).Count(&guardCount).Error)
	require.EqualValues(t, 3, guardCount)
	require.NoError(t, db.Model(&governanceinfra.SystemLogModel{}).
		Where("event_type = ?", "gmail.resource_history_identified").Count(&logCount).Error)
	require.EqualValues(t, 1, logCount)

	require.NoError(t, service.ProcessValidatedLocalGmailHistory(context.Background(), task))
	require.NoError(t, db.Model(&allocationModel{}).Count(&guardCount).Error)
	require.EqualValues(t, 3, guardCount, "history task replay must be idempotent")
}

func TestValidatedLocalGmailHistoryDefersBeforeValidationCommitAndIgnoresStaleFence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-validated-history-fence?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "fenced@gmail.com", Identity: "fenced@gmail.com", AppPassword: "app-password",
		CredentialRevision: 3, ValidationGeneration: 4, Status: LocalResourceValidating,
	}).Error)
	service := NewService(db, nil)
	fetchCalls := 0
	service.fetch = func(context.Context, string, string, localGmailFolderCursors, time.Time, bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		fetchCalls++
		return nil, localGmailFolderCursors{}, nil
	}
	current := localGmailHistoryTask{
		ResourceID: root.ID, OwnerUserID: 7, ValidationGeneration: 4, ExpectedCredentialRevision: 3,
	}
	require.ErrorIs(t, service.ProcessValidatedLocalGmailHistory(context.Background(), current), platform.ErrBackgroundExecutionDeferred)
	require.Zero(t, fetchCalls)

	stale := current
	stale.ExpectedCredentialRevision = 2
	require.NoError(t, service.ProcessValidatedLocalGmailHistory(context.Background(), stale))
	stale = current
	stale.ValidationGeneration = 3
	require.NoError(t, service.ProcessValidatedLocalGmailHistory(context.Background(), stale))
	stale = current
	stale.OwnerUserID = 8
	require.NoError(t, service.ProcessValidatedLocalGmailHistory(context.Background(), stale))
	require.Zero(t, fetchCalls)
}

func TestLocalGmailHistoryRequiresOneOriginalRecipient(t *testing.T) {
	mailbox, recipient, ok := localGmailHistoryRecipient("firstname@gmail.com", []string{
		"first.name@gmail.com", "coworker@gmail.com",
	})
	require.False(t, ok)
	require.Empty(t, mailbox)
	require.Empty(t, recipient)

	mailbox, recipient, ok = localGmailHistoryRecipient("firstname@gmail.com", []string{
		"first.name+legacy@googlemail.com",
	})
	require.True(t, ok)
	require.Equal(t, GmailMailboxPlus, mailbox)
	require.Equal(t, "first.name+legacy@googlemail.com", recipient)
}

func prepareLocalGmailHistorySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT NOT NULL, role TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec(`CREATE TABLE projects (
		id INTEGER PRIMARY KEY, status TEXT NOT NULL, access_type TEXT NOT NULL, loose_match INTEGER NOT NULL,
		gmail_history_scan_status TEXT NOT NULL DEFAULT 'normal', gmail_history_scan_generation INTEGER NOT NULL DEFAULT 0,
		gmail_history_scan_failures INTEGER NOT NULL DEFAULT 0, gmail_history_scan_scanned_count INTEGER NOT NULL DEFAULT 0,
		gmail_history_scan_matched_count INTEGER NOT NULL DEFAULT 0, gmail_history_scan_skipped_count INTEGER NOT NULL DEFAULT 0,
		gmail_history_scan_request_id TEXT NOT NULL DEFAULT '', gmail_history_scan_last_safe_error TEXT NOT NULL DEFAULT '',
		gmail_history_scan_requested_at DATETIME NULL, gmail_history_scan_started_at DATETIME NULL,
		gmail_history_scan_finished_at DATETIME NULL, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error)
	require.NoError(t, db.Exec("CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL, code_enabled INTEGER NOT NULL, purchase_enabled INTEGER NOT NULL, main_weight INTEGER NOT NULL, dot_weight INTEGER NOT NULL, plus_weight INTEGER NOT NULL, code_window_minutes INTEGER NOT NULL DEFAULT 1440, activation_window_minutes INTEGER NOT NULL DEFAULT 60, warranty_minutes INTEGER NOT NULL DEFAULT 60)").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_mail_rules (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, rule_type TEXT NOT NULL, pattern TEXT NOT NULL, enabled INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_accesses (project_id INTEGER NOT NULL, user_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO users(id, status, role) VALUES (7, 'active', 'supplier')").Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, status, access_type, loose_match) VALUES (11, 'listed', 'public', 0)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled, main_weight, dot_weight, plus_weight) VALUES (12, 11, 'gmail', 'enabled', 1, 1, 1, 1, 1)").Error)
	for id, pattern := range []string{"exact", "dot", "plus"} {
		require.NoError(t, db.Exec("INSERT INTO project_mail_rules(id, project_id, rule_type, pattern, enabled) VALUES (?, 11, 'recipient', ?, 1)", id+1, pattern).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(id, project_id, rule_type, pattern, enabled) VALUES (4, 11, 'sender', 'noreply@example\\.com', 1)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(id, project_id, rule_type, pattern, enabled) VALUES (5, 11, 'subject', 'Legacy sign-in', 1)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(id, project_id, rule_type, pattern, enabled) VALUES (6, 11, 'body', 'old project code', 1)").Error)
}
