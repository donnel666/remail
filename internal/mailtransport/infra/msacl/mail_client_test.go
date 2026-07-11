package msacl

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeMailboxReader struct {
	calls atomic.Int32
}

type failingMailboxReader struct{}

func (failingMailboxReader) List(context.Context, string, int, bool) ([]EmailObj, error) {
	return nil, errors.New("mailbox unavailable")
}

func (failingMailboxReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, errors.New("mailbox unavailable")
}

func (r *fakeMailboxReader) List(_ context.Context, mailbox string, _ int, _ bool) ([]EmailObj, error) {
	call := r.calls.Add(1)
	if call < 3 {
		return nil, nil
	}
	return []EmailObj{{
		ID:      call,
		Subject: "Microsoft account security code",
		Preview: "Your security code is 123456.",
		To:      mailbox,
	}}, nil
}

func (r *fakeMailboxReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, nil
}

func TestMailWaitCodeUsesLateArrivalGrace(t *testing.T) {
	previousReader := activeMailboxReader()
	previousInterval := mailPollInterval
	previousGrace := mailLateArrivalGrace
	defer func() {
		SetMailboxReader(previousReader)
		mailPollInterval = previousInterval
		mailLateArrivalGrace = previousGrace
	}()

	SetMailboxReader(&fakeMailboxReader{})
	mailPollInterval = 1
	mailLateArrivalGrace = 2

	started := time.Now()
	code, err := mailWaitCode(context.Background(), "code@example.com", "", 1, nil)
	require.NoError(t, err)
	require.Equal(t, "123456", code)
	require.GreaterOrEqual(t, time.Since(started), time.Second)
}

func TestMailWatcherDefaultWaitIncludesLateArrivalGrace(t *testing.T) {
	previousInterval := mailPollInterval
	previousGrace := mailLateArrivalGrace
	defer func() {
		mailPollInterval = previousInterval
		mailLateArrivalGrace = previousGrace
	}()

	mailPollInterval = 2
	mailLateArrivalGrace = 12
	watcher := &MailWatcher{timeout: 60}

	require.Equal(t, 79, watcher.defaultCodeWaitTimeout())
}

func TestSnapshotMailboxKeysReturnsBaselineFailure(t *testing.T) {
	previousReader := activeMailboxReader()
	defer SetMailboxReader(previousReader)
	SetMailboxReader(failingMailboxReader{})

	keys, err := snapshotMailboxKeys(context.Background(), "code@example.com", "")
	require.Error(t, err)
	require.Nil(t, keys)
}
