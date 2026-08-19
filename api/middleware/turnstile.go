package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

// TurnstileHeaderName carries the single-use Turnstile token on guarded routes.
// A header rather than a body field keeps one guard usable on both the JSON and
// the multipart routes, and lets a bad token be rejected before the handler
// reads a 100 MB upload.
const TurnstileHeaderName = "X-Turnstile-Token"

// turnstileActions maps a gin route to its Turnstile action string. Cloudflare
// echoes the action back on siteverify, so a token minted for one form cannot
// be replayed against another route.
//
// Only low-frequency, high-damage writes belong here. A session cookie is
// replayable by any script, so authorization alone does not bound what one
// account can do; a challenge does, for actions a real user performs rarely.
// High-frequency writes (check-in, order placement, ticket replies) are
// deliberately absent — they need rate limits, not challenges.
var turnstileActions = map[string]string{
	// Money leaves the platform, or the input is guessable.
	"/v1/cards/redeem":                "card_redeem",
	"/v1/wallet/supplier-withdrawals": "supplier_withdrawal",
	"/v1/wallet/referrals/transfer":   "referral_transfer",
	"/v1/wallet/supplier-transfers":   "supplier_transfer",

	// Output lands in a queue a human has to read.
	"/v1/projects":                     "project_submit",
	"/v1/projects/:projectId/resubmit": "project_resubmit",
	"/v1/tickets":                      "ticket_create",
	"/v1/lotteries/:token/entries":     "lottery_enter",
	"/v1/admin/lotteries":              "lottery_publish",

	// Bulk supplier writes: rare per user, large blast radius per call.
	// ponytail: unconditional. Charging a challenge only past a daily free
	// allowance would spare routine bulk work, but the token has to exist
	// before a multipart upload starts, so a "challenge now required" retry
	// would mean re-uploading the file. Revisit if suppliers complain.
	"/v1/resources/imports": "resource_import",
	"/v1/domains":           "domain_import",
}

// TurnstileVerifier validates a single-use Cloudflare Turnstile token.
type TurnstileVerifier interface {
	Verify(ctx context.Context, token, remoteIP, expectedAction string) error
}

// TurnstileSiteLimiter bounds outbound siteverify calls by client IP.
type TurnstileSiteLimiter interface {
	HitTurnstile(ctx context.Context, ip string) (int, error)
}

// TurnstileGuard requires a valid Turnstile token on the routes listed in
// turnstileActions. Mount it after authentication and CSRF middleware so an
// invalid session cannot consume the siteverify budget. Unlisted routes and
// safe methods pass straight through.
//
// Responses mirror the IAM handler's existing Turnstile errors so the frontend
// only has one shape to handle: 422 for a bad token, 503 when Cloudflare is
// unreachable, 429 with Retry-After when the siteverify budget is spent.
func TurnstileGuard(verifier TurnstileVerifier, limiter TurnstileSiteLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		action, guarded := turnstileActions[c.FullPath()]
		if !guarded || isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}
		// Lottery entry and publication are money-equivalent reward operations;
		// they remain fail-closed even when the general low-risk CAPTCHA switch
		// is disabled for other routes.
		if !runtimeconfig.Bool("captcha_enabled", true) && action != "lottery_enter" && action != "lottery_publish" {
			c.Next()
			return
		}
		token := strings.TrimSpace(c.GetHeader(TurnstileHeaderName))
		if token == "" {
			abortTurnstile(c, iamdomain.ErrTurnstileInvalid)
			return
		}
		if limiter != nil {
			retryAfter, err := limiter.HitTurnstile(c.Request.Context(), c.ClientIP())
			if err != nil {
				abortTurnstile(c, err)
				return
			}
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"message":   "Too many requests.",
					"requestId": GetRequestID(c),
				})
				return
			}
		}
		if verifier == nil {
			abortTurnstile(c, iamdomain.ErrTurnstileUnavailable)
			return
		}
		if err := verifier.Verify(c.Request.Context(), token, c.ClientIP(), action); err != nil {
			abortTurnstile(c, err)
			return
		}

		c.Next()
	}
}

func abortTurnstile(c *gin.Context, err error) {
	if errors.Is(err, iamdomain.ErrTurnstileInvalid) {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Human verification failed.",
			"requestId": GetRequestID(c),
		})
		return
	}
	slog.Warn("turnstile unavailable", "request_id", GetRequestID(c), "error", err.Error())
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"message":   "Human verification is temporarily unavailable.",
		"requestId": GetRequestID(c),
	})
}
