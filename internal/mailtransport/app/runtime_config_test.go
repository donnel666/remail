package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAdminViewsUseRuntimeLimits(t *testing.T) {
	runtimeconfig.Set("microsoft_alias_weekly_limit", "4")
	runtimeconfig.Set("microsoft_alias_yearly_limit", "20")
	runtimeconfig.Set("token_refresh_max_attempts", "5")
	t.Cleanup(func() {
		runtimeconfig.Delete("microsoft_alias_weekly_limit")
		runtimeconfig.Delete("microsoft_alias_yearly_limit")
		runtimeconfig.Delete("token_refresh_max_attempts")
	})

	service := NewMicrosoftAliasService(&fakeMicrosoftAliasStore{}, nil, nil)
	schedule, err := service.GetAdminSchedule(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 4, schedule.WeekLimit)
	require.Equal(t, 20, schedule.YearLimit)
	require.Equal(t, 5, microsoftTokenRefreshTaskView(MicrosoftTokenRefreshState{ResourceID: 42}).MaxAttempts)
}
