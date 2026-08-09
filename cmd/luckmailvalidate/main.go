package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
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
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
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
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

const (
	phaseFreeze     = "freeze"
	phaseProcessing = "processing"
	phaseDone       = "done"

	validationProxyCooldown          = time.Minute
	validationProxySourceTimeout     = 30 * time.Second
	validationProxySourceMaxBytes    = 4 << 20
	validationProxySourceRetryDelay  = 5 * time.Second
	validationProxySourceMaxRequests = 8
	recoveryMailboxBusyRetryDelay    = 5 * time.Second
	recoveryDispatchQueryChunkSize   = 1000
	recoveryDispatchRefreshTimeout   = 5 * time.Second

	requestPath = "/ops/luckmail-validation"
)

var (
	errRecoveryMailboxBusy            = errors.New("recovery mailbox is busy")
	errValidationProxySourceExhausted = errors.New("validation proxy source traffic is exhausted")
)

type config struct {
	apply                   bool
	filePath                string
	proxyFilePath           string
	proxyURL                string
	proxyBatchSize          int
	proxyRefillThreshold    int
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
	credentialTypeRPS       int
	credentialTypeBurst     int
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
	ValidationGen      uint64
	CredentialRevision uint64
	Eligible           bool
	RecoveryKey        string
	CheckRecoveryLease bool
}

type fallbackOAuthCredential struct {
	ClientID     string
	RefreshToken string
}

type databaseState struct {
	Status               coredomain.MicrosoftResourceStatus
	ValidationGeneration uint64
	CredentialRevision   uint64
}

type commandRuntime struct {
	platform          *platform.Platform
	cleanup           func()
	resources         *coreinfra.ResourceRepo
	bindings          *mailinfra.MicrosoftBindingRepo
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
	upstream        *proxyapp.ProxyUseCase
	refill          func(context.Context) ([]proxyapp.ProxyConfig, error)
	reuseReleased   bool
	refillThreshold int
	refillMu        sync.Mutex
	mu              sync.Mutex
	available       []proxyapp.ProxyConfig
	leased          map[string]proxyapp.ProxyConfig
	cooldownUntil   map[uint]time.Time
	refillErr       error
	refillRetryAt   time.Time
	sourceExhausted bool
	exhaustedLogged bool
	wake            chan struct{}
	used            map[uint]struct{}
	seenURLs        map[string]struct{}
	capacity        int
	rotations       int
	peak            int
}

type validationProxyRouteLoader func(context.Context) ([]proxyapp.ProxyConfig, error)

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
	recoveryBusy atomic.Int64
	errorOut     string
	rateLimitOut string
	successOut   string
}

type stage1Completion struct {
	Item      manifestRecord
	ActiveKey string
	RetryAt   time.Time
	Retry     bool
}

type commandHistoryTrigger struct {
	mu        sync.Mutex
	bound     map[uint]manifestRecord
	scheduled map[uint]struct{}
	output    chan<- manifestRecord
}

func newCommandHistoryTrigger(output chan<- manifestRecord) *commandHistoryTrigger {
	return &commandHistoryTrigger{
		bound: make(map[uint]manifestRecord), scheduled: make(map[uint]struct{}), output: output,
	}
}

func (t *commandHistoryTrigger) bind(item manifestRecord) {
	if t == nil || item.ResourceID == 0 {
		return
	}
	t.mu.Lock()
	t.bound[item.ResourceID] = item
	t.mu.Unlock()
}

func (t *commandHistoryTrigger) unbind(resourceID uint) {
	if t == nil || resourceID == 0 {
		return
	}
	t.mu.Lock()
	delete(t.bound, resourceID)
	t.mu.Unlock()
}

func (t *commandHistoryTrigger) ScheduleValidatedMicrosoftHistory(ctx context.Context, resourceID uint, _ string) error {
	if t == nil || resourceID == 0 || t.output == nil {
		return errors.New("CMD history trigger is unavailable")
	}
	t.mu.Lock()
	item, ok := t.bound[resourceID]
	t.mu.Unlock()
	if !ok {
		return errors.New("CMD history trigger has no active validation item")
	}
	return t.enqueue(ctx, item)
}

func (t *commandHistoryTrigger) enqueue(ctx context.Context, item manifestRecord) error {
	if t == nil || item.ResourceID == 0 || t.output == nil {
		return errors.New("CMD history trigger is unavailable")
	}
	t.mu.Lock()
	if _, exists := t.scheduled[item.ResourceID]; exists {
		t.mu.Unlock()
		return nil
	}
	t.scheduled[item.ResourceID] = struct{}{}
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.scheduled, item.ResourceID)
		t.mu.Unlock()
		return ctx.Err()
	case t.output <- item:
		return nil
	}
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
	uniqueRows := make([]validationProxyRoute, 0, len(rows))
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
		uniqueRows = append(uniqueRows, row)
	}
	if len(uniqueRows) == 0 {
		return newValidationProxyLeasePool(upstream, nil), nil
	}
	uniqueRows, subnetCount := interleaveValidationProxyRoutesBy24(uniqueRows)
	routes := make([]proxyapp.ProxyConfig, 0, len(uniqueRows))
	for _, row := range uniqueRows {
		routes = append(routes, proxyapp.ProxyConfig{
			ID: row.ID, ProxyServerID: row.ProxyServerID, Pool: proxydomain.ProxyPoolResource,
			URL: row.URL, IPVersion: proxydomain.ProxyIPv4, Country: row.Country, LatencyMs: row.LatencyMs,
		})
	}
	log.Printf("validation_ipv4_subnet_rotation routes=%d subnets=%d", len(routes), subnetCount)
	return newValidationProxyLeasePool(upstream, routes), nil
}

func interleaveValidationProxyRoutesBy24(rows []validationProxyRoute) ([]validationProxyRoute, int) {
	groups := make(map[string][]validationProxyRoute, len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key := validationProxySubnetKey(row.OutboundIP)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], row)
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for _, key := range keys {
		group := groups[key]
		rand.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
		groups[key] = group
	}
	ordered := make([]validationProxyRoute, 0, len(rows))
	for len(ordered) < len(rows) {
		for _, key := range keys {
			group := groups[key]
			if len(group) == 0 {
				continue
			}
			ordered = append(ordered, group[0])
			groups[key] = group[1:]
		}
	}
	return ordered, len(keys)
}

func validationProxySubnetKey(rawIP string) string {
	trimmed := strings.TrimSpace(rawIP)
	address, err := netip.ParseAddr(trimmed)
	if err != nil || !address.Unmap().Is4() {
		return "unknown:" + strings.ToLower(trimmed)
	}
	octets := address.Unmap().As4()
	return fmt.Sprintf("%d.%d.%d.0/24", octets[0], octets[1], octets[2])
}

