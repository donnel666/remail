package runtimeconfig

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/stretchr/testify/require"
)

func TestAnnouncementValidationAndActiveFeed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	announcements := []Announcement{
		{ID: 1, Title: "Immediate", Content: "Visible", Type: "default", Enabled: true},
		{ID: 2, Title: "Disabled", Content: "Hidden", Type: "warning", Enabled: false},
		{ID: 3, Title: "Future", Content: "Hidden", Type: "ongoing", StartTime: now.Add(time.Hour).Format(time.RFC3339), Enabled: true},
		{ID: 4, Title: "Expired", Content: "Hidden", Type: "error", EndTime: now.Format(time.RFC3339), Enabled: true},
		{ID: 5, Title: "Scheduled", Content: "Visible", Type: "success", StartTime: now.Add(-time.Hour).Format(time.RFC3339), EndTime: now.Add(time.Hour).Format(time.RFC3339), Enabled: true},
		{ID: 100, Title: "Older", Content: "Visible", Type: "default", StartTime: now.Add(-2 * time.Hour).Format(time.RFC3339), Enabled: true},
	}
	payload, err := json.Marshal(announcements)
	require.NoError(t, err)
	require.NoError(t, Validate("announcements", string(payload)))

	Replace([]domain.Setting{{Key: "announcement_enabled", Value: "true"}, {Key: "announcements", Value: string(payload)}})
	t.Cleanup(func() { Replace(nil) })
	active := ActiveAnnouncements(now, 20)
	require.Equal(t, []int64{5, 100, 1}, []int64{active[0].ID, active[1].ID, active[2].ID})

	Set("announcement_enabled", "false")
	require.Empty(t, ActiveAnnouncements(now, 20))

	for _, invalid := range []string{
		`{"id":1}`,
		`[{"id":1,"title":"A","content":"B","type":"unknown","startTime":"","endTime":"","enabled":true}]`,
		`[{"id":1,"title":"A","content":"B","type":"default","startTime":"2026-07-26T00:00:00Z","endTime":"2026-07-25T00:00:00Z","enabled":true}]`,
		`[{"id":1,"title":"A","content":"B","type":"default","startTime":"","endTime":"","enabled":true},{"id":1,"title":"C","content":"D","type":"default","startTime":"","endTime":"","enabled":true}]`,
		`[{"id":9007199254740992,"title":"A","content":"B","type":"default","startTime":"","endTime":"","enabled":true}]`,
	} {
		require.ErrorIs(t, Validate("announcements", invalid), domain.ErrInvalidValue)
	}

	boundaryContent := strings.Repeat("界", maxAnnouncementContentBytes/3) + "x"
	boundary, err := json.Marshal([]Announcement{{ID: 1, Title: "A", Content: boundaryContent, Type: "default", Enabled: true}})
	require.NoError(t, err)
	require.NoError(t, Validate("announcements", string(boundary)))
	overLimit, err := json.Marshal([]Announcement{{ID: 1, Title: "A", Content: boundaryContent + "x", Type: "default", Enabled: true}})
	require.NoError(t, err)
	require.ErrorIs(t, Validate("announcements", string(overLimit)), domain.ErrInvalidValue)
}
