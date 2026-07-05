package infra

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

const (
	microsoftGraphAPIBase        = "https://graph.microsoft.com/v1.0"
	microsoftGraphMessagePageTop = 100
	microsoftIMAPTokenURL        = "https://login.live.com/oauth20_token.srf"
	outlookIMAPAddress           = "outlook.office365.com:993"
)

var defaultMicrosoftMailFolders = []MicrosoftMailFolder{
	{ID: "inbox", Label: "Inbox"},
	{ID: "junkemail", Label: "Junk"},
}

type MicrosoftMailFetchClient struct {
	timeout           time.Duration
	graphFetch        func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error)
	exchangeIMAPToken func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error)
}

type MicrosoftMailFetchRequest struct {
	EmailAddress string
	ClientID     string
	RefreshToken string
	AccessToken  string
	ProxyURL     string
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

// TODO(P1-I3/mailmatch): Resource health validation stops after RT + mail
// fetch succeeds. When Project and MailMatch are ready, pass Messages to that
// bounded context to match project rules and insert binding relationships.

type MicrosoftMailFolder struct {
	ID    string
	Label string
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
	Protocol          string
	HasAttachments    bool
}

type graphMessagesPage struct {
	Value    []graphMessage      `json:"value"`
	NextLink string              `json:"@odata.nextLink"`
	Error    *graphErrorEnvelope `json:"error"`
}

type graphMessage struct {
	ID                string              `json:"id"`
	InternetMessageID string              `json:"internetMessageId"`
	Subject           string              `json:"subject"`
	BodyPreview       string              `json:"bodyPreview"`
	From              *graphRecipient     `json:"from"`
	ToRecipients      []graphRecipient    `json:"toRecipients"`
	ReceivedDateTime  string              `json:"receivedDateTime"`
	HasAttachments    bool                `json:"hasAttachments"`
	Error             *graphErrorEnvelope `json:"error"`
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
	graphResult, graphErr := c.fetchGraphAll(ctx, req)
	if graphErr == nil && graphResult.Valid {
		return graphResult, nil
	}
	graphSafeError := graphResult.SafeMessage
	if graphSafeError == "" {
		graphSafeError = "Microsoft mail service is temporarily unavailable."
	}

	if imapFallback != nil {
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
				tokenResult = microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false)
			} else {
				tokenResult = imapResult
			}
		}
		if tokenResult.SafeMessage != "" {
			tokenResult.GraphSafeError = graphSafeError
			tokenResult.FallbackFrom = "graph"
			if graphResult.ProxyFailure {
				tokenResult.ProxyFailure = true
			}
			return tokenResult, nil
		}
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
	session, err := msacl.NewAPISession(ctx, req.ProxyURL, timeoutSeconds(c.timeout))
	if err != nil {
		return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", strings.TrimSpace(req.ProxyURL) != ""), err
	}
	result := MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "graph",
		RefreshToken: refreshToken,
		FolderCounts: map[string]int{},
	}
	for _, folder := range defaultMicrosoftMailFolders {
		messages, err := fetchGraphFolderMessages(ctx, session, accessToken, folder)
		if err != nil {
			category, message, proxyFailure := classifyMicrosoftGraphFailure(err)
			return microsoftMailFetchFailure(category, message, proxyFailure), nil
		}
		result.FolderCounts[folder.Label] = len(messages)
		result.MessageCount += len(messages)
		result.Messages = append(result.Messages, messages...)
	}
	sort.SliceStable(result.Messages, func(i, j int) bool {
		return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt)
	})
	return result, nil
}

func fetchGraphFolderMessages(ctx context.Context, session *msacl.Session, accessToken string, folder MicrosoftMailFolder) ([]MicrosoftFetchedMessage, error) {
	selectFields := "$select=id,internetMessageId,subject,bodyPreview,from,toRecipients,receivedDateTime,hasAttachments"
	nextURL := fmt.Sprintf("%s/me/mailFolders/%s/messages?$top=%d&$orderby=receivedDateTime%%20desc&%s",
		microsoftGraphAPIBase,
		url.PathEscape(folder.ID),
		microsoftGraphMessagePageTop,
		selectFields,
	)
	headers := map[string]string{
		"Authorization":    "Bearer " + accessToken,
		"Accept":           "application/json",
		"ConsistencyLevel": "eventual",
	}
	messages := make([]MicrosoftFetchedMessage, 0)
	for strings.TrimSpace(nextURL) != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page graphMessagesPage
		resp, err := session.GetJSON(nextURL, headers, &page)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &microsoftGraphHTTPError{
				statusCode: resp.StatusCode,
				message:    graphErrorMessage(page),
			}
		}
		for _, message := range page.Value {
			messages = append(messages, normalizeGraphFetchedMessage(message, folder))
		}
		nextURL = strings.TrimSpace(page.NextLink)
	}
	return messages, nil
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
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	client, err := imapclient.DialTLS(outlookIMAPAddress, &imapclient.Options{
		Dialer:    dialer,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "outlook.office365.com"},
	})
	if err != nil {
		return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), err
	}
	defer client.Close()

	if err := client.Authenticate(newXOAuth2Client(req.EmailAddress, accessToken)); err != nil {
		_ = client.Logout().Wait()
		return microsoftMailFetchFailure("imap_auth_failed", "Microsoft IMAP authentication failed.", false), err
	}

	result := MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		RefreshToken: req.RefreshToken,
		FolderCounts: map[string]int{},
	}
	selectedFolders := 0
	for _, folder := range imapCandidateFolders(client) {
		if err := ctx.Err(); err != nil {
			_ = client.Logout().Wait()
			return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), err
		}
		selectData, err := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			continue
		}
		selectedFolders++
		count := int(selectData.NumMessages)
		result.FolderCounts[folder.Label] += count
		result.MessageCount += count
		if count == 0 {
			continue
		}
		seqSet := imap.SeqSet{}
		seqSet.AddRange(1, selectData.NumMessages)
		rows, err := client.Fetch(seqSet, &imap.FetchOptions{
			Envelope:     true,
			InternalDate: true,
			UID:          true,
		}).Collect()
		if err != nil {
			continue
		}
		for _, row := range rows {
			result.Messages = append(result.Messages, normalizeIMAPFetchedMessage(row, folder))
		}
	}
	_ = client.Logout().Wait()
	if selectedFolders == 0 {
		return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), nil
	}
	sort.SliceStable(result.Messages, func(i, j int) bool {
		return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt)
	})
	return result, nil
}

func imapCandidateFolders(client *imapclient.Client) []MicrosoftMailFolder {
	folders := []MicrosoftMailFolder{
		{ID: "INBOX", Label: "Inbox"},
		{ID: "Junk", Label: "Junk"},
		{ID: "Junk Email", Label: "Junk"},
	}
	if client == nil {
		return folders
	}
	listed, err := client.List("", "*", nil).Collect()
	if err != nil {
		return folders
	}
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

func normalizeGraphFetchedMessage(message graphMessage, folder MicrosoftMailFolder) MicrosoftFetchedMessage {
	receivedAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(message.ReceivedDateTime))
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

func normalizeIMAPFetchedMessage(row *imapclient.FetchMessageBuffer, folder MicrosoftMailFolder) MicrosoftFetchedMessage {
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
	return message
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
