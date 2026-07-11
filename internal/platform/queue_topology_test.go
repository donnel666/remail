package platform

import "testing"

// enqueuedQueues lists every queue name that some module enqueues tasks to.
// Each must be served by at least one worker tier or its tasks would stall.
var enqueuedQueues = []string{
	"mailfetch",
	"mailtransport",
	"default",
	"background_validation",
	"background_alias",
}

func TestForegroundExcludesDedicatedCodePickupQueue(t *testing.T) {
	fg := foregroundQueueConfig()
	if _, ok := fg["mailfetch"]; ok {
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
	if _, ok := rt["mailfetch"]; !ok {
		t.Fatal("realtime config must reserve capacity for the 接码 mailfetch queue")
	}
	for name := range rt {
		if name == "background_validation" || name == "background_alias" {
			t.Fatalf("realtime tier must not serve background queue %s", name)
		}
	}
}

func TestBackgroundTierOnlyServesBackgroundQueues(t *testing.T) {
	bg := backgroundQueueConfig()
	for name := range bg {
		if name != "background_validation" && name != "background_alias" {
			t.Fatalf("background tier must not serve foreground/realtime queue %s", name)
		}
	}
	if bg["background_validation"] != 3 || bg["background_alias"] != 1 {
		t.Fatalf("background queues must retain 3:1 weighted fairness, got %#v", bg)
	}
}

func TestBackgroundAdmissionMatchesDedicatedWorkerCapacity(t *testing.T) {
	if asynqBackgroundWorkerConcurrency != backgroundIdleExecutionCap {
		t.Fatalf(
			"idle background admission cap must match worker capacity: workers=%d cap=%d",
			asynqBackgroundWorkerConcurrency,
			backgroundIdleExecutionCap,
		)
	}
}

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
	for _, name := range enqueuedQueues {
		if served[name] != 1 {
			t.Fatalf("queue %q must be served by exactly one tier, got %d", name, served[name])
		}
	}
}
