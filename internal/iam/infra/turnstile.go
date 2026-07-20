package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
)

const (
	turnstileVerifyURL        = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	turnstileExpectedHostname = "remail.aishop6.com"
)

// TurnstileVerifier validates single-use Cloudflare Turnstile tokens.
type TurnstileVerifier struct {
	secretKey string
	client    *http.Client
	verifyURL string
}

func NewTurnstileVerifier(secretKey string) *TurnstileVerifier {
	return &TurnstileVerifier{
		secretKey: secretKey,
		client:    &http.Client{Timeout: 5 * time.Second},
		verifyURL: turnstileVerifyURL,
	}
}

func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP, expectedAction string) error {
	if token = strings.TrimSpace(token); token == "" {
		return domain.ErrTurnstileInvalid
	}

	form := url.Values{
		"secret":   {v.secretKey},
		"response": {token},
	}
	if remoteIP = strings.TrimSpace(remoteIP); remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create turnstile request: %w", domain.ErrTurnstileUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("verify turnstile: %v: %w", err, domain.ErrTurnstileUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("verify turnstile status %d: %w", resp.StatusCode, domain.ErrTurnstileUnavailable)
	}

	var result struct {
		Success    bool     `json:"success"`
		Action     string   `json:"action"`
		Hostname   string   `json:"hostname"`
		ErrorCodes []string `json:"error-codes"`
		Metadata   struct {
			ResultWithTestingKey bool `json:"result_with_testing_key"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode turnstile response: %v: %w", err, domain.ErrTurnstileUnavailable)
	}
	if !result.Success {
		if len(result.ErrorCodes) == 0 {
			return fmt.Errorf("turnstile failed without an error code: %w", domain.ErrTurnstileUnavailable)
		}
		for _, code := range result.ErrorCodes {
			switch code {
			case "missing-input-response", "invalid-input-response", "timeout-or-duplicate":
			default:
				return fmt.Errorf("turnstile service error %v: %w", result.ErrorCodes, domain.ErrTurnstileUnavailable)
			}
		}
		return domain.ErrTurnstileInvalid
	}
	if len(result.ErrorCodes) > 0 {
		return fmt.Errorf("turnstile succeeded with errors %v: %w", result.ErrorCodes, domain.ErrTurnstileUnavailable)
	}
	if result.Metadata.ResultWithTestingKey {
		return nil
	}
	if result.Action != expectedAction || result.Hostname != turnstileExpectedHostname {
		return domain.ErrTurnstileInvalid
	}
	return nil
}
