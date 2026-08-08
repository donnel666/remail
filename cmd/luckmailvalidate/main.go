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
	"math/rand/v2"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	allocapi "github.com/donnel666/remail/internal/alloc/api"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailapi "github.com/donnel666/remail/internal/mailtransport/api"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapi "github.com/donnel666/remail/internal/trade/api"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

const (
	phaseFreeze     = "freeze"
	phaseProcessing = "processing"
	phaseDone       = "done"

	validationProxyCooldown = time.Minute

	requestPath = "/ops/luckmail-validation"
)

type config struct {
	apply                   bool
	filePath                string
	fallbackCredentialsPath string
	manifestPath            string
	statePath               string
	errorPath               string
	runID                   string
	operatorID              uint64
	concurrency             int
	pendingCap              int
	chunkSize               int
	offset                  int
	limit                   int
	stage1Retries           int
	stage2Retries           int
	stage1Timeout           time.Duration
	stage2Timeout           time.Duration
	retryAllErrors          bool
}

type checkpoint struct {
	Version      int       `json:"version"`
	RunID        string    `json:"runId"`
	Phase        string    `json:"phase"`
	ManifestPath string    `json:"manifestPath"`
	ErrorPath    string    `json:"errorPath"`
	Total        int       `json:"total"`
	Eligible     int       `json:"eligible"`
	FreezeOffset int       `json:"freezeOffset"`
	Concurrency  int       `json:"concurrency"`
	PendingCap   int       `json:"pendingCapacity"`
	OperatorID   uint64    `json:"operatorUserId"`
	StartedAt    time.Time `json:"startedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type manifestRecord struct {
	Email              string
	ResourceID         uint
	OwnerUserID        uint
	OriginalForSale    bool
	ValidationGen      uint64
	CredentialRevision uint64
	Eligible           bool
}

type fallbackOAuthCredential struct {
	ClientID     string
	RefreshToken string
}

type databaseState struct {
	Status               coredomain.MicrosoftResourceStatus
	ForSale              bool
	ValidationGeneration uint64
	CredentialRevision   uint64
}

type commandRuntime struct {
	platform          *platform.Platform
	cleanup           func()
	resources         *coreinfra.ResourceRepo
	validation        *coreapp.ResourceValidationUseCase
	history           *mailmatchapp.ProjectHistoryScanUseCase
	validationProxies *validationProxyLeasePool
	fallbackOAuth     map[string]fallbackOAuthCredential
}

type validationProxyRoute struct {
	ID            uint   `gorm:"column:id"`
	ProxyServerID uint   `gorm:"column:proxy_server_id"`
	URL           string `gorm:"column:url"`
	OutboundIP    string `gorm:"column:outbound_ip"`
	Country       string `gorm:"column:country"`
	LatencyMs     int    `gorm:"column:latency_ms"`
}

type validationProxyLeasePool struct {
	upstream      *proxyapp.ProxyUseCase
	mu            sync.Mutex
	available     []proxyapp.ProxyConfig
	leased        map[string]proxyapp.ProxyConfig
	cooldownUntil map[uint]time.Time
	wake          chan struct{}
	used          map[uint]struct{}
	capacity      int
	rotations     int
	peak          int
}

type tracker struct {
	mu           sync.Mutex
	failed       map[string]struct{}
	rateLimited  map[string]struct{}
	succeeded    map[string]struct{}
	seen         map[string]struct{}
	success      atomic.Int64
	failure      atomic.Int64
	stage1       atomic.Int64
	stage2       atomic.Int64
	errorOut     string
	rateLimitOut string
	successOut   string
}

func loadValidationProxyLeasePool(ctx context.Context, db *gorm.DB, upstream *proxyapp.ProxyUseCase) (*validationProxyLeasePool, error) {
	if db == nil || upstream == nil {
		return nil, errors.New("validation IPv4 proxy pool is unavailable")
	}
	var rows []validationProxyRoute
	if err := db.WithContext(ctx).
		Table("proxies AS p").
		Select("p.id, p.proxy_server_id, p.url, p.outbound_ip, p.country, p.latency_ms").
		Joins("JOIN proxy_servers AS s ON s.id = p.proxy_server_id").
		Where("p.pool = ? AND p.ip_version = ? AND p.status = ?", string(proxydomain.ProxyPoolResource), string(proxydomain.ProxyIPv4), string(proxydomain.ProxyStatusNormal)).
		Where("p.expire_at IS NULL OR p.expire_at > UTC_TIMESTAMP(3)").
		Where("s.admin_status = ? AND s.health_status = ?", string(proxydomain.ProxyServerAdminOnline), string(proxydomain.ProxyServerHealthy)).
		Order("p.errors ASC, p.latency_sort_ms ASC, p.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load validation IPv4 proxy pool: %w", err)
	}
	routes := make([]proxyapp.ProxyConfig, 0, len(rows))
	seenOutboundIPs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		outboundIP := strings.TrimSpace(row.OutboundIP)
		if row.ID == 0 || row.ProxyServerID == 0 || strings.TrimSpace(row.URL) == "" || outboundIP == "" {
			continue
		}
		if _, duplicate := seenOutboundIPs[outboundIP]; duplicate {
			continue
		}
		seenOutboundIPs[outboundIP] = struct{}{}
		routes = append(routes, proxyapp.ProxyConfig{
			ID: row.ID, ProxyServerID: row.ProxyServerID, Pool: proxydomain.ProxyPoolResource,
			URL: row.URL, IPVersion: proxydomain.ProxyIPv4, Country: row.Country, LatencyMs: row.LatencyMs,
		})
	}
	if len(routes) == 0 {
		return nil, errors.New("no eligible unique-outbound IPv4 resource proxies are available")
	}
	rand.Shuffle(len(routes), func(i, j int) { routes[i], routes[j] = routes[j], routes[i] })
	return newValidationProxyLeasePool(upstream, routes), nil
}

func newValidationProxyLeasePool(upstream *proxyapp.ProxyUseCase, routes []proxyapp.ProxyConfig) *validationProxyLeasePool {
	available := append([]proxyapp.ProxyConfig(nil), routes...)
	return &validationProxyLeasePool{
		upstream: upstream, available: available,
		leased:        make(map[string]proxyapp.ProxyConfig, len(available)),
		cooldownUntil: make(map[uint]time.Time, len(available)),
		wake:          make(chan struct{}, 1),
		used:          make(map[uint]struct{}, len(available)), capacity: len(available),
	}
}

func (p *validationProxyLeasePool) Acquire(ctx context.Context, req proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := strings.ToLower(strings.TrimSpace(req.Key))
	if p == nil || key == "" || req.IPVersion != proxydomain.ProxyIPv4 {
		if p == nil || p.upstream == nil {
			return nil, errors.New("validation proxy provider is unavailable")
		}
		return p.upstream.Acquire(ctx, req)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		now := time.Now()
		if current, ok := p.leased[key]; ok {
			_, cooling := p.cooldownUntil[current.ID]
			if until, ok := p.cooldownUntil[current.ID]; ok && !now.Before(until) {
				delete(p.cooldownUntil, current.ID)
				cooling = false
			}
			if !proxyServerIsAvoided(current.ProxyServerID, req.AvoidProxyServerIDs) && !cooling {
				leased := current
				p.mu.Unlock()
				return &leased, nil
			}
			delete(p.leased, key)
			p.available = append(p.available, current)
			p.rotations++
		}

		availableCount := len(p.available)
		skippedAvoided := 0
		var earliestCooldown time.Time
		for remaining := availableCount; remaining > 0; remaining-- {
			candidate := p.available[0]
			p.available = p.available[1:]
			if until, ok := p.cooldownUntil[candidate.ID]; ok {
				if now.Before(until) {
					p.available = append(p.available, candidate)
					if earliestCooldown.IsZero() || until.Before(earliestCooldown) {
						earliestCooldown = until
					}
					continue
				}
				delete(p.cooldownUntil, candidate.ID)
			}
			if proxyServerIsAvoided(candidate.ProxyServerID, req.AvoidProxyServerIDs) {
				p.available = append(p.available, candidate)
				skippedAvoided++
				continue
			}
			p.leased[key] = candidate
			p.used[candidate.ID] = struct{}{}
			p.peak = max(p.peak, len(p.leased))
			leased := candidate
			p.mu.Unlock()
			return &leased, nil
		}
		leasedCount := len(p.leased)
		p.mu.Unlock()

		if availableCount > 0 && skippedAvoided == availableCount && earliestCooldown.IsZero() {
			return nil, errors.New("no unleased validation IPv4 proxy is available")
		}
		if availableCount == 0 && leasedCount == 0 {
			return nil, errors.New("no validation IPv4 proxy is available")
		}
		if earliestCooldown.IsZero() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-p.wake:
				continue
			}
		}
		wait := time.Until(earliestCooldown)
		if wait <= 0 {
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-p.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (p *validationProxyLeasePool) ReportSuccess(ctx context.Context, proxyID uint) error {
	if p == nil {
		return nil
	}
	if proxyID != 0 {
		p.mu.Lock()
		delete(p.cooldownUntil, proxyID)
		p.mu.Unlock()
		p.notify()
	}
	if p.upstream == nil {
		return nil
	}
	return p.upstream.ReportSuccess(ctx, proxyID)
}

func (p *validationProxyLeasePool) ReportFailure(ctx context.Context, proxyID uint, safeError string) error {
	if p == nil {
		return nil
	}
	if proxyID != 0 && isRateLimitedSafeMessage(safeError) {
		until := time.Now().Add(validationProxyCooldown)
		p.mu.Lock()
		if previous, ok := p.cooldownUntil[proxyID]; !ok || until.After(previous) {
			p.cooldownUntil[proxyID] = until
		}
		p.mu.Unlock()
		p.notify()
	}
	if p.upstream == nil {
		return nil
	}
	return p.upstream.ReportFailure(ctx, proxyID, safeError)
}

func (p *validationProxyLeasePool) Release(key string) {
	if p == nil {
		return
	}
	key = strings.ToLower(strings.TrimSpace(key))
	p.mu.Lock()
	if current, ok := p.leased[key]; ok {
		delete(p.leased, key)
		p.available = append(p.available, current)
	}
	p.mu.Unlock()
	p.notify()
}

func (p *validationProxyLeasePool) notify() {
	if p == nil || p.wake == nil {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *validationProxyLeasePool) Capacity() int {
	if p == nil {
		return 0
	}
	return p.capacity
}

func (p *validationProxyLeasePool) Stats() (used, rotations, peak int) {
	if p == nil {
		return 0, 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.used), p.rotations, p.peak
}

func proxyServerIsAvoided(serverID uint, avoided []uint) bool {
	for _, id := range avoided {
		if id == serverID {
			return true
		}
	}
	return false
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
	flag.BoolVar(&cfg.apply, "apply", false, "freeze and process the selected resources")
	flag.StringVar(&cfg.filePath, "file", "/state/luckmail_ok.txt", "newline-delimited email input")
	flag.StringVar(&cfg.fallbackCredentialsPath, "fallback-credentials", "", "import TXT used only when the database refresh token is missing or expired")
	flag.StringVar(&cfg.manifestPath, "manifest", "/state/luckmail-validation.tsv", "durable resource manifest")
	flag.StringVar(&cfg.statePath, "state", "/state/luckmail-validation.json", "durable checkpoint")
	flag.StringVar(&cfg.errorPath, "error", "/state/error.txt", "failed email output")
	flag.StringVar(&cfg.runID, "run-id", "", "safe run identifier")
	flag.Uint64Var(&cfg.operatorID, "operator-user-id", 1, "active admin or super-admin user ID")
	flag.IntVar(&cfg.concurrency, "concurrency", 150, "workers in each independent stage")
	flag.IntVar(&cfg.pendingCap, "pending-capacity", 1000, "maximum frozen or active stage-one resources")
	flag.IntVar(&cfg.chunkSize, "chunk-size", 1000, "database batch size")
	flag.IntVar(&cfg.offset, "offset", 0, "skip this many input emails")
	flag.IntVar(&cfg.limit, "limit", 0, "process at most this many emails; zero means all remaining")
	flag.IntVar(&cfg.stage1Retries, "stage1-retries", 3, "hard-reauthorization attempts per resource")
	flag.IntVar(&cfg.stage2Retries, "stage2-retries", 3, "history-identification attempts per resource")
	flag.DurationVar(&cfg.stage1Timeout, "stage1-timeout", 15*time.Minute, "timeout for one hard reauthorization attempt")
	flag.DurationVar(&cfg.stage2Timeout, "stage2-timeout", 30*time.Minute, "timeout for one history-identification attempt")
	flag.BoolVar(&cfg.retryAllErrors, "retry-all-errors", false, "on checkpoint resume, retry all error.txt entries instead of only 429.txt")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config) error {
	if cfg.concurrency < 1 || cfg.concurrency > 500 || cfg.pendingCap < cfg.concurrency || cfg.pendingCap > 10000 || cfg.chunkSize < 1 || cfg.chunkSize > 5000 || cfg.offset < 0 || cfg.limit < 0 || cfg.stage1Retries < 1 || cfg.stage1Retries > 5 || cfg.stage2Retries < 1 || cfg.stage2Retries > 5 || cfg.stage1Timeout < time.Minute || cfg.stage2Timeout < time.Minute {
		return errors.New("invalid command limits")
	}
	runtime, err := openRuntime(ctx)
	if err != nil {
		return err
	}
	defer runtime.cleanup()
	if err := requirePausedQueues(runtime.platform); err != nil {
		return err
	}
	conn, err := runtime.platform.SQLDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve MySQL connection: %w", err)
	}
	defer conn.Close()
	locked, err := acquireLock(ctx, conn)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("another luckmail validation command owns the database lock")
	}
	defer releaseLock(conn)

	state, found, err := loadCheckpoint(cfg.statePath)
	if err != nil {
		return err
	}
	var manifest []manifestRecord
	var previousSuccess map[string]struct{}
	var resumeSkippedErrors map[string]struct{}
	if !found {
		previousSuccess, err = loadEmailSet(filepath.Join(filepath.Dir(cfg.errorPath), "success.txt"))
		if err != nil {
			return err
		}
		emails, err := loadEmails(cfg.filePath, cfg.offset, cfg.limit)
		if err != nil {
			return err
		}
		emails, skipped := excludeEmails(emails, previousSuccess)
		if skipped > 0 {
			log.Printf("input_success_skip skipped=%d remaining=%d", skipped, len(emails))
		}
		if len(emails) == 0 {
			log.Printf("input already completed by success.txt")
			return nil
		}
		manifest, err = snapshotManifest(ctx, conn, emails, cfg.chunkSize)
		if err != nil {
			return err
		}
		if err := saveManifest(cfg.manifestPath, manifest); err != nil {
			return err
		}
		eligible := 0
		for _, item := range manifest {
			if item.Eligible {
				eligible++
			}
		}
		log.Printf("snapshot total=%d matched=%d missing=%d stage1_workers=%d stage2_workers=%d pending_capacity=%d", len(manifest), eligible, len(manifest)-eligible, cfg.concurrency, cfg.concurrency, cfg.pendingCap)
		if !cfg.apply {
			return nil
		}
		if err := validateOperator(ctx, conn, cfg.operatorID); err != nil {
			return err
		}
		runID, err := normalizeRunID(cfg.runID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		state = checkpoint{
			Version: 1, RunID: runID, Phase: phaseFreeze,
			ManifestPath: cfg.manifestPath, ErrorPath: cfg.errorPath,
			Total: len(manifest), Eligible: eligible, Concurrency: cfg.concurrency,
			PendingCap: cfg.pendingCap, OperatorID: cfg.operatorID, StartedAt: now, UpdatedAt: now,
		}
		if err := saveCheckpoint(cfg.statePath, &state); err != nil {
			return err
		}
	} else {
		if !cfg.apply {
			return errors.New("checkpoint exists; pass --apply to resume")
		}
		manifest, err = loadManifest(state.ManifestPath)
		if err != nil {
			return err
		}
		if len(manifest) != state.Total {
			return fmt.Errorf("manifest contains %d records, checkpoint expects %d", len(manifest), state.Total)
		}
		cfg.concurrency = state.Concurrency
		if state.PendingCap > 0 {
			cfg.pendingCap = state.PendingCap
		}
		cfg.operatorID = state.OperatorID
		cfg.errorPath = state.ErrorPath
		previousSuccess, err = loadEmailSet(filepath.Join(filepath.Dir(cfg.errorPath), "success.txt"))
		if err != nil {
			return err
		}
		previousErrors, err := loadEmailSet(cfg.errorPath)
		if err != nil {
			return err
		}
		previousRateLimited, err := loadEmailSet(filepath.Join(filepath.Dir(cfg.errorPath), "429.txt"))
		if err != nil {
			return err
		}
		resumeSkippedErrors = resumeErrorSkipSet(previousErrors, previousRateLimited, cfg.retryAllErrors)
		retryableErrors := len(previousErrors) - len(resumeSkippedErrors)
		mode := "429-only"
		if cfg.retryAllErrors {
			mode = "all-errors"
		}
		log.Printf("resume_failure_policy mode=%s skipped_error=%d retry_error=%d", mode, len(resumeSkippedErrors), retryableErrors)
		if cfg.pendingCap < cfg.concurrency || cfg.pendingCap > 10000 {
			return errors.New("checkpoint contains an invalid pending capacity")
		}
		log.Printf("resuming run=%s phase=%s freeze_offset=%d/%d", state.RunID, state.Phase, state.FreezeOffset, state.Total)
	}
	if state.Phase == phaseDone {
		return nil
	}
	if strings.TrimSpace(cfg.fallbackCredentialsPath) != "" {
		runtime.fallbackOAuth, err = loadFallbackOAuthCredentials(cfg.fallbackCredentialsPath, manifest)
		if err != nil {
			return err
		}
		log.Printf("fallback_oauth_credentials loaded=%d", len(runtime.fallbackOAuth))
	}
	if runtime.validationProxies.Capacity() < cfg.concurrency {
		return fmt.Errorf("validation concurrency %d requires at least %d unique IPv4 routes; only %d are available", cfg.concurrency, cfg.concurrency, runtime.validationProxies.Capacity())
	}
	log.Printf("validation_ipv4_pool unique_routes=%d exclusive_workers=%d", runtime.validationProxies.Capacity(), cfg.concurrency)
	defer func() {
		used, rotations, peak := runtime.validationProxies.Stats()
		log.Printf("validation_ipv4_pool used_routes=%d rotations=%d peak_exclusive_leases=%d", used, rotations, peak)
	}()
	if err := validateOperator(ctx, conn, state.OperatorID); err != nil {
		return err
	}

	if state.FreezeOffset < 0 || state.FreezeOffset > len(manifest) {
		return errors.New("checkpoint freeze offset is outside the manifest")
	}
	if state.Phase == phaseFreeze {
		state.Phase = phaseProcessing
		if err := saveCheckpoint(cfg.statePath, &state); err != nil {
			return err
		}
		if err := writeAudit(ctx, conn, state, "started", state.Eligible); err != nil {
			return err
		}
	} else if err := recoverAbandonedValidations(ctx, conn, manifest[:state.FreezeOffset], cfg.chunkSize, resumeSkippedErrors); err != nil {
		return err
	}

	result := newTracker(cfg.errorPath, previousSuccess)
	stage1Jobs, stage2Jobs, err := classifyJobs(ctx, conn, manifest[:state.FreezeOffset], cfg.chunkSize, result, resumeSkippedErrors)
	if err != nil {
		return err
	}
	log.Printf("dispatch frozen=%d/%d stage1=%d stage2=%d already_complete=%d rejected=%d", state.FreezeOffset, len(manifest), len(stage1Jobs), len(stage2Jobs), result.success.Load(), result.failure.Load())

	flushCtx, stopFlush := context.WithCancel(context.Background())
	flushDone := make(chan struct{})
	go result.flushLoop(flushCtx, flushDone)
	throughputCtx, stopThroughput := context.WithCancel(context.Background())
	throughputDone := make(chan struct{})
	go result.throughputLoop(throughputCtx, throughputDone)

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	runState := state
	stage1Ch := make(chan manifestRecord, cfg.pendingCap)
	stage2Input := make(chan manifestRecord, cfg.pendingCap)
	stage2Ch := make(chan manifestRecord, cfg.pendingCap)
	stage1Slots := make(chan struct{}, cfg.pendingCap)
	var stage1Workers sync.WaitGroup
	var stage2Workers sync.WaitGroup
	var stage2Producers sync.WaitGroup
	go relayJobs(workCtx, stage2Input, stage2Ch)

	stage2Workers.Add(cfg.concurrency)
	for worker := 0; worker < cfg.concurrency; worker++ {
		go func() {
			defer stage2Workers.Done()
			for item := range stage2Ch {
				if workCtx.Err() != nil {
					return
				}
				err := processHistory(workCtx, runtime, runState, cfg, item)
				result.stage2.Add(1)
				if err != nil {
					result.fail(item.Email)
					continue
				}
				result.succeed(item.Email)
			}
		}()
	}

	stage2Producers.Add(1)
	go func() {
		defer stage2Producers.Done()
		sendJobs(workCtx, stage2Jobs, stage2Input)
	}()

	stage1Workers.Add(cfg.concurrency)
	for worker := 0; worker < cfg.concurrency; worker++ {
		go func() {
			defer stage1Workers.Done()
			for item := range stage1Ch {
				if workCtx.Err() != nil {
					<-stage1Slots
					return
				}
				needsHistory, complete, err := processValidation(workCtx, runtime, runState, cfg, item)
				runtime.validationProxies.Release(item.Email)
				result.stage1.Add(1)
				<-stage1Slots
				switch {
				case err != nil:
					result.fail(item.Email)
					if validationFailureWasRateLimited(workCtx, runtime, item.ResourceID) {
						result.markRateLimited(item.Email)
					}
				case complete:
					result.succeed(item.Email)
				case needsHistory:
					select {
					case <-workCtx.Done():
						return
					case stage2Input <- item:
					}
				default:
					result.fail(item.Email)
				}
			}
		}()
	}

	freezeDone := make(chan error, 1)
	go func() {
		err := freezeAndFeed(workCtx, conn, manifest, stage1Jobs, cfg, &state, result, stage1Slots, stage1Ch)
		if err != nil {
			cancelWork()
		}
		freezeDone <- err
	}()
	stage2Producers.Add(1)
	go func() {
		defer stage2Producers.Done()
		stage1Workers.Wait()
	}()
	go func() {
		stage2Producers.Wait()
		close(stage2Input)
	}()

	stage2Workers.Wait()
	freezeErr := <-freezeDone
	stopThroughput()
	<-throughputDone
	stopFlush()
	<-flushDone
	if err := result.saveErrors(); err != nil {
		return err
	}
	if freezeErr != nil {
		return freezeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	completed := int(result.success.Load() + result.failure.Load())
	if completed != len(manifest) {
		return fmt.Errorf("batch stopped with %d of %d resources accounted", completed, len(manifest))
	}
	if err := writeAudit(ctx, conn, state, "completed", int(result.success.Load())); err != nil {
		return err
	}
	state.Phase = phaseDone
	if err := saveCheckpoint(cfg.statePath, &state); err != nil {
		return err
	}
	elapsed := time.Since(state.StartedAt)
	log.Printf("completed total=%d succeeded=%d failed=%d rate_limited=%d elapsed=%s average_per_minute=%.2f", len(manifest), result.success.Load(), result.failure.Load(), result.rateLimitedCount(), elapsed.Round(time.Second), float64(completed)/max(elapsed.Minutes(), 1.0/60.0))
	return nil
}

func openRuntime(ctx context.Context) (*commandRuntime, error) {
	cfg, err := platform.Load()
	if err != nil {
		return nil, err
	}
	p, cleanup, err := platform.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*commandRuntime, error) {
		cleanup()
		return nil, err
	}
	settings, err := settingsinfra.NewRepository(p.DB).List(ctx)
	if err != nil {
		return fail(err)
	}
	runtimeconfig.Replace(settings)
	files := governanceinfra.NewMinIOFileStore(p.MinIO, p.MinIOBucket)
	proxyModule, err := proxyapi.NewProxyModule(p.DB, p.Asynq)
	if err != nil {
		return fail(err)
	}
	resources := coreinfra.NewResourceRepo(p.DB)
	domains, allocationDomains, err := resources.ListBindingDomains(ctx)
	if err != nil {
		return fail(err)
	}
	if len(domains) == 0 {
		return fail(errors.New("no normal binding-purpose domains are configured"))
	}
	msacl.SetAuxiliaryDomainPolicy(domains, allocationDomains)
	msacl.SetMailboxReader(mailinfra.NewMSACLMailboxReaderWithContentWindow(p.DB, files, 90*24*time.Hour))
	msacl.SetRecoveryLeaseStore(mailinfra.NewMicrosoftBindingRecoveryLeaseStore(p.DB))
	bindingRepo := mailinfra.NewMicrosoftBindingRepo(p.DB)
	aliasStore := mailinfra.NewMicrosoftAliasStore(p.DB)
	validationProxies, err := loadValidationProxyLeasePool(ctx, p.DB, proxyModule.ProxyUseCase)
	if err != nil {
		return fail(err)
	}
	validator := mailapi.NewResourceValidationAdapterWithProxyProvider(validationProxies, bindingRepo)
	validationRepo := coreinfra.NewResourceValidationRepo(p.DB, p.Redis)
	validationRepo.SetMicrosoftValidationBindingCommitPort(mailapi.NewMicrosoftValidationBindingCommitAdapter(bindingRepo, aliasStore))
	validation := coreapp.NewResourceValidationUseCase(resources, validationRepo, nil, validator)

	adminRepo := coreinfra.NewAdminResourceRepo(p.DB, p.Redis)
	credentials := coreapp.NewMicrosoftCredentialService(adminRepo)
	billing := billingapi.NewBillingModule(p.DB, p.Asynq)
	allocation := allocapi.NewModule(p.DB, p.Redis, p.Asynq)
	projects := coreapp.NewProjectUseCase(coreinfra.NewProjectRepo(p.DB))
	trade := tradeapi.NewModule(p.DB, projects, billing.WalletUseCase, allocation.UseCase, openapiapi.NewModule(p.DB, p.Redis).UseCase)
	mailmatch := mailmatchapi.NewModule(p.DB, files, p.Redis, p.Asynq, proxyModule.ProxyUseCase, trade.UseCase, validation)
	mailmatch.SetMicrosoftCredentialPort(credentials)
	return &commandRuntime{
		platform: p, cleanup: cleanup, resources: resources, validation: validation,
		history: mailmatch.ProjectHistory, validationProxies: validationProxies,
	}, nil
}

func requirePausedQueues(p *platform.Platform) error {
	if p == nil {
		return errors.New("platform is unavailable")
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{
		Addr: p.Redis.Options().Addr, Password: p.Redis.Options().Password, DB: p.Redis.Options().DB,
	})
	defer inspector.Close()
	for _, queue := range []string{platform.QueueBackgroundValidation, platform.QueueBackgroundProjectHistory, platform.QueueBackgroundAlias} {
		info, err := inspector.GetQueueInfo(queue)
		if err != nil {
			return fmt.Errorf("inspect %s queue: %w", queue, err)
		}
		if !info.Paused || info.Active != 0 {
			return fmt.Errorf("queue %s must remain paused with zero active tasks", queue)
		}
	}
	return nil
}

func processValidation(ctx context.Context, runtime *commandRuntime, state checkpoint, cfg config, item manifestRecord) (needsHistory bool, complete bool, resultErr error) {
	var lastErr error
	for attempt := 0; attempt < cfg.stage1Retries; attempt++ {
		root, err := runtime.resources.FindByID(ctx, item.ResourceID)
		if err != nil || root == nil {
			return false, false, firstError(err, coredomain.ErrResourceNotFound)
		}
		resource, err := runtime.resources.FindMicrosoftByID(ctx, item.ResourceID)
		if err != nil || resource == nil {
			return false, false, firstError(err, coredomain.ErrResourceNotFound)
		}
		switch resource.Status {
		case coredomain.MicrosoftStatusIdentifying:
			return true, false, nil
		case coredomain.MicrosoftStatusNormal:
			if err := restoreForSale(ctx, runtime.platform.DB, item); err != nil {
				return false, false, err
			}
			return false, true, nil
		case coredomain.MicrosoftStatusAbnormal, coredomain.MicrosoftStatusDisabled, coredomain.MicrosoftStatusDeleted:
			return false, false, errors.New("hard reauthorization did not complete")
		case coredomain.MicrosoftStatusPending, coredomain.MicrosoftStatusValidating:
		default:
			return false, false, errors.New("resource is outside the validation state machine")
		}
		task := coreapp.ResourceValidationTask{
			ResourceID: item.ResourceID, ResourceType: coredomain.ResourceTypeMicrosoft,
			OwnerUserID: root.OwnerUserID, ValidationGeneration: resource.ValidationGeneration,
			ExpectedCredentialRevision: resource.CredentialRevision,
			RequestID:                  requestID(state.RunID, "validation", attempt+1, item.ResourceID),
		}
		attemptCtx, cancel := context.WithTimeout(ctx, cfg.stage1Timeout)
		if credentials, ok := runtime.fallbackOAuth[item.Email]; ok {
			attemptCtx = mailapi.WithMicrosoftValidationFallbackOAuthCredentials(
				attemptCtx,
				item.Email,
				credentials.ClientID,
				credentials.RefreshToken,
			)
		}
		err = runtime.validation.Process(attemptCtx, task, attempt+1 == cfg.stage1Retries)
		cancel()
		lastErr = err
		latest, loadErr := runtime.resources.FindMicrosoftByID(ctx, item.ResourceID)
		if loadErr != nil {
			lastErr = errors.Join(lastErr, loadErr)
		} else if latest != nil {
			switch latest.Status {
			case coredomain.MicrosoftStatusIdentifying:
				return true, false, nil
			case coredomain.MicrosoftStatusNormal:
				if err := restoreForSale(ctx, runtime.platform.DB, item); err != nil {
					return false, false, err
				}
				return false, true, nil
			case coredomain.MicrosoftStatusAbnormal:
				return false, false, firstError(lastErr, errors.New("hard reauthorization failed"))
			}
		}
		if attempt+1 < cfg.stage1Retries {
			if err := sleepContext(ctx, time.Duration(attempt+1)*3*time.Second); err != nil {
				return false, false, err
			}
		}
	}
	_ = markAbnormal(context.WithoutCancel(ctx), runtime.platform.DB, item.ResourceID, "Microsoft hard reauthorization did not complete.")
	return false, false, firstError(lastErr, errors.New("hard reauthorization attempts exhausted"))
}

func processHistory(ctx context.Context, runtime *commandRuntime, state checkpoint, cfg config, item manifestRecord) error {
	var lastErr error
	for attempt := 0; attempt < cfg.stage2Retries; attempt++ {
		resource, err := runtime.resources.FindMicrosoftByID(ctx, item.ResourceID)
		if err != nil || resource == nil {
			return firstError(err, coredomain.ErrResourceNotFound)
		}
		switch resource.Status {
		case coredomain.MicrosoftStatusNormal:
			return restoreForSale(ctx, runtime.platform.DB, item)
		case coredomain.MicrosoftStatusAbnormal, coredomain.MicrosoftStatusDisabled, coredomain.MicrosoftStatusDeleted:
			return errors.New("old-project identification did not complete")
		case coredomain.MicrosoftStatusIdentifying:
		default:
			return errors.New("resource is not ready for old-project identification")
		}
		attemptCtx, cancel := context.WithTimeout(ctx, cfg.stage2Timeout)
		err = runtime.history.ProcessValidatedMicrosoftHistory(attemptCtx, mailmatchapp.ValidatedMicrosoftHistoryScanTask{
			ResourceID: item.ResourceID,
			RequestID:  requestID(state.RunID, "history", attempt+1, item.ResourceID),
		})
		cancel()
		lastErr = err
		latest, loadErr := runtime.resources.FindMicrosoftByID(ctx, item.ResourceID)
		if loadErr != nil {
			lastErr = errors.Join(lastErr, loadErr)
		} else if latest != nil {
			switch latest.Status {
			case coredomain.MicrosoftStatusNormal:
				return restoreForSale(ctx, runtime.platform.DB, item)
			case coredomain.MicrosoftStatusAbnormal:
				return firstError(lastErr, errors.New("old-project identification failed"))
			}
		}
		if attempt+1 < cfg.stage2Retries {
			if err := sleepContext(ctx, time.Duration(attempt+1)*5*time.Second); err != nil {
				return err
			}
		}
	}
	_ = markAbnormal(context.WithoutCancel(ctx), runtime.platform.DB, item.ResourceID, "Old-project identification did not complete.")
	return firstError(lastErr, errors.New("old-project identification attempts exhausted"))
}

func classifyJobs(ctx context.Context, conn *sql.Conn, manifest []manifestRecord, chunkSize int, result *tracker, skippedErrors map[string]struct{}) ([]manifestRecord, []manifestRecord, error) {
	stage1 := make([]manifestRecord, 0, len(manifest))
	stage2 := make([]manifestRecord, 0, len(manifest)/4)
	for start := 0; start < len(manifest); start += chunkSize {
		end := min(start+chunkSize, len(manifest))
		chunk := manifest[start:end]
		ids := make([]uint64, 0, len(chunk))
		for _, item := range chunk {
			if item.Eligible && item.ResourceID != 0 {
				if _, skipped := skippedErrors[item.Email]; skipped {
					continue
				}
				ids = append(ids, uint64(item.ResourceID))
			}
		}
		states, err := loadDatabaseStates(ctx, conn, ids)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range chunk {
			if !item.Eligible || item.ResourceID == 0 {
				result.fail(item.Email)
				continue
			}
			if _, skipped := skippedErrors[item.Email]; skipped {
				result.fail(item.Email)
				continue
			}
			current, ok := states[item.ResourceID]
			if !ok || current.ValidationGeneration < item.ValidationGen+1 {
				result.fail(item.Email)
				continue
			}
			switch current.Status {
			case coredomain.MicrosoftStatusPending, coredomain.MicrosoftStatusValidating:
				stage1 = append(stage1, item)
			case coredomain.MicrosoftStatusIdentifying:
				stage2 = append(stage2, item)
			case coredomain.MicrosoftStatusNormal:
				if err := restoreForSaleSQL(ctx, conn, item); err != nil {
					return nil, nil, err
				}
				result.succeed(item.Email)
			default:
				result.fail(item.Email)
			}
		}
	}
	return stage1, stage2, nil
}

func recoverAbandonedValidations(ctx context.Context, conn *sql.Conn, manifest []manifestRecord, chunkSize int, skippedErrors map[string]struct{}) error {
	for start := 0; start < len(manifest); start += chunkSize {
		end := min(start+chunkSize, len(manifest))
		ids := make([]uint64, 0, end-start)
		for _, item := range manifest[start:end] {
			if item.Eligible && item.ResourceID != 0 {
				if _, skipped := skippedErrors[item.Email]; skipped {
					continue
				}
				ids = append(ids, uint64(item.ResourceID))
			}
		}
		if len(ids) == 0 {
			continue
		}
		placeholders := sqlPlaceholders(len(ids))
		if _, err := conn.ExecContext(ctx, `UPDATE microsoft_resources
SET status = 'pending', validation_generation = validation_generation + 1, updated_at = UTC_TIMESTAMP(3),
validation_failures = 0, graph_available = 0, last_safe_error = ''
WHERE id IN (`+placeholders+`) AND status IN ('validating','abnormal')`, uint64Args(ids)...); err != nil {
			return fmt.Errorf("recover abandoned validation resources: %w", err)
		}
	}
	return nil
}

func loadEmails(path string, offset, limit int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input file: %w", err)
	}
	defer file.Close()
	emails := make([]string, 0, 700000)
	seen := make(map[string]struct{}, 700000)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	index := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		email := line
		if strings.Contains(line, "----") {
			if strings.Count(line, "----") != 3 {
				return nil, errors.New("input contains an invalid imported resource record")
			}
			email, _, _ = strings.Cut(line, "----")
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || strings.Count(email, "@") != 1 || strings.ContainsAny(email, "\r\n\t ") {
			return nil, errors.New("input contains an invalid email address")
		}
		if _, ok := seen[email]; ok {
			return nil, errors.New("input contains a duplicate email address")
		}
		seen[email] = struct{}{}
		if index >= offset && (limit == 0 || len(emails) < limit) {
			emails = append(emails, email)
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(emails) == 0 {
		return nil, errors.New("selected input range is empty")
	}
	return emails, nil
}

func loadFallbackOAuthCredentials(path string, manifest []manifestRecord) (map[string]fallbackOAuthCredential, error) {
	wanted := make(map[string]struct{}, len(manifest))
	for _, item := range manifest {
		if item.Eligible && item.ResourceID != 0 {
			wanted[item.Email] = struct{}{}
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fallback credential file: %w", err)
	}
	defer file.Close()
	credentials := make(map[string]fallbackOAuthCredential, len(wanted))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "----")
		if len(parts) != 4 && len(parts) != 5 {
			return nil, errors.New("fallback credential file contains an invalid imported resource record")
		}
		email := strings.ToLower(strings.TrimSpace(parts[0]))
		address, parseErr := mail.ParseAddress(email)
		if parseErr != nil || address.Address != email {
			return nil, errors.New("fallback credential file contains an invalid email address")
		}
		if _, ok := wanted[email]; !ok {
			continue
		}
		clientID := strings.TrimSpace(parts[2])
		refreshToken := strings.TrimSpace(parts[3])
		if clientID == "" || refreshToken == "" {
			return nil, errors.New("fallback credential file contains incomplete OAuth credentials")
		}
		if _, duplicate := credentials[email]; duplicate {
			return nil, errors.New("fallback credential file contains a duplicate selected email")
		}
		credentials[email] = fallbackOAuthCredential{ClientID: clientID, RefreshToken: refreshToken}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(credentials) != len(wanted) {
		return nil, fmt.Errorf("fallback credential file matched %d of %d selected resources", len(credentials), len(wanted))
	}
	return credentials, nil
}

func loadEmailSet(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]struct{}), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open completed email file: %w", err)
	}
	defer file.Close()
	emails := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		email := strings.ToLower(strings.TrimSpace(scanner.Text()))
		address, parseErr := mail.ParseAddress(email)
		if parseErr != nil || email == "" || address.Address != email {
			return nil, errors.New("completed email file contains an invalid email address")
		}
		emails[email] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return emails, nil
}

func excludeEmails(emails []string, excluded map[string]struct{}) ([]string, int) {
	if len(excluded) == 0 {
		return emails, 0
	}
	remaining := emails[:0]
	for _, email := range emails {
		if _, ok := excluded[email]; ok {
			continue
		}
		remaining = append(remaining, email)
	}
	return remaining, len(emails) - len(remaining)
}

func resumeErrorSkipSet(previousErrors, previousRateLimited map[string]struct{}, retryAll bool) map[string]struct{} {
	if retryAll || len(previousErrors) == 0 {
		return nil
	}
	skipped := make(map[string]struct{}, len(previousErrors))
	for email := range previousErrors {
		if _, retry := previousRateLimited[email]; !retry {
			skipped[email] = struct{}{}
		}
	}
	return skipped
}

func snapshotManifest(ctx context.Context, conn *sql.Conn, emails []string, chunkSize int) ([]manifestRecord, error) {
	manifest := make([]manifestRecord, 0, len(emails))
	for start := 0; start < len(emails); start += chunkSize {
		end := min(start+chunkSize, len(emails))
		chunk := emails[start:end]
		rows, err := conn.QueryContext(ctx, `SELECT
mr.id, er.owner_user_id, LOWER(TRIM(mr.email_address)), mr.for_sale,
mr.validation_generation, mr.credential_revision
FROM microsoft_resources mr
JOIN email_resources er ON er.id = mr.id AND er.type = 'microsoft'
WHERE mr.email_address IN (`+sqlPlaceholders(len(chunk))+`)`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		found := make(map[string]manifestRecord, len(chunk))
		for rows.Next() {
			var item manifestRecord
			if err := rows.Scan(&item.ResourceID, &item.OwnerUserID, &item.Email, &item.OriginalForSale, &item.ValidationGen, &item.CredentialRevision); err != nil {
				rows.Close()
				return nil, err
			}
			item.Eligible = true
			found[item.Email] = item
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, email := range chunk {
			item, ok := found[email]
			if !ok {
				item = manifestRecord{Email: email}
			}
			manifest = append(manifest, item)
		}
	}
	return manifest, nil
}

func freezeChunk(ctx context.Context, conn *sql.Conn, records []manifestRecord) (resultErr error) {
	ids := make([]uint64, 0, len(records))
	for _, item := range records {
		if item.Eligible && item.ResourceID != 0 {
			ids = append(ids, uint64(item.ResourceID))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback()
		}
	}()
	placeholders := sqlPlaceholders(len(ids))
	args := uint64Args(ids)
	updated, err := tx.ExecContext(ctx, `UPDATE microsoft_resources
SET status = 'pending', for_sale = 0,
validation_generation = IF(validation_generation = 0, 1, validation_generation + 1),
validation_failures = 0, graph_available = 0, last_safe_error = '', updated_at = UTC_TIMESTAMP(3)
WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	if affected, err := updated.RowsAffected(); err != nil || affected != int64(len(ids)) {
		return fmt.Errorf("freeze affected %d of %d resources", affected, len(ids))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_resources
SET version = version + 1, updated_at = UTC_TIMESTAMP(3)
WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func freezeAndFeed(
	ctx context.Context,
	conn *sql.Conn,
	manifest []manifestRecord,
	resume []manifestRecord,
	cfg config,
	state *checkpoint,
	result *tracker,
	slots chan struct{},
	output chan<- manifestRecord,
) error {
	defer close(output)
	for _, item := range resume {
		if err := acquireSlot(ctx, slots); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			<-slots
			return ctx.Err()
		case output <- item:
		}
	}

	maxBatch := min(cfg.chunkSize, cfg.pendingCap)
	for state.FreezeOffset < len(manifest) {
		start := state.FreezeOffset
		next := start
		batch := make([]manifestRecord, 0, maxBatch)
		for next < len(manifest) && len(batch) == 0 {
			item := manifest[next]
			next++
			if !item.Eligible || item.ResourceID == 0 {
				result.fail(item.Email)
				continue
			}
			if err := acquireSlot(ctx, slots); err != nil {
				return err
			}
			batch = append(batch, item)
		}
	gather:
		for next < len(manifest) && len(batch) < maxBatch {
			item := manifest[next]
			if !item.Eligible || item.ResourceID == 0 {
				result.fail(item.Email)
				next++
				continue
			}
			select {
			case <-ctx.Done():
				releaseSlots(slots, len(batch))
				return ctx.Err()
			case slots <- struct{}{}:
				batch = append(batch, item)
				next++
			default:
				break gather
			}
		}
		if len(batch) > 0 {
			if err := freezeChunk(ctx, conn, batch); err != nil {
				releaseSlots(slots, len(batch))
				return err
			}
		}
		state.FreezeOffset = next
		if err := saveCheckpoint(cfg.statePath, state); err != nil {
			releaseSlots(slots, len(batch))
			return err
		}
		if next == len(manifest) || next/10000 != start/10000 {
			log.Printf("freeze progress=%d/%d pending_capacity=%d", next, len(manifest), cfg.pendingCap)
		}
		for index, item := range batch {
			select {
			case <-ctx.Done():
				releaseSlots(slots, len(batch)-index)
				return ctx.Err()
			case output <- item:
			}
		}
	}
	return nil
}

func acquireSlot(ctx context.Context, slots chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case slots <- struct{}{}:
		return nil
	}
}

