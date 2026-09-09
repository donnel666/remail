package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOptionsDefaultToOneReadOnlyResource(t *testing.T) {
	opts, err := parseOptions([]string{"-resource-id", "1"}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "inspect", opts.Mode)
	require.False(t, opts.Apply)
	require.Equal(t, "python", opts.Engine)
	require.NotEmpty(t, opts.RequestID)
	for _, args := range [][]string{
		{}, {"-resource-id", "1", "-stdin"}, {"-mode", "login", "-file", "input.txt"},
		{"-resource-id", "1", "-line", "1"}, {"-stdin"}, {"-resource-id", "1", "-apply"},
		{"-resource-id", "1", "-engine", "python"}, {"-resource-id", "1", "-proxy-env", "PROXY"},
		{"-mode", "login", "-stdin", "-apply"}, {"-mode", "login", "-stdin", "-direct", "-proxy-env", "PROXY"},
		{"-mode", "validate", "-resource-id", "1", "-apply"},
		{"-mode", "history", "-resource-id", "1", "-apply"},
		{"-mode", "fetch", "-resource-id", "1", "-apply"},
		{"-mode", "fetch", "-resource-id", "1", "-limit", "101"},
		{"-mode", "login", "-stdin", "-engine", "other"},
		{"-resource-id", "1", "-timeout", "0"}, {"-resource-id", "1", "extra"},
	} {
		_, err := parseOptions(args, io.Discard)
		require.Error(t, err, "%v", args)
	}
}

func TestUsageNeverEchoesAccidentallyPastedCredentials(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseOptions([]string{"-resource-id", "password-canary"}, &stderr)
	require.Error(t, err)
	require.NotContains(t, err.Error()+stderr.String(), "password-canary")
	_, err = parseOptions([]string{"-help"}, &stderr)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, stderr.String(), "-apply")
	require.Contains(t, stderr.String(), "-resource-id")
	require.Contains(t, stderr.String(), "usernames default to @proton.me")
}

func TestCredentialSelectionPreservesPasswordAndRejectsMultipleStdinLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "credentials.txt")
	require.NoError(t, os.WriteFile(file, []byte("ignored\n\xef\xbb\xbfpERSON@EXAMPLE.TEST---- password with spaces \r\nignored"), 0600))
	credential, err := readCredential(options{File: file, Line: 2}, nil)
	require.NoError(t, err)
	require.Equal(t, "person@example.test", credential.Email)
	require.Equal(t, " password with spaces ", credential.Password)
	for _, input := range []string{"", "bad", "person@example.test----secret----extra", "person@example.test----secret\nsecond@example.test----secret"} {
		_, err := readCredential(options{Stdin: true}, strings.NewReader(input))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}

type protocolStub struct {
	login func(context.Context, proton.LoginRequest) (proton.Session, error)
}

func (p protocolStub) Login(ctx context.Context, req proton.LoginRequest) (proton.Session, error) {
	return p.login(ctx, req)
}
func (protocolStub) Fetch(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error) {
	panic("login diagnostics must not fetch messages")
}

func TestStandaloneLoginPreviewDoesNotNeedDatabaseOrProvider(t *testing.T) {
	opts, err := parseOptions([]string{"-mode", "login", "-stdin"}, io.Discard)
	require.NoError(t, err)
	out, err := execute(context.Background(), opts, strings.NewReader("person@example.test----secret"), nil, protocolStub{login: func(context.Context, proton.LoginRequest) (proton.Session, error) {
		t.Fatal("dry-run contacted provider")
		return proton.Session{}, nil
	}})
	require.NoError(t, err)
	require.True(t, out.DryRun)
	require.Equal(t, "preview", out.Outcome)
	require.True(t, out.PasswordConfigured)
}

