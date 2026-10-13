package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailbox"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func enableLivenessSMTPTest(t *testing.T, s *Service, sender *iCloudLivenessMailStub) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	s.deviceRedis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, s.deviceRedis.Close()) })
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: runtimeconfig.ICloudLivenessSMTPEnabledKey, Value: "true"},
		{Key: runtimeconfig.ICloudLivenessSMTPServersKey, Value: `[{"id":"qq-1","name":"QQ 1","enabled":true,"addr":"smtp.qq.com:465","username":"probe@qq.com","password":"authorization-code","from":"probe@qq.com"}]`},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	s.newLivenessSMTP = func(cfg mailinfra.SMTPConfig) mailapp.SenderPort {
		require.Equal(t, "smtp.qq.com:465", cfg.Addr)
		require.Equal(t, "probe@qq.com", cfg.Username)
		require.Equal(t, "authorization-code", cfg.Password)
		require.Equal(t, "probe@qq.com", cfg.From)
		require.Nil(t, cfg.DKIM, "QQ must sign using its own domain")
		return sender
	}
	return server
}

func queueSMTPTestRun(t *testing.T, s *Service) iCloudMaintenanceRunModel {
	t.Helper()
	_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 1, 99, "smtp-probe", "req", "/liveness")
	require.NoError(t, err)
	var run iCloudMaintenanceRunModel
	require.NoError(t, s.db.Where("resource_id = ? AND kind = ?", 1, iCloudMaintenanceLivenessSMTP).Take(&run).Error)
	return run
}

func TestICloudLivenessSMTPRequiresMatchingDeliveredProbe(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	*now = now.Add(123456789 * time.Nanosecond)
	enableLivenessSMTPTest(t, s, sender)
	sender.rejectInbox = true // QQ does not deliver bounces into the local sender domain.
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	require.NoError(t, s.db.Model(&iCloudAliasModel{}).Where("resource_id = 1").Updates(map[string]any{"anonymous_id": "abc", "forward_to_email": "relay@example.com"}).Error)
	require.NoError(t, s.db.AutoMigrate(&iCloudDotAliasModel{}, &iCloudPlusAliasModel{}))
	run := queueSMTPTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Len(t, sender.messages, 1)
	require.Equal(t, "probe@qq.com", sender.messages[0].From)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	token, err := s.deviceRedis.Get(context.Background(), livenessProbeKey(run)).Result()
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, sender.messages[0].Subject, iCloudLivenessSubject(token), "the nonce survives across workers")
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status, "relay acceptance without receipt is inconclusive")

	for index, probe := range []struct{ sender, subject string }{
		{"probe@qq.com", fmt.Sprintf("ReMail iCloud liveness %d-%d-%d", run.ID, run.Attempts, run.StartedAt.UnixMilli())},
		{"other@qq.com", sender.messages[0].Subject},
		{"probe@qq.com", sender.messages[0].Subject},
	} {
		key := fmt.Sprintf("smtp-probe-%d", index)
		_, err := s.files.SavePrivate(context.Background(), governancedomain.PrivateFile{ObjectKey: key,
			ContentBytes: []byte("From: " + probe.sender + "\r\nTo: alias1@icloud.com\r\nSubject: " + probe.subject + "\r\n\r\nProbe")})
		require.NoError(t, err)
		require.NoError(t, s.db.Create(&mailinfra.InboundMailModel{
			EnvelopeFrom: "probe_at_qq_com_abc@icloud.com", HeaderFrom: probe.sender,
			Recipient: "relay@example.com", MailboxKey: mailbox.Normalize("relay@example.com"), ResourceType: "domain",
			SourceObjectKey: key, Subject: probe.subject, Status: string(maildomain.InboundStatusStored), CreatedAt: *now, UpdatedAt: *now,
		}).Error)
		if index == 2 {
			for n := 0; n < 120; n++ {
				require.NoError(t, s.db.Create(&mailinfra.InboundMailModel{
					EnvelopeFrom: "probe_at_qq_com_abc@icloud.com", HeaderFrom: "probe@qq.com", Recipient: "relay@example.com",
					MailboxKey: "relay@example.com", ResourceType: "domain", Subject: "other mail", SourceObjectKey: "irrelevant",
					Status: "stored", CreatedAt: now.Add(time.Second), UpdatedAt: *now,
				}).Error)
			}
		}
		*now = now.Add(iCloudLivenessReceiptPoll)
		require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
		require.NoError(t, s.db.Take(&resource, 1).Error)
		if index < 2 {
			require.Equal(t, iCloudResourceAbnormal, resource.Status)
		}
	}
	require.Equal(t, iCloudResourceNormal, resource.Status)
	var finished iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&finished, run.ID).Error)
	require.Equal(t, iCloudMaintenanceSucceeded, finished.Status)
	require.Nil(t, finished.NextCheckAt)
	require.Len(t, sender.messages, 1)
}

