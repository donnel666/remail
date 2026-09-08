package infra

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

// ProtocolClient belongs to Proto; no Microsoft client or credential crosses it.
type ProtocolClient interface {
	Login(context.Context, proton.LoginRequest) (proton.Session, error)
	Fetch(context.Context, *proton.Session, proton.FetchRequest) (proton.FetchResult, error)
}

type ProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

// FormatSender keeps historical recognition and live pickup on the same
// sender representation as the independently copied Microsoft mail flow.
func FormatSender(sender proton.Address) string {
	name, address := strings.TrimSpace(sender.Name), strings.TrimSpace(sender.Address)
	if name == "" {
		return address
	}
	if address == "" {
		return name
	}
	return name + " <" + address + ">"
}

func (s *Service) loginProtocol(ctx context.Context, row Resource, requestID string) (session proton.Session, err error) {
	if s.Protocol == nil {
		return session, domain.ErrDependency
	}
	started := time.Now()
	defer func() { observeProtoProtocol("login", started, err) }()
	proxy, err := s.acquireProtocolProxy(ctx, row.ID, requestID, proxydomain.ProxyPurposeAuth)
	if err != nil {
		return session, err
	}
	session, err = s.Protocol.Login(ctx, proton.LoginRequest{Email: row.EmailAddress, Password: row.Password, ProxyURL: proxy.URL})
	s.reportProtocolProxy(ctx, proxy.ID, err)
	return session, err
}