func TestLoginReportsPKLMetadataWithoutSecretsOrDatabaseWrites(t *testing.T) {
	rt := testRuntime(t)
	before := databaseChanges(t, rt.db)
	opts := parsed(t, "-mode", "login", "-resource-id", "1", "-apply")
	out, err := execute(context.Background(), opts, nil, rt, protocolStub{login: func(_ context.Context, req proton.LoginRequest) (proton.Session, error) {
		require.Equal(t, "person@example.test", req.Email)
		require.Equal(t, "password-canary", req.Password)
		require.Equal(t, "http://proxy-user:proxy-secret@127.0.0.1:8080", req.ProxyURL)
		return proton.Session{Version: 2, UID: "uid-canary", PKL: []byte("pickle-canary"), KeyFingerprint: strings.Repeat("a", 64), Addresses: []proton.AddressKeys{{ID: "address-canary", Email: req.Email}}}, nil
	}})
	require.NoError(t, err)
	require.Equal(t, before, databaseChanges(t, rt.db), "diagnostic login must not touch resources, sessions, proxies, or bindings")
	require.Equal(t, "login_succeeded", out.Outcome)
	require.True(t, out.PKLGenerated)
	require.True(t, out.KeyMaterialReady)
	require.Equal(t, 1, out.AddressCount)
	require.Nil(t, out.KeyCount, "native PKL does not expose a plaintext key count")
	assertNoSecrets(t, out, "person@example.test", "password-canary", "pickle-canary", "uid-canary", "address-canary", "proxy-secret", strings.Repeat("a", 64))
}

func TestLoginAcceptsPKLInputWithoutSendingPasswordOrIgnoringIt(t *testing.T) {
	raw := bytes.Repeat([]byte("pkl-canary"), 1000)
	encoded := base64.StdEncoding.EncodeToString(raw)
	line := "owner@proton.me---- password-canary ----" + encoded + "\n"
	opts := parsed(t, "-mode", "login", "-stdin", "-direct", "-apply")
	provider := protocolStub{login: func(_ context.Context, req proton.LoginRequest) (proton.Session, error) {
		require.Empty(t, req.Password)
		require.Equal(t, raw, req.PKL)
		return proton.Session{Version: 2, UID: "uid-canary", PKL: req.PKL, KeyFingerprint: strings.Repeat("a", 64), Addresses: []proton.AddressKeys{{ID: "address-canary", Email: req.Email}}}, nil
	}}
	out, err := execute(context.Background(), opts, strings.NewReader(line), nil, provider)
	require.NoError(t, err)
	require.Equal(t, "login_succeeded", out.Outcome)
	assertNoSecrets(t, out, encoded, "password-canary", "pkl-canary", "uid-canary", "owner@proton.me")
	opts.Engine = "go"
	_, err = execute(context.Background(), opts, strings.NewReader(line), nil, nil)
	require.ErrorContains(t, err, "PKL requires the python engine")
	opts.Engine, opts.Apply = "python", false
	out, err = execute(context.Background(), opts, strings.NewReader(line), nil, nil)
	require.NoError(t, err)
	require.Equal(t, "preview", out.Outcome)
}

func TestLoginNormalizesBareUsernameWithAndWithoutPKLWithoutEchoingCredentials(t *testing.T) {
	raw := []byte("imported-pkl-canary")
	encoded := base64.StdEncoding.EncodeToString(raw)
	for _, withPKL := range []bool{false, true} {
		name := "password"
		if withPKL {
			name = "pkl"
		}
		t.Run(name, func(t *testing.T) {
			input := " BareUsernameCanary ---- password-canary "
			if withPKL {
				input += "----" + encoded
			}
			calls := 0
			provider := protocolStub{login: func(_ context.Context, req proton.LoginRequest) (proton.Session, error) {
				calls++
				require.Equal(t, "bareusernamecanary@proton.me", req.Email)
				if withPKL {
					require.Empty(t, req.Password)
					require.Equal(t, raw, req.PKL)
				} else {
					require.Equal(t, " password-canary ", req.Password)
					require.Empty(t, req.PKL)
				}
				return proton.Session{Version: 2, UID: "uid-canary", PKL: []byte("session-canary"), KeyFingerprint: strings.Repeat("a", 64),
					Addresses: []proton.AddressKeys{{ID: "address-canary", Email: req.Email}}}, nil
			}}
			opts := parsed(t, "-mode", "login", "-stdin", "-direct", "-apply")
			for _, apply := range []bool{true, false} {
				opts.Apply = apply
				out, err := execute(context.Background(), opts, strings.NewReader(input), nil, provider)
				require.NoError(t, err)
				require.Equal(t, !apply, out.DryRun)
				assertNoSecrets(t, out, "BareUsernameCanary", "bareusernamecanary", "password-canary", "imported-pkl-canary", encoded, "session-canary", "uid-canary")
			}
			require.Equal(t, 1, calls, "preview must not call the protocol client")
		})
	}
}

