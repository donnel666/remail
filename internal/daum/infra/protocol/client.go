package protocol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	stdmail "net/mail"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailtransport/netdial"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset" // Register legacy encodings for MIME headers.
	"github.com/emersion/go-message/mail"
)

type Client struct {
	dial func(context.Context, string) (net.Conn, error)
}

func NewClient() *Client { return &Client{} }

func (c *Client) Fetch(ctx context.Context, credentials Credentials, request FetchRequest) (FetchResult, error) {
	timeout := runtimeconfig.Duration("imap_operation_timeout_seconds", 60*time.Second, time.Second, 1)
	if request.FullHistory {
		timeout = runtimeconfig.Duration("imap_full_history_timeout_minutes", 15*time.Minute, time.Minute, 1)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if credentials.Email == "" || credentials.AppPassword == "" || strings.ContainsAny(credentials.Email+credentials.AppPassword, "\r\n\x00") {
		return FetchResult{}, &Failure{Category: "invalid_credentials", SafeMessage: "Daum APP password is required."}
	}
	// POP3 cannot establish full-folder history; use the same APP password over IMAP.
	if request.FullHistory || credentials.Protocol == "imap" || credentials.Protocol == "" {
		return c.fetchIMAP(ctx, credentials, request)
	}
	if credentials.Protocol == "pop3" {
		return c.fetchPOP3(ctx, credentials, request)
	}
	return FetchResult{}, &Failure{Category: "protocol", SafeMessage: "Unsupported Daum mail protocol."}
}
func (c *Client) connect(ctx context.Context, address, proxyURL, alpn string) (net.Conn, func(), error) {
	var conn net.Conn
	var err error
	if c.dial != nil {
		conn, err = c.dial(ctx, address)
	} else if strings.TrimSpace(proxyURL) == "" {
		conn, err = netdial.DialDirectTLSIPv4(ctx, address, alpn)
	} else {
		conn, err = netdial.DialTLS(ctx, proxyURL, address, alpn)
	}
	if err != nil {
		return nil, nil, failureAt(transportFailure(err), "connect_tls")
	}
	deadline := time.Now().Add(runtimeconfig.Duration("imap_operation_timeout_seconds", 60*time.Second, time.Second, 1))
	if end, ok := ctx.Deadline(); ok {
		deadline = end
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, nil, failureAt(transportFailure(err), "connect_tls")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return conn, func() { stop(); _ = conn.Close() }, nil
}
func transportFailure(err error) error {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	var response *imap.Error
	if errors.As(err, &response) {
		return authFailure(response)
	}
	return &Failure{Category: "transport", SafeMessage: "Daum mailbox connection is temporarily unavailable.", Retryable: true, Cause: err}
}
func authFailure(err error) error {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, io.EOF) {
		return transportFailure(err)
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "rate limit") || strings.Contains(text, "rate_limit") || strings.Contains(text, "too many") || strings.Contains(text, "limit exceeded") || strings.Contains(text, "connection limit") || strings.Contains(text, "[limit]") || strings.Contains(text, "in-use") || strings.Contains(text, "login delay") {
		return &Failure{Category: "rate_limited", SafeMessage: "Daum temporarily limited mailbox connections.", Retryable: true, Cause: err}
	}
	if strings.Contains(text, "disabled") || strings.Contains(text, "not enabled") {
		return &Failure{Category: "protocol_disabled", SafeMessage: "Enable IMAP/POP3 access in Daum mailbox settings.", Cause: err}
	}
	if strings.Contains(text, "unavailable") || strings.Contains(text, "temporar") || strings.Contains(text, "sys/temp") {
		return &Failure{Category: "provider_unavailable", SafeMessage: "Daum mailbox service is temporarily unavailable.", Retryable: true, Cause: err}
	}
	if strings.Contains(text, "authenticationfailed") || strings.Contains(text, "authentication failed") || strings.Contains(text, "invalid credential") || strings.Contains(text, "invalid password") || strings.Contains(text, "incorrect password") || strings.Contains(text, "wrong password") || strings.Contains(text, "bad password") || strings.Contains(text, "[auth]") || strings.Contains(text, "login failed") {
		return &Failure{Category: "invalid_credentials", SafeMessage: "Daum rejected the APP password.", Cause: err}
	}
	// Unknown rejection is not proof of invalid credentials for order refunds.
	return &Failure{Category: "protocol", SafeMessage: "Daum rejected mailbox authentication; verify the account and protocol settings.", Retryable: true, Cause: err}
}

type mailFolder struct{ ID, Label string }

func daumIMAPFolders(listed []*imap.ListData) []mailFolder {
	folders := []mailFolder{{ID: "INBOX", Label: "Inbox"}}
	var named []mailFolder
	for _, item := range listed {
		if item == nil || strings.EqualFold(item.Mailbox, "INBOX") {
			continue
		}
		selectable, junk := true, false
		for _, attr := range item.Attrs {
			selectable = selectable && attr != imap.MailboxAttrNoSelect
			junk = junk || attr == imap.MailboxAttrJunk
		}
		if !selectable {
			continue
		}
		folder := mailFolder{ID: item.Mailbox, Label: "Junk"}
		if junk {
			folders = append(folders, folder)
			continue
		}
		name := strings.ToLower(item.Mailbox)
		if strings.Contains(name, "junk") || strings.Contains(name, "spam") || strings.Contains(name, "스팸") {
			named = append(named, folder)
		}
	}
	folders = append(folders, named...)
	folders = append(folders, mailFolder{ID: "Junk", Label: "Junk"}, mailFolder{ID: "Junk Email", Label: "Junk"}, mailFolder{ID: "Spam", Label: "Junk"})
	seen := map[string]bool{}
	result := make([]mailFolder, 0, len(folders))
	for _, folder := range folders {
		key := strings.ToLower(folder.ID)
		if !seen[key] {
			result = append(result, folder)
			seen[key] = true
		}
	}
	return result
}

func (c *Client) fetchIMAP(ctx context.Context, credentials Credentials, request FetchRequest) (result FetchResult, err error) {
	conn, closeConn, err := c.connect(ctx, "imap.daum.net:993", credentials.ProxyURL, "imap")
	if err != nil {
		return result, err
	}
	defer closeConn()
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err = client.Login(credentials.Email, credentials.AppPassword).Wait(); err != nil {
		return result, authFailure(err)
	}
	listed, _ := client.List("", "*", nil).Collect()
	folders := daumIMAPFolders(listed)
	realtime := !request.FullHistory && request.MaxMessages > 0
	if realtime && request.Recipient == "" && len(request.KnownMessageIDs) > 0 {
		heads, complete, headErr := daumIMAPHeads(ctx, client, folders)
		if headErr != nil {
			return result, headErr
		}
		sort.SliceStable(heads, func(i, j int) bool { return heads[i].ReceivedAt.After(heads[j].ReceivedAt) })
		if complete && (len(heads) == 0 || messageKnown(request.KnownMessageIDs, heads[0])) {
			_ = client.Logout().Wait()
			return FetchResult{Complete: true}, nil
		}
	}
	completed := map[string]bool{}
	batch := make([]Message, 0, min(runtimeconfig.Int("mail_stream_batch_size", 100, 1), 1000))
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if !realtime && request.OnMessages != nil {
			if err := request.OnMessages(batch); err != nil {
				return err
			}
		} else {
			result.Messages = append(result.Messages, batch...)
		}
		batch = batch[:0]
		return nil
	}
	filter := request
	filter.KnownMessageIDs = nil
	for _, folder := range folders {
		if completed[folder.Label] {
			continue
		}
		if err = ctx.Err(); err != nil {
			return FetchResult{}, transportFailure(err)
		}
		selected, selectErr := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if selectErr != nil {
			continue
		}
		if selected.NumMessages == 0 {
			completed[folder.Label] = true
			continue
		}
		folderLimit := request.MaxMessages
		if request.FullHistory || request.Recipient != "" {
			folderLimit = 0
		}
		sequence, searchErr := mailbox.RecentIMAPSeqSet(ctx, client, selected.NumMessages, request.SinceAt, folderLimit)
		if searchErr != nil {
			return FetchResult{}, transportFailure(searchErr)
		}
		if len(sequence) == 0 {
			completed[folder.Label] = true
			continue
		}
		body := &imap.FetchItemBodySection{Peek: true}
		fetched := client.Fetch(sequence, &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{body}})
		for data := fetched.Next(); data != nil; data = fetched.Next() {
			buffered, readErr := data.Collect()
			if readErr != nil {
				_ = fetched.Close()
				return FetchResult{}, transportFailure(readErr)
			}
			if (!request.SinceAt.IsZero() && buffered.InternalDate.Before(request.SinceAt)) || (!request.UntilAt.IsZero() && buffered.InternalDate.After(request.UntilAt)) {
				continue
			}
			id := fmt.Sprintf("imap:%s:%d:%d", folder.ID, selected.UIDValidity, buffered.UID)
			message, parseErr := parseMessage(buffered.FindBodySection(body), id, folder.ID, buffered.InternalDate)
			if parseErr != nil {
				_ = fetched.Close()
				return FetchResult{}, parseErr
			}
			if !acceptsMessage(filter, message) {
				continue
			}
			if request.FullHistory && messageKnown(request.KnownMessageIDs, message) {
				continue
			}
			batch = append(batch, message)
			if len(batch) == cap(batch) {
				if err = flush(); err != nil {
					_ = fetched.Close()
					return FetchResult{}, err
				}
			}
		}
		if err = fetched.Close(); err != nil {
			return FetchResult{}, transportFailure(err)
		}
		if err = flush(); err != nil {
			return FetchResult{}, err
		}
		completed[folder.Label] = true
	}
	_ = client.Logout().Wait()
	if !completed["Inbox"] || !completed["Junk"] {
		return FetchResult{}, &Failure{Category: "incomplete_history", SafeMessage: "Inbox and Junk must both be read completely.", Retryable: true}
	}
	if realtime {
		if err = finishRecentFetch(&result, request); err != nil {
			return FetchResult{}, err
		}
	}
	result.Complete = true
	return result, nil
}

