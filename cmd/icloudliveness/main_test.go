package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/icloud"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOptionsRequireBoundedExplicitExecution(t *testing.T) {
	opts, err := parseOptions(nil, io.Discard)
	require.NoError(t, err)
	require.False(t, opts.apply)
	require.Equal(t, 1, opts.limit)
	require.Equal(t, "2026-11-08T00:00:00+08:00", opts.from.Format(time.RFC3339))
	opts, err = parseOptions([]string{"-resource-id", "10", "-recheck"}, io.Discard)
	require.NoError(t, err)
	require.True(t, opts.recheck)
	opts, err = parseOptions([]string{"-limit", "55", "-recheck"}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, 55, opts.limit)
	require.True(t, opts.recheck)
	for _, args := range [][]string{
		{"-limit", "0"}, {"-limit", "101"}, {"-timeout", "0s"}, {"-watch"},
		{"-apply", "-operator-user-id", "0"}, {"-resource-id", "41", "-limit", "10"},
		{"-expire-from", "2026-11-10", "-expire-until", "2026-11-08"}, {"password"},
		{"-recheck", "-resource-id", "10", "-limit", "10"},
	} {
		_, err := parseOptions(args, io.Discard)
		require.Error(t, err, args)
	}
}

func commandTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, version INTEGER)`,
		`CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, status TEXT, alias_count INTEGER, expire_at DATETIME, validation_generation INTEGER)`,
		`CREATE TABLE icloud_resource_channels (resource_id INTEGER, kind TEXT, session_status TEXT)`,
		`CREATE TABLE icloud_aliases (resource_id INTEGER, status TEXT)`,
		`CREATE TABLE icloud_maintenance_runs (id INTEGER PRIMARY KEY, resource_id INTEGER, kind TEXT, status TEXT, queued_at DATETIME, probe_sender TEXT, probe_recipient TEXT, probe_sent_at DATETIME, next_check_at DATETIME, last_safe_error TEXT)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}

func TestSelectionRespectsExpiryCookieHealthAndRecentChecks(t *testing.T) {
	db := commandTestDB(t)
	for _, row := range []struct {
		id                     int
		expiry, health, cookie string
		aliases                int
	}{
		{1, "2026-11-08 00:01:00", "normal", "invalid", 709},
		{2, "2026-11-07 23:59:59", "normal", "invalid", 709},
		{3, "2026-11-10 00:00:00", "normal", "invalid", 709},
		{4, "2026-11-09 23:59:59", "normal", "invalid", 673},
		{5, "2026-11-08 12:00:00", "normal", "invalid", 750},
		{6, "2026-11-08 12:00:00", "normal", "valid", 709},
		{7, "2026-11-08 12:00:00", "abnormal", "invalid", 709},
		{8, "2026-11-08 12:00:00", "normal", "invalid", 0},
		{9, "2026-11-08 12:00:00", "normal", "invalid", 709},
		{10, "2026-11-08 12:00:00", "normal", "invalid", 709},
	} {
		require.NoError(t, db.Exec("INSERT INTO email_resources VALUES (?, 'icloud', 3)", row.id).Error)
		require.NoError(t, db.Exec("INSERT INTO icloud_resources VALUES (?, ?, ?, ?, 5)", row.id, row.health, row.aliases, row.expiry).Error)
		require.NoError(t, db.Exec("INSERT INTO icloud_resource_channels VALUES (?, 'apple_account', ?)", row.id, row.cookie).Error)
		if row.id != 9 {
			require.NoError(t, db.Exec("INSERT INTO icloud_aliases VALUES (?, 'normal')", row.id).Error)
		}
	}
	require.NoError(t, db.Exec("INSERT INTO icloud_maintenance_runs(id, resource_id, kind, queued_at) VALUES (1, 10, 'liveness', ?)", time.Now().Add(-time.Hour)).Error)
	opts, err := parseOptions([]string{"-limit", "10"}, io.Discard)
	require.NoError(t, err)
	rows, err := selectCandidates(context.Background(), db, opts)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, uint(1), rows[0].ID)
	require.Equal(t, uint(4), rows[1].ID)
	opts.resourceID, opts.limit = 1, 1
	rows, err = selectCandidates(context.Background(), db, opts)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint(1), rows[0].ID)
	opts.resourceID = 10
	rows, err = selectCandidates(context.Background(), db, opts)
	require.NoError(t, err)
	require.Empty(t, rows, "recently checked resources remain excluded by default")
	opts.recheck = true
	rows, err = selectCandidates(context.Background(), db, opts)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint(10), rows[0].ID)
	for _, id := range []uint{2, 3, 5, 6, 7, 8, 9} {
		opts.resourceID = id
		rows, err = selectCandidates(context.Background(), db, opts)
		require.NoError(t, err)
		require.Empty(t, rows, "recheck must preserve eligibility conditions for resource %d", id)
	}
	opts.resourceID, opts.limit = 0, 55
	rows, err = selectCandidates(context.Background(), db, opts)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []uint{1, 4, 10}, []uint{rows[0].ID, rows[1].ID, rows[2].ID})
}

