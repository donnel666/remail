package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestStoreLivenessReceiptUsesManualFetchIdentityWithoutOrderMatching(t *testing.T) {
	repo := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{scopes: []OrderScope{{
		OrderID: 61, OrderNo: "OR_PROBE", AllocationType: domain.ResourceTypeICloud,
		EmailResourceID: 17, Recipient: "alias@icloud.com", ServiceMode: "purchase", OrderStatus: "completed", LooseMatch: true,
	}}}}
	uc := NewUseCase(repo, nil, nil, nil)
	req := InboundMailRequest{
		EmailResourceID: 17, ResourceType: domain.ResourceTypeICloud, Recipient: "alias@icloud.com",
		EnvelopeFrom: "sender@qq.com", Raw: []byte("From: sender_at_qq_com_abc@icloud.com\r\nTo: alias@icloud.com\r\nSubject: ReMail iCloud liveness token\r\n\r\nprobe"),
		ReceivedAt: time.Now().UTC(), ProviderMessageID: "inbound:123", Protocol: "smtp", Folder: "inbound",
	}
	fenceCalls := 0
	fence := func(ctx context.Context) error {
		require.NotNil(t, ctx.Value(appendFenceTransactionMarker{}))
		fenceCalls++
		return nil
	}
	require.NoError(t, uc.StoreLivenessReceipt(context.Background(), req, fence))
	require.NoError(t, uc.StoreLivenessReceipt(context.Background(), req, fence))
	require.Equal(t, 2, fenceCalls)
	require.Equal(t, 1, repo.appended)
	require.True(t, repo.appendInTx)
	require.Len(t, repo.facts, 1)
	require.Len(t, repo.projections, 1)
	for _, message := range repo.projections {
		require.Equal(t, domain.MessageStatusIgnored, message.Status)
		require.Nil(t, message.MatchedOrderID)
		require.Empty(t, message.VerificationCode)
		require.Equal(t, "sender@qq.com", message.Sender)
		require.Equal(t, "ReMail iCloud liveness token", message.Subject)
		require.Equal(t, "probe", message.RawBody)
		require.Equal(t, baseMessageFromFetched(ParseInboundFetchedMessage(req)).DedupeKey, message.DedupeKey)
	}
	require.Empty(t, repo.matchedRecipients, "a system probe must not be used to fulfill an order")
	require.Nil(t, repo.purchaseDelivery)
	stale := errors.New("run replaced")
	req.ProviderMessageID = "inbound:456"
	require.ErrorIs(t, uc.StoreLivenessReceipt(context.Background(), req, func(context.Context) error { return stale }), stale)
	require.Equal(t, 1, repo.appended)
}

func TestStoreLivenessReceiptRequiresIdentityAndFence(t *testing.T) {
	repo := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{}}
	uc := NewUseCase(repo, nil, nil, nil)
	require.ErrorIs(t, uc.StoreLivenessReceipt(context.Background(), InboundMailRequest{}, nil), domain.ErrInvalidRequest)
	require.Empty(t, repo.facts)
}
