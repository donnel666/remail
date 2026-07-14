package infra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
)

const (
	defaultMicrosoftClientID = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"
	defaultMicrosoftScopes   = "https://graph.microsoft.com/Mail.Read https://graph.microsoft.com/Mail.Send User.Read offline_access"
	microsoftTokenURL        = "https://login.microsoftonline.com/consumers/oauth2/v2.0/token"
	defaultInboundMXRecord   = "mx.aishop6.com"
)

type MicrosoftOAuthClient struct {
	timeout time.Duration
}

type MicrosoftOAuthRequest struct {
	EmailAddress   string
	Password       string
	ClientID       string
	RefreshToken   string
	BindingAddress string
	ProxyURL       string
}

type MicrosoftOAuthResult struct {
	Valid          bool
	ClientID       string
	AccessToken    string
	RefreshToken   string
	BindingAddress string
	BindingStatus  string
	MailFetch      *MicrosoftMailFetchResult
	GraphAvailable bool
	Category       string
	SafeMessage    string
	ProxyFailure   bool
	// BoundDisplay is the masked recovery mailbox (e.g. a****b@qq.com) an account
	// is already bound to when that mailbox is on an external (non-binding)
	// domain we cannot receive codes at.
	BoundDisplay string
}

func NewMicrosoftOAuthClient() *MicrosoftOAuthClient {
	return &MicrosoftOAuthClient{timeout: 30 * time.Second}
}

func (c *MicrosoftOAuthClient) RefreshToken(ctx context.Context, req MicrosoftOAuthRequest) (MicrosoftOAuthResult, error) {
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		clientID = defaultMicrosoftClientID
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		return microsoftOAuthFailure("request", "Microsoft refresh token is missing.", false), nil
	}

	return exchangeMicrosoftAccessToken(ctx, clientID, refreshToken, defaultMicrosoftScopes, microsoftTokenURL, req.ProxyURL, c.timeout)
}

func (c *MicrosoftOAuthClient) AcquireToken(ctx context.Context, req MicrosoftOAuthRequest) (MicrosoftOAuthResult, error) {
	email := strings.TrimSpace(req.EmailAddress)
	password := req.Password
	if email == "" || password == "" {
		return microsoftOAuthFailure("password", "Microsoft account or password is missing.", false), nil
	}
	result, err := msacl.Authorize(ctx, email, password, req.ProxyURL, req.BindingAddress)
	if err != nil {
		return microsoftOAuthFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != ""), err
	}
	clientID := strings.TrimSpace(result.ClientID)
	if clientID == "" {
		clientID = defaultMicrosoftClientID
	}
	return MicrosoftOAuthResult{
		Valid:          result.Valid,
		ClientID:       clientID,
		AccessToken:    strings.TrimSpace(result.AccessToken),
		RefreshToken:   strings.TrimSpace(result.RefreshToken),
		BindingAddress: strings.TrimSpace(result.BindingAddress),
		BindingStatus:  strings.TrimSpace(result.BindingStatus),
		Category:       result.Category,
		SafeMessage:    result.SafeMessage,
		ProxyFailure:   result.ProxyFailure,
		// Masked external recovery mailbox (already_bound) preserved for
		// bound_display persistence and operator visibility.
		BoundDisplay: strings.TrimSpace(result.BoundDisplay),
	}, nil
}

type DomainDNSValidator struct {
	resolver *net.Resolver
	mxRecord string
}

type DomainDNSRequest struct {
	Domain   string
	MXRecord string
}

type DomainDNSResult struct {
	Valid       bool
	Category    string
	SafeMessage string
}

func NewDomainDNSValidator() *DomainDNSValidator {
	return &DomainDNSValidator{
		resolver: net.DefaultResolver,
		mxRecord: defaultInboundMXRecord,
	}
}

func (v *DomainDNSValidator) Validate(ctx context.Context, req DomainDNSRequest) (DomainDNSResult, error) {
	domain := strings.Trim(strings.ToLower(strings.TrimSpace(req.Domain)), ".")
	if domain == "" {
		return DomainDNSResult{Category: "dns", SafeMessage: "Domain DNS validation failed."}, nil
	}
	expectedMX := strings.Trim(strings.ToLower(strings.TrimSpace(req.MXRecord)), ".")
	if expectedMX == "" {
		expectedMX = v.mxRecord
	}
	expectedMX = strings.Trim(expectedMX, ".")

	resolver := v.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	mxRecords, err := resolver.LookupMX(ctx, domain)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return DomainDNSResult{Category: "request", SafeMessage: "Domain DNS service is temporarily unavailable."}, err
		}
		return DomainDNSResult{Category: "dns", SafeMessage: "Domain MX record is not configured correctly."}, nil
	}
	for _, mx := range mxRecords {
		host := strings.Trim(strings.ToLower(strings.TrimSpace(mx.Host)), ".")
		if host == expectedMX {
			return DomainDNSResult{Valid: true}, nil
		}
	}
	return DomainDNSResult{Category: "dns", SafeMessage: "Domain MX record is not configured correctly."}, nil
}

func microsoftOAuthFailure(category, message string, proxyFailure bool) MicrosoftOAuthResult {
	return MicrosoftOAuthResult{
		Valid:        false,
		Category:     category,
		SafeMessage:  message,
		ProxyFailure: proxyFailure,
	}
}

func classifyMicrosoftTokenFailure(statusCode int, body map[string]any) (string, string, bool) {
	errorCode := strings.ToLower(strings.TrimSpace(stringValue(body["error"])))
	errorDescription := strings.ToLower(strings.TrimSpace(stringValue(body["error_description"])))
	switch {
	case statusCode == 429 || statusCode >= 500:
		return "request", "Microsoft mail service is temporarily unavailable.", true
	case strings.Contains(errorCode, "invalid_grant") || strings.Contains(errorDescription, "invalid grant"):
		return "oauth_invalid_grant", "Microsoft refresh token is invalid or expired.", false
	case strings.Contains(errorDescription, "mfa") || strings.Contains(errorDescription, "multi-factor"):
		return "mfa", "Microsoft account requires authenticator verification.", false
	case strings.Contains(errorDescription, "passkey"):
		return "passkey", "Microsoft account requires passkey verification.", false
	case strings.Contains(errorDescription, "phone"):
		return "phone", "Microsoft account requires phone verification.", false
	case strings.Contains(errorDescription, "password"):
		return "password", "Microsoft account password is incorrect.", false
	case strings.Contains(errorDescription, "invalid username") || strings.Contains(errorDescription, "user account does not exist"):
		return "unknown_mailbox", "Microsoft account does not exist or recovery mailbox is not supported.", false
	case strings.Contains(errorDescription, "locked"):
		return "locked", "Microsoft account is locked.", false
	case strings.Contains(errorCode, "invalid_client") || strings.Contains(errorDescription, "client"):
		return "oauth_client", "Microsoft OAuth client is invalid or not allowed.", false
	case strings.Contains(errorDescription, "consent") || strings.Contains(errorDescription, "permission"):
		return "oauth_permission", "Microsoft OAuth permission is not available.", false
	default:
		return "request", "Microsoft mail service is temporarily unavailable.", statusCode >= 500
	}
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}
