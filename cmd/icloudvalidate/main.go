// Command icloudvalidate runs one Apple onboarding account interactively and
// commits the completed credential/channel snapshot to the iCloud resource.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/icloud"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	separator                 = "----"
	temporaryManageSessionTTL = 10 * time.Minute
	phaseDelayMinimum         = 60 * time.Second
	phaseDelayMaximum         = 200 * time.Second
)

var appleSMSCodePattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`)

var errRestartValidation = errors.New("restart Apple validation from checkpoint")

type options struct {
	rawLine      string
	filePath     string
	forwardTo    string
	forwardCode  string
	statePath    string
	resetState   bool
	legacyCommit bool
	ownerUserID  uint
	expireDays   int
	timeout      time.Duration
}

type accountInput struct {
	Region           string
	CountryCode      string
	PhoneCountryCode string
	ICloudOpened     bool
	Email            string
	Secret           icloud.AppleOnboardingSecret
	PhoneNumber      string
	FamilyInviteURL  string
}

type runtime struct {
	apple  icloud.AppleOnboardingProvider
	sms    *kitesim.Service
	icloud *icloud.Service
	close  func()
}

type savedPhoneBinding struct {
	PhoneID     uint   `json:"phoneId"`
	PhoneCode   string `json:"phoneCode,omitempty"`
	PhoneNumber string `json:"phoneNumber,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
	Source      string `json:"source,omitempty"`
}

type accountCheckpoint struct {
	Fingerprint            string                         `json:"fingerprint"`
	Stage                  string                         `json:"stage,omitempty"`
	PendingSMSPurpose      string                         `json:"pendingSmsPurpose,omitempty"`
	ForwardTo              string                         `json:"forwardTo,omitempty"`
	ForwardPreparationID   uint                           `json:"forwardPreparationId,omitempty"`
	ForwardPending         bool                           `json:"forwardPending,omitempty"`
	Session                json.RawMessage                `json:"appleTransaction,omitempty"`
	Binding                *savedPhoneBinding             `json:"binding,omitempty"`
	ApplePhoneBound        bool                           `json:"applePhoneBound,omitempty"`
	ICloudAuthenticated    bool                           `json:"icloudAuthenticated,omitempty"`
	ICloudCookieAuth       bool                           `json:"icloudCookieAuthenticated,omitempty"`
	ICloudReady            bool                           `json:"icloudReady,omitempty"`
	ICloudOpened           bool                           `json:"icloudOpened,omitempty"`
	FamilyAuthenticated    bool                           `json:"familyAuthenticated,omitempty"`
	FamilyJoined           bool                           `json:"familyJoined,omitempty"`
	ManageAuthenticated    bool                           `json:"manageAuthenticated,omitempty"`
	ManageReady            bool                           `json:"manageReady,omitempty"`
	ManageSessionExpiresAt time.Time                      `json:"manageSessionExpiresAt,omitempty"`
	ForwardAdded           bool                           `json:"forwardAdded,omitempty"`
	ForwardReady           bool                           `json:"forwardReady,omitempty"`
	CookiesReady           bool                           `json:"cookiesReady,omitempty"`
	Committed              bool                           `json:"committed,omitempty"`
	OldChannel             *icloud.AppleOnboardingChannel `json:"oldChannel,omitempty"`
	NewChannel             *icloud.AppleOnboardingChannel `json:"newChannel,omitempty"`
	CountryCode            string                         `json:"countryCode,omitempty"`
	ExpireAt               time.Time                      `json:"expireAt,omitempty"`
	ResourceID             uint                           `json:"resourceId,omitempty"`
	UpdatedAt              time.Time                      `json:"updatedAt"`
}

type checkpointFile struct {
	Version  int                          `json:"version"`
	Accounts map[string]accountCheckpoint `json:"accounts"`
}

