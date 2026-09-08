package proton

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-srp"
	"github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/stretchr/testify/require"
)

// Public signed-modulus test vector from ProtonMail/go-srp (MIT, Proton
// Technologies AG). It contains no account or session material.
const signedTestModulus = `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA256

W2z5HBi8RvsfYzZTS7qBaUxxPhsfHJFZpu3Kd6s1JafNrCCH9rfvPLrfuqocxWPgWDH2R8neK7PkNvjxto9TStuY5z7jAzWRvFWN9cQhAKkdWgy0JY6ywVn22+HFpF4cYesHrqFIKUPDMSSIlWjBVmEJZ/MusD44ZT29xcPrOqeZvwtCffKtGAIjLYPZIEbZKnDM1Dm3q2K/xS5h+xdhjnndhsrkwm9U9oyA2wxzSXFL+pdfj2fOdRwuR5nW0J2NFrq3kJjkRmpO/Genq1UW+TEknIWAb6VzJJJA244K/H8cnSx2+nSNZO3bbo6Ys228ruV9A8m6DhxmS+bihN3ttQ==
-----BEGIN PGP SIGNATURE-----
Version: ProtonMail
Comment: https://protonmail.com

wl4EARYIABAFAlwB1j0JEDUFhcTpUY8mAAD8CgEAnsFnF4cF0uSHKkXa1GIa
GO86yMV4zDZEZcDSJo0fgr8A/AlupGN9EdHlsrZLmTA1vhIx+rOgxdEff28N
kvNM7qIK
=q6vu
-----END PGP SIGNATURE-----`

func testKey(t *testing.T) *crypto.Key {
	t.Helper()
	key, err := crypto.PGP().KeyGeneration().AddUserId("Proto test", "one@example.com").
		OverrideProfileAlgorithm(crypto.KeyGenerationCurve25519Legacy).New().GenerateKey()
	require.NoError(t, err)
	t.Cleanup(func() { key.ClearPrivateParams() })
	return key
}

func encryptTestMessage(t *testing.T, key *crypto.Key, body string) string {
	t.Helper()
	encryptor, err := crypto.PGP().Encryption().Recipient(key).New()
	require.NoError(t, err)
	encrypted, err := encryptor.Encrypt([]byte(body))
	require.NoError(t, err)
	armored, err := encrypted.Armor()
	require.NoError(t, err)
	return armored
}

