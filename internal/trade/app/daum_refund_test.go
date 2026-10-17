package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type daumRefundProofRepo struct {
	*unavailableRefundRepoStub
	proof     DaumUnavailableOrder
	current   DaumUnavailableOrder
	status    string
	afterList func()
	inTx      bool
	events    *[]string
}

func (r *daumRefundProofRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	r.inTx = true
	defer func() { r.inTx = false }()
	return fn(ctx)
}
func (r *daumRefundProofRepo) ListUnavailableDaumOrders(context.Context, uint, int) ([]DaumUnavailableOrder, error) {
	if r.order.Status != domain.OrderStatusActive {
		return nil, nil
	}
	snapshot := r.proof
	if r.afterList != nil {
		r.afterList()
	}
	return []DaumUnavailableOrder{snapshot}, nil
}
func (r *daumRefundProofRepo) AssertDaumRefundFailure(context.Context, DaumUnavailableOrder) error {
	if !r.inTx {
		panic("refund guard must share wallet transaction")
	}
	*r.events = append(*r.events, "resource")
	if r.status != "abnormal" || r.current.ResourceID != r.proof.ResourceID || r.current.CredentialRevision != r.proof.CredentialRevision || r.current.ValidationGeneration != r.proof.ValidationGeneration {
		return domain.ErrOrderStateConflict
	}
	return nil
}
func (r *daumRefundProofRepo) LockOrderForUpdate(ctx context.Context, no string) (*domain.Order, error) {
	*r.events = append(*r.events, "order")
	return r.unavailableRefundRepoStub.LockOrderForUpdate(ctx, no)
}

type daumRefundWallet struct {
	unavailableRefundWalletStub
	events *[]string
}

func (w *daumRefundWallet) LockConsumer(context.Context, uint) error {
	*w.events = append(*w.events, "wallet")
	return nil
}

func (r *daumRefundProofRepo) ListUnavailableMicrosoftOrders(ctx context.Context, id uint, limit int) ([]MicrosoftUnavailableOrder, error) {
	return r.ListUnavailableDaumOrders(ctx, id, limit)
}
func (r *daumRefundProofRepo) AssertMicrosoftRefundFailure(ctx context.Context, proof MicrosoftUnavailableOrder) error {
	return r.AssertDaumRefundFailure(ctx, proof)
}

func TestMicrosoftDaumRefundSkipsRecoveredOrReplacedFailureProof(t *testing.T) {
	for _, provider := range []domain.ProductType{domain.ProductTypeMicrosoft, domain.ProductTypeDaum} {
		for _, change := range []string{"normal", "credential", "generation", "category", "unchanged"} {
			t.Run(string(provider)+"-"+change, func(t *testing.T) {
				debit := uint(1)
				var events []string
				proof := DaumUnavailableOrder{OrderNo: "DAUM-PROOF", ResourceID: 7, CredentialRevision: 3, ValidationGeneration: 5, FailureCategory: "invalid_credentials"}
				repo := &daumRefundProofRepo{unavailableRefundRepoStub: &unavailableRefundRepoStub{resourceID: 7, order: domain.Order{OrderNo: proof.OrderNo, UserID: 42, ProductType: provider, Status: domain.OrderStatusActive, ServiceMode: domain.ServiceModePurchase, PayAmount: "10.00", DebitTxID: &debit}}, proof: proof, current: proof, status: "abnormal", events: &events}
				repo.afterList = func() {
					switch change {
					case "normal":
						repo.status = "normal"
					case "credential":
						repo.current.CredentialRevision++
					case "generation":
						repo.current.ValidationGeneration++
					case "category":
						repo.current.FailureCategory = "account_disabled"
					}
				}
				wallet := &daumRefundWallet{events: &events}
				allocation := &unavailableRefundAllocationStub{}
				tokens := &unavailableRefundTokenStub{}
				uc := NewUseCase(repo, nil, wallet, allocation, tokens)
				refund := uc.RefundUnavailableDaumOrders
				if provider == domain.ProductTypeMicrosoft {
					refund = uc.RefundUnavailableMicrosoftOrders
				}
				count, err := refund(context.Background(), 7, "test")
				require.NoError(t, err)
				if change == "unchanged" || change == "category" {
					require.Equal(t, 1, count)
					require.Equal(t, []string{"wallet", "resource", "order"}, events)
					require.Len(t, wallet.commands, 1)
					require.Equal(t, []string{proof.OrderNo}, tokens.disabled)
					count, err = refund(context.Background(), 7, "replay")
					require.NoError(t, err)
					require.Zero(t, count)
					require.Len(t, wallet.commands, 1)
				} else {
					require.Zero(t, count)
					require.Equal(t, []string{"wallet", "resource"}, events)
					require.Empty(t, wallet.commands)
					require.Empty(t, tokens.disabled)
					require.Empty(t, allocation.released)
					require.Equal(t, domain.OrderStatusActive, repo.order.Status)
				}
			})
		}
	}

}
