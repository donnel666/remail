package infra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	stdmail "net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"golang.org/x/net/proxy"
)

const (
	microsoftGraphAPIBase           = "https://graph.microsoft.com/v1.0"
	microsoftGraphMessagePageTop    = 100
	microsoftIMAPTokenURL           = "https://login.live.com/oauth20_token.srf"
	outlookIMAPAddress              = "outlook.office365.com:993"
	microsoftIdentityMismatch       = "identity_mismatch"
	microsoftIMAPOperationTimeout   = 60 * time.Second
	microsoftIMAPFullHistoryTimeout = 15 * time.Minute
	microsoftProxyHandshakeTimeout  = 30 * time.Second
	microsoftMailStreamBatchSize    = 100
)

var defaultMicrosoftMailFolders = []MicrosoftMailFolder{
	{ID: "inbox", Label: "Inbox"},
	{ID: "junkemail", Label: "Junk"},
}

type MicrosoftMailFetchClient struct {
	timeout           time.Duration
	graphFetch        func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error)
	newGraphSession   func(context.Context, string, int) (*msacl.Session, error)
	graphIdentity     func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error)
	graphFolderFetch  func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error)
	exchangeIMAPToken func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error)
}

type MicrosoftMailFetchRequest struct {
	EmailAddress string
	ClientID     string
	RefreshToken string
	AccessToken  string
	ProxyURL     string
	SinceAt      time.Time
	UntilAt      time.Time
	MaxMessages  int
	OnMessages   func([]MicrosoftFetchedMessage)
	OnReset      func()
}

type MicrosoftMailFetchResult struct {
	Valid          bool
	Protocol       string
	FallbackFrom   string
	MessageCount   int
	FolderCounts   map[string]int
	RefreshToken   string
	Category       string
	SafeMessage    string
	ProxyFailure   bool
	Messages       []MicrosoftFetchedMessage
	GraphSafeError string
}

type MicrosoftMailFolder struct {
	ID    string
	Label string
}

type microsoftFolderFetchResult struct {
	Messages []MicrosoftFetchedMessage
	Count    int
}

type MicrosoftFetchedMessage struct {
	ID                string
	InternetMessageID string
	FolderID          string
	FolderLabel       string
	Subject           string
	From              string
	To                string
	ReceivedAt        time.Time
	Preview           string
	Body              string
	RawSource         string
	ProviderPayload   string
	Protocol          string
	HasAttachments    bool
}

type graphMessagesPage struct {
	Value    []graphMessage      `json:"value"`
	NextLink string              `json:"@odata.nextLink"`
	Error    *graphErrorEnvelope `json:"error"`
}

type graphIdentityProfile struct {
	Mail              string              `json:"mail"`
	UserPrincipalName string              `json:"userPrincipalName"`
	Error             *graphErrorEnvelope `json:"error"`
}

type microsoftGraphIdentityStatus uint8

const (
	microsoftGraphIdentityUnavailable microsoftGraphIdentityStatus = iota
	microsoftGraphIdentityMatched
	microsoftGraphIdentityMismatched
)

type graphMessage struct {
	ID                string              `json:"id"`
	InternetMessageID string              `json:"internetMessageId"`
	Subject           string              `json:"subject"`
	BodyPreview       string              `json:"bodyPreview"`
	Body              graphMessageBody    `json:"body"`
	From              *graphRecipient     `json:"from"`
	ToRecipients      []graphRecipient    `json:"toRecipients"`
	ReceivedDateTime  string              `json:"receivedDateTime"`
	HasAttachments    bool                `json:"hasAttachments"`
	Error             *graphErrorEnvelope `json:"error"`
}

type graphMessageBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type graphRecipient struct {
	EmailAddress graphEmailAddress `json:"emailAddress"`
}

type graphEmailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type graphErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type microsoftIMAPClient interface {
	FetchAll(ctx context.Context, req MicrosoftMailFetchRequest, accessToken string) (MicrosoftMailFetchResult, error)
}

type outlookIMAPClient struct{}

func NewMicrosoftMailFetchClient() *MicrosoftMailFetchClient {
	return &MicrosoftMailFetchClient{timeout: 30 * time.Second}
}

func (c *MicrosoftMailFetchClient) FetchAll(ctx context.Context, req MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
	imapFallback := outlookIMAPClient{}
	return c.fetchAll(ctx, req, imapFallback)
}

