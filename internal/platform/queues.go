package platform

import (
	"context"

	"github.com/hibiken/asynq"
)

// BackgroundTaskMaxRetry leaves ample Asynq retry headroom for rare
// infrastructure errors. Capacity deferrals do not increment this counter.
const BackgroundTaskMaxRetry = 100

// BackgroundTaskHasRetryHeadroom reports whether a handler error can still be
// moved to Asynq's retry set. Messages queued by older releases used
// MaxRetry(0); callers must release their fenced database dispatch instead of
// returning ErrBackgroundExecutionDeferred for those legacy messages.
// Contexts outside an Asynq processor do not contain retry metadata and are
// treated as retry-capable, which keeps direct handler tests deterministic.
func BackgroundTaskHasRetryHeadroom(ctx context.Context) bool {
	retried, retriedOK := asynq.GetRetryCount(ctx)
	maximum, maximumOK := asynq.GetMaxRetry(ctx)
	if !retriedOK || !maximumOK {
		return true
	}
	return backgroundTaskHasRetryHeadroom(retried, maximum)
}

func backgroundTaskHasRetryHeadroom(retried, maximum int) bool {
	return retried < maximum
}

// Single source of truth for Asynq queue names.
//
// Every queue a module enqueues to MUST be served by exactly one worker tier
// (see the tier configs below and TestEveryEnqueuedQueueIsServedByExactlyOneTier).
// Historically the enqueue-side names (scattered as per-module string consts)
// and the server-side tier configs drifted: the "resource" queue was added for
// admin bulk operations but never served, so every bulk command stalled forever.
//
// To prevent that class of bug: all queue names live here, the tier configs and
// the topology test are built from AllQueueNames, and each module's queue.go
// references these constants instead of a bare string literal. Adding a new
// queue therefore forces a constant here + a tier assignment, and the test
// fails until both exist.
const (
	// QueueMailfetch is the dedicated realtime 接码 (verification-code) pickup queue.
	QueueMailfetch = "mailfetch"
	// QueueMailtransport carries foreground outbound mail delivery.
	QueueMailtransport = "mailtransport"
	// QueueDefault carries assorted foreground work (imports, allocation, proxy checks).
	QueueDefault = "default"
	// QueueBackgroundValidation carries temporary resource validation tasks.
	QueueBackgroundValidation = "background_validation"
	// QueueBackgroundAlias carries Microsoft explicit-alias creation.
	QueueBackgroundAlias = "background_alias"
	// QueueBackgroundTokenRefresh carries Microsoft refresh-token maintenance.
	QueueBackgroundTokenRefresh = "background_token_refresh"
	// QueueResource carries admin resource bulk operations (validate/publish/unpublish/delete).
	QueueResource = "resource"
	// QueueBackgroundProjectHistory pulls mailbox history for newly listed projects.
	QueueBackgroundProjectHistory = "background_project_history"
	// QueueBackgroundInventory refreshes Redis read models such as inventory and dashboard rankings.
	QueueBackgroundInventory = "background_inventory"
)

// AllQueueNames is every queue some module enqueues tasks to. The topology test
// asserts each is served by exactly one worker tier.
var AllQueueNames = []string{
	QueueMailfetch,
	QueueMailtransport,
	QueueDefault,
	QueueBackgroundValidation,
	QueueBackgroundAlias,
	QueueBackgroundTokenRefresh,
	QueueResource,
	QueueBackgroundProjectHistory,
	QueueBackgroundInventory,
}
