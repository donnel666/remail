package protocol

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidationChecksIMAPReadAccessWithoutJunkOrMIME(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			var commands []string
			var messages []imapScriptMessage
			if !empty {
				messages = []imapScriptMessage{{uid: 7, raw: "invalid MIME body", receivedAt: time.Now().UTC()}}
			}
			client, _ := scriptedClient(t, imapMailboxScript(&commands, false, false, map[string][]imapScriptMessage{"INBOX": messages}))
			require.NoError(t, client.Validate(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "fixture-app", Protocol: "imap"}))
			joined := strings.Join(commands, "\n")
			require.Contains(t, joined, "LOGIN")
			require.Contains(t, joined, "fixture-app")
			require.Contains(t, joined, "EXAMINE INBOX")
			for _, command := range []string{" LIST ", " SEARCH ", " SORT ", "BODY.PEEK", "Junk", "Spam"} {
				require.NotContains(t, joined, command)
			}
			if !empty {
				require.Contains(t, joined, "FETCH")
			}
		})
	}
}

func TestValidationChecksPOP3WithStatWithoutFetchingMail(t *testing.T) {
	var commands []string
	client, _ := scriptedClient(t, func(conn net.Conn) error {
		if _, err := io.WriteString(conn, "+OK ready\r\n"); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		for _, want := range []string{"USER account@daum.net", "PASS fixture-app", "STAT", "QUIT"} {
			line, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			commands = append(commands, strings.TrimSpace(line))
			if strings.TrimSpace(line) != want {
				return errors.New("unexpected validation command")
			}
			if _, err := io.WriteString(conn, "+OK 10 1000\r\n"); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, client.Validate(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "fixture-app", Protocol: "pop3"}))
	require.Len(t, commands, 4)
}

func TestValidationTransportDiagnosticsExposeOnlySafeCodes(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
	}{
		{errors.New("socks connect tcp proxy: unknown error network unreachable"), "network_unreachable"},
		{errors.New("socks connect tcp proxy: connection refused"), "connection_refused"},
		{errors.New("socks connect tcp proxy: connection not allowed by ruleset"), "connection_refused"},
		{errors.New("proxy connect failed"), "proxy_connect"},
		{context.DeadlineExceeded, "timeout"},
		{io.EOF, "connection_closed"},
		{errors.New("remote error: tls: handshake failure"), "tls_handshake"},
		{errors.New("account-private@example.test http://proxy-user:proxy-secret@proxy.invalid app-secret"), "unknown"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			client := &Client{dial: func(context.Context, string) (net.Conn, error) { return nil, tc.cause }}
			err := client.Validate(context.Background(), Credentials{Email: "account-private@example.test", AppPassword: "app-secret", Protocol: "imap"})
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, "transport", failure.Category)
			require.True(t, failure.Retryable)
			stage, reason := FailureDiagnostics(err)
			require.Equal(t, "connect_tls", stage)
			require.Equal(t, tc.reason, reason)
			for _, secret := range []string{"account-private", "proxy-user", "proxy-secret", "app-secret"} {
				require.NotContains(t, err.Error()+stage+reason, secret)
			}
		})
	}
}