func daumIMAPHeads(ctx context.Context, client *imapclient.Client, folders []mailFolder) ([]Message, bool, error) {
	var heads []Message
	completed := map[string]bool{}
	for _, folder := range folders {
		if completed[folder.Label] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false, transportFailure(err)
		}
		selected, err := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			continue
		}
		completed[folder.Label] = true
		if selected.NumMessages == 0 {
			continue
		}
		sequence, err := mailbox.RecentIMAPSeqSet(ctx, client, selected.NumMessages, time.Time{}, 1)
		if err != nil || len(sequence) == 0 {
			return nil, false, err
		}
		rows, err := client.Fetch(sequence, &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
		if err != nil {
			return nil, false, transportFailure(err)
		}
		for _, row := range rows {
			message := Message{ID: fmt.Sprintf("imap:%s:%d:%d", folder.ID, selected.UIDValidity, row.UID), Folder: folder.ID, ReceivedAt: row.InternalDate}
			if row.Envelope != nil {
				message.ExternalID = strings.Trim(row.Envelope.MessageID, "<>")
			}
			heads = append(heads, message)
		}
	}
	return heads, completed["Inbox"] && completed["Junk"], nil
}

func finishRecentFetch(result *FetchResult, request FetchRequest) error {
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt) })
	for index, message := range result.Messages {
		if messageKnown(request.KnownMessageIDs, message) {
			result.Messages = result.Messages[:index]
			break
		}
	}
	if request.MaxMessages > 0 && len(result.Messages) > request.MaxMessages {
		result.Messages = result.Messages[:request.MaxMessages]
	}
	if request.OnMessages != nil && len(result.Messages) > 0 {
		if err := request.OnMessages(result.Messages); err != nil {
			return err
		}
		result.Messages = nil
	}
	return nil
}