func releaseSlots(slots chan struct{}, count int) {
	for range count {
		<-slots
	}
}

func loadDatabaseStates(ctx context.Context, conn *sql.Conn, ids []uint64) (map[uint]databaseState, error) {
	result := make(map[uint]databaseState, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, status, for_sale, validation_generation, credential_revision
FROM microsoft_resources WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, uint64Args(ids)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint
		var status string
		var state databaseState
		if err := rows.Scan(&id, &status, &state.ForSale, &state.ValidationGeneration, &state.CredentialRevision); err != nil {
			rows.Close()
			return nil, err
		}
		state.Status = coredomain.MicrosoftResourceStatus(status)
		result[id] = state
	}
	return result, rows.Close()
}

const restoreForSaleStatement = `UPDATE microsoft_resources mr
JOIN email_resources er ON er.id = mr.id
SET mr.for_sale = ?,
    mr.updated_at = UTC_TIMESTAMP(3), er.version = er.version + 1, er.updated_at = UTC_TIMESTAMP(3)
WHERE mr.id = ? AND mr.status = 'normal'`

func restoreForSale(ctx context.Context, db *gorm.DB, item manifestRecord) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Exec(restoreForSaleStatement, item.OriginalForSale, item.ResourceID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return errors.New("validated resource final sale state could not be restored")
		}
		return nil
	})
}