func loadValidationProxyFile(path string) ([]proxyapp.ProxyConfig, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("open proxy file: %w", err)
	}
	defer file.Close()

	nextID := uint(1)
	routes, err := parseValidationProxyRoutes(file, &nextID)
	if err != nil {
		return nil, err
	}
	rand.Shuffle(len(routes), func(i, j int) { routes[i], routes[j] = routes[j], routes[i] })
	return routes, nil
}

func newValidationProxyURLLoader(rawURL string, batchSize int) (validationProxyRouteLoader, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") || batchSize < 1 {
		return nil, errors.New("invalid proxy source URL")
	}
	query := parsed.Query()
	proxyScheme := "http"
	if configuredScheme := strings.ToLower(strings.TrimSpace(query.Get("proxyType"))); configuredScheme != "" {
		switch configuredScheme {
		case "http", "https", "socks5", "socks5h":
			proxyScheme = configuredScheme
		default:
			return nil, errors.New("invalid proxy source proxyType")
		}
	}
	batchParameter := "num"
	if _, ok := query["ips"]; ok {
		batchParameter = "ips"
	}
	query.Set(batchParameter, strconv.Itoa(batchSize))
	parsed.RawQuery = query.Encode()
	sourceURL := parsed.String()
	client := &http.Client{Timeout: validationProxySourceTimeout}
	nextID := uint(1)
	var sourceExhausted atomic.Bool
	return func(ctx context.Context) ([]proxyapp.ProxyConfig, error) {
		if sourceExhausted.Load() {
			return nil, errValidationProxySourceExhausted
		}
		routes := make([]proxyapp.ProxyConfig, 0, batchSize)
		seenURLs := make(map[string]struct{}, batchSize)
		for requestNumber := 0; len(routes) < batchSize && requestNumber < validationProxySourceMaxRequests; requestNumber++ {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
			if err != nil {
				if len(routes) > 0 {
					break
				}
				return nil, errors.New("build proxy source request")
			}
			request.Header.Set("Accept", "text/plain")
			response, err := client.Do(request)
			if err != nil {
				if len(routes) > 0 {
					break
				}
				return nil, errors.New("fetch proxy source failed")
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, validationProxySourceMaxBytes+1))
			response.Body.Close()
			if readErr != nil {
				if len(routes) > 0 {
					break
				}
				return nil, errors.New("read proxy source response failed")
			}
			if len(body) > validationProxySourceMaxBytes {
				if len(routes) > 0 {
					break
				}
				return nil, errors.New("proxy source response is too large")
			}
			if validationProxySourceTrafficExhausted(body) {
				sourceExhausted.Store(true)
				if len(routes) > 0 {
					break
				}
				return nil, errValidationProxySourceExhausted
			}
			if response.StatusCode != http.StatusOK {
				if len(routes) > 0 {
					break
				}
				return nil, fmt.Errorf("proxy source returned HTTP %d", response.StatusCode)
			}
			batchRoutes, err := parseValidationProxyRoutesWithScheme(strings.NewReader(string(body)), &nextID, proxyScheme)
			if err != nil {
				if len(routes) > 0 {
					break
				}
				return nil, err
			}
			added := 0
			for _, route := range batchRoutes {
				if _, duplicate := seenURLs[route.URL]; duplicate {
					continue
				}
				seenURLs[route.URL] = struct{}{}
				routes = append(routes, route)
				added++
				if len(routes) == batchSize {
					break
				}
			}
			if added == 0 {
				break
			}
		}
		if len(routes) == 0 {
			return nil, errors.New("proxy source contains no valid proxies")
		}
		rand.Shuffle(len(routes), func(i, j int) { routes[i], routes[j] = routes[j], routes[i] })
		return routes, nil
	}, nil
}

func validationProxySourceTrafficExhausted(body []byte) bool {
	message := strings.ToLower(strings.TrimSpace(string(body)))
	return strings.Contains(message, "insufficient traffic")
}

func parseValidationProxyRoutes(reader io.Reader, nextID *uint) ([]proxyapp.ProxyConfig, error) {
	return parseValidationProxyRoutesWithScheme(reader, nextID, "http")
}