func (c *MicrosoftMailFetchClient) fetchAll(ctx context.Context, req MicrosoftMailFetchRequest, imapFallback microsoftIMAPClient) (MicrosoftMailFetchResult, error) {
	req = normalizeMailFetchRequest(req)
	if req.EmailAddress == "" || req.ClientID == "" || req.RefreshToken == "" {
		return microsoftMailFetchFailure("missing_token", "Microsoft mail fetch credentials are incomplete.", false), nil
	}
	streamed := false
	if onMessages := req.OnMessages; onMessages != nil {
		req.OnMessages = func(messages []MicrosoftFetchedMessage) {
			if len(messages) > 0 {
				streamed = true
			}
			onMessages(messages)
		}
	}
	graphResult, graphErr := c.fetchGraphAll(ctx, req)
	if graphErr == nil && graphResult.Valid {
		return graphResult, nil
	}
	graphIdentityMismatched := strings.EqualFold(strings.TrimSpace(graphResult.Category), microsoftIdentityMismatch)
	if strings.TrimSpace(graphResult.RefreshToken) != "" {
		req.RefreshToken = strings.TrimSpace(graphResult.RefreshToken)
	}
	graphSafeError := graphResult.SafeMessage
	if graphSafeError == "" {
		graphSafeError = "Microsoft mail service is temporarily unavailable."
	}

	if imapFallback != nil {
		if streamed {
			if req.OnReset == nil {
				return graphResult, graphErr
			}
			req.OnReset()
			streamed = false
		}
		imapAccessToken, imapRefreshToken, tokenResult, err := c.exchangeIMAPAccessToken(ctx, req)
		if err == nil && imapAccessToken != "" {
			imapResult, imapErr := imapFallback.FetchAll(ctx, req, imapAccessToken)
			if imapErr == nil && imapResult.Valid {
				imapResult.Protocol = "imap"
				imapResult.FallbackFrom = "graph"
				imapResult.GraphSafeError = graphSafeError
				if imapRefreshToken != "" {
					imapResult.RefreshToken = imapRefreshToken
				}
				return imapResult, nil
			}
			if imapErr != nil {
				if strings.TrimSpace(imapResult.Category) != "" &&
					!isTemporaryMicrosoftMailFetchCategory(imapResult.Category) {
					tokenResult = imapResult
				} else {
					tokenResult = microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", imapResult.ProxyFailure)
				}
			} else {
				tokenResult = imapResult
			}
		}
		if strings.TrimSpace(imapRefreshToken) != "" {
			// The IMAP-scope exchange is authoritative even when the subsequent
			// mailbox proof fails. Keep its rotated RT so a one-time rotation does
			// not leave the database pointing at the superseded credential.
			rotated := strings.TrimSpace(imapRefreshToken)
			graphResult.RefreshToken = rotated
			tokenResult.RefreshToken = rotated
		}
		if strings.TrimSpace(tokenResult.RefreshToken) == "" && strings.TrimSpace(req.RefreshToken) != "" {
			// Graph may already have rotated the RT before the IMAP-scope exchange
			// fails. The fallback failure must still carry that latest credential.
			tokenResult.RefreshToken = strings.TrimSpace(req.RefreshToken)
		}
		if tokenResult.SafeMessage != "" {
			if graphIdentityMismatched && !isTemporaryMicrosoftMailFetchCategory(tokenResult.Category) {
				return graphResult, nil
			}
			tokenResult.GraphSafeError = graphSafeError
			tokenResult.FallbackFrom = "graph"
			if graphResult.ProxyFailure {
				tokenResult.ProxyFailure = true
			}
			return tokenResult, nil
		}
	}
	if graphIdentityMismatched {
		return graphResult, nil
	}

	if graphResult.SafeMessage == "" {
		graphResult = microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", graphResult.ProxyFailure)
	}
	return graphResult, graphErr
}

