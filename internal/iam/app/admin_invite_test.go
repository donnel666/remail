package app

import (
	"context"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

// --- stubs (embed the port interfaces; only the exercised methods are set) ---

type stubInviteRepo struct {
	InviteRepository
	listByFilter  func(domain.InviteListFilter, int, int) ([]domain.Invite, error)
	countByFilter func(domain.InviteListFilter) (int64, error)
	facets        func(domain.InviteKind) (*domain.InviteFacets, error)
	resolveCodes  func(domain.InviteListFilter) ([]string, error)
	setEnabled    func([]string, bool) (int64, error)
	listUses      func(string, int) ([]domain.InviteUse, error)
	createBatch   func([]*domain.Invite, uint) error
	createInvite  func(*domain.Invite, uint) error
	updateInvite  func(*domain.Invite) error
	findByCode    func(string) (*domain.Invite, error)
}

func (s *stubInviteRepo) ListInvitesByFilter(_ context.Context, f domain.InviteListFilter, offset, limit int) ([]domain.Invite, error) {
	return s.listByFilter(f, offset, limit)
}
func (s *stubInviteRepo) CountInvitesByFilter(_ context.Context, f domain.InviteListFilter) (int64, error) {
	return s.countByFilter(f)
}
func (s *stubInviteRepo) InviteFacetsByFilter(_ context.Context, kind domain.InviteKind) (*domain.InviteFacets, error) {
	if s.facets == nil {
		return &domain.InviteFacets{}, nil
	}
	return s.facets(kind)
}
func (s *stubInviteRepo) ResolveInviteCodesByFilter(_ context.Context, f domain.InviteListFilter) ([]string, error) {
	return s.resolveCodes(f)
}
func (s *stubInviteRepo) BatchSetInviteEnabled(_ context.Context, codes []string, enabled bool) (int64, error) {
	return s.setEnabled(codes, enabled)
}
func (s *stubInviteRepo) ListInviteUses(_ context.Context, code string, limit int) ([]domain.InviteUse, error) {
	return s.listUses(code, limit)
}
func (s *stubInviteRepo) CreateInvitesBatch(_ context.Context, invites []*domain.Invite, createdByUserID uint, _ *governancedomain.OperationLog) error {
	return s.createBatch(invites, createdByUserID)
}
func (s *stubInviteRepo) CreateInviteWithOperationLog(_ context.Context, invite *domain.Invite, createdByUserID uint, _ *governancedomain.OperationLog) error {
	return s.createInvite(invite, createdByUserID)
}
func (s *stubInviteRepo) UpdateInviteWithOperationLog(_ context.Context, invite *domain.Invite, _ *governancedomain.OperationLog) error {
	return s.updateInvite(invite)
}
func (s *stubInviteRepo) FindInviteByCode(_ context.Context, code string) (*domain.Invite, error) {
	return s.findByCode(code)
}

type stubOwnerRepo struct {
	UserRepository
	lookup func([]uint) (map[uint]domain.UserSummary, error)
}

func (s *stubOwnerRepo) LookupUserSummaries(_ context.Context, ids []uint) (map[uint]domain.UserSummary, error) {
	return s.lookup(ids)
}

type noopLogs struct{}

func (noopLogs) Create(_ context.Context, _ *governancedomain.OperationLog) error { return nil }

func uintPtr(v uint) *uint { return &v }

// --- tests ---

func TestListInvitesEnrichesOwnersAndClampsLimit(t *testing.T) {
	owner := uintPtr(10)
	invites := []domain.Invite{
		{Code: "A", Kind: domain.InviteKindAdmin, CreatedByUserID: owner},
		{Code: "B", Kind: domain.InviteKindAdmin, CreatedByUserID: nil}, // owner-less
	}
	var gotLimit int
	var gotLookup []uint
	var gotFacetKind domain.InviteKind
	invRepo := &stubInviteRepo{
		listByFilter: func(_ domain.InviteListFilter, _ int, limit int) ([]domain.Invite, error) {
			gotLimit = limit
			return invites, nil
		},
		countByFilter: func(domain.InviteListFilter) (int64, error) { return 2, nil },
		facets: func(kind domain.InviteKind) (*domain.InviteFacets, error) {
			gotFacetKind = kind
			return &domain.InviteFacets{Enabled: domain.InviteEnabledFacet{All: 2, Enabled: 2}}, nil
		},
	}
	ownerRepo := &stubOwnerRepo{lookup: func(ids []uint) (map[uint]domain.UserSummary, error) {
		gotLookup = ids
		return map[uint]domain.UserSummary{10: {ID: 10, Email: "o@x.io", Role: "admin", GroupID: 3, GroupName: "VIP"}}, nil
	}}
	uc := NewAdminUseCase(ownerRepo, nil, invRepo, nil, nil, noopLogs{})

	res, err := uc.ListInvites(context.Background(), domain.InviteListFilter{Kind: domain.InviteKindAdmin}, 0, 1000)
	require.NoError(t, err)
	require.Equal(t, 100, gotLimit, "limit should be clamped to 100")
	require.Equal(t, []uint{10}, gotLookup, "only the single non-nil owner id, deduped")
	require.Equal(t, domain.InviteKindAdmin, gotFacetKind)
	require.Len(t, res.Items, 2)
	require.NotNil(t, res.Items[0].Owner)
	require.Equal(t, "o@x.io", res.Items[0].Owner.Email)
	require.Nil(t, res.Items[1].Owner, "owner-less invite stays unresolved")
	require.Equal(t, int64(2), res.Facets.Enabled.All)
}

func TestBatchCreateInvitesGeneratesUniquePrefixedCodes(t *testing.T) {
	var captured []*domain.Invite
	var createdBy uint
	invRepo := &stubInviteRepo{createBatch: func(invites []*domain.Invite, by uint) error {
		captured = invites
		createdBy = by
		return nil
	}}
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})

	expire := time.Now().Add(time.Hour)
	out, err := uc.BatchCreateInvites(context.Background(), 7, "req-1", "/p", BatchCreateInviteRequest{
		Count: 5, MaxUse: 3, Enabled: true, ExpireAt: &expire, Prefix: "promo",
	})
	require.NoError(t, err)
	require.Len(t, out, 5)
	require.Equal(t, uint(7), createdBy)

	seen := map[string]bool{}
	for _, inv := range captured {
		require.True(t, strings.HasPrefix(inv.Code, "PROMO"), "prefix upper-cased and applied: %s", inv.Code)
		require.False(t, seen[inv.Code], "codes are unique: %s", inv.Code)
		seen[inv.Code] = true
		require.Equal(t, domain.InviteKindAdmin, inv.Kind)
		require.Equal(t, 3, inv.MaxUse)
		require.True(t, inv.Enabled)
		require.Equal(t, &expire, inv.ExpireAt)
	}
}