type debugger struct {
	ctx        context.Context
	input      accountInput
	runtime    *runtime
	reader     *bufio.Reader
	stdout     io.Writer
	session    json.RawMessage
	statePath  string
	state      *checkpointFile
	stateKey   string
	checkpoint *accountCheckpoint
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "icloudvalidate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	config, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(stdin)
	line, err := loadInputLine(config, reader)
	if err != nil {
		return err
	}
	input, err := parseLine(line)
	if err != nil {
		return err
	}
	state, err := loadCheckpoint(config.statePath)
	if err != nil {
		return err
	}
	stateKey := strings.ToLower(input.Email)
	fingerprint, err := accountFingerprint(input, config)
	if err != nil {
		return fmt.Errorf("fingerprint input: %w", err)
	}
	checkpoint, found := state.Accounts[stateKey]
	if found && checkpoint.Fingerprint != fingerprint {
		if !config.resetState {
			return errors.New("checkpoint input does not match; use -reset-state to restart this Apple ID")
		}
		delete(state.Accounts, stateKey)
		found = false
	}
	if config.resetState && found {
		delete(state.Accounts, stateKey)
		found = false
	}
	if !found {
		checkpoint = accountCheckpoint{Fingerprint: fingerprint, ExpireAt: time.Now().UTC().AddDate(0, 0, config.expireDays)}
		state.Accounts[stateKey] = checkpoint
		if err := saveCheckpoint(config.statePath, state); err != nil {
			return fmt.Errorf("create checkpoint: %w", err)
		}
	} else if checkpoint.ExpireAt.IsZero() {
		checkpoint.ExpireAt = time.Now().UTC().AddDate(0, 0, config.expireDays)
		state.Accounts[stateKey] = checkpoint
		if err := saveCheckpoint(config.statePath, state); err != nil {
			return fmt.Errorf("repair checkpoint expiry: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	runtimeStarted := time.Now()
	fmt.Fprintf(stdout, "runtime=open proxy_mode=sticky_pool timeout=%s\n", config.timeout)
	rt, err := openRuntime(ctx, input.Email)
	if err != nil {
		return err
	}
	defer rt.close()
	fmt.Fprintf(stdout, "runtime=ready proxy_mode=sticky_pool duration=%s\n", elapsed(runtimeStarted))

	var binding *kitesim.SMSPhoneBinding
	bindPhone := func(requested string) (kitesim.SMSPhoneBinding, error) {
		for {
			value, bindErr := rt.sms.BindICloudSMSPhone(ctx, input.Email, requested)
			if bindErr == nil {
				return value, nil
			}
			retryAt, ok := kitesim.SMSRetryAt(bindErr)
			if !ok {
				return kitesim.SMSPhoneBinding{}, bindErr
			}
			if !retryAt.After(time.Now().UTC()) {
				retryAt = time.Now().UTC().Add(time.Second)
			}
			fmt.Fprintf(stdout, "esim_phone=waiting retry_at=%s reason=%q\n", retryAt.UTC().Format(time.RFC3339), bindErr.Error())
			if err := waitContextUntil(ctx, retryAt); err != nil {
				return kitesim.SMSPhoneBinding{}, err
			}
		}
	}
	if checkpoint.Binding != nil {
		value := checkpoint.Binding.toKitesim()
		binding = &value
		input.PhoneNumber = value.PhoneNumber
		input.PhoneCountryCode = strings.ToUpper(strings.TrimSpace(value.CountryCode))
		fmt.Fprintf(stdout, "esim_phone=reused phone_id=%d country=%s source=%q suffix=%s\n", value.PhoneID, value.CountryCode, value.Source, lastDigits(value.PhoneNumber, 4))
	} else if input.PhoneNumber != "" {
		fmt.Fprintf(stdout, "esim_phone=start selection=specified_pool suffix=%s\n", lastDigits(input.PhoneNumber, 4))
		value, bindErr := bindPhone(input.PhoneNumber)
		if bindErr != nil {
			return fmt.Errorf("bind requested phone in Kitesim: %w", bindErr)
		}
		binding = &value
		input.PhoneNumber = value.PhoneNumber
		input.PhoneCountryCode = strings.ToUpper(strings.TrimSpace(value.CountryCode))
		fmt.Fprintf(stdout, "esim_phone=assigned selection=specified_pool phone_id=%d country=%s source=%q suffix=%s\n", value.PhoneID, value.CountryCode, value.Source, lastDigits(value.PhoneNumber, 4))
		checkpoint.Binding = savedPhoneBindingFrom(value)
		checkpoint.UpdatedAt = time.Now().UTC()
		state.Accounts[stateKey] = checkpoint
		if err := saveCheckpoint(config.statePath, state); err != nil {
			return fmt.Errorf("save phone binding checkpoint: %w", err)
		}
	} else {
		fmt.Fprintln(stdout, "esim_phone=start selection=automatic_pool")
		value, bindErr := bindPhone("")
		if bindErr != nil {
			return fmt.Errorf("allocate and bind an eSIM phone: %w", bindErr)
		}
		binding = &value
		input.PhoneNumber = value.PhoneNumber
		input.PhoneCountryCode = strings.ToUpper(strings.TrimSpace(value.CountryCode))
		fmt.Fprintf(stdout, "esim_phone=assigned selection=automatic_pool phone_id=%d country=%s source=%q suffix=%s\n", value.PhoneID, value.CountryCode, value.Source, lastDigits(value.PhoneNumber, 4))
		checkpoint.Binding = savedPhoneBindingFrom(value)
		checkpoint.UpdatedAt = time.Now().UTC()
		state.Accounts[stateKey] = checkpoint
		if err := saveCheckpoint(config.statePath, state); err != nil {
			return fmt.Errorf("save phone binding checkpoint: %w", err)
		}
	}

	checkpoint.Fingerprint = fingerprint
	state.Accounts[stateKey] = checkpoint
	d := &debugger{ctx: ctx, input: input, runtime: rt, reader: reader, stdout: stdout,
		statePath: config.statePath, state: &state, stateKey: stateKey, checkpoint: &checkpoint,
		session: append(json.RawMessage(nil), checkpoint.Session...)}
	if err := d.recoverPendingSMSCheckpoint(); err != nil {
		return err
	}
	err = d.run(binding, config)
	if err == nil {
		return nil
	}
	if errors.Is(err, errRestartValidation) {
		d.logf("apple_recovery=paused reason=checkpoint_restart explicit_rerun_required=true\n")
		return fmt.Errorf("apple validation checkpoint updated; explicit rerun required: %w", err)
	}
	var appleErr *icloud.AppleOnboardingError
	if errors.As(err, &appleErr) {
		if restart := strings.TrimSpace(appleErr.RestartStage); restart != "" {
			d.cancelSMSChallenge(d.checkpoint.PendingSMSPurpose)
			if resetErr := d.restartAt(restart); resetErr != nil {
				return errors.Join(err, resetErr)
			}
			d.logf("apple_recovery=paused stage=%s explicit_rerun_required=true\n", restart)
			return fmt.Errorf("apple validation paused at %s; explicit rerun required: %w", restart, err)
		}
	}
	return err
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var config options
	flags := flag.NewFlagSet("icloudvalidate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&config.rawLine, "line", "", "one complete Apple account input line")
	flags.StringVar(&config.filePath, "file", "", "file containing one Apple account input line")
	flags.StringVar(&config.forwardTo, "forward-to", "", "optional forwarding address to add and verify")
	flags.StringVar(&config.forwardCode, "forward-code", "", "optional forwarding verification code")
	flags.StringVar(&config.statePath, "state", ".icloudvalidate-state.json", "0600 resumable checkpoint path")
	flags.BoolVar(&config.resetState, "reset-state", false, "discard this email's checkpoint before starting")
	flags.BoolVar(&config.legacyCommit, "legacy-commit", false, "commit completed credentials using legacy import semantics")
	flags.UintVar(&config.ownerUserID, "owner-user-id", 1, "database owner user ID for the committed iCloud resource")
	flags.IntVar(&config.expireDays, "expire-days", 30, "resource expiry in days")
	flags.DurationVar(&config.timeout, "timeout", 15*time.Minute, "overall validation timeout")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 || (strings.TrimSpace(config.rawLine) != "" && strings.TrimSpace(config.filePath) != "") {
		return options{}, errors.New("provide exactly one of -line or -file and no positional arguments")
	}
	if config.timeout <= 0 || config.ownerUserID == 0 || config.expireDays <= 0 {
		return options{}, errors.New("timeout, owner-user-id, and expire-days must be positive")
	}
	config.statePath = strings.TrimSpace(config.statePath)
	if config.statePath == "" {
		return options{}, errors.New("state path must not be empty")
	}
	config.forwardTo = strings.ToLower(strings.TrimSpace(config.forwardTo))
	config.forwardCode = strings.TrimSpace(config.forwardCode)
	if config.forwardTo != "" {
		address, err := mail.ParseAddress(config.forwardTo)
		if err != nil || address.Address != config.forwardTo {
			return options{}, errors.New("forward-to must be a plain email address")
		}
	}
	if strings.ContainsAny(config.forwardCode, "\r\n") {
		return options{}, errors.New("forward-code contains an unsupported value")
	}
	return config, nil
}

func loadInputLine(config options, stdin io.Reader) (string, error) {
	if strings.TrimSpace(config.rawLine) != "" {
		return strings.TrimSpace(config.rawLine), nil
	}
	if path := strings.TrimSpace(config.filePath); path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open input file: %w", err)
		}
		defer file.Close()
		return firstNonEmptyLine(file)
	}
	return firstNonEmptyLine(stdin)
}

func firstNonEmptyLine(source io.Reader) (string, error) {
	if reader, ok := source.(*bufio.Reader); ok {
		for {
			line, err := reader.ReadString('\n')
			if value := strings.TrimSpace(strings.TrimSuffix(line, "\r")); value != "" {
				return value, nil
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", fmt.Errorf("read input: %w", err)
			}
		}
		return "", errors.New("input line is empty")
	}
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			return line, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return "", errors.New("input line is empty")
}

func parseLine(raw string) (accountInput, error) {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
	if !utf8.ValidString(raw) || raw == "" {
		return accountInput{}, errors.New("input line is empty or invalid UTF-8")
	}
	parts := strings.Split(raw, separator)
	if len(parts) < 8 || len(parts) > 10 {
		return accountInput{}, errors.New("apple input must contain 8 to 10 fields separated by ----")
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	region := parts[0]
	if region == "" || utf8.RuneCountInString(region) > 64 || strings.ContainsAny(region, "\r\n") {
		return accountInput{}, errors.New("region is invalid")
	}
	opened, ok := parseOpened(parts[1])
	if !ok {
		return accountInput{}, errors.New("iCloud flag must be 是/否, yes/no, true/false, or 1/0")
	}
	email := strings.ToLower(parts[2])
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || utf8.RuneCountInString(email) > 320 {
		return accountInput{}, errors.New("apple ID is invalid")
	}
	password := parts[3]
	if password == "" || len(password) > 512 || strings.ContainsAny(password, "\r\n") {
		return accountInput{}, errors.New("apple password is invalid")
	}
	var answers [3]icloud.AppleSecurityAnswer
	for index := range answers {
		answer, ok := parseAnswer(parts[index+4])
		if !ok {
			return accountInput{}, fmt.Errorf("security answer %d is invalid", index+1)
		}
		answers[index] = answer
	}
	birthday, err := time.Parse("2006-01-02", parts[7])
	if err != nil || birthday.After(time.Now().UTC()) {
		return accountInput{}, errors.New("birthday is invalid")
	}

	phone, invite := "", ""
	if len(parts) == 9 {
		if looksLikeInvite(parts[8]) {
			invite = parts[8]
		} else {
			phone = parts[8]
		}
	}
	if len(parts) == 10 {
		phone, invite = parts[8], parts[9]
	}
	if phone != "" {
		phone = digits(phone)
		if len(phone) < 7 || len(phone) > 20 {
			return accountInput{}, errors.New("phone number is invalid")
		}
	}
	if invite != "" && !validInvite(invite) {
		return accountInput{}, errors.New("family invitation is invalid")
	}
	return accountInput{
		Region: region, CountryCode: icloud.CountryCodeFromICloudRegion(region), ICloudOpened: opened,
		Email: email, Secret: icloud.AppleOnboardingSecret{Password: password, SecurityAnswers: answers, Birthday: birthday.Format("2006-01-02")},
		PhoneNumber: phone, FamilyInviteURL: invite,
	}, nil
}

func parseOpened(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "是", "yes", "true", "1", "已开通":
		return true, true
	case "否", "no", "false", "0", "未开通":
		return false, true
	default:
		return false, false
	}
}

func parseAnswer(value string) (icloud.AppleSecurityAnswer, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, ")") {
		return icloud.AppleSecurityAnswer{}, false
	}
	index := strings.LastIndex(value, "(")
	question, answer := strings.TrimSpace(value[:index]), strings.TrimSpace(value[index+1:len(value)-1])
	if index <= 0 || question == "" || answer == "" || utf8.RuneCountInString(question) > 500 || utf8.RuneCountInString(answer) > 500 || strings.ContainsAny(question+answer, "\r\n") {
		return icloud.AppleSecurityAnswer{}, false
	}
	return icloud.AppleSecurityAnswer{Question: question, Answer: answer}, true
}

func looksLikeInvite(value string) bool {
	return strings.Contains(value, "://") || strings.HasPrefix(value, "EFI_")
}

func validInvite(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	if !strings.Contains(value, "://") {
		return len(value) <= 512
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	return parsed.Query().Get("inviteCode") != "" || parsed.Query().Get("token") != ""
}

func digits(value string) string {
	var result strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func openRuntime(ctx context.Context, email string) (*runtime, error) {
	config, err := platform.Load()
	if err != nil {
		return nil, fmt.Errorf("load platform config: %w", err)
	}
	services, cleanup, err := platform.New(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open production services: %w", err)
	}
	fail := func(err error) (*runtime, error) {
		cleanup()
		return nil, err
	}
	settings, err := settingsinfra.NewRepository(services.DB).List(ctx)
	if err != nil {
		return fail(fmt.Errorf("load system settings: %w", err))
	}
	runtimeconfig.Replace(settings)
	proxies, err := proxyapi.NewProxyModule(services.DB, services.Asynq)
	if err != nil {
		return fail(fmt.Errorf("open proxy module: %w", err))
	}
	sms := kitesim.NewService(services.DB, kitesim.NewSyncQueue(services.Asynq))
	sms.SetProxyProvider(proxies.ProxyUseCase)
	files := governanceinfra.NewMinIOFileStore(services.MinIO, services.MinIOBucket)
	icloudService := icloud.NewService(services.DB, services.Asynq, files, services.Redis)
	icloudService.SetAppleProxyProvider(proxies.ProxyUseCase)
	icloudService.SetICloudSMSPhoneService(sms)
	apple := icloud.NewAppleOnboardingClientWithProxyProvider(proxies.ProxyUseCase, services.Redis)
	if strings.TrimSpace(email) == "" || apple == nil {
		return fail(errors.New("apple validation runtime is unavailable"))
	}
	return &runtime{apple: apple, sms: sms, icloud: icloudService, close: cleanup}, nil
}

func (d *debugger) run(binding *kitesim.SMSPhoneBinding, config options) error {
	d.logf("validation=start email=%s region=%q country=%s icloud_opened=%t phone_supplied=%t family_invite_supplied=%t forwarding_override=%t\n", d.input.Email, d.input.Region, d.input.CountryCode, d.input.ICloudOpened, d.input.PhoneNumber != "", d.input.FamilyInviteURL != "", config.forwardTo != "")
	if checkpointCommitted(d.checkpoint) {
		d.logf("validation=already_committed resource_id=%d\n", d.checkpoint.ResourceID)
		return nil
	}
	role := accountRoleForInput(d.input)
	icloudCompleted := false
	if d.checkpoint != nil && d.checkpoint.ICloudReady && !iCloudCheckpointReady(d.checkpoint, role) {
		if err := d.restartAt("icloud_prepare"); err != nil {
			return err
		}
		return errRestartValidation
	}
	if !iCloudCheckpointReady(d.checkpoint, role) {
		if d.checkpoint == nil || !d.checkpoint.ICloudAuthenticated {
			if err := d.checkSMSPhoneBeforePhase(binding, "icloud"); err != nil {
				return err
			}
			if _, err := d.auth(icloud.AppleOnboardingPrepareICloud, "iCloud login", binding); err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.ICloudAuthenticated = true; cp.Stage = "icloud_authenticated" }); err != nil {
				return err
			}
		} else {
			d.logf("checkpoint=skip stage=icloud_authenticated\n")
		}
		finish, err := d.execute(icloud.AppleOnboardingRequest{
			Operation: icloud.AppleOnboardingFinishICloud,
		})
		if err != nil {
			return err
		}
		if finish.ICloudOpened == nil {
			return errors.New("apple did not return iCloud activation state")
		}
		if *finish.ICloudOpened && !channelReady(finish.OldChannel) {
			return &icloud.AppleOnboardingError{
				Category: "old_cookie_missing", SafeMessage: "Apple did not return a usable iCloud V2 session cookie.",
				RestartStage: "icloud_prepare",
			}
		}
		if err := d.markCheckpoint(func(cp *accountCheckpoint) {
			cp.ICloudReady = true
			cp.ICloudOpened = *finish.ICloudOpened
			cp.OldChannel = nil
			if cp.ICloudOpened {
				cp.OldChannel = finish.OldChannel
			}
			cp.CountryCode = firstNonEmpty(finish.CountryCode, cp.CountryCode, d.input.CountryCode)
			cp.Stage = "icloud_ready"
		}); err != nil {
			return err
		}
		d.logf("icloud=ok opened=%t old_cookie=%t\n", *finish.ICloudOpened, *finish.ICloudOpened && channelReady(finish.OldChannel))
		icloudCompleted = true
	} else {
		d.logf("checkpoint=skip stage=icloud_ready old_cookie=%t\n", channelReady(d.checkpoint.OldChannel))
	}
	familyCompleted := false
	if d.input.FamilyInviteURL != "" {
		if d.checkpoint == nil || !d.checkpoint.FamilyJoined {
			if icloudCompleted {
				if err := d.waitForNextPhase(binding, "family"); err != nil {
					return err
				}
			}
			if d.checkpoint == nil || !d.checkpoint.FamilyAuthenticated {
				if err := d.checkSMSPhoneBeforePhase(binding, "family"); err != nil {
					return err
				}
				operation := icloud.AppleOnboardingPrepareFamily
				if _, err := d.authWithRequest(icloud.AppleOnboardingRequest{
					Operation: operation, FamilyInviteURL: d.input.FamilyInviteURL,
				}, "family login", binding); err != nil {
					return err
				}
				if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.FamilyAuthenticated = true; cp.Stage = "family_authenticated" }); err != nil {
					return err
				}
			}
			_, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingJoinFamily})
			if err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.FamilyJoined = true; cp.Stage = "family_joined" }); err != nil {
				return err
			}
			familyCompleted = true
			d.logf("family=joined\n")
		} else {
			d.logf("checkpoint=skip stage=family_joined\n")
		}
	}
	formalCookiesReady := d.checkpoint != nil && d.checkpoint.CookiesReady && channelReady(d.checkpoint.NewChannel)
	forwardTo := firstNonEmpty(config.forwardTo, d.checkpoint.ForwardTo)
	if !formalCookiesReady {
		if familyCompleted || (icloudCompleted && d.input.FamilyInviteURL == "") {
			if err := d.waitForNextPhase(binding, "manage"); err != nil {
				return err
			}
		}
		if d.checkpoint != nil && (d.checkpoint.ManageAuthenticated || d.checkpoint.ManageReady) && !temporaryManageSessionReady(d.checkpoint, time.Now().UTC()) {
			d.logf("checkpoint=expired stage=manage_session\n")
			if err := d.restartAt("manage_prepare"); err != nil {
				return err
			}
		}
		if d.checkpoint == nil || !d.checkpoint.ManageAuthenticated {
			if err := d.checkSMSPhoneBeforePhase(binding, "manage"); err != nil {
				return err
			}
			if _, err := d.auth(icloud.AppleOnboardingPrepareManage, "account management login", binding); err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) {
				cp.ManageAuthenticated = true
				cp.ManageSessionExpiresAt = time.Now().UTC().Add(temporaryManageSessionTTL)
				cp.Stage = "manage_authenticated"
			}); err != nil {
				return err
			}
		} else {
			d.logf("checkpoint=resume stage=manage_authenticated expires_at=%s\n", d.checkpoint.ManageSessionExpiresAt.UTC().Format(time.RFC3339))
		}
		if d.checkpoint == nil || !d.checkpoint.ManageReady {
			profile, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingFetchManage})
			if err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) {
				cp.ManageReady = true
				cp.CountryCode = firstNonEmpty(profile.CountryCode, cp.CountryCode, d.input.CountryCode)
				cp.Stage = "manage_ready"
			}); err != nil {
				return err
			}
		} else {
			d.logf("checkpoint=resume stage=manage_ready\n")
		}
		var err error
		forwardTo, err = d.forwardingAddress(config)
		if err != nil {
			return err
		}
		if d.checkpoint == nil || !d.checkpoint.ForwardAdded {
			added, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingAddForward, ForwardToEmail: forwardTo})
			if err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) {
				cp.ForwardAdded = true
				cp.ForwardTo = forwardTo
				cp.ForwardPending = added.Next == "pending"
				cp.ForwardReady = added.Next != "pending"
				cp.Stage = "forward_added"
			}); err != nil {
				return err
			}
		}
		if d.checkpoint == nil || !d.checkpoint.ForwardReady {
			code := config.forwardCode
			if code == "" {
				if d.checkpoint.ForwardPreparationID != 0 {
					code, err = d.waitForwardingCode(config.ownerUserID)
				} else {
					code, err = d.prompt("forwarding verification code")
				}
				if err != nil {
					return err
				}
			}
			if _, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingVerifyForward, ForwardToEmail: forwardTo, ForwardCode: code}); err != nil {
				return err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) {
				cp.ForwardReady = true
				cp.ForwardPending = false
				cp.Stage = "forward_ready"
			}); err != nil {
				return err
			}
		}
		d.logf("forwarding=ok\n")
		exported, err := d.execute(icloud.AppleOnboardingRequest{
			Operation: icloud.AppleOnboardingExport, ForwardToEmail: forwardTo,
		})
		if err != nil {
			return err
		}
		if exported.NewChannel == nil || exported.NewChannel.Cookie == "" {
			return errors.New("apple did not return a new account session cookie")
		}
		if err := d.markCheckpoint(func(cp *accountCheckpoint) {
			cp.NewChannel = exported.NewChannel
			if exported.OldChannel != nil {
				cp.OldChannel = exported.OldChannel
			}
			cp.CountryCode = firstNonEmpty(exported.CountryCode, cp.CountryCode, d.input.CountryCode)
			cp.CookiesReady = true
			cp.Stage = "cookies_ready"
		}); err != nil {
			return err
		}
	} else {
		d.logf("checkpoint=skip stage=cookies_ready\n")
	}
	if forwardTo == "" {
		return errors.New("checkpoint is missing the verified forwarding address")
	}
	if !checkpointCommitted(d.checkpoint) {
		if d.runtime.icloud == nil {
			return errors.New("iCloud resource service is unavailable for commit")
		}
		forwardPreparationID := d.checkpoint.ForwardPreparationID
		if config.forwardTo != "" {
			forwardPreparationID = 0
		}
		accountRole := accountRoleForInput(d.input)
		if config.legacyCommit {
			accountRole = "unknown"
		}
		result, err := d.runtime.icloud.CommitStandaloneValidatedAccount(d.ctx, config.ownerUserID, icloud.StandaloneValidatedAccount{
			Email: d.input.Email, Region: d.input.Region, CountryCode: firstNonEmpty(d.checkpoint.CountryCode, d.input.CountryCode),
			AccountRole: accountRole, ICloudOpened: d.checkpoint.ICloudOpened,
			FamilyInviteURL: d.input.FamilyInviteURL,
			PhoneNumber:     d.input.PhoneNumber, PhoneCountryCode: d.input.PhoneCountryCode,
			PhoneSource: savedBindingSource(d.checkpoint.Binding), KitesimPhoneID: savedBindingID(d.checkpoint.Binding),
			ForwardToEmail: forwardTo, ForwardPreparationID: forwardPreparationID,
			ExpireAt: d.checkpoint.ExpireAt, Secret: d.input.Secret,
			OldChannel: d.checkpoint.OldChannel, NewChannel: d.checkpoint.NewChannel,
		}, fmt.Sprintf("icloudvalidate:%s", d.input.Email))
		if err != nil {
			return fmt.Errorf("commit iCloud resource: %w", err)
		}
		d.session = nil
		if err := d.markCheckpoint(func(cp *accountCheckpoint) {
			cp.Committed = true
			cp.ResourceID = result.ResourceID
			cp.Session = nil
			cp.ManageSessionExpiresAt = time.Time{}
			cp.Stage = "committed"
		}); err != nil {
			return err
		}
		d.logf("commit=ok resource_id=%d validation_scheduled=%t\n", result.ResourceID, result.ValidationScheduled)
	}
	d.logf("manage=ok country=%s new_cookie=ok\nvalidation=ok cookies_printed=false\n", firstNonEmpty(d.checkpoint.CountryCode, d.input.CountryCode))
	return nil
}

