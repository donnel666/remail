package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
)

type MicrosoftBindingInputAdapter struct {
	repo *mailinfra.MicrosoftBindingRepo
}

func NewMicrosoftBindingInputAdapter(repo *mailinfra.MicrosoftBindingRepo) *MicrosoftBindingInputAdapter {
	return &MicrosoftBindingInputAdapter{repo: repo}
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
