package infra

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func protoValidationTask(t *testing.T, s *Service, id uint) protoapp.ValidationTaskPayload {
	t.Helper()
	row, err := s.GetResource(context.Background(), id, nil)
	require.NoError(t, err)
	return protoapp.ValidationTaskPayload{ResourceID: id, OwnerUserID: row.OwnerUserID, CredentialRevision: row.CredentialRevision, ValidationGeneration: row.ValidationGeneration, RequestID: "validation-test"}
}

func newValidatedProto(t *testing.T) (*Service, uint) {
	t.Helper()
	s, _ := newProtoAsyncTestService(t)
	id, _, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: "session@proto.test", Password: "private-password"})
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(context.Background(), protoValidationTask(t, s, id)))
	return s, id
}

func TestProtoPlainSessionBindsResourceAndCredentialsWithoutSecret(t *testing.T) {
	t.Setenv("SESSION_SECRET", "")
	s := NewService(nil)
	session := testProtoSession("session@proto.test")
	session.ExpiresAt = session.ExpiresAt.UTC()
	payload, err := encodeSession(42, 3, session)
	require.NoError(t, err)
	require.True(t, json.Valid(payload))
	require.Contains(t, string(payload), session.RefreshToken)
	row := &sessionRecord{ResourceID: 42, CredentialRevision: 3, Payload: payload}
	decoded, err := s.decodeSession(row)
	require.NoError(t, err)
	require.Equal(t, session, *decoded)
	row.ResourceID++
	_, err = s.decodeSession(row)
	require.ErrorIs(t, err, ErrSessionUnavailable)
	row.ResourceID--
	row.CredentialRevision++
	_, err = s.decodeSession(row)
	require.ErrorIs(t, err, ErrSessionUnavailable)
	row.CredentialRevision--
	t.Setenv("SESSION_SECRET", "rotated-secret")
	decoded, err = s.decodeSession(row)
	require.NoError(t, err)
	require.Equal(t, session, *decoded)
}

func TestProtoPlainSessionRejectsLegacyCiphertext(t *testing.T) {
	s := NewService(nil)
	row := &sessionRecord{ResourceID: 42, CredentialRevision: 3, Payload: append([]byte{1}, []byte("legacy-ciphertext-canary")...)}
	for _, secret := range []string{"", "legacy-secret"} {
		t.Setenv("SESSION_SECRET", secret)
		decoded, err := s.decodeSession(row)
		require.ErrorIs(t, err, ErrSessionUnavailable)
		require.Nil(t, decoded)
		require.NotContains(t, err.Error(), "legacy-ciphertext-canary")
	}
}