func restoreForSaleSQL(ctx context.Context, conn *sql.Conn, item manifestRecord) error {
	updated, err := conn.ExecContext(ctx, restoreForSaleStatement, item.OriginalForSale, item.ResourceID)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("validated resource final sale state could not be restored")
	}
	return nil
}

func markAbnormal(ctx context.Context, db *gorm.DB, resourceID uint, safeError string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Exec(`UPDATE microsoft_resources
SET status = 'abnormal', for_sale = 0, validation_generation = validation_generation + 1,
last_safe_error = ?, updated_at = UTC_TIMESTAMP(3)
WHERE id = ? AND status IN ('pending','validating','identifying')`, safeError, resourceID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return nil
		}
		return tx.Exec(`UPDATE email_resources SET version = version + 1, updated_at = UTC_TIMESTAMP(3) WHERE id = ?`, resourceID).Error
	})
}

func validationFailureWasRateLimited(ctx context.Context, runtime *commandRuntime, resourceID uint) bool {
	if runtime == nil || runtime.resources == nil || resourceID == 0 {
		return false
	}
	resource, err := runtime.resources.FindMicrosoftByID(ctx, resourceID)
	return err == nil && resource != nil && isRateLimitedSafeMessage(resource.LastSafeError)
}

func isRateLimitedSafeMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "rate limit") ||
		strings.Contains(message, "rate_limited") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "429") ||
		strings.Contains(message, "频率受限")
}