func TestICloudLivenessSMTPTimeoutAndConfigChangePreserveHealth(t *testing.T) {
	for _, status := range []string{iCloudResourceNormal, iCloudResourceAbnormal} {
		t.Run(status, func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			enableLivenessSMTPTest(t, s, sender)
			seedICloudLivenessResource(t, s, 1, status, true)
			run := queueSMTPTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPEnabledKey, "false")
			*now = now.Add(iCloudLivenessReceiptWait)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, status, resource.Status)
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceFailed, run.Status)
			require.Contains(t, run.LastSafeError, "not confirmed")
			require.Len(t, sender.messages, 1)
		})
	}
}

func TestICloudLivenessSMTPQueuedCheckNeverFallsBackToDirect(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	enableLivenessSMTPTest(t, s, sender)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueSMTPTestRun(t, s)
	result, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 2, 99, "duplicate", "req", "/liveness")
	require.NoError(t, err)
	require.False(t, result.Changed)
	candidates, err := s.iCloudValidationCandidates(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, candidates)
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPEnabledKey, "false")
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Empty(t, sender.messages)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
}

func TestICloudLivenessSMTPRotationAndSafeFailover(t *testing.T) {
	s, _, now := newICloudLivenessTestService(t)
	redisServer := enableLivenessSMTPTest(t, s, &iCloudLivenessMailStub{})
	servers := []runtimeconfig.ICloudLivenessSMTPServer{
		{ID: "a", Name: "A", Enabled: true, Addr: "smtp.qq.com:465", Username: "a@qq.com", Password: "a-secret", From: "a@qq.com"},
		{ID: "off", Name: "Disabled", Enabled: false, Addr: "smtp.qq.com:465", Username: "off@qq.com", Password: "off-secret", From: "off@qq.com"},
		{ID: "b", Name: "B", Enabled: true, Addr: "smtp.qq.com:465", Username: "b@qq.com", Password: "b-secret", From: "b@qq.com"},
	}
	raw, err := json.Marshal(servers)
	require.NoError(t, err)
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, string(raw))
	a, b := &iCloudLivenessMailStub{err: &mailapp.OutboundNotAcceptedError{Cause: errors.New("connection refused")}}, &iCloudLivenessMailStub{}
	s.newLivenessSMTP = func(cfg mailinfra.SMTPConfig) mailapp.SenderPort {
		switch cfg.From {
		case "a@qq.com":
			return a
		case "b@qq.com":
			return b
		default:
			t.Fatal("disabled service selected")
			return nil
		}
	}
	for id := uint(1); id <= 2; id++ {
		seedICloudLivenessResource(t, s, id, iCloudResourceAbnormal, true)
		_, err = s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, id, 1, 99, fmt.Sprint("pool-", id), "req", "/liveness")
		require.NoError(t, err)
		var run iCloudMaintenanceRunModel
		require.NoError(t, s.db.Where("resource_id=?", id).Take(&run).Error)
		worker := s
		if id == 2 {
			worker = NewService(s.db, nil, s.files, s.deviceRedis)
			worker.now, worker.livenessInbox, worker.newLivenessSMTP = s.now, s.livenessInbox, s.newLivenessSMTP
		}
		require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), run.ID))
		var stored iCloudMaintenanceRunModel
		require.NoError(t, s.db.Take(&stored, run.ID).Error)
		if id == 1 {
			require.Equal(t, iCloudMaintenanceQueued, stored.Status)
			require.Zero(t, stored.Attempts)
			require.Empty(t, b.messages, "SMTP failover must wait for its own send interval")
			redisServer.FastForward(iCloudLivenessSMTPSendInterval)
			*now = *stored.NextCheckAt
			worker = NewService(s.db, nil, s.files, s.deviceRedis)
			worker.now, worker.livenessInbox, worker.newLivenessSMTP = s.now, s.livenessInbox, s.newLivenessSMTP
			require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&stored, run.ID).Error)
		}
		require.Equal(t, "b@qq.com", stored.ProbeSender)
		require.NotNil(t, stored.ProbeSentAt)
		redisServer.FastForward(iCloudLivenessSMTPSendInterval)
		*now = now.Add(iCloudLivenessSMTPSendInterval)
	}
	require.Len(t, a.messages, 1, "the second task starts on B")
	require.Len(t, b.messages, 2)
	require.NotEqual(t, b.messages[0].Subject, b.messages[1].Subject)
}

