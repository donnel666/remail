package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const typeAnnouncementBroadcast = "system:announcement_broadcast"

type announcementBroadcastTask struct {
	ID        int64  `json:"id"`
	StartTime string `json:"startTime"`
}

type announcementRecipientSource interface {
	ListByFilter(ctx context.Context, filter iamdomain.UserListFilter, offset, limit int) ([]iamdomain.User, error)
}

type announcementMailer struct {
	users    announcementRecipientSource
	delivery mailapp.DeliveryPort
	client   *asynq.Client
}

func (m announcementMailer) PublishAnnouncements(ctx context.Context, announcements []runtimeconfig.Announcement) error {
	if m.client == nil {
		return fmt.Errorf("announcement queue is unavailable")
	}
	now := time.Now()
	var enqueueErrors []error
	for _, announcement := range announcements {
		start, startErr := time.Parse(time.RFC3339, announcement.StartTime)
		future := startErr == nil && start.After(now)
		if !future && !runtimeconfig.Bool("announcement_enabled", true) {
			continue
		}
		payload, err := json.Marshal(announcementBroadcastTask{ID: announcement.ID, StartTime: announcement.StartTime})
		if err != nil {
			enqueueErrors = append(enqueueErrors, err)
			continue
		}
		options := []asynq.Option{
			asynq.Queue(platform.QueueMailtransport),
			asynq.MaxRetry(0),
			asynq.Timeout(30 * time.Second),
		}
		uniqueFor := 10 * time.Minute
		if future {
			options = append(options, asynq.ProcessAt(start))
			uniqueFor += start.Sub(now)
		}
		options = append(options, asynq.Unique(uniqueFor))
		if _, err := m.client.EnqueueContext(ctx, asynq.NewTask(typeAnnouncementBroadcast, payload), options...); err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
			enqueueErrors = append(enqueueErrors, err)
		}
	}
	return errors.Join(enqueueErrors...)
}

func registerSystemEmailTaskHandlers(mux *asynq.ServeMux, mailer announcementMailer) {
	mux.HandleFunc(typeAnnouncementBroadcast, func(ctx context.Context, task *asynq.Task) error {
		var payload announcementBroadcastTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode announcement broadcast task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ID <= 0 {
			return fmt.Errorf("decode announcement broadcast task: %w", asynq.SkipRetry)
		}
		return mailer.processAnnouncementBroadcast(ctx, payload, time.Now())
	})
}

func (m announcementMailer) processAnnouncementBroadcast(ctx context.Context, task announcementBroadcastTask, now time.Time) error {
	var current *runtimeconfig.Announcement
	for _, announcement := range runtimeconfig.ActiveAnnouncements(now, 100) {
		if announcement.ID == task.ID && strings.TrimSpace(announcement.StartTime) == strings.TrimSpace(task.StartTime) {
			matched := announcement
			current = &matched
			break
		}
	}
	if current == nil {
		return nil
	}
	return m.sendAnnouncements(ctx, []runtimeconfig.Announcement{*current})
}

func (m announcementMailer) sendAnnouncements(ctx context.Context, announcements []runtimeconfig.Announcement) error {
	if m.users == nil || m.delivery == nil {
		return nil
	}
	enabled := true
	var sendErrors []error
	for offset := 0; ; offset += 200 {
		users, err := m.users.ListByFilter(ctx, iamdomain.UserListFilter{Enabled: &enabled}, offset, 200)
		if err != nil {
			return errors.Join(append(sendErrors, err)...)
		}
		for _, user := range users {
			for _, announcement := range announcements {
				if err := m.delivery.Send(ctx, mailapp.AnnouncementMessage(user.Email, announcement.ID, announcement.Title, announcement.Content)); err != nil {
					sendErrors = append(sendErrors, err)
				}
			}
		}
		if len(users) < 200 {
			break
		}
	}
	return errors.Join(sendErrors...)
}
