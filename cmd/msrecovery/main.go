// Command msrecovery provides guarded, single-resource Microsoft account
// recovery and validation diagnostics.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	stdmail "net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	systemsettingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	recoveryModeBinding     = "recover-binding"
	recoveryModeReset       = "reset-password"
	recoveryModeReauthorize = "hard-reauthorize"
)

type commandOptions struct {
	ResourceID        uint
	Email             string
	Mode              string
	Apply             bool
	OperatorUserID    uint
	RequestID         string
	Proxy             string
	Timeout           time.Duration
	HistoryWindow     time.Duration
	ConfirmResetEmail string
	ConfirmEmail      string
	AliasCount        int
	PasswordArtifact  string
	JSON              bool
}

type commandResult struct {
	Mode                     string                            `json:"mode"`
	DryRun                   bool                              `json:"dry_run"`
	ResourceID               uint                              `json:"resource_id"`
	AccountEmail             string                            `json:"account_email"`
	ResourceStatus           string                            `json:"resource_status"`
	CurrentBinding           string                            `json:"current_binding,omitempty"`
	CurrentBindingStatus     string                            `json:"current_binding_status,omitempty"`
	Proofs                   []msacl.PasswordRecoveryProofInfo `json:"proofs"`
	MaskedBinding            string                            `json:"masked_binding,omitempty"`
	RecoveredBinding         string                            `json:"recovered_binding,omitempty"`
	BindingResolved          bool                              `json:"binding_resolved"`
	BindingLocallyReceivable bool                              `json:"binding_locally_receivable"`
	BindingConfirmed         bool                              `json:"binding_confirmed"`
	BindingApplied           bool                              `json:"binding_applied"`
	BindingChanged           bool                              `json:"binding_changed"`
	ResourceVersion          uint64                            `json:"resource_version,omitempty"`
	PasswordReset            bool                              `json:"password_reset"`
	DatabasePasswordUpdated  bool                              `json:"database_password_updated"`
	CredentialRevision       uint64                            `json:"credential_revision,omitempty"`
	PasswordArtifactRetained bool                              `json:"password_artifact_retained"`
	SecurityBranch           string                            `json:"security_branch,omitempty"`
	Category                 string                            `json:"category,omitempty"`
	ExternalBinding          bool                              `json:"external_binding"`
	ConsentCleanupAttempted  bool                              `json:"consent_cleanup_attempted"`
	ConsentsBefore           int                               `json:"consents_before"`
	ConsentsRemoved          int                               `json:"consents_removed"`
	ConsentsRemaining        int                               `json:"consents_remaining"`
	NewRefreshTokenObtained  bool                              `json:"new_rt_obtained"`
	NewRefreshTokenPersisted bool                              `json:"new_rt_persisted"`
	OldRefreshTokenChecked   bool                              `json:"old_rt_checked"`
	OldRefreshTokenRejected  bool                              `json:"old_rt_rejected"`
	GraphAvailable           bool                              `json:"graph_available"`
	AliasCandidates          int                               `json:"alias_candidates"`
	AliasAttempted           int                               `json:"alias_attempted"`
	AliasConfirmed           int                               `json:"alias_confirmed"`
	ProxyRoute               string                            `json:"proxy_route,omitempty"`
	ProxyAttempts            int                               `json:"proxy_attempts,omitempty"`
}