func (c *MicrosoftMailFetchClient) fetchGraphAll(ctx context.Context, req MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
	if c != nil && c.graphFetch != nil {
		return c.graphFetch(ctx, req)
	}
	accessToken := strings.TrimSpace(req.AccessToken)
	refreshToken := strings.TrimSpace(req.RefreshToken)
	var tokenResult MicrosoftOAuthResult
	if accessToken == "" {
		var err error
		tokenResult, err = exchangeMicrosoftAccessToken(ctx, req.ClientID, req.RefreshToken, defaultMicrosoftScopes, microsoftTokenURL, req.ProxyURL, c.timeout)
		if err != nil {
			tokenResult = microsoftOAuthFailure("request", "Microsoft mail service is temporarily unavailable.", true)
		}
		if !tokenResult.Valid {
			return mailFetchResultFromOAuth(tokenResult), nil
		}
		accessToken = tokenResult.AccessToken
		refreshToken = tokenResult.RefreshToken
	}
	newGraphSession := msacl.NewAPISession
	if c != nil && c.newGraphSession != nil {
		newGraphSession = c.newGraphSession
	}
	session, err := newGraphSession(ctx, req.ProxyURL, timeoutSeconds(c.timeout))
	if err != nil {
		failure := microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != "")
		// Session construction happens after a possible OAuth exchange. Keep the
		// exchange's rotated RT even if the subsequent Graph client cannot start.
		failure.RefreshToken = refreshToken
		return failure, err
	}
	identityCheck := fetchMicrosoftGraphIdentity
	if c != nil && c.graphIdentity != nil {
		identityCheck = c.graphIdentity
	}
	identityStatus, identityErr := identityCheck(ctx, session, accessToken, req.EmailAddress)
	if identityErr != nil {
		category, message, proxyFailure := classifyMicrosoftGraphFailure(identityErr)
		failure := microsoftMailFetchFailure(category, message, proxyFailure)
		failure.RefreshToken = refreshToken
		return failure, nil
	}
	switch identityStatus {
	case microsoftGraphIdentityMatched:
		// Continue to the mailbox folders only after the configured account has
		// been matched to Graph's own identity response.
	case microsoftGraphIdentityMismatched:
		failure := microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false)
		failure.RefreshToken = refreshToken
		return failure, nil
	default:
		failure := microsoftMailFetchFailure("request", "Microsoft account identity could not be verified.", false)
		failure.RefreshToken = refreshToken
		return failure, nil
	}
	result := MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "graph",
		RefreshToken: refreshToken,
		FolderCounts: map[string]int{},
	}
	fetchFolder := fetchGraphFolderMessages
	if c != nil && c.graphFolderFetch != nil {
		fetchFolder = c.graphFolderFetch
	}
	for _, folder := range defaultMicrosoftMailFolders {
		folderResult, err := fetchFolder(ctx, session, accessToken, folder, req)
		if err != nil {
			category, message, proxyFailure := classifyMicrosoftGraphFailure(err)
			failure := microsoftMailFetchFailure(category, message, proxyFailure)
			// A successful OAuth exchange may rotate the refresh token before a
			// later Graph request fails. Preserve that authoritative credential so
			// IMAP fallback and the validation commit do not reuse the superseded RT.
			failure.RefreshToken = refreshToken
			return failure, nil
		}
		result.FolderCounts[folder.Label] = folderResult.Count
		result.MessageCount += folderResult.Count
		result.Messages = append(result.Messages, folderResult.Messages...)
	}
	if req.OnMessages == nil {
		sort.SliceStable(result.Messages, func(i, j int) bool {
			return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt)
		})
		result.Messages = limitMicrosoftMessages(result.Messages, req.MaxMessages)
		result.MessageCount = len(result.Messages)
	}
	return result, nil
}

func fetchMicrosoftGraphIdentity(
	_ context.Context,
	session *msacl.Session,
	accessToken string,
	expectedEmail string,
) (microsoftGraphIdentityStatus, error) {
	if session == nil || strings.TrimSpace(accessToken) == "" {
		return microsoftGraphIdentityUnavailable, nil
	}
	values := url.Values{}
	values.Set("$select", "mail,userPrincipalName")
	var profile graphIdentityProfile
	resp, err := session.GetJSON(microsoftGraphAPIBase+"/me?"+values.Encode(), map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(accessToken),
		"Accept":        "application/json",
	}, &profile)
	if err != nil {
		return microsoftGraphIdentityUnavailable, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := ""
		if profile.Error != nil {
			message = strings.TrimSpace(profile.Error.Message)
		}
		return microsoftGraphIdentityUnavailable, &microsoftGraphHTTPError{
			statusCode: resp.StatusCode,
			message:    message,
		}
	}
	return microsoftGraphIdentityStatusFor(expectedEmail, profile), nil
}

func microsoftGraphIdentityStatusFor(expectedEmail string, profile graphIdentityProfile) microsoftGraphIdentityStatus {
	expectedEmail = normalizeMicrosoftGraphIdentityEmail(expectedEmail)
	if expectedEmail == "" {
		return microsoftGraphIdentityUnavailable
	}
	candidates := []string{profile.Mail, profile.UserPrincipalName}
	foundIdentity := false
	for _, candidate := range candidates {
		candidate = normalizeMicrosoftGraphIdentityEmail(candidate)
		if candidate == "" {
			continue
		}
		foundIdentity = true
		if candidate == expectedEmail {
			return microsoftGraphIdentityMatched
		}
	}
	if foundIdentity {
		return microsoftGraphIdentityMismatched
	}
	return microsoftGraphIdentityUnavailable
}

func normalizeMicrosoftGraphIdentityEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, "\r\n\t") {
		return ""
	}
	parsed, err := stdmail.ParseAddress(value)
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), value) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Address))
}

func isTemporaryMicrosoftMailFetchCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "request", "auth_timeout", "rate_limited":
		return true
	default:
		return false
	}
}

