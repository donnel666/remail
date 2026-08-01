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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	stageCanarySubmit = "canary_submit"
	stageCanaryWait   = "canary_wait"
	stageBatchSubmit  = "batch_submit"
	stageBatchWait    = "batch_wait"
	stageDone         = "done"
)

type config struct {
	apply           bool
	batchSize       int
	canarySize      int
	chunkSize       int
	operatorID      uint64
	pollInterval    time.Duration
	statePath       string
	runID           string
	manifestPath    string
	restoreAbnormal bool
}

type checkpoint struct {
	Version        int       `json:"version"`
	RunID          string    `json:"runId"`
	ManifestPath   string    `json:"manifestPath"`
	CandidateCount int       `json:"candidateCount"`
	BatchSize      int       `json:"batchSize"`
	CanarySize     int       `json:"canarySize"`
	OperatorID     uint64    `json:"operatorUserId"`
	BatchIndex     int       `json:"batchIndex"`
	Stage          string    `json:"stage"`
	SubmitOffset   int       `json:"submitOffset"`
	Submitted      int       `json:"submitted"`
	Skipped        int       `json:"skipped"`
	ReconcilePass  int       `json:"reconcilePass"`
	AuditedBatch   int       `json:"auditedBatch"`
	TotalSucceeded int       `json:"totalSucceeded"`
	TotalFailed    int       `json:"totalFailed"`
	TotalChanged   int       `json:"totalChanged"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type taskProgress struct {
	Pending    int
	Processing int
	Succeeded  int
	Failed     int
	Inventory  int
}

func (p taskProgress) active() int { return p.Pending + p.Processing }

type reconciliation struct {
	Succeeded int
	Failed    int
	Changed   int
	Active    int
	RetryIDs  []uint64
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
	flag.BoolVar(&cfg.apply, "apply", false, "apply changes; omitted means read-only dry-run")
	flag.IntVar(&cfg.batchSize, "batch-size", 100000, "resources per maintenance batch")
	flag.IntVar(&cfg.canarySize, "canary-size", 10, "first-batch canary resources")
	flag.IntVar(&cfg.chunkSize, "chunk-size", 1000, "resources per database transaction")
	flag.Uint64Var(&cfg.operatorID, "operator-user-id", 0, "active admin or super-admin user ID")
	flag.DurationVar(&cfg.pollInterval, "poll-interval", 30*time.Second, "batch progress polling interval")
	flag.StringVar(&cfg.statePath, "state", "/state/history-rescan.json", "durable checkpoint path")
	flag.StringVar(&cfg.runID, "run-id", "", "safe run identifier; generated when omitted")
	flag.StringVar(&cfg.manifestPath, "manifest", "", "existing newline-delimited resource ID manifest")
	flag.BoolVar(&cfg.restoreAbnormal, "restore-abnormal", false, "allow listed sellable abnormal resources to be rescanned")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config) error {
	if cfg.batchSize < 1 || cfg.batchSize > 100000 || cfg.canarySize < 0 || cfg.canarySize > cfg.batchSize || cfg.chunkSize < 1 || cfg.chunkSize > 5000 || cfg.pollInterval < time.Second {
		return errors.New("invalid batch, canary, chunk, or poll configuration")
	}
	if cfg.restoreAbnormal && strings.TrimSpace(cfg.manifestPath) == "" {
		return errors.New("--restore-abnormal requires --manifest")
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		return errors.New("MYSQL_DSN is required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping mysql: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve mysql connection: %w", err)
	}
	defer conn.Close()

	state, found, err := loadCheckpoint(cfg.statePath)
	if err != nil {
		return err
	}
	var ids []uint64
	if found {
		ids, err = loadManifest(state.ManifestPath)
		if err != nil {
			return err
		}
		if len(ids) != state.CandidateCount {
			return fmt.Errorf("manifest contains %d IDs, checkpoint expects %d", len(ids), state.CandidateCount)
		}
		cfg.batchSize = state.BatchSize
		cfg.canarySize = state.CanarySize
		cfg.operatorID = state.OperatorID
		log.Printf("resuming run=%s stage=%s batch=%d/%d candidates=%d", state.RunID, state.Stage, state.BatchIndex+1, batchCount(len(ids), state.BatchSize), len(ids))
	} else {
		manifestPath := strings.TrimSpace(cfg.manifestPath)
		if manifestPath != "" {
			ids, err = loadManifest(manifestPath)
		} else {
			ids, err = snapshotCandidates(ctx, conn)
			manifestPath = cfg.statePath + ".ids"
		}
		if err != nil {
			return err
		}
		log.Printf("dry-run candidates=%d batches=%d batch_size=%d canary=%d", len(ids), batchCount(len(ids), cfg.batchSize), cfg.batchSize, min(cfg.canarySize, len(ids)))
		if !cfg.apply {
			return nil
		}
		if cfg.operatorID == 0 {
			return errors.New("--operator-user-id is required with --apply")
		}
		if err := validateOperator(ctx, conn, cfg.operatorID); err != nil {
			return err
		}
		runID, err := normalizeRunID(cfg.runID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cfg.manifestPath) == "" {
			if err := saveManifest(manifestPath, ids); err != nil {
				return err
			}
		}
		stage := stageBatchSubmit
		if cfg.canarySize > 0 && len(ids) > 0 {
			stage = stageCanarySubmit
		}
		now := time.Now().UTC()
		state = checkpoint{
			Version: 1, RunID: runID, ManifestPath: manifestPath, CandidateCount: len(ids),
			BatchSize: cfg.batchSize, CanarySize: cfg.canarySize, OperatorID: cfg.operatorID,
			Stage: stage, StartedAt: now, UpdatedAt: now,
		}
		if len(ids) == 0 {
			state.Stage = stageDone
		}
		if err := saveCheckpoint(cfg.statePath, &state); err != nil {
			return err
		}
	}
	if !cfg.apply {
		return errors.New("checkpoint exists; pass --apply to resume it")
	}
	if err := validateOperator(ctx, conn, state.OperatorID); err != nil {
		return err
	}
	locked, err := acquireLock(ctx, conn)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("another history-rescan process owns the database lock")
	}
	defer releaseLock(conn)

	for state.Stage != stageDone {
		if err := ctx.Err(); err != nil {
			return err
		}
		start, end := batchRange(len(ids), state.BatchSize, state.BatchIndex)
		if start >= end {
			state.Stage = stageDone
			break
		}
		batchIDs := ids[start:end]
		tag, err := batchTag(state.RunID, state.BatchIndex)
		if err != nil {
			return err
		}
		if state.AuditedBatch < state.BatchIndex+1 {
			if err := writeBatchAudit(ctx, conn, state.OperatorID, tag, state.BatchIndex+1, len(batchIDs)); err != nil {
				return err
			}
			state.AuditedBatch = state.BatchIndex + 1
			if err := saveCheckpoint(cfg.statePath, &state); err != nil {
				return err
			}
		}

		switch state.Stage {
		case stageCanarySubmit:
			limit := min(state.CanarySize, len(batchIDs))
			if err := submitRange(ctx, conn, cfg, &state, batchIDs, tag, limit, false); err != nil {
				return err
			}
			state.Stage = stageCanaryWait
			state.ReconcilePass = 0
			if err := saveCheckpoint(cfg.statePath, &state); err != nil {
				return err
			}
			log.Printf("canary submitted run=%s resources=%d", tag, state.Submitted)
		case stageCanaryWait:
			limit := min(state.CanarySize, len(batchIDs))
			done, result, err := waitBatch(ctx, conn, cfg, &state, batchIDs[:limit], tag, true)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			if result.Failed > 0 || result.Changed > 0 || result.Succeeded != limit {
				return fmt.Errorf("canary failed: succeeded=%d failed=%d changed=%d expected=%d", result.Succeeded, result.Failed, result.Changed, limit)
			}
			state.Stage = stageBatchSubmit
			state.SubmitOffset = limit
			state.ReconcilePass = 0
			if err := saveCheckpoint(cfg.statePath, &state); err != nil {
				return err
			}
			log.Printf("canary passed run=%s resources=%d", tag, result.Succeeded)
		case stageBatchSubmit:
			if err := submitRange(ctx, conn, cfg, &state, batchIDs, tag, len(batchIDs), false); err != nil {
				return err
			}
			state.Stage = stageBatchWait
			state.ReconcilePass = 0
			if err := saveCheckpoint(cfg.statePath, &state); err != nil {
				return err
			}
			log.Printf("batch submitted run=%s batch=%d/%d accounted=%d skipped=%d", tag, state.BatchIndex+1, batchCount(len(ids), state.BatchSize), state.Submitted, state.Skipped)
		case stageBatchWait:
			done, result, err := waitBatch(ctx, conn, cfg, &state, batchIDs, tag, false)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			state.TotalSucceeded += result.Succeeded
			state.TotalFailed += result.Failed
			state.TotalChanged += result.Changed
			log.Printf("batch complete run=%s batch=%d/%d succeeded=%d failed=%d changed=%d", tag, state.BatchIndex+1, batchCount(len(ids), state.BatchSize), result.Succeeded, result.Failed, result.Changed)
			state.BatchIndex++
			state.SubmitOffset = 0
			state.Submitted = 0
			state.Skipped = 0
			state.ReconcilePass = 0
			if state.BatchIndex >= batchCount(len(ids), state.BatchSize) {
				state.Stage = stageDone
			} else {
				state.Stage = stageBatchSubmit
			}
			if err := saveCheckpoint(cfg.statePath, &state); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported checkpoint stage %q", state.Stage)
		}
	}
	state.UpdatedAt = time.Now().UTC()
	if err := saveCheckpoint(cfg.statePath, &state); err != nil {
		return err
	}
	log.Printf("run complete run=%s candidates=%d succeeded=%d failed=%d changed=%d", state.RunID, state.CandidateCount, state.TotalSucceeded, state.TotalFailed, state.TotalChanged)
	return nil
}

func submitRange(ctx context.Context, conn *sql.Conn, cfg config, state *checkpoint, ids []uint64, tag string, limit int, allowIdentifying bool) error {
	for state.SubmitOffset < limit {
		end := min(state.SubmitOffset+cfg.chunkSize, limit)
		accounted, submitted, err := submitChunk(ctx, conn, ids[state.SubmitOffset:end], tag, state.OperatorID, allowIdentifying, cfg.restoreAbnormal)
		if err != nil {
			return err
		}
		state.Submitted += accounted
		state.Skipped += end - state.SubmitOffset - accounted
		state.SubmitOffset = end
		if err := saveCheckpoint(cfg.statePath, state); err != nil {
			return err
		}
		log.Printf("submission progress run=%s offset=%d/%d submitted_now=%d accounted_total=%d skipped_total=%d", tag, state.SubmitOffset, limit, submitted, state.Submitted, state.Skipped)
	}
	return nil
}

func waitBatch(ctx context.Context, conn *sql.Conn, cfg config, state *checkpoint, ids []uint64, tag string, canary bool) (bool, reconciliation, error) {
	progress, err := readTaskProgress(ctx, conn, tag)
	if err != nil {
		return false, reconciliation{}, err
	}
	log.Printf("scan progress run=%s pending=%d processing=%d succeeded=%d failed=%d normal_for_sale_inventory=%d", tag, progress.Pending, progress.Processing, progress.Succeeded, progress.Failed, progress.Inventory)
	if progress.active() > 0 {
		return false, reconciliation{}, sleepContext(ctx, cfg.pollInterval)
	}
	result, err := reconcileBatch(ctx, conn, ids, tag, cfg.chunkSize)
	if err != nil {
		return false, reconciliation{}, err
	}
	if result.Active > 0 {
		return false, reconciliation{}, sleepContext(ctx, cfg.pollInterval)
	}
	if len(result.RetryIDs) > 0 && state.ReconcilePass < 2 {
		state.ReconcilePass++
		log.Printf("reconciling missing history tasks run=%s pass=%d resources=%d", tag, state.ReconcilePass, len(result.RetryIDs))
		for start := 0; start < len(result.RetryIDs); start += cfg.chunkSize {
			end := min(start+cfg.chunkSize, len(result.RetryIDs))
			if _, _, err := submitChunk(ctx, conn, result.RetryIDs[start:end], tag, state.OperatorID, true, cfg.restoreAbnormal); err != nil {
				return false, reconciliation{}, err
			}
		}
		if err := saveCheckpoint(cfg.statePath, state); err != nil {
			return false, reconciliation{}, err
		}
		return false, reconciliation{}, sleepContext(ctx, cfg.pollInterval)
	}
	if len(result.RetryIDs) > 0 {
		result.Failed += len(result.RetryIDs)
		result.RetryIDs = nil
	}
	if canary && result.Failed == 0 && result.Changed == 0 && result.Succeeded != len(ids) {
		result.Failed += len(ids) - result.Succeeded
	}
	return true, result, nil
}

func submitChunk(ctx context.Context, conn *sql.Conn, ids []uint64, tag string, operatorID uint64, allowIdentifying bool, restoreAbnormal bool) (accounted int, submitted int, resultErr error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, 0, fmt.Errorf("begin submission transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback()
		}
	}()
	placeholders := sqlPlaceholders(len(ids))
	idArgs := uint64Args(ids)
	rows, err := tx.QueryContext(ctx, "SELECT id FROM email_resources WHERE id IN ("+placeholders+") ORDER BY id FOR UPDATE", idArgs...)
	if err != nil {
		return 0, 0, fmt.Errorf("lock resource roots: %w", err)
	}
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan locked resource root: %w", err)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("close locked resource roots: %w", err)
	}

	if !allowIdentifying {
		existingArgs := append(uint64Args(ids), tag)
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM mailmatch_resource_fetch_states WHERE email_resource_id IN ("+placeholders+") AND operation_kind = 'resource_history' AND request_id = ?", existingArgs...).Scan(&accounted); err != nil {
			return 0, 0, fmt.Errorf("count existing history tasks: %w", err)
		}
	}
	statusPredicate := "m.status = 'normal' AND m.for_sale = 1"
	if allowIdentifying {
		statusPredicate = "((m.status = 'normal' AND m.for_sale = 1) OR m.status = 'identifying')"
	}
	if restoreAbnormal {
		statusPredicate = "((m.status IN ('normal', 'abnormal') AND m.for_sale = 1) OR m.status = 'identifying')"
	}
	eligibleArgs := uint64Args(ids)
	currentTaskPredicate := ""
	if !allowIdentifying {
		currentTaskPredicate = " AND (f.email_resource_id IS NULL OR f.operation_kind <> 'resource_history' OR f.request_id <> ?)"
		eligibleArgs = append(eligibleArgs, tag)
	}
	query := "SELECT m.id FROM microsoft_resources m LEFT JOIN mailmatch_resource_fetch_states f ON f.email_resource_id = m.id WHERE m.id IN (" + placeholders + ") AND " + statusPredicate + " AND TRIM(m.client_id) <> '' AND TRIM(m.refresh_token) <> ''" + currentTaskPredicate + " AND (f.email_resource_id IS NULL OR f.status NOT IN ('pending', 'processing')) ORDER BY m.id FOR UPDATE"
	eligibleRows, err := tx.QueryContext(ctx, query, eligibleArgs...)
	if err != nil {
		return 0, 0, fmt.Errorf("lock eligible resources: %w", err)
	}
	eligible := make([]uint64, 0, len(ids))
	for eligibleRows.Next() {
		var id uint64
		if err := eligibleRows.Scan(&id); err != nil {
			eligibleRows.Close()
			return 0, 0, fmt.Errorf("scan eligible resource: %w", err)
		}
		eligible = append(eligible, id)
	}
	if err := eligibleRows.Close(); err != nil {
		return 0, 0, fmt.Errorf("close eligible resources: %w", err)
	}
	if len(eligible) > 0 {
		eligiblePlaceholders := sqlPlaceholders(len(eligible))
		eligibleArgs := uint64Args(eligible)
		versionStatuses := "m.status = 'normal'"
		if restoreAbnormal {
			versionStatuses = "m.status IN ('normal', 'abnormal')"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE email_resources er JOIN microsoft_resources m ON m.id = er.id SET er.version = er.version + 1, er.updated_at = UTC_TIMESTAMP(3) WHERE er.id IN ("+eligiblePlaceholders+") AND "+versionStatuses, eligibleArgs...); err != nil {
			return 0, 0, fmt.Errorf("advance resource versions: %w", err)
		}
		statusUpdate := "status = 'normal' AND for_sale = 1"
		resourceUpdates := "status = 'identifying', updated_at = UTC_TIMESTAMP(3)"
		if restoreAbnormal {
			statusUpdate = "status IN ('normal', 'abnormal') AND for_sale = 1"
			resourceUpdates = "status = 'identifying', validation_failures = 0, last_safe_error = '', updated_at = UTC_TIMESTAMP(3)"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE microsoft_resources SET "+resourceUpdates+" WHERE id IN ("+eligiblePlaceholders+") AND "+statusUpdate, eligibleArgs...); err != nil {
			return 0, 0, fmt.Errorf("mark resources identifying: %w", err)
		}
		insertArgs := make([]any, 0, 3+len(eligible))
		insertArgs = append(insertArgs, operatorID, tag, tag)
		insertArgs = append(insertArgs, uint64Args(eligible)...)
		insertSQL := "INSERT INTO mailmatch_resource_fetch_states (email_resource_id, status, generation, failures, operation_kind, order_no, purpose, operator_user_id, expected_credential_revision, since_at, until_at, fetched_count, stored_count, matched_count, request_id, path, idempotency_key, requested_at, started_at, finished_at, cooldown_until, last_safe_error) SELECT m.id, 'pending', 1, 0, 'resource_history', '', 'order_fetch', ?, m.credential_revision, NULL, NULL, 0, 0, 0, ?, '/ops/history-rescan', CONCAT(?, '-', m.id), UTC_TIMESTAMP(3), NULL, NULL, NULL, '' FROM microsoft_resources m WHERE m.id IN (" + eligiblePlaceholders + ") ON DUPLICATE KEY UPDATE status = 'pending', generation = mailmatch_resource_fetch_states.generation + 1, failures = 0, operation_kind = 'resource_history', order_no = '', purpose = 'order_fetch', operator_user_id = VALUES(operator_user_id), expected_credential_revision = VALUES(expected_credential_revision), since_at = NULL, until_at = NULL, fetched_count = 0, stored_count = 0, matched_count = 0, request_id = VALUES(request_id), path = VALUES(path), idempotency_key = VALUES(idempotency_key), requested_at = VALUES(requested_at), started_at = NULL, finished_at = NULL, cooldown_until = NULL, last_safe_error = ''"
		if _, err := tx.ExecContext(ctx, insertSQL, insertArgs...); err != nil {
			return 0, 0, fmt.Errorf("queue resource history states: %w", err)
		}
		submitted = len(eligible)
		accounted += submitted
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit submission transaction: %w", err)
	}
	return accounted, submitted, nil
}

func readTaskProgress(ctx context.Context, conn *sql.Conn, tag string) (taskProgress, error) {
	var result taskProgress
	rows, err := conn.QueryContext(ctx, "SELECT status, COUNT(*) FROM mailmatch_resource_fetch_states WHERE operation_kind = 'resource_history' AND request_id = ? GROUP BY status", tag)
	if err != nil {
		return result, fmt.Errorf("read history task progress: %w", err)
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return result, fmt.Errorf("scan history task progress: %w", err)
		}
		switch status {
		case "pending":
			result.Pending = count
		case "processing":
			result.Processing = count
		case "normal":
			result.Succeeded = count
		case "abnormal":
			result.Failed = count
		}
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close history task progress: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM microsoft_resources WHERE status = 'normal' AND for_sale = 1").Scan(&result.Inventory); err != nil {
		return result, fmt.Errorf("read available inventory: %w", err)
	}
	return result, nil
}

func reconcileBatch(ctx context.Context, conn *sql.Conn, ids []uint64, tag string, chunkSize int) (reconciliation, error) {
	var result reconciliation
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		placeholders := sqlPlaceholders(len(chunk))
		rows, err := conn.QueryContext(ctx, "SELECT m.id, m.status, m.for_sale, (TRIM(m.client_id) <> '' AND TRIM(m.refresh_token) <> '') AS credentials_ready, COALESCE(f.operation_kind, ''), COALESCE(f.status, ''), COALESCE(f.request_id, '') FROM microsoft_resources m LEFT JOIN mailmatch_resource_fetch_states f ON f.email_resource_id = m.id WHERE m.id IN ("+placeholders+")", uint64Args(chunk)...)
		if err != nil {
			return result, fmt.Errorf("reconcile history batch: %w", err)
		}
		seen := 0
		for rows.Next() {
			seen++
			var id uint64
			var resourceStatus, operationKind, taskStatus, requestID string
			var forSale, credentialsReady bool
			if err := rows.Scan(&id, &resourceStatus, &forSale, &credentialsReady, &operationKind, &taskStatus, &requestID); err != nil {
				rows.Close()
				return result, fmt.Errorf("scan reconciled resource: %w", err)
			}
			if operationKind == "resource_history" && requestID == tag {
				switch taskStatus {
				case "pending", "processing":
					result.Active++
				case "normal":
					if resourceStatus == "normal" {
						result.Succeeded++
					} else {
						result.Changed++
					}
				case "abnormal":
					if resourceStatus == "identifying" && credentialsReady {
						result.RetryIDs = append(result.RetryIDs, id)
					} else {
						result.Failed++
					}
				default:
					result.RetryIDs = append(result.RetryIDs, id)
				}
				continue
			}
			if credentialsReady && ((resourceStatus == "normal" && forSale) || resourceStatus == "identifying") {
				result.RetryIDs = append(result.RetryIDs, id)
			} else {
				result.Changed++
			}
		}
		if err := rows.Close(); err != nil {
			return result, fmt.Errorf("close reconciled resources: %w", err)
		}
		if seen != len(chunk) {
			result.Changed += len(chunk) - seen
		}
	}
	return result, nil
}

func snapshotCandidates(ctx context.Context, conn *sql.Conn) ([]uint64, error) {
	rows, err := conn.QueryContext(ctx, "SELECT id FROM microsoft_resources WHERE status = 'normal' AND for_sale = 1 ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("snapshot normal public resources: %w", err)
	}
	defer rows.Close()
	ids := make([]uint64, 0, 400000)
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan resource snapshot: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource snapshot: %w", err)
	}
	return ids, nil
}

func validateOperator(ctx context.Context, conn *sql.Conn, operatorID uint64) error {
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = ? AND status = 'active' AND role IN ('admin', 'super_admin')", operatorID).Scan(&count); err != nil {
		return fmt.Errorf("validate operator: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("operator user %d is not an active administrator", operatorID)
	}
	return nil
}

func writeBatchAudit(ctx context.Context, conn *sql.Conn, operatorID uint64, tag string, batch, resources int) error {
	_, err := conn.ExecContext(ctx, "INSERT INTO operation_logs (operator_user_id, operation_type, resource_type, resource_id, path, result, safe_summary, request_id, created_at) SELECT ?, 'mailmatch.ops_history_rescan_batch', 'microsoft_resource', ?, '/ops/history-rescan', 'success', ?, ?, UTC_TIMESTAMP() WHERE NOT EXISTS (SELECT 1 FROM operation_logs WHERE operation_type = 'mailmatch.ops_history_rescan_batch' AND request_id = ?)", operatorID, fmt.Sprintf("batch:%d", batch), fmt.Sprintf("Full-history rescan batch accepted; resources=%d.", resources), tag, tag)
	if err != nil {
		return fmt.Errorf("write batch audit: %w", err)
	}
	return nil
}

func acquireLock(ctx context.Context, conn *sql.Conn) (bool, error) {
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('remail_history_rescan', 0)").Scan(&locked); err != nil {
		return false, fmt.Errorf("acquire history rescan lock: %w", err)
	}
	return locked.Valid && locked.Int64 == 1, nil
}

func releaseLock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK('remail_history_rescan')")
}

func saveCheckpoint(path string, state *checkpoint) error {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace checkpoint: %w", err)
	}
	return nil
}

func loadCheckpoint(path string) (checkpoint, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint{}, false, nil
	}
	if err != nil {
		return checkpoint{}, false, fmt.Errorf("read checkpoint: %w", err)
	}
	var state checkpoint
	if err := json.Unmarshal(data, &state); err != nil {
		return checkpoint{}, false, fmt.Errorf("decode checkpoint: %w", err)
	}
	if state.Version != 1 || state.RunID == "" || state.ManifestPath == "" || state.BatchSize < 1 || state.OperatorID == 0 {
		return checkpoint{}, false, errors.New("invalid checkpoint")
	}
	return state, true, nil
}

func saveManifest(path string, ids []uint64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create manifest: %w", err)
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	for _, id := range ids {
		if _, err := writer.WriteString(strconv.FormatUint(id, 10) + "\n"); err != nil {
			file.Close()
			return fmt.Errorf("write manifest: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return fmt.Errorf("flush manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}

func loadManifest(path string) ([]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()
	ids := make([]uint64, 0, 400000)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		id, err := strconv.ParseUint(strings.TrimSpace(scanner.Text()), 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("invalid manifest resource ID %q", scanner.Text())
		}
		ids = append(ids, id)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return ids, nil
}

func normalizeRunID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "histfix-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if len(value) > 48 {
		return "", errors.New("run ID exceeds 48 characters")
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return "", errors.New("run ID may contain only letters, digits, hyphen, and underscore")
		}
	}
	return value, nil
}

func batchTag(runID string, index int) (string, error) {
	tag := fmt.Sprintf("%s-b%02d", runID, index+1)
	if len(tag) > 64 {
		return "", errors.New("batch request ID exceeds 64 characters")
	}
	return tag, nil
}

func batchCount(total, size int) int {
	if total == 0 || size <= 0 {
		return 0
	}
	return (total + size - 1) / size
}

func batchRange(total, size, index int) (int, int) {
	if total <= 0 || size <= 0 || index < 0 {
		return 0, 0
	}
	start := min(index*size, total)
	return start, min(start+size, total)
}

func sqlPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func uint64Args(ids []uint64) []any {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return args
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
