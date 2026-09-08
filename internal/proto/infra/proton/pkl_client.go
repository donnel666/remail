package proton

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed pkl_bridge.py
var pklBridgeSource string

const maxBridgeLineBytes = 32 << 20
const maxBridgeOutputBytes = 128 << 20

// PKLClient keeps Python's native session opaque. Passwords and session bytes
// travel only through stdin/stdout pipes, never command arguments or logs.
type PKLClient struct {
	python  string
	legacy  *Client
	command func(context.Context) *exec.Cmd
}

func NewPKLClient() *PKLClient {
	python := strings.TrimSpace(os.Getenv("PROTO_PYTHON_BIN"))
	if python == "" {
		python = "python3"
	}
	return &PKLClient{python: python, legacy: NewClient()}
}

// RuntimeAvailable is a local executable check; it never logs in or sends HTTP.
func (c *PKLClient) RuntimeAvailable() bool {
	if c == nil || c.python == "" {
		return false
	}
	_, err := exec.LookPath(c.python)
	return err == nil
}

type pklRequest struct {
	Action          string    `json:"action"`
	Email           string    `json:"email,omitempty"`
	Password        string    `json:"password,omitempty"`
	ProxyURL        string    `json:"proxy_url,omitempty"`
	PKL             []byte    `json:"pkl,omitempty"`
	Recipient       string    `json:"recipient,omitempty"`
	SinceAt         time.Time `json:"since_at,omitempty"`
	UntilAt         time.Time `json:"until_at,omitempty"`
	MaxMessages     int       `json:"max_messages,omitempty"`
	FullHistory     bool      `json:"full_history,omitempty"`
	KnownMessageIDs []string  `json:"known_message_ids,omitempty"`
}

type pklEvent struct {
	Event          string        `json:"event"`
	PKL            []byte        `json:"pkl"`
	UID            string        `json:"uid"`
	Addresses      []AddressKeys `json:"addresses"`
	KeyFingerprint string        `json:"key_fingerprint"`
	Messages       []Message     `json:"messages"`
	Complete       *bool         `json:"complete"`
	Stage          string        `json:"stage"`
	Category       string        `json:"category"`
	HTTPStatus     int           `json:"http_status"`
	APICode        int           `json:"api_code"`
	Retryable      bool          `json:"retryable"`
	ProxyFailure   bool          `json:"proxy_failure"`
}

func bridgeProtocolFailure() *Failure {
	return &Failure{Stage: "protocol", Category: "protocol", SafeMessage: "Proto session bridge returned an invalid or oversized result.", Retryable: true}
}

func (c *PKLClient) Login(ctx context.Context, req LoginRequest) (Session, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		return Session{}, &Failure{Stage: "input", Category: "invalid_credentials", SafeMessage: "Proto email and password are required."}
	}
	session, complete, err := c.run(ctx, pklRequest{Action: "login", Email: email, Password: req.Password, ProxyURL: req.ProxyURL}, nil)
	if err != nil {
		return Session{}, err
	}
	if !complete || !session.ValidFor(email) {
		return Session{}, bridgeProtocolFailure()
	}
	return session, nil
}

