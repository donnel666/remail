package infra

import (
	"context"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type auxiliaryICloudRootTestModel struct {
	ID   uint   `gorm:"column:id;primaryKey"`
	Type string `gorm:"column:type"`
}

func (auxiliaryICloudRootTestModel) TableName() string { return "email_resources" }

type auxiliaryICloudResourceTestModel struct {
	ID                uint   `gorm:"column:id;primaryKey"`
	SelectedForwardTo string `gorm:"column:selected_forward_to"`
	RequiredForwardTo string `gorm:"column:required_forward_to"`
}

func (auxiliaryICloudResourceTestModel) TableName() string { return "icloud_resources" }

func TestAuxiliaryMailRepoScopesICloudMailByExactForwardingAddress(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:auxiliary-icloud?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&auxiliaryICloudRootTestModel{},
		&auxiliaryICloudResourceTestModel{},
		&InboundMailModel{},
	))
	require.NoError(t, db.Create(&[]auxiliaryICloudRootTestModel{
		{ID: 41, Type: "icloud"},
		{ID: 42, Type: "icloud"},
		{ID: 99, Type: "domain"},
	}).Error)
	require.NoError(t, db.Create(&[]auxiliaryICloudResourceTestModel{
		{ID: 41, SelectedForwardTo: "wrong@relay.example", RequiredForwardTo: "icloud_one@relay.example"},
		{ID: 42, SelectedForwardTo: "icloud_two@relay.example"},
	}).Error)
	now := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&[]InboundMailModel{
		{
			ID: 1, HeaderFrom: "noreply@apple.com", Recipient: "icloud_one@relay.example",
			Subject: "Apple verification", VerificationCode: "088556",
			ResourceID: 99, ResourceType: "domain", OwnerUserID: 7,
			SourceObjectKey: "private/apple-one.eml", Status: string(domain.InboundStatusStored),
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: 2, HeaderFrom: "noreply@apple.com", Recipient: "icloud_two@relay.example",
			Subject: "Other Apple verification", VerificationCode: "123456",
			ResourceID: 99, ResourceType: "domain", OwnerUserID: 7,
			SourceObjectKey: "private/apple-two.eml", Status: string(domain.InboundStatusStored),
			CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
		},
	}).Error)

	repo := NewAuxiliaryMailRepo(db)
	exists, err := repo.ResourceExists(context.Background(), 41, domain.InboundResourceICloud)
	require.NoError(t, err)
	require.True(t, exists)
	items, total, hasMore, err := repo.ListMessages(context.Background(), mailapp.AuxiliaryMailFilter{
		ResourceID: 41, ResourceType: domain.InboundResourceICloud, Limit: 20,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.False(t, hasMore)
	require.Len(t, items, 1)
	require.Equal(t, uint(1), items[0].ID)
	require.Empty(t, items[0].SourceObjectKey)
	message, err := repo.FindMessage(context.Background(), 41, domain.InboundResourceICloud, 1)
	require.NoError(t, err)
	require.NotNil(t, message)
	message, err = repo.FindMessage(context.Background(), 41, domain.InboundResourceICloud, 2)
	require.NoError(t, err)
	require.Nil(t, message)
}