func TestLoginSRPUnlocksReusableKeysAndChecksIdentity(t *testing.T) {
	const password = "local-fixture-password"
	const addressPassword = "local-address-key-password"
	srpSalt := []byte("0123456789")
	mailSalt := []byte("0123456789abcdef")
	verifierAuth, err := srp.NewAuthForVerifier([]byte(password), signedTestModulus, srpSalt)
	require.NoError(t, err)
	verifier, err := verifierAuth.GenerateVerifier(2048)
	require.NoError(t, err)
	serverSRP, err := srp.NewServer(verifierAuth.Modulus, verifier, 2048)
	require.NoError(t, err)
	challenge, err := serverSRP.GenerateChallenge()
	require.NoError(t, err)
	hashed, err := srp.MailboxPassword([]byte(password), mailSalt)
	require.NoError(t, err)
	userKey, addressKey := testKey(t), testKey(t)
	lockedUser, err := crypto.PGP().LockKey(userKey, hashed[29:])
	require.NoError(t, err)
	userArmor, err := lockedUser.Armor()
	require.NoError(t, err)
	lockedAddress, err := crypto.PGP().LockKey(addressKey, []byte(addressPassword))
	require.NoError(t, err)
	addressArmor, err := lockedAddress.Armor()
	require.NoError(t, err)
	signer, err := crypto.PGP().Sign().SigningKey(userKey).Detached().New()
	require.NoError(t, err)
	signature, err := signer.Sign([]byte(addressPassword), crypto.Armor)
	require.NoError(t, err)
	addressRecord := apiKey{PrivateKey: addressArmor, Token: encryptTestMessage(t, userKey, addressPassword), Signature: string(signature), Active: json.RawMessage("1")}
	var twoFA atomic.Bool
	var badProof atomic.Bool
	var addressScenario atomic.Int32
	addressEnabled := 1
	addressDisabled := 0
	addressDeleting := 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, appVersion, r.Header.Get("x-pm-appversion"))
		var result any
		switch r.URL.Path {
		case "/auth/v4/info":
			result = map[string]any{"Code": 1000, "Version": 4, "Modulus": signedTestModulus,
				"Salt": base64.StdEncoding.EncodeToString(srpSalt), "ServerEphemeral": base64.StdEncoding.EncodeToString(challenge), "SRPSession": "srp-session"}
		case "/auth/v4":
			var request map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.NotContains(t, request, "Password")
			ephemeral, err := base64.StdEncoding.DecodeString(request["ClientEphemeral"])
			require.NoError(t, err)
			proof, err := base64.StdEncoding.DecodeString(request["ClientProof"])
			require.NoError(t, err)
			serverProof, err := serverSRP.VerifyProofs(ephemeral, proof)
			require.NoError(t, err)
			if badProof.Load() {
				serverProof = []byte("bad-proof")
			}
			result = map[string]any{"Code": 1000, "UID": "uid", "AccessToken": "access", "RefreshToken": "refresh",
				"ServerProof": base64.StdEncoding.EncodeToString(serverProof), "PasswordMode": 1, "2FA": map[string]any{"Enabled": twoFA.Load()}}
		case "/core/v4/users":
			require.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			result = map[string]any{"Code": 1000, "User": map[string]any{"Keys": []apiKey{{ID: "user-key", PrivateKey: userArmor, Active: json.RawMessage("1")}}}}
		case "/core/v4/keys/salts":
			result = map[string]any{"Code": 1000, "KeySalts": []map[string]string{{"ID": "user-key", "KeySalt": base64.StdEncoding.EncodeToString(mailSalt)}}}
		case "/core/v4/addresses":
			require.Empty(t, r.URL.RawQuery, "the address endpoint returns one complete array")
			address := apiAddress{ID: "address-one", Email: "one@example.com", Status: &addressEnabled, Receive: json.RawMessage("1"), Keys: []apiKey{addressRecord}}
			response := map[string]any{"Code": 1000, "Addresses": []apiAddress{address}}
			switch addressScenario.Load() {
			case 1:
				delete(response, "Addresses")
			case 2:
				response["Addresses"] = nil
			case 3:
				response["Addresses"] = []apiAddress{address, address}
			case 4:
				response["Addresses"] = []map[string]any{{"ID": address.ID, "Email": address.Email, "Keys": address.Keys}}
			case 5:
				address.Email = "not-an-email"
				response["Addresses"] = []apiAddress{address}
			case 6:
				addresses := []apiAddress{address}
				for i := 0; i < 150; i++ {
					status := &addressDisabled
					if i == 149 {
						status = &addressDeleting
					}
					addresses = append(addresses, apiAddress{ID: fmt.Sprintf("disabled-%d", i), Email: fmt.Sprintf("disabled-%d@example.com", i), Status: status})
				}
				response["Addresses"] = addresses
			case 7:
				address.Status = &addressDisabled
				response["Addresses"] = []apiAddress{address}
			}
			result = response
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(result))
	}))
	defer server.Close()
	client := NewClient()
	client.baseURL = server.URL
	session, err := client.Login(context.Background(), LoginRequest{Email: "ONE@example.com", Password: password})
	require.NoError(t, err)
	require.Len(t, session.UserKeys, 1)
	require.Len(t, session.Addresses, 1)
	ring, err := privateKeyRing(session.Addresses[0].PrivateKeys)
	require.NoError(t, err)
	defer ring.ClearPrivateParams()
	plain, err := decryptPGP(encryptTestMessage(t, addressKey, "123456"), ring)
	require.NoError(t, err)
	require.Equal(t, "123456", string(plain))
	data, err := json.Marshal(session)
	require.NoError(t, err)
	require.NotContains(t, string(data), password)

	_, err = client.Login(context.Background(), LoginRequest{Email: "other@example.com", Password: password})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "identity_mismatch", failure.Category)
	for _, tc := range []struct {
		name     string
		scenario int32
		category string
	}{
		{"missing-address-list", 1, "protocol"}, {"null-address-list", 2, "protocol"},
		{"duplicate-address-id", 3, "protocol"}, {"missing-address-status", 4, "protocol"},
		{"invalid-address-email", 5, "protocol"}, {"more-than-150-addresses", 6, ""},
		{"owned-disabled-address-needs-action", 7, "action_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addressScenario.Store(tc.scenario)
			result, err := client.Login(context.Background(), LoginRequest{Email: "one@example.com", Password: password})
			if tc.category == "" {
				require.NoError(t, err)
				require.Len(t, result.Addresses, 1)
			} else {
				var failure *Failure
				require.ErrorAs(t, err, &failure)
				require.Equal(t, tc.category, failure.Category)
			}
		})
	}
	addressScenario.Store(0)
	twoFA.Store(true)
	_, err = client.Login(context.Background(), LoginRequest{Email: "one@example.com", Password: password})
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "action_required", failure.Category)
	badProof.Store(true)
	_, err = client.Login(context.Background(), LoginRequest{Email: "one@example.com", Password: password})
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "protocol", failure.Category)

	userRing, err := privateKeyRing(session.UserKeys)
	require.NoError(t, err)
	defer userRing.ClearPrivateParams()
	addressRecord.Signature = ""
	_, err = unlockAddressKey(addressRecord, userRing, map[string][]byte{"key": []byte(addressPassword)})
	require.Error(t, err, "an unsigned token cannot fall back to an otherwise correct password")
	wrongSigner, err := crypto.PGP().Sign().SigningKey(addressKey).Detached().New()
	require.NoError(t, err)
	wrongSignature, err := wrongSigner.Sign([]byte(addressPassword), crypto.Armor)
	require.NoError(t, err)
	addressRecord.Signature = string(wrongSignature)
	_, err = unlockAddressKey(addressRecord, userRing, nil)
	require.Error(t, err, "address key tokens must be signed by an unlocked user key")
}

