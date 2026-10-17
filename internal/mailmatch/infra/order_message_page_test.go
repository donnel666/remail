package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOrderAuxiliaryPagePreservesOwnershipBeyondPickupLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&MessageModel{}, &MessageProjectionModel{}))
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	owner, other := uint(1), uint(2)
	for id := uint(1); id <= 46; id++ {
		legacyOwner := owner
		if id > 20 {
			legacyOwner = other
		}
		subject := "Code"
		if id == 3 {
			subject = "Literal 100%_!"
		}
		require.NoError(t, db.Create(&MessageModel{ID: id, EmailResourceID: 7, ResourceType: "microsoft", MatchedOrderID: &legacyOwner,
			Recipient: "helper@outlook.com", Subject: subject, Sender: "kakao@example.test", BodyPreview: "safe preview",
			DedupeKey: fmt.Sprintf("%064d", id), Status: "matched", ReceivedAt: now}).Error)
		if id > 20 && id <= 45 {
			require.NoError(t, db.Create(&MessageProjectionModel{MessageID: id, MatchedOrderID: &owner, Status: "matched", MessageReceivedAt: now}).Error)
		}
	}
	// A current projection belonging to another order must override the old owner.
	require.NoError(t, db.Create(&MessageModel{ID: 47, EmailResourceID: 7, ResourceType: "microsoft", MatchedOrderID: &owner,
		Recipient: "helper@outlook.com", Subject: "Other project private code", DedupeKey: fmt.Sprintf("%064d", 47), Status: "matched", ReceivedAt: now}).Error)
	require.NoError(t, db.Create(&MessageProjectionModel{MessageID: 47, MatchedOrderID: &other, Status: "matched", MessageReceivedAt: now}).Error)
	repo := NewRepo(db, nil)
	scope := app.OrderScope{OrderID: owner, OrderNo: "source-order", ServiceMode: "purchase", ReceiveStartedAt: &start}
	query := app.AdminMessageListQuery{Limit: 20}
	items, total, more, err := repo.ListOrderMessagePage(context.Background(), scope, query)
	require.NoError(t, err)
	require.EqualValues(t, 45, total)
	require.Len(t, items, 20)
	require.True(t, more)
	require.EqualValues(t, 45, items[0].ID)
	require.EqualValues(t, 26, items[19].ID)
	query.BeforeReceivedAt, query.BeforeID, query.SkipTotal = &items[19].ReceivedAt, items[19].ID, true
	items, total, more, err = repo.ListOrderMessagePage(context.Background(), scope, query)
	require.NoError(t, err)
	require.EqualValues(t, -1, total)
	require.Len(t, items, 20)
	require.True(t, more)
	query.BeforeReceivedAt, query.BeforeID = &items[19].ReceivedAt, items[19].ID
	items, _, more, err = repo.ListOrderMessagePage(context.Background(), scope, query)
	require.NoError(t, err)
	require.Len(t, items, 5)
	require.False(t, more)
	items, total, _, err = repo.ListOrderMessagePage(context.Background(), scope, app.AdminMessageListQuery{Search: "100%_!", Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.EqualValues(t, 3, items[0].ID)
}
