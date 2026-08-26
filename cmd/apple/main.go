// Command apple updates Apple Account security questions, birthdays, and
// passwords from exported credentials without importing account data into Remail.
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const outputSeparator = "----"

var errTwoFactorEnabled = errors.New("apple account has two-factor authentication enabled")

type commandConfig struct {
	inputPaths  []string
	outputPath  string
	pendingPath string
	concurrency int
	offset      int
	limit       int
	proxyURL    string
	timeout     time.Duration
	newAnswers  [3]string
}

type securityAnswer struct {
	Question string
	Answer   string
}

type accountInput struct {
	Region       string
	ICloudOpen   string
	Email        string
	Password     string
	NewPassword  string
	NewBirthday  string
	Recovering   bool
	Current      [3]securityAnswer
	Birthday     string
	BirthdayISO  string
	SourcePath   string
	SourceRecord int
}

type accountOutput struct {
	Region    string
	Password  string
	Birthday  string
	Questions [3]securityAnswer
}

type proxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type outputWriter struct {
	mu        sync.Mutex
	file      *os.File
	completed map[string]struct{}
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg, err := parseCommandConfig(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Printf("apple: %v", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Printf("apple: %v", err)
		os.Exit(1)
	}
}

func parseCommandConfig(args []string, stderr io.Writer) (commandConfig, error) {
	cfg := commandConfig{newAnswers: [3]string{"remail1", "remail2", "remail3"}}
	answers := "remail1,remail2,remail3"
	fs := flag.NewFlagSet("apple", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.outputPath, "output", "apple_ok.txt", "successful account output path")
	fs.StringVar(&cfg.pendingPath, "pending", "", "password and birthday recovery path; defaults to output path plus .pending")
	fs.IntVar(&cfg.concurrency, "concurrency", 1, "number of accounts processed concurrently")
	fs.IntVar(&cfg.offset, "offset", 0, "skip this many accounts after filtering and deduplication")
	fs.IntVar(&cfg.limit, "limit", 0, "process at most this many accounts; zero means all")
	fs.StringVar(&cfg.proxyURL, "proxy", "", "optional proxy override; the production proxy pool is used when omitted")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Minute, "timeout for one account")
	fs.StringVar(&answers, "answers", answers, "three replacement answers separated by commas")
	if err := fs.Parse(args); err != nil {
		return commandConfig{}, err
	}
	if cfg.concurrency < 1 || cfg.concurrency > 500 {
		return commandConfig{}, errors.New("concurrency must be between 1 and 500")
	}
	if cfg.offset < 0 || cfg.limit < 0 {
		return commandConfig{}, errors.New("offset and limit must not be negative")
	}
	if cfg.timeout <= 0 {
		return commandConfig{}, errors.New("timeout must be positive")
	}
	cfg.outputPath = strings.TrimSpace(cfg.outputPath)
	cfg.pendingPath = strings.TrimSpace(cfg.pendingPath)
	cfg.proxyURL = strings.TrimSpace(cfg.proxyURL)
	if cfg.outputPath == "" {
		return commandConfig{}, errors.New("output path is required")
	}
	if cfg.pendingPath == "" {
		cfg.pendingPath = cfg.outputPath + ".pending"
	}
	if filepath.Clean(cfg.pendingPath) == filepath.Clean(cfg.outputPath) {
		return commandConfig{}, errors.New("pending path must differ from output path")
	}
	parts := strings.Split(answers, ",")
	if len(parts) != 3 {
		return commandConfig{}, errors.New("answers must contain exactly three comma-separated values")
	}
	for index := range parts {
		cfg.newAnswers[index] = strings.TrimSpace(parts[index])
		if cfg.newAnswers[index] == "" || strings.ContainsAny(cfg.newAnswers[index], "\r\n") || strings.Contains(cfg.newAnswers[index], outputSeparator) {
			return commandConfig{}, errors.New("answers contain an unsupported value")
		}
	}
	cfg.inputPaths = fs.Args()
	return cfg, nil
}

