package platform

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
	// QueueBackgroundValidation carries resource validation + token refresh.
	QueueBackgroundValidation = "background_validation"
	// QueueBackgroundAlias carries Microsoft explicit-alias creation.
	QueueBackgroundAlias = "background_alias"
	// QueueResource carries admin resource bulk operations (validate/publish/unpublish/delete).
	QueueResource = "resource"
)

// AllQueueNames is every queue some module enqueues tasks to. The topology test
// asserts each is served by exactly one worker tier.
var AllQueueNames = []string{
	QueueMailfetch,
	QueueMailtransport,
	QueueDefault,
	QueueBackgroundValidation,
	QueueBackgroundAlias,
	QueueResource,
}
