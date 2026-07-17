package api

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
)

// AdminUserSelectionResolver resolves an admin user bulk selection to the set of
// adjustable (non-super-admin) user IDs for cross-bounded-context callers such
// as billing balance adjustments. It exposes only primitive parameters so no
// shared selection struct crosses the port.
type AdminUserSelectionResolver struct {
	repo *infra.UserRepo
}

func NewAdminUserSelectionResolver(repo *infra.UserRepo) *AdminUserSelectionResolver {
	return &AdminUserSelectionResolver{repo: repo}
}

// ResolveAdjustableUserIDs returns the non-super-admin user IDs targeted by the
// selection (uncapped). In ids mode an empty id list is a no-op; in filter mode
// a non-empty but invalid role is rejected to avoid selecting every user.
func (a *AdminUserSelectionResolver) ResolveAdjustableUserIDs(ctx context.Context, mode string, userIDs []uint, search string, role string, enabled *bool, userGroupID uint, createdFrom *time.Time, createdTo *time.Time) ([]uint, error) {
	if a == nil || a.repo == nil {
		return nil, nil
	}
	if mode == "ids" {
		if len(userIDs) == 0 {
			return nil, nil
		}
		return a.repo.ResolveBulkUserIDs(ctx, userIDs, domain.UserListFilter{})
	}
	filter := domain.UserListFilter{
		Search:      search,
		Enabled:     enabled,
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}
	if role != "" {
		r := domain.Role(role)
		if !r.IsValid() {
			return nil, domain.ErrInvalidRole
		}
		filter.Role = &r
	}
	if userGroupID != 0 {
		filter.UserGroupID = &userGroupID
	}
	return a.repo.ResolveBulkUserIDs(ctx, nil, filter)
}
