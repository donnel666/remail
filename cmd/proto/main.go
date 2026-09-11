// Command proto provides single-resource diagnostics and guarded maintenance.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/joho/godotenv"
)

type options struct {
	Mode, Engine, File, ProxyEnv, RequestID, IdempotencyKey string
	ResourceID, OperatorUserID                              uint
	Version                                                 uint64
	Line, Limit                                             int
	Stdin, Apply, Direct, JSON                              bool
	Timeout, Since                                          time.Duration
}

type result struct {
	Mode                 string        `json:"mode"`
	Engine               string        `json:"engine,omitempty"`
	DryRun               bool          `json:"dryRun"`
	Outcome              string        `json:"outcome"`
	RequestID            string        `json:"requestId"`
	ResourceID           uint          `json:"resourceId,omitempty"`
	Version              uint64        `json:"version,omitempty"`
	CredentialRevision   uint64        `json:"credentialRevision,omitempty"`
	ValidationGeneration uint64        `json:"validationGeneration,omitempty"`
	ResourceStatus       string        `json:"resourceStatus,omitempty"`
	PasswordConfigured   bool          `json:"passwordConfigured"`
	SessionStored        bool          `json:"sessionStored"`
	SessionCurrent       bool          `json:"sessionCurrent"`
	SessionVersion       int           `json:"sessionVersion,omitempty"`
	PKLGenerated         bool          `json:"pklGenerated"`
	KeyMaterialReady     bool          `json:"keyMaterialReady"`
	AddressCount         int           `json:"addressCount,omitempty"`
	KeyCount             *int          `json:"keyCount,omitempty"`
	Queued               bool          `json:"queued"`
	Reused               bool          `json:"reused"`
	Fetched              int           `json:"fetched,omitempty"`
	Complete             bool          `json:"complete"`
	Category             string        `json:"category,omitempty"`
	Stage                string        `json:"stage,omitempty"`
	HTTPStatus           int           `json:"httpStatus,omitempty"`
	APICode              int           `json:"apiCode,omitempty"`
	Message              string        `json:"message,omitempty"`
	Maintenance          []maintenance `json:"maintenance,omitempty"`
}

type maintenance struct {
	ID                   uint64 `json:"id"`
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	Attempts             int    `json:"attempts"`
	ValidationGeneration uint64 `json:"validationGeneration"`
	CredentialRevision   uint64 `json:"credentialRevision"`
}

type safeError string