func fetchGraphFolderMessages(ctx context.Context, session *msacl.Session, accessToken string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
	nextURL := graphFolderMessagesURL(folder, req)
	headers := map[string]string{
		"Authorization":    "Bearer " + accessToken,
		"Accept":           "application/json",
		"ConsistencyLevel": "eventual",
	}
	result := microsoftFolderFetchResult{Messages: make([]MicrosoftFetchedMessage, 0)}
	for strings.TrimSpace(nextURL) != "" {
		if err := ctx.Err(); err != nil {
			return microsoftFolderFetchResult{}, err
		}
		var page graphMessagesPage
		resp, err := session.GetJSON(nextURL, headers, &page)
		if err != nil {
			return microsoftFolderFetchResult{}, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return microsoftFolderFetchResult{}, &microsoftGraphHTTPError{
				statusCode: resp.StatusCode,
				message:    graphErrorMessage(page),
			}
		}
		messages := make([]MicrosoftFetchedMessage, 0, len(page.Value))
		for _, message := range page.Value {
			messages = append(messages, normalizeGraphFetchedMessage(message, folder, req.MaxMessages > 0))
		}
		if req.MaxMessages > 0 && result.Count+len(messages) > req.MaxMessages {
			messages = messages[:req.MaxMessages-result.Count]
		}
		result.Count += len(messages)
		if req.OnMessages != nil && len(messages) > 0 {
			req.OnMessages(messages)
		} else {
			result.Messages = append(result.Messages, messages...)
		}
		if req.MaxMessages > 0 && result.Count >= req.MaxMessages {
			break
		}
		nextURL = strings.TrimSpace(page.NextLink)
	}
	return result, nil
}

func graphFolderMessagesURL(folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) string {
	values := url.Values{}
	top := microsoftGraphMessagePageTop
	if req.MaxMessages > 0 && req.MaxMessages < top {
		top = req.MaxMessages
	}
	values.Set("$top", strconv.Itoa(top))
	values.Set("$orderby", "receivedDateTime desc")
	values.Set("$select", "id,internetMessageId,subject,bodyPreview,body,from,toRecipients,receivedDateTime,hasAttachments")
	filter := microsoftGraphReceivedFilter(req.SinceAt, req.UntilAt)
	if filter != "" {
		values.Set("$filter", filter)
	}
	return fmt.Sprintf("%s/me/mailFolders/%s/messages?%s", microsoftGraphAPIBase, url.PathEscape(folder.ID), values.Encode())
}

