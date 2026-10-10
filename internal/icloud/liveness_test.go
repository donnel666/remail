package icloud

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type iCloudLivenessMailStub struct {
	messages    []maildomain.OutboundMessage
	err         error
	rejectInbox bool
}

func (s *iCloudLivenessMailStub) Send(_ context.Context, message maildomain.OutboundMessage) error {
	s.messages = append(s.messages, message)
	return s.err
}

func (s *iCloudLivenessMailStub) ResolveInboundRecipient(_ context.Context, email string) (*maildomain.InboundRecipient, error) {
	if s.rejectInbox {
		return nil, maildomain.ErrInboundRecipientRejected
	}
	return &maildomain.InboundRecipient{Email: email, ResourceID: 9, OwnerUserID: 7, ResourceType: maildomain.InboundResourceDomain}, nil
}

func newICloudLivenessTestService(t *testing.T) (*Service, *iCloudLivenessMailStub, *time.Time) {
	t.Helper()
	db := newAdminICloudCommandTestDB(t, "icloud-liveness-"+strings.ReplaceAll(t.Name(), "/", "-"))
	require.NoError(t, db.AutoMigrate(&mailinfra.InboundMailModel{}))
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	files := &icloudImportFileStore{}
	service := NewService(db, nil, files)
	service.now = func() time.Time { return now }
	sender := &iCloudLivenessMailStub{}
	service.SetLivenessMail(sender, mailapp.NewInboundService(nil, sender, files, nil, nil), "ReMail <no-reply@relay.example>")
	return service, sender, &now
}

func seedICloudLivenessResource(t *testing.T, s *Service, id uint, status string, hasAlias bool) {
	t.Helper()
	now := s.now().UTC()
	createAdminICloudCommandResource(t, s.db, now, iCloudResourceModel{
		ID: id, ResourceType: "icloud", PrimaryEmail: fmt.Sprintf("owner%d@icloud.com", id), Status: status,
		CredentialRevision: 1, ValidationGeneration: 1, AliasCount: iCloudMaxAliases, ExpireAt: now.Add(time.Hour),
		NextValidationAt: &now,
	})
	if hasAlias {
		require.NoError(t, s.db.Create(&iCloudAliasModel{
			ResourceID: id, AnonymousID: fmt.Sprintf("alias-%d", id), Email: fmt.Sprintf("alias%d@icloud.com", id),
			Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
}

func queueICloudLivenessTestRun(t *testing.T, s *Service) iCloudMaintenanceRunModel {
	t.Helper()
	result, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 1, 99, "probe-1", "req-1", "/liveness")
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, uint64(2), result.Version)
	var run iCloudMaintenanceRunModel
	require.NoError(t, s.db.Where("resource_id = ? AND kind = ?", 1, iCloudMaintenanceLiveness).Take(&run).Error)
	return run
}

func addICloudLivenessBounce(t *testing.T, s *Service, runID uint64, report string) {
	t.Helper()
	var run iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&run, runID).Error)
	key := fmt.Sprintf("probe-bounce-%d", runID)
	report = "To: " + run.ProbeSender + "\r\n" + report
	raw := signICloudLivenessReport(t, s, report, "icloud.com", []string{"From", "To", "Content-Type"})
	_, err := s.files.SavePrivate(context.Background(), governancedomain.PrivateFile{ObjectKey: key, ContentBytes: raw})
	require.NoError(t, err)
	require.NoError(t, s.db.Create(&mailinfra.InboundMailModel{
		Recipient: run.ProbeSender, SourceObjectKey: key, ResourceID: 9, ResourceType: "domain", OwnerUserID: 7,
		Status: "stored", CreatedAt: s.now().UTC(), UpdatedAt: s.now().UTC(),
	}).Error)
}

func signICloudLivenessReport(t *testing.T, s *Service, report, domain string, headers []string) []byte {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var signed bytes.Buffer
	require.NoError(t, dkim.Sign(&signed, strings.NewReader(report), &dkim.SignOptions{
		Domain: domain, Selector: "liveness", Signer: private, HeaderKeys: headers,
	}))
	s.livenessLookupTXT = func(_ context.Context, name string) ([]string, error) {
		if name != "liveness._domainkey."+domain {
			return nil, &net.DNSError{IsNotFound: true, Name: name}
		}
		return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(public)}, nil
	}
	return signed.Bytes()
}