func run(ctx context.Context, cfg commandConfig) error {
	paths := cfg.inputPaths
	if len(paths) == 0 {
		var err error
		paths, err = filepath.Glob("gmail-*.csv")
		if err != nil {
			return fmt.Errorf("discover input CSV files: %w", err)
		}
	}
	if len(paths) == 0 {
		return errors.New("no input CSV files found")
	}

	completed, err := loadCompletedEmails(cfg.outputPath)
	if err != nil {
		return err
	}
	accounts, metadataTwoFactor, duplicate, completedSkip, err := loadAccounts(paths, completed)
	if err != nil {
		return err
	}
	if cfg.offset >= len(accounts) {
		accounts = nil
	} else if cfg.offset > 0 {
		accounts = accounts[cfg.offset:]
	}
	if cfg.limit > 0 && len(accounts) > cfg.limit {
		accounts = accounts[:cfg.limit]
	}
	pending, err := prepareAccountChanges(accounts, cfg.pendingPath, time.Now())
	if err != nil {
		return err
	}
	log.Printf("input files=%d queued=%d metadata_2fa_skipped=%d duplicate_skipped=%d completed_skipped=%d offset=%d concurrency=%d", len(paths), len(accounts), metadataTwoFactor, duplicate, completedSkip, cfg.offset, cfg.concurrency)
	if len(accounts) == 0 {
		return prunePendingChanges(cfg.pendingPath, pending, completed)
	}

	writer, err := openOutputWriter(cfg.outputPath, completed)
	if err != nil {
		return err
	}
	defer writer.file.Close()

	var provider proxyProvider
	cleanup := func() {}
	if cfg.proxyURL == "" {
		provider, cleanup, err = openProductionProxyProvider(ctx)
		if err != nil {
			return err
		}
	}
	defer cleanup()

	jobs := make(chan accountInput)
	var workers sync.WaitGroup
	var succeeded atomic.Int64
	var failed atomic.Int64
	var runtimeTwoFactor atomic.Int64
	workerCount := min(cfg.concurrency, len(accounts))
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for account := range jobs {
				accountCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
				result, processErr := processAccountWithProxy(accountCtx, provider, cfg.proxyURL, account, cfg.newAnswers)
				cancel()
				if errors.Is(processErr, errTwoFactorEnabled) {
					runtimeTwoFactor.Add(1)
					log.Printf("skip_2fa email=%s source=%s record=%d", account.Email, filepath.Base(account.SourcePath), account.SourceRecord)
					continue
				}
				if processErr != nil {
					failed.Add(1)
					log.Printf("failed email=%s source=%s record=%d error=%v", account.Email, filepath.Base(account.SourcePath), account.SourceRecord, processErr)
					continue
				}
				if err := writer.append(account, result); err != nil {
					failed.Add(1)
					log.Printf("failed email=%s source=%s record=%d error=%v", account.Email, filepath.Base(account.SourcePath), account.SourceRecord, err)
					continue
				}
				succeeded.Add(1)
				log.Printf("success email=%s", account.Email)
			}
		}()
	}

	for _, account := range accounts {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		case jobs <- account:
		}
	}
	close(jobs)
	workers.Wait()
	log.Printf("completed queued=%d succeeded=%d failed=%d runtime_2fa_skipped=%d output=%s", len(accounts), succeeded.Load(), failed.Load(), runtimeTwoFactor.Load(), cfg.outputPath)
	if err := prunePendingChanges(cfg.pendingPath, pending, writer.completed); err != nil {
		return err
	}
	if failed.Load() != 0 {
		return fmt.Errorf("%d accounts failed", failed.Load())
	}
	return nil
}

func loadAccounts(paths []string, completed map[string]struct{}) ([]accountInput, int, int, int, error) {
	accounts := make([]accountInput, 0, 1024)
	seen := make(map[string]struct{}, 1024)
	metadataTwoFactor := 0
	duplicates := 0
	completedSkip := 0
	for _, path := range paths {
		if strings.EqualFold(filepath.Ext(path), ".txt") {
			file, err := os.Open(path)
			if err != nil {
				return nil, 0, 0, 0, fmt.Errorf("open input %s: %w", path, err)
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64<<10), 1<<20)
			recordNumber := 0
			for scanner.Scan() {
				recordNumber++
				raw := strings.TrimSpace(scanner.Text())
				if raw == "" {
					continue
				}
				var account accountInput
				var parseErr error
				if len(strings.Split(raw, outputSeparator)) == 6 {
					account, parseErr = parseCredential(raw)
					account.Region = "未知地区"
					if strings.Contains(strings.ToLower(filepath.Base(path)), ".uk.") {
						account.Region = "英国区"
					}
					account.ICloudOpen = "未知"
				} else {
					account, parseErr = parseOutputAccountLine(raw)
				}
				if parseErr != nil {
					file.Close()
					return nil, 0, 0, 0, fmt.Errorf("input %s line %d: %w", path, recordNumber, parseErr)
				}
				account.SourcePath = path
				account.SourceRecord = recordNumber
				key := strings.ToLower(account.Email)
				if _, ok := completed[key]; ok {
					completedSkip++
					continue
				}
				if _, ok := seen[key]; ok {
					duplicates++
					continue
				}
				seen[key] = struct{}{}
				accounts = append(accounts, account)
			}
			if err := scanner.Err(); err != nil {
				file.Close()
				return nil, 0, 0, 0, fmt.Errorf("read input %s: %w", path, err)
			}
			if err := file.Close(); err != nil {
				return nil, 0, 0, 0, fmt.Errorf("close input %s: %w", path, err)
			}
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("open input %s: %w", path, err)
		}
		reader := csv.NewReader(file)
		reader.FieldsPerRecord = -1
		if _, err := reader.Read(); err != nil {
			file.Close()
			return nil, 0, 0, 0, fmt.Errorf("read input header %s: %w", path, err)
		}
		recordNumber := 1
		for {
			record, readErr := reader.Read()
			if errors.Is(readErr, io.EOF) {
				break
			}
			recordNumber++
			if readErr != nil {
				file.Close()
				return nil, 0, 0, 0, fmt.Errorf("read input %s record %d: %w", path, recordNumber, readErr)
			}
			if len(record) < 2 {
				file.Close()
				return nil, 0, 0, 0, fmt.Errorf("input %s record %d has fewer than two columns", path, recordNumber)
			}
			region, icloudOpen, twoFactor := parseProductMetadata(record[0])
			for _, raw := range strings.Split(record[1], "|") {
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				if twoFactor {
					metadataTwoFactor++
					continue
				}
				account, err := parseCredential(raw)
				if err != nil {
					file.Close()
					return nil, 0, 0, 0, fmt.Errorf("input %s record %d: %w", path, recordNumber, err)
				}
				account.Region = region
				account.ICloudOpen = icloudOpen
				account.SourcePath = path
				account.SourceRecord = recordNumber
				key := strings.ToLower(account.Email)
				if _, ok := completed[key]; ok {
					completedSkip++
					continue
				}
				if _, ok := seen[key]; ok {
					duplicates++
					continue
				}
				seen[key] = struct{}{}
				accounts = append(accounts, account)
			}
		}
		if err := file.Close(); err != nil {
			return nil, 0, 0, 0, fmt.Errorf("close input %s: %w", path, err)
		}
	}
	return accounts, metadataTwoFactor, duplicates, completedSkip, nil
}