func (c *PKLClient) Fetch(ctx context.Context, session *Session, req FetchRequest) (FetchResult, error) {
	if c == nil {
		return FetchResult{}, bridgeProtocolFailure()
	}
	if session != nil && session.Version == 1 {
		legacy := c.legacy
		if legacy == nil {
			legacy = NewClient()
		}
		return legacy.Fetch(ctx, session, req)
	}
	req.Recipient = strings.ToLower(strings.TrimSpace(req.Recipient))
	if session == nil || session.Version != 2 || !session.ValidFor(req.Recipient) {
		return FetchResult{}, &Failure{Stage: "session", Category: "session_revoked", SafeMessage: "No reusable Proto session is available."}
	}
	if req.UntilAt.IsZero() {
		req.UntilAt = time.Now().UTC()
	}
	if req.MaxMessages <= 0 {
		req.MaxMessages = 30
	}
	req.MaxMessages = min(req.MaxMessages, 1000)
	if req.FullHistory {
		req.KnownMessageIDs = nil
	}
	if req.SinceAt.After(req.UntilAt) || len(req.KnownMessageIDs) > 100000 {
		return FetchResult{}, bridgeProtocolFailure()
	}
	knownBytes := 0
	for _, id := range req.KnownMessageIDs {
		knownBytes += len(id)
		if len(id) > 2048 || knownBytes > 4<<20 {
			return FetchResult{}, bridgeProtocolFailure()
		}
	}
	var address AddressKeys
	for _, candidate := range session.Addresses {
		if strings.EqualFold(candidate.Email, req.Recipient) {
			address = candidate
		}
	}
	result := FetchResult{Messages: []Message{}}
	seen := make(map[string]bool)
	messageBytes := 0
	var consumerErr error
	emit := func(messages []Message) error {
		batch := make([]Message, 0, len(messages))
		for _, message := range messages {
			if message.ID == "" || len(message.ID) > 1024 || (message.Folder != "Inbox" && message.Folder != "Junk") ||
				message.OriginalToCount != len(message.ToList) ||
				message.ReceivedAt.IsZero() || message.ReceivedAt.Before(req.SinceAt) || message.ReceivedAt.After(req.UntilAt) ||
				!messageForAddress(apiMessage{AddressID: message.AddressID, ToList: message.ToList, CCList: message.CCList, BCCList: message.BCCList}, address) {
				return bridgeProtocolFailure()
			}
			// Proton IDs survive folder moves, including a move during token refresh.
			key := message.ID
			if seen[key] {
				continue
			}
			if len(seen) >= 100000 {
				return bridgeProtocolFailure()
			}
			seen[key] = true
			batch = append(batch, message)
		}
		if len(batch) == 0 {
			return nil
		}
		if req.OnMessages != nil {
			consumerErr = req.OnMessages(batch)
			return consumerErr
		}
		encoded, err := json.Marshal(batch)
		if err != nil {
			return bridgeProtocolFailure()
		}
		messageBytes += len(encoded)
		if messageBytes > maxBridgeLineBytes {
			return bridgeProtocolFailure()
		}
		result.Messages = append(result.Messages, batch...)
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, complete, err := c.run(ctx, pklRequest{Action: "fetch", PKL: session.PKL, ProxyURL: req.ProxyURL, Recipient: req.Recipient,
			SinceAt: req.SinceAt, UntilAt: req.UntilAt, MaxMessages: req.MaxMessages, FullHistory: req.FullHistory, KnownMessageIDs: req.KnownMessageIDs}, emit)
		if consumerErr != nil {
			return FetchResult{}, consumerErr
		}
		if err == nil {
			if req.FullHistory && !complete {
				return FetchResult{}, &Failure{Stage: "list", Category: "incomplete_history", SafeMessage: "Proto mailbox history was not completely read.", Retryable: true}
			}
			result.Complete = complete
			return result, nil
		}
		var failure *Failure
		if attempt != 0 || !errors.As(err, &failure) || failure.Category != "session_revoked" {
			return FetchResult{}, err
		}
		refresh := func(refreshCtx context.Context, current *Session) error {
			next, complete, err := c.run(refreshCtx, pklRequest{Action: "refresh", PKL: current.PKL, ProxyURL: req.ProxyURL, Recipient: req.Recipient}, nil)
			if err != nil {
				return err
			}
			if !complete || !next.ValidFor(req.Recipient) || next.UID != current.UID || next.KeyFingerprint != current.KeyFingerprint {
				return bridgeProtocolFailure()
			}
			*current = next
			return nil
		}
		if req.RefreshSession != nil {
			err = req.RefreshSession(ctx, session, refresh)
		} else if req.OnSession != nil {
			next := *session
			if err = refresh(ctx, &next); err == nil {
				err = req.OnSession(next)
				if err == nil {
					*session = next
				}
			}
		} else {
			err = &Failure{Stage: "refresh", Category: "session_persistence", SafeMessage: "Proto session refresh requires a session store.", Retryable: true}
		}
		if err != nil {
			return FetchResult{}, err
		}
	}
	return FetchResult{}, bridgeProtocolFailure()
}

