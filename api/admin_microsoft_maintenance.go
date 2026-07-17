package api

import (
	"context"
	"errors"
	"fmt"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type adminMicrosoftMaintenanceAdapter struct {
	aliases *mailapp.MicrosoftAliasService
	tokens  *mailapp.MicrosoftTokenRefreshService
	history *mailmatchapp.ResourceFetchUseCase
}

func (a adminMicrosoftMaintenanceAdapter) SubmitAdminResourceMaintenance(
	ctx context.Context,
	command coreapp.AdminResourceMaintenanceCommand,
) (string, error) {
	switch command.Action {
	case coreapp.AdminResourceBulkAlias:
		if a.aliases == nil {
			return "", fmt.Errorf("microsoft alias service is unavailable")
		}
		_, err := a.aliases.AcceptAdminExpedite(ctx, mailapp.MicrosoftAliasExpediteCommand{
			ResourceID: command.ResourceID, OperatorUserID: command.OperatorUserID,
			IdempotencyKey: command.IdempotencyKey, RequestID: command.RequestID, Path: command.Path,
		})
		return adminAliasMaintenanceResult(err)
	case coreapp.AdminResourceBulkHistory:
		if a.history == nil {
			return "", fmt.Errorf("microsoft project history service is unavailable")
		}
		_, err := a.history.Submit(ctx, mailmatchapp.ResourceFetchSubmitCommand{
			Kind:       mailmatchdomain.ResourceFetchJobHistory,
			ResourceID: command.ResourceID, OperatorUserID: command.OperatorUserID,
			IdempotencyKey: command.IdempotencyKey, RequestID: command.RequestID, Path: command.Path,
		})
		return adminHistoryMaintenanceResult(err)
	case coreapp.AdminResourceBulkToken:
		if a.tokens == nil {
			return "", fmt.Errorf("microsoft token refresh service is unavailable")
		}
		_, err := a.tokens.Accept(ctx, mailapp.MicrosoftTokenRefreshCommand{
			ResourceID: command.ResourceID, OperatorUserID: command.OperatorUserID,
			IdempotencyKey: command.IdempotencyKey, RequestID: command.RequestID, Path: command.Path,
		})
		return adminTokenMaintenanceResult(err)
	default:
		return "", fmt.Errorf("unsupported microsoft maintenance action %q", command.Action)
	}
}

func adminAliasMaintenanceResult(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, mailapp.ErrMicrosoftAliasResourceNotFound):
		return "not_found", nil
	case errors.Is(err, mailapp.ErrMicrosoftAliasResourceConflict),
		errors.Is(err, mailapp.ErrMicrosoftAliasScheduleNotFound),
		errors.Is(err, mailapp.ErrMicrosoftAliasSchedulePaused):
		return "invalid_state", nil
	default:
		return "", err
	}
}

func adminHistoryMaintenanceResult(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, mailmatchdomain.ErrResourceFetchNotFound):
		return "not_found", nil
	case errors.Is(err, mailmatchdomain.ErrResourceFetchDeleted):
		return "invalid_state", nil
	case errors.Is(err, mailmatchdomain.ErrResourceFetchJobConflict):
		return "active_task_conflict", nil
	case errors.Is(err, mailmatchdomain.ErrResourceFetchCredentialsMissing):
		return "credentials_missing", nil
	default:
		return "", err
	}
}

func adminTokenMaintenanceResult(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, mailapp.ErrMicrosoftTokenRefreshNotFound):
		return "not_found", nil
	case errors.Is(err, mailapp.ErrMicrosoftTokenRefreshConflict),
		errors.Is(err, mailapp.ErrMicrosoftTokenRefreshStale):
		return "invalid_state", nil
	case errors.Is(err, mailapp.ErrMicrosoftTokenCredentialsMissing):
		return "credentials_missing", nil
	default:
		return "", err
	}
}

var _ coreapp.AdminResourceMaintenancePort = adminMicrosoftMaintenanceAdapter{}