func TestProtoValidationFailureBudgetAndInFlightCredentialFence(t *testing.T) {
	for _, test := range []struct {
		category string
		maximum  int
	}{{"request", 3}, {"protocol", 3}, {"protocol", 2}, {"dependency", 1}} {
		t.Run(test.category+" budget "+strconv.Itoa(test.maximum), func(t *testing.T) {
			const key = "resource_validation_max_failures"
			previous, existed := runtimeconfig.Snapshot()[key]
			runtimeconfig.Set(key, strconv.Itoa(test.maximum))
			t.Cleanup(func() {
				if existed {
					runtimeconfig.Set(key, previous)
				} else {
					runtimeconfig.Delete(key)
				}
			})
			s, _ := newProtoAsyncTestService(t)
			ctx := context.Background()
			id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "retry@proto.test", Password: "secret"})
			require.NoError(t, err)
			require.NoError(t, s.SetForSale(ctx, id, nil, true))
			s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
				return proton.Session{}, &proton.Failure{Category: test.category, SafeMessage: "Service temporarily unavailable.", Retryable: true, Cause: errors.New("sensitive-upstream-body")}
			}}
			for attempt := 1; attempt <= test.maximum; attempt++ {
				task := protoValidationTask(t, s, id)
				require.NoError(t, s.ProcessValidation(ctx, task))
				row, err := s.GetResource(ctx, id, nil)
				require.NoError(t, err)
				require.Equal(t, attempt, row.ValidationFailures)
				require.NotContains(t, row.LastSafeError, "sensitive-upstream-body")
				if attempt < test.maximum {
					require.Equal(t, domain.StatusPending, row.Status)
					require.Equal(t, task.ValidationGeneration+1, row.ValidationGeneration)
				} else {
					require.Equal(t, domain.StatusValidationFailed, row.Status)
					require.Equal(t, task.ValidationGeneration, row.ValidationGeneration)
				}
				require.True(t, row.ForSale)
				run, err := s.FindMaintenanceRun(ctx, id, task.ValidationGeneration, maintenanceKindValidation)
				require.NoError(t, err)
				require.Equal(t, maintenanceFailed, run.Status)
			}
			queued, err := s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
			require.NoError(t, err)
			require.Zero(t, queued, "exhausted failures must stop without becoming permanently abnormal")
			var physical Resource
			require.NoError(t, s.DB.First(&physical, id).Error)
			require.Equal(t, domain.StatusPending, physical.Status)
			// A deliberate manual retry creates a fresh budget/generation; it is not
			// blocked by the previous failed run or silently queued by a read.
			_, err = s.ClaimForValidation(ctx, id, nil)
			require.NoError(t, err)
			queued, err = s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
			require.NoError(t, err)
			require.Equal(t, 1, queued)
		})
	}
	t.Run("infrastructure failures release the same attempt for retry", func(t *testing.T) {
		s, _ := newProtoAsyncTestService(t)
		ctx := context.Background()
		id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "infrastructure@proto.test", Password: "secret"})
		require.NoError(t, err)
		task := protoValidationTask(t, s, id)
		s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
			return proton.Session{}, context.DeadlineExceeded
		}}
		require.ErrorIs(t, s.ProcessValidation(ctx, task), domain.ErrDependency)
		s.Protocol = protoValidationClientStub{}
		require.NoError(t, s.ProcessValidation(ctx, task))
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Zero(t, row.ValidationFailures)
		require.Equal(t, domain.StatusIdentifying, row.Status)
	})
	t.Run("password edit during login discards every returned key", func(t *testing.T) {
		s, _ := newProtoAsyncTestService(t)
		ctx := context.Background()
		id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "fenced@proto.test", Password: "old-password"})
		require.NoError(t, err)
		s.Protocol = protoValidationClientStub{login: func(ctx context.Context, request proton.LoginRequest) (proton.Session, error) {
			_, inTransaction := platform.GormTxFromContext(ctx)
			require.False(t, inTransaction)
			require.NoError(t, s.ReplaceCredentials(ctx, id, nil, "new-password"))
			return testProtoSession(request.Email), nil
		}}
		require.ErrorIs(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)), domain.ErrInvalidClaim)
		var count int64
		require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&count).Error)
		require.Zero(t, count)
	})
}

func TestProtoValidationOnlyPermanentCredentialFailuresBecomeAbnormal(t *testing.T) {
	for _, test := range []struct {
		category  string
		retryable bool
		status    string
		queued    int
	}{
		{"invalid_credentials", true, domain.StatusAbnormal, 0},
		{"identity_mismatch", true, domain.StatusAbnormal, 0},
		{"action_required", true, domain.StatusValidationFailed, 0},
		{"protocol", true, domain.StatusPending, 1},
		{"protocol", false, domain.StatusValidationFailed, 0},
		{"decryption", false, domain.StatusValidationFailed, 0},
		{"invalid_session", false, domain.StatusValidationFailed, 0},
	} {
		t.Run(test.category+strconv.FormatBool(test.retryable), func(t *testing.T) {
			s, _ := newProtoAsyncTestService(t)
			ctx := context.Background()
			id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "classification@proto.test", Password: "secret"})
			require.NoError(t, err)
			require.NoError(t, s.SetForSale(ctx, id, nil, true))
			s.Protocol = protoValidationClientStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
				return proton.Session{}, &proton.Failure{Category: test.category, SafeMessage: "Safe validation failure.", Retryable: test.retryable}
			}}
			task := protoValidationTask(t, s, id)
			require.NoError(t, s.ProcessValidation(ctx, task))
			row, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			require.True(t, row.ForSale)
			run, err := s.FindMaintenanceRun(ctx, id, task.ValidationGeneration, maintenanceKindValidation)
			require.NoError(t, err)
			require.Equal(t, test.status, row.Status)
			require.Equal(t, maintenanceFailed, run.Status)
			queued, err := s.DispatchPendingValidations(ctx, &protoQueueStub{}, 10)
			require.NoError(t, err)
			require.Equal(t, test.queued, queued)
		})
	}
}