func TestLoginOnlyUsesExplicitDirectOrProxyEnvironmentWhenBindingMissing(t *testing.T) {
	rt := testRuntime(t)
	require.NoError(t, rt.db.Exec("UPDATE proxy_bindings SET expire_at = ?", time.Now().UTC().Add(-time.Hour)).Error)
	before := databaseChanges(t, rt.db)
	_, err := loginProxy(context.Background(), options{ResourceID: 1}, rt)
	require.ErrorContains(t, err, "no valid existing")
	value, err := loginProxy(context.Background(), options{Direct: true}, rt)
	require.NoError(t, err)
	require.Empty(t, value)
	t.Setenv("PROTO_TEST_PROXY", "socks5://user:proxy-canary@127.0.0.1:1080")
	value, err = loginProxy(context.Background(), options{ProxyEnv: "PROTO_TEST_PROXY"}, nil)
	require.NoError(t, err)
	require.Contains(t, value, "proxy-canary")
	require.Equal(t, before, databaseChanges(t, rt.db))
}

func TestDiagnosticBindingRejectsUnhealthyOrIneligibleRoutesWithoutMutation(t *testing.T) {
	for _, statement := range []string{
		"UPDATE proxies SET status = 'disabled'",
		"UPDATE proxies SET pool = 'system'",
		"UPDATE proxy_servers SET health_status = 'unhealthy'",
		"UPDATE proxy_servers SET admin_status = 'offline'",
		"UPDATE proxy_bindings SET bind_key = 'proto:2'",
		"UPDATE proxy_bindings SET ip_version = ''",
	} {
		t.Run(statement, func(t *testing.T) {
			rt := testRuntime(t)
			require.NoError(t, rt.db.Exec(statement).Error)
			before := databaseChanges(t, rt.db)
			_, err := loginProxy(context.Background(), options{ResourceID: 1}, rt)
			require.ErrorContains(t, err, "no valid existing")
			require.Equal(t, before, databaseChanges(t, rt.db))
		})
	}
}

func TestLegacyLoginCountsKeysAndRejectsWrongMailbox(t *testing.T) {
	opts := parsed(t, "-mode", "login", "-stdin", "-apply", "-direct", "-engine", "go")
	for _, valid := range []bool{true, false} {
		out, err := execute(context.Background(), opts, strings.NewReader("person@example.test----password-canary"), nil, protocolStub{login: func(_ context.Context, req proton.LoginRequest) (proton.Session, error) {
			email := req.Email
			if !valid {
				email = "other@example.test"
			}
			return proton.Session{Version: 1, UID: "uid-canary", AccessToken: "access-canary", RefreshToken: "refresh-canary", Addresses: []proton.AddressKeys{{ID: "address", Email: email, PrivateKeys: []string{"key-one", "key-two"}}}}, nil
		}})
		if valid {
			require.NoError(t, err)
			require.False(t, out.PKLGenerated)
			require.True(t, out.KeyMaterialReady)
			require.NotNil(t, out.KeyCount)
			require.Equal(t, 2, *out.KeyCount)
		} else {
			require.ErrorContains(t, err, "invalid or mismatched session")
		}
		assertNoSecrets(t, out, "person@example.test", "other@example.test", "password-canary", "access-canary", "refresh-canary", "key-one", "key-two")
	}
}

func TestInspectAndMaintenancePreviewsDoNotWriteOrQueue(t *testing.T) {
	for _, mode := range []string{"inspect", "validate", "history", "fetch"} {
		t.Run(mode, func(t *testing.T) {
			rt := testRuntime(t)
			queue := &queueStub{}
			rt.service.Queue = queue
			rt.fetch = func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
				t.Fatal("dry-run fetched remote mail")
				return proton.FetchResult{}, nil
			}
			before := databaseChanges(t, rt.db)
			out, err := execute(context.Background(), parsed(t, "-mode", mode, "-resource-id", "1"), nil, rt, nil)
			require.NoError(t, err)
			require.Equal(t, before, databaseChanges(t, rt.db))
			require.Empty(t, queue.tasks)
			require.True(t, out.DryRun)
			if mode == "inspect" {
				require.True(t, out.SessionStored)
				require.True(t, out.SessionCurrent)
			}
			assertNoSecrets(t, out, "person@example.test", "password-canary", "encrypted-session-canary")
		})
	}
}