func (d *debugger) auth(operation, label string, binding *kitesim.SMSPhoneBinding) (icloud.AppleOnboardingResponse, error) {
	return d.authWithRequest(icloud.AppleOnboardingRequest{Operation: operation}, label, binding)
}

func (d *debugger) authWithRequest(request icloud.AppleOnboardingRequest, label string, binding *kitesim.SMSPhoneBinding) (icloud.AppleOnboardingResponse, error) {
	if d.checkpoint != nil && d.checkpoint.PendingSMSPurpose != "" && isApplePrepareOperation(request.Operation) {
		d.logf("checkpoint=resume stage=sms_wait purpose=%s label=%q\n", d.checkpoint.PendingSMSPurpose, label)
		return d.smsRound(d.checkpoint.PendingSMSPurpose, label, binding)
	}
	response, err := d.execute(request)
	if err != nil {
		return icloud.AppleOnboardingResponse{}, err
	}
	if response.Next == "" || response.Next == "ready" {
		d.logf("auth=ok label=%q\n", label)
		return response, nil
	}
	if !isSMSPurpose(response.Next) {
		return icloud.AppleOnboardingResponse{}, fmt.Errorf("%s returned unsupported next step %q", label, response.Next)
	}
	if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.PendingSMSPurpose = response.Next; cp.Stage = "sms_wait" }); err != nil {
		return icloud.AppleOnboardingResponse{}, err
	}
	return d.smsRound(response.Next, label, binding)
}

