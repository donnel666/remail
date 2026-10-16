package icloud

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAppleAccountNotActiveIsTerminal(t *testing.T) {
	for _, body := range []string{
		`{"serviceErrors":[{"code":"-20755","message":"This Apple Account is not active."}]}`,
		`{"serviceErrors":[{"code":"-20755","message":"此 Apple 账户未激活。"}]}`,
		`{"service_errors":[{"message":"This Apple ID is not active."}]}`,
		`{"serviceErrors":[{"message":"Your Apple Account has been locked for security reasons."}]}`,
		`{"serviceErrors":[{"message":"This Apple ID is disabled."}]}`,
	} {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusForbidden, body: body}}}
		client := appleOnboardingTestClient(time.Now(), session)
		flow, err := loadAppleOnboardingFlow(context.Background(), client, appleOnboardingTestState(t, nil), "test@example.com")
		require.NoError(t, err)
		_, err = flow.postObject("https://idmsa.apple.com/appleauth/auth/signin/complete", map[string]any{}, "signin/complete", false, false, false)
		var appleErr *AppleOnboardingError
		require.ErrorAs(t, err, &appleErr)
		require.Equal(t, iCloudAccountLockedCategory, appleErr.Category)
		require.Equal(t, http.StatusForbidden, appleErr.HTTPStatus)
		require.False(t, appleErr.Retryable)
		require.Empty(t, appleErr.RestartStage)
		require.False(t, appleErr.SendRejected)
		require.Len(t, session.requests, 1)
	}
	for _, message := range []string{
		"Incorrect Apple Account or password.", "Your session has expired.", "Too many verification requests.",
		"This request has been temporarily blocked.", "Your Apple Account has been unlocked.",
		"Your Apple Account is not locked.", "Your Apple Account has not been disabled.",
		"This session is not active.", "SMS sending is disabled.", "The Apple Account login request is blocked.",
	} {
		require.False(t, appleOnboardingLooksLocked(map[string]any{"serviceErrors": []any{map[string]any{"message": message}}}), message)
	}
}

func TestAccountLockedStopsAllMaintenanceAndPreservesReceiving(t *testing.T) {
	for _, kind := range []string{"onboarding", iCloudUserImportTaskKind, "refresh", iCloudCookieRecoveryTaskKind} {
		t.Run(kind, func(t *testing.T) {
			s, db, task, apple := newOnboardingStateTest(t)
			require.NoError(t, db.AutoMigrate(&iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}, &iCloudAliasModel{}))
			now := s.now()
			require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id=?", task.ID).Updates(map[string]any{
				"task_kind": kind, "status": iCloudResourceNormal, "for_sale": true, "alias_count": 1, "stage": "manage_prepare",
				"dispatch_status": "running", "claim_token": "locked-test", "next_provision_at": now, "next_validation_at": now,
				"device_code_api": "https://device.example/code",
			}).Error)
			require.NoError(t, db.First(task, task.ID).Error)
			for _, channelKind := range []string{iCloudChannelAppleAccount, iCloudChannelWeb} {
				require.NoError(t, db.Create(&iCloudResourceChannelModel{ResourceID: task.ID, Kind: channelKind, Cookie: "saved-cookie", SessionStatus: iCloudSessionInvalid, NextKeepaliveAt: &now}).Error)
			}
			alias := iCloudAliasModel{ResourceID: task.ID, Email: "alias@icloud.com", AnonymousID: "saved", Status: iCloudResourceNormal}
			require.NoError(t, db.Create(&alias).Error)
			run := iCloudMaintenanceRunModel{ResourceID: task.ID, Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning, ValidationGeneration: 1, CredentialRevision: 1, MaxAttempts: 1, QueuedAt: now}
			require.NoError(t, db.Create(&run).Error)
			var before iCloudResourceModel
			require.NoError(t, db.First(&before, task.ID).Error)
			require.NoError(t, s.handleICloudOnboardingAppleError(context.Background(), task, &AppleOnboardingError{Category: iCloudAccountLockedCategory, SafeMessage: "not active", Retryable: true, RestartStage: "manage_prepare"}))
			var resource iCloudResourceModel
			require.NoError(t, db.First(&resource, task.ID).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
			require.True(t, resource.ForSale)
			require.Equal(t, uint(1), resource.AliasCount)
			require.True(t, isICloudAccountLocked(resource))
			require.Nil(t, resource.NextProvisionAt)
			require.Nil(t, resource.NextValidationAt)
			require.Nil(t, resource.WorkflowNextAttemptAt)
			require.Empty(t, resource.WorkflowSecretPayload)
			require.Empty(t, resource.WorkflowSessionPayload)
			require.Greater(t, resource.WorkflowGeneration, task.Generation)
			require.Equal(t, before.CredentialRevision, resource.CredentialRevision)
			require.Equal(t, before.ValidationGeneration, resource.ValidationGeneration)
			require.Equal(t, before.LastSafeError, resource.LastSafeError)
			require.NoError(t, db.First(&alias, alias.ID).Error)
			require.Equal(t, iCloudResourceNormal, alias.Status)
			var channels []iCloudResourceChannelModel
			require.NoError(t, db.Find(&channels).Error)
			for _, channel := range channels {
				require.Equal(t, iCloudSessionLocked, channel.SessionStatus)
				require.Nil(t, channel.NextKeepaliveAt)
				require.Equal(t, "saved-cookie", channel.Cookie)
				require.ErrorIs(t, s.persistICloudProvisionChannel(context.Background(), before, channel, true, false, now), errICloudValidationStale)
				_, _, err := s.loadICloudValidationProvisionScope(context.Background(), before, channel.ID)
				require.ErrorIs(t, err, errICloudValidationStale)
			}
			require.NoError(t, db.First(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceCanceled, run.Status)
			// Replayed workers and explicit recovery requests cannot restart either channel.
			for _, generation := range []uint64{task.Generation, resource.WorkflowGeneration} {
				require.NoError(t, s.ProcessICloudOnboardingTask(context.Background(), iCloudOnboardingTask{TaskID: task.ID, Generation: generation}))
			}
			require.NoError(t, s.EnsureICloudCookieRefresh(context.Background(), task.ID))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				for _, forceOld := range []bool{false, true} {
					created, err := s.ensureICloudCookieMaintenanceTx(context.Background(), tx, task.ID, forceOld, true)
					require.NoError(t, err)
					require.False(t, created)
				}
				return nil
			}))
			// A changed revision or an accidentally restored due time cannot clear the account-level lock.
			require.NoError(t, db.Model(&resource).Updates(map[string]any{"credential_revision": resource.CredentialRevision + 1, "next_provision_at": now, "next_validation_at": now}).Error)
			_, _, claimed, err := s.claimICloudProvision(context.Background(), resource.ID)
			require.NoError(t, err)
			require.False(t, claimed)
			candidates, err := s.iCloudValidationCandidates(context.Background(), 100)
			require.NoError(t, err)
			require.Empty(t, candidates)
			for _, command := range []AdminICloudCommand{AdminICloudValidate, AdminICloudAlias} {
				_, _, err := mutateAdminICloudResourceTx(context.Background(), db, command, resource.ID, nil, nil, now)
				require.ErrorIs(t, err, ErrICloudResourceStatus)
			}
			require.Empty(t, apple.operations)
		})
	}
}