func (e safeError) Error() string { return string(e) }

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func main() {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "proto:", err)
		os.Exit(2)
	}
	_ = godotenv.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	rt, err := openRuntime(ctx, opts)
	var output *result
	if err == nil {
		if rt != nil {
			defer rt.close()
		}
		var client protoinfra.ProtocolClient
		if opts.Mode == "login" && opts.Apply {
			client = proton.NewPKLClient()
			if opts.Engine == "go" {
				client = proton.NewClient()
			}
		}
		output, err = execute(ctx, opts, os.Stdin, rt, client)
	}
	if output == nil {
		output = &result{Mode: opts.Mode, DryRun: !opts.Apply, RequestID: opts.RequestID}
	}
	if err != nil {
		setFailure(output, err)
	}
	if writeErr := writeResult(os.Stdout, opts.JSON, output); writeErr != nil {
		fmt.Fprintln(os.Stderr, "proto: result output failed")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "proto:", output.Message)
		os.Exit(1)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("proto", flag.ContinueOnError)
	// flag errors otherwise echo malformed values, possibly pasted credentials.
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Mode, "mode", "inspect", "inspect, login, validate, history, or fetch")
	fs.UintVar(&opts.ResourceID, "resource-id", 0, "one Proto resource ID")
	fs.StringVar(&opts.File, "file", "", "login only: credential TXT file; never pass credentials as arguments")
	fs.IntVar(&opts.Line, "line", 0, "login only: one-based physical line in -file")
	fs.BoolVar(&opts.Stdin, "stdin", false, "login only: read one email----password[----base64(PKL)] line; email must include @proton.me or @protonmail.com")
	fs.StringVar(&opts.Engine, "engine", "python", "login only: python (native PKL) or go (legacy comparison)")
	fs.BoolVar(&opts.Apply, "apply", false, "execute remote login or database maintenance; default is preview only")
	fs.UintVar(&opts.OperatorUserID, "operator-user-id", 0, "enabled admin/super-admin required for validate/history/fetch -apply")
	fs.Uint64Var(&opts.Version, "version", 0, "optional expected resource version; current snapshot is always fenced")
	fs.BoolVar(&opts.Direct, "direct", false, "login only: explicitly use a direct connection")
	fs.StringVar(&opts.ProxyEnv, "proxy-env", "", "login only: environment variable containing a proxy URL")
	fs.StringVar(&opts.RequestID, "request-id", "", "safe audit identifier; generated when omitted")
	fs.StringVar(&opts.IdempotencyKey, "idempotency-key", "", "validate/history command key; generated when omitted")
	fs.DurationVar(&opts.Timeout, "timeout", 3*time.Minute, "overall command timeout")
	fs.DurationVar(&opts.Since, "since", 24*time.Hour, "fetch lookback window")
	fs.IntVar(&opts.Limit, "limit", 20, "fetch limit (1-100)")
	fs.BoolVar(&opts.JSON, "json", false, "emit secret-free JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stderr)
			fs.PrintDefaults()
			return opts, flag.ErrHelp
		}
		return opts, safeError("invalid command flags; use -help")
	}
	if fs.NArg() != 0 {
		return opts, safeError("positional arguments are not accepted")
	}
	switch opts.Mode {
	case "inspect", "login", "validate", "history", "fetch":
	default:
		return opts, safeError("unsupported mode")
	}
	selectors := 0
	if opts.ResourceID != 0 {
		selectors++
	}
	if opts.File != "" {
		selectors++
	}
	if opts.Stdin {
		selectors++
	}
	if selectors != 1 || opts.File != "" && opts.Line <= 0 || opts.File == "" && opts.Line != 0 {
		return opts, safeError("select one resource-id, file with a positive line, or stdin")
	}
	invalidScope := false
	fs.Visit(func(f *flag.Flag) {
		if opts.Mode != "login" && (f.Name == "engine" || f.Name == "direct" || f.Name == "proxy-env") {
			invalidScope = true
		}
		if opts.Mode != "fetch" && (f.Name == "since" || f.Name == "limit") {
			invalidScope = true
		}
		if opts.Mode != "validate" && opts.Mode != "history" && f.Name == "idempotency-key" {
			invalidScope = true
		}
		if opts.ResourceID == 0 && f.Name == "version" {
			invalidScope = true
		}
	})
	if invalidScope || opts.Mode != "login" && opts.ResourceID == 0 || opts.Mode == "inspect" && opts.Apply {
		return opts, safeError("flags are not valid for the selected mode")
	}
	if opts.Engine != "python" && opts.Engine != "go" || opts.Direct && opts.ProxyEnv != "" || opts.ProxyEnv != "" && !environmentName.MatchString(opts.ProxyEnv) {
		return opts, safeError("invalid login engine or proxy selection")
	}
	if opts.Mode == "login" && opts.Apply && opts.ResourceID == 0 && !opts.Direct && opts.ProxyEnv == "" {
		return opts, safeError("file/stdin login requires explicit -direct or -proxy-env")
	}
	if opts.Apply && opts.Mode != "login" && opts.OperatorUserID == 0 {
		return opts, safeError("-operator-user-id is required for database maintenance")
	}
	if opts.Timeout <= 0 || opts.Since <= 0 || opts.Limit < 1 || opts.Limit > 100 {
		return opts, safeError("timeout/since must be positive and limit must be 1-100")
	}
	if opts.RequestID == "" {
		opts.RequestID = platform.NewUUIDV7String()
	}
	if !safeIdentifier.MatchString(opts.RequestID) {
		return opts, safeError("request-id must be a safe identifier of at most 64 characters")
	}
	if opts.IdempotencyKey == "" {
		opts.IdempotencyKey = opts.RequestID
	}
	if !safeIdentifier.MatchString(opts.IdempotencyKey) {
		return opts, safeError("idempotency-key must be a safe identifier of at most 64 characters")
	}
	return opts, nil
}