func newTracker(errorOut string, previousSuccess map[string]struct{}) *tracker {
	succeeded := make(map[string]struct{}, len(previousSuccess))
	for email := range previousSuccess {
		succeeded[email] = struct{}{}
	}
	return &tracker{
		failed: make(map[string]struct{}), rateLimited: make(map[string]struct{}), succeeded: succeeded, seen: make(map[string]struct{}),
		errorOut: errorOut, rateLimitOut: filepath.Join(filepath.Dir(errorOut), "429.txt"), successOut: filepath.Join(filepath.Dir(errorOut), "success.txt"),
	}
}

func (t *tracker) succeed(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[email]; ok {
		return
	}
	t.seen[email] = struct{}{}
	t.succeeded[email] = struct{}{}
	t.success.Add(1)
}

func (t *tracker) fail(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[email]; ok {
		return
	}
	t.seen[email] = struct{}{}
	t.failed[email] = struct{}{}
	t.failure.Add(1)
}

func (t *tracker) markRateLimited(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, failed := t.failed[email]; failed {
		t.rateLimited[email] = struct{}{}
	}
}

func (t *tracker) rateLimitedCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.rateLimited)
}

func (t *tracker) saveErrors() error {
	t.mu.Lock()
	emails := make([]string, 0, len(t.failed))
	for email := range t.failed {
		emails = append(emails, email)
	}
	rateLimited := make([]string, 0, len(t.rateLimited))
	for email := range t.rateLimited {
		rateLimited = append(rateLimited, email)
	}
	succeeded := make([]string, 0, len(t.succeeded))
	for email := range t.succeeded {
		succeeded = append(succeeded, email)
	}
	t.mu.Unlock()
	sort.Strings(emails)
	sort.Strings(rateLimited)
	sort.Strings(succeeded)
	return errors.Join(
		saveErrors(t.errorOut, emails),
		saveErrors(t.rateLimitOut, rateLimited),
		saveErrors(t.successOut, succeeded),
	)
}

