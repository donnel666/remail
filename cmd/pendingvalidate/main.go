// pendingvalidate selects administrator-owned Microsoft resources that are
// still pending and delegates the actual state machine to the existing
// luckmail runner.  Keeping selection here makes the new cleanup an isolated
// command while leaving luckmail's default input/policy untouched.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type config struct {
	apply             bool
	ownerUserID       uint64
	luckmailFile      string
	inputFile         string
	stateDir          string
	tool              string
	limit             int
	concurrency       int
	startupInterval   time.Duration
	credentialTypeRPS int
	duration          time.Duration
	outsideOnly       bool
	continuous        bool
	refillThreshold   int
	runID             string
}

type candidate struct {
	id    uint64
	email string
}

type poolPaths struct {
	input    string
	state    string
	manifest string
}

// poolPrefetchResult keeps the next 1000 candidates in memory until the
// current runner exits. If the process is interrupted, no half-written next
// pool is committed and the next start simply queries pending rows again.
type poolPrefetchResult struct {
	candidates []candidate
	err        error
}

const activePoolPointerName = "pending-validation-active"

const rollingPoolMax = 1000

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() config {
	var cfg config
	flag.BoolVar(&cfg.apply, "apply", false, "select and process resources; omitted means dry-run")
	flag.Uint64Var(&cfg.ownerUserID, "owner-user-id", 1, "resource owner; must be an active administrator")
	flag.StringVar(&cfg.luckmailFile, "luckmail-file", "", "optional luckmail email set used for prioritization")
	flag.StringVar(&cfg.inputFile, "input", "/state/pending-validation-input.txt", "durable selected-email file")
	flag.StringVar(&cfg.stateDir, "state-dir", "/state", "durable state directory passed to luckmailvalidate")
	flag.StringVar(&cfg.tool, "tool", "/tool/luckmailvalidate", "existing validation runner binary")
	flag.IntVar(&cfg.limit, "limit", 1000, "rolling candidate-pool size; use a smaller value for debugging")
	flag.IntVar(&cfg.concurrency, "concurrency", 10, "validation worker count")
	flag.DurationVar(&cfg.startupInterval, "startup-interval", 2*time.Second, "worker ramp interval")
	flag.IntVar(&cfg.credentialTypeRPS, "credential-type-rps", 3, "GetCredentialType requests per second")
	flag.DurationVar(&cfg.duration, "duration", 0, "stop after this duration; zero means run to completion")
	flag.BoolVar(&cfg.outsideOnly, "outside-luckmail-only", true, "select only resources absent from luckmail-file")
	flag.BoolVar(&cfg.continuous, "continuous", true, "keep selecting candidate pools until no matching resources remain; pass -continuous=false for one debug pool")
	flag.IntVar(&cfg.refillThreshold, "refill-threshold", 500, "select the next candidate pool when the active pool falls to this many remaining")
	flag.StringVar(&cfg.runID, "run-id", "", "runner run identifier")
	flag.Parse()
	return cfg
}

