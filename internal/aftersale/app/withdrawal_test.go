package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/aftersale/domain"
	"github.com/stretchr/testify/require"
)

func withdrawalTicket() *domain.Ticket {
	return &domain.Ticket{
		TicketNo: "AS-WITHDRAWAL", TicketType: domain.TicketTypeGeneral,
		Title: supplierWithdrawalTitle, Status: domain.TicketStatusOpen, RequesterUserID: 7,
		Messages: []domain.TicketMessage{{
			SenderType:  domain.SenderTypeSystem,
			Content:     "提现积分：260000\n提现方式：支付宝\n备注：无",
			Attachments: []domain.TicketAttachment{{AttachmentNo: "QR"}},
		}},
	}
}

func TestSupplierWithdrawalAmountOnlyTrustsOpeningSystemMessage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*domain.Ticket)
		want   string
	}{
		{"existing request", func(*domain.Ticket) {}, "260000"},
		{"note cannot change amount", func(ticket *domain.Ticket) { ticket.Messages[0].Content += "\n提现积分：999999" }, "260000"},
		{"user-authored", func(ticket *domain.Ticket) { ticket.Messages[0].SenderType = domain.SenderTypeUser }, ""},
		{"platform-authored", func(ticket *domain.Ticket) { ticket.Messages[0].SenderType = domain.SenderTypePlatform }, ""},
		{"sender id", func(ticket *domain.Ticket) { ticket.Messages[0].SenderUserID = 7 }, ""},
		{"wrong title", func(ticket *domain.Ticket) { ticket.Title = "Help" }, ""},
		{"order ticket", func(ticket *domain.Ticket) { ticket.TicketType = domain.TicketTypeOrder }, ""},
		{"missing attachment", func(ticket *domain.Ticket) { ticket.Messages[0].Attachments = nil }, ""},
		{"negative", func(ticket *domain.Ticket) {
			ticket.Messages[0].Content = "提现积分：-5\n提现方式：支付宝\n备注：无"
		}, ""},
		{"fractional", func(ticket *domain.Ticket) {
			ticket.Messages[0].Content = "提现积分：1.5\n提现方式：支付宝\n备注：无"
		}, ""},
		{"zero", func(ticket *domain.Ticket) {
			ticket.Messages[0].Content = "提现积分：0\n提现方式：支付宝\n备注：无"
		}, ""},
		{"overflow", func(ticket *domain.Ticket) {
			ticket.Messages[0].Content = "提现积分：1000000000000\n提现方式：支付宝\n备注：无"
		}, ""},
		{"no messages", func(ticket *domain.Ticket) { ticket.Messages = nil }, ""},
		{"later system message", func(ticket *domain.Ticket) {
			ticket.Messages = append([]domain.TicketMessage{{SenderType: domain.SenderTypeUser}}, ticket.Messages...)
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ticket := withdrawalTicket()
			tt.change(ticket)
			require.Equal(t, tt.want, supplierWithdrawalAmount(ticket))
		})
	}
}

func TestWithdrawAndCloseUsesOriginalAmountAndRejectsRepeat(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.ticket = withdrawalTicket()
	wallet := &fakeSupplierWallet{}
	uc.SetSupplierWalletPort(wallet)
	req := WithdrawTicketRequest{TicketNo: repo.ticket.TicketNo, OperatorUserID: 42, RequestID: "request-1"}
	view, err := uc.WithdrawAndCloseTicket(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "260000", view.WithdrawalAmount)
	require.Equal(t, domain.ResolutionWithdrawn, view.Ticket.Resolution.Kind)
	require.Equal(t, domain.TicketStatusClosed, view.Ticket.Status)
	require.Equal(t, []SupplierWithdrawalCommand{{TicketNo: req.TicketNo, UserID: 7, Amount: "260000", OperatorUserID: 42, RequestID: "request-1"}}, wallet.withdrawals)
	_, err = uc.WithdrawAndCloseTicket(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrWithdrawalAlreadyProcessed)
	require.Len(t, wallet.withdrawals, 1)
}

func TestWithdrawAndCloseLeavesTicketOpenWhenDebitFails(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.ticket = withdrawalTicket()
	wallet := &fakeSupplierWallet{err: domain.ErrInsufficientSupplierBalance}
	uc.SetSupplierWalletPort(wallet)
	_, err := uc.WithdrawAndCloseTicket(context.Background(), WithdrawTicketRequest{TicketNo: repo.ticket.TicketNo, OperatorUserID: 42})
	require.ErrorIs(t, err, domain.ErrInsufficientSupplierBalance)
	require.Equal(t, domain.TicketStatusOpen, repo.ticket.Status)
	require.Nil(t, repo.ticket.Resolution)
	require.Len(t, repo.ticket.Messages, 1)
}

func TestWithdrawAndCloseRejectsUntrustedTicketsWithoutDebiting(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.ticket = withdrawalTicket()
	repo.ticket.Messages[0].SenderType = domain.SenderTypeUser
	wallet := &fakeSupplierWallet{}
	uc.SetSupplierWalletPort(wallet)
	_, err := uc.WithdrawAndCloseTicket(context.Background(), WithdrawTicketRequest{TicketNo: repo.ticket.TicketNo, OperatorUserID: 42})
	require.ErrorIs(t, err, domain.ErrInvalidTicketRequest)
	require.Empty(t, wallet.withdrawals)
}
