package icloud

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/redis/go-redis/v9"
)

var errAppleProxyUnavailable = errors.New("icloud: Apple proxy unavailable")

const (
	appleProxyRotationHeader       = "X-Remail-Apple-Proxy-Rotation"
	appleProxyRotationDelayHeader  = "X-Remail-Apple-Proxy-Rotation-Delay"
	appleProxyRetryExhaustedHeader = "X-Remail-Apple-Proxy-Retry-Exhausted"
	appleProxyRotationTTL          = 10 * time.Minute
	appleProxyRotationKeyPrefix    = "remail:icloud:apple-proxy-rotation:"
	appleProxyRotationPending      = "pending"
	appleProxyRotationClaimed      = "claimed"
	appleProxyRotationExhausted    = "exhausted"
	appleProxyRotationClaimWait    = time.Second
)

func appleProxyRotationDelay() time.Duration {
	return time.Duration(60+rand.Intn(241)) * time.Second
}

func appleProxyRetryState(headers http.Header) (time.Duration, bool) {
	if headers == nil {
		return 0, false
	}
	if strings.EqualFold(strings.TrimSpace(headers.Get(appleProxyRetryExhaustedHeader)), "1") {
		return 0, true
	}
	if !strings.EqualFold(strings.TrimSpace(headers.Get(appleProxyRotationHeader)), "1") {
		return 0, false
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(headers.Get(appleProxyRotationDelayHeader)))
	if err != nil || seconds <= 0 || seconds > int((5*time.Minute)/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, false
}

func appleProxyResponseRetryAfter(headers http.Header, body []byte, now time.Time) (time.Duration, bool) {
	if delay, exhausted := appleProxyRetryState(headers); exhausted || delay > 0 {
		return delay, exhausted
	}
	return iCloudResponseRetryAfter(headers.Get("Retry-After"), body, now), false
}

type AppleProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type appleHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type appleRouteEmailKey struct{}

type appleRouteDeferredRotationKey struct{}

type appleRouteManager struct {
	proxies    AppleProxyProvider
	newSession func(context.Context, string) (appleOnboardingHTTPSession, error)
	stateStore redis.UniversalClient
	now        func() time.Time
	mu         sync.Mutex
	rotations  map[string]appleProxyRotationState
}

type appleProxyRotationState struct {
	Consecutive5xx int       `json:"consecutive5xx"`
	Phase          string    `json:"phase,omitempty"`
	AvoidServerID  uint      `json:"avoidServerId,omitempty"`
	NotBefore      time.Time `json:"notBefore,omitempty"`
	Token          string    `json:"token,omitempty"`
	expiresAt      time.Time `json:"-"`
}

type appleProxyRotationError struct {
	RetryAfter time.Duration
	Exhausted  bool
	cause      error
}

func (e *appleProxyRotationError) Error() string {
	if e == nil {
		return errAppleProxyUnavailable.Error()
	}
	if e.Exhausted {
		return "icloud: Apple proxy rotation exhausted"
	}
	if e.RetryAfter > 0 {
		return "icloud: Apple proxy rotation is pending"
	}
	if e.cause != nil {
		return fmt.Sprintf("icloud: Apple proxy rotation state unavailable: %v", e.cause)
	}
	return errAppleProxyUnavailable.Error()
}

func (e *appleProxyRotationError) Unwrap() error {
	if e == nil || e.cause == nil {
		return errAppleProxyUnavailable
	}
	return e.cause
}

func appleProxyRotationDetails(err error) (time.Duration, bool, bool) {
	var rotationErr *appleProxyRotationError
	if !errors.As(err, &rotationErr) {
		return 0, false, false
	}
	return rotationErr.RetryAfter, rotationErr.Exhausted, true
}

func newAppleRouteManager(redisClients ...redis.UniversalClient) *appleRouteManager {
	var stateStore redis.UniversalClient
	for _, client := range redisClients {
		if client != nil {
			stateStore = client
			break
		}
	}
	return &appleRouteManager{stateStore: stateStore, rotations: make(map[string]appleProxyRotationState), now: time.Now, newSession: func(ctx context.Context, proxyURL string) (appleOnboardingHTTPSession, error) {
		session, err := msacl.NewAppleAPISession(ctx, proxyURL, 30)
		if err != nil {
			return nil, err
		}
		return &appleOnboardingMSACLSession{session: session}, nil
	}}
}

func (r *appleRouteManager) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func appleProxyRotationKey(email string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return appleProxyRotationKeyPrefix + fmt.Sprintf("%x", digest[:])
}

func appleProxyRotationErrorFor(cause error, retryAfter time.Duration, exhausted bool) error {
	return &appleProxyRotationError{RetryAfter: retryAfter, Exhausted: exhausted, cause: cause}
}

func appleProxyRotationWait(now, until time.Time) time.Duration {
	if !until.After(now) {
		return 0
	}
	delay := until.Sub(now)
	if remainder := delay % time.Second; remainder != 0 {
		delay += time.Second - remainder
	}
	return delay
}

func (r *appleRouteManager) loadRotation(ctx context.Context, email string) (appleProxyRotationState, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if r.stateStore == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.rotations == nil {
			r.rotations = make(map[string]appleProxyRotationState)
		}
		state, ok := r.rotations[email]
		if ok && !state.expiresAt.After(r.currentTime()) {
			delete(r.rotations, email)
			return appleProxyRotationState{}, false, nil
		}
		return state, ok, nil
	}
	raw, err := r.stateStore.Get(ctx, appleProxyRotationKey(email)).Result()
	if err == redis.Nil {
		return appleProxyRotationState{}, false, nil
	}
	if err != nil {
		return appleProxyRotationState{}, false, err
	}
	var state appleProxyRotationState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return appleProxyRotationState{}, false, fmt.Errorf("decode Apple proxy rotation state: %w", err)
	}
	return state, true, nil
}