func TestMaintenanceRequiresEnabledAdminAndResourceVersion(t *testing.T) {
	rt := testRuntime(t)
	for _, values := range []string{"'user', 'active'", "'supplier', 'active'", "'admin', 'disabled'"} {
		require.NoError(t, rt.db.Exec("UPDATE users SET (role, status) = ("+values+") WHERE id = 99").Error)
		_, err := execute(context.Background(), parsed(t, "-mode", "validate", "-resource-id", "1", "-apply", "-operator-user-id", "99"), nil, rt, nil)
		require.ErrorContains(t, err, "enabled administrator")
	}
	_, err := execute(context.Background(), parsed(t, "-resource-id", "1", "-version", "99"), nil, rt, nil)
	require.ErrorIs(t, err, domain.ErrVersionConflict)
}

type queueStub struct {
	tasks []*asynq.Task
	err   error
}

func (q *queueStub) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return nil, q.err
}

func TestMaintenanceUsesExistingCommandAndExactFencedQueuePayload(t *testing.T) {
	for _, mode := range []string{"validate", "history"} {
		t.Run(mode, func(t *testing.T) {
			rt := testRuntime(t)
			queue := &queueStub{}
			rt.service.Queue = queue
			opts := parsed(t, "-mode", mode, "-resource-id", "1", "-version", "1", "-apply", "-operator-user-id", "99")
			out, err := execute(context.Background(), opts, nil, rt, nil)
			require.NoError(t, err)
			require.Equal(t, "queued", out.Outcome, "acceptance is not successful validation/history")
			require.True(t, out.Queued)
			require.Equal(t, uint64(2), out.Version)
			require.Len(t, queue.tasks, 1)
			var payload protoapp.ValidationTaskPayload
			require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &payload))
			require.Equal(t, uint(1), payload.ResourceID)
			require.Equal(t, uint(7), payload.OwnerUserID)
			require.Equal(t, uint64(2), payload.CredentialRevision)
			require.Equal(t, out.ValidationGeneration, payload.ValidationGeneration)
			require.Equal(t, opts.RequestID, payload.RequestID)
			kind := protoapp.TaskValidate
			if mode == "history" {
				kind = protoapp.TaskHistory
			}
			require.Equal(t, kind, queue.tasks[0].Type())
			require.NotContains(t, string(queue.tasks[0].Payload()), "password-canary")
		})
	}
}

func TestFailedEnqueueDoesNotClaimBusinessSuccessOrExposeBackendErrors(t *testing.T) {
	rt := testRuntime(t)
	rt.service.Queue = &queueStub{err: errors.New("queue-password-canary")}
	out, err := execute(context.Background(), parsed(t, "-mode", "validate", "-resource-id", "1", "-apply", "-operator-user-id", "99"), nil, rt, nil)
	require.ErrorContains(t, err, "command committed but enqueue failed")
	setFailure(out, err)
	require.Equal(t, "failed", out.Outcome)
	require.False(t, out.Queued)
	require.Equal(t, uint64(2), out.ValidationGeneration)
	resource, loadErr := rt.service.GetResource(context.Background(), 1, nil)
	require.NoError(t, loadErr)
	require.Equal(t, domain.StatusPending, resource.Status)
	assertNoSecrets(t, out, "queue-password-canary")
}

func TestFetchIsBoundedAndNeverPrintsMailBody(t *testing.T) {
	t.Setenv("SESSION_SECRET", "")
	rt := testRuntime(t)
	rt.fetch = func(_ context.Context, id uint, revision uint64, req proton.FetchRequest) (proton.FetchResult, error) {
		require.Equal(t, uint(1), id)
		require.Equal(t, uint64(2), revision)
		require.Equal(t, 20, req.MaxMessages)
		require.Equal(t, 24*time.Hour, req.UntilAt.Sub(req.SinceAt))
		require.False(t, req.FullHistory)
		return proton.FetchResult{Complete: true, Messages: []proton.Message{{Subject: "subject-canary", Body: "body-canary", Sender: proton.Address{Address: "sender@example.test"}}}}, nil
	}
	out, err := execute(context.Background(), parsed(t, "-mode", "fetch", "-resource-id", "1", "-apply", "-operator-user-id", "99"), nil, rt, nil)
	require.NoError(t, err)
	require.Equal(t, 1, out.Fetched)
	require.True(t, out.Complete)
	assertNoSecrets(t, out, "subject-canary", "body-canary", "sender@example.test")
}

