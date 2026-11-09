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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"golang.org/x/time/rate"
)

type config struct {
	dir                 string
	confirmedOldMailbox string
	apply, retryFailed  bool
	verifyMail          bool
	skipLocked          bool
	continuous          bool
	continueOnError     bool
	limit, concurrency  int
	ids                 []uint
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var cfg config
	var ids, selectedSource string
	flag.StringVar(&selectedSource, "source-domain", "wsu.edu.kg", "authorized source auxiliary domain: wsu.edu.kg or lclaitech.com")
	flag.StringVar(&cfg.dir, "state-dir", "", "private server-side checkpoint directory")
	flag.BoolVar(&cfg.apply, "apply", false, "claim accounts and migrate auxiliary mailboxes internally")
	flag.IntVar(&cfg.concurrency, "concurrency", 10, "simultaneous accounts, maximum 10")
	flag.IntVar(&cfg.limit, "limit", 0, "maximum accounts for a trial; zero processes the remaining inventory")
	flag.StringVar(&ids, "ids", "", "exact resource IDs for a trial or reconciliation")
	flag.BoolVar(&cfg.retryFailed, "retry-failed", false, "retry failed selected IDs after investigation")
	flag.BoolVar(&cfg.verifyMail, "verify-mail", false, "verify one migrated mailbox, authorizing missing mail credentials before a real fetch")
	flag.BoolVar(&cfg.skipLocked, "skip-locked", false, "exclude confirmed Microsoft account locks with an audit record; uncertain proof changes still stop")
	flag.BoolVar(&cfg.continuous, "continuous", false, "keep processing other accounts and retry failures with durable cooldowns until manually paused")
	flag.BoolVar(&cfg.continueOnError, "continue-on-error", false, "finish an explicit finite sample while retaining failed accounts for scheduled retry")
	flag.StringVar(&cfg.confirmedOldMailbox, "confirmed-old-mailbox", "", "explicitly approved current system mailbox for one exact account; preserves the planned target")
	flag.Parse()
	if err := selectSource(selectedSource); err != nil {
		slog.Error("invalid migration source", "error", err.Error())
		os.Exit(1)
	}
	for _, value := range strings.Split(ids, ",") {
		if value == "" {
			continue
		}
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			slog.Error("invalid resource ID")
			os.Exit(1)
		}
		cfg.ids = append(cfg.ids, uint(id))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		err := run(ctx, cfg)
		if !cfg.continuous || ctx.Err() != nil {
			if err != nil {
				slog.Error("migration stopped", "error", safeError(err))
				os.Exit(1)
			}
			return
		}
		if err != nil {
			slog.Warn("migration runtime will reconnect automatically", "error", safeError(err), "retry_in_seconds", 30)
		}
		if waitForRetry(ctx, 30*time.Second) != nil {
			return
		}
	}
}