// updateRotation performs a compare-and-set mutation. Redis is authoritative
// in production; the mutex path only keeps dependency-free unit tests useful.
func (r *appleRouteManager) updateRotation(ctx context.Context, email string, mutate func(appleProxyRotationState, bool) (appleProxyRotationState, bool, error)) (appleProxyRotationState, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if r.stateStore == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		state, exists := r.rotations[email]
		if exists && !state.expiresAt.After(r.currentTime()) {
			delete(r.rotations, email)
			exists = false
			state = appleProxyRotationState{}
		}
		next, keep, err := mutate(state, exists)
		if err != nil {
			return next, keep, err
		}
		if keep {
			if !exists || next.expiresAt.IsZero() {
				next.expiresAt = r.currentTime().Add(appleProxyRotationTTL)
			}
			r.rotations[email] = next
		} else {
			delete(r.rotations, email)
		}
		return next, keep, nil
	}

	key := appleProxyRotationKey(email)
	for attempt := 0; attempt < 5; attempt++ {
		var result appleProxyRotationState
		var resultKeep bool
		err := r.stateStore.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Result()
			exists := err == nil
			if err != nil && err != redis.Nil {
				return err
			}
			var state appleProxyRotationState
			var existingTTL time.Duration
			if exists {
				if err := json.Unmarshal([]byte(raw), &state); err != nil {
					return fmt.Errorf("decode Apple proxy rotation state: %w", err)
				}
				existingTTL, err = tx.PTTL(ctx, key).Result()
				if err != nil {
					return err
				}
			}
			next, keep, err := mutate(state, exists)
			if err != nil {
				return err
			}
			result, resultKeep = next, keep
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if keep {
					payload, marshalErr := json.Marshal(next)
					if marshalErr != nil {
						return marshalErr
					}
					if exists && existingTTL > 0 {
						pipe.SetArgs(ctx, key, payload, redis.SetArgs{KeepTTL: true})
					} else {
						pipe.Set(ctx, key, payload, appleProxyRotationTTL)
					}
				} else {
					pipe.Del(ctx, key)
				}
				return nil
			})
			return err
		}, key)
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			return appleProxyRotationState{}, false, err
		}
		return result, resultKeep, nil
	}
	return appleProxyRotationState{}, false, fmt.Errorf("Apple proxy rotation state changed concurrently")
}

