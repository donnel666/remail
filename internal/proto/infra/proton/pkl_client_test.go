package proton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fixturePKLSession() Session {
	return Session{Version: 2, UID: "fixture-uid", PKL: []byte("opaque-native-pickle-old"), KeyFingerprint: strings.Repeat("a", 64),
		Addresses: []AddressKeys{{ID: "address", Email: "owner@proton.me"}}}
}

func pklTestClient(scenario string) *PKLClient {
	client := NewPKLClient()
	client.command = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPKLBridgeHelper$", "--", scenario)
	}
	return client
}

// An offline child process exercises the real pipe/parser/cancellation boundary.
// It never imports Python or contacts Proton.
func TestPKLBridgeHelper(_ *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	scenario := os.Args[len(os.Args)-1]
	var raw json.RawMessage
	var request pklRequest
	if json.NewDecoder(os.Stdin).Decode(&raw) != nil || json.Unmarshal(raw, &request) != nil {
		os.Exit(3)
	}
	write := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(4)
		}
	}
	writeSession := func(pkl string) {
		session := fixturePKLSession()
		write(pklEvent{Event: "session", PKL: []byte(pkl), UID: session.UID, Addresses: session.Addresses, KeyFingerprint: session.KeyFingerprint})
	}
	writeMessage := func(id string) {
		folder := "Inbox"
		if scenario == "refresh-moved" && string(request.PKL) == "opaque-native-pickle-new" && id == "one" {
			folder = "Junk"
		}
		write(pklEvent{Event: "messages", Messages: []Message{{ID: id, AddressID: "address", Folder: folder, Body: "fixture body", ReceivedAt: time.Unix(100, 0).UTC()}}})
	}
	complete := true
	switch scenario {
	case "environment":
		for _, key := range []string{"MYSQL_DSN", "SESSION_SECRET", "REDIS_PASSWORD", "PROTO_PYTHON_BIN", "PYTHONPATH", "HTTP_PROXY", "HTTPS_PROXY"} {
			if _, exists := os.LookupEnv(key); exists {
				os.Exit(9)
			}
		}
		if os.Getenv("TZ") != "UTC" {
			os.Exit(10)
		}
		writeSession("opaque-native-pickle-old")
		write(pklEvent{Event: "result", Complete: &complete})
	case "login":
		if request.Action != "login" || request.Email != "owner@proton.me" || request.Password != " secret-fixture-password " {
			os.Exit(5)
		}
		writeSession("opaque-native-pickle-old")
		write(pklEvent{Event: "result", Complete: &complete})
	case "resume", "resume-revoked", "resume-identity-mismatch", "resume-invalid-credentials":
		if request.Action != "resume" || request.Email != "owner@proton.me" || request.Recipient != request.Email ||
			string(request.PKL) != "opaque-native-pickle-imported" || request.ProxyURL != "http://proxy.example.test:8080" ||
			strings.Contains(string(raw), `"password"`) {
			os.Exit(5)
		}
		switch scenario {
		case "resume-revoked":
			write(pklEvent{Event: "error", Stage: "keys", Category: "session_revoked", HTTPStatus: 401, APICode: 10013})
			os.Exit(1)
		case "resume-identity-mismatch":
			write(pklEvent{Event: "error", Stage: "addresses", Category: "identity_mismatch"})
			os.Exit(1)
		case "resume-invalid-credentials":
			write(pklEvent{Event: "error", Stage: "auth", Category: "invalid_credentials", HTTPStatus: 422, APICode: 8002})
			os.Exit(1)
		}
		writeSession("opaque-native-pickle-old")
		write(pklEvent{Event: "result", Complete: &complete})
	case "refresh", "refresh-moved":
		if request.Password != "" || request.Recipient != "owner@proton.me" {
			os.Exit(6)
		}
		if request.Action == "refresh" {
			writeSession("opaque-native-pickle-new")
			write(pklEvent{Event: "result", Complete: &complete})
		} else {
			writeMessage("one")
			if string(request.PKL) == "opaque-native-pickle-old" {
				write(pklEvent{Event: "error", Stage: "list", Category: "session_revoked", HTTPStatus: 401, APICode: 10013})
				os.Exit(1)
			}
			writeMessage("two")
			write(pklEvent{Event: "result", Complete: &complete})
		}
	case "partial":
		writeMessage("one")
		complete = false
		write(pklEvent{Event: "result", Complete: &complete})
	case "missing-result":
		writeSession("opaque-native-pickle-old")
	case "early-eof":
		writeSession("opaque-native-pickle-old")
		_ = os.Stdout.Close()
		time.Sleep(10 * time.Second)
	case "malformed":
		fmt.Fprintln(os.Stdout, `{"event":secret-output-canary`)
	case "unknown-field":
		write(map[string]any{"event": "error", "stage": "auth", "category": "action_required", "raw": "secret-output-canary"})
	case "duplicate-session":
		writeSession("opaque-native-pickle-old")
		writeSession("opaque-native-pickle-old")
	case "result-before-session":
		write(pklEvent{Event: "result", Complete: &complete})
	case "invalid-session":
		write(pklEvent{Event: "session", PKL: []byte("secret-output-canary"), UID: "secret-uid-canary", KeyFingerprint: strings.Repeat("a", 64), Addresses: []AddressKeys{{ID: "address", Email: "other-secret@example.test"}}})
	case "incomplete-login":
		writeSession("opaque-native-pickle-old")
		complete = false
		write(pklEvent{Event: "result", Complete: &complete})
	case "worker-exit":
		writeSession("opaque-native-pickle-old")
		write(pklEvent{Event: "result", Complete: &complete})
		fmt.Fprintln(os.Stderr, "secret-output-canary")
		os.Exit(7)
	case "unknown-category":
		write(pklEvent{Event: "error", Stage: "auth", Category: "secret-output-canary"})
	case "unknown-stage":
		write(pklEvent{Event: "error", Stage: "secret-output-canary", Category: "action_required"})
	case "invalid-error-code":
		write(pklEvent{Event: "error", Stage: "auth", Category: "action_required", HTTPStatus: 700})
	case "action-required":
		write(pklEvent{Event: "error", Stage: "auth", Category: "action_required", HTTPStatus: 422, APICode: 9001})
		os.Exit(1)
	case "trailing-output":
		writeSession("opaque-native-pickle-old")
		write(pklEvent{Event: "result", Complete: &complete})
		fmt.Fprintln(os.Stdout, "secret-output-canary")
	case "oversized":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", maxBridgeLineBytes+1))
	case "stderr":
		fmt.Fprintln(os.Stderr, "password=secret-output-canary token=secret-output-canary")
		os.Exit(7)
	case "hang":
		time.Sleep(time.Minute)
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func TestPKLWorkerNeverInheritsApplicationSecrets(t *testing.T) {
	for _, key := range []string{"MYSQL_DSN", "SESSION_SECRET", "REDIS_PASSWORD", "PROTO_PYTHON_BIN", "PYTHONPATH", "HTTP_PROXY", "HTTPS_PROXY"} {
		t.Setenv(key, "sensitive-environment-canary")
	}
	t.Setenv("TZ", "UTC")
	session, err := pklTestClient("environment").Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "secret"})
	require.NoError(t, err)
	require.True(t, session.ValidFor("owner@proton.me"))
}

