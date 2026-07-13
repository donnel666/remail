package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
)

// MicrosoftAliasValidationAdapter gives Core a deliberately narrow callback
// after its validation transaction has succeeded. MailTransport owns the
// durable alias schedule and worker; Core never writes that table directly.
type MicrosoftAliasValidationAdapter struct {
	service *MailTransportModule
}

var _ coreapp.MicrosoftAliasScheduleTriggerPort = (*MicrosoftAliasValidationAdapter)(nil)

func NewMicrosoftAliasValidationAdapter(module *MailTransportModule) *MicrosoftAliasValidationAdapter {
	return &MicrosoftAliasValidationAdapter{service: module}
}

func (a *MicrosoftAliasValidationAdapter) EnsureForValidatedMicrosoftResource(ctx context.Context, resourceID uint) error {
	if a == nil || a.service == nil || a.service.MicrosoftAliases == nil {
		return nil
	}
	return a.service.MicrosoftAliases.EnsureForValidatedMicrosoftResource(ctx, resourceID)
}