func (d *debugger) checkSMSPhoneBeforePhase(binding *kitesim.SMSPhoneBinding, phase string) error {
	if binding == nil {
		return nil
	}
	if d.checkpoint != nil && d.checkpoint.PendingSMSPurpose != "" {
		d.logf("sms_preflight=resume phase=%s purpose=%s\n", phase, d.checkpoint.PendingSMSPurpose)
		return nil
	}
	return d.waitForSMSPhoneUntil(binding, phase, time.Time{})
}

func (d *debugger) waitForNextPhase(binding *kitesim.SMSPhoneBinding, phase string) error {
	delay := phaseDelayMinimum + time.Duration(rand.Intn(int(phaseDelayMaximum-phaseDelayMinimum)+1))
	deadline := time.Now().UTC().Add(delay)
	d.logf("phase=waiting next=%s delay=%s\n", phase, delay)
	return d.waitForSMSPhoneUntil(binding, phase, deadline)
}

func (d *debugger) waitForSMSPhoneUntil(binding *kitesim.SMSPhoneBinding, phase string, deadline time.Time) error {
	if binding == nil {
		return d.waitUntil(deadline)
	}
	if d.runtime == nil || d.runtime.sms == nil {
		return errors.New("SMS phone service is unavailable")
	}
	for {
		now := time.Now().UTC()
		if !deadline.IsZero() && !deadline.After(now) {
			deadline = time.Time{}
			now = time.Now().UTC()
		}
		err := d.runtime.sms.CheckSMSPhoneAvailable(d.ctx, binding.PhoneID)
		if err == nil {
			if deadline.IsZero() {
				d.logf("sms_preflight=ready phase=%s phone_id=%d\n", phase, binding.PhoneID)
				return nil
			}
		} else {
			retryAt, ok := kitesim.SMSRetryAt(err)
			if !ok {
				return fmt.Errorf("check SMS phone before %s phase: %w", phase, err)
			}
			if !retryAt.After(now) {
				retryAt = now.Add(time.Second)
			}
			if retryAt.After(deadline) {
				deadline = retryAt
			}
			d.logf("sms_preflight=waiting phase=%s phone_id=%d retry_at=%s\n", phase, binding.PhoneID, retryAt.UTC().Format(time.RFC3339))
		}
		if err := d.waitUntil(deadline); err != nil {
			return err
		}
	}
}