func TestMonitorVerifiesPersistedAbnormalStateAndKeepsPrivateMailFactsHidden(t *testing.T) {
	db := commandTestDB(t)
	require.NoError(t, db.Exec("INSERT INTO icloud_resources(id, status) VALUES (41, 'abnormal')").Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_maintenance_runs(id, resource_id, kind, status, probe_sender, probe_recipient, last_safe_error)
VALUES (17, 41, 'liveness', 'failed', 'private-probe@relay.example', 'private-alias@icloud.com', 'iCloud alias rejected the liveness email.')`).Error)
	var output bytes.Buffer
	err := monitor(context.Background(), icloud.NewService(db, nil, nil), db,
		[]checkRun{{ID: 17, ResourceID: 41}}, false, json.NewEncoder(&output))
	require.NoError(t, err)
	var result observation
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.Persisted)
	require.Equal(t, "abnormal", result.ResourceStatus)
	require.False(t, strings.Contains(output.String(), "private-probe"))
	require.False(t, strings.Contains(output.String(), "private-alias"))
}

func TestMonitorReportsReceiptWithoutSMTPAcknowledgement(t *testing.T) {
	for _, kind := range []string{"liveness", "liveness_smtp"} {
		t.Run(kind, func(t *testing.T) {
			db := commandTestDB(t)
			require.NoError(t, db.Exec("INSERT INTO icloud_resources(id, status) VALUES (41, 'normal')").Error)
			require.NoError(t, db.Exec(`INSERT INTO icloud_maintenance_runs(id, resource_id, kind, status, probe_sender, probe_recipient)
VALUES (17, 41, ?, 'succeeded', 'private-probe@relay.example', 'private-alias@icloud.com')`, kind).Error)
			var output bytes.Buffer
			require.NoError(t, monitor(context.Background(), icloud.NewService(db, nil, nil), db,
				[]checkRun{{ID: 17, ResourceID: 41}}, false, json.NewEncoder(&output)))
			var result observation
			require.NoError(t, json.Unmarshal(output.Bytes(), &result))
			require.True(t, result.Persisted)
			require.True(t, result.ProbeReceived)
		})
	}
}

type senderFunc func(context.Context, maildomain.OutboundMessage) error

func (f senderFunc) Send(ctx context.Context, message maildomain.OutboundMessage) error {
	return f(ctx, message)
}

func TestDiagnosticSenderKeepsFailureClassificationAndSendCount(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "accepted=true"},
		{&mailapp.OutboundSendFailure{RecipientRejected: true, SafeMessage: "SMTP rejected recipient"}, "recipient_rejected=true retryable=false"},
		{errors.New("connection unavailable"), "transport_error=true"},
	} {
		var logs bytes.Buffer
		calls := 0
		sender := diagnosticSender{stderr: &logs, SenderPort: senderFunc(func(context.Context, maildomain.OutboundMessage) error { calls++; return tc.err })}
		require.Equal(t, tc.err, sender.Send(context.Background(), maildomain.OutboundMessage{To: "private-alias@icloud.com"}))
		require.Equal(t, 1, calls)
		require.Contains(t, logs.String(), tc.want)
		require.NotContains(t, logs.String(), "private-alias")
	}
}
