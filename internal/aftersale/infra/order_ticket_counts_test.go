package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCountOrderTicketsByUsersIncludesAllStatuses(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:order-ticket-counts-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TicketModel{}))
	rows := []TicketModel{
		{RequesterUserID: 7, TicketType: "order", Status: "open", OrderNo: "same-order"},
		{RequesterUserID: 7, TicketType: "order", Status: "processing", OrderNo: "same-order"},
		{RequesterUserID: 7, TicketType: "order", Status: "closed", ResolutionKind: "refunded"},
		{RequesterUserID: 7, TicketType: "order", Status: "closed", ResolutionKind: "rejected"},
		{RequesterUserID: 7, TicketType: "general", Status: "open", Title: "咨询"},
		{RequesterUserID: 7, TicketType: "general", Status: "closed", Title: "供应商提现申请"},
		{RequesterUserID: 8, TicketType: "order", Status: "open"},
		{RequesterUserID: 99, TicketType: "order", Status: "open"},
	}
	for i := range rows {
		rows[i].TicketNo = fmt.Sprintf("ticket-%d", i)
		rows[i].PayAmount, rows[i].RefundAmount = "0", "0"
	}
	require.NoError(t, db.Create(&rows).Error)
	repo := NewRepo(db)
	counts, err := repo.CountOrderTicketsByUsers(context.Background(), []uint{7, 7, 8, 9})
	require.NoError(t, err)
	require.Equal(t, map[uint]int64{7: 4, 8: 1}, counts)
	counts, err = repo.CountOrderTicketsByUsers(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, counts)

	rollback := errors.New("rollback ticket")
	err = db.Transaction(func(tx *gorm.DB) error {
		ticket := rows[0]
		ticket.ID, ticket.TicketNo = 0, "uncommitted-ticket"
		require.NoError(t, tx.Create(&ticket).Error)
		counts, err := repo.CountOrderTicketsByUsers(platform.WithGormTx(context.Background(), tx), []uint{7})
		require.NoError(t, err)
		require.Equal(t, int64(5), counts[7])
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	counts, err = repo.CountOrderTicketsByUsers(context.Background(), []uint{7})
	require.NoError(t, err)
	require.Equal(t, int64(4), counts[7])
}