func (d *debugger) waitUntil(deadline time.Time) error {
	return waitContextUntil(d.ctx, deadline)
}

func waitContextUntil(ctx context.Context, deadline time.Time) error {
	if !deadline.After(time.Now().UTC()) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *debugger) smsRound(purpose, label string, binding *kitesim.SMSPhoneBinding) (icloud.AppleOnboardingResponse, error) {
	var reservation kitesim.SMSReservation
	reserved := false
	if binding != nil {
		if d.runtime == nil || d.runtime.sms == nil {
			return icloud.AppleOnboardingResponse{}, errors.New("SMS phone service is unavailable")
		}
		for {
			var err error
			reservation, err = d.runtime.sms.ReserveSMSChallenge(d.ctx, binding.PhoneID, purpose, d.smsOwner(purpose), time.Now().UTC().Add(2*time.Minute))
			if err == nil {
				break
			}
			if retryAt, ok := kitesim.SMSRetryAt(err); ok {
				d.logf("sms_challenge=waiting purpose=%s retry_at=%s\n", purpose, retryAt.UTC().Format(time.RFC3339))
				if waitErr := d.waitForSMSPhoneUntil(binding, label, retryAt); waitErr != nil {
					return icloud.AppleOnboardingResponse{}, waitErr
				}
				continue
			}
			resetErr := d.restartSMSCheckpoint(purpose, "challenge_reservation_failed")
			return icloud.AppleOnboardingResponse{}, errors.Join(fmt.Errorf("reserve SMS challenge for %s: %w", label, err), resetErr)
		}
		reserved = true
		d.logf("sms_challenge=reserved challenge_id=%d phone_id=%d purpose=%s status=%s expires_at=%s cooldown_until=%s\n", reservation.ID, reservation.PhoneID, purpose, reservation.Status, reservation.ExpiresAt.UTC().Format(time.RFC3339), optionalTime(reservation.CooldownUntil))
	}
	if !reserved || reservation.Status == kitesim.SMSChallengeReserved {
		d.logf("sms_send=start challenge_id=%d purpose=%s reserved=%t\n", reservation.ID, purpose, reserved)
		if reserved {
			if err := d.runtime.sms.MarkSMSAttemptSent(context.WithoutCancel(d.ctx), reservation.ID); err != nil {
				return icloud.AppleOnboardingResponse{}, fmt.Errorf("mark SMS challenge sent: %w", err)
			}
		}
		request := icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingSendSMS, SMSPurpose: purpose}
		sendResponse, err := d.execute(request)
		if err != nil {
			var appleErr *icloud.AppleOnboardingError
			if reserved && errors.As(err, &appleErr) {
				switch {
				case appleErr.SendRejected:
					_ = d.runtime.sms.MarkSMSAttemptSendFailed(context.WithoutCancel(d.ctx), reservation.ID)
					if resetErr := d.restartSMSCheckpoint(purpose, "send_rejected"); resetErr != nil {
						return icloud.AppleOnboardingResponse{}, errors.Join(err, resetErr)
					}
				case strings.TrimSpace(appleErr.RestartStage) != "":
					// A definite authentication rejection means Apple did not
					// accept this send; release the phone immediately.
					_ = d.runtime.sms.MarkSMSAttemptInfrastructureFailed(context.WithoutCancel(d.ctx), reservation.ID)
				}
			}
			return icloud.AppleOnboardingResponse{}, err
		}
		if reserved {
			if err := d.runtime.sms.ConfirmSMSAttemptSent(context.WithoutCancel(d.ctx), reservation.ID); err != nil {
				return icloud.AppleOnboardingResponse{}, fmt.Errorf("confirm SMS challenge sent: %w", err)
			}
		}
		d.logf("sms_send=accepted challenge_id=%d purpose=%s reserved=%t http_status=%d provider_phone=true\n", reservation.ID, purpose, reserved, sendResponse.HTTPStatus)
	} else {
		d.logf("sms_send=reused challenge_id=%d purpose=%s status=%s\n", reservation.ID, purpose, reservation.Status)
	}
	code, err := d.smsCode(reservation.ID, reserved, purpose)
	if err != nil {
		return icloud.AppleOnboardingResponse{}, err
	}
	if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.Stage = "sms_verify_recover" }); err != nil {
		return icloud.AppleOnboardingResponse{}, err
	}
	response, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingVerifySMS, SMSPurpose: purpose, Code: code})
	if err != nil {
		var appleErr *icloud.AppleOnboardingError
		if reserved && errors.As(err, &appleErr) && (appleErr.CodeRejected || strings.TrimSpace(appleErr.RestartStage) != "") {
			_ = d.runtime.sms.CancelSMSChallenge(context.WithoutCancel(d.ctx), reservation.ID)
		}
		if errors.As(err, &appleErr) && appleErr.CodeRejected {
			if resetErr := d.restartSMSCheckpoint(purpose, "code_rejected"); resetErr != nil {
				return icloud.AppleOnboardingResponse{}, errors.Join(err, resetErr)
			}
		}
		return icloud.AppleOnboardingResponse{}, err
	}
	d.logf("auth=sms_verified label=%q purpose=%s\n", label, purpose)
	if err := d.markCheckpoint(func(cp *accountCheckpoint) {
		cp.PendingSMSPurpose = ""
		cp.Stage = "sms_verified"
		if purpose == icloud.AppleSMSPhoneEnrollment {
			cp.ApplePhoneBound = true
		}
	}); err != nil {
		return icloud.AppleOnboardingResponse{}, err
	}
	if purpose == icloud.AppleSMSPhoneEnrollment {
		d.logf("apple_phone=bound\n")
	}
	if reserved {
		if err := d.runtime.sms.CompleteSMSChallenge(context.WithoutCancel(d.ctx), reservation.ID); err != nil {
			d.logf("sms_challenge=complete_warning challenge_id=%d purpose=%s error_type=%T\n", reservation.ID, purpose, err)
		} else {
			d.logf("sms_challenge=completed challenge_id=%d purpose=%s\n", reservation.ID, purpose)
		}
	}
	return response, nil
}

