package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/time/rate"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type config struct {
	manifest, dir      string
	cancelPending      string
	keepOldProof       bool
	apply              bool
	inspect            bool
	finishExisting     bool
	limit, concurrency int
	timeout            time.Duration
}
type runtime struct {
	db            *gorm.DB
	proxies       *proxyapp.ProxyUseCase
	mailbox       *forwardingMailbox
	close         func()
	dir           string
	cancelPending string
	keepOldProof  bool
	outputMu      sync.Mutex
	completed     map[string]receipt
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var cfg config
	flag.StringVar(&cfg.manifest, "manifest", "", "JSON list of exact resource IDs and emails")
	flag.StringVar(&cfg.dir, "state-dir", "", "private durable result directory")
	flag.BoolVar(&cfg.apply, "apply", false, "perform rebinding, export, and database disable")
	flag.BoolVar(&cfg.inspect, "inspect-cached", false, "read saved authenticated proof lists without changing accounts")
	flag.BoolVar(&cfg.finishExisting, "finish-existing", false, "finish existing replacement proofs without adding new ones")
	flag.StringVar(&cfg.cancelPending, "cancel-pending-recovery", "", "cancel an explicitly approved pending security-info request with this exact masked destination (one account)")
	flag.BoolVar(&cfg.keepOldProof, "keep-old-proof", false, "verify the target recovery email while retaining the existing Microsoft email proof")
	flag.IntVar(&cfg.limit, "limit", 0, "optional first-account limit for a canary")
	flag.IntVar(&cfg.concurrency, "concurrency", 3, "concurrent accounts")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Minute, "per-account deadline")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		slog.Error("rebind command stopped", "error", safeError(err))
		os.Exit(1)
	}
}

func readManifest(path string) ([]candidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []candidate
	if json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		return nil, errors.New("invalid candidate manifest")
	}
	ids := map[uint]bool{}
	emails := map[string]bool{}
	targets := map[string]bool{}
	for _, row := range rows {
		if row.ExpectedStatus != "" && row.ExpectedStatus != "normal" && row.ExpectedStatus != "abnormal" {
			return nil, errors.New("manifest status must be normal or abnormal")
		}
		address, e := mail.ParseAddress(row.Email)
		if (row.ID == 0) != (row.Password != "") || row.ID == 0 && row.ExpectedStatus != "" || e != nil || address.Address != row.Email || row.Email != strings.ToLower(row.Email) || row.ID != 0 && ids[row.ID] || emails[row.Email] || targets[targetAddress(row.Email)] {
			return nil, errors.New("manifest contains an invalid or duplicate resource")
		}
		ids[row.ID], emails[row.Email], targets[targetAddress(row.Email)] = true, true, true
	}
	return rows, nil
}

