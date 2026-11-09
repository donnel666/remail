package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rebindTestDB(t *testing.T) (*gorm.DB, snapshot) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&coreinfra.EmailResourceModel{}, &coreinfra.MicrosoftResourceModel{}, &mailinfra.MicrosoftBindingMailboxModel{}, &governanceinfra.OperationLogModel{}))
	require.NoError(t, db.Exec(`ALTER TABLE microsoft_binding_mailboxes ADD COLUMN active_binding_address TEXT GENERATED ALWAYS AS (CASE WHEN status <> 'expired' THEN binding_address ELSE NULL END)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE microsoft_allocations (resource_id INTEGER,status TEXT)`).Error)
	require.NoError(t, db.Create(&coreinfra.EmailResourceModel{ID: 1, Type: "microsoft", OwnerUserID: 1, Version: 1}).Error)
	require.NoError(t, db.Create(&coreinfra.MicrosoftResourceModel{ID: 1, EmailAddress: "one@outlook.com", Password: "pw", ClientID: "client", RefreshToken: "token", Status: "normal", CredentialRevision: 1}).Error)
	require.NoError(t, db.Create(&mailinfra.MicrosoftBindingMailboxModel{ID: 1, ResourceID: 1, ResourceType: "microsoft", OwnerUserID: 1, AccountEmail: "one@outlook.com", BindingAddress: "old@aishop6.com", Status: "verified"}).Error)
	s, err := loadSnapshot(context.Background(), db, candidate{ID: 1, Email: "one@outlook.com"})
	require.NoError(t, err)
	require.Equal(t, uint(1), s.ID)
	require.Equal(t, uint(1), s.BindingID)
	require.Equal(t, "token", s.RefreshToken)
	return db, s
}

func TestCommitRebindDisablesAndChangesBindingAtomically(t *testing.T) {
	db, s := rebindTestDB(t)
	r := receipt{Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, RequestID: "rebind-test"}
	require.NoError(t, commitRebind(context.Background(), db, r))
	var resource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&resource, 1).Error)
	require.Equal(t, "disabled", resource.Status)
	require.Equal(t, "pw", resource.Password)
	require.Equal(t, "token", resource.RefreshToken)
	var binding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.First(&binding, 1).Error)
	require.Equal(t, "one@fan7z.com", binding.BindingAddress)
	require.Equal(t, "verified", binding.Status)
	require.NoError(t, commitRebind(context.Background(), db, r))
	var count int64
	require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCommitAdditionalRecoveryMailboxAuditsThatOldProofWasRetained(t *testing.T) {
	db, s := rebindTestDB(t)
	r := receipt{Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, OldProofRetained: true, RequestID: "retain-old-test"}
	require.NoError(t, commitRebind(context.Background(), db, r))
	var summary string
	require.NoError(t, db.Table("operation_logs").Select("safe_summary").Where("request_id=?", r.RequestID).Scan(&summary).Error)
	require.Contains(t, summary, "retained as requested")
	require.NotContains(t, summary, "removed")
}

func TestCommitRebindRejectsAllocationAndRollsBackAuditFailure(t *testing.T) {
	for _, scenario := range []string{"allocation", "audit_failure", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			db, s := rebindTestDB(t)
			switch scenario {
			case "allocation":
				require.NoError(t, db.Exec(`INSERT INTO microsoft_allocations VALUES (1,'allocated')`).Error)
			case "audit_failure":
				require.NoError(t, db.Exec(`DROP TABLE operation_logs`).Error)
			case "stale":
				require.NoError(t, db.Exec(`UPDATE email_resources SET version=2 WHERE id=1`).Error)
			}
			err := commitRebind(context.Background(), db, receipt{Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, RequestID: "rebind-test"})
			require.Error(t, err)
			var resource coreinfra.MicrosoftResourceModel
			require.NoError(t, db.First(&resource, 1).Error)
			require.Equal(t, "normal", resource.Status)
			var binding mailinfra.MicrosoftBindingMailboxModel
			require.NoError(t, db.First(&binding, 1).Error)
			require.Equal(t, "old@aishop6.com", binding.BindingAddress)
		})
	}
}