func TestPKLLoginUsesOpaqueSessionAndStdin(t *testing.T) {
	client := pklTestClient("login")
	session, err := client.Login(context.Background(), LoginRequest{Email: " OWNER@proton.me ", Password: " secret-fixture-password "})
	require.NoError(t, err)
	require.Equal(t, fixturePKLSession(), session)
	require.True(t, session.ValidFor("owner@proton.me"))
	require.Empty(t, session.AccessToken)
	require.Empty(t, session.RefreshToken)
	require.Empty(t, session.UserKeys)
}

func TestPKLResumeVerifiesImportedSessionWithoutSendingPassword(t *testing.T) {
	for _, password := range []string{"", "must-not-send-import-password"} {
		client := pklTestClient("resume")
		session, err := client.Login(context.Background(), LoginRequest{Email: " OWNER@proton.me ", Password: password,
			ProxyURL: "http://proxy.example.test:8080", PKL: []byte("opaque-native-pickle-imported")})
		require.NoError(t, err)
		require.Equal(t, fixturePKLSession(), session)
	}
}

func TestPKLResumeFailureNeverFallsBackToPasswordLoginOrRefresh(t *testing.T) {
	for _, test := range []struct{ scenario, category, stage string }{
		{"resume-revoked", "session_revoked", "keys"},
		{"resume-identity-mismatch", "identity_mismatch", "addresses"},
		{"resume-invalid-credentials", "protocol", string(bridgeErrorMetadata)},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			client := pklTestClient(test.scenario)
			command := client.command
			calls := 0
			client.command = func(ctx context.Context) *exec.Cmd {
				calls++
				return command(ctx)
			}
			session, err := client.Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "must-not-send-import-password",
				ProxyURL: "http://proxy.example.test:8080", PKL: []byte("opaque-native-pickle-imported")})
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, test.category, failure.Category)
			require.Equal(t, test.stage, failure.Stage)
			require.Equal(t, 1, calls)
			require.Empty(t, session.PKL)
			require.Nil(t, failure.Cause)
			for _, secret := range []string{"must-not-send-import-password", "opaque-native-pickle-imported", "owner@proton.me", "proxy.example.test"} {
				require.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestPKLResumeRejectsOversizedImportBeforeStartingWorker(t *testing.T) {
	client := pklTestClient("resume")
	client.command = func(context.Context) *exec.Cmd {
		t.Fatal("oversized PKL must not start the worker")
		return nil
	}
	_, err := client.Login(context.Background(), LoginRequest{Email: "owner@proton.me", PKL: make([]byte, MaxPKLBytes+1)})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, string(bridgeLimit), failure.Stage)
}

