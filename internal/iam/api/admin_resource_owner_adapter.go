package api

import (
	"context"
	"fmt"

	coreapp "github.com/donnel666/remail/internal/core/app"
	iamapp "github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
)

// AdminResourceOwnerAdapter publishes the IAM-owned safe owner summary to
// the Core administrator composition use case. Password/session/permission
// facts never cross this port.
type AdminResourceOwnerAdapter struct {
	users iamapp.UserRepository
}

func NewAdminResourceOwnerAdapter(users iamapp.UserRepository) *AdminResourceOwnerAdapter {
	return &AdminResourceOwnerAdapter{users: users}
}

func (a *AdminResourceOwnerAdapter) GetByIDs(ctx context.Context, ids []uint) (map[uint]coreapp.AdminOwnerSummary, error) {
	if a == nil || a.users == nil {
		return nil, fmt.Errorf("admin resource owner repository is unavailable")
	}
	users, err := a.users.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[uint]coreapp.AdminOwnerSummary, len(users))
	for i := range users {
		result[users[i].ID] = adminOwnerSummary(users[i])
	}
	return result, nil
}

func (a *AdminResourceOwnerAdapter) SearchAdminOwners(ctx context.Context, search string, limit int) ([]coreapp.AdminOwnerSummary, error) {
	if a == nil || a.users == nil {
		return nil, fmt.Errorf("admin resource owner repository is unavailable")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 1000 {
		limit = 1000
	}
	users, err := a.users.ListByFilter(ctx, domain.UserListFilter{Search: search}, 0, limit)
	if err != nil {
		return nil, err
	}
	result := make([]coreapp.AdminOwnerSummary, len(users))
	for i := range users {
		result[i] = adminOwnerSummary(users[i])
	}
	return result, nil
}

func (a *AdminResourceOwnerAdapter) ValidateTargetOwner(ctx context.Context, id uint) (*coreapp.AdminOwnerSummary, error) {
	if a == nil || a.users == nil || id == 0 {
		return nil, nil
	}
	user, err := a.users.FindByID(ctx, id)
	if err != nil || user == nil {
		return nil, err
	}
	result := adminOwnerSummary(*user)
	return &result, nil
}

func adminOwnerSummary(user domain.User) coreapp.AdminOwnerSummary {
	groupName := user.UserGroup.Name
	if groupName == "" {
		groupName = user.UserGroup.Code
	}
	return coreapp.AdminOwnerSummary{
		ID:        user.ID,
		Email:     user.Email,
		Nickname:  user.Nickname,
		GroupName: groupName,
		Role:      user.Role.String(),
		Enabled:   user.IsActive(),
	}
}

var _ coreapp.OwnerQueryPort = (*AdminResourceOwnerAdapter)(nil)
