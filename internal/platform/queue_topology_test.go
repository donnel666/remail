package platform

import (
	"strconv"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

func TestForegroundExcludesDedicatedCodePickupQueue(t *testing.T) {
	fg := foregroundQueueConfig()
	if _, ok := fg[QueueMailfetch]; ok {
		t.Fatal("foreground config must not duplicate the dedicated 接码 queue")
	}
	if _, ok := fg[QueuePaymentReconcile]; ok {
		t.Fatal("foreground config must not duplicate the dedicated payment reconciliation queue")
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
	if _, ok := rt[QueuePaymentReconcile]; !ok {
		t.Fatal("realtime config must reserve capacity for payment reconciliation")
	}
	for name := range rt {
		if name == QueueBackgroundValidation || name == QueueBackgroundDomainValidation || name == QueueBackgroundAlias || name == QueueBackgroundTokenRefresh || name == QueueBackgroundProjectHistory || name == QueueBackgroundInventory || name == QueueResource {
			t.Fatalf("realtime tier must not serve background queue %s", name)
		}
	}
}

func TestBackgroundTierOnlyServesBackgroundQueues(t *testing.T) {
	bg := backgroundQueueConfig()
	// The background tier must never serve the realtime/foreground queues.
	for _, foreground := range []string{QueueMailfetch, QueuePaymentReconcile, QueueMailtransport, QueueDefault} {
		if _, ok := bg[foreground]; ok {
			t.Fatalf("background tier must not serve realtime/foreground queue %s", foreground)
		}
	}
	if bg[QueueBackgroundValidation] != 3 || bg[QueueBackgroundDomainValidation] != 1 || bg[QueueBackgroundAlias] != 1 || bg[QueueBackgroundTokenRefresh] != 1 || bg[QueueBackgroundProjectHistory] != 1 || bg[QueueBackgroundInventory] != 1 || bg[QueueResource] != 2 {
		t.Fatalf("background queues must retain non-zero weighted fairness, got %#v", bg)
	}
}

func TestEveryQueueWeightUsesItsRuntimeSetting(t *testing.T) {
	type queueSetting struct {
		key    string
		queue  string
		config func() map[string]int
	}
	settings := []queueSetting{
		{"asynq_queue_mailfetch_weight", QueueMailfetch, realtimeQueueConfig},
		{"asynq_queue_payment_reconcile_weight", QueuePaymentReconcile, realtimeQueueConfig},
		{"asynq_queue_mailtransport_weight", QueueMailtransport, foregroundQueueConfig},
		{"asynq_queue_default_weight", QueueDefault, foregroundQueueConfig},
		{"asynq_queue_background_validation_weight", QueueBackgroundValidation, backgroundQueueConfig},
		{"asynq_queue_background_domain_validation_weight", QueueBackgroundDomainValidation, backgroundQueueConfig},
		{"asynq_queue_background_alias_weight", QueueBackgroundAlias, backgroundQueueConfig},
		{"asynq_queue_background_token_refresh_weight", QueueBackgroundTokenRefresh, backgroundQueueConfig},
		{"asynq_queue_resource_weight", QueueResource, backgroundQueueConfig},
		{"asynq_queue_background_project_history_weight", QueueBackgroundProjectHistory, backgroundQueueConfig},
		{"asynq_queue_background_inventory_weight", QueueBackgroundInventory, backgroundQueueConfig},
	}
	if len(settings) != len(AllQueueNames) {
		t.Fatalf("every queue must expose one weight setting: got %d settings for %d queues", len(settings), len(AllQueueNames))
	}
	seen := make(map[string]struct{}, len(settings))
	for index, setting := range settings {
		seen[setting.queue] = struct{}{}
		value := index + 1
		runtimeconfig.Set(setting.key, strconv.Itoa(value))
		key := setting.key
		t.Cleanup(func() { runtimeconfig.Delete(key) })
		if got := setting.config()[setting.queue]; got != value {
			t.Fatalf("queue %s weight must use %s: got %d want %d", setting.queue, setting.key, got, value)
		}
	}
	for _, queue := range AllQueueNames {
		if _, ok := seen[queue]; !ok {
			t.Fatalf("queue %s has no runtime weight setting", queue)
		}
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
