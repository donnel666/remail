package gmail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type localGmailProjectHistoryHarness struct {
	db        *gorm.DB
	service   *Service
	trade     *gmailTradeSpy
	inspector *asynq.Inspector
}

func newLocalGmailProjectHistoryHarness(t *testing.T) *localGmailProjectHistoryHarness {
	t.Helper()
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	queue := asynq.NewClient(redisOptions)
	inspector := asynq.NewInspector(redisOptions)
	databaseName := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{},
	))
	prepareLocalGmailHistorySchema(t, db)
	trade := &gmailTradeSpy{}
	service := NewService(db, queue)
	service.SetTrade(trade)
	service.now = func() time.Time { return time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, queue.Close())
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return &localGmailProjectHistoryHarness{db: db, service: service, trade: trade, inspector: inspector}
}

func (h *localGmailProjectHistoryHarness) seedResource(t *testing.T) resourceRootModel {
	t.Helper()
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, h.db.Create(&root).Error)
	require.NoError(t, h.db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "history.project@gmail.com", Identity: "historyproject@gmail.com",
		Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 3, ValidationGeneration: 4, Status: LocalResourceNormal, ForSale: true,
	}).Error)
	require.NoError(t, h.db.Model(&localGmailProjectHistoryStateModel{}).Where("id = ?", 11).Updates(map[string]any{
		"gmail_history_scan_status": "pending", "gmail_history_scan_generation": 1,
		"gmail_history_scan_request_id":   "project-history-request",
		"gmail_history_scan_requested_at": h.service.now(),
	}).Error)
	return root
}

func (h *localGmailProjectHistoryHarness) continuationTask(t *testing.T) localGmailProjectHistoryTask {
	t.Helper()
	pending, err := h.inspector.ListPendingTasks(platform.QueueBackgroundProjectHistory)
	require.NoError(t, err)
	for _, item := range pending {
		if item.Type != typeGmailProjectHistoryScan {
			continue
		}
		var task localGmailProjectHistoryTask
		require.NoError(t, json.Unmarshal(item.Payload, &task))
		if task.AfterResourceID > 0 {
			return task
		}
	}
	t.Fatal("Gmail project history continuation was not queued")
	return localGmailProjectHistoryTask{}
}

func TestLocalGmailProjectHistoryPersistsFactsAndCompletesContinuation(t *testing.T) {
	harness := newLocalGmailProjectHistoryHarness(t)
	root := harness.seedResource(t)
	matchedAt := harness.service.now().Add(-24 * time.Hour)
	harness.service.fetch = func(
		_ context.Context, email, appPassword string, cursors localGmailFolderCursors, since time.Time, fullHistory bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		require.Equal(t, "history.project@gmail.com", email)
		require.Equal(t, "app-password", appPassword)
		require.True(t, since.IsZero())
		require.True(t, fullHistory)
		if cursors.Inbox != 0 || cursors.Spam != 0 {
			return nil, cursors, nil
		}
		return []localGmailFetchedMessage{{
			UID: 1, Folder: localGmailInboxFolder, Recipient: "history.project@gmail.com",
			ProviderMessageID: "inbox:1:1", ReceivedAt: matchedAt,
			Raw: []byte("To: history.project@gmail.com\r\nFrom: noreply@example.com\r\nSubject: Legacy sign-in\r\n\r\nYour old project code is 123456."),
		}}, localGmailFolderCursors{Inbox: 1, Spam: 1}, nil
	}

	task := localGmailProjectHistoryTask{ProjectID: 11, Generation: 1, RequestID: "project-history-request"}
	require.NoError(t, harness.service.ProcessLocalGmailProjectHistory(context.Background(), task))
	next := harness.continuationTask(t)
	require.Equal(t, root.ID, next.AfterResourceID)
	require.Equal(t, root.ID, next.MaxResourceID)
	require.Equal(t, 1, next.ScannedCount)
	require.Equal(t, 1, next.MatchedCount)
	require.NoError(t, harness.service.ProcessLocalGmailProjectHistory(context.Background(), next))

	var state localGmailProjectHistoryStateModel
	require.NoError(t, harness.db.First(&state, 11).Error)
	require.Equal(t, "normal", state.Status)
	require.Equal(t, 1, state.ScannedCount)
	require.Equal(t, 1, state.MatchedCount)
	require.Zero(t, state.SkippedCount)
	require.NotNil(t, state.FinishedAt)

	var allocation allocationModel
	require.NoError(t, harness.db.Take(&allocation).Error)
	require.True(t, strings.HasPrefix(allocation.OrderNo, "HIST-GMAIL-"))
	require.Equal(t, AllocationStatusReleased, allocation.Status)
	require.Equal(t, "purchase", allocation.ServiceMode)
	require.Len(t, harness.trade.history, 1)
}

func TestLocalGmailProjectHistoryRejectsChangedScopeBeforeWritingFacts(t *testing.T) {
	harness := newLocalGmailProjectHistoryHarness(t)
	harness.seedResource(t)
	changed := false
	harness.service.fetch = func(
		_ context.Context, _ string, _ string, cursors localGmailFolderCursors, _ time.Time, _ bool,
	) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		if cursors.Inbox != 0 {
			return nil, cursors, nil
		}
		if !changed {
			changed = true
			require.NoError(t, harness.db.Table("project_products").Where("id = ?", 12).Update("warranty_minutes", 61).Error)
		}
		return []localGmailFetchedMessage{{
			UID: 1, Folder: localGmailInboxFolder, Recipient: "history.project@gmail.com",
			ProviderMessageID: "inbox:1:1", ReceivedAt: harness.service.now().Add(-time.Hour),
			Raw: []byte("To: history.project@gmail.com\r\nFrom: noreply@example.com\r\nSubject: Legacy sign-in\r\n\r\nYour old project code is 123456."),
		}}, localGmailFolderCursors{Inbox: 1, Spam: 1}, nil
	}

	require.NoError(t, harness.service.ProcessLocalGmailProjectHistory(context.Background(), localGmailProjectHistoryTask{
		ProjectID: 11, Generation: 1, RequestID: "scope-change",
	}))

	var state localGmailProjectHistoryStateModel
	require.NoError(t, harness.db.First(&state, 11).Error)
	require.Equal(t, "pending", state.Status)
	require.Equal(t, 1, state.Failures)
	require.Contains(t, state.LastSafeError, "scope changed")
	var allocations int64
	require.NoError(t, harness.db.Model(&allocationModel{}).Count(&allocations).Error)
	require.Zero(t, allocations)
	require.Empty(t, harness.trade.history)
}
