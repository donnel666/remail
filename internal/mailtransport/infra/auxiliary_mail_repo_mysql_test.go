package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuxiliaryMailRepoScopesSafeSummariesAndSingleDetailMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9201, "normal")
	createMicrosoftAliasTestResource(t, db, 9202, "normal")
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&MicrosoftBindingMailboxModel{
		ResourceID:     9201,
		ResourceType:   "microsoft",
		OwnerUserID:    9201,
		AccountEmail:   "account9201@outlook.com",
		BindingAddress: "proof9201@example.com",
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingVerified),
		UpdatedAt:      now,
	}).Error)
	require.NoError(t, db.Create(&InboundMailModel{
		EnvelopeFrom:     "private-envelope@example.net",
		HeaderFrom:       "account-security-noreply@accountprotection.microsoft.com",
		Recipient:        "proof9201@example.com",
		Subject:          "Microsoft account security code",
		BodyPreview:      "Your Microsoft account security code is 654321.",
		VerificationCode: "654321",
		MessageIDHeader:  "message-9201@example.com",
		ReceivedAt:       &now,
		ParsedAt:         &now,
		ResourceID:       9201,
		ResourceType:     "microsoft",
		OwnerUserID:      9201,
		SourceObjectKey:  "private/9201-secret.eml",
		Status:           string(domain.InboundStatusStored),
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error)
	older := now.Add(-time.Minute)
	require.NoError(t, db.Create(&InboundMailModel{
		HeaderFrom:      "literal@example.com",
		Recipient:       "proof9201@example.com",
		Subject:         `Literal % _ \\ marker`,
		BodyPreview:     `Literal % _ \\ marker`,
		ReceivedAt:      &older,
		ParsedAt:        &older,
		ResourceID:      9201,
		ResourceType:    "microsoft",
		OwnerUserID:     9201,
		SourceObjectKey: "private/9201-literal.eml",
		Status:          string(domain.InboundStatusStored),
		CreatedAt:       older,
		UpdatedAt:       older,
	}).Error)
	var first InboundMailModel
	require.NoError(t, db.Where("resource_id = ?", 9201).First(&first).Error)
	require.NoError(t, db.Create(&InboundMailModel{
		HeaderFrom:      "other@example.com",
		Recipient:       "proof9202@example.com",
		Subject:         "Other resource",
		BodyPreview:     "Do not cross resource boundaries.",
		ReceivedAt:      &now,
		ParsedAt:        &now,
		ResourceID:      9202,
		ResourceType:    "microsoft",
		OwnerUserID:     9202,
		SourceObjectKey: "private/9202-secret.eml",
		Status:          string(domain.InboundStatusStored),
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error)

	repo := NewAuxiliaryMailRepo(db)
	exists, err := repo.MicrosoftResourceExists(context.Background(), 9201)
	require.NoError(t, err)
	assert.True(t, exists)
	exists, err = repo.MicrosoftResourceExists(context.Background(), 999999)
	require.NoError(t, err)
	assert.False(t, exists)

	items, total, hasMore, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 9201,
		Search:     "654321",
		Limit:      20,
	})
	require.NoError(t, err)
	assert.False(t, hasMore)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, "654321", items[0].VerificationCode)
	assert.Empty(t, items[0].SourceObjectKey)
	assert.Empty(t, items[0].EnvelopeFrom)

	for _, wildcard := range []string{"%", "_", `\`} {
		wildcardItems, wildcardTotal, wildcardHasMore, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
			ResourceID: 9201,
			Search:     wildcard,
			Limit:      20,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, wildcardTotal)
		require.Len(t, wildcardItems, 1)
		assert.Equal(t, `Literal % _ \\ marker`, wildcardItems[0].Subject)
		assert.False(t, wildcardHasMore)
	}

	firstPage, firstTotal, firstHasMore, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 9201,
		Limit:      1,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, firstTotal)
	assert.True(t, firstHasMore)
	require.Len(t, firstPage, 1)
	secondPage, skippedTotal, secondHasMore, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID:       9201,
		Limit:            1,
		BeforeReceivedAt: firstPage[0].ReceivedAt,
		BeforeID:         firstPage[0].ID,
		SkipTotal:        true,
	})
	require.NoError(t, err)
	assert.EqualValues(t, -1, skippedTotal)
	assert.False(t, secondHasMore)
	require.Len(t, secondPage, 1)
	assert.Equal(t, `Literal % _ \\ marker`, secondPage[0].Subject)

	detail, err := repo.FindByMicrosoftResource(context.Background(), 9201, first.ID)
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Equal(t, "private/9201-secret.eml", detail.SourceObjectKey)
	crossResource, err := repo.FindByMicrosoftResource(context.Background(), 9202, first.ID)
	require.NoError(t, err)
	assert.Nil(t, crossResource)

	require.NoError(t, db.Exec("UPDATE microsoft_resources SET status = 'deleted' WHERE id = ?", 9201).Error)
	exists, err = repo.MicrosoftResourceExists(context.Background(), 9201)
	require.NoError(t, err)
	assert.True(t, exists)
	deletedItems, deletedTotal, deletedHasMore, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 9201,
		Limit:      20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, deletedTotal)
	assert.False(t, deletedHasMore)
	require.Len(t, deletedItems, 2)
	deletedDetail, err := repo.FindByMicrosoftResource(context.Background(), 9201, first.ID)
	require.NoError(t, err)
	require.NotNil(t, deletedDetail)
	assert.Equal(t, "private/9201-secret.eml", deletedDetail.SourceObjectKey)

	bindings, err := NewMicrosoftBindingRepo(db).FindByResourceIDs(context.Background(), []uint{9201, 9201, 0, 999999})
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, "proof9201@example.com", bindings[9201].BindingAddress)
}

func TestMicrosoftBindingRepoAdminInputSemanticsAndCallerTransactionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9210, "normal")
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, 'hash', 'supplier')",
		9211,
		"binding-owner-9211@test.local",
	).Error)
	verifiedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	require.NoError(t, db.Create(&MicrosoftBindingMailboxModel{
		ResourceID:     9210,
		ResourceType:   "microsoft",
		OwnerUserID:    9210,
		AccountEmail:   "old-account@outlook.com",
		BindingAddress: "preserve@example.com",
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingVerified),
		SelectedAt:     &verifiedAt,
		VerifiedAt:     &verifiedAt,
	}).Error)
	repo := NewMicrosoftBindingRepo(db)

	// An owner-only move keeps the binding fact because the Microsoft account
	// identity did not change.
	require.NoError(t, repo.ReplaceAdminInput(
		context.Background(),
		9210,
		9211,
		"old-account@outlook.com",
		false,
		nil,
	))
	current := loadBindingForAdminTest(t, db, 9210)
	assert.Equal(t, uint(9211), current.OwnerUserID)
	assert.Equal(t, "old-account@outlook.com", current.AccountEmail)
	assert.Equal(t, "preserve@example.com", current.BindingAddress)
	assert.Equal(t, string(domain.MicrosoftBindingVerified), current.Status)
	require.NotNil(t, current.VerifiedAt)
	assert.Equal(t, verifiedAt.Truncate(time.Second), current.VerifiedAt.UTC().Truncate(time.Second))

	staleTime := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 9210).
		Updates(map[string]any{
			"status":          string(domain.MicrosoftBindingFailed),
			"code_msg_id":     "stale-code-message",
			"bound_display":   "st***@external.test",
			"category":        "already_bound",
			"last_safe_error": "stale binding state",
			"code_sent_at":    staleTime,
			"verified_at":     staleTime,
			"expires_at":      staleTime.Add(time.Hour),
		}).Error)

	// Changing only the Microsoft account email preserves the candidate address,
	// but invalidates every protocol fact collected against the old account.
	require.NoError(t, repo.ReplaceAdminInput(
		context.Background(),
		9210,
		9211,
		"new-account@outlook.com",
		false,
		nil,
	))
	current = loadBindingForAdminTest(t, db, 9210)
	assert.Equal(t, uint(9211), current.OwnerUserID)
	assert.Equal(t, "new-account@outlook.com", current.AccountEmail)
	assert.Equal(t, "preserve@example.com", current.BindingAddress)
	assert.Equal(t, string(domain.MicrosoftBindingPending), current.Status)
	assert.Empty(t, current.CodeMessageID)
	assert.Empty(t, current.BoundDisplay)
	assert.Empty(t, current.Category)
	assert.Empty(t, current.LastSafeError)
	assert.Nil(t, current.CodeSentAt)
	assert.Nil(t, current.VerifiedAt)
	assert.Nil(t, current.ExpiresAt)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 9210).
		Updates(map[string]any{
			"status":          string(domain.MicrosoftBindingFailed),
			"code_msg_id":     "replacement-stale-code",
			"bound_display":   "re***@external.test",
			"category":        "already_bound",
			"last_safe_error": "replacement stale binding state",
			"code_sent_at":    staleTime,
			"verified_at":     staleTime,
			"expires_at":      staleTime.Add(time.Hour),
		}).Error)

	replacement := "Replacement@Example.com"
	require.NoError(t, repo.ReplaceAdminInput(
		context.Background(),
		9210,
		9211,
		"new-account@outlook.com",
		true,
		&replacement,
	))
	current = loadBindingForAdminTest(t, db, 9210)
	assert.Equal(t, "replacement@example.com", current.BindingAddress)
	assert.Equal(t, string(domain.MicrosoftBindingPending), current.Status)
	assert.Empty(t, current.CodeMessageID)
	assert.Empty(t, current.BoundDisplay)
	assert.Empty(t, current.Category)
	assert.Empty(t, current.LastSafeError)
	assert.Nil(t, current.CodeSentAt)
	assert.Nil(t, current.VerifiedAt)
	assert.Nil(t, current.ExpiresAt)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	txCtx := platform.WithGormTx(context.Background(), tx)
	rollbackReplacement := "rollback@example.com"
	require.NoError(t, repo.ReplaceAdminInput(txCtx, 9210, 9211, "new-account@outlook.com", true, &rollbackReplacement))
	require.NoError(t, tx.Rollback().Error)
	current = loadBindingForAdminTest(t, db, 9210)
	assert.Equal(t, "replacement@example.com", current.BindingAddress)

	require.NoError(t, repo.ReplaceAdminInput(context.Background(), 9210, 9211, "new-account@outlook.com", true, nil))
	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9210).Count(&count).Error)
	assert.Zero(t, count)
}

func TestMicrosoftBindingRepoApplyRecoveredBindingRepairsAndIsIdempotentMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9220, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "aishop6.com")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9220)
	oldTime := time.Date(2026, time.July, 14, 4, 5, 6, 0, time.UTC)
	expiresAt := oldTime.Add(time.Hour)
	record := &MicrosoftBindingMailboxModel{
		ResourceID:     9220,
		ResourceType:   "microsoft",
		OwnerUserID:    1,
		AccountEmail:   "stale-account@outlook.com",
		BindingAddress: "wrong@aishop6.com",
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingTimeout),
		CodeMessageID:  "stale-message-id",
		BoundDisplay:   "wr***@aishop6.com",
		Category:       "code_timeout",
		LastSafeError:  "stale safe error",
		SelectedAt:     &oldTime,
		CodeSentAt:     &oldTime,
		VerifiedAt:     &oldTime,
		ExpiresAt:      &expiresAt,
		CreatedAt:      oldTime,
		UpdatedAt:      oldTime,
	}
	require.NoError(t, db.Create(record).Error)
	snapshot := loadBindingForAdminTest(t, db, 9220)

	repo := NewMicrosoftBindingRepo(db)
	result, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:               9220,
		BindingAddress:           "Recovered@AISHOP6.com",
		ExpectedAccountEmail:     accountEmail,
		ExpectedBindingID:        snapshot.ID,
		ExpectedBindingAddress:   snapshot.BindingAddress,
		ExpectedBindingUpdatedAt: snapshot.UpdatedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, snapshot.ID, result.BindingID)
	assert.False(t, result.Created)
	assert.True(t, result.Changed)
	assert.Equal(t, version+1, result.ResourceVersion)
	assert.False(t, result.VerifiedAt.IsZero())

	stored := loadBindingForAdminTest(t, db, 9220)
	assert.Equal(t, uint(9220), stored.OwnerUserID)
	assert.Equal(t, accountEmail, stored.AccountEmail)
	assert.Equal(t, "recovered@aishop6.com", stored.BindingAddress)
	assert.Equal(t, "microsoft", stored.ResourceType)
	assert.Equal(t, "validation", stored.Purpose)
	assert.Equal(t, string(domain.MicrosoftBindingVerified), stored.Status)
	assert.Empty(t, stored.CodeMessageID)
	assert.Empty(t, stored.BoundDisplay)
	assert.Empty(t, stored.Category)
	assert.Empty(t, stored.LastSafeError)
	assert.NotNil(t, stored.SelectedAt)
	assert.Nil(t, stored.CodeSentAt)
	assert.NotNil(t, stored.VerifiedAt)
	assert.Nil(t, stored.ExpiresAt)
	_, versionAfterRepair := loadBindingRecoveryResource(t, db, 9220)
	assert.Equal(t, version+1, versionAfterRepair)

	passwordBefore := loadBindingRecoveryPassword(t, db, 9220)
	updatedAtBeforeReplay := stored.UpdatedAt
	verifiedAtBeforeReplay := *stored.VerifiedAt
	replay, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:               9220,
		BindingAddress:           stored.BindingAddress,
		ExpectedAccountEmail:     accountEmail,
		ExpectedBindingID:        stored.ID,
		ExpectedBindingAddress:   stored.BindingAddress,
		ExpectedBindingUpdatedAt: stored.UpdatedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, replay)
	assert.False(t, replay.Created)
	assert.False(t, replay.Changed)
	assert.Equal(t, versionAfterRepair, replay.ResourceVersion)

	replayed := loadBindingForAdminTest(t, db, 9220)
	assert.Equal(t, updatedAtBeforeReplay, replayed.UpdatedAt)
	require.NotNil(t, replayed.VerifiedAt)
	assert.Equal(t, verifiedAtBeforeReplay, *replayed.VerifiedAt)
	_, versionAfterReplay := loadBindingRecoveryResource(t, db, 9220)
	assert.Equal(t, versionAfterRepair, versionAfterReplay)
	assert.Equal(t, passwordBefore, loadBindingRecoveryPassword(t, db, 9220))
}

func TestMicrosoftBindingRepoApplyRecoveredBindingJoinsCallerTransactionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9221, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "aishop6.com")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9221)
	repo := NewMicrosoftBindingRepo(db)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	txCtx := platform.WithGormTx(context.Background(), tx)
	result, err := repo.ApplyRecoveredBinding(txCtx, MicrosoftRecoveredBindingInput{
		ResourceID:           9221,
		BindingAddress:       "rollback-recovered@aishop6.com",
		ExpectedAccountEmail: accountEmail,
	})
	require.NoError(t, err)
	require.True(t, result.Created)
	require.True(t, result.Changed)
	require.NoError(t, tx.Rollback().Error)

	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9221).Count(&count).Error)
	assert.Zero(t, count)
	_, versionAfterRollback := loadBindingRecoveryResource(t, db, 9221)
	assert.Equal(t, version, versionAfterRollback)

	committed, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:           9221,
		BindingAddress:       "committed-recovered@aishop6.com",
		ExpectedAccountEmail: accountEmail,
	})
	require.NoError(t, err)
	require.True(t, committed.Created)
	require.True(t, committed.Changed)
	assert.Equal(t, version+1, committed.ResourceVersion)
	stored := loadBindingForAdminTest(t, db, 9221)
	assert.Equal(t, "committed-recovered@aishop6.com", stored.BindingAddress)
	assert.Equal(t, string(domain.MicrosoftBindingVerified), stored.Status)
}

func TestMicrosoftBindingRepoApplyRecoveredBindingForValidationRequiresActiveDomainAndDefersRootVersionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9228, "normal")
	createMicrosoftAliasTestResource(t, db, 9229, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "recovery-active.test")
	repo := NewMicrosoftBindingRepo(db)
	account9228, version9228 := loadBindingRecoveryResource(t, db, 9228)

	_, err := repo.ApplyRecoveredBindingForValidation(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:           9228,
		BindingAddress:       "candidate@recovery-active.test",
		ExpectedAccountEmail: account9228,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryTransaction)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	result, err := repo.ApplyRecoveredBindingForValidation(
		platform.WithGormTx(context.Background(), tx),
		MicrosoftRecoveredBindingInput{
			ResourceID:           9228,
			BindingAddress:       "candidate@recovery-active.test",
			ExpectedAccountEmail: account9228,
		},
	)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.True(t, result.Changed)
	require.Equal(t, version9228, result.ResourceVersion)

	var rootVersionInTx uint64
	require.NoError(t, tx.Table("email_resources").Select("version").Where("id = ?", 9228).Scan(&rootVersionInTx).Error)
	require.Equal(t, version9228, rootVersionInTx, "Core owns the single validation result version advance")
	require.NoError(t, tx.Commit().Error)
	stored := loadBindingForAdminTest(t, db, 9228)
	require.Equal(t, "candidate@recovery-active.test", stored.BindingAddress)
	require.Equal(t, string(domain.MicrosoftBindingVerified), stored.Status)

	require.NoError(t, db.Table("domain_resources").Where("id = ?", 9291).Update("status", "disabled").Error)
	account9229, version9229 := loadBindingRecoveryResource(t, db, 9229)
	tx = db.Begin()
	require.NoError(t, tx.Error)
	_, err = repo.ApplyRecoveredBindingForValidation(
		platform.WithGormTx(context.Background(), tx),
		MicrosoftRecoveredBindingInput{
			ResourceID:           9229,
			BindingAddress:       "must-not-verify@recovery-active.test",
			ExpectedAccountEmail: account9229,
		},
	)
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryIneligible)
	require.NoError(t, tx.Rollback().Error)

	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9229).Count(&count).Error)
	require.Zero(t, count)
	_, versionAfterRejectedCommit := loadBindingRecoveryResource(t, db, 9229)
	require.Equal(t, version9229, versionAfterRejectedCommit)
}

func TestMicrosoftBindingRepoValidationObservationRequiresCallerTransactionMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9230, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "observation.test")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9230)
	repo := NewMicrosoftBindingRepo(db)
	input := MicrosoftValidationBindingObservationInput{
		ResourceID: 9230, OwnerUserID: 9230, AccountEmail: accountEmail,
		Address: "observed@observation.test", Status: string(domain.MicrosoftBindingVerified),
	}

	_, err := repo.ApplyValidationBindingObservation(context.Background(), input)
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryTransaction)
	for _, invalidAddress := range []string{"ob******@observation.test", "bad address@observation.test"} {
		invalidInput := input
		invalidInput.Address = invalidAddress
		tx := db.Begin()
		require.NoError(t, tx.Error)
		changed, invalidErr := repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), invalidInput)
		require.ErrorIs(t, invalidErr, ErrMicrosoftBindingRecoveryInvalidInput)
		require.False(t, changed)
		require.NoError(t, tx.Rollback().Error)
	}

	tx := db.Begin()
	require.NoError(t, tx.Error)
	changed, err := repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), input)
	require.NoError(t, err)
	require.True(t, changed)
	var versionInTx uint64
	require.NoError(t, tx.Table("email_resources").Select("version").Where("id = ?", 9230).Scan(&versionInTx).Error)
	require.Equal(t, version, versionInTx)
	require.NoError(t, tx.Commit().Error)

	stored := loadBindingForAdminTest(t, db, 9230)
	require.Equal(t, "observed@observation.test", stored.BindingAddress)
	require.Equal(t, string(domain.MicrosoftBindingVerified), stored.Status)
	require.NotNil(t, stored.VerifiedAt)
	require.Empty(t, stored.BoundDisplay)

	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id = ?", 9230).
		Update("code_msg_id", "legacy-validation-message").Error)
	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), input)
	require.NoError(t, err)
	require.True(t, changed, "a stale code message id must be repaired instead of treated as a no-op")
	require.NoError(t, tx.Commit().Error)
	stored = loadBindingForAdminTest(t, db, 9230)
	require.Empty(t, stored.CodeMessageID)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), input)
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, tx.Commit().Error)

	for _, downgradeStatus := range []domain.MicrosoftBindingStatus{
		domain.MicrosoftBindingPending,
		domain.MicrosoftBindingCodeSent,
		domain.MicrosoftBindingTimeout,
		domain.MicrosoftBindingFailed,
	} {
		t.Run("verified_is_monotonic_against_"+string(downgradeStatus), func(t *testing.T) {
			downgrade := input
			downgrade.Status = string(downgradeStatus)
			downgrade.SafeMessage = "temporary validation observation"
			downgradeTx := db.Begin()
			require.NoError(t, downgradeTx.Error)
			downgraded, downgradeErr := repo.ApplyValidationBindingObservation(
				platform.WithGormTx(context.Background(), downgradeTx),
				downgrade,
			)
			require.NoError(t, downgradeErr)
			require.False(t, downgraded)
			require.NoError(t, downgradeTx.Commit().Error)

			preserved := loadBindingForAdminTest(t, db, 9230)
			require.Equal(t, string(domain.MicrosoftBindingVerified), preserved.Status)
			require.Equal(t, "observed@observation.test", preserved.BindingAddress)
			require.Empty(t, preserved.BoundDisplay)
			require.NotNil(t, preserved.VerifiedAt)
		})
	}

	external := input
	external.Status = string(domain.MicrosoftBindingFailed)
	external.BoundDisplay = "e******@external.test"
	external.SafeMessage = "Microsoft account is already bound to an external recovery mailbox."
	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), external)
	require.NoError(t, err)
	require.True(t, changed, "an explicit external BoundDisplay is authoritative enough to replace the verified fact")
	require.NoError(t, tx.Commit().Error)
	stored = loadBindingForAdminTest(t, db, 9230)
	require.Equal(t, string(domain.MicrosoftBindingFailed), stored.Status)
	require.Equal(t, "e******@external.test", stored.BoundDisplay)
}

func TestMicrosoftBindingRepoExternalObservationRepairsLegacyInvariantAndIsIdempotentMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9232, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9292, 9293, "external-observation.test")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9232)
	expiresAt := time.Now().UTC().Add(time.Hour)
	require.NoError(t, db.Create(&MicrosoftBindingMailboxModel{
		ResourceID:     9232,
		ResourceType:   "microsoft",
		OwnerUserID:    9232,
		AccountEmail:   accountEmail,
		BindingAddress: "candidate@external-observation.test",
		Purpose:        "legacy",
		Status:         string(domain.MicrosoftBindingFailed),
		CodeMessageID:  "legacy-external-message",
		BoundDisplay:   "e******@external.test",
		Category:       bindingStatusCategory(domain.MicrosoftBindingFailed),
		LastSafeError:  "An external recovery mailbox is already bound.",
		ExpiresAt:      &expiresAt,
	}).Error)

	repo := NewMicrosoftBindingRepo(db)
	input := MicrosoftValidationBindingObservationInput{
		ResourceID:   9232,
		OwnerUserID:  9232,
		AccountEmail: accountEmail,
		Address:      "candidate@external-observation.test",
		BoundDisplay: "e******@external.test",
		SafeMessage:  "An external recovery mailbox is already bound.",
	}
	tx := db.Begin()
	require.NoError(t, tx.Error)
	changed, err := repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), input)
	require.NoError(t, err)
	require.True(t, changed, "legacy purpose/expiry state must be repaired instead of treated as a no-op")
	require.NoError(t, tx.Commit().Error)

	stored := loadBindingForAdminTest(t, db, 9232)
	require.Equal(t, "validation", stored.Purpose)
	require.Equal(t, string(domain.MicrosoftBindingFailed), stored.Status)
	require.Empty(t, stored.CodeMessageID)
	require.Equal(t, "e******@external.test", stored.BoundDisplay)
	require.NotNil(t, stored.SelectedAt)
	require.Nil(t, stored.CodeSentAt)
	require.Nil(t, stored.VerifiedAt)
	require.Nil(t, stored.ExpiresAt)
	_, versionAfterRepair := loadBindingRecoveryResource(t, db, 9232)
	require.Equal(t, version, versionAfterRepair, "Core owns the observation transaction's root version advance")

	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), input)
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, tx.Commit().Error)
}

func TestMicrosoftBindingRepoExternalObservationCreatesOnlyFromConcreteCandidateMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9233, "normal")
	createMicrosoftAliasTestResource(t, db, 9234, "normal")
	createMicrosoftAliasTestResource(t, db, 9235, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9294, 9295, "external-create.test")
	repo := NewMicrosoftBindingRepo(db)

	account9233, version9233 := loadBindingRecoveryResource(t, db, 9233)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	changed, err := repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), MicrosoftValidationBindingObservationInput{
		ResourceID:   9233,
		OwnerUserID:  9233,
		AccountEmail: account9233,
		Address:      "candidate@external-create.test",
		BoundDisplay: "e******@external.test",
		SafeMessage:  "An external recovery mailbox is already bound.",
	})
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, tx.Commit().Error)
	stored := loadBindingForAdminTest(t, db, 9233)
	require.Equal(t, "candidate@external-create.test", stored.BindingAddress)
	require.Equal(t, string(domain.MicrosoftBindingFailed), stored.Status)
	require.Equal(t, "e******@external.test", stored.BoundDisplay)
	require.NotNil(t, stored.SelectedAt)
	_, versionAfterCreate := loadBindingRecoveryResource(t, db, 9233)
	require.Equal(t, version9233, versionAfterCreate, "Core owns the observation transaction's root version advance")

	account9234, _ := loadBindingRecoveryResource(t, db, 9234)
	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), MicrosoftValidationBindingObservationInput{
		ResourceID:   9234,
		OwnerUserID:  9234,
		AccountEmail: account9234,
		Address:      "c*******@external-create.test",
		BoundDisplay: "e******@external.test",
	})
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, tx.Commit().Error)

	account9235, _ := loadBindingRecoveryResource(t, db, 9235)
	tx = db.Begin()
	require.NoError(t, tx.Error)
	changed, err = repo.ApplyValidationBindingObservation(platform.WithGormTx(context.Background(), tx), MicrosoftValidationBindingObservationInput{
		ResourceID:   9235,
		OwnerUserID:  9235,
		AccountEmail: account9235,
		BoundDisplay: "e******@external.test",
	})
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, tx.Commit().Error)

	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("resource_id IN ?", []uint{9234, 9235}).
		Count(&count).Error)
	require.Zero(t, count, "masked or absent proof addresses must never become local binding rows")
}

func TestMicrosoftBindingRepoStandaloneRecoveryRejectsDisabledBindingDomainMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9231, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "standalone-disabled.test")
	require.NoError(t, db.Table("domain_resources").Where("id = ?", 9291).Update("status", "disabled").Error)
	accountEmail, version := loadBindingRecoveryResource(t, db, 9231)

	_, err := NewMicrosoftBindingRepo(db).ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID: 9231, BindingAddress: "recovered@standalone-disabled.test", ExpectedAccountEmail: accountEmail,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryIneligible)
	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9231).Count(&count).Error)
	require.Zero(t, count)
	_, versionAfter := loadBindingRecoveryResource(t, db, 9231)
	require.Equal(t, version, versionAfter)
}

func TestMicrosoftBindingRepoRecoveryRejectsMaskedOrMalformedAddressMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9236, "normal")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9236)
	repo := NewMicrosoftBindingRepo(db)

	for _, invalidAddress := range []string{"qa*****@recovery-invalid.test", "bad address@recovery-invalid.test"} {
		result, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
			ResourceID:           9236,
			BindingAddress:       invalidAddress,
			ExpectedOwnerUserID:  9236,
			ExpectedAccountEmail: accountEmail,
		})
		require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryInvalidInput)
		require.Nil(t, result)
	}

	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9236).Count(&count).Error)
	require.Zero(t, count)
	_, versionAfter := loadBindingRecoveryResource(t, db, 9236)
	require.Equal(t, version, versionAfter)
}

func TestMicrosoftBindingRepoStandaloneRecoveryRejectsDisabledResourceMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9236, "disabled")
	createActiveBindingDomainForRecoveryTest(t, db, 9296, 9297, "standalone-disabled-resource.test")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9236)

	_, err := NewMicrosoftBindingRepo(db).ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID: 9236, BindingAddress: "recovered@standalone-disabled-resource.test", ExpectedAccountEmail: accountEmail,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryConflict)
	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9236).Count(&count).Error)
	require.Zero(t, count)
	_, versionAfter := loadBindingRecoveryResource(t, db, 9236)
	require.Equal(t, version, versionAfter)
}

func TestMicrosoftBindingRepoApplyRecoveredBindingRejectsStaleSnapshotAndOccupiedAddressMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9222, "normal")
	createMicrosoftAliasTestResource(t, db, 9223, "normal")
	createMicrosoftAliasTestResource(t, db, 9224, "normal")
	createActiveBindingDomainForRecoveryTest(t, db, 9290, 9291, "aishop6.com")
	account9222, version9222 := loadBindingRecoveryResource(t, db, 9222)
	account9223, version9223 := loadBindingRecoveryResource(t, db, 9223)
	account9224, _ := loadBindingRecoveryResource(t, db, 9224)

	stale := &MicrosoftBindingMailboxModel{
		ResourceID:     9222,
		ResourceType:   "microsoft",
		OwnerUserID:    9222,
		AccountEmail:   account9222,
		BindingAddress: "stale-snapshot@aishop6.com",
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingPending),
	}
	require.NoError(t, db.Create(stale).Error)
	snapshot := loadBindingForAdminTest(t, db, 9222)
	concurrentUpdate := snapshot.UpdatedAt.Add(time.Minute)
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).
		Where("id = ?", snapshot.ID).
		Updates(map[string]any{"last_safe_error": "concurrent update", "updated_at": concurrentUpdate}).Error)

	repo := NewMicrosoftBindingRepo(db)
	_, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:               9222,
		BindingAddress:           "must-not-apply@aishop6.com",
		ExpectedAccountEmail:     account9222,
		ExpectedBindingID:        snapshot.ID,
		ExpectedBindingAddress:   snapshot.BindingAddress,
		ExpectedBindingUpdatedAt: snapshot.UpdatedAt,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryConflict)
	unchanged := loadBindingForAdminTest(t, db, 9222)
	assert.Equal(t, "stale-snapshot@aishop6.com", unchanged.BindingAddress)
	assert.Equal(t, "concurrent update", unchanged.LastSafeError)
	_, versionAfterConflict := loadBindingRecoveryResource(t, db, 9222)
	assert.Equal(t, version9222, versionAfterConflict)

	occupiedAddress := "occupied-recovery@aishop6.com"
	require.NoError(t, db.Create(&MicrosoftBindingMailboxModel{
		ResourceID:     9224,
		ResourceType:   "microsoft",
		OwnerUserID:    9224,
		AccountEmail:   account9224,
		BindingAddress: occupiedAddress,
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingFailed),
	}).Error)
	_, err = repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:           9223,
		BindingAddress:       occupiedAddress,
		ExpectedAccountEmail: account9223,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingAddressOccupied)
	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9223).Count(&count).Error)
	assert.Zero(t, count)
	_, versionAfterOccupied := loadBindingRecoveryResource(t, db, 9223)
	assert.Equal(t, version9223, versionAfterOccupied)
}

func TestMicrosoftBindingRepoApplyRecoveredBindingRejectsChangedAccountAndDeletedResourceMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9225, "normal")
	accountEmail, version := loadBindingRecoveryResource(t, db, 9225)
	repo := NewMicrosoftBindingRepo(db)

	_, err := repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:           9225,
		BindingAddress:       "changed-account@aishop6.com",
		ExpectedAccountEmail: "another-account@outlook.com",
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryConflict)
	_, versionAfterConflict := loadBindingRecoveryResource(t, db, 9225)
	assert.Equal(t, version, versionAfterConflict)

	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 9225).Update("status", "deleted").Error)
	_, err = repo.ApplyRecoveredBinding(context.Background(), MicrosoftRecoveredBindingInput{
		ResourceID:           9225,
		BindingAddress:       "deleted-resource@aishop6.com",
		ExpectedAccountEmail: accountEmail,
	})
	require.ErrorIs(t, err, ErrMicrosoftBindingRecoveryResourceDeleted)
	var count int64
	require.NoError(t, db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", 9225).Count(&count).Error)
	assert.Zero(t, count)
	_, versionAfterDeleted := loadBindingRecoveryResource(t, db, 9225)
	assert.Equal(t, version, versionAfterDeleted)
}

func createActiveBindingDomainForRecoveryTest(t *testing.T, db *gorm.DB, ownerID, resourceID uint, domainName string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, 'hash', 'supplier')",
		ownerID,
		fmt.Sprintf("binding-domain-owner-%d@test.local", ownerID),
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO mail_servers(id, owner_user_id, name, server_address, status) VALUES (?, ?, ?, ?, 'online')",
		ownerID,
		ownerID,
		"binding-domain-test",
		"mx."+domainName,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'domain', ?)",
		resourceID,
		ownerID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, mail_server_id, purpose, status) VALUES (?, 'domain', ?, ?, ?, 'binding', 'normal')",
		resourceID,
		ownerID,
		domainName,
		ownerID,
	).Error)
}

func loadBindingForAdminTest(t *testing.T, db *gorm.DB, resourceID uint) MicrosoftBindingMailboxModel {
	t.Helper()
	var binding MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id = ?", resourceID).First(&binding).Error)
	return binding
}

func loadBindingRecoveryResource(t *testing.T, db *gorm.DB, resourceID uint) (string, uint64) {
	t.Helper()
	var row struct {
		EmailAddress string `gorm:"column:email_address"`
		Version      uint64 `gorm:"column:version"`
	}
	require.NoError(t, db.Raw(`
SELECT mr.email_address, er.version
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id
WHERE er.id = ?`, resourceID).Scan(&row).Error)
	require.NotEmpty(t, row.EmailAddress)
	return row.EmailAddress, row.Version
}

func loadBindingRecoveryPassword(t *testing.T, db *gorm.DB, resourceID uint) string {
	t.Helper()
	var password string
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", resourceID).Pluck("password", &password).Error)
	return password
}
