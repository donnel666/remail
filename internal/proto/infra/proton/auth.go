package proton

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-srp"
	http "github.com/bogdanfinn/fhttp"
)

type authResponse struct {
	UID          string
	AccessToken  string
	RefreshToken string
	ServerProof  string
	ExpiresIn    int64
	PasswordMode int
	TwoFactor    json.RawMessage
	TwoFA        struct {
		Enabled json.RawMessage
	} `json:"2FA"`
}

type apiKey struct {
	ID         string
	PrivateKey string
	Token      string
	Signature  string
	Active     json.RawMessage
}

type apiAddress struct {
	ID      string
	Email   string
	Status  *int
	Receive json.RawMessage
	Keys    []apiKey
}

func (c *Client) Login(ctx context.Context, req LoginRequest) (Session, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		return Session{}, &Failure{Category: "invalid_credentials", SafeMessage: "Proto email and password are required."}
	}
	t, err := c.transport(req.ProxyURL)
	if err != nil {
		return Session{}, err
	}
	defer t.client.CloseIdleConnections()
	var info struct {
		Version                                    int
		Salt, Modulus, ServerEphemeral, SRPSession string
	}
	if err := t.request(ctx, nil, http.MethodPost, "/auth/v4/info", map[string]string{"Username": email}, &info); err != nil {
		return Session{}, err
	}
	// go-srp verifies the pinned modulus signature; reject malformed cleartext
	// armor first because the library expects a decoded signed-message block.
	block, _ := clearsign.Decode([]byte(info.Modulus))
	if block == nil || block.ArmoredSignature == nil || info.SRPSession == "" {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto returned invalid authentication parameters."}
	}
	auth, err := srp.NewAuth(info.Version, email, []byte(req.Password), info.Salt, info.Modulus, info.ServerEphemeral)
	if err != nil {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto authentication parameters could not be verified.", Cause: err}
	}
	defer clear(auth.HashedPassword)
	proofs, err := auth.GenerateProofs(2048)
	if err != nil {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto authentication proof could not be prepared.", Cause: err}
	}
	var response authResponse
	if err := t.request(ctx, nil, http.MethodPost, "/auth/v4", map[string]any{
		"Username": email, "ClientEphemeral": base64.StdEncoding.EncodeToString(proofs.ClientEphemeral),
		"ClientProof": base64.StdEncoding.EncodeToString(proofs.ClientProof), "SRPSession": info.SRPSession,
	}, &response); err != nil {
		return Session{}, err
	}
	serverProof, err := base64.StdEncoding.DecodeString(response.ServerProof)
	if err != nil || subtle.ConstantTimeCompare(serverProof, proofs.ExpectedServerProof) != 1 {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto server authentication proof did not match."}
	}
	if flagEnabled(response.TwoFactor) || flagEnabled(response.TwoFA.Enabled) || response.PasswordMode == 2 {
		return Session{}, &Failure{Category: "action_required", SafeMessage: "Proto requires two-factor authentication or a separate mailbox password."}
	}
	if response.UID == "" || response.AccessToken == "" || response.RefreshToken == "" {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto returned an incomplete authenticated session."}
	}
	session := Session{Version: 1, UID: response.UID, AccessToken: response.AccessToken,
		RefreshToken: response.RefreshToken, ExpiresAt: tokenExpiry(response.ExpiresIn)}
	var user struct {
		User struct{ Keys []apiKey }
	}
	if err := t.request(ctx, &session, http.MethodGet, "/core/v4/users", nil, &user); err != nil {
		return Session{}, err
	}
	var salts struct {
		KeySalts []struct{ ID, KeySalt string }
	}
	if err := t.request(ctx, &session, http.MethodGet, "/core/v4/keys/salts", nil, &salts); err != nil {
		return Session{}, err
	}
	keyPasswords := make(map[string][]byte)
	defer func() {
		for _, password := range keyPasswords {
			clear(password)
		}
	}()
	for _, salt := range salts.KeySalts {
		if salt.KeySalt == "" {
			keyPasswords[salt.ID] = []byte(req.Password)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(salt.KeySalt)
		if err != nil || len(decoded) != 16 {
			return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto returned an invalid mailbox key salt."}
		}
		hashed, err := srp.MailboxPassword([]byte(req.Password), decoded)
		if err != nil || len(hashed) != 60 {
			return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto mailbox key password could not be derived.", Cause: err}
		}
		keyPasswords[salt.ID] = append([]byte(nil), hashed[29:]...)
		clear(hashed)
	}
	for _, key := range user.User.Keys {
		password, found := keyPasswords[key.ID]
		if !found || (len(key.Active) > 0 && !flagEnabled(key.Active)) {
			continue
		}
		unlocked, err := unlockPrivateKey(key.PrivateKey, password)
		if err == nil {
			session.UserKeys = append(session.UserKeys, unlocked)
		}
	}
	if len(session.UserKeys) == 0 {
		return Session{}, &Failure{Category: "action_required", SafeMessage: "Proto mailbox keys could not be unlocked; account recovery may be required."}
	}
	userKeys, err := privateKeyRing(session.UserKeys)
	if err != nil {
		return Session{}, err
	}
	defer userKeys.ClearPrivateParams()
	var addresses struct{ Addresses []apiAddress }
	if err := t.request(ctx, &session, http.MethodGet, "/core/v4/addresses", nil, &addresses); err != nil {
		return Session{}, err
	}
	if addresses.Addresses == nil {
		return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto returned no address list."}
	}
	seen := make(map[string]bool)
	identityFound := false
	for _, address := range addresses.Addresses {
		address.Email = strings.ToLower(strings.TrimSpace(address.Email))
		parsed, err := mail.ParseAddress(address.Email)
		if strings.TrimSpace(address.ID) == "" || seen[address.ID] || err != nil || parsed.Address != address.Email || address.Status == nil || *address.Status < 0 || *address.Status > 2 {
			return Session{}, &Failure{Category: "protocol", SafeMessage: "Proto returned an incomplete or repeated address."}
		}
		seen[address.ID] = true
		identityFound = identityFound || address.Email == email
		if *address.Status != 1 || (len(address.Receive) > 0 && !flagEnabled(address.Receive)) {
			continue
		}
		keys := AddressKeys{ID: address.ID, Email: address.Email}
		for _, key := range address.Keys {
			if len(key.Active) > 0 && !flagEnabled(key.Active) {
				continue
			}
			unlocked, err := unlockAddressKey(key, userKeys, keyPasswords)
			if err == nil {
				keys.PrivateKeys = append(keys.PrivateKeys, unlocked)
			}
		}
		if len(keys.PrivateKeys) > 0 {
			session.Addresses = append(session.Addresses, keys)
		}
	}
	if !identityFound {
		return Session{}, &Failure{Category: "identity_mismatch", SafeMessage: "The authenticated Proto account does not own the imported receiving address."}
	}
	for _, address := range session.Addresses {
		if address.Email == email {
			return session, nil
		}
	}
	return Session{}, &Failure{Category: "action_required", SafeMessage: "The imported Proto address has no usable decryption keys."}
}

func flagEnabled(raw json.RawMessage) bool {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number int
	return json.Unmarshal(raw, &number) == nil && number != 0
}