func microsoftGraphReceivedFilter(sinceAt, untilAt time.Time) string {
	parts := make([]string, 0, 2)
	if !sinceAt.IsZero() {
		parts = append(parts, "receivedDateTime ge "+sinceAt.UTC().Format(time.RFC3339Nano))
	}
	if !untilAt.IsZero() {
		parts = append(parts, "receivedDateTime le "+untilAt.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(parts, " and ")
}

func (c *MicrosoftMailFetchClient) exchangeIMAPAccessToken(ctx context.Context, req MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
	if c != nil && c.exchangeIMAPToken != nil {
		return c.exchangeIMAPToken(ctx, req)
	}
	result, err := exchangeMicrosoftAccessToken(ctx, req.ClientID, req.RefreshToken, "", microsoftIMAPTokenURL, req.ProxyURL, c.timeout)
	if err != nil {
		return "", "", microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != ""), err
	}
	if !result.Valid {
		return "", "", mailFetchResultFromOAuth(result), nil
	}
	return result.AccessToken, result.RefreshToken, MicrosoftMailFetchResult{}, nil
}

func (outlookIMAPClient) FetchAll(ctx context.Context, req MicrosoftMailFetchRequest, accessToken string) (MicrosoftMailFetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationTimeout := microsoftIMAPOperationTimeout
	if req.MaxMessages == 0 {
		operationTimeout = microsoftIMAPFullHistoryTimeout
	}
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	client, err := dialOutlookIMAPClient(operationCtx, req.ProxyURL)
	if err != nil {
		return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != ""), err
	}
	var closeOnce sync.Once
	closeClient := func() {
		closeOnce.Do(func() { _ = client.Close() })
	}
	stopCancellationClose := context.AfterFunc(operationCtx, closeClient)
	defer func() {
		stopCancellationClose()
		closeClient()
	}()

	if err := client.Authenticate(newXOAuth2Client(req.EmailAddress, accessToken)); err != nil {
		_ = client.Logout().Wait()
		if isDefinitiveMicrosoftIMAPAuthenticationFailure(err) {
			return microsoftMailFetchFailure("imap_auth_failed", "Microsoft IMAP authentication failed.", false), err
		}
		return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != ""), err
	}

	result := MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		RefreshToken: req.RefreshToken,
		FolderCounts: map[string]int{},
	}
	completedFolders := map[string]bool{}
	for _, folder := range imapCandidateFolders(client) {
		if completedFolders[folder.Label] {
			continue
		}
		if err := operationCtx.Err(); err != nil {
			_ = client.Logout().Wait()
			return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), err
		}
		selectData, err := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			continue
		}
		count := int(selectData.NumMessages)
		if count == 0 {
			completedFolders[folder.Label] = true
			result.FolderCounts[folder.Label] = 0
			continue
		}
		seqSet, err := recentIMAPSeqSet(operationCtx, client, selectData.NumMessages, req.SinceAt, req.MaxMessages)
		if err != nil || len(seqSet) == 0 {
			return microsoftMailFetchFailure("request", "Microsoft mailbox history could not be read completely.", false), err
		}
		bodySection := &imap.FetchItemBodySection{Peek: true}
		command := client.Fetch(seqSet, &imap.FetchOptions{
			Envelope:     true,
			InternalDate: true,
			UID:          true,
			BodySection:  []*imap.FetchItemBodySection{bodySection},
		})
		batch := make([]MicrosoftFetchedMessage, 0, microsoftMailStreamBatchSize)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			result.FolderCounts[folder.Label] += len(batch)
			result.MessageCount += len(batch)
			if req.OnMessages != nil {
				req.OnMessages(batch)
			} else {
				result.Messages = append(result.Messages, batch...)
			}
			batch = batch[:0]
		}
		for data := command.Next(); data != nil; data = command.Next() {
			row, collectErr := data.Collect()
			if collectErr != nil {
				_ = command.Close()
				return microsoftMailFetchFailure("request", "Microsoft mailbox history could not be read completely.", false), collectErr
			}
			message := normalizeIMAPFetchedMessage(row, folder, bodySection, req.MaxMessages > 0)
			if inMicrosoftFetchWindow(message.ReceivedAt, req.SinceAt, req.UntilAt) {
				batch = append(batch, message)
				if len(batch) == cap(batch) {
					flush()
				}
			}
		}
		if err := command.Close(); err != nil {
			return microsoftMailFetchFailure("request", "Microsoft mailbox history could not be read completely.", false), err
		}
		flush()
		completedFolders[folder.Label] = true
	}
	_ = client.Logout().Wait()
	if !requiredMicrosoftMailFoldersCompleted(completedFolders) {
		return microsoftMailFetchFailure("request", "Inbox and Junk must both be read completely.", false), nil
	}
	if req.OnMessages == nil {
		sort.SliceStable(result.Messages, func(i, j int) bool {
			return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt)
		})
		result.Messages = limitMicrosoftMessages(result.Messages, req.MaxMessages)
		result.MessageCount = len(result.Messages)
	}
	return result, nil
}

func requiredMicrosoftMailFoldersCompleted(completed map[string]bool) bool {
	return completed["Inbox"] && completed["Junk"]
}

func isDefinitiveMicrosoftIMAPAuthenticationFailure(err error) bool {
	var imapErr *imap.Error
	if !errors.As(err, &imapErr) || imapErr == nil {
		return false
	}
	switch imapErr.Code {
	case imap.ResponseCodeAuthenticationFailed, imap.ResponseCodeAuthorizationFailed:
		return true
	}
	if imapErr.Type != imap.StatusResponseTypeNo {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(imapErr.Text))
	return strings.Contains(text, "authenticate failed") ||
		strings.Contains(text, "authentication failed") ||
		strings.Contains(text, "login failed") ||
		strings.Contains(text, "invalid credentials")
}

func dialOutlookIMAPClient(ctx context.Context, proxyURL string) (*imapclient.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "outlook.office365.com", NextProtos: []string{"imap"}}
	conn, err := dialOutlookIMAPConn(ctx, proxyURL)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := setConnectionDeadline(ctx, tlsConn, microsoftIMAPOperationTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return imapclient.New(tlsConn, &imapclient.Options{TLSConfig: tlsConfig}), nil
}

func dialOutlookIMAPConn(ctx context.Context, proxyURL string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	proxyURL = normalizeMailProxyURL(proxyURL)
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	if proxyURL == "" {
		return nil, fmt.Errorf("proxy url is required for outlook imap fallback")
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		auth := (*proxy.Auth)(nil)
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 proxy does not support context cancellation")
		}
		conn, err := contextDialer.DialContext(ctx, "tcp", outlookIMAPAddress)
		if err != nil {
			return nil, err
		}
		if err := setConnectionDeadline(ctx, conn, microsoftIMAPOperationTimeout); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	case "http", "https":
		return dialHTTPConnect(ctx, dialer, parsed, outlookIMAPAddress)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme")
	}
}

func normalizeMailProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

func dialHTTPConnect(ctx context.Context, dialer *net.Dialer, parsed *url.URL, target string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	if err := setConnectionDeadline(ctx, conn, microsoftProxyHandshakeTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		credential := base64.StdEncoding.EncodeToString([]byte(parsed.User.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + credential + "\r\n"
	}
	request += "\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(line, " 200 ") {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy connect failed")
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if strings.TrimSpace(header) == "" {
			break
		}
	}
	return conn, nil
}

func setConnectionDeadline(ctx context.Context, conn net.Conn, fallback time.Duration) error {
	if conn == nil {
		return errors.New("network connection is nil")
	}
	if fallback <= 0 {
		fallback = microsoftProxyHandshakeTimeout
	}
	deadline := time.Now().Add(fallback)
	if ctx != nil {
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
	}
	return conn.SetDeadline(deadline)
}

func recentIMAPSeqSet(ctx context.Context, client *imapclient.Client, total uint32, sinceAt time.Time, maxMessages int) (imap.SeqSet, error) {
	if total == 0 {
		return nil, nil
	}
	var nums []uint32
	if !sinceAt.IsZero() {
		data, err := client.Search(&imap.SearchCriteria{Since: sinceAt.UTC()}, nil).Wait()
		if err != nil {
			return nil, err
		}
		nums = data.AllSeqNums()
	}
	if len(nums) == 0 {
		start := uint32(1)
		if maxMessages > 0 && int(total) > maxMessages {
			start = total - uint32(maxMessages) + 1
		}
		seqSet := imap.SeqSet{}
		seqSet.AddRange(start, total)
		return seqSet, ctx.Err()
	}
	if maxMessages > 0 && len(nums) > maxMessages {
		nums = nums[len(nums)-maxMessages:]
	}
	seqSet := imap.SeqSetNum(nums...)
	return seqSet, ctx.Err()
}

func imapCandidateFolders(client *imapclient.Client) []MicrosoftMailFolder {
	fallback := []MicrosoftMailFolder{
		{ID: "INBOX", Label: "Inbox"},
		{ID: "Junk", Label: "Junk"},
		{ID: "Junk Email", Label: "Junk"},
	}
	if client == nil {
		return fallback
	}
	listed, err := client.List("", "*", nil).Collect()
	if err != nil {
		return fallback
	}
	folders := make([]MicrosoftMailFolder, 0, len(listed)+len(fallback))
	for _, item := range listed {
		name := strings.TrimSpace(item.Mailbox)
		if name == "" {
			continue
		}
		label := ""
		if strings.EqualFold(name, "INBOX") {
			label = "Inbox"
		}
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerName, "junk") || strings.Contains(lowerName, "spam") {
			label = "Junk"
		}
		for _, attr := range item.Attrs {
			if attr == imap.MailboxAttrJunk {
				label = "Junk"
				break
			}
		}
		if label != "" {
			folders = append(folders, MicrosoftMailFolder{ID: name, Label: label})
		}
	}
	folders = append(folders, fallback...)
	return uniqueMicrosoftMailFolders(folders)
}

func uniqueMicrosoftMailFolders(folders []MicrosoftMailFolder) []MicrosoftMailFolder {
	out := make([]MicrosoftMailFolder, 0, len(folders))
	seen := make(map[string]struct{}, len(folders))
	for _, folder := range folders {
		key := strings.ToLower(strings.TrimSpace(folder.ID))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, folder)
	}
	return out
}

type xoauth2Client struct {
	username string
	token    string
}

func newXOAuth2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: strings.TrimSpace(username), token: strings.TrimSpace(token)}
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	if c.username == "" || c.token == "" {
		return "XOAUTH2", nil, errors.New("missing xoauth2 credentials")
	}
	payload := "user=" + c.username + "\x01auth=Bearer " + c.token + "\x01\x01"
	return "XOAUTH2", []byte(payload), nil
}

func (c *xoauth2Client) Next(_ []byte) ([]byte, error) {
	return nil, nil
}

type microsoftGraphHTTPError struct {
	statusCode int
	message    string
}

func (e *microsoftGraphHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("graph http %d: %s", e.statusCode, e.message)
}

