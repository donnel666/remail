package proton

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFetchRefreshHookCoversExpiredMetadataAndDetail(t *testing.T) {
	key := testKey(t)
	armor, err := key.Armor()
	require.NoError(t, err)
	body := encryptTestMessage(t, key, "123456")
	for _, trigger := range []string{"expired", "metadata-401", "detail-401"} {
		for _, fullHistory := range []bool{false, true} {
			name := trigger + "/bounded"
			if fullHistory {
				name = trigger + "/history"
			}
			t.Run(name, func(t *testing.T) {
				var hooks, refreshes, rejected atomic.Int32
				var inHook, saved atomic.Bool
				at := time.Now().UTC().Add(-time.Minute).Unix()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/auth/v4/refresh" {
						require.True(t, inHook.Load(), "the store hook must enclose the actual token rotation")
						refreshes.Add(1)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "UID": "uid", "AccessToken": "new-access", "RefreshToken": "new-refresh"}))
						return
					}
					old := r.Header.Get("Authorization") == "Bearer old-access"
					if trigger == "expired" {
						require.False(t, old, "an expired session must invoke the hook before the first GET")
					}
					if old && (trigger == "metadata-401" || strings.HasPrefix(r.URL.Path, "/mail/v4/messages/")) {
						rejected.Add(1)
						w.WriteHeader(http.StatusUnauthorized)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 10013}))
						return
					}
					if !old {
						require.True(t, saved.Load(), "GET must wait for the hook's persistence step")
					}
					message := apiMessage{ID: "mail", AddressID: "main", Time: at, Body: &body, MIMEType: "text/plain", ToList: []Address{{Address: "one@example.com"}}}
					response := map[string]any{"Code": 1000, "Message": message}
					if r.URL.Path == "/mail/v4/messages" {
						messages := []apiMessage{}
						if r.URL.Query().Get("LabelID") == "0" {
							messages = append(messages, message)
						}
						response = map[string]any{"Code": 1000, "Messages": messages}
					}
					require.NoError(t, json.NewEncoder(w).Encode(response))
				}))
				defer server.Close()
				client := NewClient()
				client.baseURL = server.URL
				session := Session{Version: 1, UID: "uid", AccessToken: "old-access", RefreshToken: "old-refresh", Addresses: []AddressKeys{{ID: "main", Email: "one@example.com", PrivateKeys: []string{armor}}}}
				if trigger == "expired" {
					session.ExpiresAt = time.Now().Add(-time.Minute)
				}
				result, err := client.Fetch(context.Background(), &session, FetchRequest{
					Recipient: "one@example.com", FullHistory: fullHistory,
					OnSession: func(Session) error {
						t.Error("legacy callback must not run when the store hook is configured")
						return nil
					},
					RefreshSession: func(ctx context.Context, session *Session, rotate func(context.Context, *Session) error) error {
						hooks.Add(1)
						inHook.Store(true)
						defer inHook.Store(false)
						if err := rotate(ctx, session); err != nil {
							return err
						}
						saved.Store(true)
						return nil
					},
				})
				require.NoError(t, err)
				require.True(t, result.Complete)
				require.Len(t, result.Messages, 1)
				require.Equal(t, "123456", result.Messages[0].Body)
				require.EqualValues(t, 1, hooks.Load())
				require.EqualValues(t, 1, refreshes.Load())
				if trigger != "expired" {
					require.EqualValues(t, 1, rejected.Load())
				}
			})
		}
	}
}

func TestConcurrentFetchHooksCanReuseTheLatestPersistedSession(t *testing.T) {
	key := testKey(t)
	armor, err := key.Armor()
	require.NoError(t, err)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v4/refresh" {
			refreshes.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "UID": "uid", "AccessToken": "new-access", "RefreshToken": "new-refresh"}))
			return
		}
		if r.Header.Get("Authorization") == "Bearer old-access" {
			w.WriteHeader(http.StatusUnauthorized)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 10013}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "Messages": []apiMessage{}}))
	}))
	defer server.Close()
	client := NewClient()
	client.baseURL = server.URL
	initial := Session{Version: 1, UID: "uid", AccessToken: "old-access", RefreshToken: "old-refresh", Addresses: []AddressKeys{{ID: "main", Email: "one@example.com", PrivateKeys: []string{armor}}}}
	persisted := initial
	var lease sync.Mutex
	hook := func(ctx context.Context, session *Session, rotate func(context.Context, *Session) error) error {
		lease.Lock()
		defer lease.Unlock()
		if persisted.RefreshToken != session.RefreshToken {
			*session = persisted
			return nil
		}
		if err := rotate(ctx, session); err != nil {
			return err
		}
		persisted = *session
		return nil
	}
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			session := initial
			_, err := client.Fetch(context.Background(), &session, FetchRequest{Recipient: "one@example.com", RefreshSession: hook})
			errors <- err
		}()
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errors)
	}
	require.EqualValues(t, 1, refreshes.Load())
}

func TestFetchRefreshHookFailureStopsAnd401DoesNotRecurse(t *testing.T) {
	key := testKey(t)
	armor, err := key.Armor()
	require.NoError(t, err)
	for _, failStore := range []bool{false, true} {
		var refreshes, gets atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/v4/refresh" {
				refreshes.Add(1)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "UID": "uid", "AccessToken": "new-access", "RefreshToken": "new-refresh"}))
				return
			}
			gets.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 10013}))
		}))
		client := NewClient()
		client.baseURL = server.URL
		session := Session{Version: 1, UID: "uid", AccessToken: "old-access", RefreshToken: "old-refresh", Addresses: []AddressKeys{{ID: "main", Email: "one@example.com", PrivateKeys: []string{armor}}}}
		storeError := errors.New("test session store failure")
		result, err := client.Fetch(context.Background(), &session, FetchRequest{Recipient: "one@example.com", RefreshSession: func(ctx context.Context, session *Session, rotate func(context.Context, *Session) error) error {
			if err := rotate(ctx, session); err != nil {
				return err
			}
			if failStore {
				return storeError
			}
			return nil
		}})
		require.Error(t, err)
		require.False(t, result.Complete)
		require.EqualValues(t, 1, refreshes.Load())
		if failStore {
			require.ErrorIs(t, err, storeError)
			require.EqualValues(t, 1, gets.Load())
		} else {
			require.EqualValues(t, 2, gets.Load())
		}
		server.Close()
	}
}

func TestRefreshWithoutPersistenceDoesNotConsumeToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewClient()
	client.baseURL = server.URL
	transport, err := client.transport("")
	require.NoError(t, err)
	defer transport.client.CloseIdleConnections()
	err = transport.refresh(context.Background(), &Session{UID: "uid", AccessToken: "access", RefreshToken: "refresh"}, nil)
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "session_persistence", failure.Category)
	require.Zero(t, calls.Load())
}
