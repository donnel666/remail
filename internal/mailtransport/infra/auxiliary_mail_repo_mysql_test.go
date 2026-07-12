package infra

import (
	"context"
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

	items, total, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 9201,
		Search:     "654321",
		Limit:      20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, "654321", items[0].VerificationCode)
	assert.Empty(t, items[0].SourceObjectKey)
	assert.Empty(t, items[0].EnvelopeFrom)

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
	deletedItems, deletedTotal, err := repo.ListByMicrosoftResource(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 9201,
		Limit:      20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, deletedTotal)
	require.Len(t, deletedItems, 1)
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
	require.NoError(t, db.Create(&MicrosoftBindingMailboxModel{
		ResourceID:     9210,
		ResourceType:   "microsoft",
		OwnerUserID:    9210,
		AccountEmail:   "old-account@outlook.com",
		BindingAddress: "preserve@example.com",
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingVerified),
	}).Error)
	repo := NewMicrosoftBindingRepo(db)

	require.NoError(t, repo.ReplaceAdminInput(
		context.Background(),
		9210,
		9211,
		"new-account@outlook.com",
		false,
		nil,
	))
	current := loadBindingForAdminTest(t, db, 9210)
	assert.Equal(t, uint(9211), current.OwnerUserID)
	assert.Equal(t, "new-account@outlook.com", current.AccountEmail)
	assert.Equal(t, "preserve@example.com", current.BindingAddress)
	assert.Equal(t, string(domain.MicrosoftBindingVerified), current.Status)

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
	assert.Nil(t, current.VerifiedAt)

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

func loadBindingForAdminTest(t *testing.T, db *gorm.DB, resourceID uint) MicrosoftBindingMailboxModel {
	t.Helper()
	var binding MicrosoftBindingMailboxModel
	require.NoError(t, db.Where("resource_id = ?", resourceID).First(&binding).Error)
	return binding
}