func exchangeMicrosoftAccessToken(ctx context.Context, clientID, refreshToken, scopes, endpoint, proxyURL string, timeout time.Duration) (MicrosoftOAuthResult, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = defaultMicrosoftClientID
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return microsoftOAuthFailure("request", "Microsoft refresh token is missing.", false), nil
	}
	session, err := msacl.NewAPISession(ctx, proxyURL, timeoutSeconds(timeout))
	if err != nil {
		return microsoftOAuthFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(proxyURL) != ""), err
	}
	form := map[string]string{
		"client_id":     clientID,
		"refresh_token": refreshToken,
		"grant_type":    "refresh_token",
	}
	if strings.TrimSpace(scopes) != "" {
		form["scope"] = strings.TrimSpace(scopes)
	}
	var body map[string]any
	resp, err := session.PostFormJSON(endpoint, form, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Accept":       "application/json",
	}, &body)
	if err != nil {
		return microsoftOAuthFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(proxyURL) != ""), err
	}
	if resp.StatusCode != 200 {
		category, message, proxyFailure := classifyMicrosoftTokenFailure(resp.StatusCode, body)
		return microsoftOAuthFailure(category, message, proxyFailure), nil
	}
	accessToken := stringValue(body["access_token"])
	if accessToken == "" {
		return microsoftOAuthFailure("request", "Microsoft mail service is temporarily unavailable.", false), nil
	}
	rotatedRefreshToken := strings.TrimSpace(stringValue(body["refresh_token"]))
	if rotatedRefreshToken == "" {
		rotatedRefreshToken = refreshToken
	}
	return MicrosoftOAuthResult{
		Valid:        true,
		ClientID:     clientID,
		AccessToken:  accessToken,
		RefreshToken: rotatedRefreshToken,
	}, nil
}

func normalizeMailFetchRequest(req MicrosoftMailFetchRequest) MicrosoftMailFetchRequest {
	req.EmailAddress = strings.TrimSpace(req.EmailAddress)
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.ClientID == "" {
		req.ClientID = defaultMicrosoftClientID
	}
	req.RefreshToken = strings.TrimSpace(req.RefreshToken)
	req.AccessToken = strings.TrimSpace(req.AccessToken)
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	return req
}

func normalizeGraphFetchedMessage(message graphMessage, folder MicrosoftMailFolder, includeProviderPayload bool) MicrosoftFetchedMessage {
	receivedAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(message.ReceivedDateTime))
	body := strings.TrimSpace(message.Body.Content)
	if strings.EqualFold(message.Body.ContentType, "html") {
		body = stripHTMLForMSACL(body)
	}
	if body == "" {
		body = strings.TrimSpace(message.BodyPreview)
	}
	providerPayload := ""
	if includeProviderPayload {
		encoded, _ := json.Marshal(message)
		providerPayload = string(encoded)
	}
	return MicrosoftFetchedMessage{
		ID:                strings.TrimSpace(message.ID),
		InternetMessageID: strings.TrimSpace(message.InternetMessageID),
		FolderID:          folder.ID,
		FolderLabel:       folder.Label,
		Subject:           strings.TrimSpace(message.Subject),
		From:              formatGraphRecipientList([]graphRecipient{derefGraphRecipient(message.From)}),
		To:                formatGraphRecipientList(message.ToRecipients),
		ReceivedAt:        receivedAt,
		Preview:           strings.TrimSpace(message.BodyPreview),
		Body:              body,
		ProviderPayload:   providerPayload,
		Protocol:          "graph",
		HasAttachments:    message.HasAttachments,
	}
}

func derefGraphRecipient(recipient *graphRecipient) graphRecipient {
	if recipient == nil {
		return graphRecipient{}
	}
	return *recipient
}

func normalizeIMAPFetchedMessage(row *imapclient.FetchMessageBuffer, folder MicrosoftMailFolder, bodySection *imap.FetchItemBodySection, includeRawSource bool) MicrosoftFetchedMessage {
	if row == nil {
		return MicrosoftFetchedMessage{FolderID: folder.ID, FolderLabel: folder.Label, Protocol: "imap"}
	}
	message := MicrosoftFetchedMessage{
		ID:          fmt.Sprintf("%d", row.UID),
		FolderID:    folder.ID,
		FolderLabel: folder.Label,
		ReceivedAt:  row.InternalDate,
		Protocol:    "imap",
	}
	if row.Envelope != nil {
		message.InternetMessageID = strings.Trim(row.Envelope.MessageID, "<>")
		message.Subject = strings.TrimSpace(row.Envelope.Subject)
		message.From = formatIMAPAddressList(row.Envelope.From)
		message.To = formatIMAPAddressList(row.Envelope.To)
		if message.ReceivedAt.IsZero() {
			message.ReceivedAt = row.Envelope.Date
		}
	}
	if bodySection != nil {
		applyIMAPBody(&message, row.FindBodySection(bodySection), includeRawSource)
	}
	return message
}