func withAppleRouteEmail(ctx context.Context, email string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, appleRouteEmailKey{}, strings.ToLower(strings.TrimSpace(email)))
}

func withAppleRouteDeferredRotation(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, appleRouteDeferredRotationKey{}, true)
}

func appleRouteDeferredRotation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	deferred, _ := ctx.Value(appleRouteDeferredRotationKey{}).(bool)
	return deferred
}

func appleRouteEmail(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	email, _ := ctx.Value(appleRouteEmailKey{}).(string)
	return strings.ToLower(strings.TrimSpace(email))
}

func (r *appleRouteManager) session(ctx context.Context) (appleOnboardingHTTPSession, error) {
	if r == nil || r.proxies == nil || r.newSession == nil {
		return nil, errAppleProxyUnavailable
	}
	email := appleRouteEmail(ctx)
	if email == "" {
		return nil, errAppleProxyUnavailable
	}
	rotation, rotating, err := r.takeRotation(ctx, email)
	if err != nil {
		return nil, err
	}
	attempt := 0
	var avoidServerIDs []uint
	if rotating {
		attempt = 1
		if rotation.AvoidServerID != 0 {
			avoidServerIDs = []uint{rotation.AvoidServerID}
		}
	}
	config, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 email,
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeBinding,
		AllowSystemFallback: false,
		RenewBinding:        true,
		Attempt:             attempt,
		RequestID:           appleRouteRequestID(ctx),
		AvoidProxyServerIDs: avoidServerIDs,
	})
	if err != nil {
		if rotating {
			return nil, r.finishRotatedFailure(ctx, email, rotation.Token, err)
		} else {
			if stateErr := r.recordProxyTransportFailure(ctx, email, "", false); stateErr != nil {
				return nil, stateErr
			}
		}
		return nil, fmt.Errorf("%w: %v", errAppleProxyUnavailable, err)
	}
	if config == nil || config.Direct || config.ID == 0 || strings.TrimSpace(config.URL) == "" {
		if rotating {
			return nil, r.finishRotatedFailure(ctx, email, rotation.Token, errAppleProxyUnavailable)
		} else {
			if stateErr := r.recordProxyTransportFailure(ctx, email, "", false); stateErr != nil {
				return nil, stateErr
			}
		}
		return nil, errAppleProxyUnavailable
	}
	if rotating && rotation.AvoidServerID != 0 && config.ProxyServerID == rotation.AvoidServerID {
		return nil, r.finishRotatedFailure(ctx, email, rotation.Token, fmt.Errorf("proxy provider returned the avoided proxy server"))
	}
	session, err := r.newSession(ctx, config.URL)
	if err != nil {
		if rotating {
			failure := r.finishRotatedFailure(ctx, email, rotation.Token, err)
			if isAppleProxyTransportFailure(ctx, err) {
				r.reportFailure(ctx, config.ID)
			}
			return nil, failure
		} else {
			if stateErr := r.recordProxyTransportFailure(ctx, email, "", false); stateErr != nil {
				return nil, stateErr
			}
		}
		if isAppleProxyTransportFailure(ctx, err) {
			r.reportFailure(ctx, config.ID)
		}
		return nil, err
	}
	return &appleProxySession{
		ctx:                 ctx,
		inner:               session,
		routes:              r,
		email:               email,
		proxyID:             config.ID,
		proxyServerID:       config.ProxyServerID,
		rotationToken:       rotation.Token,
		rotated:             rotating,
		rotationAttempted:   rotating,
		deferRotationCommit: appleRouteDeferredRotation(ctx),
	}, nil
}

