package infra

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailDispatchStateMigrationReleasesInflightRowsMySQL(t *testing.T) {
	server := testmysql.New("remail_mail_dispatch_migration")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	db := server.Database(t, copyMigrationsThrough(t, 28))

	require.NoError(t, db.Exec("INSERT INTO users(id, email, password_hash, role) VALUES (99001, 'mail-dispatch@test.local', 'hash', 'supplier')").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (99002, 'domain', 99001)").Error)
	require.NoError(t, db.Exec(`
INSERT INTO inbound_mails(
    envelope_from, recipient, resource_id, resource_type, owner_user_id,
    source_object_key, status
) VALUES ('sender@test.local', 'recipient@test.local', 99002, 'domain', 99001, 'mail.eml', 'processing')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO outbound_mails(
    idempotency_key, request_hash, purpose, sender, recipient, subject,
    text_body, html_body, status, retries
) VALUES (REPEAT('a', 64), REPEAT('b', 64), 'system_notification',
          'sender@test.local', 'recipient@test.local', 'subject', 'text', 'html',
          'sending', 5)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 29))

	var inbound InboundMailModel
	require.NoError(t, db.Where("resource_id = 99002").First(&inbound).Error)
	assert.Equal(t, "pending", inbound.Status)
	assert.Equal(t, uint64(2), inbound.ProcessGeneration)
	assert.Zero(t, inbound.ProcessAttempts)

	var outbound OutboundMailModel
	require.NoError(t, db.Where("idempotency_key = ?", strings.Repeat("a", 64)).First(&outbound).Error)
	assert.Equal(t, "pending", outbound.Status)
	assert.Equal(t, uint64(2), outbound.SendGeneration)
	assert.Equal(t, 3, outbound.Retries)

	assert.Error(t, db.Model(&InboundMailModel{}).Where("id = ?", inbound.ID).Update("process_attempts", 4).Error)
	assert.Error(t, db.Model(&OutboundMailModel{}).Where("id = ?", outbound.ID).Update("retries", 4).Error)

	for _, index := range []struct {
		table string
		name  string
	}{
		{table: "inbound_mails", name: "idx_inbound_mails_status_created"},
		{table: "outbound_mails", name: "idx_outbound_mails_status_created"},
	} {
		var columns string
		require.NoError(t, db.Raw(`
SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, index.table, index.name).Scan(&columns).Error)
		assert.Equal(t, "status,created_at,id", columns, index.name)

		var plan []struct {
			PossibleKeys *string `gorm:"column:possible_keys"`
		}
		require.NoError(t, db.Raw("EXPLAIN SELECT id FROM "+index.table+" WHERE status = 'pending' ORDER BY created_at, id LIMIT 100").Scan(&plan).Error)
		require.NotEmpty(t, plan)
		require.NotNil(t, plan[0].PossibleKeys)
		assert.Contains(t, *plan[0].PossibleKeys, index.name)
	}

	ctx := context.Background()
	inboundRepo := NewInboundMailRepo(db)
	inboundPending, err := inboundRepo.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, inboundPending, 1)
	assert.Equal(t, inbound.ID, inboundPending[0].ID)
	assert.Equal(t, uint64(2), inboundPending[0].ProcessGeneration)
	assert.Empty(t, inboundPending[0].SourceObjectKey)
	activated, err := inboundRepo.ActivateProcessing(ctx, inbound.ID, 2)
	require.NoError(t, err)
	require.True(t, activated)
	activated, err = inboundRepo.ActivateProcessing(ctx, inbound.ID, 2)
	require.NoError(t, err)
	require.False(t, activated)
	for generation := uint64(2); generation <= 4; generation++ {
		terminal, applied, err := inboundRepo.RecordProcessFailure(ctx, inbound.ID, generation, "match failed", true)
		require.NoError(t, err)
		require.True(t, applied)
		assert.Equal(t, generation == 4, terminal)
		if generation < 4 {
			stored, err := inboundRepo.MarkStored(ctx, inbound.ID, generation)
			require.NoError(t, err)
			require.False(t, stored)
			activated, err = inboundRepo.ActivateProcessing(ctx, inbound.ID, generation+1)
			require.NoError(t, err)
			require.True(t, activated)
		}
	}
	require.NoError(t, db.First(&inbound, inbound.ID).Error)
	assert.Equal(t, "failed", inbound.Status)
	assert.Equal(t, 3, inbound.ProcessAttempts)

	outboundStore := NewOutboundMailStore(db)
	outboundPending, err := outboundStore.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, outboundPending, 1)
	assert.Equal(t, strings.Repeat("a", 64), outboundPending[0].IdempotencyKey)
	assert.Equal(t, uint64(2), outboundPending[0].SendGeneration)
	assert.Empty(t, outboundPending[0].HTMLBody)
	mail := domain.NewOutboundMail(domain.OutboundMessage{
		IdempotencyKey: strings.Repeat("c", 64),
		Purpose:        domain.PurposeSystemNotice,
		From:           "sender@test.local",
		To:             "recipient@test.local",
		Subject:        "subject",
		TextBody:       "text",
		HTMLBody:       "html",
	}, time.Now().UTC())
	reserved, created, err := outboundStore.Reserve(ctx, mail)
	require.NoError(t, err)
	require.True(t, created)
	for generation := uint64(1); generation <= 3; generation++ {
		activated, err = outboundStore.ActivateSending(ctx, reserved.IdempotencyKey, generation, time.Now().UTC())
		require.NoError(t, err)
		require.True(t, activated)
		terminal, applied, err := outboundStore.RecordSendFailure(ctx, reserved.IdempotencyKey, generation, "smtp failed", true)
		require.NoError(t, err)
		require.True(t, applied)
		assert.Equal(t, generation == 3, terminal)
		if generation < 3 {
			sent, err := outboundStore.MarkSent(ctx, reserved.IdempotencyKey, generation, time.Now().UTC())
			require.NoError(t, err)
			require.False(t, sent)
		}
	}
	reserved, err = outboundStore.FindByIdempotencyKey(ctx, reserved.IdempotencyKey)
	require.NoError(t, err)
	require.NotNil(t, reserved)
	assert.Equal(t, domain.OutboundStatusFailed, reserved.Status)
	assert.Equal(t, 3, reserved.Retries)
}

func copyMigrationsThrough(t *testing.T, maximum int) string {
	t.Helper()
	source := mailTransportMigrationsDir(t)
	target := t.TempDir()
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".sql" || len(name) < 5 {
			continue
		}
		version, err := strconv.Atoi(name[:5])
		if err != nil || version > maximum {
			continue
		}
		content, err := os.ReadFile(filepath.Join(source, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(target, name), content, 0o600))
	}
	return target
}
