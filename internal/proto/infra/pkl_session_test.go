package infra

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

func testPKLSession(email string) proton.Session {
	return proton.Session{Version: 2, PKL: []byte("native-pkl-secret-fixture"), UID: "pkl-uid", KeyFingerprint: strings.Repeat("a", 64),
		Addresses: []proton.AddressKeys{{ID: "address", Email: email}}}
}

func newPKLValidatedProto(t *testing.T) (*Service, uint) {
	t.Helper()
	s, _ := newProtoAsyncTestService(t)
	s.Protocol = protoValidationClientStub{login: func(_ context.Context, request proton.LoginRequest) (proton.Session, error) {
		return testPKLSession(request.Email), nil
	}}
	id, _, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: "pkl@proton.me", Password: "password-fixture"})
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(context.Background(), protoValidationTask(t, s, id)))
	return s, id
}

func TestProtoPKLUsesExistingEncryptedSessionStorage(t *testing.T) {
	s, id := newPKLValidatedProto(t)
	var stored sessionRecord
	require.NoError(t, s.DB.First(&stored, id).Error)
	require.NotContains(t, string(stored.Payload), "native-pkl-secret-fixture")
	loaded, err := s.ReadSession(context.Background(), id, 1)
	require.NoError(t, err)
	require.Equal(t, testPKLSession("pkl@proton.me"), *loaded)
	require.Empty(t, loaded.AccessToken)
	require.Empty(t, loaded.RefreshToken)
	stored.ResourceID++
	_, err = s.decryptSession(&stored)
	require.ErrorIs(t, err, ErrSessionUnavailable)
}

func TestProtoPKLMetadataFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*proton.Session)
	}{
		{"missing pickle", func(s *proton.Session) { s.PKL = nil }},
		{"oversized pickle", func(s *proton.Session) { s.PKL = make([]byte, proton.MaxPKLBytes+1) }},
		{"missing identity", func(s *proton.Session) { s.UID = "" }},
		{"missing fingerprint", func(s *proton.Session) { s.KeyFingerprint = "" }},
		{"invalid fingerprint", func(s *proton.Session) { s.KeyFingerprint = strings.Repeat("z", 64) }},
		{"wrong recipient", func(s *proton.Session) { s.Addresses[0].Email = "other@proton.me" }},
		{"missing address ID", func(s *proton.Session) { s.Addresses[0].ID = "" }},
		{"duplicate address", func(s *proton.Session) { s.Addresses = append(s.Addresses, s.Addresses[0]) }},
		{"unknown version", func(s *proton.Session) { s.Version = 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := testPKLSession("pkl@proton.me")
			test.edit(&session)
			require.False(t, validSession(session, "pkl@proton.me"))
		})
	}
}

func TestProtoPKLConcurrentReadersShareOnePersistedRefresh(t *testing.T) {
	s, id := newPKLValidatedProto(t)
	db, err := s.DB.DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
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
		err := request.RefreshSession(ctx, session, func(ctx context.Context, next *proton.Session) error {
			refreshes.Add(1)
			if _, transaction := platform.GormTxFromContext(ctx); transaction {
				return errors.New("network under transaction")
			}
			next.PKL = []byte("rotated-native-pkl")
			return nil
		})
		if reader == 1 {
			close(firstFinished)
		}
		if err != nil {
			return proton.FetchResult{}, err
		}
		if string(session.PKL) != "rotated-native-pkl" {
			return proton.FetchResult{}, errors.New("rotation not persisted")
		}
		return proton.FetchResult{Complete: true}, nil
	}}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.FetchMailbox(context.Background(), id, 1, proton.FetchRequest{})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, refreshes.Load())
	current, err := s.ReadSession(context.Background(), id, 1)
	require.NoError(t, err)
	require.Equal(t, "rotated-native-pkl", string(current.PKL))
}

