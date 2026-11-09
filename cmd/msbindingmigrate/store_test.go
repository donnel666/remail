package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var mysqlTest = testmysql.New("remail_binding_migrate_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = mysqlTest.Close(context.Background())
	os.Exit(code)
}

func fixture(t *testing.T, count int) *gorm.DB {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "00001_empty.sql"), []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"), 0600))
	db := mysqlTest.Database(t, dir).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, db.AutoMigrate(&coreinfra.EmailResourceModel{}, &coreinfra.MicrosoftResourceModel{}, &mailinfra.MicrosoftBindingMailboxModel{}, &governanceinfra.OperationLogModel{}))
	require.NoError(t, db.Exec(`ALTER TABLE microsoft_binding_mailboxes ADD COLUMN active_binding_address VARCHAR(320) GENERATED ALWAYS AS (CASE WHEN status<>'expired' THEN binding_address ELSE NULL END) STORED, ADD UNIQUE INDEX test_binding_address (active_binding_address)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE microsoft_allocations(resource_id BIGINT UNSIGNED,status VARCHAR(32),allocation_value VARCHAR(255))`).Error)
	var roots []coreinfra.EmailResourceModel
	var resources []coreinfra.MicrosoftResourceModel
	var bindings []mailinfra.MicrosoftBindingMailboxModel
	for n := 1; n <= count; n++ {
		id := uint(n)
		email := fmt.Sprintf("account%d@outlook.com", n)
		roots = append(roots, coreinfra.EmailResourceModel{ID: id, Type: "microsoft", OwnerUserID: 1, Version: 3})
		resources = append(resources, coreinfra.MicrosoftResourceModel{ID: id, EmailAddress: email, Password: "fixture-password", ClientID: "fixture-client", RefreshToken: "fixture-token", Status: "normal", CredentialRevision: 2, CredentialUpdatedAt: time.Now().UTC(), ValidationGeneration: 4, ForSale: true, GraphAvailable: true, QualityScore: 100})
		bindings = append(bindings, mailinfra.MicrosoftBindingMailboxModel{ID: id + 10000, ResourceID: id, ResourceType: "microsoft", OwnerUserID: 1, AccountEmail: email, BindingAddress: fmt.Sprintf("old%d@wsu.edu.kg", n), Status: "verified", UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)})
	}
	require.NoError(t, db.CreateInBatches(roots, 500).Error)
	require.NoError(t, db.CreateInBatches(resources, 500).Error)
	require.NoError(t, db.CreateInBatches(bindings, 500).Error)
	require.NoError(t, db.Exec(`INSERT INTO microsoft_allocations VALUES(1,'allocated','preserve-existing-allocation')`).Error)
	require.NoError(t, seed(context.Background(), db))
	return db
}

func prepared(t *testing.T, db *gorm.DB) (item, checkpoint) {
	t.Helper()
	ctx := context.Background()
	rows, err := claim(ctx, db, 1, []uint{1}, false)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	i := rows[0]
	i.ResolvedSource = i.SourceAddress
	i.TargetAddress, err = destination(i.ResolvedSource)
	require.NoError(t, err)
	require.NoError(t, saveItem(ctx, db, i, map[string]any{"resolved_source": i.ResolvedSource, "target_address": i.TargetAddress, "state": "running"}))
	c := checkpoint{ResourceID: i.ResourceID, Generation: i.Generation, Target: i.TargetAddress, Result: msacl.RebindResult{BindingAddress: i.TargetAddress, Verified: true, OldProofRemoved: true, Stage: "remote_complete"}}
	return i, c
}