func run(ctx context.Context, cfg config) error {
	if cfg.manifest == "" || cfg.dir == "" || cfg.limit < 0 || cfg.concurrency < 1 || cfg.concurrency > 10 || cfg.timeout < time.Minute {
		return errors.New("invalid command arguments")
	}
	rows, err := readManifest(cfg.manifest)
	if err != nil {
		return err
	}
	if cfg.cancelPending != "" && (len(rows) != 1 || cfg.inspect || cfg.finishExisting || !strings.Contains(cfg.cancelPending, "@")) {
		return errors.New("pending recovery cancellation requires one exact account and an approved destination")
	}
	if cfg.limit > 0 && cfg.limit < len(rows) {
		rows = rows[:cfg.limit]
	}
	if err := os.MkdirAll(filepath.Join(cfg.dir, "receipts"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.dir, "sessions"), 0700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.dir, 0700); err != nil {
		return err
	}
	r, err := openRuntime(ctx, cfg.dir)
	if err != nil {
		return err
	}
	defer r.close()
	r.cancelPending = cfg.cancelPending
	r.keepOldProof = cfg.keepOldProof
	if cfg.inspect {
		return r.inspectCached(ctx, rows)
	}
	sqlDB, _ := r.db.DB()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('remail_ms_rebind_fan7z',0)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("another rebind run owns the database lock")
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "DO RELEASE_LOCK('remail_ms_rebind_fan7z')") }()
	if err := r.restoreExports(); err != nil {
		return err
	}
	if _, err := r.mailbox.List(ctx, targetAddress(rows[0].Email), 5, false); err != nil {
		return err
	}
	if !cfg.apply {
		for _, row := range rows {
			if _, ok := r.completed[accountKey(row.ID, row.Email)]; ok {
				continue
			}
			if _, err := loadSnapshot(ctx, r.db, row); err != nil {
				return fmt.Errorf("resource %d: %w", row.ID, err)
			}
		}
		slog.Info("preflight passed", "targets", len(rows), "qq_imap", true)
		return nil
	}
	var success, failed, deferred, skipped atomic.Int64
	limiter := rate.NewLimiter(1, 1)
	ctx = msacl.WithCredentialTypeRateLimitRetry(ctx)
	ctx = msacl.WithCredentialTypeRateLimiter(ctx, limiter.Wait)
	dispatchCtx, stopDispatch := context.WithCancel(ctx)
	defer stopDispatch()
	jobs := make(chan candidate)
	var workers sync.WaitGroup
	for i := 0; i < cfg.concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for row := range jobs {
				if dispatchCtx.Err() != nil {
					return
				}
				// Stop dispatch on pause; let an account already changing proofs finish its bounded workflow.
				accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.timeout)
				if cfg.finishExisting {
					accountCtx = msacl.WithExistingRecoveryProofOnly(accountCtx)
				}
				err := r.process(accountCtx, row)
				cancel()
				if errors.Is(err, msacl.ErrRecoveryProofNotStarted) {
					deferred.Add(1)
					slog.Info("account deferred without creating replacement", "resource_id", row.ID, "email", row.Email)
					_ = writePrivateJSON(filepath.Join(cfg.dir, "failure-"+accountKey(row.ID, row.Email)+".json"), map[string]any{"id": row.ID, "email": row.Email, "stage": "not_started", "error": err.Error(), "at": time.Now().UTC()})
				} else if isExternalBindingSkip(err) {
					skipped.Add(1)
					key := accountKey(row.ID, row.Email)
					var auth *msacl.AuthError
					errors.As(err, &auth)
					path := filepath.Join(cfg.dir, "skip-"+key+".json")
					_, writeErr := os.Stat(path)
					if errors.Is(writeErr, os.ErrNotExist) {
						writeErr = writePrivateJSON(path, map[string]any{"id": row.ID, "email": row.Email, "stage": "external_binding", "binding_address": auth.BoundMailbox, "reason": safeError(err), "at": time.Now().UTC()})
					}
					if writeErr != nil {
						stopDispatch()
						slog.Error("skip result could not be saved", "email", row.Email, "error", safeError(writeErr))
					} else {
						_ = os.Remove(filepath.Join(cfg.dir, "failure-"+key+".json"))
					}
					slog.Info("account skipped with external recovery email", "resource_id", row.ID, "email", row.Email, "reason", safeError(err))
				} else if err != nil {
					failed.Add(1)
					phase := "before_change"
					var incomplete *incompleteRebindError
					if errors.As(err, &incomplete) {
						phase = incomplete.stage
						stopDispatch()
						slog.Warn("dispatch stopped to reconcile an incomplete account", "resource_id", row.ID, "stage", phase)
					}
					slog.Error("account failed", "resource_id", row.ID, "email", row.Email, "error", safeError(err))
					_ = writePrivateJSON(filepath.Join(cfg.dir, "failure-"+accountKey(row.ID, row.Email)+".json"), map[string]any{"id": row.ID, "email": row.Email, "stage": phase, "error": safeError(err), "at": time.Now().UTC()})
				} else {
					success.Add(1)
					slog.Info("account complete", "resource_id", row.ID, "email", row.Email, "binding_address", targetAddress(row.Email))
				}
				slog.Info("progress", "success", success.Load(), "failed", failed.Load(), "deferred", deferred.Load(), "skipped", skipped.Load(), "total", len(rows))
			}
		}()
	}