func TestProtoPKLStaleRevocationCannotEraseNewerSession(t *testing.T) {
	s, id := newPKLValidatedProto(t)
	ctx := context.Background()
	old, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	oldIdentity := proton.SessionTokenIdentity(*old)
	require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, current *proton.Session, save func(proton.Session) error) error {
		next := *current
		next.PKL = []byte("newer-pkl")
		return save(next)
	}))
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.RequeueSessionValidation(ctx, id, 1, row.ValidationGeneration, oldIdentity, "stale worker"))
	current, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	require.Equal(t, "newer-pkl", string(current.PKL))
	require.True(t, sameSessionMailbox(old, current), "cookie rotation must not fence a valid mailbox read")
	current.KeyFingerprint = strings.Repeat("b", 64)
	require.False(t, sameSessionMailbox(old, current), "changing mailbox keys must fence a stale read")
}

func TestProtoPKLRefreshRejectsChangedKeysAndConcurrentCredentialEdits(t *testing.T) {
	for _, changeKeys := range []bool{false, true} {
		t.Run(map[bool]string{false: "credentials", true: "keys"}[changeKeys], func(t *testing.T) {
			s, id := newPKLValidatedProto(t)
			ctx := context.Background()
			observed, err := s.ReadSession(ctx, id, 1)
			require.NoError(t, err)
			identity := proton.SessionTokenIdentity(*observed)
			err = s.refreshMailboxSession(ctx, id, 1, observed, func(ctx context.Context, next *proton.Session) error {
				next.PKL = []byte("must-not-be-published")
				if changeKeys {
					next.KeyFingerprint = strings.Repeat("b", 64)
					return nil
				}
				return s.ReplaceCredentials(ctx, id, nil, "new-password")
			})
			require.ErrorIs(t, err, domain.ErrInvalidClaim)
			require.Equal(t, identity, proton.SessionTokenIdentity(*observed))
		})
	}
}

func TestProtoPKLTemporaryHTTPFailureKeepsPreviouslyVerifiedSession(t *testing.T) {
	s, id := newPKLValidatedProto(t)
	ctx := context.Background()
	before, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
		return proton.Session{}, &proton.Failure{Stage: "auth", HTTPStatus: 503, APICode: 8002, Category: "request", SafeMessage: "Proto service is temporarily unavailable.", Retryable: true}
	}}
	_, err = s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	for attempt := 1; attempt <= 3; attempt++ {
		require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		if attempt < 3 {
			require.Equal(t, domain.StatusPending, row.Status)
		} else {
			require.Equal(t, domain.StatusValidationFailed, row.Status)
		}
		after, err := s.ReadSession(ctx, id, 1)
		require.NoError(t, err)
		require.Equal(t, proton.SessionTokenIdentity(*before), proton.SessionTokenIdentity(*after))
	}
}

func TestProtoMalformedPKLRevalidatesOnlyTheObservedSession(t *testing.T) {
	for _, newer := range []bool{false, true} {
		for _, category := range []string{"protocol", "identity_mismatch"} {
			t.Run(category+map[bool]string{false: "/current", true: "/stale"}[newer], func(t *testing.T) {
				s, id := newPKLValidatedProto(t)
				s.Protocol = protoMailboxClientStub{fetch: func(ctx context.Context, _ *proton.Session, _ proton.FetchRequest) (proton.FetchResult, error) {
					if newer {
						require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, current *proton.Session, save func(proton.Session) error) error {
							next := *current
							next.PKL = []byte("newer-verified-pkl")
							return save(next)
						}))
					}
					return proton.FetchResult{}, &proton.Failure{Stage: "session", Category: category, SafeMessage: "Invalid stored session."}
				}}
				_, err := s.FetchMailbox(context.Background(), id, 1, proton.FetchRequest{})
				var failure *proton.Failure
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "session_unavailable", failure.Category)
				row, err := s.GetResource(context.Background(), id, nil)
				require.NoError(t, err)
				if newer {
					require.Equal(t, domain.StatusIdentifying, row.Status)
					current, err := s.ReadSession(context.Background(), id, 1)
					require.NoError(t, err)
					require.Equal(t, "newer-verified-pkl", string(current.PKL))
				} else {
					require.Equal(t, domain.StatusPending, row.Status)
					_, err := s.ReadSession(context.Background(), id, 1)
					require.ErrorIs(t, err, ErrSessionUnavailable)
				}
			})
		}
	}
}