func parseProductMetadata(product string) (region, icloudOpen string, twoFactor bool) {
	product = strings.TrimSpace(product)
	lower := strings.ToLower(product)
	region = "未知地区"
	if index := strings.Index(product, "区"); index > 0 {
		region = strings.TrimSpace(product[:index]) + "区"
	} else if strings.Contains(product, "韩服") || strings.Contains(product, "韩国") {
		region = "韩国区"
	}
	switch {
	case strings.Contains(lower, "未开通icloud"), strings.Contains(lower, "没开通icloud"):
		icloudOpen = "否"
	case strings.Contains(lower, "已开通icloud"):
		icloudOpen = "是"
	default:
		icloudOpen = "未知"
	}
	return region, icloudOpen, strings.Contains(product, "双重认证")
}

func parseCredential(raw string) (accountInput, error) {
	parts := strings.Split(raw, outputSeparator)
	if len(parts) != 6 {
		return accountInput{}, fmt.Errorf("credential must contain six fields, got %d", len(parts))
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	address, err := mail.ParseAddress(parts[0])
	if err != nil || !strings.EqualFold(address.Address, parts[0]) {
		return accountInput{}, errors.New("credential email is invalid")
	}
	if parts[1] == "" || strings.ContainsAny(parts[1], "\r\n") {
		return accountInput{}, errors.New("credential password is invalid")
	}
	birthdayISO, err := normalizeBirthday(parts[5])
	if err != nil {
		return accountInput{}, err
	}
	account := accountInput{
		Email:       strings.ToLower(parts[0]),
		Password:    parts[1],
		Birthday:    parts[5],
		BirthdayISO: birthdayISO,
	}
	for index := range account.Current {
		account.Current[index] = parseSecurityAnswer(parts[index+2])
		if account.Current[index].Answer == "" {
			return accountInput{}, fmt.Errorf("security answer %d is empty", index+1)
		}
	}
	return account, nil
}

func parseOutputAccountLine(raw string) (accountInput, error) {
	parts := strings.Split(raw, outputSeparator)
	if len(parts) != 8 {
		return accountInput{}, fmt.Errorf("successful account line must contain eight fields, got %d", len(parts))
	}
	account, err := parseCredential(strings.Join(parts[2:], outputSeparator))
	if err != nil {
		return accountInput{}, err
	}
	account.Region = strings.TrimSpace(parts[0])
	account.ICloudOpen = strings.TrimSpace(parts[1])
	if account.Region == "" || account.ICloudOpen == "" {
		return accountInput{}, errors.New("successful account line has empty metadata")
	}
	return account, nil
}

func parseSecurityAnswer(raw string) securityAnswer {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, ")") {
		if index := strings.LastIndex(raw, "("); index > 0 {
			return securityAnswer{
				Question: strings.TrimSpace(raw[:index]),
				Answer:   strings.TrimSpace(raw[index+1 : len(raw)-1]),
			}
		}
	}
	return securityAnswer{Answer: raw}
}