func livenessDSN(recipient, action, status string) string {
	return "From: Mail Delivery System <mailer-daemon@icloud.com>\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=report\r\n\r\n" +
		"--report\r\nContent-Type: text/plain\r\n\r\nDelivery report\r\n" +
		"--report\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx.icloud.com\r\n\r\n" +
		fmt.Sprintf("Final-Recipient: rfc822; %s\r\nAction: %s\r\nStatus: %s\r\n\r\n", recipient, action, status) +
		"--report--\r\n"
}

func TestICloudLivenessSubmissionKeepsCookiesAndReplays(t *testing.T) {
	s, _, _ := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	require.NoError(t, s.db.Create(&iCloudResourceChannelModel{ResourceID: 1, Kind: iCloudChannelWeb, Cookie: "saved-cookie", SessionStatus: iCloudSessionInvalid}).Error)
	run := queueICloudLivenessTestRun(t, s)
	require.Equal(t, "alias1@icloud.com", run.ProbeRecipient)
	require.Equal(t, iCloudMaintenanceQueued, run.Status)
	for _, key := range []string{"probe-1", "probe-duplicate"} {
		version := uint64(1)
		if key == "probe-duplicate" {
			version = 2
		}
		_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, version, 99, key, "req", "/liveness")
		require.NoError(t, err)
	}
	s.livenessSender = nil
	_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 1, 99, "probe-1", "req", "/liveness")
	require.NoError(t, err, "completed command receipts remain replayable when mail transport becomes unavailable")
	var count int64
	require.NoError(t, s.db.Model(&iCloudMaintenanceRunModel{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var channel iCloudResourceChannelModel
	require.NoError(t, s.db.First(&channel).Error)
	require.Equal(t, "saved-cookie", channel.Cookie)
	require.Equal(t, iCloudSessionInvalid, channel.SessionStatus)
	candidates, err := s.iCloudValidationCandidates(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, candidates)
	_, claimed, err := s.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 2, ExpectedCredentialRevision: 1,
	})
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestICloudLivenessBatchSkipsIneligibleResources(t *testing.T) {
	s, _, _ := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	seedICloudLivenessResource(t, s, 2, iCloudResourceNormal, false)
	seedICloudLivenessResource(t, s, 3, iCloudResourceDisabled, true)
	result, err := s.ApplyAdminICloudBatch(context.Background(), AdminICloudLiveness,
		AdminICloudResourceSelection{Mode: "ids", ResourceIDs: []uint{1, 2, 3}}, nil, 99, "batch-probe", "req", "/batch/liveness")
	require.NoError(t, err)
	require.Equal(t, 1, result.Affected)
	require.Equal(t, 2, result.Skipped)
	require.Equal(t, []uint{1}, result.AffectedResourceIDs)
	require.Contains(t, result.ReasonCounts, AdminICloudReasonCount{Reason: "no_alias", Count: 1})
	require.Contains(t, result.ReasonCounts, AdminICloudReasonCount{Reason: "invalid_state", Count: 1})
}

func TestICloudLivenessAcceptsThenObservesLateBounce(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Len(t, sender.messages, 1)
	require.Equal(t, "alias1@icloud.com", sender.messages[0].To)
	require.True(t, strings.HasPrefix(sender.messages[0].From, "icloud-check-"))
	require.True(t, strings.HasSuffix(sender.messages[0].From, "@relay.example"))
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
	require.NotNil(t, resource.LastValidAt)
	*now = now.Add(time.Hour)
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
	require.Len(t, sender.messages, 1)
}

func TestICloudLivenessSendFailuresDoNotMisclassifyAccounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"network", errors.New("dial timeout"), iCloudResourceNormal},
		{"temporary SMTP", &mailapp.OutboundSendFailure{Retryable: true}, iCloudResourceNormal},
		{"SMTP policy", &mailapp.OutboundSendFailure{}, iCloudResourceNormal},
		{"invalid recipient", &mailapp.OutboundSendFailure{RecipientRejected: true}, iCloudResourceAbnormal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sender, _ := newICloudLivenessTestService(t)
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			sender.err = tc.err
			run := queueICloudLivenessTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, tc.want, resource.Status)
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceFailed, run.Status)
		})
	}
}

