package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type bindingDomainListerFunc func(context.Context) ([]string, error)

func (f bindingDomainListerFunc) ListBindingDomains(ctx context.Context) ([]string, error) {
	return f(ctx)
}

func TestAuxiliaryDomainSeedTimeoutAllowsNextRound(t *testing.T) {
	const timeout = 20 * time.Millisecond
	started := time.Now()
	refreshAuxiliaryDomainsWithin(context.Background(), bindingDomainListerFunc(func(ctx context.Context) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), timeout)

	nextRoundRan := false
	refreshAuxiliaryDomainsWithin(context.Background(), bindingDomainListerFunc(func(context.Context) ([]string, error) {
		nextRoundRan = true
		return nil, nil
	}), timeout)

	require.True(t, nextRoundRan)
	require.Less(t, time.Since(started), time.Second)
}

func TestAuxiliaryDomainSeedStopsOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshAuxiliaryDomainsWithin(ctx, bindingDomainListerFunc(func(callCtx context.Context) ([]string, error) {
			close(started)
			<-callCtx.Done()
			return nil, callCtx.Err()
		}), time.Minute)
	}()

	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher seed did not stop after parent cancellation")
	}
}
