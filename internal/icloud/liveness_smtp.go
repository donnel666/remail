package icloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"net/textproto"
	"slices"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
)

const (
	iCloudMaintenanceLivenessSMTP  = "liveness_smtp"
	iCloudLivenessReceiptWait      = 10 * time.Minute
	iCloudLivenessReceiptPoll      = 15 * time.Second
	iCloudLivenessSMTPSendInterval = time.Minute
)

var iCloudLivenessKinds = []string{iCloudMaintenanceLiveness, iCloudMaintenanceLivenessSMTP}

func configuredICloudLivenessKind() string {
	if runtimeconfig.Bool(runtimeconfig.ICloudLivenessSMTPEnabledKey, false) {
		return iCloudMaintenanceLivenessSMTP
	}
	return iCloudMaintenanceLiveness
}

func (s *Service) livenessSMTPServers() ([]runtimeconfig.ICloudLivenessSMTPServer, error) {
	if s.livenessInbox == nil || s.files == nil || s.deviceRedis == nil {
		return nil, ErrICloudLivenessUnavailable
	}
	values := runtimeconfig.Snapshot()
	if strings.TrimSpace(values.String(runtimeconfig.ICloudLivenessSMTPEnabledKey, "false")) != "true" {
		return nil, ErrICloudLivenessUnavailable
	}
	servers, err := runtimeconfig.ParseICloudLivenessSMTPServers(values.String(runtimeconfig.ICloudLivenessSMTPServersKey, "[]"))
	if err != nil {
		return nil, ErrICloudLivenessUnavailable
	}
	active := servers[:0]
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		if strings.TrimSpace(server.Password) == "" {
			return nil, ErrICloudLivenessUnavailable
		}
		active = append(active, server)
	}
	if len(active) == 0 {
		return nil, ErrICloudLivenessUnavailable
	}
	return active, nil
}

func (s *Service) checkICloudLivenessTransport(ctx context.Context, kind string) error {
	if kind != iCloudMaintenanceLivenessSMTP {
		return s.checkDirectICloudLivenessMail(ctx)
	}
	_, err := s.livenessSMTPServers()
	return err
}

const iCloudLivenessSMTPIntervalRedisKey = "icloud:liveness:smtp:send_interval"

var finishICloudLivenessSMTPSendScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0`)

func (s *Service) reserveICloudLivenessSMTPSend(ctx context.Context, interval time.Duration) (string, time.Duration, error) {
	token := rand.Text()
	allowed, err := s.deviceRedis.SetNX(ctx, iCloudLivenessSMTPIntervalRedisKey, token, interval+iCloudLivenessWorkerTimeout).Result()
	if err != nil {
		return "", interval, err
	}
	if allowed {
		return token, 0, nil
	}
	wait, err := s.deviceRedis.PTTL(ctx, iCloudLivenessSMTPIntervalRedisKey).Result()
	return "", max(wait, time.Second), err
}

func (s *Service) finishICloudLivenessSMTPSend(ctx context.Context, token string, interval time.Duration) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	// Failed renewal retains the longer lease, never a shorter sending interval.
	_ = finishICloudLivenessSMTPSendScript.Run(finishCtx, s.deviceRedis, []string{iCloudLivenessSMTPIntervalRedisKey}, token, interval.Milliseconds()).Err()
}

func (s *Service) deferICloudLivenessSMTPSend(ctx context.Context, run iCloudMaintenanceRunModel, delay time.Duration) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	now := s.now().UTC()
	return s.db.WithContext(finishCtx).Model(&iCloudMaintenanceRunModel{}).
		Where("id=? AND status=? AND attempts=? AND started_at=? AND probe_sent_at IS NULL", run.ID, iCloudMaintenanceRunning, run.Attempts, run.StartedAt).
		Updates(map[string]any{"status": iCloudMaintenanceQueued, "attempts": run.Attempts - 1, "started_at": nil,
			"probe_sender": run.ProbeSender, "next_check_at": now.Add(delay), "updated_at": now,
			"last_safe_error": "Waiting for the SMTP send interval."}).Error
}

func livenessProbeKey(run iCloudMaintenanceRunModel) string {
	return fmt.Sprintf("icloud:liveness:probe:%d:%d:%d:%d", run.ID, run.ValidationGeneration, run.Attempts, run.StartedAt.UnixMilli())
}

func iCloudLivenessSubject(token string) string { return "ReMail iCloud liveness " + token }

func (s *Service) sendICloudLivenessProbe(ctx context.Context, run *iCloudMaintenanceRunModel) (retryAfter time.Duration, sendErr error) {
	message := maildomain.OutboundMessage{Purpose: maildomain.PurposeSystemNotice, To: run.ProbeRecipient, Subject: "ReMail iCloud 邮箱测活", TextBody: "这是一封 iCloud 别名邮箱测活邮件，无需回复。"}
	send := func(sender mailapp.SenderPort, from string) error {
		result := s.db.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
			Where("id=? AND status=? AND attempts=?", run.ID, iCloudMaintenanceRunning, run.Attempts).
			Updates(map[string]any{"probe_sender": from, "updated_at": s.now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var current int64
			if err := s.db.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
				Where("id=? AND status=? AND attempts=? AND probe_sender=?", run.ID, iCloudMaintenanceRunning, run.Attempts, from).
				Count(&current).Error; err != nil {
				return err
			}
			if current != 1 {
				return errICloudValidationStale
			}
		}
		run.ProbeSender, message.From = from, from
		err := sender.Send(ctx, message)
		var unaccepted *mailapp.OutboundNotAcceptedError
		var failure *mailapp.OutboundSendFailure
		if err != nil && !errors.As(err, &unaccepted) && (run.Kind == iCloudMaintenanceLivenessSMTP || !errors.As(err, &failure)) {
			return fmt.Errorf("%w: %w", errICloudLivenessDeliveryUnconfirmed, err)
		}
		return err
	}
	if run.Kind != iCloudMaintenanceLivenessSMTP {
		from := s.livenessProbeSender()
		// Direct probes already persist a fresh random sender for each alias attempt.
		message.Subject = iCloudLivenessSubject(from)
		return 0, send(s.livenessSender, from)
	}
	servers, err := s.livenessSMTPServers()
	if err != nil {
		return 0, err
	}
	interval := runtimeconfig.Duration(runtimeconfig.ICloudLivenessSMTPIntervalKey, iCloudLivenessSMTPSendInterval, time.Second, 1)
	lease, wait, err := s.reserveICloudLivenessSMTPSend(ctx, interval)
	if err != nil || wait > 0 {
		return wait, err
	}
	defer s.finishICloudLivenessSMTPSend(ctx, lease, interval)
	planKey := fmt.Sprintf("icloud:liveness:smtp:remaining:%d:%d:%d", run.ID, run.ValidationGeneration, run.Attempts)
	var remaining []string
	plan, err := s.deviceRedis.Get(ctx, planKey).Result()
	if errors.Is(err, redis.Nil) && run.ProbeSender == "" {
		turn, err := s.deviceRedis.Incr(ctx, "icloud:liveness:smtp:rotation").Result()
		if err != nil || turn <= 0 {
			return 0, ErrICloudLivenessUnavailable
		}
		for offset := range servers {
			remaining = append(remaining, servers[(int((turn-1)%int64(len(servers)))+offset)%len(servers)].ID)
		}
	} else if err != nil || json.Unmarshal([]byte(plan), &remaining) != nil {
		return 0, ErrICloudLivenessUnavailable
	}
	defer func() {
		if retryAfter == 0 {
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
			defer cancel()
			_ = s.deviceRedis.Del(finishCtx, planKey).Err()
		}
	}()
	remaining = slices.DeleteFunc(remaining, func(id string) bool {
		return !slices.ContainsFunc(servers, func(server runtimeconfig.ICloudLivenessSMTPServer) bool { return server.ID == id })
	})
	if len(remaining) == 0 {
		return 0, ErrICloudLivenessUnavailable
	}
	token := rand.Text()
	if err = s.deviceRedis.Set(ctx, livenessProbeKey(*run), token, 15*time.Minute).Err(); err != nil {
		return 0, ErrICloudLivenessUnavailable
	}
	message.Subject = iCloudLivenessSubject(token)
	server := servers[slices.IndexFunc(servers, func(server runtimeconfig.ICloudLivenessSMTPServer) bool { return server.ID == remaining[0] })]
	cfg := mailinfra.SMTPConfig{Addr: server.Addr, Username: server.Username, Password: server.Password, From: server.From}
	var sender mailapp.SenderPort = mailinfra.NewSMTPDelivery(cfg)
	if s.newLivenessSMTP != nil {
		sender = s.newLivenessSMTP(cfg)
	}
	err = send(sender, server.From)
	if err == nil {
		return 0, nil
	}
	var failure *mailapp.OutboundSendFailure
	if errors.As(err, &failure) && (failure.RecipientRejected || failure.RecipientTemporary) {
		return 0, err
	}
	var unaccepted *mailapp.OutboundNotAcceptedError
	if !errors.As(err, &unaccepted) || ctx.Err() != nil || len(remaining) == 1 {
		return 0, err
	}
	encoded, err := json.Marshal(remaining[1:])
	if err != nil {
		return 0, err
	}
	// shortcut: cursors expire after seven days; extend this if failover queues can wait longer.
	if err := s.deviceRedis.Set(ctx, planKey, encoded, 7*24*time.Hour).Err(); err != nil {
		return 0, err
	}
	return interval, nil
}

func (s *Service) iCloudLivenessReceived(ctx context.Context, run iCloudMaintenanceRunModel) bool {
	if run.StartedAt == nil || run.ProbeSender == "" {
		return false
	}
	token := run.ProbeSender
	until := run.StartedAt.Add(iCloudLivenessLateBounceWindow)
	if run.Kind == iCloudMaintenanceLivenessSMTP {
		if s.deviceRedis == nil {
			return false
		}
		var err error
		token, err = s.deviceRedis.Get(ctx, livenessProbeKey(run)).Result()
		if err != nil || token == "" {
			return false
		}
	}
	if run.ProbeSentAt == nil || run.Kind == iCloudMaintenanceLivenessSMTP {
		until = run.StartedAt.Add(iCloudLivenessReceiptWait)
	}
	subject := iCloudLivenessSubject(token)
	result, err := s.FetchMail(ctx, MailFetchRequest{
		ResourceID: run.ResourceID, Recipient: run.ProbeRecipient, SinceAt: *run.StartedAt,
		UntilAt: until, MaxMessages: 100, Subject: subject,
	})
	if err != nil || result == nil {
		return false
	}
	for _, message := range result.Messages {
		if !strings.EqualFold(message.Recipient, run.ProbeRecipient) || message.ReceivedAt.Before(*run.StartedAt) {
			continue
		}
		parsed, err := mail.ReadMessage(bytes.NewReader(message.Raw))
		if err != nil || !iCloudLivenessReceiptSenderMatches(parsed.Header, message.Sender, run) {
			continue
		}
		decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
		if err == nil && decoded == subject {
			return true
		}
	}
	return false
}

func (s *Service) waitForICloudLivenessReceipt(ctx context.Context, run iCloudMaintenanceRunModel) error {
	if run.StartedAt == nil || run.ProbeSender == "" {
		return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "SMTP liveness sending worker expired; account health is unchanged.", false)
	}
	now := s.now().UTC()
	deadline := run.StartedAt.Add(iCloudLivenessReceiptWait)
	if !now.Before(deadline) {
		return s.finishICloudLivenessCheck(ctx, run, "", iCloudMaintenanceFailed, "SMTP liveness email delivery was not confirmed within 10 minutes; account health is unchanged.", false)
	}
	next := now.Add(iCloudLivenessReceiptPoll)
	if next.After(deadline) {
		next = deadline
	}
	updates := map[string]any{"next_check_at": next, "updated_at": now}
	if run.ProbeSentAt == nil {
		updates["last_safe_error"] = "SMTP sending result is unconfirmed; waiting for the forwarded liveness email."
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	return s.db.WithContext(finishCtx).Model(&iCloudMaintenanceRunModel{}).
		Where("id=? AND status=? AND attempts=? AND started_at=? AND next_check_at=?", run.ID, iCloudMaintenanceRunning, run.Attempts, run.StartedAt, run.NextCheckAt).
		Updates(updates).Error
}

func iCloudLivenessReceiptSenderMatches(header mail.Header, fallback string, run iCloudMaintenanceRunModel) bool {
	values := textproto.MIMEHeader(header).Values("X-ICLOUD-HME")
	if len(values) == 0 {
		return strings.EqualFold(fallback, run.ProbeSender)
	}
	if len(values) != 1 {
		return false
	}
	// Apple's masked sender loses the distinction between dots and underscores.
	params := make(map[string]string)
	for _, part := range strings.Split(values[0], ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if _, exists := params[key]; !ok || key == "" || exists {
			return false
		}
		params[key] = strings.TrimSpace(value)
	}
	return strings.EqualFold(params["p"], run.ProbeRecipient) && strings.EqualFold(params["s"], run.ProbeSender)
}
