package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	fetch func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error)
}

func (f fakeClient) Fetch(ctx context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
	return f.fetch(ctx, credentials, request)
}

func TestCredentialFormatsPreserveAPPPasswordAndIgnoreIndependentMetadata(t *testing.T) {
	for _, line := range []string{
		"\ufeff USER@Daum.Net ---- app secret \r\n",
		"USER@Daum.Net---- login secret ---- app secret ",
		"USER@Daum.Net---- login secret ---- app secret ----https://remail.example/v1/pickup?email=helper@outlook.com&token=private-token----different@kakao.example",
	} {
		account, err := parseCredential(line)
		require.NoError(t, err)
		require.Equal(t, "user@daum.net", account.Mailbox)
		require.Equal(t, " app secret ", account.AppPassword)
		if len(strings.Split(line, "----")) > 2 {
			require.Equal(t, " login secret ", account.LoginPassword)
		}
	}
	for _, suffix := range []string{"daum.net", "hanmail.net", "hanmail2.net"} {
		_, err := parseCredential("user@" + suffix + "----app")
		require.NoError(t, err)
	}
}

func TestInvalidInputIsSanitizedAndEOFReturns(t *testing.T) {
	client := fakeClient{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
		t.Fatal("invalid input must not contact Daum")
		return protocol.FetchResult{}, nil
	}}
	for _, line := range []string{"", "private-password", "user@other.example----private-app", "user@daum.net----", "user@daum.net----private-login----private-app----private-url", "user@daum.net----private\x00secret"} {
		var output bytes.Buffer
		require.Equal(t, 2, run(context.Background(), nil, strings.NewReader(line), &output, io.Discard, client))
		for _, secret := range []string{"private-password", "private-app", "private-login", "private-url", "private\x00secret"} {
			require.NotContains(t, output.String(), secret)
		}
		var out result
		require.NoError(t, json.Unmarshal(output.Bytes(), &out))
		require.False(t, out.Success)
		require.Equal(t, "input", out.Category)
	}
}

func TestStdinStopsAtNewlineWithoutWaitingForEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "user@daum.net----app\n") }()
	done := make(chan error, 1)
	go func() { _, err := readCredential(options{}, reader); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("stdin waited for EOF after a complete credential line")
	}
}

func TestFlagsAndEmptyMailboxSuccess(t *testing.T) {
	var output bytes.Buffer
	client := fakeClient{fetch: func(ctx context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
		require.Equal(t, protocol.Credentials{Email: "user@daum.net", AppPassword: "app", Protocol: "pop3"}, credentials)
		require.Equal(t, 2, request.MaxMessages)
		require.False(t, request.FullHistory)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(time.Second), deadline, time.Second)
		return protocol.FetchResult{Complete: true}, nil
	}}
	require.Equal(t, 0, run(context.Background(), []string{"-protocol", "pop3", "-limit", "2", "-timeout", "1s"}, strings.NewReader("user@daum.net----app\n"), &output, io.Discard, client))
	var out result
	require.NoError(t, json.Unmarshal(output.Bytes(), &out))
	require.True(t, out.Success)
	require.Equal(t, "user@daum.net", out.Mailbox)
	require.Zero(t, out.MessageCount)
	require.NotNil(t, out.Messages)
	require.NotEmpty(t, out.Elapsed)
	for _, args := range [][]string{{"-protocol", "private-secret"}, {"-limit", "private-secret"}, {"-timeout", "0"}, {"private-secret"}, {"-limit", "0"}} {
		output.Reset()
		require.Equal(t, 2, run(context.Background(), args, strings.NewReader(""), &output, io.Discard, client))
		require.NotContains(t, output.String(), "private-secret")
	}
}

func TestSafeSummariesAndProviderFailures(t *testing.T) {
	var output bytes.Buffer
	client := fakeClient{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
		return protocol.FetchResult{Messages: []protocol.Message{{ID: "imap:INBOX:1:2", Sender: protocol.Address{Address: "sender@example.com"}, Subject: "Code 123456 login-secret app-secret https://remail.test/?token=private", Body: "must-not-appear", ReceivedAt: time.Now()}, {ID: "extra"}}, Complete: true}, nil
	}}
	require.Equal(t, 0, run(context.Background(), []string{"-limit", "1"}, strings.NewReader("user@daum.net----login-secret----app-secret"), &output, io.Discard, client))
	for _, secret := range []string{"123456", "login-secret", "app-secret", "must-not-appear", "private", "extra"} {
		require.NotContains(t, output.String(), secret)
	}
	var out result
	require.NoError(t, json.Unmarshal(output.Bytes(), &out))
	require.Equal(t, 1, out.MessageCount)
	require.Equal(t, "sender@example.com", out.Messages[0].From)
	for _, failure := range []error{&protocol.Failure{Category: "invalid_credentials", SafeMessage: "private-secret", Cause: errors.New("private-secret")}, context.DeadlineExceeded} {
		client.fetch = func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
			return protocol.FetchResult{}, failure
		}
		output.Reset()
		require.Equal(t, 1, run(context.Background(), nil, strings.NewReader("user@daum.net----app"), &output, io.Discard, client))
		require.NotContains(t, output.String(), "private-secret")
		require.NoError(t, json.Unmarshal(output.Bytes(), &out))
		require.Contains(t, []string{"invalid_credentials", "timeout"}, out.Category)
	}
}

func TestSingleAccountFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.txt")
	require.NoError(t, os.WriteFile(path, []byte("user@daum.net---- app \n\n"), 0o600))
	account, err := readCredential(options{File: path}, strings.NewReader(""))
	require.NoError(t, err)
	require.Equal(t, " app ", account.AppPassword)
	require.NoError(t, os.WriteFile(path, []byte("user@daum.net----app\nother@daum.net----app"), 0o600))
	_, err = readCredential(options{File: path}, strings.NewReader(""))
	require.Error(t, err)
}

func TestExplicitProxyEnvironmentIsUsedWithoutExposingCredential(t *testing.T) {
	t.Setenv("DAUM_TEST_PROXY", "http://proxy-user:proxy-secret@127.0.0.1:3128")
	var output bytes.Buffer
	client := fakeClient{fetch: func(_ context.Context, credentials protocol.Credentials, _ protocol.FetchRequest) (protocol.FetchResult, error) {
		require.Equal(t, os.Getenv("DAUM_TEST_PROXY"), credentials.ProxyURL)
		return protocol.FetchResult{Complete: true}, nil
	}}
	require.Equal(t, 0, run(context.Background(), []string{"-proxy-env", "DAUM_TEST_PROXY"}, strings.NewReader("user@daum.net----app"), &output, io.Discard, client))
	require.Contains(t, output.String(), `"network":"proxy"`)
	require.NotContains(t, output.String(), "proxy-secret")
	t.Setenv("DAUM_TEST_PROXY", "https://proxy-secret:not-a-port")
	output.Reset()
	require.Equal(t, 2, run(context.Background(), []string{"-proxy-env", "DAUM_TEST_PROXY"}, strings.NewReader("user@daum.net----app"), &output, io.Discard, client))
	require.NotContains(t, output.String(), "proxy-secret")
}