func (r *appleRouteManager) takeRotation(ctx context.Context, email string) (appleProxyRotationState, bool, error) {
	now := r.currentTime()
	state, exists, err := r.loadRotation(ctx, email)
	if err != nil {
		return appleProxyRotationState{}, false, appleProxyRotationErrorFor(err, 0, false)
	}
	if !exists {
		return appleProxyRotationState{}, false, nil
	}
	if state.Phase == appleProxyRotationExhausted {
		return state, false, appleProxyRotationErrorFor(nil, 0, true)
	}
	if state.Phase != appleProxyRotationPending && state.Phase != appleProxyRotationClaimed {
		return appleProxyRotationState{}, false, nil
	}
	if state.Phase == appleProxyRotationPending && now.Before(state.NotBefore) {
		return state, false, appleProxyRotationErrorFor(nil, appleProxyRotationWait(now, state.NotBefore), false)
	}
	if state.Phase == appleProxyRotationClaimed {
		return state, false, appleProxyRotationErrorFor(nil, appleProxyRotationClaimWait, false)
	}
	token := platform.NewUUIDV4String()
	var claimed bool
	result, _, err := r.updateRotation(ctx, email, func(current appleProxyRotationState, ok bool) (appleProxyRotationState, bool, error) {
		if !ok || current.Phase != appleProxyRotationPending {
			return current, ok, nil
		}
		if r.currentTime().Before(current.NotBefore) {
			return current, true, appleProxyRotationErrorFor(nil, appleProxyRotationWait(r.currentTime(), current.NotBefore), false)
		}
		current.Phase = appleProxyRotationClaimed
		current.Token = token
		claimed = true
		return current, true, nil
	})
	if err != nil {
		return appleProxyRotationState{}, false, err
	}
	if !claimed {
		return result, false, appleProxyRotationErrorFor(nil, appleProxyRotationClaimWait, false)
	}
	return result, true, nil
}

func (r *appleRouteManager) finishRotatedFailure(ctx context.Context, email, token string, cause error) error {
	stateErr := r.recordProxyTransportFailure(ctx, email, token, true)
	if stateErr != nil {
		cause = errors.Join(cause, stateErr)
	}
	return appleProxyRotationErrorFor(cause, 0, true)
}

func (r *appleRouteManager) releaseRotatedClaim(ctx context.Context, email, token string) error {
	_, _, err := r.updateRotation(ctx, email, func(state appleProxyRotationState, exists bool) (appleProxyRotationState, bool, error) {
		if exists && state.Phase == appleProxyRotationClaimed && state.Token == token {
			state.Phase = appleProxyRotationPending
			state.Token = ""
			state.NotBefore = r.currentTime()
		}
		return state, exists, nil
	})
	return err
}

func (r *appleRouteManager) recordProxySuccess(ctx context.Context, email, token string, rotated bool) error {
	_, _, err := r.updateRotation(ctx, email, func(state appleProxyRotationState, exists bool) (appleProxyRotationState, bool, error) {
		if !exists {
			return state, false, nil
		}
		if rotated {
			if state.Phase == appleProxyRotationClaimed && state.Token == token {
				return state, false, nil
			}
			return state, true, nil
		}
		// A successful response breaks a partial 5XX sequence. Once rotation is
		// pending/claimed, however, keep that one-shot decision intact so an
		// in-flight older session cannot cancel the failover another worker owns.
		if state.Phase == appleProxyRotationPending || state.Phase == appleProxyRotationClaimed || state.Phase == appleProxyRotationExhausted {
			return state, true, nil
		}
		return state, false, nil
	})
	return err
}

func (r *appleRouteManager) recordProxy5xx(ctx context.Context, email, token string, rotated bool, proxyServerID uint) (time.Duration, bool, error) {
	var delay time.Duration
	var exhausted bool
	_, _, err := r.updateRotation(ctx, email, func(state appleProxyRotationState, exists bool) (appleProxyRotationState, bool, error) {
		if rotated {
			exhausted = true
			if exists && state.Phase == appleProxyRotationClaimed && state.Token == token {
				state.Phase = appleProxyRotationExhausted
				state.Token = ""
				return state, true, nil
			}
			return state, exists, nil
		}
		if exists && (state.Phase == appleProxyRotationPending || state.Phase == appleProxyRotationClaimed || state.Phase == appleProxyRotationExhausted) {
			return state, true, nil
		}
		state.Consecutive5xx++
		if state.Consecutive5xx < 3 {
			return state, true, nil
		}
		state.Consecutive5xx = 0
		state.Phase = appleProxyRotationPending
		state.AvoidServerID = proxyServerID
		delay = appleProxyRotationDelay()
		state.NotBefore = r.currentTime().Add(delay)
		return state, true, nil
	})
	return delay, exhausted, err
}

