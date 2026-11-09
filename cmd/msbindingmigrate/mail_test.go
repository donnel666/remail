package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/stretchr/testify/require"
)

func TestMailVerificationRequiresRealFetchAndFencedCommit(t *testing.T) {
	for _, scenario := range []string{"success", "imap", "failed_fetch", "wrong_protocol", "changed_credentials", "changed_binding", "changed_owner", "changed_generation", "disabled", "audit_failure"} {
		t.Run(scenario, func(t *testing.T) {
			db := fixture(t, 1)
			ctx := context.Background()
			require.NoError(t, db.Table("microsoft_resources").Where("id=1").Updates(map[string]any{"client_id": "", "refresh_token": ""}).Error)
			i, c := prepared(t, db)
			require.NoError(t, commit(ctx, db, i, c))
			s, err := loadSnapshot(ctx, db, 1, false)
			require.NoError(t, err)
			saved := mailCheckpoint{Snapshot: s, ClientID: "working-client", RefreshToken: "working-token"}
			path := filepath.Join(t.TempDir(), "mail-1.json")
			require.NoError(t, writeCheckpoint(path, saved))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &saved))
			require.True(t, sameMailSnapshot(saved.Snapshot, s))
			result := mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}
			switch scenario {
			case "imap":
				result.Protocol = "imap"
			case "failed_fetch":
				result.Valid = false
			case "wrong_protocol":
				result.Protocol = "unknown"
			case "changed_credentials":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET password='other' WHERE id=1`).Error)
			case "changed_binding":
				require.NoError(t, db.Exec(`UPDATE microsoft_binding_mailboxes SET binding_address='other@aishop6.com' WHERE resource_id=1`).Error)
			case "changed_owner":
				require.NoError(t, db.Exec(`UPDATE email_resources SET owner_user_id=2 WHERE id=1`).Error)
			case "changed_generation":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET validation_generation=validation_generation+1 WHERE id=1`).Error)
			case "disabled":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET status='disabled' WHERE id=1`).Error)
			case "audit_failure":
				require.NoError(t, db.Exec(`DROP TABLE operation_logs`).Error)
			}
			err = commitMailVerification(ctx, db, saved, result)
			var ledger item
			require.NoError(t, db.First(&ledger, 1).Error)
			var resource coreinfra.MicrosoftResourceModel
			require.NoError(t, db.First(&resource, 1).Error)
			if scenario != "success" && scenario != "imap" {
				require.Equal(t, "mail_pending", ledger.State)
				require.Error(t, err)
				require.Empty(t, resource.ClientID)
				require.Empty(t, resource.RefreshToken)
				require.NotEqual(t, "normal", resource.Status)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "success", ledger.State)
			require.Equal(t, "normal", resource.Status)
			require.Equal(t, "working-client", resource.ClientID)
			require.Equal(t, "working-token", resource.RefreshToken)
			require.Equal(t, "fixture-password", resource.Password)
			require.Equal(t, s.CredentialRevision+1, resource.CredentialRevision)
			require.Equal(t, result.Protocol == "graph", resource.GraphAvailable)
			require.True(t, resource.ForSale)
			var count int64
			require.NoError(t, db.Table("microsoft_allocations").Where("resource_id=1 AND status='allocated'").Count(&count).Error)
			require.EqualValues(t, 1, count)
			require.NoError(t, db.Table("operation_logs").Where("operation_type='mailtransport.microsoft.binding_domain_mail_verify' AND result='success'").Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestMailVerificationCannotExpandOrMixMigrationOptions(t *testing.T) {
	for _, cfg := range []config{
		{limit: 1, ids: []uint{1}},
		{apply: true, limit: 2, ids: []uint{1, 2}},
		{apply: true, limit: 1, ids: []uint{1}, retryFailed: true},
		{apply: true, limit: 1, ids: []uint{1}, confirmedOldMailbox: "old@lclaitech.com"},
	} {
		cfg.dir, cfg.concurrency, cfg.verifyMail = t.TempDir(), 1, true
		require.ErrorContains(t, run(context.Background(), cfg), "mail verification requires one exact apply trial")
	}
}

func TestOriginallyAbnormalMailboxNeedsFetchEvenWithExistingTokens(t *testing.T) {
	db := fixture(t, 1)
	ctx := context.Background()
	require.NoError(t, db.Table("microsoft_resources").Where("id=1").Update("status", "abnormal").Error)
	i, c := prepared(t, db)
	require.NoError(t, commit(ctx, db, i, c))
	s, err := loadSnapshot(ctx, db, 1, false)
	require.NoError(t, err)
	require.Equal(t, "abnormal", s.Status)
	saved := mailCheckpoint{Snapshot: s, ClientID: s.ClientID, RefreshToken: s.RefreshToken}
	require.NoError(t, commitMailVerification(ctx, db, saved, mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}))
	verified, err := loadSnapshot(ctx, db, 1, false)
	require.NoError(t, err)
	require.Equal(t, "normal", verified.Status)
	require.Equal(t, s.CredentialRevision, verified.CredentialRevision)
}

func TestMailVerificationRejectsOwnershipTransferBeforeSelection(t *testing.T) {
	db := fixture(t, 1)
	ctx := context.Background()
	i, c := prepared(t, db)
	require.NoError(t, commit(ctx, db, i, c))
	require.NoError(t, db.Exec(`UPDATE email_resources SET owner_user_id=2 WHERE id=1`).Error)
	s, err := loadSnapshot(ctx, db, 1, false)
	require.NoError(t, err)
	require.Error(t, migratedMailbox(ctx, db, s))
}

func TestMailLockSkipKeepsConfirmedBindingAndRejectsStaleSnapshot(t *testing.T) {
	for _, changed := range []bool{false, true} {
		db := fixture(t, 1)
		ctx := context.Background()
		i, c := prepared(t, db)
		require.NoError(t, commit(ctx, db, i, c))
		s, err := loadSnapshot(ctx, db, 1, false)
		require.NoError(t, err)
		if changed {
			require.NoError(t, db.Table("microsoft_resources").Where("id=1").Update("refresh_token", "newer-token").Error)
		}
		err = skipLockedMail(ctx, db, s)
		if changed {
			require.ErrorIs(t, err, errFence)
			continue
		}
		require.NoError(t, err)
		current, err := loadSnapshot(ctx, db, 1, false)
		require.NoError(t, err)
		require.Equal(t, "abnormal", current.Status)
		require.Equal(t, i.TargetAddress, current.BindingAddress)
		require.Equal(t, digest(s), digest(current))
		var row item
		require.NoError(t, db.First(&row, 1).Error)
		require.Equal(t, "skipped", row.State)
		require.Equal(t, "mail_account_locked", row.Phase)
		require.Equal(t, i.TargetAddress, row.TargetAddress)
	}
}
