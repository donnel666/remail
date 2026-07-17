package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type ownerLookupPortSpy struct {
	calls  int
	ids    []uint
	owners map[uint]OrderOwnerSummary
	err    error
}

func (s *ownerLookupPortSpy) GetByIDs(_ context.Context, ids []uint) (map[uint]OrderOwnerSummary, error) {
	s.calls++
	s.ids = append([]uint(nil), ids...)
	return s.owners, s.err
}

func TestAttachOwnersEnrichesAdminSiteWideScopeWithOneDeduplicatedBatch(t *testing.T) {
	port := &ownerLookupPortSpy{owners: map[uint]OrderOwnerSummary{
		7: {ID: 7, Email: "seven@example.com", Nickname: "Seven"},
		8: {ID: 8, Email: "eight@example.com"},
	}}
	uc := &UseCase{owners: port}
	results := []CheckoutResult{
		{Order: domain.Order{UserID: 7}},
		{Order: domain.Order{UserID: 7}},
		{Order: domain.Order{UserID: 8}},
		{Order: domain.Order{UserID: 9}}, // Missing/deleted buyer.
	}

	require.NoError(t, uc.attachOwners(context.Background(), OrderListFilter{IsAdmin: true, Scope: "all"}, results))
	require.Equal(t, 1, port.calls)
	require.Equal(t, []uint{7, 8, 9}, port.ids)
	require.Equal(t, "seven@example.com", results[0].Owner.Email)
	require.Equal(t, "seven@example.com", results[1].Owner.Email)
	require.Equal(t, "eight@example.com", results[2].Owner.Email)
	require.Nil(t, results[3].Owner)
}

func TestAttachOwnersSkipsNonAdminAndNonAllScopes(t *testing.T) {
	for _, filter := range []OrderListFilter{
		{IsAdmin: false, Scope: "all"}, // buyer's own list
		{IsAdmin: true, Scope: "mine"}, // admin viewing own orders
	} {
		port := &ownerLookupPortSpy{owners: map[uint]OrderOwnerSummary{7: {ID: 7}}}
		uc := &UseCase{owners: port}
		results := []CheckoutResult{{Order: domain.Order{UserID: 7}}}

		require.NoError(t, uc.attachOwners(context.Background(), filter, results))
		require.Equal(t, 0, port.calls)
		require.Nil(t, results[0].Owner)
	}
}

func TestAttachOwnersPropagatesLookupError(t *testing.T) {
	wantErr := errors.New("owner lookup failed")
	port := &ownerLookupPortSpy{err: wantErr}
	uc := &UseCase{owners: port}

	err := uc.attachOwners(context.Background(), OrderListFilter{IsAdmin: true, Scope: "all"}, []CheckoutResult{{
		Order: domain.Order{UserID: 7},
	}})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, port.calls)
}