func TestProtoSessionLeaseFencesRotationAndUnrefreshedReads(t *testing.T) {
	t.Run("only one refresher can use a session", func(t *testing.T) {
		s, id := newValidatedProto(t)
		ctx := context.Background()
		require.NoError(t, s.WithSession(ctx, id, 1, func(ctx context.Context, session *proton.Session, save func(proton.Session) error) error {
			_, inTransaction := platform.GormTxFromContext(ctx)
			require.False(t, inTransaction)
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Greater(t, time.Until(deadline), 55*time.Second)
			require.LessOrEqual(t, time.Until(deadline), time.Minute)
			var leased sessionRecord
			require.NoError(t, s.DB.First(&leased, id).Error)
			require.NotEmpty(t, leased.LeaseToken)
			require.WithinDuration(t, s.Now().Add(90*time.Second), *leased.LeaseExpiresAt, time.Second)
			err := s.WithSession(ctx, id, 1, func(context.Context, *proton.Session, func(proton.Session) error) error {
				t.Fatal("concurrent refresher admitted")
				return nil
			})
			require.ErrorIs(t, err, ErrSessionBusy)
			read, err := s.ReadSession(ctx, id, 1)
			require.NoError(t, err, "refresh must not block independent mailbox reads")
			require.Equal(t, session.RefreshToken, read.RefreshToken)
			var afterRead sessionRecord
			require.NoError(t, s.DB.First(&afterRead, id).Error)
			require.Equal(t, leased.LeaseToken, afterRead.LeaseToken)
			require.Equal(t, leased.Version, afterRead.Version)
			next := *session
			next.RefreshToken = "rotated-refresh"
			return save(next)
		}))
		require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, session *proton.Session, _ func(proton.Session) error) error {
			require.Equal(t, "rotated-refresh", session.RefreshToken)
			return nil
		}))
	})
	t.Run("expired worker cannot overwrite a newer rotation", func(t *testing.T) {
		s, id := newValidatedProto(t)
		ctx := context.Background()
		now := time.Now().UTC()
		s.Now = func() time.Time { return now }
		err := s.WithSession(ctx, id, 1, func(ctx context.Context, session *proton.Session, save func(proton.Session) error) error {
			now = now.Add(91 * time.Second)
			require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, fresh *proton.Session, persist func(proton.Session) error) error {
				next := *fresh
				next.RefreshToken = "winning-refresh"
				return persist(next)
			}))
			return save(*session)
		})
		require.ErrorIs(t, err, domain.ErrInvalidClaim)
		require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, session *proton.Session, _ func(proton.Session) error) error {
			require.Equal(t, "winning-refresh", session.RefreshToken)
			return nil
		}))
	})
	t.Run("password edit also fences a read without refresh", func(t *testing.T) {
		s, id := newValidatedProto(t)
		err := s.WithSession(context.Background(), id, 1, func(ctx context.Context, _ *proton.Session, _ func(proton.Session) error) error {
			return s.ReplaceCredentials(ctx, id, nil, "changed-password")
		})
		require.ErrorIs(t, err, domain.ErrInvalidClaim)
		var count int64
		require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&count).Error)
		require.Zero(t, count)
	})
}

func TestProtoReadSessionRejectsChangedOrDeletedCredentials(t *testing.T) {
	for _, change := range []string{"password", "deleted", "session_revision", "invalid_payload", "address_keys"} {
		t.Run(change, func(t *testing.T) {
			s, id := newValidatedProto(t)
			ctx := context.Background()
			before, err := s.ReadSession(ctx, id, 1)
			require.NoError(t, err)
			require.NotNil(t, before)
			want := ErrSessionUnavailable
			switch change {
			case "password":
				require.NoError(t, s.ReplaceCredentials(ctx, id, nil, "new-password"))
				want = domain.ErrInvalidClaim
			case "deleted":
				require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
				want = domain.ErrInvalidClaim
			case "session_revision":
				require.NoError(t, s.DB.Model(&sessionRecord{}).Where("resource_id = ?", id).Update("credential_revision", 2).Error)
			case "invalid_payload":
				require.NoError(t, s.DB.Model(&sessionRecord{}).Where("resource_id = ?", id).Update("payload", []byte("invalid-payload")).Error)
			case "address_keys":
				before.Addresses = nil
				payload, err := encodeSession(id, 1, *before)
				require.NoError(t, err)
				require.NoError(t, s.DB.Model(&sessionRecord{}).Where("resource_id = ?", id).Update("payload", payload).Error)
			}
			after, err := s.ReadSession(ctx, id, 1)
			require.ErrorIs(t, err, want)
			require.Nil(t, after, "no session snapshot may escape a failed fence")
		})
	}
}