// FetchMailbox is the single Proto session boundary used by order pickup,
// administrator fetches and historical recognition. Password login belongs to
// validation; ordinary reads only reuse/refresh the encrypted persisted session.
func (s *Service) FetchMailbox(ctx context.Context, resourceID uint, revision uint64, request proton.FetchRequest) (result proton.FetchResult, err error) {
	if s == nil || s.Protocol == nil || s.DB == nil {
		return result, domain.ErrDependency
	}
	if resourceID == 0 || revision == 0 {
		return result, domain.ErrInvalidResource
	}
	if _, inTransaction := platform.GormTxFromContext(ctx); inTransaction {
		return result, domain.ErrDependency
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	started := time.Now()
	defer func() { observeProtoProtocol("mail_fetch", started, err) }()
	row, err := s.GetResource(ctx, resourceID, nil)
	if err != nil {
		return result, err
	}
	if row.CredentialRevision != revision || row.Status == domain.StatusDeleted {
		return result, domain.ErrInvalidClaim
	}
	request.Recipient = row.EmailAddress
	requestID, _ := ctx.Value(platform.RequestIDKey).(string)
	var observedRefreshToken string
	session, err := s.ReadSession(ctx, resourceID, revision)
	if err == nil {
		var proxy *proxyapp.ProxyConfig
		proxy, err = s.acquireProtocolProxy(ctx, resourceID, requestID, proxydomain.ProxyPurposeFetch)
		if err == nil {
			request.ProxyURL = proxy.URL
			// Reads never acquire a refresh lease. Only the HTTP token exchange and
			// its durable commit are serialized, so history cannot block live mail.
			request.OnSession = nil
			request.RefreshSession = func(refreshCtx context.Context, observed *proton.Session, refresh func(context.Context, *proton.Session) error) error {
				return s.refreshMailboxSession(refreshCtx, resourceID, revision, observed, refresh)
			}
			result, err = s.Protocol.Fetch(ctx, session, request)
			observedRefreshToken = session.RefreshToken
			s.reportProtocolProxy(ctx, proxy.ID, err)
		}
	}
	if err == nil && request.FullHistory && !result.Complete {
		err = &proton.Failure{Category: "incomplete_history", SafeMessage: "Proto mailbox history was not completely read.", Retryable: true}
	}
	if err == nil {
		// A concurrent token refresh is harmless; changing mailbox credentials,
		// identity or the loaded key ring still fences the whole read.
		var current *proton.Session
		current, err = s.ReadSession(ctx, resourceID, revision)
		if err == nil && !sameSessionMailbox(session, current) {
			err = domain.ErrInvalidClaim
		}
	}
	if err != nil {
		var failure *proton.Failure
		revoked := errors.As(err, &failure) && failure.Category == "session_revoked"
		if (errors.Is(err, ErrSessionUnavailable) || revoked) && (row.Status == domain.StatusNormal || row.Status == domain.StatusIdentifying) {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			if repairErr := s.RequeueSessionValidation(cleanup, resourceID, revision, row.ValidationGeneration, observedRefreshToken, "Proto session is unavailable; mailbox validation was requested."); repairErr != nil {
				err = errors.Join(err, repairErr)
			} else if s.Queue != nil {
				_ = protoapp.EnqueueValidationDispatcher(cleanup, s.Queue)
			}
		}
		if revoked {
			err = &proton.Failure{Category: "session_unavailable", SafeMessage: "Proto session is unavailable; retry after mailbox validation.", Retryable: true, Cause: err}
		}
		// Partial or stale reads must not become successful empty/history results.
		return proton.FetchResult{}, err
	}
	return result, nil
}

func (s *Service) refreshMailboxSession(ctx context.Context, resourceID uint, revision uint64, observed *proton.Session, refresh func(context.Context, *proton.Session) error) error {
	if observed == nil || refresh == nil {
		return domain.ErrInvalidResource
	}
	var next proton.Session
	err := s.WithSession(ctx, resourceID, revision, func(callCtx context.Context, current *proton.Session, persist func(proton.Session) error) error {
		if !sameSessionMailbox(current, observed) {
			return domain.ErrInvalidClaim
		}
		rotated := current.AccessToken != observed.AccessToken || current.RefreshToken != observed.RefreshToken
		if rotated && (current.ExpiresAt.IsZero() || current.ExpiresAt.After(s.Now().UTC())) {
			next = *current
			return nil
		}
		updated := *current
		if err := refresh(callCtx, &updated); err != nil {
			return err
		}
		if err := persist(updated); err != nil {
			return err
		}
		next = updated
		return nil
	})
	if err == nil {
		*observed = next
	}
	return err
}

func sameSessionMailbox(left, right *proton.Session) bool {
	return left != nil && right != nil && left.UID == right.UID &&
		reflect.DeepEqual(left.Addresses, right.Addresses) && reflect.DeepEqual(left.UserKeys, right.UserKeys)
}

func (s *Service) acquireProtocolProxy(ctx context.Context, resourceID uint, requestID string, purpose proxydomain.ProxyPurpose) (*proxyapp.ProxyConfig, error) {
	if s.Proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	proxy, err := s.Proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key: fmt.Sprintf("proto:%d", resourceID), IPVersion: proxydomain.ProxyIPAuto,
		Purpose: purpose, AllowSystemFallback: true, RequestID: strings.TrimSpace(requestID),
	})
	if err != nil || proxy == nil {
		return nil, &proton.Failure{Category: "proxy", SafeMessage: "Proto proxy service is temporarily unavailable.", Retryable: true, Cause: err}
	}
	return proxy, nil
}

func (s *Service) reportProtocolProxy(ctx context.Context, proxyID uint, err error) {
	if s.Proxies == nil || proxyID == 0 || ctx.Err() != nil {
		return
	}
	if err == nil {
		_ = s.Proxies.ReportSuccess(ctx, proxyID)
		return
	}
	var failure *proton.Failure
	if errors.As(err, &failure) && failure.ProxyFailure {
		_ = s.Proxies.ReportFailure(ctx, proxyID, "Proto proxy connection failed.")
	}
}

func observeProtoProtocol(operation string, started time.Time, err error) {
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = "canceled"
		}
	}
	platform.ObserveExternalService("proto", operation, outcome, started)
}