func (d *debugger) smsCode(challengeID uint64, reserved bool, purpose string) (string, error) {
	if !reserved {
		return d.prompt("Apple verification code for " + purpose)
	}
	challenge, err := d.runtime.sms.GetSMSChallenge(d.ctx, challengeID)
	if err != nil {
		return "", fmt.Errorf("load SMS challenge: %w", err)
	}
	deadline := challenge.ExpiresAt
	started := time.Now()
	polls := 0
	d.logf("sms_poll=start challenge_id=%d phone_id=%d purpose=%s status=%s deadline=%s\n", challenge.ID, challenge.PhoneID, purpose, challenge.Status, deadline.UTC().Format(time.RFC3339))
	for {
		polls++
		message, claimErr := d.runtime.sms.ClaimAppleSMSMessage(d.ctx, challengeID)
		if claimErr == nil && message != nil {
			matches := appleSMSCodePattern.FindStringSubmatch(message.Content)
			if len(matches) == 2 {
				d.logf("sms_poll=matched challenge_id=%d polls=%d duration=%s body_bytes=%d\n", challengeID, polls, elapsed(started), len(message.Content))
				return matches[1], nil
			}
			d.logf("sms_poll=unmatched challenge_id=%d polls=%d duration=%s body_bytes=%d\n", challengeID, polls, elapsed(started), len(message.Content))
			return "", errors.New("apple SMS body did not contain a six-digit code")
		}
		if errors.Is(claimErr, kitesim.ErrSMSChallengeInactive) {
			resetErr := d.restartSMSCheckpoint(purpose, "challenge_inactive")
			return "", errors.Join(errors.New("apple verification SMS challenge is no longer active"), resetErr)
		}
		if claimErr != nil && !errors.Is(claimErr, kitesim.ErrAppleSMSMessageNotFound) {
			return "", fmt.Errorf("poll SMS challenge: %w", claimErr)
		}
		if !deadline.After(time.Now().UTC()) {
			resetErr := d.restartSMSCheckpoint(purpose, "challenge_expired")
			return "", errors.Join(errors.New("apple verification SMS challenge expired"), resetErr)
		}
		if polls == 1 || polls%6 == 0 {
			d.logf("sms_poll=waiting challenge_id=%d polls=%d elapsed=%s remaining=%s\n", challengeID, polls, elapsed(started), time.Until(deadline).Round(time.Second))
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-d.ctx.Done():
			timer.Stop()
			return "", d.ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *debugger) prompt(label string) (string, error) {
	d.logf("manual_input=waiting label=%q\n", label)
	fmt.Fprintf(d.writer(), "%s: ", label)
	line, err := d.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", errors.New(label + " is empty")
	}
	d.logf("manual_input=received label=%q\n", label)
	return value, nil
}

func (d *debugger) execute(request icloud.AppleOnboardingRequest) (icloud.AppleOnboardingResponse, error) {
	request.Email = d.input.Email
	request.Secret = d.input.Secret
	request.PhoneNumber = d.input.PhoneNumber
	request.PhoneCountryCode = firstNonEmpty(request.PhoneCountryCode, d.input.PhoneCountryCode, d.input.CountryCode)
	request.Session = d.session
	started := time.Now()
	d.logf("apple_operation=start operation=%s sms_purpose=%s transaction_present=%t phone_country=%s\n", request.Operation, firstNonEmpty(request.SMSPurpose, "none"), len(request.Session) > 0, firstNonEmpty(request.PhoneCountryCode, "unknown"))
	var response icloud.AppleOnboardingResponse
	var err error
	for {
		response, err = d.runtime.apple.Execute(d.ctx, request)
		if err == nil {
			break
		}
		var appleErr *icloud.AppleOnboardingError
		if !errors.As(err, &appleErr) || !appleErr.ProxyRetryPending || appleErr.RetryAt == nil {
			break
		}
		d.logf("apple_proxy=waiting retry_at=%s\n", appleErr.RetryAt.UTC().Format(time.RFC3339))
		if waitErr := d.waitUntil(*appleErr.RetryAt); waitErr != nil {
			return icloud.AppleOnboardingResponse{}, waitErr
		}
		request.Session = d.session
	}
	if err != nil {
		var appleErr *icloud.AppleOnboardingError
		if errors.As(err, &appleErr) {
			d.logf("apple_operation=failed operation=%s sms_purpose=%s duration=%s category=%q http_status=%d retryable=%t restart_stage=%q send_rejected=%t code_rejected=%t safe_message=%q provider_message=%q\n", request.Operation, firstNonEmpty(request.SMSPurpose, "none"), elapsed(started), appleErr.Category, appleErr.HTTPStatus, appleErr.Retryable, appleErr.RestartStage, appleErr.SendRejected, appleErr.CodeRejected, redactDebugMessage(appleErr.SafeMessage, request), redactDebugMessage(appleErr.ProviderMessage, request))
			if appleErr.Category != "" {
				return icloud.AppleOnboardingResponse{}, fmt.Errorf("apple %s: %s: %w", appleErr.Category, appleErr.SafeMessage, err)
			}
			return icloud.AppleOnboardingResponse{}, fmt.Errorf("%s: %w", appleErr.SafeMessage, err)
		}
		d.logf("apple_operation=failed operation=%s sms_purpose=%s duration=%s error_type=%T\n", request.Operation, firstNonEmpty(request.SMSPurpose, "none"), elapsed(started), err)
		return icloud.AppleOnboardingResponse{}, err
	}
	sessionUpdated := len(response.Session) > 0 && !bytes.Equal(response.Session, d.session)
	if len(response.Session) > 0 {
		d.session = response.Session
	}
	if d.checkpoint != nil {
		d.checkpoint.Session = append(json.RawMessage(nil), d.session...)
		if err := d.persistCheckpoint(); err != nil {
			return icloud.AppleOnboardingResponse{}, fmt.Errorf("save Apple transaction checkpoint: %w", err)
		}
	}
	icloudOpened := false
	if response.ICloudOpened != nil {
		icloudOpened = *response.ICloudOpened
	}
	d.logf("apple_operation=ok operation=%s sms_purpose=%s duration=%s http_status=%d next=%q transaction_updated=%t old_channel=%t new_channel=%t family_channel=%t icloud_opened_known=%t icloud_opened=%t\n", request.Operation, firstNonEmpty(request.SMSPurpose, "none"), elapsed(started), response.HTTPStatus, response.Next, sessionUpdated, channelReady(response.OldChannel), channelReady(response.NewChannel), channelReady(response.FamilyChannel), response.ICloudOpened != nil, icloudOpened)
	return response, nil
}

func (d *debugger) markCheckpoint(update func(*accountCheckpoint)) error {
	if d == nil || d.checkpoint == nil {
		return nil
	}
	update(d.checkpoint)
	d.checkpoint.UpdatedAt = time.Now().UTC()
	return d.persistCheckpoint()
}

func (d *debugger) restartAt(stage string) error {
	stage = strings.TrimSpace(stage)
	if d == nil || d.checkpoint == nil {
		return errors.New("apple restart checkpoint is unavailable")
	}
	if stage == "family_reconcile_prepare" {
		stage = "family_prepare"
	}
	switch stage {
	case "icloud_prepare", "icloud_cookie_prepare", "family_prepare", "manage_prepare":
	default:
		return fmt.Errorf("unsupported Apple restart stage %q", stage)
	}
	d.session = nil
	return d.markCheckpoint(func(cp *accountCheckpoint) {
		cp.Session = nil
		cp.PendingSMSPurpose = ""
		cp.Stage = stage
		switch stage {
		case "icloud_prepare":
			cp.ICloudAuthenticated = false
			cp.ICloudCookieAuth = false
			cp.ICloudReady = false
			cp.OldChannel = nil
		case "icloud_cookie_prepare":
			cp.ICloudCookieAuth = false
			cp.OldChannel = nil
		case "family_prepare":
			cp.FamilyAuthenticated = false
			cp.FamilyJoined = false
		case "manage_prepare":
			cp.ManageAuthenticated = false
			cp.ManageReady = false
			cp.ManageSessionExpiresAt = time.Time{}
		}
	})
}

func (d *debugger) recoverPendingSMSCheckpoint() error {
	if d == nil || d.checkpoint == nil || strings.TrimSpace(d.checkpoint.PendingSMSPurpose) == "" {
		return nil
	}
	purpose := strings.TrimSpace(d.checkpoint.PendingSMSPurpose)
	if d.checkpoint.Stage == "sms_verify_recover" {
		return d.restartSMSCheckpoint(purpose, "verification_result_uncertain")
	}
	if d.checkpoint.Stage != "sms_wait" || len(d.session) == 0 || d.checkpoint.Binding == nil {
		return d.restartSMSCheckpoint(purpose, "checkpoint_incomplete")
	}
	challenge, err := d.runtime.sms.GetSMSChallengeByOwner(d.ctx, d.smsOwner(purpose))
	if errors.Is(err, kitesim.ErrSMSReservationNotFound) {
		return d.restartSMSCheckpoint(purpose, "challenge_missing")
	}
	if err != nil {
		return fmt.Errorf("load checkpoint SMS challenge: %w", err)
	}
	if !reusableSMSCheckpoint(d.checkpoint, challenge, time.Now().UTC()) {
		return d.restartSMSCheckpoint(purpose, "challenge_stale")
	}
	d.logf("checkpoint=resume stage=sms_wait purpose=%s challenge_id=%d status=%s expires_at=%s\n", purpose, challenge.ID, challenge.Status, challenge.ExpiresAt.UTC().Format(time.RFC3339))
	return nil
}

func (d *debugger) restartSMSCheckpoint(purpose, reason string) error {
	restart := appleRestartStage(purpose)
	if restart == "" {
		return fmt.Errorf("unsupported Apple SMS purpose %q", purpose)
	}
	d.logf("checkpoint=restart purpose=%s stage=%s reason=%s\n", purpose, restart, reason)
	d.cancelSMSChallenge(purpose)
	return d.restartAt(restart)
}

func reusableSMSCheckpoint(checkpoint *accountCheckpoint, challenge kitesim.SMSChallenge, now time.Time) bool {
	if checkpoint == nil || checkpoint.Binding == nil || len(checkpoint.Session) == 0 || challenge.PhoneID != checkpoint.Binding.PhoneID || challenge.Purpose != strings.TrimSpace(checkpoint.PendingSMSPurpose) || !challenge.ExpiresAt.After(now) {
		return false
	}
	return challenge.Status == kitesim.SMSChallengeReserved || challenge.Status == kitesim.SMSChallengeSent
}

func (d *debugger) cancelSMSChallenge(purpose string) {
	purpose = strings.TrimSpace(purpose)
	if d == nil || d.runtime == nil || d.runtime.sms == nil || purpose == "" {
		return
	}
	challenge, err := d.runtime.sms.GetSMSChallengeByOwner(context.WithoutCancel(d.ctx), d.smsOwner(purpose))
	if err == nil {
		_ = d.runtime.sms.CancelSMSChallenge(context.WithoutCancel(d.ctx), challenge.ID)
	}
}

func (d *debugger) forwardingAddress(config options) (string, error) {
	if config.forwardTo != "" {
		return config.forwardTo, nil
	}
	if d == nil || d.runtime == nil || d.runtime.icloud == nil {
		return "", errors.New("iCloud forwarding service is unavailable")
	}
	if d != nil && d.checkpoint != nil && d.checkpoint.ForwardPreparationID != 0 && d.checkpoint.ForwardTo != "" {
		view, err := d.runtime.icloud.GetAdminICloudImportPreparation(d.ctx, config.ownerUserID, d.checkpoint.ForwardPreparationID)
		if err == nil && view != nil {
			if !strings.EqualFold(strings.TrimSpace(view.ForwardToEmail), strings.TrimSpace(d.checkpoint.ForwardTo)) {
				return "", errors.New("checkpoint forwarding address does not match its preparation")
			}
			switch view.Status {
			case "waiting", "code_received":
				return d.checkpoint.ForwardTo, nil
			case "expired", "consumed":
				if d.checkpoint.ForwardReady {
					// The Apple-side verification may already have succeeded. Keep
					// this address for an idempotent local commit.
					return d.checkpoint.ForwardTo, nil
				}
				return d.createForwardingPreparation(config.ownerUserID)
			default:
				return "", fmt.Errorf("unsupported forwarding preparation status %q", view.Status)
			}
		}
		if errors.Is(err, icloud.ErrICloudImportPreparationNotFound) || errors.Is(err, icloud.ErrICloudImportPreparationConflict) {
			if d.checkpoint.ForwardReady {
				return "", errors.New("verified forwarding preparation is missing; cannot safely reconcile the Apple-side address")
			}
			return d.createForwardingPreparation(config.ownerUserID)
		}
		if err != nil {
			return "", fmt.Errorf("check forwarding mailbox preparation: %w", err)
		}
		return "", errors.New("forwarding mailbox preparation is unavailable")
	}
	return d.createForwardingPreparation(config.ownerUserID)
}

func (d *debugger) createForwardingPreparation(operatorUserID uint) (string, error) {
	if d == nil || d.runtime == nil || d.runtime.icloud == nil {
		return "", errors.New("iCloud forwarding service is unavailable")
	}
	view, err := d.runtime.icloud.CreateAdminICloudImportPreparation(d.ctx, operatorUserID)
	if err != nil {
		return "", fmt.Errorf("create forwarding mailbox: %w", err)
	}
	if view == nil || view.ID == 0 || strings.TrimSpace(view.ForwardToEmail) == "" {
		return "", errors.New("forwarding mailbox preparation is invalid")
	}
	address := strings.ToLower(strings.TrimSpace(view.ForwardToEmail))
	if err := d.markCheckpoint(func(cp *accountCheckpoint) {
		cp.ForwardPreparationID = view.ID
		cp.ForwardTo = address
		cp.ForwardAdded = false
		cp.ForwardPending = false
		cp.ForwardReady = false
		cp.Stage = "forwarding_prepare"
	}); err != nil {
		return "", err
	}
	d.logf("forwarding=prepared preparation_id=%d address=%s expires_at=%s\n", view.ID, address, view.ExpiresAt.UTC().Format(time.RFC3339))
	return address, nil
}

func (d *debugger) waitForwardingCode(operatorUserID uint) (string, error) {
	if d == nil || d.runtime == nil || d.runtime.icloud == nil || d.checkpoint == nil || d.checkpoint.ForwardPreparationID == 0 {
		return "", errors.New("forwarding mailbox preparation is unavailable")
	}
	preparationID := d.checkpoint.ForwardPreparationID
	started := time.Now()
	polls := 0
	for {
		polls++
		view, err := d.runtime.icloud.GetAdminICloudImportPreparation(d.ctx, operatorUserID, preparationID)
		if err != nil {
			if errors.Is(err, icloud.ErrICloudImportPreparationNotFound) || errors.Is(err, icloud.ErrICloudImportPreparationConflict) {
				d.logf("forwarding=replace preparation_id=%d reason=missing\n", preparationID)
				if _, createErr := d.createForwardingPreparation(operatorUserID); createErr != nil {
					return "", createErr
				}
				return "", errRestartValidation
			}
			return "", fmt.Errorf("poll forwarding mailbox preparation: %w", err)
		}
		if view == nil {
			return "", errors.New("forwarding mailbox preparation response is empty")
		}
		if err == nil && view != nil {
			switch view.Status {
			case "code_received":
				if code := strings.TrimSpace(view.VerificationCode); code != "" {
					d.logf("forwarding_code=received preparation_id=%d polls=%d duration=%s\n", preparationID, polls, elapsed(started))
					return code, nil
				}
			case "expired", "consumed":
				d.logf("forwarding=replace preparation_id=%d status=%s\n", preparationID, view.Status)
				if _, createErr := d.createForwardingPreparation(operatorUserID); createErr != nil {
					return "", createErr
				}
				return "", errRestartValidation
			case "waiting":
			default:
				return "", fmt.Errorf("unsupported forwarding preparation status %q", view.Status)
			}
		}
		if polls == 1 || polls%5 == 0 {
			d.logf("forwarding_code=waiting preparation_id=%d polls=%d elapsed=%s\n", preparationID, polls, elapsed(started))
		}
		timer := time.NewTimer(4 * time.Second)
		select {
		case <-d.ctx.Done():
			timer.Stop()
			return "", d.ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *debugger) persistCheckpoint() error {
	if d == nil || d.checkpoint == nil || d.state == nil || strings.TrimSpace(d.statePath) == "" {
		return nil
	}
	d.state.Accounts[d.stateKey] = *d.checkpoint
	return saveCheckpoint(d.statePath, *d.state)
}

func (d *debugger) writer() io.Writer {
	if d.stdout == nil {
		return io.Discard
	}
	return d.stdout
}

func (d *debugger) logf(format string, args ...any) {
	fmt.Fprintf(d.writer(), format, args...)
}

func (d *debugger) smsOwner(purpose string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(d.input.Email))))
	return "icloudvalidate:" + hex.EncodeToString(digest[:]) + ":" + purpose
}

