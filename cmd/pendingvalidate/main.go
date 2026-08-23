// pendingvalidate selects administrator-owned Microsoft resources that are
// still pending and delegates the actual state machine to the existing
// luckmail runner.  Keeping selection here makes the new cleanup an isolated
// command while leaving luckmail's default input/policy untouched.
package main

import (
	"bufio"
	"context"
	"database/sql"
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
	runID             string
}

type candidate struct {
	id    uint64
	email string
}

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
	flag.IntVar(&cfg.limit, "limit", 100, "maximum newly selected resources")
	flag.IntVar(&cfg.concurrency, "concurrency", 10, "validation worker count")
	flag.DurationVar(&cfg.startupInterval, "startup-interval", 2*time.Second, "worker ramp interval")
	flag.IntVar(&cfg.credentialTypeRPS, "credential-type-rps", 3, "GetCredentialType requests per second")
	flag.DurationVar(&cfg.duration, "duration", 0, "stop after this duration; zero means run to completion")
	flag.BoolVar(&cfg.outsideOnly, "outside-luckmail-only", true, "select only resources absent from luckmail-file")
	flag.StringVar(&cfg.runID, "run-id", "", "runner run identifier")
	flag.Parse()
	return cfg
}

func run(parent context.Context, cfg config) error {
	if cfg.ownerUserID == 0 || cfg.limit < 1 || cfg.limit > 100000 || cfg.concurrency < 1 || cfg.concurrency > 500 || cfg.startupInterval < 0 || cfg.credentialTypeRPS < 0 || cfg.duration < 0 {
		return errors.New("invalid pending validation limits")
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

	if stat, statErr := os.Stat(cfg.inputFile); statErr == nil && stat.Size() > 0 {
		log.Printf("reusing_selected_input path=%s bytes=%d", cfg.inputFile, stat.Size())
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat selected input: %w", statErr)
	} else {
		luckmail, err := loadEmailSet(cfg.luckmailFile)
		if err != nil {
			return err
		}
		candidates, err := selectCandidates(ctx, db, cfg.ownerUserID, luckmail, cfg.limit, cfg.outsideOnly)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			log.Printf("no pending resources matched owner=%d outside_luckmail_only=%t", cfg.ownerUserID, cfg.outsideOnly)
			return nil
		}
		if err := writeCandidates(cfg.inputFile, candidates); err != nil {
			return err
		}
		log.Printf("selected pending=%d owner=%d outside_luckmail_only=%t input=%s", len(candidates), cfg.ownerUserID, cfg.outsideOnly, cfg.inputFile)
	}
	if !cfg.apply {
		log.Printf("dry_run_only input=%s", cfg.inputFile)
		return nil
	}

	args := []string{
		"--apply", "--file", cfg.inputFile,
		"--manifest", filepath.Join(cfg.stateDir, "pending-validation.tsv"),
		"--state", filepath.Join(cfg.stateDir, "pending-validation.json"),
		"--error", filepath.Join(cfg.stateDir, "pending-validation-error.txt"),
		"--run-id", runID(cfg.runID),
		"--operator-user-id", strconv.FormatUint(cfg.ownerUserID, 10),
		"--concurrency", strconv.Itoa(cfg.concurrency),
		"--pending-capacity", strconv.Itoa(max(1000, cfg.concurrency)),
		"--credential-type-rps", strconv.Itoa(cfg.credentialTypeRPS),
		"--credential-type-burst", "1",
		"--stage1-startup-interval", cfg.startupInterval.String(),
		"--lock-name", "remail_pending_validation",
	}
	if cfg.outsideOnly {
		args = append(args, "--pending-soft-fallback")
	}
	log.Printf("starting validation runner tool=%s concurrency=%d", cfg.tool, cfg.concurrency)
	cmd := exec.CommandContext(ctx, cfg.tool, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	// Let the child perform its normal SIGTERM cleanup when a bounded test
	// expires instead of immediately killing it mid-transaction.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			log.Printf("duration_reached duration=%s", cfg.duration)
			return nil
		}
		return fmt.Errorf("validation runner: %w", err)
	}
	return nil
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

func selectCandidates(ctx context.Context, db *sql.DB, owner uint64, luckmail map[string]struct{}, limit int, outsideOnly bool) ([]candidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT mr.id, LOWER(TRIM(mr.email_address))
FROM microsoft_resources AS mr
JOIN email_resources AS er ON er.id = mr.id AND er.type = 'microsoft'
WHERE er.owner_user_id = ? AND mr.status = 'pending'
ORDER BY mr.id ASC`, owner)
	if err != nil {
		return nil, fmt.Errorf("select pending resources: %w", err)
	}
	defer rows.Close()
	outside := make([]candidate, 0, limit)
	inside := make([]candidate, 0, limit)
	seen := make(map[uint64]struct{}, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.email); err != nil {
			return nil, fmt.Errorf("scan pending resource: %w", err)
		}
		item.email = strings.ToLower(strings.TrimSpace(item.email))
		if item.id == 0 || item.email == "" {
			continue
		}
		if _, ok := seen[item.id]; ok {
			continue
		}
		seen[item.id] = struct{}{}
		if _, ok := luckmail[item.email]; ok {
			if !outsideOnly && len(inside) < limit {
				inside = append(inside, item)
			}
		} else if len(outside) < limit {
			outside = append(outside, item)
		}
		if len(outside) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending resources: %w", err)
	}
	if len(outside) >= limit || outsideOnly {
		return outside, nil
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