func TestICloudLivenessSMTPPreservesOriginalForwardedSender(t *testing.T) {
	for _, from := range []string{"probe.user@example.com", "probe_user@example.com"} {
		t.Run(from, func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			enableLivenessSMTPTest(t, s, sender)
			runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, fmt.Sprintf(`[{"id":"one","name":"Example","enabled":true,"addr":"smtp.example.com:465","username":%q,"password":"test-only","from":%q}]`, from, from))
			s.newLivenessSMTP = func(mailinfra.SMTPConfig) mailapp.SenderPort { return sender }
			seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
			require.NoError(t, s.db.Model(&iCloudAliasModel{}).Where("resource_id=1").Updates(map[string]any{"anonymous_id": "abc", "forward_to_email": "relay@example.com"}).Error)
			require.NoError(t, s.db.AutoMigrate(&iCloudDotAliasModel{}, &iCloudPlusAliasModel{}))
			run := queueSMTPTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.Len(t, sender.messages, 1)
			subject := sender.messages[0].Subject
			raw := fmt.Sprintf("From: probe_user_at_example_com_abc@icloud.com\r\nTo: alias1@icloud.com\r\nX-ICLOUD-HME: p=alias1@icloud.com; f=relay@example.com; r=to; s=%s\r\nSubject: %s\r\n\r\nprobe", from, subject)
			_, err := s.files.SavePrivate(context.Background(), governancedomain.PrivateFile{ObjectKey: "forwarded-probe", ContentBytes: []byte(raw)})
			require.NoError(t, err)
			require.NoError(t, s.db.Create(&mailinfra.InboundMailModel{
				EnvelopeFrom: "probe_user_at_example_com_abc@icloud.com", HeaderFrom: "probe_user_at_example_com_abc@icloud.com",
				Recipient: "relay@example.com", MailboxKey: "relay@example.com", ResourceType: "domain", Subject: subject,
				SourceObjectKey: "forwarded-probe", Status: "stored", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
			}).Error)
			*now = now.Add(iCloudLivenessReceiptPoll)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceSucceeded, run.Status)
			require.Len(t, sender.messages, 1)
		})
	}
}