func TestPKLFetchRefreshUsesPersistenceHookAndDeduplicatesReplay(t *testing.T) {
	client := pklTestClient("refresh")
	session := fixturePKLSession()
	var ids []string
	refreshes := 0
	result, err := client.Fetch(context.Background(), &session, FetchRequest{
		Recipient: "owner@proton.me", FullHistory: true,
		RefreshSession: func(ctx context.Context, observed *Session, refresh func(context.Context, *Session) error) error {
			refreshes++
			next := *observed
			require.NoError(t, refresh(ctx, &next))
			require.Equal(t, "opaque-native-pickle-old", string(observed.PKL))
			*observed = next // The real service does this only after successful persistence.
			return nil
		},
		OnMessages: func(batch []Message) error {
			for _, message := range batch {
				ids = append(ids, message.ID)
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Empty(t, result.Messages)
	require.Equal(t, []string{"one", "two"}, ids)
	require.Equal(t, 1, refreshes)
	require.Equal(t, "opaque-native-pickle-new", string(session.PKL))
}

func TestPKLRefreshDoesNotPublishFailedPersistence(t *testing.T) {
	session := fixturePKLSession()
	original := SessionTokenIdentity(session)
	failure := errors.New("save failed")
	_, err := pklTestClient("refresh").Fetch(context.Background(), &session, FetchRequest{Recipient: "owner@proton.me",
		OnSession: func(Session) error { return failure },
	})
	require.ErrorIs(t, err, failure)
	require.Equal(t, original, SessionTokenIdentity(session))
}

func TestPKLRefreshReplayDeduplicatesMessagesMovedBetweenFolders(t *testing.T) {
	session := fixturePKLSession()
	var messages []Message
	result, err := pklTestClient("refresh-moved").Fetch(context.Background(), &session, FetchRequest{
		Recipient: "owner@proton.me", FullHistory: true,
		OnSession: func(Session) error { return nil },
		OnMessages: func(batch []Message) error {
			messages = append(messages, batch...)
			return nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Len(t, messages, 2)
	require.Equal(t, "one", messages[0].ID)
	require.Equal(t, "Inbox", messages[0].Folder)
	require.Equal(t, "two", messages[1].ID)
}

func TestPKLBridgeRejectsIncompleteMalformedAndOversizedOutput(t *testing.T) {
	for _, test := range []struct {
		scenario string
		reason   bridgeFailureReason
	}{
		{"missing-result", bridgeMissingResult},
		{"early-eof", bridgeMissingResult},
		{"malformed", bridgeDecode},
		{"unknown-field", bridgeDecode},
		{"duplicate-session", bridgeEvents},
		{"result-before-session", bridgeEvents},
		{"incomplete-login", bridgeEvents},
		{"invalid-session", bridgeSession},
		{"trailing-output", bridgeEvents},
		{"oversized", bridgeLimit},
		{"stderr", bridgeWorkerExit},
		{"worker-exit", bridgeWorkerExit},
		{"unknown-category", bridgeErrorMetadata},
		{"unknown-stage", bridgeErrorMetadata},
		{"invalid-error-code", bridgeErrorMetadata},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			started := time.Now()
			session, err := pklTestClient(test.scenario).Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "secret-fixture-password"})
			if test.scenario == "early-eof" {
				require.Less(t, time.Since(started), 3*time.Second, "missing completion must stop the live worker promptly")
			}
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, "protocol", failure.Category)
			require.Equal(t, string(test.reason), failure.Stage)
			require.Equal(t, bridgeProtocolFailure(test.reason).SafeMessage, failure.SafeMessage)
			require.True(t, failure.Retryable)
			require.Nil(t, failure.Cause)
			for _, secret := range []string{"secret-output-canary", "secret-uid-canary", "other-secret@example.test", "secret-fixture-password", "opaque-native-pickle-old", "owner@proton.me"} {
				require.NotContains(t, err.Error(), secret)
			}
			require.Empty(t, session.PKL)
		})
	}
	session := fixturePKLSession()
	result, err := pklTestClient("partial").Fetch(context.Background(), &session, FetchRequest{Recipient: "owner@proton.me", FullHistory: true})
	require.Error(t, err)
	require.Empty(t, result.Messages)
}

func TestPKLBridgeFailureReasonsAreFixedAndDistinct(t *testing.T) {
	messages := map[string]bool{}
	for _, reason := range []bridgeFailureReason{bridgeInput, bridgeDecode, bridgeEvents, bridgeMissingResult, bridgeWorkerExit, bridgeRead, bridgeLimit, bridgeSession, bridgeMessage, bridgeErrorMetadata} {
		failure := bridgeProtocolFailure(reason)
		require.Equal(t, "protocol", failure.Category)
		require.Equal(t, string(reason), failure.Stage)
		require.True(t, failure.Retryable)
		require.False(t, messages[failure.SafeMessage], "different reasons need distinguishable persisted messages")
		messages[failure.SafeMessage] = true
	}
	unknown := bridgeProtocolFailure("secret-output-canary")
	require.Equal(t, string(bridgeEvents), unknown.Stage)
	require.NotContains(t, unknown.SafeMessage, "secret-output-canary")
}

func TestPKLBridgePreservesActionRequiredError(t *testing.T) {
	session, err := pklTestClient("action-required").Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "secret-fixture-password"})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "action_required", failure.Category)
	require.Equal(t, "auth", failure.Stage)
	require.Equal(t, 422, failure.HTTPStatus)
	require.Equal(t, 9001, failure.APICode)
	require.False(t, failure.Retryable)
	require.Empty(t, session.PKL)
}