func messageKnown(known []string, message Message) bool {
	provider := "provider:" + strings.ToLower(strings.SplitN(message.ID, ":", 2)[0]) + ":" + strings.ToLower(message.Folder) + ":" + strings.ToLower(message.ID)
	internet := "internet:" + strings.ToLower(strings.Trim(strings.TrimSpace(message.ExternalID), "<>"))
	for _, value := range known {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == provider || key == strings.ToLower(message.ID) || (message.ExternalID != "" && key == internet) {
			return true
		}
		if message.RawHash != "" && (strings.HasPrefix(key, "provider:imap:") || strings.HasPrefix(key, "provider:pop3:")) && strings.HasSuffix(key, ":sha256:"+message.RawHash) {
			return true
		}
	}
	return false
}

func readPOP3OK(session *textproto.Conn) (string, error) {
	line, err := session.ReadLine()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "+OK") {
		return "", authFailure(errors.New(line))
	}
	return line, nil
}

func pop3Command(session *textproto.Conn, format string, args ...any) (string, error) {
	if err := session.PrintfLine(format, args...); err != nil {
		return "", err
	}
	return readPOP3OK(session)
}

func (c *Client) openPOP3(ctx context.Context, credentials Credentials) (*textproto.Conn, func(), error) {
	conn, closeConn, err := c.connect(ctx, "pop.daum.net:995", credentials.ProxyURL, "pop3")
	if err != nil {
		return nil, nil, err
	}
	session := textproto.NewConn(conn)
	if _, err := readPOP3OK(session); err != nil {
		closeConn()
		return nil, nil, failureAt(transportFailure(err), "greeting")
	}
	for _, command := range []struct{ format, value string }{{"USER %s", credentials.Email}, {"PASS %s", credentials.AppPassword}} {
		if _, err := pop3Command(session, command.format, command.value); err != nil {
			closeConn()
			return nil, nil, failureAt(authFailure(err), "authenticate")
		}
	}
	return session, closeConn, nil
}