func TestICloudLivenessTemporaryFailureSwitchesAliases(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "temporary", "policy"} {
		t.Run(outcome, func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			require.NoError(t, s.db.Create(&iCloudAliasModel{ResourceID: 1, AnonymousID: "alias-2", Email: "another@icloud.com", Status: iCloudResourceNormal}).Error)
			run := queueICloudLivenessTestRun(t, s)
			require.Equal(t, 2, run.MaxAttempts)
			sender.err = &mailapp.OutboundSendFailure{Retryable: true}
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var retry iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&retry, run.ID).Error)
			require.Equal(t, iCloudMaintenanceQueued, retry.Status)
			require.Equal(t, 1, retry.Attempts)
			require.Equal(t, "another@icloud.com", retry.ProbeRecipient)
			require.Empty(t, retry.ProbeSender)
			require.Nil(t, retry.ProbeSentAt)
			require.True(t, retry.NextCheckAt.After(*now))
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.Len(t, sender.messages, 1, "a queued retry must respect its next-check time")
			switch outcome {
			case "accepted":
				sender.err = nil
			case "rejected":
				sender.err = &mailapp.OutboundSendFailure{RecipientRejected: true}
			case "temporary":
				sender.err = &mailapp.OutboundSendFailure{Retryable: true}
			case "policy":
				sender.err = &mailapp.OutboundSendFailure{}
			}
			*now = *retry.NextCheckAt
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			if outcome == "accepted" {
				*now = now.Add(iCloudLivenessBounceWait)
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			}
			var finished iCloudMaintenanceRunModel
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&finished, run.ID).Error)
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, 2, finished.Attempts)
			require.Len(t, sender.messages, 2)
			require.NotEqual(t, sender.messages[0].To, sender.messages[1].To)
			require.NotEqual(t, sender.messages[0].From, sender.messages[1].From)
			wantStatus, wantHealth := iCloudMaintenanceFailed, iCloudResourceNormal
			if outcome == "accepted" {
				wantStatus = iCloudMaintenanceSucceeded
			}
			if outcome == "rejected" {
				wantHealth = iCloudResourceAbnormal
			}
			require.Equal(t, wantStatus, finished.Status)
			require.Equal(t, wantHealth, resource.Status)
		})
	}
}

func TestICloudLivenessPermanentRejectionDoesNotSwitchAliases(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	require.NoError(t, s.db.Create(&iCloudAliasModel{ResourceID: 1, AnonymousID: "alias-2", Email: "another@icloud.com", Status: iCloudResourceNormal}).Error)
	run := queueICloudLivenessTestRun(t, s)
	sender.err = &mailapp.OutboundSendFailure{RecipientRejected: true}
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var finished iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&finished, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, finished.Status)
	require.Equal(t, 1, finished.Attempts)
	require.Len(t, sender.messages, 1)
}

func TestICloudLivenessCanSwitchMoreThanOnceAndSkipDisabledAliases(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	for i, status := range []string{iCloudResourceDisabled, iCloudResourceNormal, iCloudResourceNormal} {
		require.NoError(t, s.db.Create(&iCloudAliasModel{ResourceID: 1, AnonymousID: fmt.Sprintf("extra-%d", i),
			Email: fmt.Sprintf("other%d@icloud.com", i), Status: status}).Error)
	}
	run := queueICloudLivenessTestRun(t, s)
	require.Equal(t, 3, run.MaxAttempts)
	sender.err = &mailapp.OutboundSendFailure{Retryable: true}
	for attempt := 1; attempt <= 2; attempt++ {
		require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
		var retry iCloudMaintenanceRunModel
		require.NoError(t, s.db.Take(&retry, run.ID).Error)
		require.Equal(t, iCloudMaintenanceQueued, retry.Status)
		require.Equal(t, attempt, retry.Attempts)
		*now = *retry.NextCheckAt
	}
	sender.err = nil
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var finished iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&finished, run.ID).Error)
	require.Equal(t, iCloudMaintenanceSucceeded, finished.Status)
	require.Equal(t, 3, finished.Attempts)
	require.Equal(t, []string{"alias1@icloud.com", "other1@icloud.com", "other2@icloud.com"},
		[]string{sender.messages[0].To, sender.messages[1].To, sender.messages[2].To})
}

