package api

import (
	"context"
	"errors"
	"fmt"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type MicrosoftAliasScheduleQueryAdapter struct {
	service *mailapp.MicrosoftAliasService
}

var _ coreapp.AliasScheduleQueryPort = (*MicrosoftAliasScheduleQueryAdapter)(nil)

func NewMicrosoftAliasScheduleQueryAdapter(service *mailapp.MicrosoftAliasService) *MicrosoftAliasScheduleQueryAdapter {
	return &MicrosoftAliasScheduleQueryAdapter{service: service}
}

func (a *MicrosoftAliasScheduleQueryAdapter) GetAdminAliasSchedule(ctx context.Context, resourceID uint) (*coreapp.AdminAliasScheduleSummary, error) {
	if a == nil || a.service == nil {
		return nil, mailapp.ErrMicrosoftAliasAdminUnavailable
	}
	schedule, err := a.service.GetAdminSchedule(ctx, resourceID)
	if err != nil {
		switch {
		case errors.Is(err, mailapp.ErrMicrosoftAliasResourceNotFound):
			return nil, coredomain.ErrResourceNotFound
		case errors.Is(err, mailapp.ErrMicrosoftAliasAdminUnavailable):
			return nil, fmt.Errorf("%w: alias schedule", coredomain.ErrResourceDependency)
		default:
			return nil, err
		}
	}
	return &coreapp.AdminAliasScheduleSummary{
		WeekCreated: schedule.WeekCreated,
		WeekLimit:   schedule.WeekLimit,
		YearCreated: schedule.YearCreated,
		YearLimit:   schedule.YearLimit,
		NextRunAt:   schedule.NextRunAt,
	}, nil
}