func TestFetchStreamsBothFoldersRefreshesBeforeContinuingAndFailsClosed(t *testing.T) {
	key := testKey(t)
	armored, err := key.Armor()
	require.NoError(t, err)
	ciphertext := encryptTestMessage(t, key, "<p>Your code is 123456</p>")
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	var saved atomic.Bool
	var badMessage atomic.Bool
	var pageReads atomic.Int32
	var messageReads atomic.Int32
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v4/refresh" {
			refreshes.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "UID": "uid", "AccessToken": "new-access", "RefreshToken": "new-refresh"}))
			return
		}
		if r.Header.Get("Authorization") == "Bearer old-access" {
			w.WriteHeader(http.StatusUnauthorized)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 401, "Error": "upstream-secret-must-not-leak"}))
			return
		}
		require.True(t, saved.Load(), "refreshed session must be durable before the next request")
		require.Equal(t, "Bearer new-access", r.Header.Get("Authorization"))
		if r.URL.Path == "/mail/v4/messages" {
			pageReads.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("Page"))
			label := r.URL.Query().Get("LabelID")
			require.Equal(t, "address-one", r.URL.Query().Get("AddressID"))
			messages := []apiMessage{}
			if label == "0" && page == 0 {
				// Explicit aliases or other recipients cannot become the main mailbox
				// merely because the server associates them with the same address ID.
				messages = append(messages, apiMessage{ID: "alias", AddressID: "address-one", Time: now.Unix(), ToList: []Address{{Address: "one+tag@example.com"}}})
				for i := 0; i < 149; i++ {
					messages = append(messages, apiMessage{ID: fmt.Sprintf("mail-%03d", i), AddressID: "address-one", Time: now.Add(-time.Duration(i) * time.Second).Unix(), ToList: []Address{{Address: "one@example.com"}}})
				}
			} else if label == "0" && page == 1 {
				messages = append(messages, apiMessage{ID: "mail-last", AddressID: "address-one", Time: now.Add(-150 * time.Second).Unix(), ToList: []Address{{Address: "one@example.com"}}})
			} else if label == "4" && page == 0 {
				messages = append(messages, apiMessage{ID: "spam", AddressID: "address-one", Time: now.Unix(), ToList: []Address{{Address: "one@example.com"}}})
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "Messages": messages}))
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/mail/v4/messages/")
		require.NotEqual(t, "alias", id)
		messageReads.Add(1)
		body := ciphertext
		if badMessage.Load() && id == "mail-000" {
			body = "not-a-pgp-message"
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Code": 1000, "Message": apiMessage{
			ID: id, AddressID: "address-one", Time: now.Unix(), Subject: "Code", Body: &body, MIMEType: "text/html",
			ToList: []Address{{Address: "one@example.com"}, {Address: "other@example.com"}},
		}}))
	}))
	defer server.Close()
	client := NewClient()
	client.baseURL = server.URL
	newSession := func() Session {
		return Session{Version: 1, UID: "uid", AccessToken: "old-access", RefreshToken: "old-refresh",
			Addresses: []AddressKeys{{ID: "address-one", Email: "one@example.com", PrivateKeys: []string{armored}}}}
	}
	session := newSession()
	total := 0
	request := FetchRequest{Recipient: "one@example.com", FullHistory: true, MaxMessages: 1,
		KnownMessageIDs: []string{"provider:proton:inbox:mail-000"},
		OnSession: func(updated Session) error {
			require.Equal(t, "new-refresh", updated.RefreshToken)
			saved.Store(true)
			return nil
		},
		OnMessages: func(messages []Message) error {
			total += len(messages)
			for _, message := range messages {
				require.Contains(t, message.Body, "123456")
				require.Equal(t, 2, message.OriginalToCount)
			}
			return nil
		},
	}
	result, err := client.Fetch(context.Background(), &session, request)
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Empty(t, result.Messages, "streaming fetch must not retain the entire mailbox")
	require.Equal(t, 151, total)
	require.EqualValues(t, 3, pageReads.Load())
	require.EqualValues(t, 151, messageReads.Load())
	require.EqualValues(t, 1, refreshes.Load())

	badMessage.Store(true)
	result, err = client.Fetch(context.Background(), &session, request)
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "decryption", failure.Category)
	require.False(t, result.Complete)
	badMessage.Store(false)

	request.FullHistory, request.OnMessages = false, nil
	request.KnownMessageIDs = nil
	result, err = client.Fetch(context.Background(), &session, request)
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	require.Equal(t, "mail-000", result.Messages[0].ID, "an alias must not consume the main-mailbox limit")

	before := messageReads.Load()
	request.KnownMessageIDs = []string{"provider:proton:inbox:mail-000", "provider:proton:junk:spam"}
	result, err = client.Fetch(context.Background(), &session, request)
	require.NoError(t, err)
	require.Empty(t, result.Messages)
	require.Equal(t, before, messageReads.Load(), "known boundaries must not read cached messages again")

	session = newSession()
	request.OnSession = func(Session) error { return errors.New("database unavailable") }
	pagesBefore := pageReads.Load()
	result, err = client.Fetch(context.Background(), &session, request)
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "session_persistence", failure.Category)
	require.False(t, result.Complete)
	require.Equal(t, pagesBefore, pageReads.Load(), "a failed persistence callback must stop the retry")
}