func run(parent context.Context, cfg config) error {
	if cfg.ownerUserID == 0 || cfg.limit < 1 || cfg.limit > 100000 || cfg.refillThreshold < 1 || cfg.concurrency < 1 || cfg.concurrency > 500 || cfg.startupInterval < 0 || cfg.credentialTypeRPS < 0 || cfg.duration < 0 {
		return errors.New("invalid pending validation limits")
	}
	if cfg.refillThreshold >= cfg.limit {
		cfg.refillThreshold = max(1, cfg.limit/2)
	}
	if cfg.continuous && cfg.limit > rollingPoolMax {
		cfg.limit = rollingPoolMax
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		return errors.New("MYSQL_DSN is required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.inputFile), 0o750); err != nil {
		return fmt.Errorf("create input directory: %w", err)
	}
	if err := os.MkdirAll(cfg.stateDir, 0o750); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx := parent
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, cfg.duration)
		defer cancel()
	}
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping mysql: %w", err)
	}

	luckmail, err := loadEmailSet(cfg.luckmailFile)
	if err != nil {
		return err
	}
	pool, err := loadActivePool(cfg)
	if err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		excluded, err := loadPendingLedgers(cfg.stateDir)
		if err != nil {
			return err
		}
		if stat, statErr := os.Stat(pool.input); statErr == nil && stat.Size() > 0 {
			log.Printf("reusing_candidate_pool input=%s bytes=%d", pool.input, stat.Size())
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat candidate pool: %w", statErr)
		} else {
			selected, found, selectErr := selectAndCreatePool(ctx, db, cfg, luckmail, excluded)
			if selectErr != nil {
				return selectErr
			}
			if !found {
				log.Printf("pending_validation_complete owner=%d outside_luckmail_only=%t", cfg.ownerUserID, cfg.outsideOnly)
				return nil
			}
			pool = selected
			if err := activatePool(cfg, pool); err != nil {
				return err
			}
		}
		if !cfg.apply {
			log.Printf("dry_run_only input=%s", pool.input)
			return nil
		}
		prefetchCtx, cancelPrefetch := context.WithCancel(ctx)
		prefetch := startPoolPrefetch(prefetchCtx, db, cfg, luckmail, pool)
		err = runValidation(ctx, cfg, pool)
		cancelPrefetch()
		prefetched := <-prefetch
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				log.Printf("duration_reached duration=%s", cfg.duration)
				return nil
			}
			return err
		}
		if prefetched.err != nil && !errors.Is(prefetched.err, context.Canceled) && !errors.Is(prefetched.err, context.DeadlineExceeded) {
			return prefetched.err
		}
		if !cfg.continuous {
			return nil
		}
		done, err := validationCheckpointDone(pool.state)
		if err != nil {
			return err
		}
		if !done {
			return errors.New("validation runner exited without a completed checkpoint")
		}
		if len(prefetched.candidates) > 0 {
			pool, err = createPoolWithCandidates(cfg, prefetched.candidates)
			if err != nil {
				return err
			}
		} else {
			excluded, loadErr := loadPendingLedgers(cfg.stateDir)
			if loadErr != nil {
				return loadErr
			}
			activeEmails, inputErr := loadInputSet(pool.input)
			if inputErr != nil {
				return inputErr
			}
			for email := range activeEmails {
				excluded[email] = struct{}{}
			}
			var found bool
			pool, found, err = selectAndCreatePool(ctx, db, cfg, luckmail, excluded)
			if err != nil {
				return err
			}
			if !found {
				log.Printf("pending_validation_complete owner=%d outside_luckmail_only=%t", cfg.ownerUserID, cfg.outsideOnly)
				return nil
			}
		}
		if err := activatePool(cfg, pool); err != nil {
			return err
		}
	}
}

func legacyPool(cfg config) poolPaths {
	return poolPaths{
		input:    cfg.inputFile,
		state:    filepath.Join(cfg.stateDir, "pending-validation.json"),
		manifest: filepath.Join(cfg.stateDir, "pending-validation.tsv"),
	}
}

func loadActivePool(cfg config) (poolPaths, error) {
	legacy := legacyPool(cfg)
	data, err := os.ReadFile(filepath.Join(cfg.stateDir, activePoolPointerName))
	if errors.Is(err, os.ErrNotExist) {
		return legacy, nil
	}
	if err != nil {
		return poolPaths{}, fmt.Errorf("read active candidate pool: %w", err)
	}
	relative := strings.TrimSpace(string(data))
	if relative == "" || filepath.IsAbs(relative) {
		return poolPaths{}, errors.New("active candidate pool path is invalid")
	}
	root, err := filepath.Abs(cfg.stateDir)
	if err != nil {
		return poolPaths{}, fmt.Errorf("resolve candidate state directory: %w", err)
	}
	dir := filepath.Join(root, relative)
	resolved, err := filepath.Rel(root, dir)
	if err != nil || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return poolPaths{}, errors.New("active candidate pool escapes state directory")
	}
	return poolPaths{
		input:    filepath.Join(dir, "pending-validation-input.txt"),
		state:    filepath.Join(dir, "pending-validation.json"),
		manifest: filepath.Join(dir, "pending-validation.tsv"),
	}, nil
}

