package api

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

type bindingDomainListerFunc func(context.Context) ([]string, []string, error)

func (f bindingDomainListerFunc) ListBindingDomains(ctx context.Context) ([]string, []string, error) {
	return f(ctx)
}

func TestAuxiliaryDomainSeedTimeoutAllowsNextRound(t *testing.T) {
	t.Cleanup(func() { msacl.SetAuxiliaryDomains([]string{"recovery.test"}) })
	const timeout = 20 * time.Millisecond
	started := time.Now()
	refreshAuxiliaryDomainsWithin(context.Background(), bindingDomainListerFunc(func(ctx context.Context) ([]string, []string, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}), timeout)

	nextRoundRan := false
	refreshAuxiliaryDomainsWithin(context.Background(), bindingDomainListerFunc(func(context.Context) ([]string, []string, error) {
		nextRoundRan = true
		return nil, nil, nil
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
		refreshAuxiliaryDomainsWithin(ctx, bindingDomainListerFunc(func(callCtx context.Context) ([]string, []string, error) {
			close(started)
			<-callCtx.Done()
			return nil, nil, callCtx.Err()
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