func TestICloudLivenessCannotOverwriteNewerResourceState(t *testing.T) {
	for _, change := range []map[string]any{
		{"status": iCloudResourceDisabled}, {"status": iCloudResourceDeleted},
		{"credential_revision": 2}, {"validation_generation": 3},
	} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			s, _, now := newICloudLivenessTestService(t)
			seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
			run := queueICloudLivenessTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(change).Error)
			*now = now.Add(iCloudLivenessBounceWait)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.NotEqual(t, iCloudResourceNormal, resource.Status)
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceCanceled, run.Status)
		})
	}
}

func TestICloudLivenessWaitsWhenBounceStorageIsUnavailable(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
	storedFile, err := s.files.ReadPrivate(context.Background(), fmt.Sprintf("probe-bounce-%d", run.ID))
	require.NoError(t, err)
	require.NoError(t, s.files.DeletePrivate(context.Background(), fmt.Sprintf("probe-bounce-%d", run.ID)))
	*now = now.Add(iCloudLivenessBounceWait)
	require.ErrorIs(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID), ErrICloudMailUnavailable)
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceRunning, run.Status)
	_, err = s.files.SavePrivate(context.Background(), *storedFile)
	require.NoError(t, err)
	*now = *run.NextCheckAt
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var finished iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&finished, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, finished.Status)
	require.Contains(t, finished.LastSafeError, "bounced")
	require.Len(t, sender.messages, 1)
}

type canceledLivenessFileStore struct {
	*icloudImportFileStore
	cancel context.CancelFunc
}

func (s canceledLivenessFileStore) ReadPrivate(ctx context.Context, _ string) (*governancedomain.PrivateFile, error) {
	s.cancel()
	return nil, ctx.Err()
}

func TestICloudLivenessBounceReadTimeoutStillRecordsBackoff(t *testing.T) {
	s, _, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.files = canceledLivenessFileStore{icloudImportFileStore: s.files.(*icloudImportFileStore), cancel: cancel}
	*now = now.Add(iCloudLivenessBounceWait)
	require.ErrorIs(t, s.ProcessICloudLivenessCheck(ctx, run.ID), ErrICloudMailUnavailable)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	var stored iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&stored, run.ID).Error)
	require.True(t, stored.NextCheckAt.After(*now))
}

