package infra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

func TestProtoImportPKLPersistsBeforeValidationAndReusesIt(t *testing.T) {
	t.Setenv("SESSION_SECRET", "")
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	raw := []byte("native-pkl-import-canary")
	encoded := base64.StdEncoding.EncodeToString(raw)
	content := []byte("pkl@proton.me----password-canary----" + encoded + "\npkl@protonmail.com---- old password ")
	importID, _, err := s.CreateImport(ctx, 7, 7, domain.ErrorStrategyAbort, "pkl-import", content)
	require.NoError(t, err)
	calls := 0
	s.Protocol = protoValidationClientStub{login: func(ctx context.Context, req proton.LoginRequest) (proton.Session, error) {
		calls++
		_, inTransaction := platform.GormTxFromContext(ctx)
		require.False(t, inTransaction)
		if req.Email == "pkl@proton.me" {
			require.Equal(t, raw, req.PKL)
			require.Empty(t, req.Password)
		} else {
			require.Equal(t, "pkl@protonmail.com", req.Email)
			require.Empty(t, req.PKL)
			require.Equal(t, " old password ", req.Password)
		}
		return testPKLSession(req.Email), nil
	}}
	require.NoError(t, s.ProcessImport(ctx, importID, 1, 7, domain.ErrorStrategyAbort, content))
	require.Zero(t, calls, "import must not authenticate or deserialize PKL inside its transaction")
	ids, err := s.ListImportResourceIDs(ctx, importID)
	require.NoError(t, err)
	require.Len(t, ids, 2)
	var stored sessionRecord
	require.NoError(t, s.DB.First(&stored, ids[0]).Error)
	require.True(t, json.Valid(stored.Payload))
	require.Contains(t, string(stored.Payload), encoded)
	pending, err := s.decodeSession(&stored)
	require.NoError(t, err)
	require.Equal(t, raw, pending.PKL)
	require.False(t, pending.ValidFor("pkl@proton.me"))
	_, err = s.ReadSession(ctx, ids[0], 1)
	require.ErrorIs(t, err, ErrSessionUnavailable)
	queue := &protoQueueStub{}
	_, err = s.DispatchPendingValidations(ctx, queue, 10)
	require.NoError(t, err)
	for _, task := range queue.tasks {
		require.NotContains(t, string(task.Payload()), encoded)
		require.NotContains(t, string(task.Payload()), "password-canary")
	}
	for _, id := range ids {
		require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
		resource, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusIdentifying, resource.Status)
		session, err := s.ReadSession(ctx, id, resource.CredentialRevision)
		require.NoError(t, err)
		require.True(t, session.ValidFor(resource.EmailAddress))
		history, err := s.FindMaintenanceRun(ctx, id, resource.ValidationGeneration, maintenanceKindHistory)
		require.NoError(t, err)
		require.Equal(t, maintenanceQueued, history.Status)
	}
	require.Equal(t, 2, calls)
	require.NoError(t, s.DB.First(&stored, ids[0]).Error)
	before := append([]byte(nil), stored.Payload...)
	duplicate, outcome, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "pkl@proton.me", Password: "replacement", PKLBase64: encoded})
	require.NoError(t, err)
	require.Zero(t, duplicate)
	require.Equal(t, "skipped", outcome)
	require.NoError(t, s.ProcessImport(ctx, importID, 1, 7, domain.ErrorStrategyAbort, content))
	require.NoError(t, s.DB.First(&stored, ids[0]).Error)
	require.Equal(t, before, stored.Payload, "duplicate import/replay must not replace a verified PKL")
}

func TestProtoImportLineDoesNotCompleteBareUsernames(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	for _, email := range []string{"bare", " Bare "} {
		_, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: email, Password: "password-canary"})
		require.ErrorIs(t, err, domain.ErrInvalidResource)
	}
	var count int64
	require.NoError(t, s.DB.Model(&Resource{}).Count(&count).Error)
	require.Zero(t, count)
	// The supported-domain policy belongs to ParseImport, not generic mailbox
	// editing/session validation or this internal test/helper entry point.
	require.True(t, validEmail("existing@custom.example"))
	_, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "existing@custom.example", Password: "unchanged"})
	require.NoError(t, err)
}