func isApplePrepareOperation(operation string) bool {
	switch operation {
	case icloud.AppleOnboardingPrepareICloud, icloud.AppleOnboardingPrepareICloudCookie,
		icloud.AppleOnboardingPrepareFamily, icloud.AppleOnboardingPrepareFamilyReconcile,
		icloud.AppleOnboardingPrepareManage:
		return true
	default:
		return false
	}
}

func isSMSPurpose(value string) bool {
	switch value {
	case icloud.AppleSMSICloudLogin, icloud.AppleSMSICloudCookieLogin, icloud.AppleSMSPhoneEnrollment,
		icloud.AppleSMSFamilyLogin, icloud.AppleSMSFamilyReconcileLogin, icloud.AppleSMSManageLogin:
		return true
	default:
		return false
	}
}

func appleRestartStage(purpose string) string {
	switch strings.TrimSpace(purpose) {
	case icloud.AppleSMSICloudLogin, icloud.AppleSMSPhoneEnrollment:
		return "icloud_prepare"
	case icloud.AppleSMSICloudCookieLogin:
		return "icloud_cookie_prepare"
	case icloud.AppleSMSFamilyLogin:
		return "family_prepare"
	case icloud.AppleSMSFamilyReconcileLogin:
		return "family_prepare"
	case icloud.AppleSMSManageLogin:
		return "manage_prepare"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func elapsed(started time.Time) time.Duration {
	return time.Since(started).Round(time.Millisecond)
}

func optionalTime(value time.Time) string {
	if value.IsZero() {
		return "none"
	}
	return value.UTC().Format(time.RFC3339)
}

func channelReady(channel *icloud.AppleOnboardingChannel) bool {
	return channel != nil && strings.TrimSpace(channel.Cookie) != ""
}

func checkpointCommitted(checkpoint *accountCheckpoint) bool {
	return checkpoint != nil && checkpoint.Committed && checkpoint.ResourceID != 0
}

func temporaryManageSessionReady(checkpoint *accountCheckpoint, now time.Time) bool {
	return checkpoint != nil && len(checkpoint.Session) > 0 && checkpoint.ManageSessionExpiresAt.After(now.UTC())
}

func iCloudCheckpointReady(checkpoint *accountCheckpoint, role string) bool {
	_ = role
	if checkpoint == nil || !checkpoint.ICloudReady {
		return false
	}
	return !checkpoint.ICloudOpened || channelReady(checkpoint.OldChannel)
}

func lastDigits(value string, count int) string {
	value = digits(value)
	if len(value) <= count {
		return value
	}
	return value[len(value)-count:]
}

func redactDebugMessage(value string, request icloud.AppleOnboardingRequest) string {
	secrets := []string{request.Secret.Password, request.Code, request.ForwardCode, request.FamilyInviteURL}
	for _, answer := range request.Secret.SecurityAnswers {
		secrets = append(secrets, answer.Answer)
	}
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func accountRoleForInput(_ accountInput) string {
	// Every interactive import is a normal account. The invitation URL is
	// supplied by the operator for this account; it no longer denotes a
	// primary/organizer resource.
	return "child"
}

func savedPhoneBindingFrom(value kitesim.SMSPhoneBinding) *savedPhoneBinding {
	return &savedPhoneBinding{PhoneID: value.PhoneID, PhoneCode: value.PhoneCode, PhoneNumber: value.PhoneNumber, CountryCode: value.CountryCode, Source: value.Source}
}

func (value *savedPhoneBinding) toKitesim() kitesim.SMSPhoneBinding {
	if value == nil {
		return kitesim.SMSPhoneBinding{}
	}
	return kitesim.SMSPhoneBinding{PhoneID: value.PhoneID, PhoneCode: value.PhoneCode, PhoneNumber: value.PhoneNumber, CountryCode: value.CountryCode, Source: value.Source}
}

func savedBindingID(value *savedPhoneBinding) *uint {
	if value == nil || value.PhoneID == 0 {
		return nil
	}
	id := value.PhoneID
	return &id
}

func savedBindingSource(value *savedPhoneBinding) string {
	if value == nil {
		return ""
	}
	return value.Source
}

func accountFingerprint(input accountInput, config options) (string, error) {
	payload := struct {
		Input     accountInput `json:"input"`
		ForwardTo string       `json:"forwardTo,omitempty"`
		OwnerID   uint         `json:"ownerId"`
		ExpireDay int          `json:"expireDays"`
	}{Input: input, ForwardTo: config.forwardTo, OwnerID: config.ownerUserID, ExpireDay: config.expireDays}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
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

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