func TestICloudLivenessRequiresWorkingReturnMailbox(t *testing.T) {
	s, sender, _ := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	sender.rejectInbox = true
	_, err := s.ApplyAdminICloudCommand(context.Background(), AdminICloudLiveness, 1, 1, 99, "probe", "req", "/liveness")
	require.ErrorIs(t, err, ErrICloudLivenessUnavailable)
	var count int64
	require.NoError(t, s.db.Model(&iCloudMaintenanceRunModel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestICloudLivenessUnauthenticatedReportsPreserveAccountHealth(t *testing.T) {
	for _, attack := range []string{"unsigned", "fake authentication header", "unrelated signer", "ordinary iCloud sender", "tampered body", "unsigned content type", "unsigned destination", "duplicate From", "old probe replay"} {
		for _, health := range []string{iCloudResourceNormal, iCloudResourceAbnormal} {
			t.Run(attack+"/"+health, func(t *testing.T) {
				s, sender, now := newICloudLivenessTestService(t)
				*now = time.Now().UTC()
				seedICloudLivenessResource(t, s, 1, health, true)
				run := queueICloudLivenessTestRun(t, s)
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				report := "To: " + sender.messages[0].From + "\r\n" + livenessDSN("alias1@icloud.com", "failed", "5.1.1")
				domain, headers := "icloud.com", []string{"From", "To", "Content-Type"}
				switch attack {
				case "fake authentication header":
					report = "Authentication-Results: mx.example; dkim=pass header.d=icloud.com\r\n" + report
				case "unrelated signer":
					domain = "attacker.example"
				case "ordinary iCloud sender":
					report = strings.Replace(report, "mailer-daemon@icloud.com", "customer@icloud.com", 1)
				case "unsigned content type":
					headers = []string{"From", "To"}
				case "unsigned destination":
					headers = []string{"From", "Content-Type"}
				case "duplicate From":
					report = "From: mailer-daemon@icloud.com\r\n" + report
				case "old probe replay":
					report = strings.Replace(report, sender.messages[0].From, "old-probe@relay.example", 1)
				}
				raw := []byte(report)
				if attack != "unsigned" && attack != "fake authentication header" {
					raw = signICloudLivenessReport(t, s, report, domain, headers)
				}
				if attack == "tampered body" {
					raw = bytes.Replace(raw, []byte("Status: 5.1.1"), []byte("Status: 5.2.1"), 1)
				}
				inbox := mailapp.NewInboundService(mailinfra.NewInboundMailRepo(s.db), sender, s.files, nil, nil)
				recipient, err := inbox.ResolveRecipient(context.Background(), sender.messages[0].From)
				require.NoError(t, err)
				_, err = inbox.Accept(context.Background(), mailapp.InboundRawMessage{
					EnvelopeFrom: "customer@attacker.example", Recipients: []maildomain.InboundRecipient{*recipient}, ContentBytes: raw,
				})
				require.NoError(t, err)
				*now = now.Add(iCloudLivenessBounceWait)
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				var resource iCloudResourceModel
				require.NoError(t, s.db.Take(&resource, 1).Error)
				require.Equal(t, health, resource.Status)
				require.NoError(t, s.db.Take(&run, run.ID).Error)
				require.Equal(t, iCloudMaintenanceFailed, run.Status)
				require.Contains(t, run.LastSafeError, "could not be authenticated")
				require.Len(t, sender.messages, 1)
			})
		}
	}
}

func TestICloudLivenessAuthenticatedBounceFromInboundStorage(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	*now = time.Now().UTC()
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	report := "To: " + sender.messages[0].From + "\r\n" + livenessDSN("alias1@icloud.com", "failed", "5.1.1")
	raw := signICloudLivenessReport(t, s, report, "icloud.com", []string{"From", "To", "Content-Type"})
	inbox := mailapp.NewInboundService(mailinfra.NewInboundMailRepo(s.db), sender, s.files, nil, nil)
	recipient, err := inbox.ResolveRecipient(context.Background(), sender.messages[0].From)
	require.NoError(t, err)
	mails, err := inbox.Accept(context.Background(), mailapp.InboundRawMessage{
		Recipients: []maildomain.InboundRecipient{*recipient}, ContentBytes: raw,
	})
	require.NoError(t, err)
	require.Len(t, mails, 1)
	require.Empty(t, mails[0].EnvelopeFrom)
	require.Equal(t, maildomain.InboundStatusStored, mails[0].Status)
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
	require.Contains(t, run.LastSafeError, "bounced")
	require.Len(t, sender.messages, 1)
}

func TestICloudLivenessRecognizesAuthenticatedAppleReports(t *testing.T) {
	for _, domain := range []string{"icloud.com", "me.com", "mac.com", "mx.icloud.com"} {
		t.Run(domain, func(t *testing.T) {
			s, _, _ := newICloudLivenessTestService(t)
			report := "To: probe@relay.example\r\n" + livenessDSN("alias1@icloud.com", "failed", "5.1.1")
			report = strings.Replace(report, "mailer-daemon@icloud.com", "postmaster@"+domain, 1)
			raw := signICloudLivenessReport(t, s, report, domain, []string{"From", "To", "Content-Type"})
			verified, err := s.verifyICloudLivenessBounce(context.Background(), raw, "probe@relay.example")
			require.NoError(t, err)
			require.True(t, verified)
		})
	}
}

func TestICloudLivenessDKIMLookupFailureRetriesWithoutChangingHealth(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
	workingLookup := s.livenessLookupTXT
	s.livenessLookupTXT = func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{Err: "temporary DNS failure", IsTemporary: true}
	}
	*now = now.Add(iCloudLivenessBounceWait)
	require.ErrorIs(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID), ErrICloudMailUnavailable)
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceRunning, run.Status)
	require.WithinDuration(t, now.Add(iCloudLivenessCheckInterval), *run.NextCheckAt, time.Millisecond)
	s.livenessLookupTXT = workingLookup
	*now = *run.NextCheckAt
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceAbnormal, resource.Status)
	require.Len(t, sender.messages, 1)
}