type recoveryRuntime struct {
	store   *recoveryStore
	domains map[string]struct{}
	proxies reauthorizeProxyProvider
	close   func() error
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	options, err := parseCommandOptions(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "msrecovery:", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	result, err := executeCommand(ctx, options)
	if result != nil {
		if outputErr := writeCommandResult(os.Stdout, options.JSON, *result); outputErr != nil && err == nil {
			err = outputErr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "msrecovery:", formatRecoveryCommandError(err))
		os.Exit(1)
	}
}

func formatRecoveryCommandError(err error) string {
	if err == nil {
		return ""
	}
	category := ""
	var authErr *msacl.AuthError
	if errors.As(err, &authErr) {
		switch strings.TrimSpace(authErr.Status) {
		case msacl.AuthStatusRateLimited:
			category = "rate_limited"
		case msacl.AuthStatusRequestError, msacl.AuthStatusAuthTimeout, msacl.AuthStatusCodeTimeout:
			category = "retryable"
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		category = "retryable"
	}
	if category == "" {
		return err.Error()
	}
	return "category=" + category + " " + err.Error()
}

func parseCommandOptions(args []string, stderr io.Writer) (commandOptions, error) {
	var options commandOptions
	fs := flag.NewFlagSet("msrecovery", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.UintVar(&options.ResourceID, "resource-id", 0, "Microsoft resource ID (exclusive with -email)")
	fs.StringVar(&options.Email, "email", "", "Microsoft account email (exclusive with -resource-id)")
	fs.StringVar(&options.Mode, "mode", recoveryModeBinding, "recover-binding, reset-password, or hard-reauthorize")
	fs.BoolVar(&options.Apply, "apply", false, "perform and commit the selected operation (default is dry-run)")
	fs.UintVar(&options.OperatorUserID, "operator-user-id", 0, "enabled admin/super-admin user ID required for writes")
	fs.StringVar(&options.RequestID, "request-id", "", "safe audit request ID (generated when omitted)")
	fs.StringVar(&options.Proxy, "proxy", "", "optional Microsoft HTTP proxy override; hard-reauthorize uses the production proxy pool when omitted")
	fs.DurationVar(&options.Timeout, "timeout", 3*time.Minute, "overall command timeout")
	fs.DurationVar(&options.HistoryWindow, "history-window", 90*24*time.Hour, "maximum inbound-mail evidence age")
	fs.StringVar(&options.ConfirmResetEmail, "confirm-reset-email", "", "exact target email required for password reset")
	fs.StringVar(&options.ConfirmEmail, "confirm-email", "", "exact target email required for hard reauthorization")
	fs.IntVar(&options.AliasCount, "alias-count", 2, "explicit aliases to create during hard reauthorization (1 or 2)")
	fs.StringVar(&options.PasswordArtifact, "password-artifact", "", "0600 recovery artifact path required for password reset")
	fs.BoolVar(&options.JSON, "json", false, "emit a machine-readable, secret-free result")
	if err := fs.Parse(args); err != nil {
		return commandOptions{}, err
	}
	if fs.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected positional arguments")
	}
	options.Email = strings.ToLower(strings.TrimSpace(options.Email))
	options.Mode = strings.ToLower(strings.TrimSpace(options.Mode))
	options.RequestID = strings.TrimSpace(options.RequestID)
	options.Proxy = strings.TrimSpace(options.Proxy)
	options.ConfirmResetEmail = strings.ToLower(strings.TrimSpace(options.ConfirmResetEmail))
	options.ConfirmEmail = strings.ToLower(strings.TrimSpace(options.ConfirmEmail))
	options.PasswordArtifact = strings.TrimSpace(options.PasswordArtifact)
	if (options.ResourceID == 0) == (options.Email == "") {
		return commandOptions{}, fmt.Errorf("provide exactly one of -resource-id or -email")
	}
	if options.Mode != recoveryModeBinding && options.Mode != recoveryModeReset && options.Mode != recoveryModeReauthorize {
		return commandOptions{}, fmt.Errorf("unsupported -mode %q", options.Mode)
	}
	if options.Timeout <= 0 || options.HistoryWindow <= 0 {
		return commandOptions{}, fmt.Errorf("timeout and history-window must be positive")
	}
	if options.Apply && options.OperatorUserID == 0 {
		return commandOptions{}, fmt.Errorf("-operator-user-id is required with -apply")
	}
	if options.RequestID == "" {
		options.RequestID = platform.NewUUIDV7String()
	}
	if len(options.RequestID) > 64 {
		return commandOptions{}, fmt.Errorf("request-id must be at most 64 characters")
	}
	if options.Mode == recoveryModeReset {
		if !options.Apply {
			return commandOptions{}, fmt.Errorf("reset-password requires -apply")
		}
		if !envEnabled("MSRECOVERY_PASSWORD_RESET_ENABLED") {
			return commandOptions{}, fmt.Errorf("password reset is disabled; MSRECOVERY_PASSWORD_RESET_ENABLED is not true")
		}
		if options.ConfirmResetEmail == "" {
			return commandOptions{}, fmt.Errorf("reset-password requires -confirm-reset-email")
		}
		if options.PasswordArtifact == "" {
			return commandOptions{}, fmt.Errorf("reset-password requires -password-artifact")
		}
	}
	if options.Mode == recoveryModeReauthorize {
		if options.ResourceID == 0 || options.Email != "" {
			return commandOptions{}, fmt.Errorf("hard-reauthorize requires -resource-id")
		}
		if options.AliasCount < 1 || options.AliasCount > 2 {
			return commandOptions{}, fmt.Errorf("hard-reauthorize requires -alias-count between 1 and 2")
		}
		if options.Apply && options.ConfirmEmail == "" {
			return commandOptions{}, fmt.Errorf("hard-reauthorize requires -confirm-email with -apply")
		}
	}
	return options, nil
}

func executeCommand(ctx context.Context, options commandOptions) (*commandResult, error) {
	runtime, err := openRecoveryRuntime(ctx, options.HistoryWindow)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := runtime.close(); closeErr != nil {
			slog.Warn("close recovery runtime failed", "error", closeErr)
		}
	}()

	snapshot, err := runtime.store.loadSnapshot(ctx, options.ResourceID, options.Email)
	if err != nil {
		return nil, err
	}
	result := newCommandResult(options, *snapshot)
	if options.Mode == recoveryModeReauthorize {
		return executeHardReauthorize(ctx, runtime, options, *snapshot, result)
	}
	if options.Mode == recoveryModeReset && !strings.EqualFold(snapshot.AccountEmail, options.ConfirmResetEmail) {
		return result, fmt.Errorf("confirmed reset email does not match the selected resource")
	}

	if options.Apply {
		if options.Mode == recoveryModeReset {
			err = runtime.store.preflightPasswordReset(ctx, *snapshot, options.OperatorUserID)
		} else {
			err = runtime.store.preflightBindingApply(ctx, *snapshot, options.OperatorUserID)
		}
		if err != nil {
			return result, err
		}
	}

	probe, err := msacl.ProbePasswordRecovery(ctx, snapshot.AccountEmail, options.Proxy, snapshot.preferredVerifiedBinding())
	if err != nil {
		return result, err
	}
	populateProbeResult(result, probe, runtime.domains)
	result.BindingLocallyReceivable = msacl.EvaluateActiveBindingRecoveryEligibility(ctx, probe).Allowed
	if !options.Apply {
		return result, nil
	}
	if !result.BindingResolved || !result.BindingLocallyReceivable || result.RecoveredBinding == "" {
		return result, fmt.Errorf("microsoft recovery proof did not resolve to one unique local binding address")
	}
	if err := confirmRecoveredBinding(ctx, *snapshot, result.RecoveredBinding, options.Proxy); err != nil {
		return result, err
	}
	result.BindingConfirmed = true

	applied, err := runtime.store.applyRecoveredBinding(
		ctx,
		*snapshot,
		result.RecoveredBinding,
		options.OperatorUserID,
		options.RequestID,
		options.Mode == recoveryModeBinding,
	)
	if err != nil {
		return result, err
	}
	result.BindingApplied = true
	result.BindingChanged = applied.Changed
	result.ResourceVersion = applied.ResourceVersion
	if options.Mode == recoveryModeBinding {
		return result, nil
	}
	// The destructive mode has a long remote gap between its initial preflight
	// and the reset call. Recheck the local password, validation lease, operator,
	// and alias pause immediately before generating the new credential.
	if err := runtime.store.preflightPasswordReset(ctx, *snapshot, options.OperatorUserID); err != nil {
		return result, err
	}

	newPassword, err := generatePassword()
	if err != nil {
		return result, err
	}
	artifact, err := createPasswordArtifact(options.PasswordArtifact, newPassword)
	if err != nil {
		return result, err
	}
	result.PasswordArtifactRetained = true

	resetResult, err := msacl.ResetPasswordViaRecovery(ctx, snapshot.AccountEmail, newPassword, options.Proxy, msacl.PasswordRecoveryResetOptions{
		EnablePasswordReset:     true,
		PreferredBindingAddress: result.RecoveredBinding,
		ExpectedBindingAddress:  result.RecoveredBinding,
		CodeTimeout:             90 * time.Second,
	})
	if err != nil {
		return result, fmt.Errorf("password reset did not complete; password artifact retained: %w", err)
	}
	if !resetResult.PasswordReset {
		return result, fmt.Errorf("password reset returned without confirmation; password artifact retained")
	}
	result.PasswordReset = true

	committed, err := runtime.store.commitPasswordReset(
		ctx,
		*snapshot,
		newPassword,
		options.OperatorUserID,
		options.RequestID,
	)
	if err != nil {
		return result, fmt.Errorf("%w; password artifact retained", err)
	}
	result.DatabasePasswordUpdated = true
	result.CredentialRevision = committed.CredentialRevision
	if err := artifact.Remove(); err != nil {
		return result, fmt.Errorf("password reset completed but artifact cleanup failed: %w", err)
	}
	result.PasswordArtifactRetained = false
	return result, nil
}

func confirmRecoveredBinding(ctx context.Context, snapshot recoverySnapshot, bindingAddress, proxy string) error {
	bindingAddress = normalizeConcreteRecoveryBinding(bindingAddress)
	if bindingAddress == "" {
		return fmt.Errorf("recovered binding requires OTP confirmation")
	}
	confirmed, err := msacl.ConfirmPasswordRecoveryBinding(ctx, snapshot.AccountEmail, proxy, msacl.PasswordRecoveryConfirmationOptions{
		PreferredBindingAddress: bindingAddress,
		ExpectedBindingAddress:  bindingAddress,
		CodeTimeout:             90 * time.Second,
	})
	if err != nil {
		return wrapRecoveredBindingConfirmationError(err)
	}
	if !confirmed.BindingConfirmed ||
		normalizeConcreteRecoveryBinding(confirmed.Probe.BindingAddress) != bindingAddress {
		return fmt.Errorf("recovered binding could not be confirmed through the Microsoft recovery proof")
	}
	return nil
}

func wrapRecoveredBindingConfirmationError(err error) error {
	if err == nil {
		return nil
	}
	var authErr *msacl.AuthError
	if errors.As(err, &authErr) && strings.TrimSpace(authErr.Status) == msacl.AuthStatusRateLimited {
		// Keep a stable, secret-free marker for paced operator tooling. Some
		// Microsoft recovery endpoints report a rate-limit code in a JSON body
		// rather than an HTTP 429 status, so matching only the raw HTTP text is
		// not sufficient.
		return fmt.Errorf("recovered binding confirmation rate_limited: %w", err)
	}
	return fmt.Errorf("recovered binding confirmation is temporarily unavailable: %w", err)
}

func openRecoveryRuntime(ctx context.Context, historyWindow time.Duration) (*recoveryRuntime, error) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: platform.NewGormLogger()})
	if err != nil {
		return nil, fmt.Errorf("open recovery database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access recovery database pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping recovery database: %w", err)
	}
	loadRecoveryRuntimeSettings(ctx, db)

	endpoint := strings.TrimSpace(os.Getenv("MINIO_ENDPOINT"))
	accessKey := strings.TrimSpace(os.Getenv("MINIO_ACCESS_KEY"))
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	bucket := strings.TrimSpace(os.Getenv("MINIO_BUCKET"))
	if endpoint == "" || accessKey == "" || secretKey == "" || bucket == "" {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, and MINIO_BUCKET are required")
	}
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: envEnabled("MINIO_USE_SSL"),
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("initialize recovery object storage: %w", err)
	}
	files := governanceinfra.NewMinIOFileStore(minioClient, bucket)
	msacl.SetMailboxReader(mailinfra.NewMSACLMailboxReaderWithContentWindow(db, files, historyWindow))

	store := newRecoveryStore(db)
	domains, allocationDomains, err := store.resources.ListBindingDomains(ctx)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if len(domains) == 0 {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("no normal binding-purpose domains are configured")
	}
	msacl.SetAuxiliaryDomainPolicy(domains, allocationDomains)
	allowedDomains := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		if normalized := strings.Trim(strings.ToLower(strings.TrimSpace(domain)), "."); normalized != "" {
			allowedDomains[normalized] = struct{}{}
		}
	}
	redisAddress := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if redisAddress == "" {
		redisAddress = "127.0.0.1:6379"
	}
	redisDB := 0
	if value, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("REDIS_DB"))); parseErr == nil && value >= 0 {
		redisDB = value
	}
	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddress,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       redisDB,
		PoolSize: 4,
	})
	proxyModule, err := proxyapi.NewProxyModule(db, asynqClient)
	if err != nil {
		_ = asynqClient.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("initialize production proxy selection: %w", err)
	}
	return &recoveryRuntime{
		store:   store,
		domains: allowedDomains,
		proxies: proxyModule.ProxyUseCase,
		close: func() error {
			return errors.Join(asynqClient.Close(), sqlDB.Close())
		},
	}, nil
}

