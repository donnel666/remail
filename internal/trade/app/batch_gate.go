package app

import (
	"context"
	"sync"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/trade/domain"
)

const (
	checkoutBatchConcurrency = 5
	checkoutBatchMaxWaiting  = 20
	checkoutBatchUnitSize    = 20
	checkoutBatchMaxUnits    = 10
)

type checkoutBatchWaiter struct {
	userID  uint
	units   int
	ready   chan struct{}
	granted bool
}

type checkoutBatchGate struct {
	mu          sync.Mutex
	active      int
	queuedUnits int
	waiting     []*checkoutBatchWaiter
	users       map[uint]struct{}
}

func newCheckoutBatchGate() *checkoutBatchGate {
	// ponytail: this process-local gate matches the single application replica;
	// use a Redis lease only when checkout runs in multiple replicas.
	return &checkoutBatchGate{users: make(map[uint]struct{})}
}

func (g *checkoutBatchGate) acquire(ctx context.Context, userID uint, quantity int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	units := (quantity + checkoutBatchUnitSize - 1) / checkoutBatchUnitSize
	g.mu.Lock()
	if _, exists := g.users[userID]; exists {
		g.mu.Unlock()
		return nil, domain.ErrCheckoutBusy
	}
	if g.active < checkoutBatchConcurrency && len(g.waiting) == 0 {
		g.active++
		g.users[userID] = struct{}{}
		g.observeLocked()
		g.mu.Unlock()
		return g.releaseFunc(userID), nil
	}
	if len(g.waiting) >= checkoutBatchMaxWaiting || g.queuedUnits+units > checkoutBatchMaxUnits {
		g.mu.Unlock()
		return nil, domain.ErrCheckoutOverloaded
	}
	waiter := &checkoutBatchWaiter{userID: userID, units: units, ready: make(chan struct{})}
	g.users[userID] = struct{}{}
	g.waiting = append(g.waiting, waiter)
	g.queuedUnits += units
	g.observeLocked()
	g.mu.Unlock()

	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			g.mu.Lock()
			g.releaseLocked(userID)
			g.observeLocked()
			g.mu.Unlock()
			return nil, err
		}
		return g.releaseFunc(userID), nil
	case <-ctx.Done():
		g.mu.Lock()
		if waiter.granted {
			g.releaseLocked(userID)
		} else {
			for i := range g.waiting {
				if g.waiting[i] == waiter {
					g.waiting = append(g.waiting[:i], g.waiting[i+1:]...)
					g.queuedUnits -= waiter.units
					break
				}
			}
			delete(g.users, userID)
		}
		g.observeLocked()
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *checkoutBatchGate) releaseFunc(userID uint) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.releaseLocked(userID)
			g.observeLocked()
			g.mu.Unlock()
		})
	}
}

func (g *checkoutBatchGate) releaseLocked(userID uint) {
	delete(g.users, userID)
	g.active--
	if len(g.waiting) == 0 {
		return
	}
	waiter := g.waiting[0]
	g.waiting = g.waiting[1:]
	g.queuedUnits -= waiter.units
	g.active++
	waiter.granted = true
	close(waiter.ready)
}

func (g *checkoutBatchGate) observeLocked() {
	platform.SetWorkloadState("checkout_batch", g.active, len(g.waiting), g.queuedUnits)
}