func loadCompletedEmails(path string) (map[string]struct{}, error) {
	completed := make(map[string]struct{})
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return completed, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open output: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), outputSeparator)
		if len(parts) >= 3 && strings.Contains(parts[2], "@") {
			completed[strings.ToLower(strings.TrimSpace(parts[2]))] = struct{}{}
		} else if len(parts) > 0 && strings.Contains(parts[0], "@") {
			completed[strings.ToLower(strings.TrimSpace(parts[0]))] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}
	return completed, nil
}

func openOutputWriter(path string, completed map[string]struct{}) (*outputWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open output: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect output: %w", err)
	}
	return &outputWriter{file: file, completed: completed}, nil
}

func (w *outputWriter) append(account accountInput, result accountOutput) error {
	line, err := formatOutputLine(account, result)
	if err != nil {
		return err
	}
	key := strings.ToLower(account.Email)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.completed[key]; ok {
		return nil
	}
	if _, err := w.file.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	w.completed[key] = struct{}{}
	return nil
}

func formatOutputLine(account accountInput, result accountOutput) (string, error) {
	region := strings.TrimSpace(result.Region)
	if region == "" {
		region = strings.TrimSpace(account.Region)
	}
	if region == "" {
		return "", errors.New("account region is missing")
	}
	fields := []string{sanitizeOutputField(region), account.ICloudOpen, account.Email, sanitizeOutputField(result.Password)}
	for index, item := range result.Questions {
		question := sanitizeOutputField(item.Question)
		answer := sanitizeOutputField(item.Answer)
		if question == "" || answer == "" {
			return "", fmt.Errorf("security question %d is incomplete", index+1)
		}
		fields = append(fields, question+"("+answer+")")
	}
	fields = append(fields, sanitizeOutputField(result.Birthday))
	for _, field := range fields {
		if field == "" || strings.Contains(field, outputSeparator) || strings.ContainsAny(field, "\r\n") {
			return "", errors.New("output contains an unsupported field")
		}
	}
	return strings.Join(fields, outputSeparator), nil
}

func sanitizeOutputField(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, outputSeparator, " - ")
	return strings.TrimSpace(value)
}

func openProductionProxyProvider(ctx context.Context) (proxyProvider, func(), error) {
	cfg, err := platform.Load()
	if err != nil {
		return nil, nil, err
	}
	p, cleanup, err := platform.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (proxyProvider, func(), error) {
		cleanup()
		return nil, nil, err
	}
	settings, err := settingsinfra.NewRepository(p.DB).List(ctx)
	if err != nil {
		return fail(err)
	}
	runtimeconfig.Replace(settings)
	module, err := proxyapi.NewProxyModule(p.DB, p.Asynq)
	if err != nil {
		return fail(err)
	}
	return module.ProxyUseCase, cleanup, nil
}

func processAccountWithProxy(
	ctx context.Context,
	provider proxyProvider,
	explicitProxy string,
	account accountInput,
	newAnswers [3]string,
) (accountOutput, error) {
	if explicitProxy != "" {
		return processAppleAccount(ctx, explicitProxy, account, newAnswers)
	}
	if provider == nil {
		return accountOutput{}, errors.New("production proxy selection is unavailable")
	}
	requestID := platform.NewUUIDV7String()
	maxAttempts := runtimeconfig.Int("max_proxy_attempts", 3, 1)
	var avoidServerIDs []uint
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		route, err := provider.Acquire(ctx, proxyapp.AcquireProxyRequest{
			Key:                 account.Email,
			IPVersion:           proxydomain.ProxyIPv4,
			Purpose:             proxydomain.ProxyPurposeAuth,
			AllowSystemFallback: true,
			RenewBinding:        true,
			Attempt:             attempt,
			RequestID:           requestID,
			AvoidProxyServerIDs: avoidServerIDs,
		})
		if err != nil {
			return accountOutput{}, fmt.Errorf("acquire production proxy: %w", err)
		}
		proxyURL := ""
		managed := route != nil && !route.Direct && route.ID != 0
		if route != nil && !route.Direct {
			proxyURL = strings.TrimSpace(route.URL)
		}
		result, processErr := processAppleAccount(ctx, proxyURL, account, newAnswers)
		proxyFailure := msacl.IsProxyTransportError(processErr)
		appleTransient := isAppleTransientError(processErr)
		retryable := proxyFailure || appleTransient
		if managed {
			if retryable {
				_ = provider.ReportFailure(ctx, route.ID, "Apple Account proxy request failed.")
				avoidServerIDs = proxyapp.AppendAvoidProxyServerID(avoidServerIDs, route)
			} else {
				_ = provider.ReportSuccess(ctx, route.ID)
			}
		}
		if !retryable || attempt == maxAttempts {
			return result, processErr
		}
		account.Recovering = true
	}
	return accountOutput{}, errors.New("production proxy attempts were exhausted")
}