func TestICloudLivenessReceiptRejectsAmbiguousSenderMetadata(t *testing.T) {
	const valid = "X-ICLOUD-HME: p=alias1@icloud.com; s=probe_user@example.com\r\n"
	run := iCloudMaintenanceRunModel{ProbeRecipient: "alias1@icloud.com", ProbeSender: "probe_user@example.com"}
	for _, tc := range []struct {
		name, metadata, fallback string
		matches                  bool
	}{
		{"unmasked sender", "", run.ProbeSender, true},
		{"wrong unmasked sender", "", "other@example.com", false},
		{"original sender", valid, "probe.user@example.com", true},
		{"folded header", "X-ICLOUD-HME: p=alias1@icloud.com;\r\n\ts=probe_user@example.com;\r\n", "", true},
		{"different sender", strings.ReplaceAll(valid, "probe_user", "probe.user"), run.ProbeSender, false},
		{"different alias", strings.ReplaceAll(valid, "alias1", "alias2"), run.ProbeSender, false},
		{"missing alias", "X-ICLOUD-HME: s=probe_user@example.com\r\n", run.ProbeSender, false},
		{"missing sender", "X-ICLOUD-HME: p=alias1@icloud.com\r\n", run.ProbeSender, false},
		{"empty header", "X-ICLOUD-HME: \r\n", run.ProbeSender, false},
		{"duplicate header", valid + valid, run.ProbeSender, false},
		{"duplicate parameter", strings.TrimSuffix(valid, "\r\n") + "; S=other@example.com\r\n", run.ProbeSender, false},
		{"invalid parameter", strings.TrimSuffix(valid, "\r\n") + "; broken\r\n", run.ProbeSender, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := mail.ReadMessage(strings.NewReader("From: probe_user@example.com\r\n" + tc.metadata + "\r\nprobe"))
			require.NoError(t, err)
			require.Equal(t, tc.matches, iCloudLivenessReceiptSenderMatches(parsed.Header, tc.fallback, run))
		})
	}
}

func TestICloudLivenessSMTPTemporaryRecipientRetriesAnotherAlias(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprint("accepted=", accepted), func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			redisServer := enableLivenessSMTPTest(t, s, sender)
			seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
			require.NoError(t, s.db.Create(&iCloudAliasModel{ResourceID: 1, AnonymousID: "alias-2", Email: "another@icloud.com", Status: iCloudResourceNormal}).Error)
			run := queueSMTPTestRun(t, s)
			sender.err = &mailapp.OutboundNotAcceptedError{Cause: &mailapp.OutboundSendFailure{Retryable: true, RecipientTemporary: true}}
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceQueued, run.Status)
			require.Equal(t, "another@icloud.com", run.ProbeRecipient)
			require.Equal(t, 1, run.Attempts)
			require.Empty(t, run.ProbeSender)
			require.Nil(t, run.ProbeSentAt)
			require.Nil(t, run.StartedAt)
			*now = *run.NextCheckAt
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, 1, run.Attempts, "waiting for the send interval must not consume an alias attempt")
			require.Len(t, sender.messages, 1)
			redisServer.FastForward(iCloudLivenessSMTPSendInterval)
			*now = *run.NextCheckAt
			if accepted {
				sender.err = nil
			}
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, 2, run.Attempts)
			require.Len(t, sender.messages, 2)
			require.Equal(t, "another@icloud.com", sender.messages[1].To)
			require.NotEqual(t, sender.messages[0].Subject, sender.messages[1].Subject)
			if accepted {
				require.Equal(t, iCloudMaintenanceRunning, run.Status)
				require.NotNil(t, run.ProbeSentAt)
			} else {
				require.Equal(t, iCloudMaintenanceFailed, run.Status, "alias retries must stop at the submission budget")
			}
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, iCloudResourceAbnormal, resource.Status)
		})
	}
}

