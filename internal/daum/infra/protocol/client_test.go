package protocol

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/require"
)

const koreanMail = "From: sender@example.test\r\nTo: account@hanmail2.net\r\nSubject: =?UTF-8?B?7J247Kad?=\r\nDate: Sat, 17 Oct 2026 12:00:00 +0000\r\nMessage-ID: <same-message@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n7J247KadIOy9lOuTnCAxMjM0NTY=\r\n"

func scriptedClient(t *testing.T, run func(net.Conn) error) (*Client, *[]string) {
	t.Helper()
	var endpoints []string
	return &Client{dial: func(_ context.Context, address string) (net.Conn, error) {
		endpoints = append(endpoints, address)
		client, server := net.Pipe()
		done := make(chan error, 1)
		go func() { defer server.Close(); done <- run(server) }()
		t.Cleanup(func() {
			_ = client.Close()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Error("protocol fixture did not exit")
			}
		})
		return client, nil
	}}, &endpoints
}
func imapScript(commands *[]string, deny bool, raws ...string) func(net.Conn) error {
	arrival := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	messages := []imapScriptMessage{{uid: 7, raw: koreanMail, receivedAt: arrival}}
	if len(raws) > 0 {
		messages = make([]imapScriptMessage, len(raws))
		for i, raw := range raws {
			messages[i] = imapScriptMessage{uid: uint32(i + 1), raw: raw, receivedAt: arrival.Add(time.Duration(i) * time.Minute)}
		}
	}
	return imapMailboxScript(commands, deny, false, map[string][]imapScriptMessage{"INBOX": messages, "Spam": nil})
}

type imapScriptMessage struct {
	uid        uint32
	raw        string
	receivedAt time.Time
}

// FETCH replies deliberately use ascending sequence order, regardless of the
// order requested or returned by SORT. IMAP does not promise descending replies.
func imapMailboxScript(commands *[]string, deny, supportsSort bool, mailboxes map[string][]imapScriptMessage) func(net.Conn) error {
	return func(conn net.Conn) error {
		capabilities := "IMAP4rev1"
		if supportsSort {
			capabilities += " SORT"
		}
		if _, err := fmt.Fprintf(conn, "* OK [CAPABILITY %s] Daum fixture\r\n", capabilities); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		folder := ""
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					return nil
				}
				return err
			}
			*commands = append(*commands, strings.TrimSpace(line))
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return errors.New("invalid IMAP command")
			}
			tag := fields[0]
			commandIndex := 1
			uidCommand := strings.EqualFold(fields[1], "UID")
			if uidCommand {
				commandIndex++
			}
			switch strings.ToUpper(fields[commandIndex]) {
			case "CAPABILITY":
				_, err = fmt.Fprintf(conn, "* CAPABILITY %s\r\n%s OK capabilities\r\n", capabilities, tag)
			case "LOGIN":
				if deny {
					_, err = fmt.Fprintf(conn, "%s NO [AUTHENTICATIONFAILED] do not reveal password secret\r\n", tag)
				} else {
					_, err = fmt.Fprintf(conn, "%s OK authenticated\r\n", tag)
				}
			case "LIST":
				_, err = fmt.Fprintf(conn, "* LIST () \"/\" \"INBOX\"\r\n* LIST (\\Junk) \"/\" \"Spam\"\r\n* LIST () \"/\" \"Sent\"\r\n%s OK listed\r\n", tag)
			case "EXAMINE":
				folder = strings.Trim(fields[2], "\"")
				messages, exists := mailboxes[folder]
				if !exists {
					_, err = fmt.Fprintf(conn, "%s NO [NONEXISTENT] unknown mailbox\r\n", tag)
					break
				}
				_, err = fmt.Fprintf(conn, "* %d EXISTS\r\n* OK [UIDVALIDITY 99] uid validity\r\n%s OK [READ-ONLY] selected\r\n", len(messages), tag)
			case "FETCH":
				messages := mailboxes[folder]
				maximum := uint32(len(messages))
				if uidCommand && len(messages) > 0 {
					maximum = messages[len(messages)-1].uid
				}
				for index, message := range messages {
					identifier := uint32(index + 1)
					if uidCommand {
						identifier = message.uid
					}
					selected, setErr := imapScriptSetContains(fields[commandIndex+1], identifier, maximum)
					if setErr != nil {
						return setErr
					}
					if !selected {
						continue
					}
					messageID := ""
					for _, header := range strings.Split(message.raw, "\r\n") {
						if strings.HasPrefix(header, "Message-ID:") {
							messageID = strings.TrimSpace(strings.TrimPrefix(header, "Message-ID:"))
						}
					}
					body := ""
					if strings.Contains(strings.ToUpper(line), "BODY.PEEK[") {
						body = fmt.Sprintf(" BODY[] {%d}\r\n%s", len(message.raw), message.raw)
					}
					if _, err = fmt.Fprintf(conn, "* %d FETCH (UID %d INTERNALDATE %q ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL %q)%s)\r\n", index+1, message.uid, message.receivedAt.Format("02-Jan-2006 15:04:05 -0700"), messageID, body); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintf(conn, "%s OK fetched\r\n", tag)
			case "SEARCH", "SORT":
				kind := strings.ToUpper(fields[commandIndex])
				if kind == "SORT" && (!supportsSort || !strings.Contains(strings.ToUpper(line), "REVERSE ARRIVAL")) {
					return errors.New("unexpected SORT command")
				}
				var since time.Time
				for index, field := range fields {
					if strings.EqualFold(field, "SINCE") && index+1 < len(fields) {
						since, err = time.Parse("2-Jan-2006", strings.Trim(fields[index+1], "\"\r\n"))
						if err != nil {
							return err
						}
					}
				}
				messages := mailboxes[folder]
				var indices []int
				for index, message := range messages {
					if since.IsZero() || !message.receivedAt.Before(since) {
						indices = append(indices, index)
					}
				}
				if kind == "SORT" {
					sort.SliceStable(indices, func(i, j int) bool { return messages[indices[i]].receivedAt.After(messages[indices[j]].receivedAt) })
				}
				var ids []string
				for _, index := range indices {
					identifier := uint32(index + 1)
					if uidCommand {
						identifier = messages[index].uid
					}
					ids = append(ids, fmt.Sprint(identifier))
				}
				_, err = fmt.Fprintf(conn, "* %s %s\r\n%s OK searched\r\n", kind, strings.Join(ids, " "), tag)
			case "LOGOUT":
				_, err = fmt.Fprintf(conn, "* BYE done\r\n%s OK logout\r\n", tag)
				return err
			default:
				return fmt.Errorf("unexpected command %s", fields[1])
			}
			if err != nil {
				return err
			}
		}
	}
}