func TestRuntimeDoesNotDependOnSessionSecret(t *testing.T) {
	// Verify startup does not read or inject a session secret without opening MySQL/Redis.
	source, err := os.ReadFile("runtime.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "SessionSecret")
	require.NotContains(t, string(source), "SESSION_SECRET")
}

func TestProviderFailureOnlyExposesSafeDiagnosticFields(t *testing.T) {
	out := &result{}
	setFailure(out, &proton.Failure{Category: "action_required", Stage: "auth", HTTPStatus: 422, APICode: 9001, SafeMessage: "unsafe-body-canary", Cause: errors.New("token-canary")})
	require.Equal(t, "action_required", out.Category)
	require.Equal(t, "auth", out.Stage)
	require.Equal(t, 422, out.HTTPStatus)
	require.Equal(t, 9001, out.APICode)
	assertNoSecrets(t, out, "unsafe-body-canary", "token-canary")
}

func parsed(t *testing.T, args ...string) options {
	t.Helper()
	opts, err := parseOptions(args, io.Discard)
	require.NoError(t, err)
	return opts
}

func assertNoSecrets(t *testing.T, out *result, secrets ...string) {
	t.Helper()
	for _, jsonOutput := range []bool{false, true} {
		var output bytes.Buffer
		require.NoError(t, writeResult(&output, jsonOutput, out))
		for _, secret := range secrets {
			require.NotContains(t, output.String(), secret)
		}
	}
}

type rootRow struct {
	ID                   uint `gorm:"primaryKey"`
	Type                 string
	OwnerUserID          uint
	Version              uint64
	CreatedAt, UpdatedAt time.Time
}

func (rootRow) TableName() string { return "email_resources" }

func testRuntime(t *testing.T) *commandRuntime {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&rootRow{}, &protoinfra.Resource{}, &protoinfra.MaintenanceRun{}))
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, role TEXT, status TEXT)`,
		`INSERT INTO users VALUES (99, 'admin', 'active')`,
		`CREATE TABLE proto_sessions (resource_id INTEGER PRIMARY KEY, credential_revision INTEGER, payload BLOB)`,
		`INSERT INTO proto_sessions VALUES (1, 2, 'encrypted-session-canary')`,
		`CREATE TABLE proto_command_receipts (id INTEGER PRIMARY KEY, operator_user_id INTEGER, resource_id INTEGER, command TEXT, idempotency_key TEXT, request_fingerprint TEXT, reservation_token TEXT, status TEXT, result_json TEXT, created_at DATETIME, updated_at DATETIME, UNIQUE(operator_user_id, idempotency_key))`,
		`CREATE TABLE proxy_servers (id INTEGER PRIMARY KEY, health_status TEXT, admin_status TEXT)`,
		`INSERT INTO proxy_servers VALUES (1, 'healthy', 'online')`,
		`CREATE TABLE proxies (id INTEGER PRIMARY KEY, proxy_server_id INTEGER, pool TEXT, status TEXT, expire_at DATETIME, url TEXT)`,
		`INSERT INTO proxies VALUES (1, 1, 'resource', 'normal', NULL, 'http://proxy-user:proxy-secret@127.0.0.1:8080')`,
		`CREATE TABLE proxy_bindings (id INTEGER PRIMARY KEY, bind_key TEXT, proxy_id INTEGER, ip_version TEXT, expire_at DATETIME, last_used_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO proxy_bindings VALUES (1, 'proto:1', 1, 'ipv4', ?, ?)", time.Now().UTC().Add(time.Hour), time.Now().UTC()).Error)
	require.NoError(t, db.Create(&rootRow{ID: 1, Type: "proto", OwnerUserID: 7, Version: 1}).Error)
	require.NoError(t, db.Create(&protoinfra.Resource{ID: 1, ResourceType: "proto", OwnerUserID: 7, EmailAddress: "person@example.test", Password: "password-canary", Status: domain.StatusNormal, Version: 1, ValidationGeneration: 1, CredentialRevision: 2}).Error)
	return &commandRuntime{db: db, service: protoinfra.NewService(db)}
}

func databaseChanges(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var changes int64
	require.NoError(t, db.Raw("SELECT total_changes()").Scan(&changes).Error)
	return changes
}
