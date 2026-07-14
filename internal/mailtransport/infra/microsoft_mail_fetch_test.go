package infra

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMicrosoftIMAPClient struct {
	called      bool
	accessToken string
	result      MicrosoftMailFetchResult
	err         error
}

func (c *fakeMicrosoftIMAPClient) FetchAll(_ context.Context, _ MicrosoftMailFetchRequest, accessToken string) (MicrosoftMailFetchResult, error) {
	c.called = true
	c.accessToken = accessToken
	return c.result, c.err
}

func TestMicrosoftMailFetchClientGraphSuccessDoesNotFallback(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(_ context.Context, req MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			assert.Equal(t, "user@example.com", req.EmailAddress)
			assert.Equal(t, defaultMicrosoftClientID, req.ClientID)
			return MicrosoftMailFetchResult{
				Valid:        true,
				Protocol:     "graph",
				RefreshToken: "rotated-rt",
				MessageCount: 2,
				FolderCounts: map[string]int{"Inbox": 1, "Junk": 1},
			}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	assert.Equal(t, "graph", result.Protocol)
	assert.Equal(t, "rotated-rt", result.RefreshToken)
	assert.Equal(t, 2, result.MessageCount)
	assert.False(t, imapFallback.called)
}

func TestMicrosoftMailFetchClientFallsBackToIMAPAfterGraphFailure(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", true), errors.New("graph unavailable")
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: MicrosoftMailFetchResult{
			Valid:        true,
			Protocol:     "imap",
			MessageCount: 3,
			FolderCounts: map[string]int{"Inbox": 2, "Junk": 1},
		},
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	assert.True(t, imapFallback.called)
	assert.Equal(t, "imap-access-token", imapFallback.accessToken)
	assert.Equal(t, "imap", result.Protocol)
	assert.Equal(t, "graph", result.FallbackFrom)
	assert.Equal(t, "imap-rotated-rt", result.RefreshToken)
	assert.Equal(t, "Microsoft mail service is temporarily unavailable.", result.GraphSafeError)
}

func TestMicrosoftMailFetchClientKeepsGraphRotatedTokenWhenIMAPExchangeFails(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			result := microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false)
			result.RefreshToken = "graph-rotated-rt"
			return result, nil
		},
		exchangeIMAPToken: func(_ context.Context, req MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			require.Equal(t, "graph-rotated-rt", req.RefreshToken)
			return "", "", microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), errors.New("imap token exchange unavailable")
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "graph-rotated-rt", result.RefreshToken)
	require.False(t, imapFallback.called)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchRequiresIMAPAccountProof(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{result: MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		FolderCounts: map[string]int{},
	}}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "alias@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, imapFallback.called)
	require.Equal(t, "imap", result.Protocol)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchSurvivesIMAPAuthFailure(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: microsoftMailFetchFailure("imap_auth_failed", "Microsoft IMAP authentication failed.", false),
		err:    errors.New("imap authentication failed"),
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, microsoftIdentityMismatch, result.Category)
	require.Equal(t, "Microsoft OAuth credentials do not match the configured account.", result.SafeMessage)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchKeepsTemporaryIMAPFailureRetryable(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", true),
		err:    errors.New("temporary IMAP failure"),
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientChecksGraphIdentityBeforeFolders(t *testing.T) {
	folderCalls := 0
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMismatched, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) ([]MicrosoftFetchedMessage, error) {
			folderCalls++
			return nil, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, microsoftIdentityMismatch, result.Category)
	require.Equal(t, "refresh-token", result.RefreshToken)
	require.Zero(t, folderCalls)
}

func TestMicrosoftMailFetchClientEmptyGraphIdentityCannotReachFolders(t *testing.T) {
	folderCalls := 0
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityUnavailable, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) ([]MicrosoftFetchedMessage, error) {
			folderCalls++
			return nil, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Zero(t, folderCalls)
}

func TestMicrosoftMailFetchClientGraphFolderFailureKeepsRefreshToken(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) ([]MicrosoftFetchedMessage, error) {
			return nil, errors.New("temporary folder failure")
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "authoritative-refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "authoritative-refresh-token", result.RefreshToken)
}