func imapScriptSetContains(set string, value, maximum uint32) (bool, error) {
	number := func(raw string) (uint32, error) {
		if raw == "*" {
			return maximum, nil
		}
		parsed, err := strconv.ParseUint(raw, 10, 32)
		return uint32(parsed), err
	}
	for _, entry := range strings.Split(set, ",") {
		ends := strings.Split(entry, ":")
		start, err := number(ends[0])
		if err != nil || len(ends) > 2 {
			return false, fmt.Errorf("invalid fixture sequence set %q", set)
		}
		end := start
		if len(ends) == 2 {
			end, err = number(ends[1])
			if err != nil {
				return false, err
			}
		}
		if value >= min(start, end) && value <= max(start, end) {
			return true, nil
		}
	}
	return false, nil
}
func TestIMAPUsesAppPasswordReadOnlyPeekAndRealJunkFolder(t *testing.T) {
	var commands []string
	client, endpoints := scriptedClient(t, imapScript(&commands, false))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "app secret", Protocol: "imap"}, FetchRequest{Recipient: "account@hanmail2.net", MaxMessages: 10})
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Len(t, result.Messages, 1)
	message := result.Messages[0]
	require.Equal(t, "인증", message.Subject)
	require.Contains(t, message.Body, "123456")
	require.Equal(t, "imap:INBOX:99:7", message.ID)
	require.Len(t, message.RawHash, 64)
	require.Equal(t, []string{"imap.daum.net:993"}, *endpoints)
	all := strings.Join(commands, "\n")
	require.Contains(t, all, "LOGIN")
	require.Contains(t, all, "app secret")
	require.Contains(t, all, "EXAMINE")
	require.Contains(t, all, "Spam")
	require.Contains(t, all, "BODY.PEEK[]")
	require.NotContains(t, all, " STORE ")
	require.NotContains(t, all, " EXPUNGE")
}
func TestIMAPHistoryRequiresFullFolderProtocolAndSkipsKnownIdentities(t *testing.T) {
	for _, known := range []string{"internet:same-message@example.test", "provider:imap:inbox:imap:inbox:99:7"} {
		t.Run(known, func(t *testing.T) {
			var commands []string
			client, endpoints := scriptedClient(t, imapScript(&commands, false))
			result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "app", Protocol: "pop3"}, FetchRequest{FullHistory: true, KnownMessageIDs: []string{known}})
			require.NoError(t, err)
			require.Empty(t, result.Messages)
			require.Equal(t, []string{"imap.daum.net:993"}, *endpoints)
		})
	}
}
func TestAuthenticationFailureIsSafeAndNotRetryable(t *testing.T) {
	var commands []string
	client, _ := scriptedClient(t, imapScript(&commands, true))
	err := client.Validate(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "secret", Protocol: "imap"})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "invalid_credentials", failure.Category)
	require.False(t, failure.Retryable)
	require.NotContains(t, err.Error(), "secret")
}
func pop3Script(commands *[]string, raws ...string) func(net.Conn) error {
	if len(raws) == 0 {
		raws = []string{koreanMail}
	}
	return func(conn net.Conn) error {
		if _, err := io.WriteString(conn, "+OK Daum POP3 fixture\r\n"); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			line = strings.TrimSpace(line)
			*commands = append(*commands, line)
			switch {
			case strings.HasPrefix(line, "USER "), strings.HasPrefix(line, "PASS "):
				_, err = io.WriteString(conn, "+OK authenticated\r\n")
			case line == "STAT":
				_, err = fmt.Fprintf(conn, "+OK %d %d\r\n", len(raws), len(raws)*len(koreanMail))
			case line == "LIST":
				var list strings.Builder
				list.WriteString("+OK listing\r\n")
				for i, raw := range raws {
					fmt.Fprintf(&list, "%d %d\r\n", i+1, len(raw))
				}
				list.WriteString(".\r\n")
				_, err = io.WriteString(conn, list.String())
			case line == "UIDL":
				var list strings.Builder
				list.WriteString("+OK listing\r\n1 stable-id\r\n")
				for i := 1; i < len(raws); i++ {
					fmt.Fprintf(&list, "%d stable-%d\r\n", i+1, i+1)
				}
				list.WriteString(".\r\n")
				_, err = io.WriteString(conn, list.String())
			case strings.HasPrefix(line, "RETR "):
				index, e := strconv.Atoi(strings.TrimPrefix(line, "RETR "))
				if e != nil || index < 1 || index > len(raws) {
					return errors.New("invalid fixture RETR")
				}
				_, err = io.WriteString(conn, "+OK message\r\n"+raws[index-1]+".\r\n")
			case line == "QUIT":
				_, err = io.WriteString(conn, "+OK goodbye\r\n")
				return err
			default:
				return fmt.Errorf("unexpected POP3 command %q", line)
			}
			if err != nil {
				return err
			}
		}
	}
}
func TestPOP3ReadsUIDLPreservesServerMailAndNormalizesMimeHash(t *testing.T) {
	var commands []string
	client, endpoints := scriptedClient(t, pop3Script(&commands))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "app secret", Protocol: "pop3"}, FetchRequest{MaxMessages: 5})
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	require.Equal(t, "pop3:stable-id", result.Messages[0].ID)
	require.Equal(t, []string{"pop.daum.net:995"}, *endpoints)
	original, err := parseMessage([]byte(koreanMail), "imap:1", "INBOX", time.Time{})
	require.NoError(t, err)
	require.Equal(t, original.RawHash, result.Messages[0].RawHash)
	all := strings.Join(commands, "\n")
	require.Contains(t, all, "PASS app secret")
	require.Contains(t, all, "UIDL")
	require.Contains(t, all, "RETR 1")
	require.NotContains(t, all, "DELE")
}
func TestExactRecipientsNeverInventAliasesAndFallbackHashDedupesProtocols(t *testing.T) {
	raw := strings.Replace(koreanMail, "To: account@hanmail2.net", "To: account@hanmail2.net\r\nCc: account@hanmail.net", 1)
	message, err := parseMessage([]byte(raw), "id", "INBOX", time.Time{})
	require.NoError(t, err)
	for _, tc := range []struct {
		recipient string
		want      int
	}{{"account@hanmail.net", 1}, {"account@hanmail2.net", 1}, {"account@daum.net", 0}} {
		require.Equal(t, tc.want == 1, acceptsMessage(FetchRequest{Recipient: tc.recipient}, message))
	}
	message.ExternalID = ""
	require.False(t, acceptsMessage(FetchRequest{KnownMessageIDs: []string{"provider:pop3:inbox:sha256:" + message.RawHash}}, message))
	unknown := strings.Replace(koreanMail, "To: account@hanmail2.net\r\n", "", 1)
	message, err = parseMessage([]byte(unknown), "unknown", "INBOX", time.Time{})
	require.NoError(t, err)
	require.False(t, acceptsMessage(FetchRequest{Recipient: "account@daum.net"}, message))
}