func run(ctx context.Context, cfg config) error {
	if cfg.dir == "" || cfg.limit < 0 || cfg.concurrency < 1 || cfg.concurrency > 10 || cfg.retryFailed && len(cfg.ids) == 0 {
		return errors.New("invalid arguments; failure retries require exact IDs")
	}
	if cfg.continuous && (!cfg.apply || cfg.limit != 0 || len(cfg.ids) != 0 || cfg.retryFailed || cfg.verifyMail || cfg.confirmedOldMailbox != "") {
		return errors.New("continuous migration requires full apply without trial or single-account options")
	}
	if cfg.continueOnError && (!cfg.apply || cfg.limit < 1 || len(cfg.ids) == 0 || cfg.continuous || cfg.retryFailed || cfg.verifyMail || cfg.confirmedOldMailbox != "") {
		return errors.New("continue-on-error requires an explicit finite apply sample without other operation options")
	}
	if cfg.verifyMail && (!cfg.apply || cfg.limit != 1 || len(cfg.ids) != 1 || cfg.retryFailed || cfg.confirmedOldMailbox != "") {
		return errors.New("mail verification requires one exact apply trial without migration retry options")
	}
	if cfg.confirmedOldMailbox != "" {
		address, err := mail.ParseAddress(cfg.confirmedOldMailbox)
		if err != nil || address.Address != cfg.confirmedOldMailbox || strings.ContainsAny(cfg.confirmedOldMailbox, "*\r\n\x00") || !cfg.apply || cfg.limit != 1 || len(cfg.ids) != 1 {
			return errors.New("confirmed old mailbox requires a concrete address and one exact apply trial")
		}
	}
	if err := os.MkdirAll(cfg.dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.dir, 0700); err != nil {
		return err
	}
	if err := bindStateSource(cfg.dir); err != nil {
		return err
	}
	if cfg.continuous || cfg.continueOnError {
		if err := waitForResume(ctx, cfg.dir); err != nil {
			return err
		}
	}
	r, err := openRuntime(ctx, cfg.dir)
	if err != nil {
		return err
	}
	defer r.close()
	if cfg.confirmedOldMailbox != "" && !msacl.UsesActiveAuxiliaryDomain(cfg.confirmedOldMailbox) {
		return errors.New("confirmed old mailbox must use an active system domain")
	}
	db, _ := r.db.DB()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, name := range []string{"remail_ms_binding_migrate", "remail_pending_validation", "remail_ms_rebind_fan7z"} {
		var locked int
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&locked); err != nil || locked != 1 {
			return errors.New("another Microsoft maintenance process owns a required lock")
		}
		defer func(name string) { _, _ = conn.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name) }(name)
	}
	var inventory int64
	if err := r.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM microsoft_resources m JOIN microsoft_binding_mailboxes b ON b.resource_id=m.id WHERE SUBSTRING_INDEX(LOWER(TRIM(b.binding_address)),'@',-1)=? AND m.status IN ('normal','abnormal','pending','validating')`, sourceDomain).Scan(&inventory).Error; err != nil {
		return err
	}
	slog.Info("migration preflight passed", "source_domain", sourceDomain, "ledger", table, "source_accounts", inventory, "concurrency", cfg.concurrency, "batch_size", 1000, "trial_limit", cfg.limit)
	if !cfg.apply {
		return nil
	}
	if err := seed(ctx, r.db); err != nil {
		return err
	}
	if cfg.continuous {
		count, err := queueMailRechecks(ctx, r.db)
		if err != nil {
			return err
		}
		slog.Info("mail verification revisions checked", "queued_rechecks", count)
	}
	if cfg.skipLocked {
		var locked []item
		q := r.db.Where("state='failed' AND phase IN ('','login','inspect') AND safe_error=?", msacl.AuthStatusAccountLocked)
		if len(cfg.ids) > 0 {
			q = q.Where("resource_id IN ?", cfg.ids)
		}
		if err := q.Find(&locked).Error; err != nil {
			return err
		}
		for _, row := range locked {
			if err := skipLocked(ctx, r.db, row); err != nil {
				return err
			}
			slog.Info("confirmed account lock excluded", "resource_id", row.ResourceID)
		}
	}
	if cfg.retryFailed {
		if err := r.db.WithContext(ctx).Model(&item{}).Where("resource_id IN ? AND state='failed'", cfg.ids).Updates(map[string]any{"state": "retry", "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
	}
	ctx = msacl.WithCredentialTypeRateLimitRetry(ctx)
	ctx = msacl.WithCredentialTypeRateLimiter(ctx, rate.NewLimiter(1, 1).Wait)
	dispatchCtx, stopDispatch := context.WithCancel(ctx)
	defer stopDispatch()
	// Losing the advisory-lock connection stops dispatch; claimed generations still fence commits.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-dispatchCtx.Done():
				return
			case <-ticker.C:
				if (cfg.continuous || cfg.continueOnError) && manualPauseRequested(cfg.dir) {
					slog.Info("manual pause requested; draining active accounts")
					stopDispatch()
					return
				}
				checkCtx, cancel := context.WithTimeout(dispatchCtx, 5*time.Second)
				var owned bool
				err := conn.QueryRowContext(checkCtx, "SELECT IS_USED_LOCK('remail_ms_binding_migrate')=CONNECTION_ID()").Scan(&owned)
				cancel()
				if err != nil || !owned || r.checkQueues() != nil {
					slog.Error("maintenance isolation lost; stopping dispatch")
					stopDispatch()
					return
				}
			}
		}
	}()
	if cfg.verifyMail {
		return r.verifyMail(dispatchCtx, cfg.ids[0], cfg.skipLocked)
	}
	processed := 0
	nextMailRecheck := time.Now().Add(time.Hour)
	for dispatchCtx.Err() == nil {
		if (cfg.continuous || cfg.continueOnError) && manualPauseRequested(cfg.dir) {
			stopDispatch()
			break
		}
		var rows []item
		if cfg.continueOnError {
			rows, err = pendingTrialWork(dispatchCtx, r.db, cfg.ids, time.Now().UTC())
			if err != nil {
				return err
			}
		} else if cfg.continuous {
			if time.Now().After(nextMailRecheck) {
				count, err := queueMailRechecks(dispatchCtx, r.db)
				if err != nil {
					return err
				}
				slog.Info("mail verification revisions checked", "queued_rechecks", count)
				nextMailRecheck = time.Now().Add(time.Hour)
			}
			rows, err = readyWork(dispatchCtx, r.db, time.Now().UTC())
			if err != nil {
				return err
			}
		} else {
			if err := r.db.WithContext(dispatchCtx).Where("state IN ('running','partial','remote_complete','mail_pending')").Order("resource_id").Find(&rows).Error; err != nil {
				return err
			}
		}
		if len(rows) == 0 && !cfg.continuous && !cfg.continueOnError {
			if err := r.db.WithContext(dispatchCtx).Where("state='retry'").Order("resource_id").Find(&rows).Error; err != nil {
				return err
			}
		}
		if len(rows) == 0 && !cfg.continuous && !cfg.continueOnError {
			var blocked int64
			if err := r.db.WithContext(dispatchCtx).Model(&item{}).Where("state='failed'").Count(&blocked).Error; err != nil {
				return err
			}
			if blocked > 0 {
				return fmt.Errorf("%d unresolved accounts prevent expansion; investigate before claiming another batch", blocked)
			}
			if err := r.db.WithContext(dispatchCtx).Where("state='claimed'").Order("resource_id").Find(&rows).Error; err != nil {
				return err
			}
		}
		if len(rows) == 0 {
			size := 1000
			if cfg.limit > 0 {
				size = min(size, cfg.limit-processed)
				if size <= 0 {
					break
				}
			}
			if err := r.checkQueues(); err != nil {
				return err
			}
			rows, err = claim(dispatchCtx, r.db, size, cfg.ids, cfg.continuous || cfg.continueOnError)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				if cfg.continuous {
					slog.Info("migration waiting for queued work or scheduled retries")
					if err := waitForRetry(dispatchCtx, 30*time.Second); err != nil {
						return err
					}
					continue
				}
				break
			}
			slog.Info("batch claimed as pending", "batch_id", rows[0].BatchID, "accounts", len(rows))
		}
		if cfg.limit > 0 && len(rows) > cfg.limit-processed {
			return errors.New("resume the unfinished batch before starting a smaller trial")
		}
		if err := r.runBatch(dispatchCtx, cfg, rows); err != nil {
			return err
		}
		processed += len(rows)
		if cfg.limit > 0 && processed >= cfg.limit {
			break
		}
	}
	var summary []struct {
		State string
		Count int64
	}
	if err := r.db.Model(&item{}).Select("state,COUNT(*) AS count").Group("state").Scan(&summary).Error; err != nil {
		return err
	}
	slog.Info("migration run finished", "processed", processed, "states", summary)
	return dispatchCtx.Err()
}

func (r *runtime) runBatch(ctx context.Context, cfg config, rows []item) error {
	if cfg.confirmedOldMailbox != "" && (len(cfg.ids) != 1 || len(rows) != 1 || rows[0].ResourceID != cfg.ids[0]) {
		return errors.New("confirmed old mailbox cannot apply to another unfinished account")
	}
	dispatchCtx, stop := context.WithCancel(ctx)
	defer stop()
	jobs := make(chan item)
	var wg sync.WaitGroup
	var successes, failures, skipped atomic.Int64
	var persistenceFailure atomic.Bool
	for n := 0; n < cfg.concurrency; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				if dispatchCtx.Err() != nil {
					return
				}
				if (cfg.continuous || cfg.continueOnError) && manualPauseRequested(cfg.dir) {
					stop()
					return
				}
				// A pause stops new accounts while the active proof replacement gets time to finish.
				accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
				var err error
				if row.State != "mail_pending" {
					err = r.process(accountCtx, row, cfg)
				}
				if err == nil {
					err = r.verifyMail(accountCtx, row.ResourceID, cfg.skipLocked)
					if err != nil {
						saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(accountCtx), 20*time.Second)
						if saveErr := r.db.WithContext(saveCtx).Model(&item{}).Where("resource_id=? AND generation=? AND state='mail_pending'", row.ResourceID, row.Generation).Updates(map[string]any{"safe_error": safeError(err), "updated_at": time.Now().UTC()}).Error; saveErr != nil {
							err = errors.Join(err, saveErr)
						}
						saveCancel()
					}
				}
				cancel()
				if err != nil {
					var state string
					readErr := r.db.Model(&item{}).Select("state").Where("resource_id=?", row.ResourceID).Scan(&state).Error
					if readErr == nil && state == "skipped" {
						skipped.Add(1)
						slog.Info("account excluded under the configured skip policy", "resource_id", row.ResourceID)
					} else {
						failures.Add(1)
						if cfg.continuous || cfg.continueOnError {
							retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
							retryAt, retryErr := scheduleRetry(retryCtx, r.db, row, err, time.Now().UTC())
							retryCancel()
							if retryErr != nil {
								persistenceFailure.Store(true)
								stop()
								slog.Error("retry schedule could not be saved", "resource_id", row.ResourceID, "error", safeError(retryErr))
							} else {
								slog.Warn("account deferred for retry; other accounts continue", "resource_id", row.ResourceID, "retry_at", retryAt, "error", safeError(err))
							}
						} else {
							stop()
							slog.Error("account migration incomplete; expansion stopped", "resource_id", row.ResourceID, "error", safeError(err))
						}
					}
				} else {
					successes.Add(1)
					slog.Info("account migration and mail fetch complete", "resource_id", row.ResourceID)
				}
				slog.Info("batch progress", "success", successes.Load(), "failed", failures.Load(), "skipped", skipped.Load(), "total", len(rows))
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
	wg.Wait()
	if failures.Load() > 0 && ((!cfg.continuous && !cfg.continueOnError) || persistenceFailure.Load()) {
		return errors.New("trial/batch did not reach 100%; unfinished accounts retained for reconciliation")
	}
	return ctx.Err()
}

type checkpoint struct {
	ResourceID uint
	Generation uint64
	Target     string
	Result     msacl.RebindResult
	Cookies    []msacl.SessionCookie
}

func (c checkpoint) validFor(i item) bool {
	target, err := destination(i.ResolvedSource)
	return err == nil && c.ResourceID == i.ResourceID && c.Generation == i.Generation && c.Target == i.TargetAddress && target == i.TargetAddress && c.Result.BindingAddress == i.TargetAddress
}

func beforeChange(stage string) bool { return stage == "" || stage == "login" || stage == "inspect" }

func failureState(uncertain, skipLocked bool, err error) string {
	if uncertain {
		return "partial"
	}
	var auth *msacl.AuthError
	if skipLocked && errors.As(err, &auth) && auth.Status == msacl.AuthStatusAccountLocked {
		return "skipped"
	}
	if errors.As(err, &auth) && auth.Status == msacl.AuthStatusAlreadyBound && strings.Contains(auth.BoundMailbox, "@") && !msacl.UsesActiveAuxiliaryDomain(auth.BoundMailbox) {
		return "skipped"
	}
	return "failed"
}

func (r *runtime) process(ctx context.Context, i item, cfg config) (runErr error) {
	phase := i.Phase
	uncertain := i.State == "running" || i.State == "partial" || i.State == "remote_complete" || !beforeChange(phase)
	defer func() {
		if runErr != nil {
			state := failureState(uncertain, cfg.skipLocked, runErr)
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			if state == "skipped" {
				var auth *msacl.AuthError
				errors.As(runErr, &auth)
				var skipErr error
				if auth.Status == msacl.AuthStatusAccountLocked {
					skipErr = skipLocked(saveCtx, r.db, i)
				} else {
					skipErr = skipExternal(saveCtx, r.db, i, auth.BoundMailbox)
				}
				if skipErr == nil {
					_ = os.Remove(filepath.Join(r.dir, fmt.Sprintf("%d.json", i.ResourceID)))
					return
				}
				runErr = errors.Join(runErr, skipErr)
				state = "failed"
			}
			if err := saveItem(saveCtx, r.db, i, map[string]any{"state": state, "phase": phase, "safe_error": safeError(runErr)}); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	s, err := loadSnapshot(ctx, r.db, i.ResourceID, false)
	if err != nil {
		return err
	}
	if !matches(i, s) {
		return errFence
	}
	path := filepath.Join(r.dir, fmt.Sprintf("%d.json", i.ResourceID))
	var saved checkpoint
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &saved) != nil || !saved.validFor(i) {
			return errors.New("saved checkpoint does not match the claimed account")
		}
		uncertain = uncertain || !beforeChange(saved.Result.Stage)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !saved.Result.Verified || !saved.Result.OldProofRemoved || saved.Result.Stage != "remote_complete" {
		proxy, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{Key: i.Email, IPVersion: proxydomain.ProxyIPv4, Purpose: proxydomain.ProxyPurposeBinding, AllowSystemFallback: true, RequestID: platform.NewUUIDV7String()})
		if err != nil || proxy == nil || proxy.Direct || proxy.URL == "" {
			return errors.New("a healthy production proxy is required; direct fallback was refused")
		}
		release, err := r.lockRoute(ctx, proxy.URL)
		if err != nil {
			return err
		}
		defer release()
		ctx = msacl.WithConcreteRecoveryLeaseKeys(msacl.WithRecoveryLeaseScope(ctx, i.ResourceID, ""))
		if i.TargetAddress == "" {
			i.ResolvedSource = i.SourceAddress
			if strings.Contains(i.ResolvedSource, "*") {
				i.ResolvedSource = msacl.ResolveBindingAddress(ctx, i.SourceAddress, i.Email, proxy.URL, "")
			}
			i.TargetAddress, err = destination(i.ResolvedSource)
			if err != nil {
				return err
			}
			var occupied int64
			if r.db.WithContext(ctx).Table("microsoft_binding_mailboxes").Where("active_binding_address=? AND resource_id<>?", i.TargetAddress, i.ResourceID).Count(&occupied).Error != nil || occupied != 0 {
				return errors.New("replacement address is occupied or could not be checked")
			}
			if err := saveItem(ctx, r.db, i, map[string]any{"resolved_source": i.ResolvedSource, "target_address": i.TargetAddress}); err != nil {
				return err
			}
		}
		current, err := loadSnapshot(ctx, r.db, i.ResourceID, false)
		if err != nil {
			return err
		}
		if !matches(i, current) {
			return errFence
		}
		if err := saveItem(ctx, r.db, i, map[string]any{"state": "running", "attempts": i.Attempts + 1}); err != nil {
			return err
		}
		slog.Info("account migration started", "resource_id", i.ResourceID, "proxy_id", proxy.ID, "proxy_server_id", proxy.ProxyServerID, "attempt", i.Attempts+1)
		oldMailbox := i.ResolvedSource
		if cfg.confirmedOldMailbox != "" {
			oldMailbox = cfg.confirmedOldMailbox
		}
		result, remoteErr := msacl.RebindRecoveryMailbox(ctx, i.Email, s.Password, proxy.URL, oldMailbox, i.TargetAddress, saved.Cookies...)
		phase = result.Stage
		uncertain = uncertain || !beforeChange(phase)
		saved = checkpoint{ResourceID: i.ResourceID, Generation: i.Generation, Target: i.TargetAddress, Result: result, Cookies: result.Cookies}
		if err := writeCheckpoint(path, saved); err != nil {
			uncertain = true
			return err
		}
		if remoteErr != nil {
			return remoteErr
		}
		if !result.Verified || !result.OldProofRemoved || result.Stage != "remote_complete" {
			return errors.New("microsoft proof replacement did not complete")
		}
	}
	phase = "local_commit"
	uncertain = true
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := commit(commitCtx, r.db, i, saved); err != nil {
		return err
	}
	releaseRecoveryLeases(ctx, i.ResourceID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("completed checkpoint cleanup deferred", "resource_id", i.ResourceID)
	}
	return nil
}

func writeCheckpoint(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
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
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func safeError(err error) string {
	message := []rune(msacl.SafeRebindError(err))
	if len(message) > 480 {
		message = message[:480]
	}
	return string(message)
}