func (c *PKLClient) run(ctx context.Context, request pklRequest, emit func([]Message) error) (Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, false, err
	}
	if c == nil || (c.command == nil && !c.RuntimeAvailable()) {
		return Session{}, false, &Failure{Stage: "dependency", Category: "dependency", SafeMessage: "Proto Python runtime is unavailable.", Retryable: true}
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) >= maxBridgeLineBytes {
		return Session{}, false, bridgeProtocolFailure()
	}
	data = append(data, '\n')
	defer clear(data)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, c.python, "-I", "-X", "utf8", "-c", pklBridgeSource)
	if c.command != nil {
		cmd = c.command(childCtx)
	}
	cmd.Env = pklWorkerEnvironment()
	cmd.Stdin, cmd.Stderr = bytes.NewReader(data), io.Discard
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		if ctx.Err() != nil {
			return Session{}, false, ctx.Err()
		}
		return Session{}, false, &Failure{Stage: "dependency", Category: "dependency", SafeMessage: "Proto Python worker could not be started.", Retryable: true}
	}
	defer stdout.Close()
	// ponytail: cap one helper response at 128 MiB / 100k messages; larger histories
	// fail incomplete. Add a paged resume cursor if real mailboxes exceed this budget.
	limited := &io.LimitedReader{R: stdout, N: maxBridgeOutputBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxBridgeLineBytes)
	var session Session
	var responseErr error
	var sessionSeen, terminal, complete bool
	for scanner.Scan() {
		if terminal || limited.N <= 0 {
			responseErr = bridgeProtocolFailure()
			break
		}
		var event pklEvent
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
			responseErr = bridgeProtocolFailure()
			break
		}
		switch event.Event {
		case "session":
			if sessionSeen || request.Action == "fetch" {
				responseErr = bridgeProtocolFailure()
				break
			}
			sessionSeen = true
			session = Session{Version: 2, PKL: event.PKL, UID: event.UID, Addresses: event.Addresses, KeyFingerprint: event.KeyFingerprint}
			identity := request.Email
			if identity == "" {
				identity = request.Recipient
			}
			if !session.ValidFor(identity) {
				responseErr = bridgeProtocolFailure()
			}
		case "messages":
			if request.Action != "fetch" || emit == nil || len(event.Messages) == 0 || len(event.Messages) > 100 {
				responseErr = bridgeProtocolFailure()
			} else {
				responseErr = emit(event.Messages)
			}
		case "result":
			if event.Complete == nil || (request.Action != "fetch" && !sessionSeen) {
				responseErr = bridgeProtocolFailure()
			} else {
				terminal, complete = true, *event.Complete
			}
		case "error":
			terminal = true
			responseErr = pklFailure(event, request.Action)
		default:
			responseErr = bridgeProtocolFailure()
		}
		if responseErr != nil {
			break
		}
	}
	if responseErr != nil || scanner.Err() != nil || !terminal || limited.N <= 0 {
		cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return Session{}, false, ctx.Err()
	}
	if responseErr != nil {
		return Session{}, false, responseErr
	}
	if scanner.Err() != nil || !terminal || limited.N <= 0 || waitErr != nil {
		return Session{}, false, bridgeProtocolFailure()
	}
	return session, complete, nil
}

func pklWorkerEnvironment() []string {
	env := make([]string, 0, 8)
	for _, key := range []string{"PATH", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func pklFailure(event pklEvent, action string) *Failure {
	switch event.Stage {
	case "input", "session", "login", "auth_info", "auth", "cookies", "keys", "addresses", "refresh", "list", "read", "decrypt", "mime", "transport", "dependency", "protocol":
	default:
		return bridgeProtocolFailure()
	}
	if event.HTTPStatus < 0 || event.HTTPStatus > 599 || event.APICode < 0 || event.APICode > 1000000 {
		return bridgeProtocolFailure()
	}
	if event.HTTPStatus == 429 || event.HTTPStatus >= 500 || event.HTTPStatus == 408 {
		failure := responseFailure(event.HTTPStatus, event.APICode, "")
		failure.Stage = event.Stage
		return failure
	}
	message := ""
	switch event.Category {
	case "invalid_credentials":
		if action != "login" || (event.Stage != "auth" && event.Stage != "auth_info") || (event.APICode != 8002 && event.APICode != 6003) {
			return bridgeProtocolFailure()
		}
		if event.Stage == "auth_info" && event.APICode == 8002 {
			failure := responseFailure(event.HTTPStatus, event.APICode, "/auth/v4/info")
			return failure
		}
		message = "Proto rejected the account credentials."
	case "identity_mismatch":
		message = "The Proto session does not own the requested receiving address."
	case "session_revoked":
		message = "The Proto session is no longer valid; revalidation is required."
	case "action_required":
		message = "Proto requires an additional account verification or account action."
	case "rate_limited":
		message = "Proto temporarily limited account requests."
	case "request":
		message = "Proto service is temporarily unavailable."
	case "decryption":
		message = "A Proto message could not be decrypted."
	case "protocol":
		message = "Proto session bridge could not complete the requested operation."
	default:
		return bridgeProtocolFailure()
	}
	return &Failure{Stage: event.Stage, Category: event.Category, HTTPStatus: event.HTTPStatus, APICode: event.APICode,
		SafeMessage: message, Retryable: event.Retryable, ProxyFailure: event.ProxyFailure}
}