func (c *Client) fetchPOP3(ctx context.Context, credentials Credentials, request FetchRequest) (result FetchResult, err error) {
	text, closeConn, err := c.openPOP3(ctx, credentials)
	if err != nil {
		return result, err
	}
	defer closeConn()
	command := func(format string, args ...any) (string, error) { return pop3Command(text, format, args...) }
	if _, err = command("STAT"); err != nil {
		return result, transportFailure(err)
	}
	if _, err = command("LIST"); err != nil {
		return result, transportFailure(err)
	}
	listing, err := readPOP3Lines(text)
	if err != nil {
		return result, transportFailure(err)
	}
	sizes := make(map[int]int)
	for _, line := range listing {
		var n, size int
		if _, err := fmt.Sscanf(line, "%d %d", &n, &size); err != nil || n < 1 || size < 0 {
			return result, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing."}
		}
		sizes[n] = size
	}
	if _, err = command("UIDL"); err != nil {
		return result, transportFailure(err)
	}
	listing, err = readPOP3Lines(text)
	if err != nil {
		return result, transportFailure(err)
	}
	type item struct {
		number int
		uid    string
	}
	items := make([]item, 0, len(listing))
	seen := make(map[string]bool)
	for _, line := range listing {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return result, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing."}
		}
		n, e := strconv.Atoi(fields[0])
		if e != nil || n < 1 || seen[fields[1]] {
			return result, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing."}
		}
		seen[fields[1]] = true
		items = append(items, item{n, fields[1]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].number > items[j].number })
	emitted := 0
	known := make(map[string]bool)
	for _, id := range request.KnownMessageIDs {
		known[strings.ToLower(strings.TrimSpace(id))] = true
	}
	for _, item := range items {
		id := "pop3:" + item.uid
		if known[strings.ToLower(id)] || known[strings.ToLower("provider:pop3:inbox:"+id)] {
			break
		}
		if _, ok := sizes[item.number]; !ok {
			return result, &Failure{Category: "protocol", SafeMessage: "Daum returned an incomplete POP3 listing.", Retryable: true}
		}
		if _, err = command("RETR %d", item.number); err != nil {
			return result, transportFailure(err)
		}
		raw, readErr := io.ReadAll(text.DotReader())
		if readErr != nil {
			return result, transportFailure(readErr)
		}
		message, parseErr := parseMessage(raw, id, "INBOX", time.Time{})
		if parseErr != nil {
			return result, parseErr
		}
		if messageKnown(request.KnownMessageIDs, message) {
			break
		}
		if !acceptsMessage(request, message) {
			continue
		}
		result.Messages = append(result.Messages, message)
		emitted++
		if request.MaxMessages > 0 && emitted >= request.MaxMessages {
			break
		}
	}
	// No DELE command: original server messages always remain available.
	_, _ = command("QUIT")
	if err = finishRecentFetch(&result, request); err != nil {
		return FetchResult{}, err
	}
	result.Complete = true
	return result, nil
}
func readPOP3Lines(text *textproto.Conn) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(text.DotReader(), (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<20 {
		return nil, &Failure{Category: "protocol", SafeMessage: "Daum returned an oversized POP3 listing."}
	}
	reader := bufio.NewScanner(bytes.NewReader(raw))
	var lines []string
	for reader.Scan() {
		if len(lines) >= 500000 {
			return nil, &Failure{Category: "protocol", SafeMessage: "Daum returned an oversized POP3 listing."}
		}
		lines = append(lines, reader.Text())
	}
	if err := reader.Err(); err != nil {
		return nil, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing.", Cause: err}
	}
	return lines, nil
}
func acceptsMessage(request FetchRequest, message Message) bool {
	if !request.SinceAt.IsZero() && message.ReceivedAt.Before(request.SinceAt) {
		return false
	}
	if !request.UntilAt.IsZero() && message.ReceivedAt.After(request.UntilAt) {
		return false
	}
	if request.Recipient != "" {
		matched := false
		for _, list := range [][]Address{message.ToList, message.CCList, message.BCCList} {
			for _, address := range list {
				matched = matched || strings.EqualFold(address.Address, request.Recipient)
			}
		}
		if !matched {
			return false
		}
	}
	return !messageKnown(request.KnownMessageIDs, message)
}

func parseMessage(raw []byte, id, folder string, received time.Time) (Message, error) {
	digest := sha256.Sum256(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	message := Message{ID: id, Folder: folder, ReceivedAt: received, MIMEType: "text/plain", RawHash: hex.EncodeToString(digest[:])}
	source, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		message.Body = string(raw)
		return message, nil
	}
	header := mail.HeaderFromMap(source.Header)
	message.Subject, _ = header.Subject()
	message.ExternalID, _ = header.MessageID()
	if received.IsZero() {
		message.ReceivedAt, _ = header.Date()
	}
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		headerEnd = bytes.Index(raw, []byte("\n\n"))
	}
	if headerEnd >= 0 {
		message.Header = string(raw[:headerEnd])
	}
	if senders, err := header.AddressList("From"); err == nil && len(senders) > 0 {
		message.Sender = Address{Name: senders[0].Name, Address: senders[0].Address}
	}
	originalTo, _ := header.AddressList("To")
	message.OriginalToCount = len(originalTo)
	seen := make(map[string]bool)
	for _, field := range []struct {
		name   string
		target *[]Address
	}{{"To", &message.ToList}, {"Cc", &message.CCList}, {"Bcc", &message.BCCList}} {
		addresses, err := header.AddressList(field.name)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			email := strings.ToLower(strings.TrimSpace(address.Address))
			if email == "" || seen[email] {
				continue
			}
			seen[email] = true
			*field.target = append(*field.target, Address{Name: address.Name, Address: email})
		}
	}
	// Only use delivery headers when explicit recipients are absent: Daum may
	// rewrite envelope addresses to the primary account behind an alias.
	if len(seen) == 0 {
		for _, field := range []string{"Delivered-To", "X-Original-To"} {
			addresses, err := header.AddressList(field)
			if err != nil {
				continue
			}
			for _, address := range addresses {
				email := strings.ToLower(strings.TrimSpace(address.Address))
				if email == "" || seen[email] {
					continue
				}
				seen[email] = true
				message.ToList = append(message.ToList, Address{Name: address.Name, Address: email})
			}
		}
	}
	// Match the Microsoft MIME fallback: bad content is not failed account authentication.
	body, isHTML, _, _ := mailbox.ReadMIMEBodyPart(source.Header.Get("Content-Type"), source.Header.Get("Content-Transfer-Encoding"), source.Body)
	message.Body = body
	if isHTML {
		message.MIMEType = "text/html"
	}
	return message, nil
}
