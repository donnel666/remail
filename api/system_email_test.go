package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type announcementRecipientStub struct {
	users   []iamdomain.User
	err     error
	filters []iamdomain.UserListFilter
}

func (s *announcementRecipientStub) ListByFilter(_ context.Context, filter iamdomain.UserListFilter, offset, _ int) ([]iamdomain.User, error) {
	s.filters = append(s.filters, filter)
	if s.err != nil {
		return nil, s.err
	}
	if offset > 0 {
		return nil, nil
	}
	return s.users, nil
}

type announcementDeliveryStub struct {
	messages []maildomain.OutboundMessage
	failures int
	err      error
}

func (s *announcementDeliveryStub) Send(_ context.Context, message maildomain.OutboundMessage) error {
	s.messages = append(s.messages, message)
	if s.failures > 0 {
		s.failures--
		return s.err
	}
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
		users:    &announcementRecipientStub{users: []iamdomain.User{{ID: 7, Email: "user@example.com", Status: iamdomain.UserStatusActive}}},
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

func TestProjectApplicationNotificationEmailsActiveSuperAdmins(t *testing.T) {
	applicantID := uint(7)
	users := &announcementRecipientStub{users: []iamdomain.User{
		{ID: 1, Email: "root@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive},
		{ID: 2, Email: "disabled@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusDisabled},
		{ID: 3, Email: "admin@example.com", Role: iamdomain.RoleAdmin, Status: iamdomain.UserStatusActive},
	}}
	delivery := &announcementDeliveryStub{}
	mailer := announcementMailer{users: users, delivery: delivery}

	err := mailer.notifyProjectApplication(context.Background(), coredomain.Project{
		ID:              42,
		ApplicantUserID: &applicantID,
		Name:            "GitHub",
		TargetPlatform:  "github.com",
	}, "request-1")

	require.NoError(t, err)
	require.Len(t, users.filters, 1)
	require.Equal(t, iamdomain.RoleSuperAdmin, *users.filters[0].Role)
	require.True(t, *users.filters[0].Enabled)
	require.Len(t, delivery.messages, 1)
	require.Equal(t, "root@example.com", delivery.messages[0].To)
	require.Contains(t, delivery.messages[0].Subject, "项目申请")
	require.Contains(t, delivery.messages[0].TextBody, "项目 ID：42")
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

func TestSystemLoadAlerterRequiresContinuousOverloadAndResetsImmediately(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	users := &announcementRecipientStub{users: []iamdomain.User{
		{ID: 1, Email: "root-1@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive},
		{ID: 2, Email: "root-2@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive},
	}}
	delivery := &announcementDeliveryStub{}
	alerter := systemLoadAlerter{users: users, delivery: delivery, redis: redisClient, hostname: "load-host"}
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	observe := func(offset time.Duration, cpu float64, valid bool) {
		require.NoError(t, alerter.observe(context.Background(), platform.BackgroundLoadSnapshot{
			CPUPercent:    cpu,
			MemoryPercent: 61.5,
			CPUValid:      valid,
			MemoryValid:   true,
			SampledAt:     base.Add(offset),
		}))
	}

	observe(0, 100, false)
	observe(time.Second, 80, true)
	observe(10*time.Minute, 80, true)
	require.Empty(t, delivery.messages)

	observe(10*time.Minute+time.Second, 79, true)
	observe(10*time.Minute+2*time.Second, 80, true)
	observe(20*time.Minute+time.Second, 80, true)
	require.Empty(t, delivery.messages)

	observe(20*time.Minute+2*time.Second, 80, true)
	require.Len(t, delivery.messages, 2)
	require.Contains(t, delivery.messages[0].Subject, fmt.Sprintf("（%d%%）", systemLoadAlertThresholds[0]))
	require.ElementsMatch(t, []string{"root-1@example.com", "root-2@example.com"}, []string{delivery.messages[0].To, delivery.messages[1].To})
	require.Len(t, users.filters, 1)
	require.Equal(t, iamdomain.RoleSuperAdmin, *users.filters[0].Role)
	require.True(t, *users.filters[0].Enabled)

	observe(20*time.Minute+3*time.Second, 100, true)
	require.Len(t, delivery.messages, 2)

	observe(20*time.Minute+4*time.Second, 100, false)
	observe(20*time.Minute+5*time.Second, 100, true)
	observe(30*time.Minute+4*time.Second, 100, true)
	require.Len(t, delivery.messages, 2)

	observe(30*time.Minute+5*time.Second, 100, true)
	require.Len(t, delivery.messages, 4)
	require.Contains(t, delivery.messages[2].Subject, fmt.Sprintf("（%d%%）", systemLoadAlertThresholds[len(systemLoadAlertThresholds)-1]))
	require.Len(t, users.filters, 2)
	require.Equal(t, systemLoadAlertStateTTL, server.TTL(systemLoadAlertStateKey("load-host")))
}

func TestSystemLoadAlerterRenewsActiveEpisodeTTL(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	delivery := &announcementDeliveryStub{}
	alerter := systemLoadAlerter{
		users:    &announcementRecipientStub{users: []iamdomain.User{{ID: 1, Email: "root@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive}}},
		delivery: delivery,
		redis:    redisClient,
		hostname: "ttl-host",
	}
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	observe := func(offset time.Duration) {
		require.NoError(t, alerter.observe(context.Background(), platform.BackgroundLoadSnapshot{CPUPercent: 80, CPUValid: true, SampledAt: base.Add(offset)}))
	}

	observe(0)
	observe(systemLoadAlertConfirmWindow)
	require.Len(t, delivery.messages, 1)

	server.FastForward(59 * time.Minute)
	observe(systemLoadAlertConfirmWindow + 59*time.Minute)
	require.Len(t, delivery.messages, 1)
	require.Equal(t, systemLoadAlertStateTTL, server.TTL(systemLoadAlertStateKey("ttl-host")))

	server.FastForward(59 * time.Minute)
	observe(systemLoadAlertConfirmWindow + 118*time.Minute)
	require.Len(t, delivery.messages, 1)
	require.Equal(t, systemLoadAlertStateTTL, server.TTL(systemLoadAlertStateKey("ttl-host")))
}

func TestSystemLoadAlerterRemembersInterruptionDuringRedisBackoff(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	delivery := &announcementDeliveryStub{}
	alerter := systemLoadAlerter{
		users:    &announcementRecipientStub{users: []iamdomain.User{{ID: 1, Email: "root@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive}}},
		delivery: delivery,
		redis:    redisClient,
		hostname: "backoff-host",
	}
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	observe := func(offset time.Duration, cpu float64) error {
		return alerter.observe(context.Background(), platform.BackgroundLoadSnapshot{CPUPercent: cpu, CPUValid: true, SampledAt: base.Add(offset)})
	}

	require.NoError(t, observe(0, 80))
	server.SetError("LOADING Redis is loading the dataset in memory")
	require.Error(t, observe(5*time.Minute, 80))
	server.SetError("")
	require.NoError(t, observe(5*time.Minute+10*time.Second, 79))

	require.NoError(t, observe(10*time.Minute, 80))
	require.Empty(t, delivery.messages)
	require.NoError(t, observe(20*time.Minute, 80))
	require.Len(t, delivery.messages, 1)
}

func TestSystemLoadAlerterRetriesTheSameEventAfterBackoffAndAcrossRestart(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	users := &announcementRecipientStub{users: []iamdomain.User{{ID: 1, Email: "root@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive}}}
	firstDelivery := &announcementDeliveryStub{failures: 1, err: errors.New("mail queue unavailable")}
	first := systemLoadAlerter{users: users, delivery: firstDelivery, redis: redisClient, hostname: "retry-host"}
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	snapshot := func(offset time.Duration) platform.BackgroundLoadSnapshot {
		return platform.BackgroundLoadSnapshot{CPUPercent: 80, CPUValid: true, SampledAt: base.Add(offset)}
	}

	require.NoError(t, first.observe(context.Background(), snapshot(0)))
	require.Empty(t, firstDelivery.messages)
	require.Error(t, first.observe(context.Background(), snapshot(systemLoadAlertConfirmWindow)))
	require.Len(t, firstDelivery.messages, 1)
	require.NoError(t, first.observe(context.Background(), snapshot(systemLoadAlertConfirmWindow+30*time.Second)))
	require.Len(t, firstDelivery.messages, 1)

	secondDelivery := &announcementDeliveryStub{}
	second := systemLoadAlerter{users: users, delivery: secondDelivery, redis: redisClient, hostname: "retry-host"}
	require.NoError(t, second.observe(context.Background(), snapshot(systemLoadAlertConfirmWindow+30*time.Second)))
	require.Empty(t, secondDelivery.messages)
	require.NoError(t, second.observe(context.Background(), snapshot(systemLoadAlertConfirmWindow+time.Minute)))
	require.Len(t, secondDelivery.messages, 1)
	require.Equal(t, firstDelivery.messages[0], secondDelivery.messages[0])

	thirdDelivery := &announcementDeliveryStub{}
	third := systemLoadAlerter{users: users, delivery: thirdDelivery, redis: redisClient, hostname: "retry-host"}
	require.NoError(t, third.observe(context.Background(), snapshot(systemLoadAlertConfirmWindow+time.Minute+time.Second)))
	require.Empty(t, thirdDelivery.messages)
	require.Equal(t, systemLoadAlertStateTTL, server.TTL(systemLoadAlertStateKey("retry-host")))

	server.FastForward(systemLoadAlertStateTTL)
	fourthDelivery := &announcementDeliveryStub{}
	fourth := systemLoadAlerter{users: users, delivery: fourthDelivery, redis: redisClient, hostname: "retry-host"}
	require.NoError(t, fourth.observe(context.Background(), snapshot(2*time.Hour)))
	require.Empty(t, fourthDelivery.messages)
	require.NoError(t, fourth.observe(context.Background(), snapshot(2*time.Hour+systemLoadAlertConfirmWindow)))
	require.Len(t, fourthDelivery.messages, 1)
}

func TestSystemLoadAlerterBacksOffSuperAdminQueries(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	users := &announcementRecipientStub{err: errors.New("database unavailable")}
	delivery := &announcementDeliveryStub{}
	alerter := systemLoadAlerter{users: users, delivery: delivery, redis: redisClient, hostname: "query-host"}
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	observe := func(offset time.Duration) error {
		return alerter.observe(context.Background(), platform.BackgroundLoadSnapshot{CPUPercent: 80, CPUValid: true, SampledAt: base.Add(offset)})
	}

	require.NoError(t, observe(0))
	require.Empty(t, users.filters)
	require.Error(t, observe(systemLoadAlertConfirmWindow))
	require.NoError(t, observe(systemLoadAlertConfirmWindow+2*time.Second))
	require.NoError(t, observe(systemLoadAlertConfirmWindow+59*time.Second))
	require.Len(t, users.filters, 1)
	users.err = nil
	users.users = []iamdomain.User{{ID: 1, Email: "root@example.com", Role: iamdomain.RoleSuperAdmin, Status: iamdomain.UserStatusActive}}
	require.NoError(t, observe(systemLoadAlertConfirmWindow+time.Minute))
	require.Len(t, users.filters, 2)
	require.Len(t, delivery.messages, 1)
}