func TestIMAPLimitCountsMatchingMessagesAfterOtherAlias(t *testing.T) {
	var commands []string
	newerOtherAlias := strings.Replace(koreanMail, "account@hanmail2.net", "account@hanmail.net", 1)
	client, _ := scriptedClient(t, imapScript(&commands, false, koreanMail, newerOtherAlias))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "app", Protocol: "imap"}, FetchRequest{Recipient: "account@hanmail2.net", MaxMessages: 1})
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	require.Equal(t, "account@hanmail2.net", result.Messages[0].ToList[0].Address)
}

func TestPOP3LimitCountsMatchingMessagesAfterOtherAlias(t *testing.T) {
	var commands []string
	newerOtherAlias := strings.Replace(koreanMail, "account@hanmail2.net", "account@hanmail.net", 1)
	client, _ := scriptedClient(t, pop3Script(&commands, koreanMail, newerOtherAlias))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "app", Protocol: "pop3"}, FetchRequest{Recipient: "account@hanmail2.net", MaxMessages: 1})
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	require.Equal(t, "account@hanmail2.net", result.Messages[0].ToList[0].Address)
	require.Contains(t, strings.Join(commands, "\n"), "RETR 2")
	require.Contains(t, strings.Join(commands, "\n"), "RETR 1")
}

