package icloud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type livenessReceiptTestStore struct {
	db          *gorm.DB
	err         error
	beforeStore func()
	requests    []mailmatchapp.InboundMailRequest
}

func (s *livenessReceiptTestStore) StoreLivenessReceipt(ctx context.Context, req mailmatchapp.InboundMailRequest, fence func(context.Context) error) error {
	if s.beforeStore != nil {
		s.beforeStore()
	}
	if s.err != nil {
		return s.err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fence(platform.WithGormTx(ctx, tx)); err != nil {
			return err
		}
		s.requests = append(s.requests, req)
		return nil
	})
}

func TestLivenessReceiptStorageRetriesWithoutDelayingSuccess(t *testing.T) {
	for _, smtp := range []bool{false, true} {
		t.Run(fmt.Sprintf("smtp=%t", smtp), func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			if smtp {
				enableLivenessSMTPTest(t, s, sender)
			}
			seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
			var run iCloudMaintenanceRunModel
			if smtp {
				run = queueSMTPTestRun(t, s)
			} else {
				run = queueICloudLivenessTestRun(t, s)
			}
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			addICloudLivenessReceipt(t, s, run, sender.messages[0], *now)
			store := s.livenessReceipts.(*livenessReceiptTestStore)
			store.err = errors.New("temporary inbox storage failure")
			*now = now.Add(iCloudLivenessReceiptPoll)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var waiting iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&waiting, run.ID).Error)
			require.Equal(t, iCloudMaintenanceSucceeded, waiting.Status)
			require.Contains(t, waiting.LastSafeError, "retrying resource inbox storage")
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, run.ResourceID).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
			require.Empty(t, resource.LastSafeError)
			require.Empty(t, store.requests)
			require.Len(t, sender.messages, 1)
			store.err = nil
			*now = *waiting.NextCheckAt
			worker := NewService(s.db, nil, s.files, s.deviceRedis)
			worker.now = s.now
			worker.SetLivenessReceiptStore(store)
			require.NoError(t, worker.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var finished iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&finished, run.ID).Error)
			require.Equal(t, iCloudMaintenanceSucceeded, finished.Status)
			require.Nil(t, finished.NextCheckAt)
			require.Len(t, store.requests, 1)
			receipt := store.requests[0]
			require.Equal(t, uint(1), receipt.EmailResourceID)
			require.Equal(t, "alias1@icloud.com", receipt.Recipient)
			require.Equal(t, sender.messages[0].From, receipt.EnvelopeFrom)
			require.Equal(t, "smtp", receipt.Protocol)
			require.Equal(t, "inbound", receipt.Folder)
			require.Contains(t, receipt.ProviderMessageID, "inbound:")
			require.Contains(t, string(receipt.Raw), sender.messages[0].Subject)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.Len(t, store.requests, 1)
			require.Len(t, sender.messages, 1)
		})
	}
}

func TestLivenessReceiptStorageExpiryKeepsConfirmedSuccess(t *testing.T) {
	for _, smtp := range []bool{false, true} {
		t.Run(fmt.Sprintf("smtp=%t", smtp), func(t *testing.T) {
			s, sender, now := newICloudLivenessTestService(t)
			if smtp {
				enableLivenessSMTPTest(t, s, sender)
			}
			seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
			var run iCloudMaintenanceRunModel
			if smtp {
				run = queueSMTPTestRun(t, s)
			} else {
				run = queueICloudLivenessTestRun(t, s)
			}
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			require.NoError(t, s.db.Take(&run, run.ID).Error)
			addICloudLivenessReceipt(t, s, run, sender.messages[0], *now)
			s.livenessReceipts.(*livenessReceiptTestStore).err = errors.New("inbox storage unavailable")
			*now = now.Add(iCloudLivenessReceiptPoll)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			// A later worker can lose access to the receipt source without losing the recorded proof.
			s.files = nil
			window := iCloudLivenessLateBounceWindow
			if smtp {
				window = iCloudLivenessReceiptWait
			}
			*now = run.StartedAt.Add(window)
			require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
			var finished iCloudMaintenanceRunModel
			require.NoError(t, s.db.Take(&finished, run.ID).Error)
			require.Equal(t, iCloudMaintenanceSucceeded, finished.Status)
			require.Nil(t, finished.NextCheckAt)
			require.Contains(t, finished.LastSafeError, "could not be saved")
			var resource iCloudResourceModel
			require.NoError(t, s.db.Take(&resource, run.ResourceID).Error)
			require.Equal(t, iCloudResourceNormal, resource.Status)
			require.Empty(t, resource.LastSafeError)
			require.Len(t, sender.messages, 1)
		})
	}
}

func TestLivenessReceiptStorageRejectsReplacedRun(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceNormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.db.Take(&run, run.ID).Error)
	addICloudLivenessReceipt(t, s, run, sender.messages[0], *now)
	store := s.livenessReceipts.(*livenessReceiptTestStore)
	store.beforeStore = func() {
		require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id=1").Update("credential_revision", gorm.Expr("credential_revision+1")).Error)
	}
	*now = now.Add(iCloudLivenessReceiptPoll)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.Empty(t, store.requests)
	var canceled iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&canceled, run.ID).Error)
	require.Equal(t, iCloudMaintenanceCanceled, canceled.Status)
}

func TestLivenessReceiptStorageKeepsSuccessAgainstStaleBounce(t *testing.T) {
	s, sender, now := newICloudLivenessTestService(t)
	seedICloudLivenessResource(t, s, 1, iCloudResourceAbnormal, true)
	run := queueICloudLivenessTestRun(t, s)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	*now = now.Add(iCloudLivenessBounceWait)
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	var stale iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&stale, run.ID).Error)
	require.Equal(t, iCloudMaintenanceSucceeded, stale.Status)
	require.NotNil(t, stale.NextCheckAt)
	addICloudLivenessReceipt(t, s, stale, sender.messages[0], *now)
	s.livenessReceipts.(*livenessReceiptTestStore).err = errors.New("temporary storage outage")
	*now = *stale.NextCheckAt
	require.NoError(t, s.ProcessICloudLivenessCheck(context.Background(), run.ID))
	require.NoError(t, s.finishICloudLivenessCheck(context.Background(), stale, iCloudResourceAbnormal,
		iCloudMaintenanceFailed, "stale bounce result", nil))
	var current iCloudMaintenanceRunModel
	require.NoError(t, s.db.Take(&current, run.ID).Error)
	require.Equal(t, iCloudMaintenanceSucceeded, current.Status)
	require.Equal(t, iCloudLivenessReceiptStoragePending, current.LastSafeError)
	var resource iCloudResourceModel
	require.NoError(t, s.db.First(&resource, run.ResourceID).Error)
	require.Equal(t, iCloudResourceNormal, resource.Status)
	require.Empty(t, resource.LastSafeError)
}