func parseValidationProxyRoutesWithScheme(reader io.Reader, nextID *uint, proxyScheme string) ([]proxyapp.ProxyConfig, error) {
	if reader == nil || nextID == nil || *nextID == 0 {
		return nil, errors.New("invalid proxy source")
	}
	scanner := bufio.NewScanner(reader)
	routes := make([]proxyapp.ProxyConfig, 0, 256)
	seen := make(map[string]struct{}, 256)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if lineNumber == 1 {
			line = strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF"))
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "://") {
			parts := strings.SplitN(line, ":", 4)
			if len(parts) == 4 {
				host, port := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
				username, password := parts[2], parts[3]
				if host == "" || port == "" || username == "" || password == "" {
					return nil, fmt.Errorf("proxy source line %d: invalid proxy URL", lineNumber)
				}
				line = (&url.URL{
					Scheme: proxyScheme,
					Host:   net.JoinHostPort(host, port),
					User:   url.UserPassword(username, password),
				}).String()
			} else {
				line = proxyScheme + "://" + line
			}
		}
		normalized, err := proxydomain.NormalizeProxyURL(line)
		if err != nil {
			return nil, fmt.Errorf("proxy source line %d: invalid proxy URL", lineNumber)
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		id := *nextID
		(*nextID)++
		routes = append(routes, proxyapp.ProxyConfig{
			ID: id, ProxyServerID: id, Pool: proxydomain.ProxyPoolResource,
			URL: normalized, IPVersion: proxydomain.ProxyIPv4,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("read proxy source failed")
	}
	if len(routes) == 0 {
		return nil, errors.New("proxy source contains no valid proxies")
	}
	return routes, nil
}

func newValidationProxyLeasePool(upstream *proxyapp.ProxyUseCase, routes []proxyapp.ProxyConfig) *validationProxyLeasePool {
	available := append([]proxyapp.ProxyConfig(nil), routes...)
	seenURLs := make(map[string]struct{}, len(available))
	for _, route := range available {
		seenURLs[route.URL] = struct{}{}
	}
	return &validationProxyLeasePool{
		upstream: upstream, available: available,
		reuseReleased: true,
		leased:        make(map[string]proxyapp.ProxyConfig, len(available)),
		cooldownUntil: make(map[uint]time.Time, len(available)),
		wake:          make(chan struct{}, 1),
		used:          make(map[uint]struct{}, len(available)), seenURLs: seenURLs, capacity: len(available),
	}
}

func newRefillableValidationProxyLeasePool(routes []proxyapp.ProxyConfig, refill validationProxyRouteLoader, refillThreshold int) *validationProxyLeasePool {
	pool := newValidationProxyLeasePool(nil, routes)
	pool.refill = refill
	pool.reuseReleased = false
	pool.refillThreshold = refillThreshold
	return pool
}

func (p *validationProxyLeasePool) refillRoutes(ctx context.Context) error {
	if p == nil || p.refill == nil {
		return errors.New("validation proxy refill is unavailable")
	}
	p.refillMu.Lock()
	defer p.refillMu.Unlock()

	p.mu.Lock()
	if p.sourceExhausted {
		p.mu.Unlock()
		return errValidationProxySourceExhausted
	}
	available := len(p.available)
	if available >= p.refillThreshold {
		p.mu.Unlock()
		return nil
	}
	retryAt := p.refillRetryAt
	previousErr := p.refillErr
	p.mu.Unlock()

	if !retryAt.IsZero() && time.Now().Before(retryAt) {
		if available > 0 {
			return previousErr
		}
		timer := time.NewTimer(time.Until(retryAt))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}

	routes, err := p.refill(ctx)
	if err == nil && len(routes) == 0 {
		err = errors.New("proxy source contains no valid proxies")
	}
	p.mu.Lock()
	if p.seenURLs == nil {
		p.seenURLs = make(map[string]struct{}, len(p.available)+len(routes))
		for _, route := range p.available {
			p.seenURLs[route.URL] = struct{}{}
		}
	}
	if err != nil {
		if errors.Is(err, errValidationProxySourceExhausted) {
			p.sourceExhausted = true
			p.refillErr = errValidationProxySourceExhausted
			p.refillRetryAt = time.Time{}
			available = len(p.available)
			shouldLog := !p.exhaustedLogged
			p.exhaustedLogged = true
			p.mu.Unlock()
			if shouldLog {
				log.Printf("validation_proxy_source_exhausted available=%d", available)
			}
			return errValidationProxySourceExhausted
		}
		p.refillErr = err
		p.refillRetryAt = time.Now().Add(validationProxySourceRetryDelay)
		available = len(p.available)
		p.mu.Unlock()
		log.Printf("validation_proxy_refill_failed available=%d error=%s", available, err)
		return err
	}
	added := 0
	for _, route := range routes {
		if _, duplicate := p.seenURLs[route.URL]; duplicate {
			continue
		}
		p.seenURLs[route.URL] = struct{}{}
		p.available = append(p.available, route)
		added++
	}
	if added == 0 {
		err = errors.New("proxy source returned no unused proxies")
		p.refillErr = err
		p.refillRetryAt = time.Now().Add(validationProxySourceRetryDelay)
		available = len(p.available)
		p.mu.Unlock()
		log.Printf("validation_proxy_refill_failed available=%d error=%s", available, err)
		return err
	}
	p.refillErr = nil
	p.refillRetryAt = time.Time{}
	available = len(p.available)
	p.mu.Unlock()
	p.notify()
	log.Printf("validation_proxy_refill added=%d duplicate=%d available=%d", added, len(routes)-added, available)
	return nil
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
	maxAttempts := runtimeconfig.Int("max_proxy_attempts", 3, 1)
	if req.Attempt >= maxAttempts {
		return nil, errors.New("validation proxy attempt budget exhausted")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var refillErr error
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
			if p.reuseReleased {
				p.available = append(p.available, current)
			} else {
				delete(p.cooldownUntil, current.ID)
			}
			p.rotations++
		}
		if p.refill != nil && len(p.available) < p.refillThreshold {
			p.mu.Unlock()
			refillErr = p.refillRoutes(ctx)
			p.mu.Lock()
			now = time.Now()
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
		if p.refill != nil {
			if refillErr == nil {
				refillErr = p.refillRoutes(ctx)
			}
			if refillErr == nil {
				continue
			}
			if availableCount == 0 && leasedCount > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-p.wake:
					continue
				}
			}
			if availableCount == 0 {
				return nil, refillErr
			}
		}

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
		if until, cooling := p.cooldownUntil[proxyID]; cooling {
			if time.Now().Before(until) {
				p.mu.Unlock()
				return nil
			}
			delete(p.cooldownUntil, proxyID)
		}
		p.mu.Unlock()
		p.notify()
	}
	if p.upstream == nil {
		return nil
	}
	return p.upstream.ReportSuccess(ctx, proxyID)
}

// ReportRateLimited cools only the CMD-local lease. A Microsoft 429 is an
// upstream quota signal, not evidence that the proxy itself is unhealthy, so
// it must not call the upstream proxy health reporter in either direction.
func (p *validationProxyLeasePool) ReportRateLimited(_ context.Context, proxyID uint) error {
	if p == nil || proxyID == 0 {
		return nil
	}
	p.setCooldown(proxyID)
	return nil
}

func (p *validationProxyLeasePool) ReportFailure(ctx context.Context, proxyID uint, safeError string) error {
	if p == nil {
		return nil
	}
	if proxyID != 0 && isRateLimitedSafeMessage(safeError) {
		return p.ReportRateLimited(ctx, proxyID)
	}
	p.setCooldown(proxyID)
	if p.upstream == nil {
		return nil
	}
	return p.upstream.ReportFailure(ctx, proxyID, safeError)
}

func (p *validationProxyLeasePool) setCooldown(proxyID uint) {
	if p == nil || proxyID == 0 {
		return
	}
	until := time.Now().Add(validationProxyCooldown)
	p.mu.Lock()
	if previous, ok := p.cooldownUntil[proxyID]; !ok || until.After(previous) {
		p.cooldownUntil[proxyID] = until
	}
	p.mu.Unlock()
	p.notify()
}

// CooldownLease is the worker-side safety net. It runs while the resource's
// proxy is still leased, so a late database observation of 429 cannot release
// the route into another worker before the local cooldown is installed.
func (p *validationProxyLeasePool) CooldownLease(key string) {
	if p == nil {
		return
	}
	key = strings.ToLower(strings.TrimSpace(key))
	p.mu.Lock()
	current, ok := p.leased[key]
	p.mu.Unlock()
	if ok {
		p.setCooldown(current.ID)
	}
}