func (t *tracker) flushLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = t.saveErrors()
			return
		case <-ticker.C:
			_ = t.saveErrors()
		}
	}
}

func (t *tracker) throughputLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var previousStage1, previousStage2 int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stage1 := t.stage1.Load()
			stage2 := t.stage2.Load()
			log.Printf("one_minute_throughput stage1=%d stage2=%d total_success=%d total_failed=%d", stage1-previousStage1, stage2-previousStage2, t.success.Load(), t.failure.Load())
			previousStage1, previousStage2 = stage1, stage2
		}
	}
}

func saveManifest(path string, records []manifestRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	for _, item := range records {
		if _, err := fmt.Fprintf(writer, "%s\t%d\t%d\t%t\t%d\t%d\t%t\n", item.Email, item.ResourceID, item.OwnerUserID, item.OriginalForSale, item.ValidationGen, item.CredentialRevision, item.Eligible); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadManifest(path string) ([]manifestRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	records := make([]manifestRecord, 0, 700000)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "\t")
		if len(parts) != 7 {
			return nil, errors.New("invalid manifest record")
		}
		resourceID, err1 := strconv.ParseUint(parts[1], 10, 64)
		ownerID, err2 := strconv.ParseUint(parts[2], 10, 64)
		forSale, err3 := strconv.ParseBool(parts[3])
		generation, err4 := strconv.ParseUint(parts[4], 10, 64)
		revision, err5 := strconv.ParseUint(parts[5], 10, 64)
		eligible, err6 := strconv.ParseBool(parts[6])
		if errors.Join(err1, err2, err3, err4, err5, err6) != nil {
			return nil, errors.New("invalid manifest value")
		}
		records = append(records, manifestRecord{Email: parts[0], ResourceID: uint(resourceID), OwnerUserID: uint(ownerID), OriginalForSale: forSale, ValidationGen: generation, CredentialRevision: revision, Eligible: eligible})
	}
	return records, scanner.Err()
}