func TestICloudLivenessPolicyBouncesPreserveAccountHealth(t *testing.T) {
	for _, status := range []string{"5.7.1", "5.0.0", "5.1.7", "5.1.8"} {
		for _, health := range []string{iCloudResourceNormal, iCloudResourceAbnormal} {
			t.Run(status+"/"+health, func(t *testing.T) {
				s, sender, now := newICloudLivenessTestService(t)
				seedICloudLivenessResource(t, s, 1, health, true)
				run := queueICloudLivenessTestRun(t, s)
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", status))
				*now = now.Add(iCloudLivenessBounceWait)
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				var resource iCloudResourceModel
				var stored iCloudMaintenanceRunModel
				require.NoError(t, s.db.Take(&resource, 1).Error)
				require.NoError(t, s.db.Take(&stored, run.ID).Error)
				require.Equal(t, health, resource.Status)
				require.Equal(t, iCloudMaintenanceFailed, stored.Status)
				require.Nil(t, stored.NextCheckAt)
				require.Contains(t, stored.LastSafeError, "account health is unchanged")
				require.Len(t, sender.messages, 1)
			})
		}
	}
}

func TestICloudLivenessLatePolicyBounceKeepsNormalHealth(t *testing.T) {
	s, _, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(time.Hour)
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.7.1"))
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var resource iCloudResourceModel
	var stored iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.NoError(t, s.db.Take(&stored, run.ID).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
	require.Equal(t, iCloudMaintenanceFailed, stored.Status)
	require.Nil(t, stored.NextCheckAt)
}

func TestICloudLivenessBounceReadFailureHasBackoffAndDeadline(t *testing.T) {
	for _, phase := range []string{iCloudMaintenanceRunning, iCloudMaintenanceSucceeded} {
		t.Run(phase, func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
			run := queueICloudLivenessTestRun(t, s)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			*now = now.Add(iCloudLivenessBounceWait)
			if phase == iCloudMaintenanceSucceeded {
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				*now = now.Add(5 * time.Minute)
			}
			addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
			require.NoError(t, s.files.DeletePrivate(context.Background(), fmt.Sprintf("probe-bounce-%d", run.ID)))
			require.ErrorIs(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID), ErrICloudMailUnavailable)
			var retry iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&retry, run.ID).Error)
			require.Equal(t, phase, retry.Status)
			require.WithinDuration(t, now.Add(5*time.Minute), *retry.NextCheckAt, time.Millisecond)
			require.NotEmpty(t, retry.LastSafeError)
			*now = retry.ProbeSentAt.Add(iCloudLivenessLateBounceWindow - time.Minute)
			require.ErrorIs(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID), ErrICloudMailUnavailable)
			var atDeadline iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&atDeadline, run.ID).Error)
			require.Equal(t, atDeadline.ProbeSentAt.Add(iCloudLivenessLateBounceWindow), *atDeadline.NextCheckAt)
			*now = *atDeadline.NextCheckAt
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var finished iCloudMaintenanceRunModel
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&finished, run.ID).Error)
			require.NoError(t, s.db.Take(&resource, 1).Error)
			require.Equal(t, iCloudMaintenanceFailed, finished.Status)
			require.Nil(t, finished.NextCheckAt)
			require.Equal(t, iCloudResourceNormal, resource.Status)
			candidates, err := s.iCloudValidationCandidates(context.Background(), 100)
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			require.Len(t, sender.messages, 1)
		})
	}
}