func (r *appleRouteManager) recordProxyTransportFailure(ctx context.Context, email, token string, rotated bool) error {
	_, _, err := r.updateRotation(ctx, email, func(state appleProxyRotationState, exists bool) (appleProxyRotationState, bool, error) {
		if rotated {
			if exists && state.Phase == appleProxyRotationClaimed && state.Token == token {
				state.Phase = appleProxyRotationExhausted
				state.Token = ""
				return state, true, nil
			}
			return state, exists, nil
		}
		if exists && (state.Phase == appleProxyRotationPending || state.Phase == appleProxyRotationClaimed || state.Phase == appleProxyRotationExhausted) {
			return state, true, nil
		}
		return state, false, nil
	})
	return err
}

func (r *appleRouteManager) reportSuccess(ctx context.Context, proxyID uint) {
	if r == nil || r.proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := appleProxyReportContext(ctx)
	defer cancel()
	_ = r.proxies.ReportSuccess(reportCtx, proxyID)
}

func (r *appleRouteManager) reportFailure(ctx context.Context, proxyID uint) {
	if r == nil || r.proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := appleProxyReportContext(ctx)
	defer cancel()
	_ = r.proxies.ReportFailure(reportCtx, proxyID, "Apple proxy transport failed.")
}

func appleProxyReportContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func appleRouteRequestID(ctx context.Context) string {
	if ctx != nil {
		if requestID, ok := ctx.Value(platform.RequestIDKey).(string); ok && strings.TrimSpace(requestID) != "" {
			return strings.TrimSpace(requestID)
		}
	}
	return platform.NewUUIDV7String()
}

func isAppleProxyTransportFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ctx == nil || ctx.Err() == nil
	}
	if msacl.IsProxyTransportError(err) {
		return true
	}
	return false
}

type appleProxySession struct {
	ctx                 context.Context
	inner               appleOnboardingHTTPSession
	routes              *appleRouteManager
	email               string
	proxyID             uint
	proxyServerID       uint
	rotationToken       string
	rotated             bool
	rotationAttempted   bool
	deferRotationCommit bool
	blocked             bool
}

