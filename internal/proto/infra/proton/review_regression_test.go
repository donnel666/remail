package proton

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequiredAPIEnvelopeNeverGuessesBadCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unauthorized-html", "<html>upstream authentication unavailable</html>", 401},
		{"unauthorized-unknown-code", `{"Code":9999}`, 401},
		{"missing-code", `{}`, 200}, {"null-code", `{"Code":null}`, 200},
		{"null-envelope", `null`, 200}, {"invalid-code-type", `{"Code":"1000"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient()
			client.baseURL = server.URL
			transport, err := client.transport("")
			require.NoError(t, err)
			defer transport.client.CloseIdleConnections()
			err = transport.request(context.Background(), nil, http.MethodPost, "/auth/v4", nil, nil)
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.NotEqual(t, "invalid_credentials", failure.Category)
			require.NotEqual(t, "identity_mismatch", failure.Category)
		})
	}
}

func TestFullHistoryRequiresEncryptedBodyButAcceptsDecryptedEmptyBody(t *testing.T) {
	key := testKey(t)
	armor, err := key.Armor()
	require.NoError(t, err)
	encryptedEmpty := encryptTestMessage(t, key, "")
	for _, tc := range []struct {
		name, body string
		present    bool
		null       bool
		valid      bool
	}{
		{name: "missing"}, {name: "null", present: true, null: true},
		{name: "empty-ciphertext", present: true},
		{name: "encrypted-empty-plaintext", present: true, body: encryptedEmpty, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC().Add(-time.Minute).Unix()
			meta := apiMessage{ID: "mail", AddressID: "main", Time: at, ToList: []Address{{Address: "one@example.com"}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := map[string]any{"Code": 1000}
				if r.URL.Path == "/mail/v4/messages" {
					messages := []apiMessage{}
					if r.URL.Query().Get("LabelID") == "0" {
						messages = append(messages, meta)
					}
					response["Messages"] = messages
				} else {
					message := map[string]any{"ID": meta.ID, "AddressID": meta.AddressID, "Time": at, "ToList": meta.ToList, "MIMEType": "text/plain"}
					if tc.present {
						message["Body"] = tc.body
						if tc.null {
							message["Body"] = nil
						}
					}
					response["Message"] = message
				}
				require.NoError(t, json.NewEncoder(w).Encode(response))
			}))
			defer server.Close()
			client := NewClient()
			client.baseURL = server.URL
			session := Session{Version: 1, UID: "uid", AccessToken: "access", RefreshToken: "refresh", Addresses: []AddressKeys{{ID: "main", Email: "one@example.com", PrivateKeys: []string{armor}}}}
			result, err := client.Fetch(context.Background(), &session, FetchRequest{Recipient: "one@example.com", FullHistory: true})
			if tc.valid {
				require.NoError(t, err)
				require.True(t, result.Complete)
				require.Len(t, result.Messages, 1)
				require.Empty(t, result.Messages[0].Body)
			} else {
				require.Error(t, err)
				require.False(t, result.Complete)
			}
		})
	}
}

func TestInlineRFC822BodyIsParsedButAttachedRFC822IsSkipped(t *testing.T) {
	inner := "From: sender@example.com\r\nContent-Type: text/plain\r\n\r\nYour code is 123456\r\n"
	body, err := readableBody([]byte("Content-Type: message/rfc822\r\n\r\n"+inner), "message/rfc822")
	require.NoError(t, err)
	require.Contains(t, body, "123456")
	body, err = readableBody([]byte("Content-Type: message/rfc822\r\nContent-Disposition: attachment; filename=forward.eml\r\n\r\n"+inner), "message/rfc822")
	require.NoError(t, err)
	require.Empty(t, body)
}
