package api

import (
	"context"
	"errors"

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

type MicrosoftValidationBindingCommitAdapter struct {
	repo *mailinfra.MicrosoftBindingRepo
}

var (
	_ coreapp.BindingQueryPort                     = (*MicrosoftBindingQueryAdapter)(nil)
	_ coreapp.BindingAdminPort                     = (*MicrosoftBindingAdminAdapter)(nil)
	_ coreapp.MicrosoftValidationBindingCommitPort = (*MicrosoftValidationBindingCommitAdapter)(nil)
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

func NewMicrosoftValidationBindingCommitAdapter(repo *mailinfra.MicrosoftBindingRepo) *MicrosoftValidationBindingCommitAdapter {
	return &MicrosoftValidationBindingCommitAdapter{repo: repo}
}

func (a *MicrosoftValidationBindingCommitAdapter) CommitValidationBinding(ctx context.Context, command coreapp.MicrosoftValidationBindingCommand) (bool, error) {
	if a == nil || a.repo == nil {
		return false, coreapp.ErrValidationTemporaryUnavailable
	}
	if recovered := command.RecoveredBinding; recovered != nil {
		result, err := a.repo.ApplyRecoveredBindingForValidation(ctx, mailinfra.MicrosoftRecoveredBindingInput{
			ResourceID:               command.ResourceID,
			BindingAddress:           recovered.Address,
			ExpectedOwnerUserID:      command.OwnerUserID,
			ExpectedAccountEmail:     command.AccountEmail,
			ExpectedBindingID:        recovered.ExpectedBindingID,
			ExpectedBindingAddress:   recovered.ExpectedBindingAddress,
			ExpectedBindingUpdatedAt: recovered.ExpectedBindingUpdatedAt,
		})
		if err != nil {
			return false, mapMicrosoftValidationBindingError(err)
		}
		return result != nil && result.Changed, nil
	}
	if observation := command.BindingObservation; observation != nil {
		changed, err := a.repo.ApplyValidationBindingObservation(ctx, mailinfra.MicrosoftValidationBindingObservationInput{
			ResourceID:   command.ResourceID,
			OwnerUserID:  command.OwnerUserID,
			AccountEmail: command.AccountEmail,
			Address:      observation.Address,
			Status:       observation.Status,
			BoundDisplay: observation.BoundDisplay,
			SafeMessage:  observation.SafeMessage,
		})
		if err != nil {
			return false, mapMicrosoftValidationBindingError(err)
		}
		return changed, nil
	}
	return false, nil
}

func mapMicrosoftValidationBindingError(err error) error {
	if errors.Is(err, mailinfra.ErrMicrosoftBindingRecoveryConflict) ||
		errors.Is(err, mailinfra.ErrMicrosoftBindingRecoveryResourceNotFound) ||
		errors.Is(err, mailinfra.ErrMicrosoftBindingRecoveryResourceDeleted) {
		return coreapp.ErrValidationResultStale
	}
	if errors.Is(err, mailinfra.ErrMicrosoftBindingRecoveryIneligible) ||
		errors.Is(err, mailinfra.ErrMicrosoftBindingAddressOccupied) {
		return coreapp.ErrValidationBindingRejected
	}
	return err
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

func (a *MicrosoftBindingQueryAdapter) CountActiveByDomains(ctx context.Context, domains []string) (map[string]int64, error) {
	if a == nil || a.repo == nil {
		return map[string]int64{}, nil
	}
	return a.repo.CountActiveByDomains(ctx, domains)
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