func saveErrors(path string, emails []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	for _, email := range emails {
		if _, err := writer.WriteString(email + "\n"); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveCheckpoint(path string, state *checkpoint) error {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadCheckpoint(path string) (checkpoint, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint{}, false, nil
	}
	if err != nil {
		return checkpoint{}, false, err
	}
	var state checkpoint
	if err := json.Unmarshal(data, &state); err != nil {
		return checkpoint{}, false, err
	}
	if state.Version != 1 || state.RunID == "" || state.ManifestPath == "" || state.Total < 1 || state.Concurrency < 1 || state.FreezeOffset < 0 {
		return checkpoint{}, false, errors.New("invalid checkpoint")
	}
	switch state.Phase {
	case phaseFreeze, phaseProcessing, phaseDone:
	default:
		return checkpoint{}, false, errors.New("invalid checkpoint phase")
	}
	return state, true, nil
}

func validateOperator(ctx context.Context, conn *sql.Conn, operatorID uint64) error {
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = ? AND status = 'active' AND role IN ('admin','super_admin')", operatorID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("operator user %d is not an active administrator", operatorID)
	}
	return nil
}

func writeAudit(ctx context.Context, conn *sql.Conn, state checkpoint, result string, count int) error {
	requestID := state.RunID + "-" + result
	_, err := conn.ExecContext(ctx, `INSERT INTO operation_logs
(operator_user_id, operation_type, resource_type, resource_id, path, result, safe_summary, request_id, created_at)
SELECT ?, 'core.luckmail_validation_batch', 'microsoft_resource', 'batch', ?, ?, ?, ?, UTC_TIMESTAMP()
WHERE NOT EXISTS (SELECT 1 FROM operation_logs WHERE operation_type = 'core.luckmail_validation_batch' AND request_id = ?)`,
		state.OperatorID, requestPath, result, fmt.Sprintf("Luckmail validation batch %s; resources=%d.", result, count), requestID, requestID)
	return err
}

func acquireLock(ctx context.Context, conn *sql.Conn) (bool, error) {
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('remail_luckmail_validation', 0)").Scan(&locked); err != nil {
		return false, err
	}
	return locked.Valid && locked.Int64 == 1, nil
}