func TestProtoRefreshCleanupDoesNotReleaseAnotherWorkersLease(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	entered, finish := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	})
	done := make(chan error, 1)
	go func() {
		done <- s.WithSession(ctx, id, 1, func(context.Context, *proton.Session, func(proton.Session) error) error {
			close(entered)
			<-finish
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first refresh never acquired its lease: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh did not start")
	}
	now = now.Add(91 * time.Second)
	require.NoError(t, s.WithSession(ctx, id, 1, func(_ context.Context, session *proton.Session, save func(proton.Session) error) error {
		var leased sessionRecord
		require.NoError(t, s.DB.First(&leased, id).Error)
		next := *session
		next.RefreshToken = "new-refresher-wins"
		require.NoError(t, save(next))
		close(finish)
		select {
		case err := <-done:
			require.ErrorIs(t, err, domain.ErrInvalidClaim)
		case <-time.After(5 * time.Second):
			t.Fatal("expired refresh did not finish cleanup")
		}
		var afterCleanup sessionRecord
		require.NoError(t, s.DB.First(&afterCleanup, id).Error)
		require.Equal(t, leased.LeaseToken, afterCleanup.LeaseToken, "old cleanup cannot release the new owner's lease")
		return nil
	}))
	read, err := s.ReadSession(ctx, id, 1)
	require.NoError(t, err)
	require.Equal(t, "new-refresher-wins", read.RefreshToken)
}

func TestProtoRefreshCancellationReleasesLease(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.WithSession(ctx, id, 1, func(callCtx context.Context, _ *proton.Session, _ func(proton.Session) error) error {
		cancel()
		return callCtx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	var stored sessionRecord
	require.NoError(t, s.DB.First(&stored, id).Error)
	require.Empty(t, stored.LeaseToken)
	require.Nil(t, stored.LeaseExpiresAt)
	require.NoError(t, s.WithSession(context.Background(), id, 1, func(context.Context, *proton.Session, func(proton.Session) error) error { return nil }))
}

func TestProtoUnavailableSessionRequeuesOnceWithoutRemovingSupplyIntent(t *testing.T) {
	s, id := newValidatedProto(t)
	ctx := context.Background()
	require.NoError(t, s.SetForSale(ctx, id, nil, true))
	before := protoValidationTask(t, s, id)
	require.NoError(t, s.DB.Model(&sessionRecord{}).Where("resource_id = ?", id).Update("payload", []byte("invalid-payload")).Error)
	require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, "", "Proto session needs validation."))
	require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, "", "Repeated session failure."))
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.Equal(t, before.ValidationGeneration+1, row.ValidationGeneration)
	require.True(t, row.ForSale)
	var count int64
	require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProtoLateSessionRepairKeepsNewValidationAndRefresh(t *testing.T) {
	t.Run("new validation generation", func(t *testing.T) {
		s, id := newValidatedProto(t)
		ctx := context.Background()
		before := protoValidationTask(t, s, id)
		_, err := s.ClaimForValidation(ctx, id, nil)
		require.NoError(t, err)
		require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
		require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, "", "Delayed failure."))
		after, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusIdentifying, after.Status)
		require.Equal(t, before.ValidationGeneration+1, after.ValidationGeneration)
		require.NoError(t, s.WithSession(ctx, id, after.CredentialRevision, func(context.Context, *proton.Session, func(proton.Session) error) error { return nil }))
	})
	t.Run("same generation already has a usable session", func(t *testing.T) {
		s, id := newValidatedProto(t)
		ctx := context.Background()
		before := protoValidationTask(t, s, id)
		require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, "", "Delayed missing-session observation."))
		after := protoValidationTask(t, s, id)
		require.Equal(t, before.ValidationGeneration, after.ValidationGeneration)
		require.NoError(t, s.WithSession(ctx, id, after.CredentialRevision, func(context.Context, *proton.Session, func(proton.Session) error) error { return nil }))
	})
	t.Run("refresh rotation supersedes a revoked-token observation", func(t *testing.T) {
		s, id := newValidatedProto(t)
		ctx := context.Background()
		before := protoValidationTask(t, s, id)
		observed := ""
		require.NoError(t, s.WithSession(ctx, id, before.CredentialRevision, func(_ context.Context, session *proton.Session, save func(proton.Session) error) error {
			observed = session.RefreshToken
			next := *session
			next.RefreshToken = "newer-session-refresh"
			return save(next)
		}))
		require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, observed, "Delayed revoked-session observation."))
		after := protoValidationTask(t, s, id)
		require.Equal(t, before.ValidationGeneration, after.ValidationGeneration)
		require.NoError(t, s.WithSession(ctx, id, before.CredentialRevision, func(_ context.Context, session *proton.Session, _ func(proton.Session) error) error {
			require.Equal(t, "newer-session-refresh", session.RefreshToken)
			observed = session.RefreshToken
			return nil
		}))
		require.NoError(t, s.RequeueSessionValidation(ctx, id, before.CredentialRevision, before.ValidationGeneration, observed, "Current session was revoked."))
		after = protoValidationTask(t, s, id)
		require.Equal(t, before.ValidationGeneration+1, after.ValidationGeneration)
	})
}