func TestExactRecipientsMIMEAndSafeFailures(t *testing.T) {
	address := AddressKeys{ID: "one", Email: "one@example.com"}
	require.False(t, messageForAddress(apiMessage{AddressID: "one", ToList: []Address{{Address: "one+tag@example.com"}}}, address))
	require.False(t, messageForAddress(apiMessage{AddressID: "one", ToList: []Address{{Address: "o.ne@example.com"}}}, address))
	require.False(t, messageForAddress(apiMessage{AddressID: "one", ToList: []Address{{Address: "other@example.com"}}}, address))
	require.True(t, messageForAddress(apiMessage{AddressID: "one"}, address))
	require.True(t, messageForAddress(apiMessage{CCList: []Address{{Address: "ONE@example.com"}}}, address))
	require.True(t, messageKnown(apiMessage{ID: "id", ExternalID: "<Seen@Example.com>"}, "Inbox", map[string]bool{"internet:seen@example.com": true}))
	content := "Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: text/plain\r\n\r\nplain 123456\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=file\r\nContent-Disposition: attachment; filename=attached.eml\r\n\r\n" +
		"--file\r\nContent-Type: text/html\r\n\r\n<h1>attachment 999999</h1>\r\n--file--\r\n" +
		"--outer\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte("<b>body 123456</b>")) + "\r\n--outer--\r\n"
	body, err := readableBody([]byte(content), "multipart/mixed")
	require.NoError(t, err)
	require.Equal(t, "<b>body 123456</b>", body)
	require.NotContains(t, body, "999999")
	for _, tc := range []struct {
		status, code   int
		path, category string
	}{
		{401, 10013, "/auth/v4/refresh", "session_revoked"},
		{401, 10013, "/auth/v4", "session_revoked"},
		{401, 9999, "/auth/v4", "request"},
		{422, 9001, "/auth/v4", "action_required"},
		{429, 2028, "/auth/v4", "rate_limited"},
		{422, 8002, "/auth/v4", "invalid_credentials"},
		{422, 6003, "/auth/v4/info", "invalid_credentials"},
		{422, 8002, "/auth/v4/info", "request"},
		{422, 8002, "/auth/v4/refresh", "session_revoked"},
		{422, 6003, "/auth/v4/refresh", "session_revoked"},
		{422, 8002, "/mail/v4/messages", "request"},
		{422, 6003, "/mail/v4/messages/id", "request"},
	} {
		failure := responseFailure(tc.status, tc.code, tc.path)
		require.Equal(t, tc.category, failure.Category, "path %s code %d", tc.path, tc.code)
		require.NotContains(t, failure.Error(), "upstream-secret")
	}
}

func TestRefreshInvalidResponsesPreserveSessionWithoutPermanentCredentialFailure(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"changed-uid", `{"Code":1000,"UID":"another-user","AccessToken":"new-access","RefreshToken":"new-refresh"}`},
		{"missing-refresh-token", `{"Code":1000,"UID":"uid","AccessToken":"new-access"}`},
		{"invalid-json", `{"Code":1000,"AccessToken":"upstream-secret"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/auth/v4/refresh", r.URL.Path)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			}))
			defer server.Close()
			client := NewClient()
			client.baseURL = server.URL
			transport, err := client.transport("")
			require.NoError(t, err)
			defer transport.client.CloseIdleConnections()
			session := Session{Version: 1, UID: "uid", AccessToken: "old-access", RefreshToken: "old-refresh"}
			before := session
			saved := false
			err = transport.refresh(context.Background(), &session, func(Session) error { saved = true; return nil })
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, "protocol", failure.Category)
			require.True(t, failure.Retryable)
			require.NotContains(t, failure.Error(), "upstream-secret")
			require.Equal(t, before, session)
			require.False(t, saved)
		})
	}
}
