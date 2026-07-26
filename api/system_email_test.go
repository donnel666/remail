package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type announcementRecipientStub struct {
	users []iamdomain.User
}

func (s announcementRecipientStub) ListByFilter(_ context.Context, _ iamdomain.UserListFilter, offset, _ int) ([]iamdomain.User, error) {
	if offset > 0 {
		return nil, nil
	}
	return s.users, nil
}

type announcementDeliveryStub struct {
	messages []maildomain.OutboundMessage
}

func (s *announcementDeliveryStub) Send(_ context.Context, message maildomain.OutboundMessage) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestAnnouncementBroadcastHonorsActiveWindowAndGlobalSwitch(t *testing.T) {
	oldAnnouncements := runtimeconfig.String("announcements", "[]")
	oldEnabled := runtimeconfig.String("announcement_enabled", "true")
	t.Cleanup(func() {
		runtimeconfig.Set("announcements", oldAnnouncements)
		runtimeconfig.Set("announcement_enabled", oldEnabled)
	})

	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	active := runtimeconfig.Announcement{ID: 1, Title: "Active", Content: "Now", Type: "default", StartTime: now.Add(-time.Hour).Format(time.RFC3339), Enabled: true}
	future := runtimeconfig.Announcement{ID: 2, Title: "Future", Content: "Later", Type: "default", StartTime: now.Add(time.Hour).Format(time.RFC3339), Enabled: true}
	payload, err := json.Marshal([]runtimeconfig.Announcement{active, future})
	require.NoError(t, err)
	runtimeconfig.Set("announcements", string(payload))
	runtimeconfig.Set("announcement_enabled", "true")

	delivery := &announcementDeliveryStub{}
	mailer := announcementMailer{
		users:    announcementRecipientStub{users: []iamdomain.User{{ID: 7, Email: "user@example.com", Status: iamdomain.UserStatusActive}}},
		delivery: delivery,
	}

	require.NoError(t, mailer.processAnnouncementBroadcast(context.Background(), announcementBroadcastTask{ID: future.ID, StartTime: future.StartTime}, now))
	require.Empty(t, delivery.messages)
	require.NoError(t, mailer.processAnnouncementBroadcast(context.Background(), announcementBroadcastTask{ID: active.ID, StartTime: active.StartTime}, now))
	require.Len(t, delivery.messages, 1)

	runtimeconfig.Set("announcement_enabled", "false")
	require.NoError(t, mailer.processAnnouncementBroadcast(context.Background(), announcementBroadcastTask{ID: future.ID, StartTime: future.StartTime}, now.Add(2*time.Hour)))
	require.Len(t, delivery.messages, 1)
}

func TestAnnouncementPublisherSchedulesFutureBroadcastAtStartTime(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })

	start := time.Now().Add(time.Hour).Truncate(time.Second)
	mailer := announcementMailer{client: client}
	require.NoError(t, mailer.PublishAnnouncements(context.Background(), []runtimeconfig.Announcement{{
		ID: 9, StartTime: start.Format(time.RFC3339), Enabled: true,
	}}))

	tasks, err := inspector.ListScheduledTasks(platform.QueueMailtransport)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, typeAnnouncementBroadcast, tasks[0].Type)
	require.WithinDuration(t, start, tasks[0].NextProcessAt, time.Second)
}
