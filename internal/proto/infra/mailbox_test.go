package infra

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

type protoMailboxClientStub struct {
	protoValidationClientStub
	fetch func(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error)
}

func (client protoMailboxClientStub) Fetch(ctx context.Context, session *proton.Session, request proton.FetchRequest) (proton.FetchResult, error) {
	return client.fetch(ctx, session, request)
}

func TestProtoMailboxReusesKeysAndPersistsRefresh(t *testing.T) {
	s, id := newValidatedProto(t)
	s.Protocol = protoMailboxClientStub{
		protoValidationClientStub: protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
			t.Fatal("mail fetch must not perform password login")
			return proton.Session{}, nil
		}},
		fetch: func(ctx context.Context, session *proton.Session, request proton.FetchRequest) (proton.FetchResult, error) {
			require.Equal(t, "session@proto.test", request.Recipient)
			require.Empty(t, request.ProxyURL, "callers cannot bypass the configured proxy provider")
			require.Equal(t, "secret-refresh", session.RefreshToken)
			require.Nil(t, request.OnSession)
			require.NoError(t, request.RefreshSession(ctx, session, func(_ context.Context, current *proton.Session) error {
				current.AccessToken, current.RefreshToken = "refreshed-access", "refreshed-secret"
				return nil
			}))
			return proton.FetchResult{Complete: true, Messages: []proton.Message{{ID: "one"}}}, nil
		},
	}
	result, err := s.FetchMailbox(context.Background(), id, 1, proton.FetchRequest{
		Recipient: "another@proto.test", ProxyURL: "http://caller-controlled.invalid", FullHistory: true,
		OnSession: func(proton.Session) error { t.Fatal("session keys escaped the persistence boundary"); return nil },
		RefreshSession: func(context.Context, *proton.Session, func(context.Context, *proton.Session) error) error {
			t.Fatal("callers cannot replace the session refresh guard")
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	session, err := s.ReadSession(context.Background(), id, 1)
	require.NoError(t, err)
	require.Equal(t, "refreshed-secret", session.RefreshToken)
}

func TestProtoMailboxUnavailableSessionAutomaticallyRevalidates(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	require.NoError(t, s.SetForSale(ctx, id, nil, true))
	s.Queue = &protoQueueStub{}
	s.SessionSecret = "rotated-application-secret"
	before := protoValidationTask(t, s, id)
	for range 2 {
		result, err := s.FetchMailbox(ctx, id, 1, proton.FetchRequest{})
		require.ErrorIs(t, err, ErrSessionUnavailable)
		require.Empty(t, result.Messages)
	}
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.Equal(t, before.ValidationGeneration+1, row.ValidationGeneration)
	require.True(t, row.ForSale)
	var sessions int64
	require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&sessions).Error)
	require.Zero(t, sessions)
}

func TestProtoMailboxDiscardsPartialAndStaleReads(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "partial"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			s, id := newValidatedProto(t)
			s.Protocol = protoMailboxClientStub{fetch: func(ctx context.Context, _ *proton.Session, _ proton.FetchRequest) (proton.FetchResult, error) {
				if stale {
					require.NoError(t, s.ReplaceCredentials(ctx, id, nil, "replacement"))
				}
				return proton.FetchResult{Complete: stale, Messages: []proton.Message{{ID: "must-not-return"}}}, nil
			}}
			result, err := s.FetchMailbox(context.Background(), id, 1, proton.FetchRequest{FullHistory: true})
			require.Error(t, err)
			require.Empty(t, result.Messages)
			if stale {
				require.ErrorIs(t, err, domain.ErrInvalidClaim)
			}
		})
	}
}

func TestProtoMailboxRevokedSessionRevalidatesWithoutPermanentFailure(t *testing.T) {
	s, id := newValidatedProto(t)
	s.Protocol = protoMailboxClientStub{fetch: func(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error) {
		return proton.FetchResult{}, &proton.Failure{Category: "session_revoked", SafeMessage: "Session was revoked."}
	}}
	_, err := s.FetchMailbox(context.Background(), id, 1, proton.FetchRequest{})
	var failure *proton.Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "session_unavailable", failure.Category)
	require.True(t, failure.Retryable)
	row, err := s.GetResource(context.Background(), id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status, "session recovery must not enter Trade's abnormal-resource refund scan")
	var sessions int64
	require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&sessions).Error)
	require.Zero(t, sessions)
}

