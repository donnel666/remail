package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"
)

func TestExternalRecoverySkipCannotHideAnIncompleteChange(t *testing.T) {
	msacl.SetAuxiliaryDomains([]string{"internal.test"})
	t.Cleanup(func() { msacl.SetAuxiliaryDomains(nil) })
	external := &msacl.AuthError{Status: msacl.AuthStatusAlreadyBound, BoundMailbox: "10*****@qq.com"}
	require.True(t, isExternalBindingSkip(fmt.Errorf("login: %w", external)))
	require.False(t, isExternalBindingSkip(&incompleteRebindError{stage: "new_proof_pending", err: external}))
	require.False(t, isExternalBindingSkip(nil))
	require.False(t, isExternalBindingSkip(&msacl.AuthError{Status: msacl.AuthStatusAlreadyBound}))
	require.False(t, isExternalBindingSkip(&msacl.AuthError{Status: msacl.AuthStatusCodeTimeout, BoundMailbox: "10*****@qq.com"}))
	require.False(t, isExternalBindingSkip(&msacl.AuthError{Status: msacl.AuthStatusAlreadyBound, BoundMailbox: "o*****@internal.test"}))
}

func TestSavedExternalSkipDoesNotLoginAgain(t *testing.T) {
	dir := t.TempDir()
	row := candidate{Email: "external@outlook.com", Password: "source-password"}
	path := filepath.Join(dir, "skip-"+accountKey(0, row.Email)+".json")
	require.NoError(t, writePrivateJSON(path, map[string]any{"id": 0, "email": row.Email, "stage": "external_binding", "binding_address": "10*****@qq.com", "reason": "previously skipped"}))
	r := runtime{dir: dir, completed: map[string]receipt{}}
	err := r.process(context.Background(), row)
	require.True(t, isExternalBindingSkip(err))
	require.Empty(t, r.completed)
}

func TestManifestRejectsCollidingTargetsAndPreservesPrefix(t *testing.T) {
	require.Equal(t, "some.name+tag@fan7z.com", targetAddress("some.name+tag@outlook.com"))
	path := filepath.Join(t.TempDir(), "input.json")
	for _, input := range []string{`[]`, `[{"id":1,"email":"bad"}]`, `[{"id":1,"email":"one@outlook.com"},{"id":1,"email":"two@outlook.com"}]`, `[{"id":1,"email":"one@outlook.com"},{"id":2,"email":"one@hotmail.com"}]`} {
		require.NoError(t, os.WriteFile(path, []byte(input), 0600))
		_, err := readManifest(path)
		require.Error(t, err)
	}
	require.NoError(t, os.WriteFile(path, []byte(`[{"id":4919098,"email":"weisui104017@outlook.com"}]`), 0600))
	rows, err := readManifest(path)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestExportUsesExactCredentialsAndRejectsAmbiguousRecords(t *testing.T) {
	s := snapshot{Email: "one@outlook.com", Password: " password with spaces ", ClientID: "client-id", RefreshToken: "refresh-token"}
	line, err := exportLine(s, "one@fan7z.com")
	require.NoError(t, err)
	require.Equal(t, "one@outlook.com---- password with spaces ----client-id----refresh-token----one@fan7z.com\n", line)
	for _, password := range []string{"", "bad\npassword", "bad----password"} {
		s.Password = password
		_, err = exportLine(s, "one@fan7z.com")
		require.Error(t, err)
	}
}

func TestExternalManifestKeepsAccountsDistinctAndExportsMissingFieldsAsX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"email":"one@outlook.com","password":"pw1"},{"email":"two@outlook.com","password":"pw2"}]`), 0600))
	rows, err := readManifest(path)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, accountKey(0, rows[0].Email), accountKey(0, rows[1].Email))
	line, err := exportLine(snapshot{Email: rows[0].Email, Password: rows[0].Password}, targetAddress(rows[0].Email))
	require.NoError(t, err)
	require.Equal(t, "one@outlook.com----pw1----x----x----one@fan7z.com\n", line)
	for _, input := range []string{`[{"email":"one@outlook.com"}]`, `[{"id":1,"email":"one@outlook.com","password":"override"}]`, `[{"id":1,"email":"one@outlook.com","status":"deleted"}]`} {
		require.NoError(t, os.WriteFile(path, []byte(input), 0600))
		_, err := readManifest(path)
		require.Error(t, err)
	}
}

func TestForwardedQQMessageIsMatchedToOriginalExactRecipient(t *testing.T) {
	raw := []byte("From: Microsoft account team <account-security-noreply@accountprotection.microsoft.com>\r\nTo: weisui104017@fan7z.com\r\nX-Original-To: shared@qq.com\r\nSubject: Microsoft security code\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSecurity code: 123456\r\n")
	message, ok := parseForwardedCode(raw, "qq:1:2", time.Now())
	require.True(t, ok)
	require.Equal(t, "weisui104017@fan7z.com", message.To)
	require.Contains(t, message.Preview, "123456")
	_, ok = parseForwardedCode([]byte(strings.ReplaceAll(string(raw), "account-security-noreply@accountprotection.microsoft.com", "other@example.com")), "qq:1:2", time.Now())
	require.False(t, ok)
	_, ok = parseForwardedCode([]byte(strings.ReplaceAll(string(raw), "shared@qq.com", "other@fan7z.com")), "qq:1:2", time.Now())
	require.False(t, ok)
}

func TestQQPollCancellationClosesAStalledIMAPConnection(t *testing.T) {
	conn, server := net.Pipe()
	defer conn.Close()
	defer server.Close()
	commandReceived := make(chan struct{})
	go func() {
		_, _ = io.WriteString(server, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		_, _ = bufio.NewReader(server).ReadString('\n')
		close(commandReceived)
	}()
	r := forwardingMailbox{conn: conn, client: imapclient.New(conn, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.List(ctx, "one@fan7z.com", 1, false)
		done <- err
	}()
	select {
	case <-commandReceived:
	case <-time.After(time.Second):
		t.Fatal("IMAP command was not sent")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Nil(t, r.client)
	case <-time.After(time.Second):
		t.Fatal("cancelled QQ poll remained blocked")
	}
}

func TestPrivateWriteAndReceiptExportResume(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "receipts"), 0700))
	saved := receipt{Snapshot: snapshot{ID: 1, Email: "one@outlook.com", Password: "pw", ClientID: "id", RefreshToken: "rt"}, Target: "one@fan7z.com", RemoteVerified: true, Committed: true}
	path := filepath.Join(dir, "receipts", "1.json")
	require.NoError(t, writePrivateJSON(path, saved))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
	r := runtime{dir: dir, completed: map[string]receipt{}}
	require.NoError(t, r.restoreExports())
	data, err := os.ReadFile(filepath.Join(dir, "success.txt"))
	require.NoError(t, err)
	require.Equal(t, "one@outlook.com----pw----id----rt----one@fan7z.com\n", string(data))
	saved.Committed = false
	require.NoError(t, writePrivateJSON(path, saved))
	r.completed = map[string]receipt{}
	require.NoError(t, r.restoreExports())
	data, err = os.ReadFile(filepath.Join(dir, "success.txt"))
	require.NoError(t, err)
	require.Empty(t, data)
}