func execute(ctx context.Context, opts options, stdin io.Reader, rt *commandRuntime, login protoinfra.ProtocolClient) (*result, error) {
	out := &result{Mode: opts.Mode, DryRun: !opts.Apply, Outcome: "preview", RequestID: opts.RequestID}
	ctx = context.WithValue(ctx, platform.RequestIDKey, opts.RequestID)
	var resource *protoinfra.Resource
	if opts.ResourceID != 0 {
		if rt == nil || rt.service == nil {
			return out, domain.ErrDependency
		}
		var err error
		resource, err = rt.service.GetResource(ctx, opts.ResourceID, nil)
		if err != nil {
			return out, err
		}
		if opts.Version != 0 && resource.Version != opts.Version {
			return out, domain.ErrVersionConflict
		}
		out.ResourceID, out.Version, out.CredentialRevision = resource.ID, resource.Version, resource.CredentialRevision
		out.ValidationGeneration, out.ResourceStatus = resource.ValidationGeneration, resource.Status
		out.PasswordConfigured = resource.PasswordConfigured
	}
	if opts.Mode == "login" {
		out.Engine = opts.Engine
		var credential domain.ImportLine
		var err error
		if resource == nil {
			credential, err = readCredential(opts, stdin)
		} else if opts.Apply {
			credential, err = rt.loginCredential(ctx, *resource)
		}
		if err != nil {
			return out, err
		}
		if resource == nil {
			out.PasswordConfigured = credential.Password != ""
		}
		if credential.PKLBase64 != "" && opts.Engine == "go" {
			return out, safeError("native PKL requires the python engine")
		}
		if !opts.Apply {
			return out, nil
		}
		proxyURL, err := loginProxy(ctx, opts, rt)
		if err != nil {
			return out, err
		}
		if login == nil {
			return out, domain.ErrDependency
		}
		request := proton.LoginRequest{Email: credential.Email, Password: credential.Password, ProxyURL: proxyURL}
		if credential.PKLBase64 != "" {
			request.PKL, err = base64.StdEncoding.Strict().DecodeString(credential.PKLBase64)
			if err != nil {
				return out, safeError("PKL must be valid standard Base64")
			}
			request.Password = ""
		}
		session, err := login.Login(ctx, request)
		if err != nil {
			return out, err
		}
		if !session.ValidFor(credential.Email) {
			return out, safeError("login returned an invalid or mismatched session")
		}
		out.SessionVersion, out.PKLGenerated, out.AddressCount = session.Version, len(session.PKL) > 0, len(session.Addresses)
		out.KeyMaterialReady = session.KeyFingerprint != "" || len(session.UserKeys) > 0
		if session.Version != 2 {
			count := len(session.UserKeys)
			for _, address := range session.Addresses {
				count += len(address.PrivateKeys)
			}
			out.KeyCount = &count
			out.KeyMaterialReady = count > 0
		}
		out.Outcome = "login_succeeded"
		return out, nil
	}
	if opts.Mode == "inspect" {
		runs, err := rt.service.ListMaintenanceRuns(ctx, resource.ID, 0, 5)
		if err != nil {
			return out, err
		}
		for _, run := range runs.Items {
			out.Maintenance = append(out.Maintenance, maintenance{ID: run.ID, Kind: run.Kind, Status: run.Status, Attempts: run.Attempts, ValidationGeneration: run.ValidationGeneration, CredentialRevision: run.CredentialRevision})
		}
		out.SessionStored, out.SessionCurrent, err = rt.sessionMetadata(ctx, *resource)
		out.Outcome = "inspected"
		return out, err
	}
	if !opts.Apply {
		return out, nil
	}
	if err := rt.requireOperator(ctx, opts.OperatorUserID); err != nil {
		return out, err
	}
	if opts.Mode == "fetch" {
		if rt.fetch == nil {
			return out, domain.ErrDependency
		}
		now := time.Now().UTC()
		fetched, err := rt.fetch(ctx, resource.ID, resource.CredentialRevision, proton.FetchRequest{SinceAt: now.Add(-opts.Since), UntilAt: now, MaxMessages: opts.Limit})
		out.Fetched, out.Complete = len(fetched.Messages), fetched.Complete
		if err != nil {
			return out, err
		}
		out.Outcome = "fetched"
		return out, nil
	}
	accepted, err := rt.service.ExecuteCommand(ctx, protoinfra.Command{ResourceID: resource.ID, Version: resource.Version, Action: opts.Mode, OperatorUserID: opts.OperatorUserID, IdempotencyKey: opts.IdempotencyKey, RequestID: opts.RequestID, Path: "cmd/proto"})
	if err != nil {
		return out, err
	}
	out.Version, out.ValidationGeneration, out.ResourceStatus, out.Reused = accepted.Version, accepted.ValidationGeneration, accepted.Status, accepted.Reused
	if opts.Mode == "validate" {
		err = protoapp.EnqueueValidation(ctx, rt.service.Queue, protoapp.ValidationTaskPayload{ResourceID: resource.ID, OwnerUserID: resource.OwnerUserID, ValidationGeneration: accepted.ValidationGeneration, CredentialRevision: resource.CredentialRevision, RequestID: opts.RequestID})
	} else {
		err = protoapp.EnqueueHistory(ctx, rt.service.Queue, protoapp.HistoryTaskPayload{ResourceID: resource.ID, OwnerUserID: resource.OwnerUserID, ValidationGeneration: accepted.ValidationGeneration, CredentialRevision: resource.CredentialRevision, RequestID: opts.RequestID})
	}
	if err != nil {
		return out, safeError("command committed but enqueue failed; inspect its maintenance state before retrying")
	}
	out.Queued, out.Outcome = true, "queued"
	return out, nil
}