func loadRecoveryRuntimeSettings(ctx context.Context, db *gorm.DB) {
	if db == nil {
		return
	}
	settings, err := systemsettingsinfra.NewRepository(db).List(ctx)
	if err != nil {
		return
	}
	runtimeconfig.Replace(settings)
}

func newCommandResult(options commandOptions, snapshot recoverySnapshot) *commandResult {
	result := &commandResult{
		Mode:           options.Mode,
		DryRun:         !options.Apply,
		ResourceID:     snapshot.ResourceID,
		AccountEmail:   snapshot.AccountEmail,
		ResourceStatus: string(snapshot.Status),
	}
	if snapshot.Binding != nil {
		result.CurrentBinding = snapshot.Binding.BindingAddress
		result.CurrentBindingStatus = string(snapshot.Binding.Status)
	}
	return result
}

func populateProbeResult(result *commandResult, probe msacl.PasswordRecoveryProbeResult, domains map[string]struct{}) {
	result.Proofs = probe.Proofs
	result.MaskedBinding = probe.MaskedBindingAddress
	result.RecoveredBinding = normalizeConcreteRecoveryBinding(probe.BindingAddress)
	result.BindingResolved = probe.BindingResolved && result.RecoveredBinding != ""
	result.BindingLocallyReceivable = isAllowedBindingAddress(result.RecoveredBinding, domains)
}