func TestProtoImportPKLRestorationBindsNewCredentialRevision(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	encoded := base64.StdEncoding.EncodeToString([]byte("first-pkl"))
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "restore@proton.me", Password: "old", PKLBase64: encoded})
	require.NoError(t, err)
	require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
	encoded = base64.StdEncoding.EncodeToString([]byte("replacement-pkl"))
	restored, outcome, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "restore@proton.me", Password: "new", PKLBase64: encoded})
	require.NoError(t, err)
	require.Equal(t, id, restored)
	require.Equal(t, "restored", outcome)
	var stored sessionRecord
	require.NoError(t, s.DB.First(&stored, id).Error)
	require.EqualValues(t, 2, stored.CredentialRevision)
	plain, err := s.decodeSession(&stored)
	require.NoError(t, err)
	require.Equal(t, "replacement-pkl", string(plain.PKL))
	stored.CredentialRevision--
	_, err = s.decodeSession(&stored)
	require.ErrorIs(t, err, ErrSessionUnavailable)
}

func TestProtoImportPKLFailuresDoNotLeavePartialResourcesOrLeakInput(t *testing.T) {
	for _, strategy := range []string{domain.ErrorStrategyAbort, domain.ErrorStrategySkip} {
		t.Run(strategy, func(t *testing.T) {
			s, files := newProtoAsyncTestService(t)
			ctx := context.Background()
			encoded := base64.StdEncoding.EncodeToString([]byte("pkl-canary"))
			content := []byte("good@proton.me----secret----" + encoded + "\ninvalid-format-" + encoded + "\nbad@proton.me----secret----not-base64!" +
				"\nbare----secret----" + encoded + "\nunsupported@example.com----secret----" + encoded)
			id, _, err := s.CreateImport(ctx, 7, 7, strategy, "invalid-pkl", content)
			require.NoError(t, err)
			require.NoError(t, s.ProcessImport(ctx, id, 1, 7, strategy, content))
			var count int64
			require.NoError(t, s.DB.Model(&sessionRecord{}).Count(&count).Error)
			if strategy == domain.ErrorStrategyAbort {
				require.Zero(t, count)
			} else {
				require.EqualValues(t, 1, count)
			}
			for _, failureFile := range files.objects {
				require.NotContains(t, string(failureFile), encoded)
				require.NotContains(t, string(failureFile), "secret")
			}
		})
	}
	s, _ := newProtoAsyncTestService(t)
	require.NoError(t, s.DB.Exec("CREATE TRIGGER fail_proto_session_write BEFORE INSERT ON proto_sessions BEGIN SELECT RAISE(FAIL, 'session write failed'); END").Error)
	_, _, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: "rollback@proton.me", Password: "secret", PKLBase64: base64.StdEncoding.EncodeToString([]byte("pkl-canary"))})
	require.Error(t, err)
	for _, table := range []any{&Resource{}, &resourceRoot{}, &sessionRecord{}} {
		var count int64
		require.NoError(t, s.DB.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestProtoImportedPKLFailureNeverFallsBackToPassword(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	raw := []byte("invalid-imported-pkl")
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "wrong@proton.me", Password: "password-canary", PKLBase64: base64.StdEncoding.EncodeToString(raw)})
	require.NoError(t, err)
	s.Protocol = protoValidationClientStub{login: func(_ context.Context, req proton.LoginRequest) (proton.Session, error) {
		require.Equal(t, raw, req.PKL)
		require.Empty(t, req.Password)
		return proton.Session{}, &proton.Failure{Category: "identity_mismatch", SafeMessage: "Imported session does not own the mailbox."}
	}}
	for attempt := 0; attempt < 2; attempt++ {
		require.NoError(t, s.ProcessValidation(ctx, protoValidationTask(t, s, id)))
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusValidationFailed, row.Status)
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), string(raw))
		require.NotContains(t, string(encoded), "password-canary")
		_, err = s.ClaimForValidation(ctx, id, nil)
		require.NoError(t, err)
	}
}