func readCredential(opts options, stdin io.Reader) (domain.ImportLine, error) {
	reader := stdin
	if opts.File != "" {
		file, err := os.Open(opts.File)
		if err != nil {
			return domain.ImportLine{}, safeError("credential file could not be opened")
		}
		defer file.Close()
		reader = file
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), protoinfra.MaxImportLineBytes+2)
	lineNumber, target := 0, opts.Line
	if opts.Stdin {
		target = 1
	}
	var selected string
	for scanner.Scan() {
		lineNumber++
		if lineNumber == target {
			selected = scanner.Text()
			if !opts.Stdin {
				break
			}
		} else if opts.Stdin {
			return domain.ImportLine{}, safeError("stdin must contain exactly one credential line")
		}
	}
	if scanner.Err() != nil {
		return domain.ImportLine{}, safeError("credential input could not be read or exceeds the line limit")
	}
	entries, _, err := protoinfra.ParseImport(selected, domain.ErrorStrategyAbort)
	if err != nil || len(entries) != 1 {
		return domain.ImportLine{}, safeError("selected line must contain email----password[----base64(PKL)]; email must include @proton.me or @protonmail.com")
	}
	return entries[0], nil
}

func setFailure(out *result, err error) {
	out.Outcome, out.Category, out.Message = "failed", "command", "Proto maintenance could not complete."
	var known safeError
	var failure *proton.Failure
	switch {
	case errors.As(err, &known):
		out.Message = known.Error()
	case errors.As(err, &failure):
		out.Category, out.Stage = safeLabel(failure.Category), safeLabel(failure.Stage)
		out.HTTPStatus, out.APICode = failure.HTTPStatus, failure.APICode
		out.Message = "Proto provider operation failed."
	case errors.Is(err, context.Canceled):
		out.Category, out.Message = "canceled", "Command canceled."
	case errors.Is(err, context.DeadlineExceeded):
		out.Category, out.Message = "timeout", "Command timed out; accepted maintenance may continue in the background."
	case errors.Is(err, domain.ErrVersionConflict), errors.Is(err, domain.ErrInvalidClaim):
		out.Category, out.Message = "resource_changed", "Proto resource changed; inspect it before retrying."
	case errors.Is(err, domain.ErrResourceMissing):
		out.Category, out.Message = "not_found", "Proto resource was not found."
	case errors.Is(err, protoinfra.ErrSessionUnavailable), errors.Is(err, protoinfra.ErrSessionBusy):
		out.Category, out.Message = "session_unavailable", "Proto session is unavailable or busy; inspect maintenance state."
	}
}

func safeLabel(value string) string {
	if safeIdentifier.MatchString(value) {
		return value
	}
	return "unknown"
}

func writeResult(w io.Writer, jsonOutput bool, out *result) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(out)
	}
	_, err := fmt.Fprintf(w, "mode=%s outcome=%s dry_run=%t resource_id=%d version=%d credential_revision=%d generation=%d status=%s request_id=%s\nengine=%s pkl_generated=%t key_material_ready=%t addresses=%d queued=%t reused=%t fetched=%d complete=%t\n",
		out.Mode, out.Outcome, out.DryRun, out.ResourceID, out.Version, out.CredentialRevision, out.ValidationGeneration, out.ResourceStatus, out.RequestID,
		out.Engine, out.PKLGenerated, out.KeyMaterialReady, out.AddressCount, out.Queued, out.Reused, out.Fetched, out.Complete)
	if err != nil {
		return err
	}
	if out.Mode == "inspect" {
		if _, err = fmt.Fprintf(w, "password_configured=%t session_stored=%t session_current=%t\n", out.PasswordConfigured, out.SessionStored, out.SessionCurrent); err != nil {
			return err
		}
	}
	if out.KeyCount != nil {
		if _, err = fmt.Fprintf(w, "key_count=%d\n", *out.KeyCount); err != nil {
			return err
		}
	}
	for _, run := range out.Maintenance {
		if _, err = fmt.Fprintf(w, "maintenance_id=%d kind=%s status=%s attempts=%d generation=%d revision=%d\n", run.ID, run.Kind, run.Status, run.Attempts, run.ValidationGeneration, run.CredentialRevision); err != nil {
			return err
		}
	}
	if out.Message != "" {
		_, err = fmt.Fprintf(w, "category=%s stage=%s http_status=%d api_code=%d message=%s\n", out.Category, out.Stage, out.HTTPStatus, out.APICode, out.Message)
	}
	return err
}