func isAllowedBindingAddress(address string, domains map[string]struct{}) bool {
	address = normalizeConcreteRecoveryBinding(address)
	if address == "" {
		return false
	}
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	_, ok = domains[strings.Trim(domain, ".")]
	return ok
}

func normalizeConcreteRecoveryBinding(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" || strings.Contains(address, "*") || strings.ContainsAny(address, "\r\n\t ") {
		return ""
	}
	parsed, err := stdmail.ParseAddress(address)
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), address) {
		return ""
	}
	local, domain, ok := strings.Cut(parsed.Address, "@")
	domain = strings.Trim(domain, ".")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return ""
	}
	return strings.ToLower(local + "@" + domain)
}

func writeCommandResult(w io.Writer, jsonOutput bool, result commandResult) error {
	if jsonOutput {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "mode=%s dry_run=%t\n", result.Mode, result.DryRun)
	fmt.Fprintf(&output, "resource_id=%d account_email=%s status=%s\n", result.ResourceID, result.AccountEmail, result.ResourceStatus)
	if result.CurrentBinding != "" {
		fmt.Fprintf(&output, "current_binding=%s current_binding_status=%s\n", result.CurrentBinding, result.CurrentBindingStatus)
	}
	for _, proof := range result.Proofs {
		fmt.Fprintf(&output, "proof type=%s channel=%s masked=%s requires_reentry=%t\n", proof.Type, proof.Channel, proof.MaskedAddress, proof.RequiresReentry)
	}
	fmt.Fprintf(
		&output,
		"recovered_binding=%s resolved=%t locally_receivable=%t confirmed=%t applied=%t changed=%t\n",
		result.RecoveredBinding,
		result.BindingResolved,
		result.BindingLocallyReceivable,
		result.BindingConfirmed,
		result.BindingApplied,
		result.BindingChanged,
	)
	if result.PasswordReset || result.DatabasePasswordUpdated || result.PasswordArtifactRetained {
		fmt.Fprintf(
			&output,
			"password_reset=%t database_updated=%t credential_revision=%d artifact_retained=%t\n",
			result.PasswordReset,
			result.DatabasePasswordUpdated,
			result.CredentialRevision,
			result.PasswordArtifactRetained,
		)
	}
	if result.Mode == recoveryModeReauthorize {
		fmt.Fprintf(&output, "proxy_route=%s proxy_attempts=%d\n", result.ProxyRoute, result.ProxyAttempts)
		fmt.Fprintf(
			&output,
			"security_branch=%s category=%s external_binding=%t cleanup_attempted=%t consents_before=%d removed=%d remaining=%d\n",
			result.SecurityBranch,
			result.Category,
			result.ExternalBinding,
			result.ConsentCleanupAttempted,
			result.ConsentsBefore,
			result.ConsentsRemoved,
			result.ConsentsRemaining,
		)
		fmt.Fprintf(
			&output,
			"new_rt_obtained=%t new_rt_persisted=%t old_rt_checked=%t old_rt_rejected=%t graph_available=%t alias_candidates=%d attempted=%d confirmed=%d credential_revision=%d\n",
			result.NewRefreshTokenObtained,
			result.NewRefreshTokenPersisted,
			result.OldRefreshTokenChecked,
			result.OldRefreshTokenRejected,
			result.GraphAvailable,
			result.AliasCandidates,
			result.AliasAttempted,
			result.AliasConfirmed,
			result.CredentialRevision,
		)
	}
	text := output.String()
	written, err := io.WriteString(w, text)
	if err != nil {
		return err
	}
	if written != len(text) {
		return io.ErrShortWrite
	}
	return nil
}

func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