func (p *validationProxyLeasePool) Release(key string) {
	if p == nil {
		return
	}
	key = strings.ToLower(strings.TrimSpace(key))
	p.mu.Lock()
	if current, ok := p.leased[key]; ok {
		delete(p.leased, key)
		if p.reuseReleased {
			p.available = append(p.available, current)
		} else {
			delete(p.cooldownUntil, current.ID)
		}
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

func (p *validationProxyLeasePool) sourceExhaustedAndEmpty() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sourceExhausted && len(p.available) == 0 && len(p.leased) == 0
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
	flag.StringVar(&cfg.proxyFilePath, "proxy-file", "", "newline-delimited proxy URLs or host:port values; overrides database validation proxies")
	flag.StringVar(&cfg.proxyURL, "proxy-url", "", "HTTP(S) endpoint returning newline-delimited proxy URLs, host:port, or host:port:user:password values")
	flag.IntVar(&cfg.proxyBatchSize, "proxy-batch-size", 1000, "number of proxies requested from --proxy-url via its num or ips parameter")
	flag.IntVar(&cfg.proxyRefillThreshold, "proxy-refill-threshold", 300, "refill --proxy-url when fewer than this many unused proxies remain")
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
	flag.IntVar(&cfg.credentialTypeRPS, "credential-type-rps", 5, "CMD-wide GetCredentialType requests per second; zero disables the gate")
	flag.IntVar(&cfg.credentialTypeBurst, "credential-type-burst", 1, "maximum immediate GetCredentialType burst")
	flag.BoolVar(&cfg.retryAllErrors, "retry-all-errors", false, "on checkpoint resume, retry all error.txt entries instead of only 429.txt")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config) error {
	if cfg.concurrency < 1 || cfg.concurrency > 500 || cfg.pendingCap < cfg.concurrency || cfg.pendingCap > 10000 || cfg.chunkSize < 1 || cfg.chunkSize > 5000 || cfg.offset < 0 || cfg.limit < 0 || cfg.stage1Retries < 1 || cfg.stage1Retries > 5 || cfg.stage2Retries < 1 || cfg.stage2Retries > 5 || cfg.stage1Timeout < time.Minute || cfg.stage2Timeout < time.Minute || cfg.credentialTypeRPS < 0 || cfg.credentialTypeRPS > 1000 || cfg.credentialTypeBurst < 1 || cfg.credentialTypeBurst > 100 {
		return errors.New("invalid command limits")
	}
	if strings.TrimSpace(cfg.proxyFilePath) != "" && strings.TrimSpace(cfg.proxyURL) != "" {
		return errors.New("--proxy-file and --proxy-url are mutually exclusive")
	}
	if strings.TrimSpace(cfg.proxyURL) != "" && (cfg.proxyBatchSize < 1 || cfg.proxyBatchSize > 10000 || cfg.proxyRefillThreshold < 1 || cfg.proxyRefillThreshold >= cfg.proxyBatchSize) {
		return errors.New("invalid dynamic proxy limits")
	}
	runtime, err := openRuntime(ctx, cfg.proxyFilePath, cfg.proxyURL, cfg.proxyBatchSize, cfg.proxyRefillThreshold)
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
	var previousErrors map[string]struct{}
	var previousRateLimited map[string]struct{}
	var resumeSkippedErrors map[string]struct{}
	if !found {
		previousSuccess, err = loadEmailSet(filepath.Join(filepath.Dir(cfg.errorPath), "success.txt"))
		if err != nil {
			return err
		}
		emails, skipped, err := loadEmails(cfg.filePath, cfg.offset, cfg.limit, previousSuccess)
		if err != nil {
			return err
		}
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
		shuffleManifestForRecoveryFairness(manifest)
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
		previousErrors, err = loadEmailSet(cfg.errorPath)
		if err != nil {
			return err
		}
		previousRateLimited, err = loadEmailSet(filepath.Join(filepath.Dir(cfg.errorPath), "429.txt"))
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
	skippedSuccess := 0
	for _, item := range manifest {
		if _, skipped := previousSuccess[item.Email]; skipped {
			skippedSuccess++
		}
	}
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
	} else if err := recoverAbandonedValidations(ctx, conn, manifest[:state.FreezeOffset], cfg.chunkSize, resumeSkippedErrors, previousSuccess); err != nil {
		return err
	}

	result := newTracker(cfg.errorPath, previousSuccess, previousErrors, previousRateLimited)
	stage1Jobs, stage2Jobs, err := classifyJobs(ctx, conn, manifest[:state.FreezeOffset], cfg.chunkSize, result, resumeSkippedErrors, previousSuccess)
	if err != nil {
		return err
	}
	log.Printf("dispatch frozen=%d/%d stage1=%d stage2=%d already_complete=%d skipped_success=%d rejected=%d", state.FreezeOffset, len(manifest), len(stage1Jobs), len(stage2Jobs), result.success.Load(), skippedSuccess, result.failure.Load())

	stage1Manifest := collectStage1Manifest(manifest, state.FreezeOffset, stage1Jobs, resumeSkippedErrors, previousSuccess)
	if len(stage1Manifest) > 0 {
		if strings.TrimSpace(cfg.fallbackCredentialsPath) != "" {
			runtime.fallbackOAuth, err = loadFallbackOAuthCredentials(cfg.fallbackCredentialsPath, stage1Manifest, previousSuccess, resumeSkippedErrors)
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
	}

	flushCtx, stopFlush := context.WithCancel(context.Background())
	flushDone := make(chan struct{})
	go result.flushLoop(flushCtx, flushDone)
	throughputCtx, stopThroughput := context.WithCancel(context.Background())
	throughputDone := make(chan struct{})
	go result.throughputLoop(throughputCtx, throughputDone)

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	if cfg.credentialTypeRPS > 0 {
		limiter := rate.NewLimiter(rate.Limit(cfg.credentialTypeRPS), cfg.credentialTypeBurst)
		workCtx = msacl.WithCredentialTypeRateLimiter(workCtx, limiter.Wait)
		log.Printf("credential_type_rate_gate rps=%d burst=%d", cfg.credentialTypeRPS, cfg.credentialTypeBurst)
	}
	runState := state
	stage1Input := make(chan manifestRecord, cfg.pendingCap)
	stage1Ch := make(chan manifestRecord)
	stage1Completions := make(chan stage1Completion, cfg.pendingCap)
	stage2Input := make(chan manifestRecord, cfg.pendingCap)
	stage2Ch := make(chan manifestRecord, cfg.pendingCap)
	historyTrigger := newCommandHistoryTrigger(stage2Input)
	runtime.validation.SetMicrosoftHistoryScanTrigger(historyTrigger)
	stage1Slots := make(chan struct{}, cfg.pendingCap)
	var stage1Workers sync.WaitGroup
	var stage2Workers sync.WaitGroup
	var stage2Producers sync.WaitGroup
	go dispatchStage1ByRecoveryKey(workCtx, stage1Input, stage1Completions, stage1Ch)
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
				activeKey := item.RecoveryKey
				if item.CheckRecoveryLease {
					leaseActive, checkErr := recoveryDispatchLeaseActive(workCtx, runtime, item.RecoveryKey)
					if checkErr != nil {
						log.Printf("recovery_dispatch_lease_check_failed resource_id=%d error=%s", item.ResourceID, checkErr)
					}
					if checkErr != nil || leaseActive {
						result.recoveryBusy.Add(1)
						select {
						case <-workCtx.Done():
							return
						case stage1Completions <- stage1Completion{
							Item: item, ActiveKey: activeKey,
							RetryAt: time.Now().Add(recoveryMailboxBusyRetryDelay), Retry: true,
						}:
						}
						continue
					}
					item.CheckRecoveryLease = false
				}
				historyTrigger.bind(item)
				needsHistory, complete, err := processValidation(workCtx, runtime, runState, cfg, item)
				historyTrigger.unbind(item.ResourceID)
				busy := errors.Is(err, errRecoveryMailboxBusy)
				rateLimited := err != nil && validationFailureWasRateLimited(workCtx, runtime, item.ResourceID)
				if rateLimited {
					runtime.validationProxies.CooldownLease(item.Email)
				}
				runtime.validationProxies.Release(item.Email)
				result.stage1.Add(1)
				if err != nil && runtime.validationProxies.sourceExhaustedAndEmpty() {
					<-stage1Slots
					cancelWork()
					return
				}
				if busy {
					result.recoveryBusy.Add(1)
					item = refreshRecoveryDispatchKey(context.WithoutCancel(ctx), runtime, item)
					item.CheckRecoveryLease = true
					select {
					case <-workCtx.Done():
						return
					case stage1Completions <- stage1Completion{
						Item: item, ActiveKey: activeKey,
						RetryAt: time.Now().Add(recoveryMailboxBusyRetryDelay), Retry: true,
					}:
					}
					continue
				}
				<-stage1Slots
				select {
				case <-workCtx.Done():
					return
				case stage1Completions <- stage1Completion{Item: item, ActiveKey: activeKey}:
				}
				switch {
				case err != nil:
					result.fail(item.Email)
					if rateLimited {
						result.markRateLimited(item.Email)
					}
				case complete:
					result.succeed(item.Email)
				case needsHistory:
					if scheduleErr := historyTrigger.enqueue(workCtx, item); scheduleErr != nil {
						result.fail(item.Email)
					}
				default:
					result.fail(item.Email)
				}
			}
		}()
	}

	freezeDone := make(chan error, 1)
	go func() {
		err := freezeAndFeed(workCtx, conn, manifest, stage1Jobs, cfg, &state, result, stage1Slots, stage1Input, resumeSkippedErrors, previousSuccess)
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
	processed := int(result.success.Load() + result.failure.Load())
	accounted := processed + skippedSuccess
	if accounted != len(manifest) {
		return fmt.Errorf("batch stopped with %d of %d resources accounted", accounted, len(manifest))
	}
	if err := writeAudit(ctx, conn, state, "completed", int(result.success.Load())+skippedSuccess); err != nil {
		return err
	}
	state.Phase = phaseDone
	if err := saveCheckpoint(cfg.statePath, &state); err != nil {
		return err
	}
	elapsed := time.Since(state.StartedAt)
	log.Printf("completed total=%d succeeded=%d failed=%d skipped_success=%d rate_limited=%d recovery_mailbox_busy_events=%d elapsed=%s average_per_minute=%.2f", len(manifest), result.success.Load(), result.failure.Load(), skippedSuccess, result.rateLimitedCount(), result.recoveryBusy.Load(), elapsed.Round(time.Second), float64(processed)/max(elapsed.Minutes(), 1.0/60.0))
	return nil
}