func TestAuthClassificationDoesNotTreatTemporaryOrUnknownRejectionAsBadPassword(t *testing.T) {
	for _, tc := range []struct {
		message, category string
		retryable         bool
	}{{"-ERR [AUTH] authentication failed", "invalid_credentials", false}, {"-ERR protocol is disabled", "protocol_disabled", false}, {"-ERR [IN-USE] too many sessions", "rate_limited", true}, {"-ERR [SYS/TEMP] service unavailable", "provider_unavailable", true}, {"-ERR access denied", "protocol", true}} {
		var failure *Failure
		require.ErrorAs(t, authFailure(errors.New(tc.message)), &failure)
		require.Equal(t, tc.category, failure.Category)
		require.Equal(t, tc.retryable, failure.Retryable)
	}
}

func TestProviderRepliesNeverBecomeProxyTransportFailures(t *testing.T) {
	for _, reply := range []struct {
		response  *imap.Error
		category  string
		retryable bool
	}{
		{&imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCode("UNAVAILABLE"), Text: "Temporary server failure"}, "provider_unavailable", true},
		{&imap.Error{Type: imap.StatusResponseTypeNo, Text: "Mailbox cannot be selected"}, "protocol", true},
		{&imap.Error{Type: imap.StatusResponseTypeNo, Text: "Rate limit exceeded"}, "rate_limited", true},
	} {
		var failure *Failure
		require.ErrorAs(t, transportFailure(reply.response), &failure)
		require.Equal(t, reply.category, failure.Category)
		require.Equal(t, reply.retryable, failure.Retryable)
	}
	client, _ := scriptedClient(t, func(conn net.Conn) error {
		if _, err := io.WriteString(conn, "+OK ready\r\n"); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		for i := 0; i < 3; i++ {
			if _, err := reader.ReadString('\n'); err != nil {
				return err
			}
			response := "+OK accepted\r\n"
			if i == 2 {
				response = "-ERR [SYS/TEMP] service temporarily unavailable\r\n"
			}
			if _, err := io.WriteString(conn, response); err != nil {
				return err
			}
		}
		return nil
	})
	err := client.Validate(context.Background(), Credentials{Email: "fixture@daum.net", AppPassword: "fixture-only", Protocol: "pop3"})
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "provider_unavailable", failure.Category)
	require.True(t, failure.Retryable)
	require.ErrorAs(t, transportFailure(io.ErrUnexpectedEOF), &failure)
	require.Equal(t, "transport", failure.Category)
}

func TestPasswordMentionAloneIsNotPermanentCredentialEvidence(t *testing.T) {
	for _, tc := range []struct{ reply, category string }{
		{"Password authentication is not supported on this service", "protocol"},
		{"Password service internal error", "protocol"},
		{"Password backend unavailable", "provider_unavailable"},
		{"[AUTH] Generate an app password", "invalid_credentials"},
		{"[AUTHENTICATIONFAILED] Invalid password", "invalid_credentials"},
		{"Incorrect password", "invalid_credentials"},
	} {
		var failure *Failure
		require.ErrorAs(t, authFailure(errors.New(tc.reply)), &failure)
		require.Equal(t, tc.category, failure.Category, tc.reply)
	}
}
