package infra

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminMessageSummaryAndDetailBoundaryMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_ADMIN_MESSAGE")
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (100, 1, 'alias@example.com', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, matched_order_id, recipient, sender, subject,
    raw_body, body_preview, verification_code, message_id_header, provider_message_id,
    dedupe_key, protocol, folder, status, match_diagnostic, received_at
) VALUES
    (100, 'microsoft', NULL, 'main@example.com', 'main-sender@example.net', 'Main subject',
     'main-body-sensitive-canary', 'Main safe preview', '', 'main-message@example.net', 'provider-main',
     REPEAT('1', 64), 'graph', 'inbox', 'ignored', 'Message did not match any active order service.', ?),
    (100, 'microsoft', ?, 'alias@example.com', 'alias-sender@example.net', 'Alias subject',
     'alias-body-sensitive-canary', 'Alias safe preview', '654321', 'alias-message@example.net', 'provider-alias',
     REPEAT('2', 64), 'graph', 'inbox', 'matched', '', ?)
`, now.Add(-time.Minute), orderID, now).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (101, 'microsoft', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, email_domain, password, client_id, refresh_token, status)
VALUES (101, 'microsoft', 'other@example.com', 'example.com', 'other-password', 'other-client', 'other-rt', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body, body_preview,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    101, 'microsoft', 'other@example.com', 'other-sender@example.net', 'Other subject',
    'other-body-sensitive-canary', 'Other safe preview', REPEAT('3', 64), 'ignored', '', ?
)`, now).Error)

	repo := NewAdminMessageRepo(db)
	exists, err := repo.AdminMessageResourceExists(context.Background(), 100)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = repo.AdminMessageResourceExists(context.Background(), 99999)
	require.NoError(t, err)
	require.False(t, exists)

	items, total, err := repo.ListAdminMessageSummaries(context.Background(), mailmatchapp.AdminMessageListQuery{
		ResourceID: 100,
		Offset:     0,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	require.Equal(t, "alias", items[0].Mailbox)
	require.Equal(t, "Alias safe preview", items[0].Preview)
	require.NotNil(t, items[0].VerificationCode)
	require.Equal(t, "654321", *items[0].VerificationCode)
	require.NotNil(t, items[0].OrderNo)
	require.Equal(t, "OR_ADMIN_MESSAGE", *items[0].OrderNo)
	require.Equal(t, "main", items[1].Mailbox)
	require.Nil(t, items[1].VerificationCode)
	require.Nil(t, items[1].OrderNo)
	serializedSummaries := strings.ToLower(fmt.Sprintf("%+v", items))
	for _, secret := range []string{"main-body-sensitive-canary", "alias-body-sensitive-canary", "other-body-sensitive-canary"} {
		require.NotContains(t, serializedSummaries, secret)
	}

	searched, searchedTotal, err := repo.ListAdminMessageSummaries(context.Background(), mailmatchapp.AdminMessageListQuery{
		ResourceID: 100,
		Search:     "main-body-sensitive-canary",
		Offset:     0,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), searchedTotal)
	require.Len(t, searched, 1)
	require.Equal(t, "Main subject", searched[0].Subject)
	require.NotContains(t, strings.ToLower(fmt.Sprintf("%+v", searched[0])), "main-body-sensitive-canary")

	mainID := adminMessageIDBySubject(t, db, "Main subject")
	detail, err := repo.FindAdminMessageDetailWithLog(
		context.Background(),
		100,
		mainID,
		adminMessageReadLog(1, 100, mainID, "req-admin-message-detail"),
	)
	require.NoError(t, err)
	require.Equal(t, "main-body-sensitive-canary", detail.Body)
	require.NotNil(t, detail.MatchDiagnostic)
	require.Equal(t, "Message did not match any active order service.", *detail.MatchDiagnostic)

	_, err = repo.FindAdminMessageDetailWithLog(
		context.Background(),
		101,
		mainID,
		adminMessageReadLog(1, 101, mainID, "req-admin-message-cross-resource"),
	)
	require.ErrorIs(t, err, domain.ErrMessageNotFound)
	var logs []governanceinfra.OperationLogModel
	require.NoError(t, db.Where("operation_type = ?", "mailmatch.admin_message.body.read").Find(&logs).Error)
	require.Len(t, logs, 1, "cross-resource 404 must not write a success audit")
	serializedLogs := strings.ToLower(fmt.Sprintf("%+v", logs))
	for _, forbidden := range []string{
		"main-body-sensitive-canary",
		"main-sender@example.net",
		"main subject",
		"654321",
		"main@example.com",
	} {
		require.NotContains(t, serializedLogs, forbidden)
	}
}

func TestAdminMessageDetailSanitizesUnknownDiagnosticAndAuditsAtomicallyMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body, body_preview,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    100, 'microsoft', 'main@example.com', 'sender@example.net', 'Unknown diagnostic',
    'detail-body-canary', 'safe preview', REPEAT('4', 64), 'ignored',
    'raw-rule-secret-canary password=do-not-return', ?
)`, now).Error)
	messageID := adminMessageIDBySubject(t, db, "Unknown diagnostic")
	repo := NewAdminMessageRepo(db)

	detail, err := repo.FindAdminMessageDetailWithLog(
		context.Background(),
		100,
		messageID,
		adminMessageReadLog(1, 100, messageID, "req-admin-message-sanitize"),
	)
	require.NoError(t, err)
	require.NotNil(t, detail.MatchDiagnostic)
	require.Equal(t, "Message matching diagnostic is unavailable.", *detail.MatchDiagnostic)
	require.NotContains(t, *detail.MatchDiagnostic, "raw-rule-secret-canary")

	failingLog := adminMessageReadLog(1, 100, messageID, "req-admin-message-log-failure")
	failingLog.SafeSummary = strings.Repeat("x", 501)
	_, err = repo.FindAdminMessageDetailWithLog(
		context.Background(),
		100,
		messageID,
		failingLog,
	)
	require.Error(t, err, "audit persistence failure must fail the controlled body read")
	var failedRequestCount int64
	require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).
		Where("request_id = ?", "req-admin-message-log-failure").
		Count(&failedRequestCount).Error)
	require.Zero(t, failedRequestCount)
}

func adminMessageReadLog(operatorID uint, resourceID uint, messageID uint, requestID string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorID,
		OperationType:  "mailmatch.admin_message.body.read",
		ResourceType:   "microsoft_message",
		ResourceID:     fmt.Sprintf("%d", messageID),
		Path:           "/v1/admin/messages/:messageId",
		Result:         "success",
		SafeSummary:    fmt.Sprintf("Primary mailbox message body read; resource=%d; message=%d.", resourceID, messageID),
		RequestID:      requestID,
	}
}

func adminMessageIDBySubject(t *testing.T, db *gorm.DB, subject string) uint {
	t.Helper()
	var id uint
	require.NoError(t, db.Table("mailmatch_messages").Select("id").Where("subject = ?", subject).Scan(&id).Error)
	require.NotZero(t, id)
	return id
}
