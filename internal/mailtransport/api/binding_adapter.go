package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
)

type MicrosoftBindingInputAdapter struct {
	repo *mailinfra.MicrosoftBindingRepo
}

type MicrosoftBindingQueryAdapter struct {
	repo *mailinfra.MicrosoftBindingRepo
}

type MicrosoftBindingAdminAdapter struct {
	repo *mailinfra.MicrosoftBindingRepo
}

var (
	_ coreapp.BindingQueryPort = (*MicrosoftBindingQueryAdapter)(nil)
	_ coreapp.BindingAdminPort = (*MicrosoftBindingAdminAdapter)(nil)
)

func NewMicrosoftBindingInputAdapter(repo *mailinfra.MicrosoftBindingRepo) *MicrosoftBindingInputAdapter {
	return &MicrosoftBindingInputAdapter{repo: repo}
}

func NewMicrosoftBindingQueryAdapter(repo *mailinfra.MicrosoftBindingRepo) *MicrosoftBindingQueryAdapter {
	return &MicrosoftBindingQueryAdapter{repo: repo}
}

func NewMicrosoftBindingAdminAdapter(repo *mailinfra.MicrosoftBindingRepo) *MicrosoftBindingAdminAdapter {
	return &MicrosoftBindingAdminAdapter{repo: repo}
}

func (a *MicrosoftBindingAdminAdapter) ReplaceAdminInput(ctx context.Context, command coreapp.AdminBindingCommand) error {
	if a == nil || a.repo == nil {
		return nil
	}
	return a.repo.ReplaceAdminInput(
		ctx,
		command.ResourceID,
		command.OwnerUserID,
		command.AccountEmail,
		command.BindingAddressSet,
		command.BindingAddress,
	)
}

func (a *MicrosoftBindingQueryAdapter) GetByResourceIDs(ctx context.Context, resourceIDs []uint) (map[uint]coreapp.AdminBindingSummary, error) {
	result := make(map[uint]coreapp.AdminBindingSummary)
	if a == nil || a.repo == nil || len(resourceIDs) == 0 {
		return result, nil
	}
	bindings, err := a.repo.FindByResourceIDs(ctx, resourceIDs)
	if err != nil {
		return nil, err
	}
	result = make(map[uint]coreapp.AdminBindingSummary, len(bindings))
	for resourceID, binding := range bindings {
		result[resourceID] = coreapp.AdminBindingSummary{
			ID:            binding.ID,
			ResourceID:    binding.ResourceID,
			EmailAddress:  binding.BindingAddress,
			Status:        string(binding.Status),
			LastSafeError: binding.LastSafeError,
			UpdatedAt:     binding.UpdatedAt,
		}
	}
	return result, nil
}

func (a *MicrosoftBindingInputAdapter) RecordMicrosoftBindingInputs(ctx context.Context, inputs []coreapp.MicrosoftBindingInput) error {
	if a == nil || a.repo == nil || len(inputs) == 0 {
		return nil
	}
	repoInputs := make([]mailinfra.MicrosoftBindingImportInput, 0, len(inputs))
	for _, input := range inputs {
		repoInputs = append(repoInputs, mailinfra.MicrosoftBindingImportInput{
			OwnerUserID:    input.OwnerUserID,
			AccountEmail:   input.EmailAddress,
			BindingAddress: input.BindingAddress,
		})
	}
	return a.repo.UpsertByEmail(ctx, repoInputs)
}