func TestICloudLivenessSMTPSeparatesServiceAndAliasRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		sends  int
		status string
	}{
		{"connection", &mailapp.OutboundNotAcceptedError{Cause: errors.New("dial failed")}, 2, iCloudMaintenanceFailed},
		{"authentication", &mailapp.OutboundNotAcceptedError{Cause: &mailapp.OutboundSendFailure{}}, 2, iCloudMaintenanceFailed},
		{"temporary policy", &mailapp.OutboundNotAcceptedError{Cause: &mailapp.OutboundSendFailure{Retryable: true}}, 2, iCloudMaintenanceFailed},
		{"temporary recipient", &mailapp.OutboundNotAcceptedError{Cause: &mailapp.OutboundSendFailure{Retryable: true, RecipientTemporary: true}}, 1, iCloudMaintenanceQueued},
		{"ambiguous recipient result", &mailapp.OutboundSendFailure{Retryable: true, RecipientTemporary: true}, 1, iCloudMaintenanceRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			redisServer := enableLivenessSMTPTest(t, s, sender)
			runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, `[
 {"id":"a","name":"A","enabled":true,"addr":"smtp.qq.com:465","username":"a@qq.com","password":"a-secret","from":"a@qq.com"},
 {"id":"b","name":"B","enabled":true,"addr":"smtp.qq.com:465","username":"b@qq.com","password":"b-secret","from":"b@qq.com"}]`)
			s.newLivenessSMTP = func(mailinfra.SMTPConfig) mailapp.SenderPort { return sender }
			sender.err = tc.err
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			require.NoError(t, s.db.Create(&iCloudAliasModel{ResourceID: 1, AnonymousID: "alias-2", Email: "another@icloud.com", Status: iCloudResourceNormal}).Error)
			run := queueSMTPTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			if tc.sends > 1 {
				require.NoError(t, s.db.Take(&run, run.ID).Error)
				require.Equal(t, iCloudMaintenanceQueued, run.Status)
				require.Zero(t, run.Attempts)
				require.Len(t, sender.messages, 1)
				redisServer.FastForward(iCloudLivenessSMTPSendInterval)
				*now = *run.NextCheckAt
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			}
			require.Len(t, sender.messages, tc.sends)
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, 1, run.Attempts)
			require.Equal(t, tc.status, run.Status)
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
		})
	}
}

func TestICloudLivenessSMTPThrottlesThousandConcurrentChecks(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	redisServer := enableLivenessSMTPTest(t, s, sender)
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPIntervalKey, "120")
	sqlDB, err := s.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	var resources []iCloudResourceModel
	var runs []iCloudMaintenanceRunModel
	for id := uint(1); id <= 1000; id++ {
		resources = append(resources, iCloudResourceModel{ID: id, ResourceType: "icloud", PrimaryEmail: fmt.Sprintf("owner%d@icloud.com", id), Status: iCloudResourceNormal, CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour)})
		runs = append(runs, iCloudMaintenanceRunModel{ID: uint64(id), ResourceID: id, Kind: iCloudMaintenanceLivenessSMTP, Status: iCloudMaintenanceQueued,
			CredentialRevision: 1, ValidationGeneration: 1, MaxAttempts: 1, ProbeRecipient: fmt.Sprintf("alias%d@icloud.com", id), NextCheckAt: now})
	}
	require.NoError(t, s.db.CreateInBatches(resources, 50).Error)
	require.NoError(t, s.db.CreateInBatches(runs, 50).Error)
	results := make(chan error, len(runs))
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			worker := NewService(s.db, nil, s.files, s.deviceRedis)
			worker.now, worker.livenessInbox, worker.newLivenessSMTP = s.now, s.livenessInbox, s.newLivenessSMTP
			for index := offset; index < len(runs); index += 20 {
				results <- worker.ProcessICloudLivenessCheck(context.Background(), runs[index].ID)
			}
		}(i)
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Len(t, sender.messages, 1, "1000 simultaneous checks must produce only one SMTP call")
	var queued int64
	require.NoError(t, s.db.Model(&iCloudMaintenanceRunModel{}).
		Where("status=? AND attempts=0 AND started_at IS NULL AND probe_sent_at IS NULL", iCloudMaintenanceQueued).Count(&queued).Error)
	require.EqualValues(t, 999, queued)
	var pending iCloudMaintenanceRunModel
	require.NoError(t, s.db.Where("status=?", iCloudMaintenanceQueued).Order("next_check_at ASC").Take(&pending).Error)
	require.False(t, pending.NextCheckAt.Before(now.Add(120*time.Second)))
	redisServer.FastForward(119 * time.Second)
	*now = now.Add(119 * time.Second)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), pending.ID))
	require.Len(t, sender.messages, 1)
	redisServer.FastForward(pending.NextCheckAt.Sub(*now))
	*now = *pending.NextCheckAt
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), pending.ID))
	require.Len(t, sender.messages, 2)
}

