package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/openapi/domain"
)

const (
	apiKeyRuntimeMetaTTL       = 30 * time.Second
	apiKeyRuntimeFlushInterval = 5 * time.Second
	apiKeyRPMWindow            = time.Minute
)

type apiKeyRuntime struct {
	repo            Repository
	concurrencyGate APIKeyConcurrencyGate
	now             func() time.Time

	mu      sync.RWMutex
	byPlain map[string]*apiKeyState
	byID    map[uint]*apiKeyState

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type apiKeyState struct {
	mu sync.Mutex

	plain    string
	meta     domain.APIKey
	loadedAt time.Time

	active int

	quotaDelta int64
	lastUsedAt time.Time
	recent     []time.Time
}

func newAPIKeyRuntime(repo Repository, now func() time.Time, concurrencyGates ...APIKeyConcurrencyGate) *apiKeyRuntime {
	rt := &apiKeyRuntime{
		repo:    repo,
		now:     now,
		byPlain: make(map[string]*apiKeyState),
		byID:    make(map[uint]*apiKeyState),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if len(concurrencyGates) > 0 {
		rt.concurrencyGate = concurrencyGates[0]
	}
	go rt.flushLoop()
	return rt
}

func (rt *apiKeyRuntime) begin(ctx context.Context, plain string, leaseIDs ...string) (*domain.APIKey, error) {
	state, err := rt.stateForPlain(ctx, plain)
	if err != nil {
		return nil, err
	}
	now := rt.now()
	state.mu.Lock()
	defer state.mu.Unlock()
	if now.Sub(state.loadedAt) >= apiKeyRuntimeMetaTTL {
		if err := rt.reloadStateLocked(ctx, state); err != nil {
			return nil, err
		}
	}
	meta := state.meta
	if !meta.Enabled {
		return nil, domain.ErrAPIKeyDisabled
	}
	ownerRole, active, groupConcurrencyLimit, err := rt.repo.GetAPIKeyOwnerAccess(ctx, meta.UserID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, domain.ErrAPIKeyDisabled
	}
	meta.OwnerRole = ownerRole
	state.meta.OwnerRole = ownerRole
	if meta.ExpireAt != nil && !meta.ExpireAt.After(now) {
		return nil, domain.ErrAPIKeyExpired
	}
	if meta.QuotaLimit != nil && meta.QuotaUsed+state.quotaDelta >= *meta.QuotaLimit {
		return nil, domain.ErrAPIKeyQuotaExceeded
	}
	limit := effectiveAPIKeyConcurrency(meta.ConcurrencyLimit, groupConcurrencyLimit)
	globalActive := 0
	if rt.concurrencyGate == nil {
		if state.active >= limit {
			return nil, domain.ErrAPIKeyConcurrencyLimit
		}
	} else {
		leaseID := ""
		if len(leaseIDs) > 0 {
			leaseID = leaseIDs[0]
		}
		var acquired bool
		globalActive, acquired, err = rt.concurrencyGate.Acquire(ctx, meta.UserID, meta.ID, limit, leaseID)
		if err != nil {
			return nil, err
		}
		if !acquired {
			return nil, domain.ErrAPIKeyConcurrencyLimit
		}
	}
	state.active++
	state.quotaDelta++
	state.lastUsedAt = now
	if rt.concurrencyGate == nil {
		state.trimRecentLocked(now.Add(-apiKeyRPMWindow))
		state.recent = append(state.recent, now)
	}
	result := state.overlayLocked(now)
	if rt.concurrencyGate != nil {
		result.ActiveRequests = globalActive
	}
	return cloneAPIKey(result), nil
}

func (rt *apiKeyRuntime) finish(keyID uint) {
	_ = rt.finishRequest(context.Background(), 0, keyID, "")
}

func (rt *apiKeyRuntime) finishRequest(ctx context.Context, userID, keyID uint, leaseID string) error {
	state := rt.stateForID(keyID)
	if state != nil {
		state.mu.Lock()
		if userID == 0 {
			userID = state.meta.UserID
		}
		if state.active > 0 {
			state.active--
		}
		state.mu.Unlock()
	}
	if rt.concurrencyGate != nil {
		return rt.concurrencyGate.Release(ctx, userID, keyID, leaseID)
	}
	return nil
}

func (rt *apiKeyRuntime) realtimeUsage(ctx context.Context, userID uint) (int64, int64, error) {
	if rt.concurrencyGate != nil {
		return rt.concurrencyGate.RealtimeUsage(ctx, userID)
	}
	now := rt.now()
	cutoff := now.Add(-apiKeyRPMWindow)
	var active, rpm int64
	rt.mu.RLock()
	states := make([]*apiKeyState, 0, len(rt.byID))
	for _, state := range rt.byID {
		states = append(states, state)
	}
	rt.mu.RUnlock()
	for _, state := range states {
		state.mu.Lock()
		if state.meta.UserID == userID {
			state.trimRecentLocked(cutoff)
			active += int64(state.active)
			rpm += int64(len(state.recent))
		}
		state.mu.Unlock()
	}
	return active, rpm, nil
}

func (state *apiKeyState) trimRecentLocked(cutoff time.Time) {
	first := 0
	for first < len(state.recent) && !state.recent[first].After(cutoff) {
		first++
	}
	state.recent = state.recent[first:]
}

func (rt *apiKeyRuntime) updateKey(key domain.APIKey) {
	state := rt.stateForID(key.ID)
	if state == nil {
		return
	}
	state.mu.Lock()
	key.OwnerRole = state.meta.OwnerRole
	state.meta = key
	state.loadedAt = rt.now()
	state.mu.Unlock()
}

func (rt *apiKeyRuntime) invalidateKey(keyID uint) {
	rt.mu.Lock()
	if state := rt.byID[keyID]; state != nil {
		delete(rt.byPlain, state.plain)
		delete(rt.byID, keyID)
	}
	rt.mu.Unlock()
}

func (rt *apiKeyRuntime) overlayKeys(items []domain.APIKey) {
	for i := range items {
		state := rt.stateForID(items[i].ID)
		if state == nil {
			continue
		}
		state.mu.Lock()
		items[i].ActiveRequests = state.active
		items[i].QuotaUsed += state.quotaDelta
		if !state.lastUsedAt.IsZero() {
			lastUsedAt := state.lastUsedAt
			items[i].LastUsedAt = &lastUsedAt
		}
		state.mu.Unlock()
	}
}

func (rt *apiKeyRuntime) quotaDeltaForUser(userID uint) int64 {
	var total int64
	rt.mu.RLock()
	states := make([]*apiKeyState, 0, len(rt.byID))
	for _, state := range rt.byID {
		states = append(states, state)
	}
	rt.mu.RUnlock()
	for _, state := range states {
		state.mu.Lock()
		if state.meta.UserID == userID {
			total += state.quotaDelta
		}
		state.mu.Unlock()
	}
	return total
}

func (rt *apiKeyRuntime) stateForPlain(ctx context.Context, plain string) (*apiKeyState, error) {
	rt.mu.RLock()
	state := rt.byPlain[plain]
	rt.mu.RUnlock()
	if state != nil {
		return state, nil
	}
	key, err := rt.repo.FindAPIKeyByPlain(ctx, plain)
	if err != nil {
		return nil, err
	}
	state = &apiKeyState{
		plain:    plain,
		meta:     *key,
		loadedAt: rt.now(),
	}
	rt.mu.Lock()
	existing := rt.byPlain[plain]
	if existing != nil {
		rt.mu.Unlock()
		return existing, nil
	}
	rt.byPlain[plain] = state
	rt.byID[key.ID] = state
	rt.mu.Unlock()
	return state, nil
}

func (rt *apiKeyRuntime) stateForID(keyID uint) *apiKeyState {
	rt.mu.RLock()
	state := rt.byID[keyID]
	rt.mu.RUnlock()
	return state
}

func (rt *apiKeyRuntime) reloadStateLocked(ctx context.Context, state *apiKeyState) error {
	key, err := rt.repo.FindAPIKeyByPlain(ctx, state.plain)
	if err != nil {
		return err
	}
	state.meta = *key
	state.loadedAt = rt.now()
	return nil
}

func (state *apiKeyState) overlayLocked(now time.Time) domain.APIKey {
	meta := state.meta
	meta.ActiveRequests = state.active
	meta.QuotaUsed += state.quotaDelta
	if !state.lastUsedAt.IsZero() {
		lastUsedAt := state.lastUsedAt
		meta.LastUsedAt = &lastUsedAt
	} else if !now.IsZero() {
		meta.LastUsedAt = &now
	}
	return meta
}

func (rt *apiKeyRuntime) flushLoop() {
	defer close(rt.done)
	ticker := time.NewTicker(apiKeyRuntimeFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rt.stop:
			_ = rt.flush(context.Background())
			return
		case <-ticker.C:
			_ = rt.flush(context.Background())
		}
	}
}

func (rt *apiKeyRuntime) close(ctx context.Context) error {
	rt.stopOnce.Do(func() {
		close(rt.stop)
	})
	select {
	case <-rt.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rt *apiKeyRuntime) flush(ctx context.Context) error {
	return rt.flushQuota(ctx)
}

func (rt *apiKeyRuntime) flushQuota(ctx context.Context) error {
	rt.mu.RLock()
	states := make([]*apiKeyState, 0, len(rt.byID))
	for _, state := range rt.byID {
		states = append(states, state)
	}
	rt.mu.RUnlock()
	var errs []error
	for _, state := range states {
		state.mu.Lock()
		delta := state.quotaDelta
		lastUsedAt := state.lastUsedAt
		if delta == 0 {
			state.mu.Unlock()
			continue
		}
		if err := rt.repo.AddAPIKeyQuotaUsed(ctx, state.meta.ID, delta, lastUsedAt); err != nil {
			errs = append(errs, err)
			state.mu.Unlock()
			continue
		}
		state.meta.QuotaUsed += delta
		if !lastUsedAt.IsZero() {
			lastUsedAtCopy := lastUsedAt
			state.meta.LastUsedAt = &lastUsedAtCopy
		}
		state.quotaDelta = 0
		state.mu.Unlock()
	}
	return errors.Join(errs...)
}

func cloneAPIKey(key domain.APIKey) *domain.APIKey {
	cloned := key
	return &cloned
}