func newPool(cfg config) (poolPaths, error) {
	root := filepath.Join(cfg.stateDir, "pools")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return poolPaths{}, fmt.Errorf("create candidate pool root: %w", err)
	}
	dir, err := os.MkdirTemp(root, "pending-")
	if err != nil {
		return poolPaths{}, fmt.Errorf("create candidate pool: %w", err)
	}
	pool := poolPaths{
		input:    filepath.Join(dir, "pending-validation-input.txt"),
		state:    filepath.Join(dir, "pending-validation.json"),
		manifest: filepath.Join(dir, "pending-validation.tsv"),
	}
	return pool, nil
}

func activatePool(cfg config, pool poolPaths) error {
	return writeActivePoolPointer(cfg.stateDir, filepath.Dir(pool.input))
}

func selectAndCreatePool(ctx context.Context, db *sql.DB, cfg config, luckmail, excluded map[string]struct{}) (poolPaths, bool, error) {
	candidates, err := selectCandidates(ctx, db, cfg.ownerUserID, luckmail, excluded, cfg.limit, cfg.outsideOnly)
	if err != nil {
		return poolPaths{}, false, err
	}
	if len(candidates) == 0 {
		return poolPaths{}, false, nil
	}
	pool, err := createPoolWithCandidates(cfg, candidates)
	return pool, err == nil, err
}

func createPoolWithCandidates(cfg config, candidates []candidate) (poolPaths, error) {
	pool, err := newPool(cfg)
	if err != nil {
		return poolPaths{}, err
	}
	if err := writeCandidates(pool.input, candidates); err != nil {
		return poolPaths{}, err
	}
	log.Printf("selected_candidate_pool size=%d owner=%d input=%s", len(candidates), cfg.ownerUserID, pool.input)
	return pool, nil
}

func startPoolPrefetch(ctx context.Context, db *sql.DB, cfg config, luckmail map[string]struct{}, active poolPaths) <-chan poolPrefetchResult {
	result := make(chan poolPrefetchResult, 1)
	go func() {
		defer close(result)
		if !cfg.continuous {
			result <- poolPrefetchResult{}
			return
		}
		if err := waitForPoolLowWater(ctx, active.state, cfg.refillThreshold); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result <- poolPrefetchResult{}
			} else {
				result <- poolPrefetchResult{err: err}
			}
			return
		}
		excluded, err := loadPendingLedgers(cfg.stateDir)
		if err != nil {
			result <- poolPrefetchResult{err: err}
			return
		}
		activeEmails, err := loadInputSet(active.input)
		if err != nil {
			result <- poolPrefetchResult{err: err}
			return
		}
		for email := range activeEmails {
			excluded[email] = struct{}{}
		}
		candidates, err := selectCandidates(ctx, db, cfg.ownerUserID, luckmail, excluded, cfg.limit, cfg.outsideOnly)
		if err != nil && ctx.Err() != nil {
			result <- poolPrefetchResult{}
			return
		}
		result <- poolPrefetchResult{candidates: candidates, err: err}
	}()
	return result
}

func waitForPoolLowWater(ctx context.Context, statePath string, threshold int) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(statePath)
		if err == nil {
			var state struct {
				Phase        string `json:"phase"`
				Total        int    `json:"total"`
				FreezeOffset int    `json:"freezeOffset"`
			}
			if jsonErr := json.Unmarshal(data, &state); jsonErr == nil {
				remaining := state.Total - state.FreezeOffset
				if state.Phase == "done" || (state.Phase == "processing" && remaining <= threshold) {
					return nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read candidate pool checkpoint: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func writeActivePoolPointer(stateDir, dir string) error {
	root, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve pool directory: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("candidate pool is outside state directory")
	}
	path := filepath.Join(stateDir, activePoolPointerName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(relative+"\n"), 0o600); err != nil {
		return fmt.Errorf("write active candidate pool: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit active candidate pool: %w", err)
	}
	return nil
}

func validationCheckpointDone(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read validation checkpoint: %w", err)
	}
	var state struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("decode validation checkpoint: %w", err)
	}
	return state.Phase == "done", nil
}