func TestICloudLivenessReadFailuresDoNotBlockLaterDispatch(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	redis := miniredis.RunT(t)
	redisOpt := asynq.RedisClientOpt{Addr: redis.Addr()}
	s.queue = asynq.NewClient(redisOpt)
	inspector := asynq.NewInspector(redisOpt)
	t.Cleanup(func() { _ = s.queue.Close(); _ = inspector.Close() })
	for id := uint(1); id <= 101; id++ {
		seedICloudLivenessResource(t, s, id, iCloudResourceNormal, true)
		due := now.Add(-time.Hour)
		run := iCloudMaintenanceRunModel{
			ResourceID: id, Kind: iCloudMaintenanceLiveness, Status: iCloudMaintenanceRunning,
			ValidationGeneration: 1, CredentialRevision: 1, MaxAttempts: 1,
			ProbeRecipient: fmt.Sprintf("alias%d@icloud.com", id), ProbeSender: fmt.Sprintf("probe%d@relay.example", id),
			NextCheckAt: &due, ProbeSentAt: &due, QueuedAt: due, CreatedAt: due, UpdatedAt: due,
		}
		if id == 101 {
			run.Status, run.NextCheckAt, run.ProbeSentAt = iCloudMaintenanceQueued, now, nil
		}
		require.NoError(t, s.db.Create(&run).Error)
		if id <= 100 {
			require.NoError(t, s.db.Create(&mailinfra.InboundMailModel{
				Recipient: run.ProbeSender, SourceObjectKey: "missing-file", ResourceID: 9, ResourceType: "domain", OwnerUserID: 7,
				Status: "stored", CreatedAt: due, UpdatedAt: due,
			}).Error)
			require.ErrorIs(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID), ErrICloudMailUnavailable)
		}
	}
	require.NoError(t, s.DispatchICloudLivenessChecks(context.Background()))
	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation, asynq.PageSize(1000))
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var payload struct{ RunID uint64 }
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, uint64(101), payload.RunID)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), payload.RunID))
	require.Len(t, sender.messages, 1)
	require.Equal(t, "alias101@icloud.com", sender.messages[0].To)
}

func TestICloudLivenessExpiresSendingWithoutResending(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.db.Model(&run).Updates(map[string]any{"status": iCloudMaintenanceRunning, "started_at": *now, "next_check_at": *now}).Error)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Empty(t, sender.messages)
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	require.Equal(t, iCloudMaintenanceFailed, run.Status)
	var resource iCloudResourceModel
	require.NoError(t, s.db.Take(&resource, 1).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
}

func TestICloudLivenessStopsWatchingAfterLastWindowCheck(t *testing.T) {
	s, _, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(iCloudLivenessLateBounceWindow)
	addICloudLivenessBounce(t, s, run.ID, livenessDSN("alias1@icloud.com", "failed", "5.1.1"))
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var stored iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&stored, run.ID).Error)
	require.Equal(t, iCloudMaintenanceSucceeded, stored.Status)
	require.Nil(t, stored.NextCheckAt)
}

func TestICloudLivenessDeliveryReportsMatchOnlyPermanentTargetFailure(t *testing.T) {
	for _, tc := range []struct {
		recipient, action, status string
		failed                    bool
		policy                    bool
	}{
		{"alias1@icloud.com", "failed", "5.1.1", true, false},
		{"ALIAS1@icloud.com", "failed", "5.2.1", true, false},
		{"alias2@icloud.com", "failed", "5.1.1", false, false},
		{"alias1@icloud.com", "delayed", "4.2.1", false, false},
		{"alias1@icloud.com", "delivered", "2.0.0", false, false},
		{"alias1@icloud.com", "failed", "4.0.0", false, false},
		{"alias1@icloud.com", "failed", "5.7.1", false, true},
		{"alias1@icloud.com", "failed", "5.1.7", false, true},
		{"alias1@icloud.com", "failed", "5.1.8", false, true},
		{"alias2@icloud.com", "failed", "5.7.1", false, false},
	} {
		failure := iCloudLivenessDeliveryFailure([]byte(livenessDSN(tc.recipient, tc.action, tc.status)), "alias1@icloud.com")
		if tc.failed || tc.policy {
			require.NotNil(t, failure)
			require.Equal(t, tc.failed, failure.RecipientRejected)
		} else {
			require.Nil(t, failure)
		}
	}
	require.Nil(t, iCloudLivenessDeliveryFailure([]byte("From: sender@example.com\r\nContent-Type: text/plain\r\n\r\nDelivery failed alias1@icloud.com"), "alias1@icloud.com"))
	// A DSN may contain reports for multiple recipients.
	report := strings.Replace(livenessDSN("alias2@icloud.com", "failed", "5.1.1"), "--report--", "Final-Recipient: rfc822; alias1@icloud.com\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--report--", 1)
	require.True(t, iCloudLivenessDeliveryFailure([]byte(report), "alias1@icloud.com").RecipientRejected)
	report = strings.Replace(livenessDSN("alias1@icloud.com", "failed", "5.7.1"), "--report--", "Final-Recipient: rfc822; alias1@icloud.com\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--report--", 1)
	require.True(t, iCloudLivenessDeliveryFailure([]byte(report), "alias1@icloud.com").RecipientRejected)
}