func TestICloudLivenessSMTPSendIntervalIsSharedAcrossWorkers(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	redisServer := enableLivenessSMTPTest(t, s, sender)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	first := queueSMTPTestRun(t, s)
	seedICloudLivenessResource(t, s, 2, iCloudResourceNormal, true)
	_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 2, 1, 99, "second", "req", "/liveness")
	require.NoError(t, err)
	var second iCloudMaintenanceRunModel
	require.NoError(t, s.db.Where("resource_id=2").Take(&second).Error)
	worker := NewService(s.db, nil, s.files, s.deviceRedis)
	worker.now, worker.livenessInbox, worker.newLivenessSMTP = s.now, s.livenessInbox, s.newLivenessSMTP
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), first.ID))
	require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), second.ID))
	require.NoError(t, s.db.Take(&second, second.ID).Error)
	require.Equal(t, iCloudMaintenanceQueued, second.Status)
	require.Zero(t, second.Attempts)
	require.Nil(t, second.StartedAt)
	require.Len(t, sender.messages, 1)
	require.Equal(t, now.Add(iCloudLivenessSMTPSendInterval), *second.NextCheckAt)
	redisServer.FastForward(iCloudLivenessSMTPSendInterval)
	*now = *second.NextCheckAt
	require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), second.ID))
	require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), second.ID))
	require.Len(t, sender.messages, 2, "repeated worker execution must not resend")
	require.NoError(t, s.db.Take(&second, second.ID).Error)
	require.Equal(t, 1, second.Attempts)
	require.NotNil(t, second.ProbeSentAt)
}

func TestICloudLivenessSMTPRedisFailureDoesNotSendOrConsumeAttempts(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	redisServer := enableLivenessSMTPTest(t, s, sender)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueSMTPTestRun(t, s)
	redisServer.SetError("ERR unavailable")
	require.Error(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Empty(t, sender.messages)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceQueued, run.Status)
	require.Zero(t, run.Attempts)
}

func TestICloudLivenessSMTPLeaseCoversSendingAndCooldown(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	redisServer := enableLivenessSMTPTest(t, s, sender)
	ctx := context.Background()
	interval := 5 * time.Second
	first, delay, err := s.reserveICloudLivenessSMTPSend(ctx, interval)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.Zero(t, delay)
	redisServer.FastForward(interval)
	blocked, delay, err := s.reserveICloudLivenessSMTPSend(ctx, interval)
	require.NoError(t, err)
	require.Empty(t, blocked, "a slow send must not overlap another send when the interval expires")
	require.Equal(t, iCloudLivenessWorkerTimeout, delay)
	keys := []string{iCloudLivenessSMTPIntervalRedisKey}
	require.NoError(t, finishICloudLivenessSMTPSendScript.Run(ctx, s.deviceRedis, keys, first, interval.Milliseconds()).Err())
	redisServer.FastForward(interval)
	second, delay, err := s.reserveICloudLivenessSMTPSend(ctx, interval)
	require.NoError(t, err)
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second)
	require.Zero(t, delay)
	changed, err := finishICloudLivenessSMTPSendScript.Run(ctx, s.deviceRedis, keys, first, interval.Milliseconds()).Int()
	require.NoError(t, err)
	require.Zero(t, changed, "an old worker must not shorten the current sending lease")
	require.Equal(t, interval+iCloudLivenessWorkerTimeout, redisServer.TTL(iCloudLivenessSMTPIntervalRedisKey))
}