func TestMicrosoftMailFetchClientGraphSessionFailureKeepsRefreshToken(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		newGraphSession: func(context.Context, string, int) (*msacl.Session, error) {
			return nil, errors.New("graph session unavailable")
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "authoritative-refresh-token",
		AccessToken:  "access-token",
	})

	require.Error(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "authoritative-refresh-token", result.RefreshToken)
}

func TestMicrosoftMailFetchClientTemporaryGraphIdentityFailureFallsBackToIMAP(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityUnavailable, errors.New("temporary profile failure")
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{result: MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		FolderCounts: map[string]int{},
	}}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, imapFallback.called)
	require.Equal(t, "imap", result.Protocol)
}

func TestMicrosoftGraphIdentityStatusUsesMailAndUPN(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		profile  graphIdentityProfile
		status   microsoftGraphIdentityStatus
	}{
		{name: "mail", expected: "owner@example.com", profile: graphIdentityProfile{Mail: "OWNER@example.com"}, status: microsoftGraphIdentityMatched},
		{name: "upn", expected: "owner@example.com", profile: graphIdentityProfile{UserPrincipalName: "owner@example.com"}, status: microsoftGraphIdentityMatched},
		{name: "mismatch", expected: "owner@example.com", profile: graphIdentityProfile{Mail: "other@example.com"}, status: microsoftGraphIdentityMismatched},
		{name: "empty", expected: "owner@example.com", profile: graphIdentityProfile{}, status: microsoftGraphIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.status, microsoftGraphIdentityStatusFor(tt.expected, tt.profile))
		})
	}
}

func TestMicrosoftIMAPAuthenticationFailureClassification(t *testing.T) {
	require.True(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeAuthenticationFailed,
		Text: "AUTHENTICATE failed.",
	}))
	require.True(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Text: "AUTHENTICATE failed.",
	}))
	require.False(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeUnavailable,
		Text: "Service temporarily unavailable",
	}))
	require.False(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(io.EOF))
}

func TestDialHTTPConnectHonorsContextWhenProxyGoesSilent(t *testing.T) {
	proxyAddress := startSilentMicrosoftMailProxy(t)
	parsed, err := url.Parse("http://" + proxyAddress)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	conn, err := dialHTTPConnect(ctx, &net.Dialer{Timeout: time.Second}, parsed, outlookIMAPAddress)
	if conn != nil {
		_ = conn.Close()
	}

	require.Error(t, err)
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestDialOutlookIMAPSOCKSHandshakeHonorsContext(t *testing.T) {
	proxyAddress := startSilentMicrosoftMailProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	conn, err := dialOutlookIMAPConn(ctx, "socks5://"+proxyAddress)
	if conn != nil {
		_ = conn.Close()
	}

	require.Error(t, err)
	require.Less(t, time.Since(started), 2*time.Second)
}

func startSilentMicrosoftMailProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		_ = listener.Close()
	})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 1024)
		_, _ = conn.Read(buffer)
		<-release
	}()
	return listener.Addr().String()
}

func TestMicrosoftMailFetchClientRejectsIncompleteCredentialsWithSpecificCategory(t *testing.T) {
	client := NewMicrosoftMailFetchClient()

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
	}, nil)

	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "missing_token", result.Category)
	assert.Equal(t, "Microsoft mail fetch credentials are incomplete.", result.SafeMessage)
}

func TestClassifyMicrosoftGraphFailureKeepsAuthFailureGranularity(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		category    string
		safeMessage string
		proxy       bool
	}{
		{
			name: "unauthorized",
			err: &microsoftGraphHTTPError{
				statusCode: 401,
				message:    "invalid token",
			},
			category:    "graph_unauthorized",
			safeMessage: "Microsoft Graph access token is unauthorized or expired.",
		},
		{
			name: "forbidden",
			err: &microsoftGraphHTTPError{
				statusCode: 403,
				message:    "missing permission",
			},
			category:    "graph_forbidden",
			safeMessage: "Microsoft Graph mailbox permission is not available.",
		},
		{
			name: "rate limited",
			err: &microsoftGraphHTTPError{
				statusCode: 429,
				message:    "too many requests",
			},
			category:    "request",
			safeMessage: "Microsoft mail service is temporarily unavailable.",
			proxy:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, message, proxy := classifyMicrosoftGraphFailure(tt.err)

			assert.Equal(t, tt.category, category)
			assert.Equal(t, tt.safeMessage, message)
			assert.Equal(t, tt.proxy, proxy)
		})
	}
}
