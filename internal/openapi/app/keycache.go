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
)

type apiKeyRuntime struct {
	repo Repository
	now  func() time.Time

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
	window apiKeySlidingWindow

	quotaDelta int64
	lastUsedAt time.Time
}

type apiKeySlidingWindow struct {
	currentBucket int64
	currentCount  int
	prevBucket    int64
	prevCount     int
}

func newAPIKeyRuntime(repo Repository, now func() time.Time) *apiKeyRuntime {
	rt := &apiKeyRuntime{
		repo:    repo,
		now:     now,
		byPlain: make(map[string]*apiKeyState),
		byID:    make(map[uint]*apiKeyState),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go rt.flushLoop()
	return rt
}

func (rt *apiKeyRuntime) begin(ctx context.Context, plain string) (*domain.APIKey, error) {
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
	ownerRole, active, err := rt.repo.GetAPIKeyOwnerAccess(ctx, meta.UserID)
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
	if state.active >= meta.ConcurrencyLimit {
		return nil, domain.ErrAPIKeyConcurrencyLimit
	}
	if meta.RateLimitPerMinute != nil && !state.window.allow(now, *meta.RateLimitPerMinute) {
		return nil, domain.ErrAPIKeyRateLimited
	}
	if meta.QuotaLimit != nil && meta.QuotaUsed+state.quotaDelta >= *meta.QuotaLimit {
		return nil, domain.ErrAPIKeyQuotaExceeded
	}
	state.active++
	state.quotaDelta++
	state.lastUsedAt = now
	return cloneAPIKey(state.overlayLocked(now)), nil
}

func (rt *apiKeyRuntime) finish(keyID uint) {
	state := rt.stateForID(keyID)
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.active > 0 {
		state.active--
	}
	state.mu.Unlock()
}

func (rt *apiKeyRuntime) invalidateAll() {
	rt.mu.Lock()
	rt.byPlain = make(map[string]*apiKeyState)
	rt.byID = make(map[uint]*apiKeyState)
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

func (w *apiKeySlidingWindow) allow(now time.Time, limit int) bool {
	if limit <= 0 {
		return true
	}
	bucket := now.Unix() / 60
	if w.currentBucket != bucket {
		if w.currentBucket == bucket-1 {
			w.prevBucket = w.currentBucket
			w.prevCount = w.currentCount
		} else {
			w.prevBucket = 0
			w.prevCount = 0
		}
		w.currentBucket = bucket
		w.currentCount = 0
	}
	elapsed := now.Unix() % 60
	effective := float64(w.currentCount)
	if w.prevBucket == bucket-1 {
		effective += float64(w.prevCount) * float64(60-elapsed) / 60.0
	}
	if effective >= float64(limit) {
		return false
	}
	w.currentCount++
	return true
}

func cloneAPIKey(key domain.APIKey) *domain.APIKey {
	cloned := key
	return &cloned
}
