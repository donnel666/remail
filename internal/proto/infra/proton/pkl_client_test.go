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
	var request pklRequest
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
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
			*observed = next // The real service does this only after encrypted persistence.
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
	for _, scenario := range []string{"missing-result", "trailing-output", "oversized", "stderr"} {
		t.Run(scenario, func(t *testing.T) {
			session, err := pklTestClient(scenario).Login(context.Background(), LoginRequest{Email: "owner@proton.me", Password: "secret-fixture-password"})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-output-canary")
			require.Empty(t, session.PKL)
		})
	}
	session := fixturePKLSession()
	result, err := pklTestClient("partial").Fetch(context.Background(), &session, FetchRequest{Recipient: "owner@proton.me", FullHistory: true})
	require.Error(t, err)
	require.Empty(t, result.Messages)
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
