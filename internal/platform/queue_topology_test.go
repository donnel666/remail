package platform

import "testing"

func TestForegroundExcludesDedicatedCodePickupQueue(t *testing.T) {
	fg := foregroundQueueConfig()
	if _, ok := fg[QueueMailfetch]; ok {
		t.Fatal("foreground config must not duplicate the dedicated 接码 queue")
	}
	for name, weight := range fg {
		if weight <= 0 {
			t.Fatalf("foreground queue %s must have positive weight", name)
		}
	}
}

func TestRealtimeTierReservesDedicatedCapacityForCodePickup(t *testing.T) {
	rt := realtimeQueueConfig()
	if _, ok := rt[QueueMailfetch]; !ok {
		t.Fatal("realtime config must reserve capacity for the 接码 mailfetch queue")
	}
	for name := range rt {
		if name == QueueBackgroundValidation || name == QueueBackgroundAlias || name == QueueBackgroundTokenRefresh || name == QueueBackgroundProjectHistory || name == QueueBackgroundInventory || name == QueueResource {
			t.Fatalf("realtime tier must not serve background queue %s", name)
		}
	}
}

func TestBackgroundTierOnlyServesBackgroundQueues(t *testing.T) {
	bg := backgroundQueueConfig()
	// The background tier must never serve the realtime/foreground queues.
	for _, foreground := range []string{QueueMailfetch, QueueMailtransport, QueueDefault} {
		if _, ok := bg[foreground]; ok {
			t.Fatalf("background tier must not serve realtime/foreground queue %s", foreground)
		}
	}
	if bg[QueueBackgroundValidation] != 3 || bg[QueueBackgroundAlias] != 1 || bg[QueueBackgroundTokenRefresh] != 1 || bg[QueueBackgroundProjectHistory] != 1 || bg[QueueBackgroundInventory] != 1 || bg[QueueResource] != 2 {
		t.Fatalf("background queues must retain non-zero weighted fairness, got %#v", bg)
	}
}

func TestWorkerTierConcurrencyBudget(t *testing.T) {
	if asynqRealtimeWorkerConcurrency != 256 || asynqWorkerConcurrency != 768 {
		t.Fatalf("foreground ceilings must be realtime=256 and general=768, got realtime=%d general=%d", asynqRealtimeWorkerConcurrency, asynqWorkerConcurrency)
	}
	if asynqRealtimeWorkerConcurrency+asynqWorkerConcurrency != 1024 {
		t.Fatalf("foreground worker capacity must total 1024, got realtime=%d general=%d", asynqRealtimeWorkerConcurrency, asynqWorkerConcurrency)
	}
	if asynqBackgroundWorkerConcurrency != 512 {
		t.Fatalf("background worker ceiling must be 512, got %d", asynqBackgroundWorkerConcurrency)
	}
}

// TestEveryEnqueuedQueueIsServedByExactlyOneTier is the guard for the whole
// enqueue↔consume invariant: every queue in AllQueueNames (the single source of
// truth referenced by both the tier configs and the module enqueuers) must be
// served by exactly one worker tier. This is what would have caught the
// "resource" queue never being consumed.
func TestEveryEnqueuedQueueIsServedByExactlyOneTier(t *testing.T) {
	served := map[string]int{}
	for name := range realtimeQueueConfig() {
		served[name]++
	}
	for name := range foregroundQueueConfig() {
		served[name]++
	}
	for name := range backgroundQueueConfig() {
		served[name]++
	}
	for _, name := range AllQueueNames {
		if served[name] != 1 {
			t.Fatalf("queue %q must be served by exactly one tier, got %d", name, served[name])
		}
	}
	// And no tier serves a queue that nobody enqueues to (dead config entry).
	known := map[string]struct{}{}
	for _, name := range AllQueueNames {
		known[name] = struct{}{}
	}
	for name := range served {
		if _, ok := known[name]; !ok {
			t.Fatalf("tier serves queue %q that is not in AllQueueNames", name)
		}
	}
}

func TestLoadControllerCeilingMatchesBackgroundServer(t *testing.T) {
	controller := NewBackgroundLoadController(asynqBackgroundWorkerConcurrency)
	snapshot := controller.Snapshot()
	if snapshot.Maximum != asynqBackgroundWorkerConcurrency {
		t.Fatalf("adaptive ceiling must match the background Asynq ceiling, got adaptive=%d asynq=%d", snapshot.Maximum, asynqBackgroundWorkerConcurrency)
	}
	if snapshot.Limit < 1 || snapshot.Limit > snapshot.Maximum {
		t.Fatalf("adaptive initial limit must be within its ceiling, got %#v", snapshot)
	}
}