func applyIMAPBody(message *MicrosoftFetchedMessage, raw []byte, includeRawSource bool) {
	if message == nil || len(raw) == 0 {
		return
	}
	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		if includeRawSource {
			message.RawSource = string(raw)
		}
		message.Body = strings.TrimSpace(string(raw))
		message.Preview = bodyPreview(message.Body)
		return
	}
	if includeRawSource {
		message.RawSource = string(raw)
	}
	decoder := new(mime.WordDecoder)
	if subject := decodeMIMEHeader(decoder, msg.Header.Get("Subject")); subject != "" {
		message.Subject = subject
	}
	if from := decodeMIMEHeader(decoder, msg.Header.Get("From")); from != "" {
		message.From = from
	}
	if to := decodeMIMEHeader(decoder, msg.Header.Get("To")); to != "" {
		message.To = to
	}
	if messageID := strings.Trim(strings.TrimSpace(msg.Header.Get("Message-Id")), "<>"); messageID != "" {
		message.InternetMessageID = messageID
	}
	body, _ := readMIMEBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	message.Body = strings.TrimSpace(body)
	message.Preview = bodyPreview(message.Body)
}

func inMicrosoftFetchWindow(receivedAt, sinceAt, untilAt time.Time) bool {
	if receivedAt.IsZero() {
		return true
	}
	if !sinceAt.IsZero() && receivedAt.Before(sinceAt) {
		return false
	}
	if !untilAt.IsZero() && receivedAt.After(untilAt) {
		return false
	}
	return true
}

func limitMicrosoftMessages(messages []MicrosoftFetchedMessage, limit int) []MicrosoftFetchedMessage {
	if limit <= 0 {
		return messages
	}
	if len(messages) <= limit {
		return messages
	}
	return messages[:limit]
}

func bodyPreview(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) <= 1000 {
		return value
	}
	return value[:1000]
}

func formatGraphRecipientList(recipients []graphRecipient) string {
	values := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		address := strings.TrimSpace(recipient.EmailAddress.Address)
		name := strings.TrimSpace(recipient.EmailAddress.Name)
		if address == "" && name == "" {
			continue
		}
		if name != "" && address != "" {
			values = append(values, name+" <"+address+">")
			continue
		}
		values = append(values, firstNonEmpty(address, name))
	}
	return strings.Join(values, ", ")
}

func formatIMAPAddressList(addresses []imap.Address) string {
	values := make([]string, 0, len(addresses))
	for i := range addresses {
		address := strings.TrimSpace(addresses[i].Addr())
		name := strings.TrimSpace(addresses[i].Name)
		if address == "" && name == "" {
			continue
		}
		if name != "" && address != "" {
			values = append(values, name+" <"+address+">")
			continue
		}
		values = append(values, firstNonEmpty(address, name))
	}
	return strings.Join(values, ", ")
}

func graphErrorMessage(page graphMessagesPage) string {
	if page.Error != nil && strings.TrimSpace(page.Error.Message) != "" {
		return page.Error.Message
	}
	for _, message := range page.Value {
		if message.Error != nil && strings.TrimSpace(message.Error.Message) != "" {
			return message.Error.Message
		}
	}
	data, _ := json.Marshal(page)
	return string(data)
}

func classifyMicrosoftGraphFailure(err error) (string, string, bool) {
	if err == nil {
		return "", "", false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "request", "Microsoft mail service is temporarily unavailable.", true
	}
	var httpErr *microsoftGraphHTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.statusCode == 401:
			return "graph_unauthorized", "Microsoft Graph access token is unauthorized or expired.", false
		case httpErr.statusCode == 403:
			return "graph_forbidden", "Microsoft Graph mailbox permission is not available.", false
		case httpErr.statusCode == 429 || httpErr.statusCode >= 500:
			return "request", "Microsoft mail service is temporarily unavailable.", true
		default:
			return "request", "Microsoft mail service is temporarily unavailable.", false
		}
	}
	return "request", "Microsoft mail service is temporarily unavailable.", true
}

func microsoftMailFetchFailure(category, message string, proxyFailure bool) MicrosoftMailFetchResult {
	return MicrosoftMailFetchResult{
		Valid:        false,
		Category:     category,
		SafeMessage:  message,
		ProxyFailure: proxyFailure,
		FolderCounts: map[string]int{},
	}
}

func mailFetchResultFromOAuth(result MicrosoftOAuthResult) MicrosoftMailFetchResult {
	return MicrosoftMailFetchResult{
		Valid:        result.Valid,
		RefreshToken: result.RefreshToken,
		Category:     result.Category,
		SafeMessage:  result.SafeMessage,
		ProxyFailure: result.ProxyFailure,
		FolderCounts: map[string]int{},
	}
}

func timeoutSeconds(timeout time.Duration) int {
	if timeout <= 0 {
		return 30
	}
	seconds := int(timeout / time.Second)
	if seconds <= 0 {
		return 30
	}
	return seconds
}
