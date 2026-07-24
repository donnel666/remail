package app

import (
	"context"
	"errors"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

var (
	ErrMicrosoftAliasAdminUnavailable = errors.New("microsoft alias administration is temporarily unavailable")
	ErrMicrosoftAliasResourceNotFound = errors.New("microsoft alias resource not found")
	ErrMicrosoftAliasResourceConflict = errors.New("microsoft alias resource state conflict")
	ErrMicrosoftAliasScheduleNotFound = errors.New("microsoft alias schedule not found")
	ErrMicrosoftAliasSchedulePaused   = errors.New("microsoft alias schedule is paused")
)

type MicrosoftAliasAdminSchedule struct {
	WeekCreated    int
	WeekLimit      int
	YearCreated    int
	YearLimit      int
	NextRunAt      *time.Time
	ScheduleStatus string
	UpdatedAt      time.Time
}

type MicrosoftAliasExpediteResult struct {
	TaskID         string
	ResourceID     uint
	Status         string
	Reused         bool
	NextRunAt      *time.Time
	QueuedAt       time.Time
	StartedAt      *time.Time
	UpdatedAt      time.Time
	WakeDispatcher bool
}

type MicrosoftAliasAdminScheduleStore interface {
	GetAdminSchedule(ctx context.Context, resourceID uint, yearStart, yearEnd, weekStart, weekEnd time.Time) (*MicrosoftAliasAdminSchedule, error)
}

type MicrosoftAliasScheduleQueryPort interface {
	GetAdminSchedule(ctx context.Context, resourceID uint) (*MicrosoftAliasAdminSchedule, error)
}

func (s *MicrosoftAliasService) GetAdminSchedule(ctx context.Context, resourceID uint) (*MicrosoftAliasAdminSchedule, error) {
	if s == nil || resourceID == 0 {
		return nil, ErrMicrosoftAliasResourceNotFound
	}
	store, ok := s.store.(MicrosoftAliasAdminScheduleStore)
	if !ok || store == nil {
		return nil, ErrMicrosoftAliasAdminUnavailable
	}
	now := s.now().UTC()
	yearStart, yearEnd, weekStart, weekEnd := microsoftAliasQuotaWindows(now)
	schedule, err := store.GetAdminSchedule(ctx, resourceID, yearStart, yearEnd, weekStart, weekEnd)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return &MicrosoftAliasAdminSchedule{
			WeekLimit: runtimeconfig.Int("microsoft_alias_weekly_limit", MicrosoftAliasWeeklyLimit, 1),
			YearLimit: runtimeconfig.Int("microsoft_alias_yearly_limit", MicrosoftAliasYearlyLimit, 1),
		}, nil
	}
	schedule.WeekLimit = runtimeconfig.Int("microsoft_alias_weekly_limit", MicrosoftAliasWeeklyLimit, 1)
	schedule.YearLimit = runtimeconfig.Int("microsoft_alias_yearly_limit", MicrosoftAliasYearlyLimit, 1)
	return schedule, nil
}