func releaseLock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK('remail_luckmail_validation')")
}

func normalizeRunID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "luckmail-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if len(value) > 32 {
		return "", errors.New("run ID exceeds 32 characters")
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return "", errors.New("run ID contains unsupported characters")
		}
	}
	return value, nil
}

func requestID(runID, stage string, attempt int, resourceID uint) string {
	return fmt.Sprintf("%.24s-%s%d-%d", runID, string(stage[0]), attempt, resourceID)
}

func sendJobs(ctx context.Context, jobs []manifestRecord, output chan<- manifestRecord) {
	for _, item := range jobs {
		select {
		case <-ctx.Done():
			return
		case output <- item:
		}
	}
}

func relayJobs(ctx context.Context, input <-chan manifestRecord, output chan<- manifestRecord) {
	defer close(output)
	queue := make([]manifestRecord, 0)
	for input != nil || len(queue) > 0 {
		var send chan<- manifestRecord
		var next manifestRecord
		if len(queue) > 0 {
			send = output
			next = queue[0]
		}
		select {
		case <-ctx.Done():
			return
		case item, ok := <-input:
			if !ok {
				input = nil
				continue
			}
			queue = append(queue, item)
		case send <- next:
			queue[0] = manifestRecord{}
			queue = queue[1:]
			if len(queue) == 0 {
				queue = nil
			}
		}
	}
}

func sqlPlaceholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

func stringArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func uint64Args(values []uint64) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
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

func firstError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