func TestBatchCreateInvitesValidates(t *testing.T) {
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, &stubInviteRepo{}, nil, nil, noopLogs{})
	past := time.Now().Add(-time.Hour)
	cases := []BatchCreateInviteRequest{
		{Count: 0, MaxUse: 1},
		{Count: 101, MaxUse: 1},
		{Count: 1, MaxUse: 0},
		{Count: 1, MaxUse: 1, ExpireAt: &past},
	}
	for _, req := range cases {
		_, err := uc.BatchCreateInvites(context.Background(), 1, "r", "/p", req)
		require.ErrorIs(t, err, domain.ErrInviteInvalid)
	}
}

func TestBulkSetInviteEnabledIdsAndFilter(t *testing.T) {
	var gotCodes []string
	var gotFilter domain.InviteListFilter
	invRepo := &stubInviteRepo{
		setEnabled: func(codes []string, _ bool) (int64, error) {
			gotCodes = codes
			return int64(len(codes) - 1), nil // one already in target state
		},
		resolveCodes: func(f domain.InviteListFilter) ([]string, error) {
			gotFilter = f
			return []string{"X", "Y"}, nil
		},
	}
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})

	// ids mode: requested is the raw code count; skipped counts the unchanged row.
	res, err := uc.BulkSetInviteEnabled(context.Background(), 1, "r", "/p",
		InviteBulkSelection{Mode: "ids", Codes: []string{"A", "B", "C"}}, true)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C"}, gotCodes)
	require.Equal(t, 3, res.Requested)
	require.Equal(t, 2, res.Affected)
	require.Equal(t, 1, res.Skipped)

	// filter mode: requested is the number of resolved codes.
	res, err = uc.BulkSetInviteEnabled(context.Background(), 1, "r", "/p",
		InviteBulkSelection{Mode: "filter", Filter: domain.InviteListFilter{Kind: domain.InviteKindAdmin}}, false)
	require.NoError(t, err)
	require.Equal(t, domain.InviteKindAdmin, gotFilter.Kind)
	require.Equal(t, 2, res.Requested)
	require.Equal(t, 1, res.Affected)
}