func TestProtoProjectHistoryDoesNotBlockLiveMailbox(t *testing.T) {
	s := newHistoryTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "history-live@proton.me", Password: "fixture-password"})
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.CompleteHistorySuccess(ctx, id, row.ValidationGeneration))
	require.NoError(t, s.SetForSale(ctx, id, nil, true))
	require.NoError(t, s.DB.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, role TEXT)`).Error)
	require.NoError(t, s.DB.Exec(`INSERT INTO users VALUES (7, 'active', 'supplier')`).Error)
	require.NoError(t, s.DB.Exec(`ALTER TABLE proto_allocations ADD COLUMN project_id INTEGER`).Error)
	require.NoError(t, s.DB.Exec(`INSERT INTO proto_allocations(resource_id, status, project_id) VALUES (?, 'allocated', 88)`, id).Error)
	require.NoError(t, s.ScheduleProjectHistory(ctx, 10, "history-live"))
	s.Queue = &protoQueueStub{}
	calls := 0
	s.Protocol = protoMailboxClientStub{fetch: func(fetchCtx context.Context, _ *proton.Session, request proton.FetchRequest) (proton.FetchResult, error) {
		calls++
		if !request.FullHistory {
			return proton.FetchResult{Complete: true, Messages: []proton.Message{{ID: "new-live-code", Body: "123456"}}}, nil
		}
		deadline, ok := fetchCtx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 14*time.Minute)
		var stored sessionRecord
		require.NoError(t, s.DB.Where("resource_id = ?", id).Take(&stored).Error)
		require.Empty(t, stored.LeaseToken, "the long history read must not own a refresh lease")
		live, err := s.FetchMailbox(ctx, id, row.CredentialRevision, proton.FetchRequest{MaxMessages: 1})
		require.NoError(t, err)
		require.Len(t, live.Messages, 1)
		require.Equal(t, "new-live-code", live.Messages[0].ID)
		candidates, err := allocinfra.NewRepo(s.DB).ListProtoSourceCandidates(ctx, 99, 8, allocdomain.SupplyScopePublic, nil, 10)
		require.NoError(t, err)
		require.Len(t, candidates, 1)
		return proton.FetchResult{Complete: true}, nil
	}}
	require.NoError(t, s.ProcessProjectHistory(ctx, ProjectHistoryTask{ProjectID: 10, Generation: 1}))
	require.Equal(t, 2, calls, "both the background and live request must reach the provider")
}

func TestProtoMailboxConcurrentReadersReuseOneRefreshedToken(t *testing.T) {
	s, id := newValidatedProto(t)
	sqlDB, err := s.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // Serialize SQLite transactions, never the provider calls.
	ctx := context.Background()
	entered := make(chan struct{}, 2)
	firstFinished := make(chan struct{})
	results := make(chan error, 2)
	var readers, refreshes atomic.Int32
	s.Protocol = protoMailboxClientStub{fetch: func(ctx context.Context, session *proton.Session, request proton.FetchRequest) (proton.FetchResult, error) {
		reader := readers.Add(1)
		entered <- struct{}{}
		if reader == 2 {
			<-firstFinished
		} else {
			<-entered
			<-entered
		}
		err := request.RefreshSession(ctx, session, func(callCtx context.Context, current *proton.Session) error {
			refreshes.Add(1)
			_, inTransaction := platform.GormTxFromContext(callCtx)
			if inTransaction {
				return errors.New("network exchange still holds a transaction")
			}
			current.AccessToken, current.RefreshToken = "rotated-access", "rotated-refresh"
			return nil
		})
		if reader == 1 {
			close(firstFinished)
		}
		if err != nil {
			return proton.FetchResult{}, err
		}
		if session.RefreshToken != "rotated-refresh" {
			return proton.FetchResult{}, errors.New("reader did not receive persisted rotation")
		}
		return proton.FetchResult{Complete: true}, nil
	}}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.FetchMailbox(ctx, id, 1, proton.FetchRequest{})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, refreshes.Load())
}

func TestProtoMailboxDoesNotPublishUncommittedRefresh(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	observed, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	before := observed.RefreshToken
	err = s.refreshMailboxSession(ctx, id, 1, observed, func(callCtx context.Context, updated *proton.Session) error {
		updated.RefreshToken = "must-not-publish"
		return s.ReplaceCredentials(callCtx, id, nil, "replacement-password")
	})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
	require.Equal(t, before, observed.RefreshToken)
}

func TestProtoMailboxRejectsTransactionsBeforeNetwork(t *testing.T) {
	s, id := newValidatedProto(t)
	s.Protocol = protoMailboxClientStub{fetch: func(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error) {
		t.Fatal("network must not run in a caller's database transaction")
		return proton.FetchResult{}, nil
	}}
	_, err := s.FetchMailbox(platform.WithGormTx(context.Background(), s.DB), id, 1, proton.FetchRequest{})
	require.ErrorIs(t, err, domain.ErrDependency)
}

func TestProtoMailboxNewLoginFencesAlreadyLoadedKeys(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	s.Protocol = protoMailboxClientStub{
		protoValidationClientStub: protoValidationClientStub{login: func(_ context.Context, request proton.LoginRequest) (proton.Session, error) {
			session := testProtoSession(request.Email)
			session.UID = "new-authenticated-session"
			return session, nil
		}},
		fetch: func(ctx context.Context, _ *proton.Session, _ proton.FetchRequest) (proton.FetchResult, error) {
			_, err := s.ClaimForValidation(ctx, id, nil)
			require.NoError(t, err)
			require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
			return proton.FetchResult{Complete: true, Messages: []proton.Message{{ID: "old-key-ring"}}}, nil
		},
	}
	result, err := s.FetchMailbox(ctx, id, 1, proton.FetchRequest{})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
	require.Empty(t, result.Messages)
	current, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	require.Equal(t, "new-authenticated-session", current.UID)
}

func TestProtoMailboxRefreshRejectsChangedKeyRing(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	observed, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, current *proton.Session, persist func(proton.Session) error) error {
		current.Addresses[0].PrivateKeys = []string{"new-address-key"}
		return persist(*current)
	}))
	err = s.refreshMailboxSession(ctx, id, 1, observed, func(context.Context, *proton.Session) error {
		t.Fatal("old reader must reload its key ring before refreshing a new session")
		return nil
	})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
}
