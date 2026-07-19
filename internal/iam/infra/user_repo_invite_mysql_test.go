package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func inviteTestOpLog(requestID string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "iam.invite.test",
		ResourceType:   "invite",
		ResourceID:     "test",
		Path:           "/v1/admin/invites",
		Result:         "success",
		SafeSummary:    "test",
		RequestID:      requestID,
	}
}

// TestUserRepoInviteBrowseAndEnrichmentMySQL exercises the owner-joined invite
// list/facet/enrichment SQL end to end against a real MySQL schema.
func TestUserRepoInviteBrowseAndEnrichmentMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	staff := &domain.UserGroup{Code: "staff", Name: "Staff", Enabled: true}
	require.NoError(t, repo.CreateUserGroup(ctx, staff))
	vip := &domain.UserGroup{Code: "vip", Name: "VIP", Enabled: true}
	require.NoError(t, repo.CreateUserGroup(ctx, vip))

	admin := &domain.User{Email: "admin@test.local", PasswordHash: "h", Nickname: "Admin", Status: domain.UserStatusActive, Role: domain.RoleAdmin, UserGroupID: staff.ID}
	require.NoError(t, repo.Create(ctx, admin))
	member := &domain.User{Email: "member@test.local", PasswordHash: "h", Nickname: "Member", Status: domain.UserStatusActive, Role: domain.RoleUser, UserGroupID: vip.ID}
	require.NoError(t, repo.Create(ctx, member))

	inv1 := &domain.Invite{Code: "ADMIN1", Enabled: true, MaxUse: 5}
	require.NoError(t, repo.CreateInviteWithOperationLog(ctx, inv1, admin.ID, inviteTestOpLog("r1")))
	inv2 := &domain.Invite{Code: "ADMIN2", Enabled: false, MaxUse: 5}
	require.NoError(t, repo.CreateInviteWithOperationLog(ctx, inv2, admin.ID, inviteTestOpLog("r2")))
	ref, err := repo.GetOrCreateReferralInvite(ctx, member.ID, "AFFMEMBER01", 100)
	require.NoError(t, err)

	adminFilter := domain.InviteListFilter{Kind: domain.InviteKindAdmin}

	// kind filter + owner resolution
	total, err := repo.CountInvitesByFilter(ctx, adminFilter)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	items, err := repo.ListInvitesByFilter(ctx, adminFilter, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byCode := map[string]domain.Invite{}
	for _, it := range items {
		byCode[it.Code] = it
	}
	require.Contains(t, byCode, "ADMIN1")
	require.Equal(t, admin.ID, *byCode["ADMIN1"].CreatedByUserID)
	require.Equal(t, domain.InviteKindAdmin, byCode["ADMIN1"].Kind)

	allTotal, err := repo.CountInvitesByFilter(ctx, domain.InviteListFilter{})
	require.NoError(t, err)
	require.Equal(t, int64(3), allTotal)

	// enabled + ownerRole + ownerGroup + search filters
	disabled := false
	got, err := repo.CountInvitesByFilter(ctx, domain.InviteListFilter{Kind: domain.InviteKindAdmin, Enabled: &disabled})
	require.NoError(t, err)
	require.Equal(t, int64(1), got)

	userRole := domain.RoleUser
	items, err = repo.ListInvitesByFilter(ctx, domain.InviteListFilter{OwnerRole: &userRole}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ref.Code, items[0].Code)

	items, err = repo.ListInvitesByFilter(ctx, domain.InviteListFilter{OwnerGroupID: &vip.ID}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ref.Code, items[0].Code)

	items, err = repo.ListInvitesByFilter(ctx, domain.InviteListFilter{Search: "member@"}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ref.Code, items[0].Code)

	items, err = repo.ListInvitesByFilter(ctx, domain.InviteListFilter{Search: "ADMIN"}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 2)

	items, err = repo.ListInvitesByFilter(ctx, domain.InviteListFilter{Kind: domain.InviteKindAdmin, Search: fmt.Sprintf("%d", admin.ID)}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 2, "search by owner id matches both admin-owned invites")

	// facets over kind=admin only
	f, err := repo.InviteFacetsByFilter(ctx, domain.InviteKindAdmin)
	require.NoError(t, err)
	require.Equal(t, int64(2), f.Role.All)
	require.Equal(t, int64(2), f.Role.Admin)
	require.Equal(t, int64(2), f.Enabled.All)
	require.Equal(t, int64(1), f.Enabled.Enabled)
	require.Equal(t, int64(1), f.Enabled.Disabled)
	require.Len(t, f.Group, 1)
	require.Equal(t, staff.ID, f.Group[0].ID)
	require.Equal(t, "Staff", f.Group[0].Name)
	require.Equal(t, int64(2), f.Group[0].Count)

	fAll, err := repo.InviteFacetsByFilter(ctx, "")
	require.NoError(t, err)
	require.Equal(t, int64(3), fAll.Role.All)
	require.Equal(t, int64(2), fAll.Role.Admin)
	require.Equal(t, int64(1), fAll.Role.User)
	groupCounts := map[uint]int64{}
	for _, g := range fAll.Group {
		groupCounts[g.ID] = g.Count
	}
	require.Equal(t, int64(2), groupCounts[staff.ID])
	require.Equal(t, int64(1), groupCounts[vip.ID])

	// LookupUserSummaries batch join
	sums, err := repo.LookupUserSummaries(ctx, []uint{admin.ID, member.ID, 99999})
	require.NoError(t, err)
	require.Len(t, sums, 2)
	require.Equal(t, "admin@test.local", sums[admin.ID].Email)
	require.Equal(t, "admin", sums[admin.ID].Role)
	require.Equal(t, staff.ID, sums[admin.ID].GroupID)
	require.Equal(t, "Staff", sums[admin.ID].GroupName)
	require.Equal(t, "VIP", sums[member.ID].GroupName)

	// ListUserSummaries search/total/order
	list, count, err := repo.ListUserSummaries(ctx, "member@", 0, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Len(t, list, 1)
	require.Equal(t, member.ID, list[0].ID)
	listAll, countAll, err := repo.ListUserSummaries(ctx, "", 0, 10)
	require.NoError(t, err)
	require.Equal(t, 2, countAll)
	require.Len(t, listAll, 2)
	require.Equal(t, admin.ID, listAll[0].ID, "ordered by id asc")

	// bulk resolve + idempotent enable/disable
	rc, err := repo.ResolveInviteCodesByFilter(ctx, adminFilter)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"ADMIN1", "ADMIN2"}, rc)
	affected, err := repo.BatchSetInviteEnabled(ctx, []string{"ADMIN1", "ADMIN2"}, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected, "ADMIN1 flips; ADMIN2 already disabled and is skipped")

	// batch create (mixed enabled to prove enabled=false is not flipped to the
	// column default of true)
	batch := []*domain.Invite{{Code: "BULK1", Enabled: true, MaxUse: 2}, {Code: "BULK2", Enabled: false, MaxUse: 2}}
	require.NoError(t, repo.CreateInvitesBatch(ctx, batch, admin.ID, inviteTestOpLog("rb")))
	require.False(t, batch[0].CreatedAt.IsZero(), "timestamps written back")
	bulk1, err := repo.FindInviteByCode(ctx, "BULK1")
	require.NoError(t, err)
	require.NotNil(t, bulk1)
	require.Equal(t, domain.InviteKindAdmin, bulk1.Kind)
	require.Equal(t, admin.ID, *bulk1.CreatedByUserID)
	require.True(t, bulk1.Enabled)
	bulk2, err := repo.FindInviteByCode(ctx, "BULK2")
	require.NoError(t, err)
	require.NotNil(t, bulk2)
	require.False(t, bulk2.Enabled, "explicit enabled=false must survive batch insert")

	// redemption history newest first, with cap honored
	require.NoError(t, db.Create(&InviteUseModel{InviteCode: "ADMIN1", UserID: member.ID, UsedAt: time.Now().Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&InviteUseModel{InviteCode: "ADMIN1", UserID: admin.ID, UsedAt: time.Now()}).Error)
	uses, err := repo.ListInviteUses(ctx, "ADMIN1", 500)
	require.NoError(t, err)
	require.Len(t, uses, 2)
	require.Equal(t, admin.ID, uses[0].UserID)
	require.Equal(t, member.ID, uses[1].UserID)

	// admins can now edit referral invites too: the single-update no longer has
	// an invite_kind='admin' WHERE guard, so it hits the referral row.
	ref.Enabled = false
	require.NoError(t, repo.UpdateInviteWithOperationLog(ctx, ref, inviteTestOpLog("rref")))
	gotRef, err := repo.FindInviteByCode(ctx, ref.Code)
	require.NoError(t, err)
	require.False(t, gotRef.Enabled, "admin can disable a referral invite")
}