func TestCreateInviteAutoGeneratesBlankCode(t *testing.T) {
	var captured *domain.Invite
	invRepo := &stubInviteRepo{createInvite: func(inv *domain.Invite, _ uint) error {
		captured = inv
		return nil
	}}
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})

	inv, err := uc.CreateInvite(context.Background(), 1, "r", "/p", CreateInviteRequest{Code: "  ", MaxUse: 3, Enabled: true})
	require.NoError(t, err)
	require.NotEmpty(t, inv.Code, "blank code is server-generated")
	require.Equal(t, inv.Code, captured.Code)
}

func TestUpdateInviteExpireAtTriState(t *testing.T) {
	future := time.Now().Add(time.Hour)
	run := func(expireAt **time.Time) *domain.Invite {
		var captured *domain.Invite
		invRepo := &stubInviteRepo{
			findByCode: func(string) (*domain.Invite, error) {
				return &domain.Invite{Code: "A", Kind: domain.InviteKindAdmin, ExpireAt: &future}, nil
			},
			updateInvite: func(inv *domain.Invite) error { captured = inv; return nil },
		}
		uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})
		_, err := uc.UpdateInvite(context.Background(), 1, "r", "/p", "A", UpdateInviteRequest{ExpireAt: expireAt})
		require.NoError(t, err)
		return captured
	}

	require.NotNil(t, run(nil).ExpireAt, "absent (nil carrier) leaves the existing expiry")
	var cleared *time.Time
	require.Nil(t, run(&cleared).ExpireAt, "null (carrier -> nil) clears the expiry")
	next := time.Now().Add(2 * time.Hour)
	np := &next
	require.Equal(t, &next, run(&np).ExpireAt, "value sets the expiry")
}

func TestUpdateInviteAcceptsReferralKind(t *testing.T) {
	var captured *domain.Invite
	invRepo := &stubInviteRepo{
		findByCode: func(string) (*domain.Invite, error) {
			return &domain.Invite{Code: "AFF1", Kind: domain.InviteKindReferral, Enabled: true, MaxUse: 100, Used: 3}, nil
		},
		updateInvite: func(inv *domain.Invite) error { captured = inv; return nil },
	}
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})

	disabled := false
	maxUse := 50
	_, err := uc.UpdateInvite(context.Background(), 1, "r", "/p", "AFF1", UpdateInviteRequest{Enabled: &disabled, MaxUse: &maxUse})
	require.NoError(t, err)
	require.False(t, captured.Enabled, "admins can disable a referral invite")
	require.Equal(t, 50, captured.MaxUse)
}

func TestListInviteUsesNotFoundAndEnrichment(t *testing.T) {
	invRepo := &stubInviteRepo{findByCode: func(string) (*domain.Invite, error) { return nil, nil }}
	uc := NewAdminUseCase(&stubOwnerRepo{}, nil, invRepo, nil, nil, noopLogs{})
	_, err := uc.ListInviteUses(context.Background(), "missing")
	require.ErrorIs(t, err, domain.ErrInviteNotFound)

	uses := []domain.InviteUse{
		{ID: 2, InviteCode: "A", UserID: 5, UsedAt: time.Now()},
		{ID: 1, InviteCode: "A", UserID: 9, UsedAt: time.Now().Add(-time.Hour)},
	}
	invRepo = &stubInviteRepo{
		findByCode: func(string) (*domain.Invite, error) { return &domain.Invite{Code: "A"}, nil },
		listUses:   func(_ string, limit int) ([]domain.InviteUse, error) { require.Equal(t, 500, limit); return uses, nil },
	}
	ownerRepo := &stubOwnerRepo{lookup: func([]uint) (map[uint]domain.UserSummary, error) {
		return map[uint]domain.UserSummary{5: {ID: 5, Email: "u5@x.io", Role: "user"}}, nil // 9 absent
	}}
	uc = NewAdminUseCase(ownerRepo, nil, invRepo, nil, nil, noopLogs{})

	items, err := uc.ListInviteUses(context.Background(), "A")
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NotNil(t, items[0].User)
	require.Equal(t, "u5@x.io", items[0].User.Email)
	require.Nil(t, items[1].User, "unresolved redeemer stays nil")
}