func (s *appleProxySession) Request(method, rawURL string, headers map[string]string, body any, follow bool) (*appleOnboardingHTTPResponse, error) {
	if s == nil || s.routes == nil || s.blocked {
		return nil, appleProxyRotationErrorFor(nil, 0, true)
	}
	rotated := s.rotated
	response, err := s.inner.Request(method, rawURL, headers, body, follow)
	if err != nil {
		s.rotated = false
		if s.rotationAttempted {
			s.blocked = true
			s.rotationAttempted = false
			failure := s.routes.finishRotatedFailure(s.ctx, s.email, s.rotationToken, err)
			if isAppleProxyTransportFailure(s.ctx, err) {
				s.routes.reportFailure(s.ctx, s.proxyID)
			}
			return nil, failure
		}
		if stateErr := s.routes.recordProxyTransportFailure(s.ctx, s.email, "", false); stateErr != nil {
			return nil, appleProxyRotationErrorFor(errors.Join(err, stateErr), 0, false)
		}
		if isAppleProxyTransportFailure(s.ctx, err) {
			s.routes.reportFailure(s.ctx, s.proxyID)
		}
		return nil, err
	}
	if response == nil {
		s.rotated = false
		if s.rotationAttempted {
			s.blocked = true
			s.rotationAttempted = false
			return nil, s.routes.finishRotatedFailure(s.ctx, s.email, s.rotationToken, io.ErrUnexpectedEOF)
		}
		if stateErr := s.routes.recordProxyTransportFailure(s.ctx, s.email, "", false); stateErr != nil {
			return nil, stateErr
		}
		return nil, io.ErrUnexpectedEOF
	}
	if response.StatusCode >= 500 && response.StatusCode <= 599 {
		rotationForFailure := s.rotationAttempted || rotated
		delay, exhausted, stateErr := s.routes.recordProxy5xx(s.ctx, s.email, s.rotationToken, rotationForFailure, s.proxyServerID)
		if stateErr != nil {
			if rotationForFailure {
				s.blocked = true
				s.rotationAttempted = false
				return nil, s.routes.finishRotatedFailure(s.ctx, s.email, s.rotationToken, stateErr)
			}
			return nil, appleProxyRotationErrorFor(stateErr, 0, false)
		}
		if response.Header == nil {
			response.Header = make(http.Header)
		}
		response.Header.Del(appleProxyRotationHeader)
		response.Header.Del(appleProxyRotationDelayHeader)
		response.Header.Del(appleProxyRetryExhaustedHeader)
		if exhausted {
			response.ProxyRetryExhausted = true
			response.Header.Set(appleProxyRetryExhaustedHeader, "1")
		} else if delay > 0 {
			response.ProxyRotationPending = true
			response.ProxyRetryAfter = delay
			response.Header.Set(appleProxyRotationHeader, "1")
			response.Header.Set(appleProxyRotationDelayHeader, strconv.Itoa(int(delay/time.Second)))
		}
		if exhausted {
			s.blocked = true
			s.rotationAttempted = false
		}
		s.routes.reportFailure(s.ctx, s.proxyID)
	} else {
		if s.rotationAttempted {
			if !s.deferRotationCommit || response.StatusCode >= http.StatusBadRequest {
				if stateErr := s.routes.recordProxySuccess(s.ctx, s.email, s.rotationToken, true); stateErr != nil {
					return nil, appleProxyRotationErrorFor(stateErr, 0, false)
				}
				s.rotationAttempted = false
			}
		} else if stateErr := s.routes.recordProxySuccess(s.ctx, s.email, s.rotationToken, false); stateErr != nil {
			return nil, appleProxyRotationErrorFor(stateErr, 0, false)
		}
		s.routes.reportSuccess(s.ctx, s.proxyID)
	}
	s.rotated = false
	return response, nil
}

func (s *appleProxySession) SnapshotCookies(rawURLs ...string) ([]msacl.SessionCookie, error) {
	if s == nil || s.inner == nil {
		return nil, errAppleProxyUnavailable
	}
	if s.blocked {
		return nil, appleProxyRotationErrorFor(nil, 0, true)
	}
	cookies, err := s.inner.SnapshotCookies(rawURLs...)
	if err != nil && s.rotationAttempted {
		s.blocked = true
		s.rotationAttempted = false
		return nil, s.routes.finishRotatedFailure(s.ctx, s.email, s.rotationToken, err)
	}
	return cookies, err
}

func (s *appleProxySession) RestoreCookies(cookies []msacl.SessionCookie) error {
	if s == nil || s.inner == nil {
		return errAppleProxyUnavailable
	}
	if s.blocked {
		return appleProxyRotationErrorFor(nil, 0, true)
	}
	err := s.inner.RestoreCookies(cookies)
	if err != nil && s.rotationAttempted {
		s.blocked = true
		s.rotationAttempted = false
		return s.routes.finishRotatedFailure(s.ctx, s.email, s.rotationToken, err)
	}
	return err
}

func (s *appleProxySession) finalizeProxyRotation() error {
	if s == nil || !s.rotationAttempted || !s.deferRotationCommit {
		return nil
	}
	if s.blocked {
		return appleProxyRotationErrorFor(nil, 0, true)
	}
	if err := s.routes.recordProxySuccess(s.ctx, s.email, s.rotationToken, true); err != nil {
		return appleProxyRotationErrorFor(err, 0, false)
	}
	s.rotationAttempted = false
	return nil
}