func TestClaimCapsAt1000AndConcurrentClaimsDoNotOverlap(t *testing.T) {
	db := fixture(t, 1005)
	ctx := context.Background()
	_, err := claim(ctx, db, 1001, nil, false)
	require.Error(t, err)
	var results [2][]item
	var errs [2]error
	var wg sync.WaitGroup
	for n := range results {
		wg.Add(1)
		go func(n int) { defer wg.Done(); results[n], errs[n] = claim(ctx, db, 1000, nil, false) }(n)
	}
	wg.Wait()
	seen := map[uint]bool{}
	for n, rows := range results {
		require.NoError(t, errs[n])
		require.LessOrEqual(t, len(rows), 1000)
		for _, i := range rows {
			require.False(t, seen[i.ResourceID])
			seen[i.ResourceID] = true
			require.Equal(t, uint64(5), i.Generation)
		}
	}
	require.Len(t, seen, 1005)
	var pending int64
	require.NoError(t, db.Table("microsoft_resources").Where("status='pending' AND validation_generation=5 AND graph_available=1 AND for_sale=1").Count(&pending).Error)
	require.EqualValues(t, 1005, pending)
	rows, err := claim(ctx, db, 1000, nil, false)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestClaimRollsBackWholeBatchOnChangedBinding(t *testing.T) {
	db := fixture(t, 2)
	require.NoError(t, db.Table("microsoft_binding_mailboxes").Where("resource_id=2").Update("binding_address", "changed@wsu.edu.kg").Error)
	_, err := claim(context.Background(), db, 2, nil, false)
	require.ErrorIs(t, err, errFence)
	var normal int64
	require.NoError(t, db.Table("microsoft_resources").Where("status='normal' AND validation_generation=4").Count(&normal).Error)
	require.EqualValues(t, 2, normal)
}

func TestCommitPreservesSaleAllocationsCredentialsAndFencesOldCleaner(t *testing.T) {
	db := fixture(t, 1)
	ctx := context.Background()
	i, c := prepared(t, db)
	task := coreapp.ResourceValidationTask{ResourceID: 1, OwnerUserID: 1, ValidationGeneration: 4, ExpectedCredentialRevision: 2}
	err := coreinfra.NewResourceValidationRepo(db).ApplyMicrosoftResult(ctx, task, coreapp.MicrosoftValidationResult{Valid: true, RefreshToken: "stale-cleaner-token"}, nil)
	require.ErrorIs(t, err, coreapp.ErrValidationResultStale)
	path := filepath.Join(t.TempDir(), "1.json")
	require.NoError(t, writeCheckpoint(path, c))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var resumed checkpoint
	require.NoError(t, json.Unmarshal(data, &resumed))
	require.True(t, resumed.validFor(i))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	r := &runtime{db: db, dir: filepath.Dir(path)}
	require.NoError(t, r.process(ctx, i, config{}))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, commit(ctx, db, i, resumed))
	var resource coreinfra.MicrosoftResourceModel
	require.NoError(t, db.First(&resource, 1).Error)
	require.Equal(t, "abnormal", resource.Status)
	var ledger item
	require.NoError(t, db.First(&ledger, 1).Error)
	require.Equal(t, "mail_pending", ledger.State)
	require.True(t, resource.ForSale)
	require.True(t, resource.GraphAvailable)
	require.Equal(t, 100, resource.QualityScore)
	require.Equal(t, "fixture-password", resource.Password)
	require.Equal(t, "fixture-client", resource.ClientID)
	require.Equal(t, "fixture-token", resource.RefreshToken)
	require.Equal(t, uint64(2), resource.CredentialRevision)
	var binding mailinfra.MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id=1").Take(&binding).Error)
	require.Equal(t, "old1@aishop6.com", binding.BindingAddress)
	require.Equal(t, "verified", binding.Status)
	var allocation string
	require.NoError(t, db.Table("microsoft_allocations").Select("allocation_value").Where("resource_id=1 AND status='allocated'").Scan(&allocation).Error)
	require.Equal(t, "preserve-existing-allocation", allocation)
	var count int64
	require.NoError(t, db.Table("operation_logs").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCommitKeepsMissingMailCredentialsUnavailable(t *testing.T) {
	for _, credentials := range []struct{ client, token string }{
		{"", "fixture-token"},
		{"fixture-client", ""},
		{" \t", " \n"},
	} {
		t.Run(fmt.Sprintf("client=%q/token=%q", credentials.client, credentials.token), func(t *testing.T) {
			db := fixture(t, 1)
			ctx := context.Background()
			require.NoError(t, db.Table("microsoft_resources").Where("id=1").Updates(map[string]any{"client_id": credentials.client, "refresh_token": credentials.token}).Error)
			i, c := prepared(t, db)
			require.NoError(t, commit(ctx, db, i, c))
			s, err := loadSnapshot(ctx, db, 1, false)
			require.NoError(t, err)
			require.Equal(t, "abnormal", s.Status)
			require.Equal(t, i.CredentialDigest, digest(s))
			require.Equal(t, i.TargetAddress, s.BindingAddress)
			var resource coreinfra.MicrosoftResourceModel
			require.NoError(t, db.First(&resource, 1).Error)
			require.True(t, resource.ForSale)
			require.Equal(t, "Microsoft mail fetch credentials are incomplete.", resource.LastSafeError)
			var saved item
			require.NoError(t, db.First(&saved, 1).Error)
			require.Equal(t, "mail_pending", saved.State)
			var audit governanceinfra.OperationLogModel
			require.NoError(t, db.First(&audit).Error)
			require.Contains(t, audit.SafeSummary, "Resource status: abnormal;")
		})
	}
}

func TestCommitRejectsStaleOrIncompleteAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"generation", "credentials", "revision", "binding", "owner", "disabled", "audit_failure", "unverified", "old_not_removed", "wrong_target"} {
		t.Run(scenario, func(t *testing.T) {
			db := fixture(t, 1)
			i, c := prepared(t, db)
			switch scenario {
			case "generation":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET validation_generation=6 WHERE id=1`).Error)
			case "credentials":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET password='new-password' WHERE id=1`).Error)
			case "revision":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET credential_revision=3 WHERE id=1`).Error)
			case "binding":
				require.NoError(t, db.Exec(`UPDATE microsoft_binding_mailboxes SET binding_address='changed@wsu.edu.kg' WHERE resource_id=1`).Error)
			case "owner":
				require.NoError(t, db.Exec(`UPDATE email_resources SET owner_user_id=2 WHERE id=1`).Error)
			case "disabled":
				require.NoError(t, db.Exec(`UPDATE microsoft_resources SET status='disabled' WHERE id=1`).Error)
			case "audit_failure":
				require.NoError(t, db.Exec(`DROP TABLE operation_logs`).Error)
			case "unverified":
				c.Result.Verified = false
			case "old_not_removed":
				c.Result.OldProofRemoved = false
			case "wrong_target":
				c.Target = "different@aishop6.com"
			}
			require.Error(t, commit(context.Background(), db, i, c))
			var status string
			require.NoError(t, db.Table("microsoft_resources").Select("status").Where("id=1").Scan(&status).Error)
			require.NotEqual(t, "normal", status)
			var address string
			require.NoError(t, db.Table("microsoft_binding_mailboxes").Select("binding_address").Where("resource_id=1").Scan(&address).Error)
			require.NotContains(t, address, "@aishop6.com")
		})
	}
}

func TestAddressMappingAndPartialExternalProofClassification(t *testing.T) {
	target, err := destination("ocom_abc@wsu.edu.kg")
	require.NoError(t, err)
	require.Equal(t, "ocom_abc@aishop6.com", target)
	for _, source := range []string{"oc***@wsu.edu.kg", "other@gmail.com", "name <old@wsu.edu.kg>", "old@wsu.edu.kg\n"} {
		_, err := destination(source)
		require.Error(t, err)
	}
	external := &msacl.AuthError{Status: msacl.AuthStatusAlreadyBound, BoundMailbox: "someone@qq.com"}
	require.Equal(t, "skipped", failureState(false, false, external))
	require.Equal(t, "partial", failureState(true, false, external))
	locked := &msacl.AuthError{Status: msacl.AuthStatusAccountLocked}
	require.Equal(t, "skipped", failureState(false, true, locked))
	require.Equal(t, "failed", failureState(false, false, locked))
	require.Equal(t, "partial", failureState(true, true, locked))
	for _, status := range []string{msacl.AuthStatusRateLimited, msacl.AuthStatusPhoneVerification, msacl.AuthStatusMFARequired, msacl.AuthStatusPasswordError} {
		require.Equal(t, "failed", failureState(false, true, &msacl.AuthError{Status: status}))
	}
	require.False(t, beforeChange("new_proof_pending"))
	require.True(t, beforeChange("login"))
}

func TestExternalSkipRestoresOriginalStatusAndKeepsBindingAndCredentials(t *testing.T) {
	for _, status := range []string{"normal", "abnormal"} {
		t.Run(status, func(t *testing.T) {
			db := fixture(t, 1)
			ctx := context.Background()
			require.NoError(t, db.Table("microsoft_resources").Where("id=1").Update("status", status).Error)
			i, _ := prepared(t, db)
			require.NoError(t, skipExternal(ctx, db, i, "u2*****@888910.xyz"))
			require.NoError(t, skipExternal(ctx, db, i, "u2*****@888910.xyz"))
			s, err := loadSnapshot(ctx, db, 1, false)
			require.NoError(t, err)
			require.Equal(t, status, s.Status)
			require.Equal(t, "old1@wsu.edu.kg", s.BindingAddress)
			require.Equal(t, i.CredentialDigest, digest(s))
			var record item
			require.NoError(t, db.First(&record, 1).Error)
			require.Equal(t, "skipped", record.State)
			require.Empty(t, record.TargetAddress)
			var count int64
			require.NoError(t, db.Table("microsoft_allocations").Where("resource_id=1 AND status='allocated'").Count(&count).Error)
			require.EqualValues(t, 1, count)
			require.NoError(t, db.Table("operation_logs").Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
	db := fixture(t, 1)
	i, _ := prepared(t, db)
	require.NoError(t, db.Exec(`UPDATE microsoft_resources SET password='updated' WHERE id=1`).Error)
	require.ErrorIs(t, skipExternal(context.Background(), db, i, "u2*****@888910.xyz"), errFence)
}

func TestLockedSkipRetainsOriginalBindingAndRejectsUncertainChanges(t *testing.T) {
	for _, scenario := range []string{"locked", "partial", "mail_pending", "changed_credentials"} {
		t.Run(scenario, func(t *testing.T) {
			db := fixture(t, 1)
			ctx := context.Background()
			i, _ := prepared(t, db)
			switch scenario {
			case "partial", "mail_pending":
				require.NoError(t, db.Model(&item{}).Where("resource_id=1").Update("state", scenario).Error)
			case "changed_credentials":
				require.NoError(t, db.Table("microsoft_resources").Where("id=1").Update("password", "changed").Error)
			}
			err := skipLocked(ctx, db, i)
			if scenario != "locked" {
				require.ErrorIs(t, err, errFence)
				return
			}
			require.NoError(t, err)
			require.NoError(t, skipLocked(ctx, db, i))
			s, err := loadSnapshot(ctx, db, 1, false)
			require.NoError(t, err)
			require.Equal(t, "abnormal", s.Status)
			require.Equal(t, i.SourceAddress, s.BindingAddress)
			require.Equal(t, i.CredentialDigest, digest(s))
			var record item
			require.NoError(t, db.First(&record, 1).Error)
			require.Equal(t, "skipped", record.State)
			require.Equal(t, "account_locked", record.Phase)
			require.Empty(t, record.TargetAddress)
			var count int64
			require.NoError(t, db.Table("operation_logs").Where("result='skipped'").Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestConfirmedOldMailboxCannotReachOtherAccounts(t *testing.T) {
	cfg := config{dir: t.TempDir(), apply: true, limit: 1, concurrency: 1, ids: []uint{1}, confirmedOldMailbox: "known@lclaitech.com"}
	r := &runtime{}
	require.ErrorContains(t, r.runBatch(context.Background(), cfg, []item{{ResourceID: 2}}), "another unfinished account")
	require.ErrorContains(t, r.runBatch(context.Background(), cfg, []item{{ResourceID: 1}, {ResourceID: 2}}), "another unfinished account")
	for _, mutate := range []func(*config){
		func(c *config) { c.limit = 0 },
		func(c *config) { c.ids = []uint{1, 2} },
		func(c *config) { c.confirmedOldMailbox = "ma***@lclaitech.com" },
		func(c *config) { c.apply = false },
	} {
		invalid := cfg
		mutate(&invalid)
		require.ErrorContains(t, run(context.Background(), invalid), "one exact apply trial")
	}
}
