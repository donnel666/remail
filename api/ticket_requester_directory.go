package api

import (
	"context"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
)

// ticketParticipantDirectory adapts IAM's user data to the safe aftersale
// participant directory and lives here to avoid a cross-context dependency.
type ticketParticipantDirectory struct {
	owners coreapp.OwnerQueryPort
	users  *iaminfra.UserRepo
}

func (d ticketParticipantDirectory) GetByIDs(ctx context.Context, ids []uint) (map[uint]aftersaleapp.RequesterSummary, error) {
	if d.owners == nil || len(ids) == 0 {
		return map[uint]aftersaleapp.RequesterSummary{}, nil
	}
	summaries, err := d.owners.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]aftersaleapp.RequesterSummary, len(summaries))
	for id, summary := range summaries {
		out[id] = aftersaleapp.RequesterSummary{
			ID:        summary.ID,
			Email:     summary.Email,
			Nickname:  summary.Nickname,
			GroupName: summary.GroupName,
			Role:      summary.Role,
			Enabled:   summary.Enabled,
		}
	}
	return out, nil
}

func (d ticketParticipantDirectory) ListActiveSuperAdmins(ctx context.Context) ([]aftersaleapp.RequesterSummary, error) {
	if d.users == nil {
		return []aftersaleapp.RequesterSummary{}, nil
	}
	role, enabled := iamdomain.RoleSuperAdmin, true
	users, err := d.users.ListByFilter(ctx, iamdomain.UserListFilter{Role: &role, Enabled: &enabled}, 0, -1)
	if err != nil {
		return nil, err
	}
	out := make([]aftersaleapp.RequesterSummary, len(users))
	for i := range users {
		out[i] = aftersaleapp.RequesterSummary{
			ID:       users[i].ID,
			Email:    users[i].Email,
			Nickname: users[i].Nickname,
			Role:     users[i].Role.String(),
			Enabled:  users[i].IsActive(),
		}
	}
	return out, nil
}