func (s *appleProxySession) resetProxyRotation() error {
	if s == nil || !s.rotationAttempted || s.blocked {
		return nil
	}
	var err error
	if s.rotated {
		err = s.routes.releaseRotatedClaim(s.ctx, s.email, s.rotationToken)
	} else {
		err = s.routes.recordProxySuccess(s.ctx, s.email, s.rotationToken, true)
	}
	if err != nil {
		return appleProxyRotationErrorFor(err, 0, false)
	}
	s.rotated = false
	s.rotationAttempted = false
	return nil
}

type appleProxyHTTPClient struct {
	routes *appleRouteManager
}

func (c *appleProxyHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || c == nil || c.routes == nil {
		return nil, errAppleProxyUnavailable
	}
	var body any
	if request.Body != nil {
		data, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			body = json.RawMessage(data)
		}
	}
	session, err := c.routes.session(request.Context())
	if err != nil {
		return nil, err
	}
	response, err := session.Request(request.Method, request.URL.String(), appleRequestHeaders(request.Header), body, false)
	if err != nil {
		return nil, err
	}
	responseURL, parseErr := url.Parse(response.URL)
	if parseErr != nil {
		responseURL = request.URL
	}
	responseHeaders := response.Header.Clone()
	if responseHeaders == nil {
		responseHeaders = make(http.Header)
	}
	if response.ProxyRotationPending {
		responseHeaders.Set(appleProxyRotationHeader, "1")
		responseHeaders.Set(appleProxyRotationDelayHeader, strconv.Itoa(int(response.ProxyRetryAfter/time.Second)))
	}
	if response.ProxyRetryExhausted {
		responseHeaders.Set(appleProxyRetryExhaustedHeader, "1")
	}
	responseRequest := request.Clone(request.Context())
	responseRequest.URL = responseURL
	return &http.Response{
		StatusCode: response.StatusCode,
		Status:     fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode)),
		Header:     responseHeaders,
		Body:       io.NopCloser(strings.NewReader(response.Body)),
		Request:    responseRequest,
	}, nil
}

func appleRequestHeaders(source http.Header) map[string]string {
	headers := make(map[string]string, len(source))
	for key, values := range source {
		separator := ", "
		if strings.EqualFold(key, "Cookie") {
			separator = "; "
		}
		headers[key] = strings.Join(values, separator)
	}
	return headers
}

type routedAppleOnboardingProvider struct {
	delegate AppleOnboardingProvider
}

func (p *routedAppleOnboardingProvider) Execute(ctx context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if p == nil || p.delegate == nil {
		return AppleOnboardingResponse{}, ErrICloudOnboardingProvider
	}
	return p.delegate.Execute(withAppleRouteDeferredRotation(withAppleRouteEmail(ctx, request.Email)), request)
}

func newRoutedAppleOnboardingProvider(routes *appleRouteManager) AppleOnboardingProvider {
	return &routedAppleOnboardingProvider{delegate: &appleOnboardingClient{
		newSession: routes.session,
		endpoints:  defaultAppleOnboardingEndpoints(),
		now:        time.Now,
	}}
}

// NewAppleOnboardingClientWithProxyProvider reuses the production sticky
// Apple proxy route for standalone validation tools.
func NewAppleOnboardingClientWithProxyProvider(provider AppleProxyProvider, redisClients ...redis.UniversalClient) AppleOnboardingProvider {
	routes := newAppleRouteManager(redisClients...)
	routes.proxies = provider
	return newRoutedAppleOnboardingProvider(routes)
}

func newRoutedAppleAccountClient(routes *appleRouteManager) *AppleAccountClient {
	return &AppleAccountClient{httpClient: &appleProxyHTTPClient{routes: routes}}
}

func newRoutedHMEClient(routes *appleRouteManager) *HMEClient {
	return &HMEClient{httpClient: &appleProxyHTTPClient{routes: routes}}
}

func newRoutedICloudFamilyClient(routes *appleRouteManager) *iCloudFamilyClient {
	return &iCloudFamilyClient{
		httpClient: &appleProxyHTTPClient{routes: routes},
		endpoint:   iCloudFamilyMembersEndpoint,
		now:        time.Now,
	}
}