dispatch:
	for _, row := range rows {
		select {
		case jobs <- row:
		case <-dispatchCtx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	summary := map[string]any{"total": len(rows), "success": success.Load(), "failed": failed.Load(), "deferred": deferred.Load(), "skipped": skipped.Load(), "unstarted": len(rows) - int(success.Load()+failed.Load()+deferred.Load()+skipped.Load()), "dispatch_stopped": dispatchCtx.Err() != nil, "at": time.Now().UTC()}
	if err := writePrivateJSON(filepath.Join(cfg.dir, "summary.json"), summary); err != nil {
		return err
	}
	slog.Info("batch finished", "success", success.Load(), "failed", failed.Load(), "deferred", deferred.Load(), "skipped", skipped.Load(), "total", len(rows))
	return nil
}

func isExternalBindingSkip(err error) bool {
	var auth *msacl.AuthError
	var incomplete *incompleteRebindError
	return !errors.As(err, &incomplete) && errors.As(err, &auth) && auth.Status == msacl.AuthStatusAlreadyBound && strings.Contains(auth.BoundMailbox, "@") && !msacl.UsesActiveAuxiliaryDomain(auth.BoundMailbox)
}

func openRuntime(ctx context.Context, dir string) (*runtime, error) {
	if os.Getenv("MYSQL_DSN") == "" {
		return nil, errors.New("MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("MYSQL_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("database initialization failed")
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(12)
	sqlDB.SetMaxIdleConns(4)
	var operator int64
	if err := db.WithContext(ctx).Table("users").Where("id=1 AND status='active' AND role IN ('admin','super_admin')").Count(&operator).Error; err != nil || operator != 1 {
		_ = sqlDB.Close()
		return nil, errors.New("operator 1 is not an active administrator")
	}
	settings, err := settingsinfra.NewRepository(db).List(ctx)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	runtimeconfig.Replace(settings)
	servers, err := runtimeconfig.ParseICloudLivenessSMTPServers(runtimeconfig.String(runtimeconfig.ICloudLivenessSMTPServersKey, "[]"))
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	var qq []runtimeconfig.ICloudLivenessSMTPServer
	for _, server := range servers {
		if server.Enabled && strings.HasPrefix(strings.ToLower(server.Addr), "smtp.qq.com:") && server.Password != "" {
			qq = append(qq, server)
		}
	}
	if len(qq) != 1 {
		_ = sqlDB.Close()
		return nil, errors.New("exactly one QQ liveness mailbox is required")
	}
	client, err := minio.New(os.Getenv("MINIO_ENDPOINT"), &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY"), ""), Secure: strings.EqualFold(os.Getenv("MINIO_USE_SSL"), "true")})
	if err != nil {
		_ = sqlDB.Close()
		return nil, errors.New("object store initialization failed")
	}
	files := governanceinfra.NewMinIOFileStore(client, os.Getenv("MINIO_BUCKET"))
	reader := &forwardingMailbox{local: mailinfra.NewMSACLMailboxReaderWithContentWindow(db, files, 24*time.Hour), username: qq[0].Username, password: qq[0].Password}
	domains, _, err := coreinfra.NewResourceRepo(db).ListBindingDomains(ctx)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	msacl.SetAuxiliaryDomainPolicy(append(domains, "fan7z.com"), []string{"fan7z.com"})
	msacl.SetMailboxReader(reader)
	msacl.SetRecoveryLeaseStore(mailinfra.NewMicrosoftBindingRecoveryLeaseStore(db))
	redisOption := asynq.RedisClientOpt{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD"), PoolSize: 4}
	inspector := asynq.NewInspector(redisOption)
	for _, queue := range []string{platform.QueueBackgroundValidation, platform.QueueBackgroundProjectHistory, platform.QueueBackgroundAlias} {
		q, e := inspector.GetQueueInfo(queue)
		if e != nil || !q.Paused || q.Active != 0 {
			_ = inspector.Close()
			_ = sqlDB.Close()
			return nil, errors.New("microsoft maintenance queues must already be paused and idle")
		}
	}
	_ = inspector.Close()
	asynqClient := asynq.NewClient(redisOption)
	proxyModule, err := proxyapi.NewProxyModule(db, asynqClient)
	if err != nil {
		_ = asynqClient.Close()
		_ = sqlDB.Close()
		return nil, err
	}
	return &runtime{db: db, proxies: proxyModule.ProxyUseCase, mailbox: reader, dir: dir, completed: map[string]receipt{}, close: func() { reader.Close(); _ = asynqClient.Close(); _ = sqlDB.Close() }}, nil
}

type incompleteRebindError struct {
	stage string
	err   error
}

func (e *incompleteRebindError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *incompleteRebindError) Unwrap() error { return e.err }

func (r *runtime) process(ctx context.Context, row candidate) (runErr error) {
	key := accountKey(row.ID, row.Email)
	partialStage := ""
	defer func() {
		if runErr != nil && partialStage != "" {
			runErr = &incompleteRebindError{stage: partialStage, err: runErr}
		}
	}()
	r.outputMu.Lock()
	done, ok := r.completed[key]
	r.outputMu.Unlock()
	if ok {
		if done.Snapshot.Email != row.Email {
			return errors.New("completed receipt identity differs")
		}
		return nil
	}
	path := filepath.Join(r.dir, "receipts", key+".json")
	var saved receipt
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &saved) != nil || saved.Snapshot.ID != row.ID || saved.Snapshot.Email != row.Email || row.ID == 0 && saved.Snapshot.Password != row.Password || !saved.RemoteVerified {
			return errors.New("durable receipt is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if data, readErr := os.ReadFile(filepath.Join(r.dir, "skip-"+key+".json")); readErr == nil {
			var skipped struct {
				ID             uint   `json:"id"`
				Email          string `json:"email"`
				Stage          string `json:"stage"`
				BindingAddress string `json:"binding_address"`
				Reason         string `json:"reason"`
			}
			if json.Unmarshal(data, &skipped) != nil || skipped.ID != row.ID || skipped.Email != row.Email || skipped.Stage != "external_binding" || skipped.BindingAddress == "" {
				return errors.New("saved skip record is invalid")
			}
			return &msacl.AuthError{Status: msacl.AuthStatusAlreadyBound, BoundMailbox: skipped.BindingAddress, Message: skipped.Reason}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		s, err := loadSnapshot(ctx, r.db, row)
		if err != nil {
			return err
		}
		proxy, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{Key: row.Email, IPVersion: proxydomain.ProxyIPv4, Purpose: proxydomain.ProxyPurposeBinding, AllowSystemFallback: true, RequestID: platform.NewUUIDV7String()})
		if err != nil {
			return errors.New("production proxy selection failed")
		}
		proxyURL := ""
		if proxy != nil && !proxy.Direct {
			proxyURL = proxy.URL
		}
		slog.Info("account started", "resource_id", row.ID, "email", row.Email)
		ctx = msacl.WithConcreteRecoveryLeaseKeys(msacl.WithRecoveryLeaseScope(ctx, row.ID, ""))
		if r.keepOldProof {
			ctx = msacl.WithRetainedRecoveryProof(ctx)
		}
		cookiePath := filepath.Join(r.dir, "sessions", key+".json")
		var cookies []msacl.SessionCookie
		if data, readErr := os.ReadFile(cookiePath); readErr == nil {
			if json.Unmarshal(data, &cookies) != nil {
				return errors.New("saved session file is invalid")
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if r.cancelPending != "" {
			cancelled, cancelErr := msacl.CancelPendingSecurityInfo(ctx, s.Email, proxyURL, s.BindingAddress, r.cancelPending, cookies...)
			if cancelled.Stage == "cancel_pending_recovery" || cancelled.Cancelled {
				partialStage = "pending_recovery_cancellation"
			}
			if cancelled.Cancelled || cancelled.RecoveryCode != "" {
				dir := filepath.Join(r.dir, "security-cancellations")
				if err := os.MkdirAll(dir, 0700); err != nil {
					return err
				}
				evidence := map[string]any{"resource_id": row.ID, "email": row.Email, "previous_pending_destination": r.cancelPending, "cancelled": cancelled.Cancelled, "recovery_code": cancelled.RecoveryCode, "at": time.Now().UTC()}
				if err := writePrivateJSON(filepath.Join(dir, key+".json"), evidence); err != nil {
					return err
				}
				slog.Info("pending security-info request cancelled", "resource_id", row.ID, "recovery_code_saved", cancelled.RecoveryCode != "")
			}
			if len(cancelled.Cookies) > 0 {
				cookies = cancelled.Cookies
				if err := writePrivateJSON(cookiePath, cookies); err != nil {
					return err
				}
			}
			if cancelErr != nil {
				return cancelErr
			}
		}
		result, err := msacl.RebindRecoveryMailbox(ctx, s.Email, s.Password, proxyURL, s.BindingAddress, targetAddress(s.Email), cookies...)
		if result.Stage != "" && result.Stage != "login" && result.Stage != "inspect" {
			partialStage = result.Stage
		}
		if len(result.Cookies) > 0 {
			if saveErr := writePrivateJSON(cookiePath, result.Cookies); saveErr != nil {
				return saveErr
			}
		}
		if err != nil {
			return err
		}
		if !result.Verified || !result.OldProofRemoved && !r.keepOldProof {
			return errors.New("remote proof replacement did not finish")
		}
		saved = receipt{Operation: result.Operation, Snapshot: s, Target: result.BindingAddress, RemoteVerified: true, OldProofRetained: !result.OldProofRemoved, RequestID: platform.NewUUIDV7String()}
		if err := writePrivateJSON(path, saved); err != nil {
			return err
		}
	}
	partialStage = "local_commit_pending"
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := commitRebind(commitCtx, r.db, saved); err != nil {
		return err
	}
	saved.Committed = true
	if err := writePrivateJSON(path, saved); err != nil {
		return err
	}
	r.outputMu.Lock()
	defer r.outputMu.Unlock()
	partialStage = "export_pending"
	r.completed[key] = saved
	if err := r.writeExport(); err != nil {
		delete(r.completed, key)
		return err
	}
	_ = os.Remove(filepath.Join(r.dir, "failure-"+key+".json"))
	_ = os.Remove(filepath.Join(r.dir, "skip-"+key+".json"))
	_ = os.Remove(filepath.Join(r.dir, "sessions", key+".json"))
	return nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".rebind-write-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (r *runtime) restoreExports() error {
	files, err := filepath.Glob(filepath.Join(r.dir, "receipts", "*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var saved receipt
		if json.Unmarshal(data, &saved) != nil {
			return errors.New("invalid saved receipt")
		}
		if saved.Committed && saved.RemoteVerified {
			r.completed[accountKey(saved.Snapshot.ID, saved.Snapshot.Email)] = saved
		}
	}
	return r.writeExport()
}

func (r *runtime) writeExport() error {
	ids := make([]string, 0, len(r.completed))
	for id := range r.completed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out strings.Builder
	for _, id := range ids {
		saved := r.completed[id]
		line, err := exportLine(saved.Snapshot, saved.Target)
		if err != nil {
			return err
		}
		out.WriteString(line)
	}
	return writePrivate(filepath.Join(r.dir, "success.txt"), []byte(out.String()))
}

var sensitiveErrorPattern = regexp.MustCompile(`https?://\S+|[A-Za-z0-9+/_%=-]{64,}`)

func (r *runtime) inspectCached(ctx context.Context, rows []candidate) error {
	for _, row := range rows {
		data, err := os.ReadFile(filepath.Join(r.dir, "sessions", accountKey(row.ID, row.Email)+".json"))
		if err != nil {
			slog.Warn("proof inspection", "resource_id", row.ID, "error", "no saved session")
			continue
		}
		var cookies []msacl.SessionCookie
		if json.Unmarshal(data, &cookies) != nil {
			return errors.New("invalid saved session")
		}
		proxy, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{Key: row.Email, IPVersion: proxydomain.ProxyIPv4, Purpose: proxydomain.ProxyPurposeBinding, AllowSystemFallback: true})
		if err != nil {
			return err
		}
		proxyURL := ""
		if proxy != nil && !proxy.Direct {
			proxyURL = proxy.URL
		}
		probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		proofs, err := msacl.InspectRecoveryMailboxes(probeCtx, row.Email, proxyURL, cookies)
		cancel()
		if err != nil {
			slog.Warn("proof inspection", "resource_id", row.ID, "email", row.Email, "error", safeError(err))
		} else {
			slog.Info("proof inspection", "resource_id", row.ID, "email", row.Email, "proofs", proofs)
		}
	}
	return nil
}

func safeError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "interrupted by cancellation"
	}
	var auth *msacl.AuthError
	if errors.As(err, &auth) {
		return msacl.SafeRebindError(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "account operation deadline exceeded"
	}
	message := sensitiveErrorPattern.ReplaceAllString(err.Error(), "[redacted]")
	if len(message) > 350 {
		message = message[:350]
	}
	return message
}