func TestAccountLockedResultCannotReplaceNewerWorkflow(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id = ?", task.ID).Updates(map[string]any{"generation": task.Generation + 1}).Error)
	require.NoError(t, s.failICloudOnboardingTask(context.Background(), task, iCloudAccountLockedCategory, iCloudAccountLockedMessage))
	var resource iCloudResourceModel
	require.NoError(t, db.First(&resource, task.ID).Error)
	require.False(t, isICloudAccountLocked(resource))
}

func TestAccountLockedCookieViewKeepsIndependentMailHealth(t *testing.T) {
	id := uint(1)
	for _, health := range []string{iCloudResourceNormal, iCloudResourceAbnormal} {
		message := ""
		if health == iCloudResourceAbnormal {
			message = "iCloud alias rejected the liveness email."
		}
		view := adminICloudResourceView(adminICloudResourceRow{ID: id, Status: health, ForSale: true,
			LastErrorCategory: iCloudAccountLockedCategory, LastSafeError: message,
			NewChannelID: &id, NewSessionStatus: iCloudSessionInvalid, OldChannelID: &id, OldSessionStatus: iCloudSessionValid})
		require.Equal(t, health, view.Status)
		require.True(t, view.ForSale)
		require.Equal(t, iCloudSessionLocked, view.NewSession.Status)
		require.Equal(t, iCloudSessionLocked, view.OldSession.Status)
		if message == "" {
			require.Nil(t, view.LastSafeError)
		} else {
			require.Equal(t, message, *view.LastSafeError)
		}
		taskView := iCloudOnboardingTaskView(iCloudOnboardingTaskModel{LastErrorCategory: iCloudAccountLockedCategory, LastSafeError: message})
		require.Equal(t, iCloudAccountLockedMessage, taskView.LastSafeError)
	}
}

func TestAccountLockedBeforeFirstCookieIsVisible(t *testing.T) {
	s, db, task, _ := newOnboardingStateTest(t)
	require.NoError(t, db.AutoMigrate(&iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}))
	require.NoError(t, s.failICloudOnboardingTask(context.Background(), task, iCloudAccountLockedCategory, iCloudAccountLockedMessage))
	var resource iCloudResourceModel
	require.NoError(t, db.Take(&resource, task.ID).Error)
	var channels int64
	require.NoError(t, db.Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", task.ID).Count(&channels).Error)
	require.Zero(t, channels)
	view := adminICloudResourceView(adminICloudResourceRow{ID: resource.ID, Status: resource.Status,
		LastErrorCategory: resource.WorkflowLastErrorCategory, LastSafeError: resource.LastSafeError})
	require.Equal(t, iCloudResourcePending, view.Status)
	require.Nil(t, view.LastSafeError)
	require.NotNil(t, view.NewSession)
	require.NotNil(t, view.OldSession)
	require.Equal(t, iCloudSessionLocked, view.NewSession.Status)
	require.Equal(t, iCloudSessionLocked, view.OldSession.Status)
	require.Nil(t, view.NewSession.LastValidAt)
	require.Nil(t, view.OldSession.LastValidAt)
	for _, status := range []string{iCloudSessionUnchecked, iCloudSessionValid, iCloudSessionInvalid} {
		require.Nil(t, adminICloudSessionView(nil, status, 0, nil, nil, nil, nil))
	}
}

