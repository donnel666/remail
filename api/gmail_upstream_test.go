package api

import (
	"context"
	"errors"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	"github.com/donnel666/remail/internal/smsbower"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/upstream"
	"github.com/stretchr/testify/require"
)

func TestOverlaySMSBowerInventoryAddsOnlyGmailCodeSupply(t *testing.T) {
	localCode, localPurchase := int64(4), int64(2)
	snapshots := map[uint]*allocapp.ProjectProductInventoryTotals{
		7: {
			ProjectID: 7, TotalAvailable: 4,
			Items: []allocapp.ProductInventoryTotal{{
				ProductID: 70, TotalAvailable: 4, PublicAvailable: 4,
				CodeAvailable: &localCode, CodePublicAvailable: &localCode,
				PurchaseAvailable: &localPurchase, PurchasePublicAvailable: &localPurchase,
			}},
		},
	}

	overlaySMSBowerInventory(snapshots, []smsbower.InventoryItem{{ProjectID: 7, ProductID: 70, CodeAvailable: 3}})

	item := snapshots[7].Items[0]
	require.Equal(t, coredomain.ProductTypeGmail, item.ProductType)
	require.Equal(t, int64(7), *item.CodeAvailable)
	require.Equal(t, int64(7), *item.CodePublicAvailable)
	require.Equal(t, int64(2), *item.PurchaseAvailable)
	require.Equal(t, int64(2), *item.PurchasePublicAvailable)
	require.Equal(t, int64(7), item.TotalAvailable)
	require.Equal(t, int64(7), snapshots[7].TotalAvailable)
}

func TestOverlaySMSBowerInventoryAddsGmailProductTypeToNewItem(t *testing.T) {
	snapshots := map[uint]*allocapp.ProjectProductInventoryTotals{}

	overlaySMSBowerInventory(snapshots, []smsbower.InventoryItem{{ProjectID: 7, ProductID: 70, CodeAvailable: 3}})

	require.Equal(t, coredomain.ProductTypeGmail, snapshots[7].Items[0].ProductType)
}

type gmailOrderTokenReaderStub struct {
	orderNo string
	err     error
}

type gmailOrderTokenReaderSpy struct{ calls int }

func (s *gmailOrderTokenReaderSpy) FindOrderTokenByPlain(context.Context, string) (*openapidomain.OrderToken, error) {
	s.calls++
	return &openapidomain.OrderToken{OrderNo: "NORMAL-ORDER"}, nil
}

func (s gmailOrderTokenReaderStub) FindOrderTokenByPlain(context.Context, string) (*openapidomain.OrderToken, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &openapidomain.OrderToken{OrderNo: s.orderNo}, nil
}

type gmailUpstreamPickupSpy struct {
	pickup      *upstream.PickupResult
	handled     bool
	err         error
	pickupCalls int
	lastRequest upstream.PickupRequest
}

func (*gmailUpstreamPickupSpy) Supply(context.Context, upstream.Demand) (*upstream.SupplyQuote, error) {
	return nil, nil
}

func (*gmailUpstreamPickupSpy) AcceptPaidOrder(context.Context, upstream.PaidOrder) (bool, error) {
	return false, nil
}

func (*gmailUpstreamPickupSpy) OwnsOrder(context.Context, string) (bool, error) {
	return false, nil
}

func (*gmailUpstreamPickupSpy) CancelOrder(context.Context, string) (bool, error) {
	return false, nil
}

func (s *gmailUpstreamPickupSpy) Pickup(_ context.Context, request upstream.PickupRequest) (*upstream.PickupResult, bool, error) {
	s.pickupCalls++
	s.lastRequest = request
	return s.pickup, s.handled, s.err
}

func TestBotCodeDiagnosisRefreshRoutesUpstreamExactlyOnce(t *testing.T) {
	receivedAt := time.Now().UTC().Add(-time.Minute)
	provider := &gmailUpstreamPickupSpy{handled: true, pickup: &upstream.PickupResult{
		Codes: []upstream.Code{{Seq: 1, Value: "must-not-leak", ReceivedAt: receivedAt}},
	}}
	adapter := botCodeDiagnosisRefreshAdapter{upstreams: upstream.NewRouter(provider)}

	result, err := adapter.RefreshCodeDiagnosis(context.Background(), "ORDER-UPSTREAM", "user@gmail.com", 0)
	require.NoError(t, err)
	require.True(t, result.DeliveryFound)
	require.Equal(t, receivedAt, result.ReceivedAt)
	require.Equal(t, 1, provider.pickupCalls)
	require.Equal(t, upstream.PickupRequest{OrderNo: "ORDER-UPSTREAM", Email: "user@gmail.com"}, provider.lastRequest)

	localOnly := &gmailUpstreamPickupSpy{handled: true}
	adapter = botCodeDiagnosisRefreshAdapter{upstreams: upstream.NewRouter(localOnly)}
	result, err = adapter.RefreshCodeDiagnosis(context.Background(), "ORDER-LOCAL", "user@gmail.com", 8)
	require.NoError(t, err)
	require.False(t, result.DeliveryFound)
	require.Zero(t, localOnly.pickupCalls)
}

func TestGmailPickupAdapterFallsThroughWhenUpstreamDoesNotOwnOrder(t *testing.T) {
	provider := &gmailUpstreamPickupSpy{}
	adapter := gmailPickupAdapter{
		upstreams: upstream.NewRouter(provider), tokens: gmailOrderTokenReaderStub{orderNo: "VARIANT-PURCHASE-1"},
	}

	items, matched, err := adapter.ReadUpstreamPickup(context.Background(), "buyer+variant@gmail.com", "st_variant_purchase")

	require.NoError(t, err)
	require.False(t, matched)
	require.Nil(t, items)
	require.Equal(t, 1, provider.pickupCalls)
}