func openRuntime(ctx context.Context, proxyFilePath, proxyURL string, proxyBatchSize, proxyRefillThreshold int) (*commandRuntime, error) {
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
	var validationProxies *validationProxyLeasePool
	switch {
	case strings.TrimSpace(proxyFilePath) != "":
		routes, err := loadValidationProxyFile(proxyFilePath)
		if err != nil {
			return fail(err)
		}
		validationProxies = newValidationProxyLeasePool(nil, routes)
		log.Printf("validation_proxy_source kind=file routes=%d", len(routes))
	case strings.TrimSpace(proxyURL) != "":
		loader, err := newValidationProxyURLLoader(proxyURL, proxyBatchSize)
		if err != nil {
			return fail(err)
		}
		routes, err := loader(ctx)
		if err != nil {
			return fail(err)
		}
		validationProxies = newRefillableValidationProxyLeasePool(routes, loader, proxyRefillThreshold)
		log.Printf("validation_proxy_source kind=url initial_routes=%d batch_size=%d refill_threshold=%d", len(routes), proxyBatchSize, proxyRefillThreshold)
	default:
		var err error
		validationProxies, err = loadValidationProxyLeasePool(ctx, p.DB, proxyModule.ProxyUseCase)
		if err != nil {
			return fail(err)
		}
	}
	validator := mailapi.NewResourceValidationAdapterWithProxyProvider(validationProxies, bindingRepo, aliasStore)
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
		platform: p, cleanup: cleanup, resources: resources, bindings: bindingRepo, validation: validation,
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
		if runtime.validationProxies.sourceExhaustedAndEmpty() {
			if err != nil {
				releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				releaseErr := runtime.validation.ReleaseDispatch(releaseCtx, task)
				releaseCancel()
				if releaseErr != nil {
					log.Printf("validation_proxy_source_release_failed resource_id=%d error=%s", item.ResourceID, releaseErr)
				}
			}
			return false, false, errValidationProxySourceExhausted
		}
		if err != nil && isRecoveryMailboxBusySafeMessage(err.Error()) {
			return false, false, fmt.Errorf("%w: %s", errRecoveryMailboxBusy, strings.TrimSpace(err.Error()))
		}
		latest, loadErr := runtime.resources.FindMicrosoftByID(ctx, item.ResourceID)
		if loadErr != nil {
			lastErr = errors.Join(lastErr, loadErr)
		} else if latest != nil {
			if isRecoveryMailboxBusySafeMessage(latest.LastSafeError) {
				return false, false, fmt.Errorf("%w: %s", errRecoveryMailboxBusy, strings.TrimSpace(latest.LastSafeError))
			}
			if isRateLimitedSafeMessage(latest.LastSafeError) {
				return false, false, errors.New(latest.LastSafeError)
			}
			switch latest.Status {
			case coredomain.MicrosoftStatusIdentifying:
				return true, false, nil
			case coredomain.MicrosoftStatusNormal:
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
			return nil
		case coredomain.MicrosoftStatusAbnormal, coredomain.MicrosoftStatusDisabled, coredomain.MicrosoftStatusDeleted:
			return errors.New("old-project identification did not complete")
		case coredomain.MicrosoftStatusIdentifying:
		case coredomain.MicrosoftStatusValidating:
			lastErr = platform.ErrBackgroundExecutionDeferred
			if attempt+1 < cfg.stage2Retries {
				if err := sleepContext(ctx, 2*time.Second); err != nil {
					return err
				}
				continue
			}
			return lastErr
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
				return nil
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

func classifyJobs(ctx context.Context, conn *sql.Conn, manifest []manifestRecord, chunkSize int, result *tracker, skippedErrors, previousSuccess map[string]struct{}) ([]manifestRecord, []manifestRecord, error) {
	stage1 := make([]manifestRecord, 0, len(manifest))
	stage2 := make([]manifestRecord, 0, len(manifest)/4)
	for start := 0; start < len(manifest); start += chunkSize {
		end := min(start+chunkSize, len(manifest))
		chunk := manifest[start:end]
		ids := make([]uint64, 0, len(chunk))
		for _, item := range chunk {
			if item.Eligible && item.ResourceID != 0 {
				if _, succeeded := previousSuccess[item.Email]; succeeded {
					continue
				}
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
			if _, succeeded := previousSuccess[item.Email]; succeeded {
				continue
			}
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
				result.succeed(item.Email)
			default:
				result.fail(item.Email)
			}
		}
	}
	return stage1, stage2, nil
}

func collectStage1Manifest(manifest []manifestRecord, freezeOffset int, resumed []manifestRecord, skippedErrors, previousSuccess map[string]struct{}) []manifestRecord {
	result := append(make([]manifestRecord, 0, len(resumed)+len(manifest)-freezeOffset), resumed...)
	for _, item := range manifest[freezeOffset:] {
		if !item.Eligible || item.ResourceID == 0 {
			continue
		}
		if _, skipped := previousSuccess[item.Email]; skipped {
			continue
		}
		if _, skipped := skippedErrors[item.Email]; skipped {
			continue
		}
		result = append(result, item)
	}
	return result
}

func recoverAbandonedValidations(ctx context.Context, conn *sql.Conn, manifest []manifestRecord, chunkSize int, skippedErrors, previousSuccess map[string]struct{}) error {
	for start := 0; start < len(manifest); start += chunkSize {
		end := min(start+chunkSize, len(manifest))
		ids := make([]uint64, 0, end-start)
		for _, item := range manifest[start:end] {
			if item.Eligible && item.ResourceID != 0 {
				if _, succeeded := previousSuccess[item.Email]; succeeded {
					continue
				}
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

func loadEmails(path string, offset, limit int, excluded map[string]struct{}) ([]string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open input file: %w", err)
	}
	defer file.Close()
	emails := make([]string, 0, 700000)
	seen := make(map[string]struct{}, 700000)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	index := 0
	selected := 0
	skipped := 0
	rangeSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		email, _, hasFields := strings.Cut(line, "----")
		email = strings.ToLower(strings.TrimSpace(email))
		if index >= offset {
			rangeSeen = true
		}
		if _, skip := excluded[email]; skip {
			if index >= offset && (limit == 0 || selected < limit) {
				skipped++
			}
			index++
			continue
		}
		if hasFields {
			if strings.Count(line, "----") != 3 {
				return nil, 0, errors.New("input contains an invalid imported resource record")
			}
		}
		if email == "" || strings.Count(email, "@") != 1 || strings.ContainsAny(email, "\r\n\t ") {
			return nil, 0, errors.New("input contains an invalid email address")
		}
		if _, ok := seen[email]; ok {
			return nil, 0, errors.New("input contains a duplicate email address")
		}
		seen[email] = struct{}{}
		selectedLine := index >= offset && (limit == 0 || selected < limit)
		if selectedLine {
			selected++
			emails = append(emails, email)
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	if !rangeSeen {
		return nil, 0, errors.New("selected input range is empty")
	}
	return emails, skipped, nil
}

func loadFallbackOAuthCredentials(path string, manifest []manifestRecord, skippedSuccess, skippedErrors map[string]struct{}) (map[string]fallbackOAuthCredential, error) {
	wanted := make(map[string]struct{}, len(manifest))
	for _, item := range manifest {
		if item.Eligible && item.ResourceID != 0 {
			if _, ignored := skippedSuccess[item.Email]; ignored {
				continue
			}
			if _, ignored := skippedErrors[item.Email]; ignored {
				continue
			}
			wanted[item.Email] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return map[string]fallbackOAuthCredential{}, nil
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
		line := scanner.Text()
		emailPart, _, _ := strings.Cut(line, "----")
		email := strings.ToLower(strings.TrimSpace(emailPart))
		if _, ok := wanted[email]; !ok {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) != 4 && len(parts) != 5 {
			return nil, errors.New("fallback credential file contains an invalid imported resource record")
		}
		address, parseErr := mail.ParseAddress(email)
		if parseErr != nil || address.Address != email {
			return nil, errors.New("fallback credential file contains an invalid email address")
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
	mr.id, er.owner_user_id, LOWER(TRIM(mr.email_address)),
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
			if err := rows.Scan(&item.ResourceID, &item.OwnerUserID, &item.Email, &item.ValidationGen, &item.CredentialRevision); err != nil {
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

// shuffleManifestForRecoveryFairness prevents a contiguous import block that
// shares one masked recovery mailbox from monopolizing the bounded 1000-item
// pending window. The durable manifest preserves this shuffled order for every
// later checkpoint resume; exact same-key serialization still happens in the
// dispatcher.
func shuffleManifestForRecoveryFairness(records []manifestRecord) {
	if len(records) < 2 {
		return
	}
	rand.Shuffle(len(records), func(i, j int) {
		records[i], records[j] = records[j], records[i]
	})
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
SET status = 'pending',
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
	skippedErrors, previousSuccess map[string]struct{},
) error {
	defer close(output)
	resumed := append([]manifestRecord(nil), resume...)
	if err := attachRecoveryDispatchKeys(ctx, conn, resumed); err != nil {
		return err
	}
	for _, item := range resumed {
		if _, succeeded := previousSuccess[item.Email]; succeeded {
			continue
		}
		if _, skipped := skippedErrors[item.Email]; skipped {
			result.fail(item.Email)
			continue
		}
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
			if _, succeeded := previousSuccess[item.Email]; succeeded {
				continue
			}
			if _, skipped := skippedErrors[item.Email]; skipped {
				result.fail(item.Email)
				continue
			}
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
			if _, succeeded := previousSuccess[item.Email]; succeeded {
				next++
				continue
			}
			if _, skipped := skippedErrors[item.Email]; skipped {
				result.fail(item.Email)
				next++
				continue
			}
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
			if err := attachRecoveryDispatchKeys(ctx, conn, batch); err != nil {
				releaseSlots(slots, len(batch))
				return err
			}
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

func attachRecoveryDispatchKeys(ctx context.Context, conn *sql.Conn, records []manifestRecord) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(records))
	seen := make(map[uint]struct{}, len(records))
	for _, item := range records {
		if item.ResourceID == 0 {
			continue
		}
		if _, exists := seen[item.ResourceID]; exists {
			continue
		}
		seen[item.ResourceID] = struct{}{}
		ids = append(ids, uint64(item.ResourceID))
	}
	addresses := make(map[uint]string, len(ids))
	accounts := make(map[uint]string, len(ids))
	for start := 0; start < len(ids); start += recoveryDispatchQueryChunkSize {
		end := min(start+recoveryDispatchQueryChunkSize, len(ids))
		chunk := ids[start:end]
		rows, err := conn.QueryContext(ctx, `SELECT resource_id, account_email, binding_address, status
FROM microsoft_binding_mailboxes
WHERE resource_id IN (`+sqlPlaceholders(len(chunk))+`)`, uint64Args(chunk)...)
		if err != nil {
			return fmt.Errorf("load recovery dispatch keys: %w", err)
		}
		for rows.Next() {
			var resourceID uint
			var account, address, status string
			if err := rows.Scan(&resourceID, &account, &address, &status); err != nil {
				rows.Close()
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(status), string(maildomain.MicrosoftBindingExpired)) {
				addresses[resourceID] = address
				accounts[resourceID] = account
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for index := range records {
		item := records[index]
		bindingAddress := addresses[item.ResourceID]
		if account := strings.TrimSpace(accounts[item.ResourceID]); !strings.EqualFold(account, item.Email) {
			bindingAddress = ""
		}
		records[index].RecoveryKey = recoveryDispatchKey(item, bindingAddress)
	}
	return nil
}

func recoveryDispatchKey(item manifestRecord, bindingAddress string) string {
	if mailbox := normalizeConcreteRecoveryDispatchAddress(bindingAddress); mailbox != "" {
		if msacl.UsesActiveAuxiliaryDomain(mailbox) {
			return mailbox
		}
		return recoveryDispatchFallbackKey(item)
	}
	if mask := normalizeMaskedRecoveryDispatchAddress(bindingAddress); mask != "" {
		if !msacl.UsesActiveAuxiliaryDomain(mask) {
			return recoveryDispatchFallbackKey(item)
		}
		if inferred := normalizeConcreteRecoveryDispatchAddress(msacl.InferBindingAddress(item.Email, mask)); inferred != "" {
			return inferred
		}
		return mask
	}
	return recoveryDispatchFallbackKey(item)
}

func recoveryDispatchFallbackKey(item manifestRecord) string {
	if item.ResourceID != 0 {
		return fmt.Sprintf("resource:%d", item.ResourceID)
	}
	return "email:" + strings.ToLower(strings.TrimSpace(item.Email))
}

func normalizeConcreteRecoveryDispatchAddress(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.Contains(value, "*") || strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(strings.TrimSpace(address.Address), value) {
		return ""
	}
	local, domain, ok := strings.Cut(strings.ToLower(address.Address), "@")
	domain = strings.Trim(domain, ".")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return ""
	}
	return local + "@" + domain
}

func normalizeMaskedRecoveryDispatchAddress(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || domain == "" || !strings.Contains(local, "*") || strings.Contains(domain, "@") || strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return local + "@" + strings.Trim(domain, ".")
}

func refreshRecoveryDispatchKey(ctx context.Context, runtime *commandRuntime, item manifestRecord) manifestRecord {
	if runtime == nil || runtime.bindings == nil || item.ResourceID == 0 {
		return item
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(baseCtx), recoveryDispatchRefreshTimeout)
	defer cancel()
	bindings, err := runtime.bindings.FindByResourceIDs(refreshCtx, []uint{item.ResourceID})
	if err != nil {
		log.Printf("recovery_dispatch_key_refresh_failed resource_id=%d error=%s", item.ResourceID, err)
		return item
	}
	binding, ok := bindings[item.ResourceID]
	if ok && binding.Status != maildomain.MicrosoftBindingExpired && strings.EqualFold(strings.TrimSpace(binding.AccountEmail), item.Email) {
		item.RecoveryKey = recoveryDispatchKey(item, binding.BindingAddress)
	}
	return item
}

// recoveryDispatchLeaseActive is used only after a task has already observed
// a local mailbox-busy result. It is deliberately a cheap database preflight:
// while the fenced lease remains, the retry stays in the dispatcher and never
// acquires a proxy or starts another Microsoft session.
func recoveryDispatchLeaseActive(ctx context.Context, runtime *commandRuntime, key string) (bool, error) {
	if runtime == nil || runtime.platform == nil || runtime.platform.DB == nil {
		return false, nil
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if normalizeConcreteRecoveryDispatchAddress(key) == "" && normalizeMaskedRecoveryDispatchAddress(key) == "" {
		return false, nil
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(baseCtx, recoveryDispatchRefreshTimeout)
	defer cancel()
	var row struct {
		LeaseUntil time.Time `gorm:"column:lease_until"`
	}
	err := runtime.platform.DB.WithContext(checkCtx).
		Table("microsoft_binding_recovery_leases").
		Select("lease_until").
		Where("normalized_mask = ? AND lease_until > ?", key, time.Now().UTC()).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return false, fmt.Errorf("query recovery mailbox lease: %w", err)
	}
	return !row.LeaseUntil.IsZero() && row.LeaseUntil.After(time.Now().UTC()), nil
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
	rows, err := conn.QueryContext(ctx, `SELECT id, status, validation_generation, credential_revision
FROM microsoft_resources WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, uint64Args(ids)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint
		var status string
		var state databaseState
		if err := rows.Scan(&id, &status, &state.ValidationGeneration, &state.CredentialRevision); err != nil {
			rows.Close()
			return nil, err
		}
		state.Status = coredomain.MicrosoftResourceStatus(status)
		result[id] = state
	}
	return result, rows.Close()
}

func markAbnormal(ctx context.Context, db *gorm.DB, resourceID uint, safeError string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Exec(`UPDATE microsoft_resources
SET status = 'abnormal', validation_generation = validation_generation + 1,
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

func isRecoveryMailboxBusySafeMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "recovery_mailbox_busy") ||
		strings.Contains(message, "recovery mailbox is already processing another verification code") ||
		strings.Contains(message, "recovery mailbox is busy") ||
		strings.Contains(message, "辅助邮箱忙")
}

func isRateLimitedSafeMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "rate limit") ||
		strings.Contains(message, "rate_limited") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "429") ||
		strings.Contains(message, "频率受限")
}

func newTracker(errorOut string, previousSuccess, previousErrors, previousRateLimited map[string]struct{}) *tracker {
	succeeded := make(map[string]struct{}, len(previousSuccess))
	for email := range previousSuccess {
		succeeded[email] = struct{}{}
	}
	failed := make(map[string]struct{}, len(previousErrors))
	for email := range previousErrors {
		if _, completed := succeeded[email]; !completed {
			failed[email] = struct{}{}
		}
	}
	rateLimited := make(map[string]struct{}, len(previousRateLimited))
	for email := range previousRateLimited {
		if _, failed := failed[email]; failed {
			rateLimited[email] = struct{}{}
		}
	}
	return &tracker{
		failed: failed, rateLimited: rateLimited, succeeded: succeeded, seen: make(map[string]struct{}),
		errorOut: errorOut, rateLimitOut: filepath.Join(filepath.Dir(errorOut), "429.txt"), successOut: filepath.Join(filepath.Dir(errorOut), "success.txt"),
	}
}

func (t *tracker) succeed(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.succeeded[email]; ok {
		return
	}
	if _, ok := t.seen[email]; ok {
		return
	}
	t.seen[email] = struct{}{}
	delete(t.failed, email)
	delete(t.rateLimited, email)
	t.succeeded[email] = struct{}{}
	t.success.Add(1)
}

func (t *tracker) fail(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.succeeded[email]; ok {
		return
	}
	if _, ok := t.seen[email]; ok {
		return
	}
	t.seen[email] = struct{}{}
	delete(t.rateLimited, email)
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
	var previousStage1, previousStage2, previousRecoveryBusy int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stage1 := t.stage1.Load()
			stage2 := t.stage2.Load()
			recoveryBusy := t.recoveryBusy.Load()
			log.Printf("one_minute_throughput stage1=%d stage2=%d recovery_mailbox_busy=%d total_success=%d total_failed=%d", stage1-previousStage1, stage2-previousStage2, recoveryBusy-previousRecoveryBusy, t.success.Load(), t.failure.Load())
			previousStage1, previousStage2, previousRecoveryBusy = stage1, stage2, recoveryBusy
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
		if _, err := fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%t\n", item.Email, item.ResourceID, item.OwnerUserID, item.ValidationGen, item.CredentialRevision, item.Eligible); err != nil {
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
		if len(parts) != 6 && len(parts) != 7 {
			return nil, errors.New("invalid manifest record")
		}
		generationIndex := 3
		if len(parts) == 7 {
			if _, err := strconv.ParseBool(parts[3]); err != nil {
				return nil, errors.New("invalid manifest value")
			}
			generationIndex = 4
		}
		resourceID, err1 := strconv.ParseUint(parts[1], 10, 64)
		ownerID, err2 := strconv.ParseUint(parts[2], 10, 64)
		generation, err3 := strconv.ParseUint(parts[generationIndex], 10, 64)
		revision, err4 := strconv.ParseUint(parts[generationIndex+1], 10, 64)
		eligible, err5 := strconv.ParseBool(parts[generationIndex+2])
		if errors.Join(err1, err2, err3, err4, err5) != nil {
			return nil, errors.New("invalid manifest value")
		}
		records = append(records, manifestRecord{Email: parts[0], ResourceID: uint(resourceID), OwnerUserID: uint(ownerID), ValidationGen: generation, CredentialRevision: revision, Eligible: eligible})
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

// dispatchStage1ByRecoveryKey keeps the global worker pool full while ensuring
// that only one queued or running task can consume a given recovery recipient.
// A concrete recipient and an unresolved mask are both valid keys; unrelated
// keys rotate fairly through readyKeys.
func dispatchStage1ByRecoveryKey(
	ctx context.Context,
	input <-chan manifestRecord,
	completions <-chan stage1Completion,
	output chan<- manifestRecord,
) {
	defer close(output)
	buckets := make(map[string][]manifestRecord)
	active := make(map[string]struct{})
	readySet := make(map[string]struct{})
	readyKeys := make([]string, 0)
	blockedUntil := make(map[string]time.Time)

	addReadyKey := func(key string) {
		if key == "" || len(buckets[key]) == 0 {
			return
		}
		if until, blocked := blockedUntil[key]; blocked {
			if time.Now().Before(until) {
				return
			}
			delete(blockedUntil, key)
		}
		if _, running := active[key]; running {
			return
		}
		if _, queued := readySet[key]; queued {
			return
		}
		readySet[key] = struct{}{}
		readyKeys = append(readyKeys, key)
	}
	removeReadyKey := func(key string) {
		if _, queued := readySet[key]; !queued {
			return
		}
		delete(readySet, key)
		for index, readyKey := range readyKeys {
			if readyKey != key {
				continue
			}
			copy(readyKeys[index:], readyKeys[index+1:])
			readyKeys[len(readyKeys)-1] = ""
			readyKeys = readyKeys[:len(readyKeys)-1]
			return
		}
	}
	enqueueReady := func(item manifestRecord, front bool) {
		if strings.TrimSpace(item.RecoveryKey) == "" {
			item.RecoveryKey = recoveryDispatchKey(item, "")
		}
		key := item.RecoveryKey
		if front {
			queue := append(buckets[key], manifestRecord{})
			copy(queue[1:], queue[:len(queue)-1])
			queue[0] = item
			buckets[key] = queue
		} else {
			buckets[key] = append(buckets[key], item)
		}
		addReadyKey(key)
	}
	unblockDue := func(now time.Time) {
		for key, until := range blockedUntil {
			if now.Before(until) {
				continue
			}
			delete(blockedUntil, key)
			addReadyKey(key)
		}
	}
	blockKey := func(key string, until time.Time) {
		if key == "" || len(buckets[key]) == 0 {
			return
		}
		if previous, exists := blockedUntil[key]; !exists || until.After(previous) {
			blockedUntil[key] = until
		}
		removeReadyKey(key)
	}

	inputChannel := input
	for {
		now := time.Now()
		unblockDue(now)
		// Keep the dispatcher self-healing if a future completion/input path
		// forgets to re-add a bucket. This is only a defensive scan; the normal
		// path maintains readySet/readyKeys incrementally.
		if len(readyKeys) == 0 && len(active) == 0 && len(blockedUntil) == 0 && len(buckets) > 0 {
			for key := range buckets {
				addReadyKey(key)
			}
		}
		if inputChannel == nil && len(active) == 0 && len(buckets) == 0 {
			return
		}

		var sendChannel chan<- manifestRecord
		var next manifestRecord
		var nextKey string
		if len(readyKeys) > 0 {
			nextKey = readyKeys[0]
			next = buckets[nextKey][0]
			sendChannel = output
		}

		var timer *time.Timer
		var timerChannel <-chan time.Time
		var earliest time.Time
		for key, until := range blockedUntil {
			if len(buckets[key]) == 0 {
				delete(blockedUntil, key)
				continue
			}
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
		}
		if !earliest.IsZero() {
			wait := time.Until(earliest)
			if wait < 0 {
				wait = 0
			}
			timer = time.NewTimer(wait)
			timerChannel = timer.C
		}

		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case item, ok := <-inputChannel:
			if !ok {
				inputChannel = nil
			} else {
				enqueueReady(item, false)
			}
		case completion := <-completions:
			delete(active, completion.ActiveKey)
			if completion.Retry {
				if strings.TrimSpace(completion.Item.RecoveryKey) == "" {
					completion.Item.RecoveryKey = completion.ActiveKey
				}
				if completion.RetryAt.IsZero() {
					completion.RetryAt = time.Now().Add(recoveryMailboxBusyRetryDelay)
				}
				enqueueReady(completion.Item, true)
				blockKey(completion.ActiveKey, completion.RetryAt)
				blockKey(completion.Item.RecoveryKey, completion.RetryAt)
			} else {
				addReadyKey(completion.ActiveKey)
			}
		case sendChannel <- next:
			readyKeys[0] = ""
			readyKeys = readyKeys[1:]
			delete(readySet, nextKey)
			queue := buckets[nextKey]
			queue[0] = manifestRecord{}
			queue = queue[1:]
			if len(queue) == 0 {
				delete(buckets, nextKey)
			} else {
				buckets[nextKey] = queue
			}
			active[nextKey] = struct{}{}
		case <-timerChannel:
			unblockDue(time.Now())
		}
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
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