func runValidation(ctx context.Context, cfg config, pool poolPaths) error {
	args := []string{
		"--apply", "--file", pool.input,
		"--manifest", pool.manifest,
		"--state", pool.state,
		"--error", filepath.Join(cfg.stateDir, "pending-validation-error.txt"),
		"--run-id", runID(cfg.runID),
		"--operator-user-id", strconv.FormatUint(cfg.ownerUserID, 10),
		"--concurrency", strconv.Itoa(cfg.concurrency),
		"--pending-capacity", strconv.Itoa(max(1000, cfg.concurrency)),
		"--credential-type-rps", strconv.Itoa(cfg.credentialTypeRPS),
		"--credential-type-burst", "1",
		"--stage1-startup-interval", cfg.startupInterval.String(),
		"--lock-name", "remail_pending_validation",
		"--pending-no-freeze",
	}
	if cfg.outsideOnly {
		args = append(args, "--pending-soft-fallback")
	}
	log.Printf("starting validation runner tool=%s concurrency=%d input=%s", cfg.tool, cfg.concurrency, pool.input)
	cmd := exec.CommandContext(ctx, cfg.tool, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("validation runner: %w", err)
	}
	return nil
}

func loadInputSet(path string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return set, nil
		}
		return nil, fmt.Errorf("open candidate pool input: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if email, _, ok := strings.Cut(line, "----"); ok {
			line = email
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "" {
			set[line] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read candidate pool input: %w", err)
	}
	return set, nil
}

func loadPendingLedgers(stateDir string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	for _, name := range []string{"success.txt", "recoverable.txt", "429.txt", "pending-validation-error.txt"} {
		path := filepath.Join(stateDir, name)
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open pending validation ledger: %w", err)
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			email := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if email != "" {
				set[email] = struct{}{}
			}
		}
		closeErr := file.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read pending validation ledger: %w", err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close pending validation ledger: %w", closeErr)
		}
	}
	return set, nil
}

func loadEmailSet(path string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	if strings.TrimSpace(path) == "" {
		return set, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open luckmail file: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if email, _, ok := strings.Cut(line, "----"); ok {
			line = email
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "" {
			set[line] = struct{}{}
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read luckmail file: %w", err)
	}
	log.Printf("loaded luckmail set emails=%d file=%s", len(set), path)
	return set, nil
}

func selectCandidates(ctx context.Context, db *sql.DB, owner uint64, luckmail, excluded map[string]struct{}, limit int, outsideOnly bool) ([]candidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT mr.id, LOWER(TRIM(mr.email_address))
FROM microsoft_resources AS mr
JOIN email_resources AS er ON er.id = mr.id AND er.type = 'microsoft'
WHERE er.owner_user_id = ? AND mr.status = 'pending'
ORDER BY mr.id ASC`, owner)
	if err != nil {
		return nil, fmt.Errorf("select pending resources: %w", err)
	}
	defer rows.Close()
	capacity := limit
	if capacity < 1 {
		capacity = 1024
	}
	outside := make([]candidate, 0, capacity)
	inside := make([]candidate, 0, capacity)
	seen := make(map[uint64]struct{}, capacity)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.email); err != nil {
			return nil, fmt.Errorf("scan pending resource: %w", err)
		}
		item.email = strings.ToLower(strings.TrimSpace(item.email))
		if item.id == 0 || item.email == "" {
			continue
		}
		if _, skip := excluded[item.email]; skip {
			continue
		}
		if _, ok := seen[item.id]; ok {
			continue
		}
		seen[item.id] = struct{}{}
		if _, ok := luckmail[item.email]; ok {
			if !outsideOnly && (limit == 0 || len(inside) < limit) {
				inside = append(inside, item)
			}
		} else if limit == 0 || len(outside) < limit {
			outside = append(outside, item)
		}
		if limit > 0 && len(outside) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending resources: %w", err)
	}
	if outsideOnly || (limit > 0 && len(outside) >= limit) {
		return outside, nil
	}
	if limit == 0 {
		return append(outside, inside...), nil
	}
	return append(outside, inside[:min(limit-len(outside), len(inside))]...), nil
}

func writeCandidates(path string, candidates []candidate) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return fmt.Errorf("create selected input: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, item := range candidates {
		if _, err := fmt.Fprintln(w, item.email); err != nil {
			_ = f.Close()
			return fmt.Errorf("write selected input: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush selected input: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close selected input: %w", err)
	}
	return nil
}

func runID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return "pending-" + time.Now().UTC().Format("20060102T150405Z")
}