func TestPKLBridgeCancellationReapsWorkerAndPreservesCallbackErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := pklTestClient("hang").Login(ctx, LoginRequest{Email: "owner@proton.me", Password: "secret"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 3*time.Second)
	session := fixturePKLSession()
	failure := errors.New("consumer failed")
	_, err = pklTestClient("partial").Fetch(context.Background(), &session, FetchRequest{Recipient: "owner@proton.me", OnMessages: func([]Message) error { return failure }})
	require.ErrorIs(t, err, failure)
	callbackFailure := &Failure{Category: "session_revoked", SafeMessage: "Consumer failure, not a provider response."}
	_, err = pklTestClient("partial").Fetch(context.Background(), &session, FetchRequest{
		Recipient: "owner@proton.me", OnMessages: func([]Message) error { return callbackFailure },
		RefreshSession: func(context.Context, *Session, func(context.Context, *Session) error) error {
			t.Fatal("consumer errors must not trigger a credential refresh")
			return nil
		},
	})
	require.Same(t, callbackFailure, err)
}

func TestPKLRuntimeMissingNeverMeansBadCredentials(t *testing.T) {
	t.Setenv("PROTO_PYTHON_BIN", t.TempDir()+"/missing-python")
	client := NewPKLClient()
	require.False(t, client.RuntimeAvailable())
	_, err := client.Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "secret"})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "dependency", failure.Category)
}

func TestPKLClientKeepsLegacyV1FetchWithoutPython(t *testing.T) {
	key := testKey(t)
	armored, err := key.Armor()
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "Messages": []any{}}))
	}))
	defer server.Close()
	client := NewPKLClient()
	client.python = "missing-python"
	client.legacy.baseURL = server.URL
	session := Session{Version: 1, UID: "uid", AccessToken: "access", RefreshToken: "refresh", Addresses: []AddressKeys{{ID: "address", Email: "owner@proton.me", PrivateKeys: []string{armored}}}}
	result, err := client.Fetch(context.Background(), &session, FetchRequest{Recipient: "owner@proton.me", FullHistory: true})
	require.NoError(t, err)
	require.True(t, result.Complete)
}

func TestProtoHTTPFailuresOverridePermanentCredentialCodes(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503} {
		for _, code := range []int{8002, 6003, 10013} {
			failure := responseFailure(status, code, "/auth/v4")
			require.NotEqual(t, "invalid_credentials", failure.Category)
			require.NotEqual(t, "session_revoked", failure.Category)
			require.True(t, failure.Retryable)
			require.Equal(t, status, failure.HTTPStatus)
			require.Equal(t, code, failure.APICode)
			require.Equal(t, "auth", failure.Stage)
			bridged := pklFailure(pklEvent{Stage: "auth", Category: "invalid_credentials", HTTPStatus: status, APICode: code}, "login")
			require.Equal(t, failure.Category, bridged.Category)
		}
	}
}