func TestGmailPickupAdapterSkipsNonGmailWithoutLookup(t *testing.T) {
	for _, email := range []string{"buyer@outlook.com", "buyer@icloud.com"} {
		t.Run(email, func(t *testing.T) {
			tokens := &gmailOrderTokenReaderSpy{}
			provider := &gmailUpstreamPickupSpy{}
			adapter := gmailPickupAdapter{upstreams: upstream.NewRouter(provider), tokens: tokens}

			items, matched, err := adapter.ReadUpstreamPickup(context.Background(), email, "st_normal")

			require.NoError(t, err)
			require.False(t, matched)
			require.Nil(t, items)
			require.Zero(t, tokens.calls)
			require.Zero(t, provider.pickupCalls)
		})
	}
}

func TestGmailPickupAdapterNormalizesUpstreamCodesAsMail(t *testing.T) {
	receivedAt := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	provider := &gmailUpstreamPickupSpy{
		handled: true,
		pickup: &upstream.PickupResult{
			Email: "buyer@gmail.com", Codes: []upstream.Code{{Seq: 1, Value: "654321", ReceivedAt: receivedAt}},
		},
	}
	adapter := gmailPickupAdapter{
		upstreams: upstream.NewRouter(provider), tokens: gmailOrderTokenReaderStub{orderNo: "GMAIL-UPSTREAM-1"},
	}

	items, matched, err := adapter.ReadUpstreamPickup(context.Background(), "buyer@gmail.com", "st_upstream")

	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, []mailmatchdomain.MailContent{{
		Recipient: "buyer@gmail.com", ReceivedAt: receivedAt, VerificationCode: "654321",
	}}, items)
	require.Equal(t, 1, provider.pickupCalls)
}

type gmailDeliveryReaderSpy struct {
	deliveries map[string]tradeapp.GmailDeliverySummary
	err        error
	orderNos   []string
}

func (s *gmailDeliveryReaderSpy) ListGmailDeliveries(_ context.Context, orderNos []string) (map[string]tradeapp.GmailDeliverySummary, error) {
	s.orderNos = append([]string(nil), orderNos...)
	return s.deliveries, s.err
}

type smsbowerDeliveryReaderSpy struct {
	deliveries map[string]upstream.PickupResult
	err        error
	calls      int
	orderNos   []string
}

func (s *smsbowerDeliveryReaderSpy) ListDeliveries(_ context.Context, orderNos []string) (map[string]upstream.PickupResult, error) {
	s.calls++
	s.orderNos = append([]string(nil), orderNos...)
	return s.deliveries, s.err
}

func TestGmailDeliveryCompositeNeverSendsVariantToSMSBower(t *testing.T) {
	for _, test := range []struct {
		name       string
		local      map[string]tradeapp.GmailDeliverySummary
		wantExists bool
	}{
		{name: "allocated", local: map[string]tradeapp.GmailDeliverySummary{"VARIANT-1": {AllocationID: 41}}, wantExists: true},
		{name: "unresolved", local: map[string]tradeapp.GmailDeliverySummary{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sms := &smsbowerDeliveryReaderSpy{err: errors.New("SMSBower unavailable")}
			composite := gmailDeliveryComposite{gmail: &gmailDeliveryReaderSpy{deliveries: test.local}, smsbower: sms}

			deliveries, err := composite.ListGmailDeliveries(context.Background(), []tradeapp.GmailDeliveryOrder{{
				OrderNo: "VARIANT-1", ProductType: tradedomain.ProductTypeGmailVariant,
			}})

			require.NoError(t, err)
			_, exists := deliveries["VARIANT-1"]
			require.Equal(t, test.wantExists, exists)
			require.Zero(t, sms.calls)
		})
	}
}

func TestGmailDeliveryCompositeSendsOnlyUnresolvedOriginalGmailToSMSBower(t *testing.T) {
	local := &gmailDeliveryReaderSpy{deliveries: map[string]tradeapp.GmailDeliverySummary{
		"GMAIL-LOCAL": {AllocationID: 41},
	}}
	sms := &smsbowerDeliveryReaderSpy{deliveries: map[string]upstream.PickupResult{
		"GMAIL-UPSTREAM": {Email: "buyer@gmail.com", Codes: []upstream.Code{{Seq: 1, Value: "123456"}}},
	}}
	composite := gmailDeliveryComposite{gmail: local, smsbower: sms}

	deliveries, err := composite.ListGmailDeliveries(context.Background(), []tradeapp.GmailDeliveryOrder{
		{OrderNo: "VARIANT-UNRESOLVED", ProductType: tradedomain.ProductTypeGmailVariant},
		{OrderNo: "GMAIL-LOCAL", ProductType: tradedomain.ProductTypeGmail},
		{OrderNo: "GMAIL-UPSTREAM", ProductType: tradedomain.ProductTypeGmail},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"VARIANT-UNRESOLVED", "GMAIL-LOCAL", "GMAIL-UPSTREAM"}, local.orderNos)
	require.Equal(t, []string{"GMAIL-UPSTREAM"}, sms.orderNos)
	require.Equal(t, "123456", deliveries["GMAIL-UPSTREAM"].Codes[0].Code)
	require.Equal(t, uint(41), deliveries["GMAIL-LOCAL"].AllocationID)
}