func TestCommitRebindCreatesMissingLocalBinding(t *testing.T) {
	db, _ := rebindTestDB(t)
	require.NoError(t, db.Exec(`DELETE FROM microsoft_binding_mailboxes`).Error)
	s, err := loadSnapshot(context.Background(), db, candidate{ID: 1, Email: "one@outlook.com"})
	require.NoError(t, err)
	require.Zero(t, s.BindingID)
	require.True(t, s.BindingUpdatedAt.IsZero())
	require.NoError(t, commitRebind(context.Background(), db, receipt{Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, RequestID: "missing-binding-test"}))
	var binding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id=1").First(&binding).Error)
	require.Equal(t, "one@fan7z.com", binding.BindingAddress)
}

func TestAbnormalAccountRequiresExplicitManifestStatusAndDisablesWithoutMarkingNormal(t *testing.T) {
	db, _ := rebindTestDB(t)
	require.NoError(t, db.Exec(`UPDATE microsoft_resources SET status='abnormal' WHERE id=1`).Error)
	target := candidate{ID: 1, Email: "one@outlook.com"}
	_, err := loadSnapshot(context.Background(), db, target)
	require.Error(t, err)
	target.ExpectedStatus = "abnormal"
	s, err := loadSnapshot(context.Background(), db, target)
	require.NoError(t, err)
	r := receipt{Operation: "bind", Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, RequestID: "initial-bind-test"}
	require.NoError(t, commitRebind(context.Background(), db, r))
	var resource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&resource, 1).Error)
	require.Equal(t, "disabled", resource.Status)
	var summary string
	require.NoError(t, db.Table("operation_logs").Select("safe_summary").Where("request_id=?", r.RequestID).Scan(&summary).Error)
	require.Contains(t, summary, "initial recovery mailbox")
	require.NotContains(t, summary, "removed")
}

func TestExternalAccountDoesNotCreateOrOverwriteSystemResources(t *testing.T) {
	db, _ := rebindTestDB(t)
	target := candidate{Email: "external@outlook.com", Password: "source-password"}
	s, err := loadSnapshot(context.Background(), db, target)
	require.NoError(t, err)
	require.Zero(t, s.ID)
	require.Empty(t, s.ClientID)
	require.Empty(t, s.RefreshToken)
	r := receipt{Snapshot: s, Target: targetAddress(s.Email), RemoteVerified: true}
	require.NoError(t, commitRebind(context.Background(), db, r))
	var count int64
	require.NoError(t, db.Table("microsoft_resources").Count(&count).Error)
	require.EqualValues(t, 1, count)
	target.Email = "one@outlook.com"
	_, err = loadSnapshot(context.Background(), db, target)
	require.ErrorContains(t, err, "exact resource ID")
	r.Snapshot.Email, r.Target = target.Email, targetAddress(target.Email)
	require.ErrorContains(t, commitRebind(context.Background(), db, r), "exact resource ID")
}

func TestExportFailureRetainsCommittedReceiptForRepair(t *testing.T) {
	db, s := rebindTestDB(t)
	saved := receipt{Snapshot: s, Target: "one@fan7z.com", RemoteVerified: true, Committed: true, RequestID: "export-repair-test"}
	require.NoError(t, commitRebind(context.Background(), db, saved))
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "receipts"), 0700))
	require.NoError(t, writePrivateJSON(filepath.Join(dir, "receipts", "1.json"), saved))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "success.txt"), 0700))
	r := runtime{db: db, dir: dir, completed: map[string]receipt{}}
	err := r.process(context.Background(), candidate{ID: 1, Email: "one@outlook.com"})
	var partial *incompleteRebindError
	require.True(t, errors.As(err, &partial))
	require.Equal(t, "export_pending", partial.stage)
	require.Empty(t, r.completed)
	require.NoError(t, os.Remove(filepath.Join(dir, "success.txt")))
	require.NoError(t, r.process(context.Background(), candidate{ID: 1, Email: "one@outlook.com"}))
	data, err := os.ReadFile(filepath.Join(dir, "success.txt"))
	require.NoError(t, err)
	require.Equal(t, "one@outlook.com----pw----client----token----one@fan7z.com\n", string(data))
	var count int64
	require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
