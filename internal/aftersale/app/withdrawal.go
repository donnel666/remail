package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/aftersale/domain"
	moneyfmt "github.com/donnel666/remail/internal/money"
)

// Read the immutable, server-authored opening message so existing withdrawal
// tickets work too. User-authored messages never authorize a withdrawal.
func supplierWithdrawalAmount(ticket *domain.Ticket) string {
	if ticket.TicketType != domain.TicketTypeGeneral || ticket.Title != supplierWithdrawalTitle || ticket.Order != nil || len(ticket.Messages) == 0 {
		return ""
	}
	first := ticket.Messages[0]
	if first.SenderType != domain.SenderTypeSystem || first.SenderUserID != 0 || len(first.Attachments) != 1 {
		return ""
	}
	line, rest, ok := strings.Cut(first.Content, "\n")
	if !ok || !strings.HasPrefix(line, "提现积分：") || !strings.HasPrefix(rest, "提现方式：支付宝\n备注：") {
		return ""
	}
	amount, err := moneyfmt.Parse(strings.TrimPrefix(line, "提现积分："))
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Truncate(0)) {
		return ""
	}
	return amount.String()
}

func (uc *UseCase) WithdrawAndCloseTicket(ctx context.Context, req WithdrawTicketRequest) (*TicketView, error) {
	if req.OperatorUserID == 0 {
		return nil, domain.ErrTicketForbidden
	}
	ticket, err := uc.repo.Get(ctx, req.TicketNo, true)
	if err != nil {
		return nil, err
	}
	if ticket.Status.IsTerminal() {
		if ticket.Resolution != nil && ticket.Resolution.Kind == domain.ResolutionWithdrawn {
			return nil, domain.ErrWithdrawalAlreadyProcessed
		}
		return nil, domain.ErrTicketStateConflict
	}
	amount := supplierWithdrawalAmount(ticket)
	if amount == "" || ticket.RequesterUserID == 0 {
		return nil, domain.ErrInvalidTicketRequest
	}
	if uc.supplierWallet == nil {
		return nil, domain.ErrSupplierWalletUnavailable
	}
	// Resolve presentation data before any financial write can commit.
	view, err := uc.viewOf(ctx, ticket)
	if err != nil {
		return nil, err
	}
	updated, err := uc.repo.Close(ctx, CloseParams{
		TicketNo:      ticket.TicketNo,
		By:            domain.SenderTypePlatform,
		Resolution:    domain.Resolution{Kind: domain.ResolutionWithdrawn},
		SystemMessage: fmt.Sprintf("平台已确认提现，扣除供应商可提现积分 %s，并关闭工单。", amount),
		BeforeClose: func(txCtx context.Context) error {
			return uc.supplierWallet.WithdrawSupplier(txCtx, SupplierWithdrawalCommand{
				TicketNo:       ticket.TicketNo,
				UserID:         ticket.RequesterUserID,
				Amount:         amount,
				OperatorUserID: req.OperatorUserID,
				RequestID:      req.RequestID,
			})
		},
	})
	if err != nil {
		return nil, err
	}
	view.Ticket = updated
	uc.notifyRequester(ctx, view, ticketMailResolved)
	return view, nil
}