func TestAccountLockedCannotBeReimported(t *testing.T) {
	for _, status := range []string{iCloudResourceNormal, iCloudResourceDeleted} {
		t.Run(status, func(t *testing.T) {
			s, db, task, _ := newOnboardingStateTest(t)
			s.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
			require.NoError(t, db.Model(&iCloudResourceModel{}).Where("id=?", task.ID).Updates(map[string]any{
				"status": status, "last_error_category": iCloudAccountLockedCategory, "onboarding_status": iCloudOnboardingFailed,
			}).Error)
			content := []byte(fmt.Sprintf("美国区----否----%s----Secret1!----q1(a1)----q2(a2)----q3(a3)----2000-11-02----14155550001", task.PrimaryEmail))
			_, _, err := s.AcceptAdminICloudOnboardingImport(context.Background(), 1, 1, content, s.now().Add(time.Hour), "locked-reimport", "test", "/test")
			require.ErrorIs(t, err, ErrICloudResourceIdentity)
		})
	}
}

func TestAccountLockedAndLivenessHaveIndependentResults(t *testing.T) {
	for _, smtp := range []bool{false, true} {
		for _, received := range []bool{false, true} {
			t.Run(fmt.Sprintf("smtp=%t/received=%t", smtp, received), func(t *testing.T) {
				s, sender, now := newICloudLivenessTestService(t)
				if smtp {
					enableLivenessSMTPTest(t, s, sender)
				}
				initialHealth := iCloudResourceNormal
				if received {
					initialHealth = iCloudResourceAbnormal
				}
				seedICloudLivenessResource(t, s, 1, initialHealth, true)
				require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id=1").Update("alias_count", 1).Error)
				for _, kind := range []string{iCloudChannelAppleAccount, iCloudChannelWeb} {
					require.NoError(t, s.db.Create(&iCloudResourceChannelModel{
						ResourceID: 1, Kind: kind, Cookie: "saved-cookie", SessionStatus: iCloudSessionInvalid,
					}).Error)
				}
				var run iCloudMaintenanceRunModel
				if smtp {
					run = queueSMTPTestRun(t, s)
				} else {
					run = queueICloudLivenessTestRun(t, s)
				}
				if received {
					require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
					require.NoError(t, s.db.Take(&run, run.ID).Error)
					addICloudLivenessReceipt(t, s, run, sender.messages[0], *now)
				}
				var before iCloudResourceModel
				require.NoError(t, s.db.First(&before, 1).Error)
				require.NoError(t, s.stopLockedICloudAccount(context.Background(), before))
				var stopped iCloudResourceModel
				require.NoError(t, s.db.First(&stopped, 1).Error)
				require.Equal(t, initialHealth, stopped.Status)
				require.Equal(t, before.LastSafeError, stopped.LastSafeError)
				require.True(t, currentICloudLivenessRun(stopped, run))
				var preserved iCloudMaintenanceRunModel
				require.NoError(t, s.db.Take(&preserved, run.ID).Error)
				require.Equal(t, run.Status, preserved.Status, "locking Cookie access must not cancel a queued or running mail check")
				if received {
					*now = *run.NextCheckAt
				} else {
					sender.err = &mailapp.OutboundNotAcceptedError{Cause: &mailapp.OutboundSendFailure{RecipientRejected: true}}
				}
				require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
				var resource iCloudResourceModel
				require.NoError(t, s.db.First(&resource, 1).Error)
				var finished iCloudMaintenanceRunModel
				require.NoError(t, s.db.Take(&finished, run.ID).Error)
				if received {
					require.Equal(t, iCloudResourceNormal, resource.Status)
					require.Empty(t, resource.LastSafeError)
					require.Equal(t, iCloudMaintenanceSucceeded, finished.Status)
				} else {
					require.Equal(t, iCloudResourceAbnormal, resource.Status)
					require.Equal(t, "iCloud alias rejected the liveness email.", resource.LastSafeError)
					require.Equal(t, iCloudMaintenanceFailed, finished.Status)
				}
				require.True(t, isICloudAccountLocked(resource))
				require.Nil(t, resource.NextProvisionAt)
				require.Nil(t, resource.WorkflowNextAttemptAt)
				var channels []iCloudResourceChannelModel
				require.NoError(t, s.db.Find(&channels).Error)
				for _, channel := range channels {
					require.Equal(t, iCloudSessionLocked, channel.SessionStatus)
					require.Nil(t, channel.NextKeepaliveAt)
				}
				require.Len(t, sender.messages, 1)
			})
		}
	}
}