func TestICloudLivenessSMTPFailoverNeverRestartsTriedServices(t *testing.T) {
	for _, scenario := range []string{"lost progress", "disabled remaining service", "ambiguous delivery"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, now := newICloudLivenessTestService(t)
			redisServer := enableLivenessSMTPTest(t, s, &iCloudLivenessMailStub{})
			const servers = `[
 {"id":"a","name":"A","enabled":true,"addr":"smtp.qq.com:465","username":"a@qq.com","password":"a-secret","from":"a@qq.com"},
 {"id":"b","name":"B","enabled":true,"addr":"smtp.qq.com:465","username":"b@qq.com","password":"b-secret","from":"b@qq.com"}]`
			runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, servers)
			first := &iCloudLivenessMailStub{err: &mailapp.OutboundNotAcceptedError{Cause: errors.New("connection refused")}}
			second := &iCloudLivenessMailStub{err: errors.New("lost DATA acknowledgement")}
			s.newLivenessSMTP = func(cfg mailinfra.SMTPConfig) mailapp.SenderPort {
				if cfg.From == "a@qq.com" {
					return first
				}
				return second
			}
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			run := queueSMTPTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceQueued, run.Status)
			if scenario == "lost progress" {
				require.NoError(t, s.deviceRedis.FlushDB(context.Background()).Err())
			}
			if scenario == "disabled remaining service" {
				runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, strings.ReplaceAll(servers, `"name":"B","enabled":true`, `"name":"B","enabled":false`))
			}
			redisServer.FastForward(iCloudLivenessSMTPSendInterval)
			*now = *run.NextCheckAt
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.Len(t, first.messages, 1)
			if scenario == "ambiguous delivery" {
				require.Len(t, second.messages, 1)
			} else {
				require.Empty(t, second.messages)
			}
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			if scenario == "ambiguous delivery" {
				require.Equal(t, iCloudMaintenanceRunning, run.Status)
				require.Nil(t, run.ProbeSentAt)
			} else {
				require.Equal(t, iCloudMaintenanceFailed, run.Status)
			}
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
		})
	}
}

func TestICloudLivenessSMTPAmbiguousDeliveryDoesNotFailOver(t *testing.T) {
	s, _, now := newICloudLivenessTestService(t)
	enableLivenessSMTPTest(t, s, &iCloudLivenessMailStub{})
	runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, `[
 {"id":"a","name":"A","enabled":true,"addr":"smtp.qq.com:465","username":"a@qq.com","password":"a-secret","from":"a@qq.com"},
 {"id":"b","name":"B","enabled":true,"addr":"smtp.qq.com:465","username":"b@qq.com","password":"b-secret","from":"b@qq.com"}]`)
	first, second := &iCloudLivenessMailStub{err: errors.New("connection lost after DATA")}, &iCloudLivenessMailStub{}
	s.newLivenessSMTP = func(cfg mailinfra.SMTPConfig) mailapp.SenderPort {
		if cfg.From == "a@qq.com" {
			return first
		}
		return second
	}
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueSMTPTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Len(t, first.messages, 1)
	require.Empty(t, second.messages)
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceRunning, run.Status)
	require.Nil(t, run.ProbeSentAt)
	*now = run.StartedAt.Add(iCloudLivenessReceiptWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
	require.Len(t, first.messages, 1)
	require.Empty(t, second.messages)
}

func TestICloudLivenessSMTPEmptyOrDisabledPoolDoesNotUseSystemSender(t *testing.T) {
	for _, raw := range []string{`[]`, `[{"id":"off","name":"Off","enabled":false,"addr":"smtp.qq.com:465","username":"off@qq.com","from":"off@qq.com"}]`} {
		t.Run(raw, func(t *testing.T) {
			s, sender, _ := newICloudLivenessTestService(t)
			enableLivenessSMTPTest(t, s, sender)
			runtimeconfig.Set(runtimeconfig.ICloudLivenessSMTPServersKey, raw)
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 1, 99, "empty-pool", "req", "/liveness")
			require.ErrorIs(t, err, ErrICloudLivenessUnavailable)
			require.Empty(t, sender.messages)
			var count int64
			require.NoError(t, s.db.Model(&iCloudMaintenanceRunModel{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestICloudLivenessSMTPMissingNonceDoesNotConfirmHealth(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	enableLivenessSMTPTest(t, s, sender)
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	run := queueSMTPTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.deviceRedis.FlushDB(context.Background()).Err())
	*now = now.Add(iCloudLivenessReceiptWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
}
