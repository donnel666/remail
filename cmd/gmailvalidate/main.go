// Command gmailvalidate manually rotates one Gmail account from a TXT file.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/donnel666/remail/internal/gmail"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type options struct {
	filePath    string
	line        int
	proxy       string
	browser     string
	statePath   string
	outputPath  string
	timeout     time.Duration
	ownerUserID uint
	apply       bool
	commit      bool
}

type checkpointFile struct {
	Version  int                          `json:"version"`
	Accounts map[string]accountCheckpoint `json:"accounts"`
}

type accountCheckpoint struct {
	TwoFactorSecret    string    `json:"twoFactorSecret,omitempty"`
	AppPassword        string    `json:"appPassword,omitempty"`
	TwoFactorRevoked   bool      `json:"twoFactorRevoked,omitempty"`
	AppPasswordRevoked bool      `json:"appPasswordRevoked,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type productionProxyRuntime struct {
	proxies gmail.StandaloneProxyProvider
	service *gmail.Service
	close   func()
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gmailvalidate:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	var config options
	flags := flag.NewFlagSet("gmailvalidate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&config.filePath, "file", "gmail.txt", "credential TXT path")
	flags.IntVar(&config.line, "line", 0, "one-based physical source line to validate")
	flags.StringVar(&config.proxy, "proxy", "", "optional HTTP/HTTPS/SOCKS proxy URL")
	flags.StringVar(&config.browser, "browser", "", "optional Chrome-compatible browser path")
	flags.StringVar(&config.statePath, "state", ".gmailvalidate-state.json", "0600 credential checkpoint path")
	flags.StringVar(&config.outputPath, "output", "gmail_ok.txt", "0600 successful four-field output path")
	flags.DurationVar(&config.timeout, "timeout", 10*time.Minute, "overall validation timeout")
	flags.UintVar(&config.ownerUserID, "owner-user-id", 1, "database owner user ID for committed Gmail resources")
	flags.BoolVar(&config.apply, "apply", false, "revoke old credentials and create replacements")
	flags.BoolVar(&config.commit, "commit", false, "commit the successful four-field output to the database without contacting Google")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || config.line <= 0 || config.timeout <= 0 || config.ownerUserID == 0 || config.apply && config.commit {
		return errors.New("provide a positive -line, one operation, and no positional arguments")
	}
	credential, err := loadCredential(config.filePath, config.line)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credential.Password) == "" {
		return errors.New("gmailvalidate requires the Gmail login password; APP-password-only lines belong in the Web importer")
	}
	if !config.apply && !config.commit {
		fmt.Fprintf(stdout, "line=%d parsed binding=%t 2fa=%t app_password=%t; add -apply to rotate\n",
			config.line, credential.BindingEmail != "", credential.TwoFactorSecret != "", credential.AppPassword != "")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	if config.commit {
		return commitSuccessfulCredential(ctx, config, credential, stdout)
	}
	if config.browser != "" {
		if err := os.Setenv("GMAIL_BROWSER_BINARY", config.browser); err != nil {
			return fmt.Errorf("set browser path: %w", err)
		}
	}
	state, err := loadCheckpoint(config.statePath)
	if err != nil {
		return err
	}
	key := strings.ToLower(credential.Email)
	if saved, ok := state.Accounts[key]; ok {
		if saved.TwoFactorRevoked {
			credential.TwoFactorSecret = ""
		} else if saved.TwoFactorSecret != "" {
			credential.TwoFactorSecret = saved.TwoFactorSecret
		}
		if saved.AppPasswordRevoked {
			credential.AppPassword = ""
		} else if saved.AppPassword != "" {
			credential.AppPassword = saved.AppPassword
		}
	}

	var result gmail.StandaloneRotationResult
	var runtime *productionProxyRuntime
	if proxyURL := strings.TrimSpace(config.proxy); proxyURL != "" {
		result = gmail.RotateStandaloneAccount(ctx, credential, proxyURL, config.line)
	} else {
		runtime, err = openProductionProxyRuntime(ctx)
		if err != nil {
			return errors.New("gmail production validation proxy pool is unavailable")
		}
		defer runtime.close()
		result = gmail.RotateStandaloneAccountWithProxyProvider(
			ctx, credential, runtime.proxies, config.line, fmt.Sprintf("gmailvalidate-line-%d", config.line),
		)
	}
	saved := state.Accounts[key]
	changed := false
	if result.TwoFactorRevoked {
		saved.TwoFactorSecret = ""
		saved.TwoFactorRevoked = true
		credential.TwoFactorSecret = ""
		changed = true
	}
	if result.TwoFactorAuthoritative {
		saved.TwoFactorSecret = result.TwoFactorSecret
		saved.TwoFactorRevoked = false
		credential.TwoFactorSecret = result.TwoFactorSecret
		changed = true
	}
	if result.AppPasswordRevoked {
		saved.AppPassword = ""
		saved.AppPasswordRevoked = true
		credential.AppPassword = ""
		changed = true
	}
	if result.AppPasswordAuthoritative {
		saved.AppPassword = result.AppPassword
		saved.AppPasswordRevoked = false
		credential.AppPassword = result.AppPassword
		changed = true
	}
	if changed {
		saved.UpdatedAt = time.Now().UTC()
		state.Accounts[key] = saved
		if err := saveCheckpoint(config.statePath, state); err != nil {
			return fmt.Errorf("save authoritative credential checkpoint: %w", err)
		}
	}
	if result.Err != nil {
		safe := strings.TrimSpace(result.SafeError)
		if safe == "" {
			safe = "Gmail validation failed."
		}
		return fmt.Errorf("%s temporary=%t proxy_failure=%t", safe, result.Temporary, result.ProxyFailure)
	}
	if credential.TwoFactorSecret == "" || credential.AppPassword == "" {
		return errors.New("gmail returned incomplete replacement credentials")
	}
	if runtime == nil {
		runtime, err = openProductionProxyRuntime(ctx)
		if err != nil {
			return errors.New("gmail production database is unavailable")
		}
		defer runtime.close()
	}
	if _, err := runtime.service.CommitStandaloneValidatedCredentials(ctx, config.ownerUserID, credential,
		gmail.StandaloneRotationResult{
			TwoFactorSecret: credential.TwoFactorSecret, AppPassword: credential.AppPassword,
			TwoFactorAuthoritative: true, AppPasswordAuthoritative: true,
		}, fmt.Sprintf("gmailvalidate-line-%d", config.line)); err != nil {
		return fmt.Errorf("commit successful Gmail credentials: %w", err)
	}
	line := strings.Join([]string{credential.Email, credential.Password, credential.TwoFactorSecret, credential.AppPassword}, "----")
	if err := upsertSuccess(config.outputPath, credential.Email, line); err != nil {
		return fmt.Errorf("save successful credentials: %w", err)
	}
	fmt.Fprintf(stdout, "line=%d validation=ok checkpoint=%s output=%s\n", config.line, config.statePath, config.outputPath)
	return nil
}

func openProductionProxyRuntime(ctx context.Context) (*productionProxyRuntime, error) {
	config, err := platform.Load()
	if err != nil {
		return nil, err
	}
	services, cleanup, err := platform.New(ctx, config)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*productionProxyRuntime, error) {
		cleanup()
		return nil, err
	}
	settings, err := settingsinfra.NewRepository(services.DB).List(ctx)
	if err != nil {
		return fail(err)
	}
	runtimeconfig.Replace(settings)
	proxies, err := proxyapi.NewProxyModule(services.DB, services.Asynq)
	if err != nil {
		return fail(err)
	}
	service := gmail.NewService(services.DB, services.Asynq)
	service.SetPickupProxyProvider(proxies.ProxyUseCase)
	return &productionProxyRuntime{
		proxies: proxies.ProxyUseCase,
		service: service, close: cleanup,
	}, nil
}

func commitSuccessfulCredential(ctx context.Context, config options, source gmail.StandaloneCredential, stdout io.Writer) error {
	replacement, err := loadSuccessfulCredential(config.outputPath, source.Email)
	if err != nil {
		return err
	}
	if replacement.Password != source.Password {
		return errors.New("successful output password does not match the selected source line")
	}
	replacement.BindingEmail = source.BindingEmail
	runtime, err := openProductionProxyRuntime(ctx)
	if err != nil {
		return errors.New("gmail production database is unavailable")
	}
	defer runtime.close()
	committed, err := runtime.service.CommitStandaloneValidatedCredentials(ctx, config.ownerUserID, replacement,
		gmail.StandaloneRotationResult{
			TwoFactorSecret: replacement.TwoFactorSecret, AppPassword: replacement.AppPassword,
			TwoFactorAuthoritative: true, AppPasswordAuthoritative: true,
		}, fmt.Sprintf("gmailvalidate-line-%d-commit", config.line))
	if err != nil {
		return fmt.Errorf("commit successful Gmail credentials: %w", err)
	}
	fmt.Fprintf(stdout, "line=%d commit=ok resource_id=%d history_queued=%t\n", config.line, committed.ResourceID, committed.HistoryQueued)
	return nil
}

func loadSuccessfulCredential(path, email string) (gmail.StandaloneCredential, error) {
	file, err := os.Open(path)
	if err != nil {
		return gmail.StandaloneCredential{}, fmt.Errorf("open successful output: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		candidate, ok := gmail.ParseStandaloneCredentialLine(strings.TrimSuffix(scanner.Text(), "\r"))
		if ok && strings.EqualFold(candidate.Email, email) {
			return candidate, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return gmail.StandaloneCredential{}, fmt.Errorf("read successful output: %w", err)
	}
	return gmail.StandaloneCredential{}, errors.New("successful output does not contain the selected email")
}

func loadCredential(path string, selectedLine int) (gmail.StandaloneCredential, error) {
	file, err := os.Open(path)
	if err != nil {
		return gmail.StandaloneCredential{}, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if lineNumber != selectedLine {
			continue
		}
		credential, ok := gmail.ParseStandaloneCredentialLine(strings.TrimSuffix(scanner.Text(), "\r"))
		if !ok {
			return gmail.StandaloneCredential{}, fmt.Errorf("line %d has an unsupported Gmail credential format", selectedLine)
		}
		return credential, nil
	}
	if err := scanner.Err(); err != nil {
		return gmail.StandaloneCredential{}, fmt.Errorf("read input: %w", err)
	}
	return gmail.StandaloneCredential{}, fmt.Errorf("line %d does not exist", selectedLine)
}

func loadCheckpoint(path string) (checkpointFile, error) {
	state := checkpointFile{Version: 1, Accounts: make(map[string]accountCheckpoint)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return checkpointFile{}, fmt.Errorf("read checkpoint: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Accounts == nil {
		return checkpointFile{}, errors.New("checkpoint is invalid")
	}
	return state, nil
}

func saveCheckpoint(path string, state checkpointFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

func upsertSuccess(path, email, replacement string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	replaced := false
	for i, line := range lines {
		account, _, _ := strings.Cut(line, "----")
		if strings.EqualFold(strings.TrimSpace(account), email) {
			lines[i], replaced = replacement, true
			break
		}
	}
	if !replaced {
		lines = append(lines, replacement)
	}
	return writePrivateFile(path, []byte(strings.Join(lines, "\n")+"\n"))
}

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
