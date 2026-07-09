package platform

import (
	"sync"
	"time"
)

type TTLCache[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]ttlCacheEntry[V]
	now   func() time.Time
}

type ttlCacheEntry[V any] struct {
	value    V
	expireAt time.Time
}

func NewTTLCache[K comparable, V any]() *TTLCache[K, V] {
	return &TTLCache[K, V]{
		items: make(map[K]ttlCacheEntry[V]),
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	now := c.now()
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return zero, false
	}
	if !entry.expireAt.After(now) {
		c.mu.Lock()
		if current, exists := c.items[key]; exists && !current.expireAt.After(now) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		return zero, false
	}
	return entry.value, true
}

func (c *TTLCache[K, V]) Set(key K, value V, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	c.mu.Lock()
	c.items[key] = ttlCacheEntry[V]{
		value:    value,
		expireAt: c.now().Add(ttl),
	}
	c.mu.Unlock()
}

func (c *TTLCache[K, V]) Delete(key K) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

func (c *TTLCache[K, V]) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.items = make(map[K]ttlCacheEntry[V])
	c.mu.Unlock()
}
