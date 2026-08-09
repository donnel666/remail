package msacl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const recoveryCodeMailLeaseDuration = 10 * time.Minute

// ErrRecoveryLeaseOwnershipLost means the lease row disappeared or was
// replaced before the owner could mark its code as sent. It is distinct from
// a database/transport failure: only this fenced ownership loss is a normal
// mailbox-busy condition.
var ErrRecoveryLeaseOwnershipLost = errors.New("recovery lease ownership lost")

type RecoveryLeaseStore interface {
	Claim(ctx context.Context, normalizedKey string, resourceID uint, leaseUntil time.Time) (claimToken string, claimed bool, err error)
	MarkSent(ctx context.Context, normalizedKey, claimToken string, sentAt time.Time) error
	Release(ctx context.Context, normalizedKey, claimToken string) error
}

type recoveryLeaseContextKey struct{}
type concreteRecoveryLeaseKeyContext struct{}

type recoveryLeaseScope struct {
	resourceID uint
	mask       string
	mu         sync.Mutex
	leases     map[string]*codeMailLease
}

type codeMailLease struct {
	store      RecoveryLeaseStore
	scope      *recoveryLeaseScope
	key        string
	claimToken string
	sent       bool
}

var (
	recoveryLeaseStoreMu sync.RWMutex
	recoveryLeaseStore   RecoveryLeaseStore
)

func SetRecoveryLeaseStore(store RecoveryLeaseStore) {
	recoveryLeaseStoreMu.Lock()
	recoveryLeaseStore = store
	recoveryLeaseStoreMu.Unlock()
}

func WithRecoveryLeaseScope(ctx context.Context, resourceID uint, maskedProof string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope, _ := ctx.Value(recoveryLeaseContextKey{}).(*recoveryLeaseScope); scope != nil && scope.resourceID == resourceID {
		mask := normalizeRecoveryMask(maskedProof)
		if mask != "" {
			scope.mu.Lock()
			if scope.mask == "" {
				scope.mask = mask
			}
			scope.mu.Unlock()
		}
		return ctx
	}
	return context.WithValue(ctx, recoveryLeaseContextKey{}, &recoveryLeaseScope{
		resourceID: resourceID,
		mask:       normalizeRecoveryMask(maskedProof),
		leases:     make(map[string]*codeMailLease),
	})
}

// WithConcreteRecoveryLeaseKeys opts a validation flow into concrete-recipient
// lease keys. The independent weekly alias workflow does not set this marker
// and therefore keeps its historical masked-key scheduling behavior.
func WithConcreteRecoveryLeaseKeys(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, concreteRecoveryLeaseKeyContext{}, true)
}

func concreteRecoveryLeaseKeysEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(concreteRecoveryLeaseKeyContext{}).(bool)
	return enabled
}

func recoveryMaskFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if scope, _ := ctx.Value(recoveryLeaseContextKey{}).(*recoveryLeaseScope); scope != nil {
		scope.mu.Lock()
		defer scope.mu.Unlock()
		return scope.mask
	}
	return ""
}

func claimCodeMailLease(ctx context.Context, maskedProof string, concreteMailbox ...string) (*codeMailLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope, _ := ctx.Value(recoveryLeaseContextKey{}).(*recoveryLeaseScope)
	if !concreteRecoveryLeaseKeysEnabled(ctx) {
		concreteMailbox = nil
		// Preserve the legacy weekly/ordinary authorization boundary: a full
		// proof address was never a lease key outside validation, only a mask.
		maskedProof = normalizeRecoveryMask(maskedProof)
	}
	key := recoveryCodeMailLeaseKey(maskedProof, concreteMailbox...)
	if scope == nil || scope.resourceID == 0 || key == "" {
		return &codeMailLease{}, nil
	}

	scope.mu.Lock()
	defer scope.mu.Unlock()
	if lease := scope.leases[key]; lease != nil {
		if lease.sent {
			return nil, newRecoveryMailboxLeaseBusyError(ctx, "相同辅助邮箱已有验证码邮件尚未处理", key)
		}
		return lease, nil
	}
	recoveryLeaseStoreMu.RLock()
	store := recoveryLeaseStore
	recoveryLeaseStoreMu.RUnlock()
	if store == nil {
		lease := &codeMailLease{scope: scope, key: key}
		scope.leases[key] = lease
		return lease, nil
	}
	settings := runtimeconfig.Snapshot()
	leaseDuration := settings.Duration("recovery_code_lease_minutes", recoveryCodeMailLeaseDuration, time.Minute, 1)
	minimumLease := settings.Duration("password_recovery_code_wait_seconds", passwordRecoveryDefaultCodeWait, time.Second, 1) + 30*time.Second
	if leaseDuration < minimumLease {
		leaseDuration = minimumLease
	}
	claimToken, claimed, err := store.Claim(ctx, key, scope.resourceID, time.Now().UTC().Add(leaseDuration))
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, newRecoveryMailboxLeaseBusyError(ctx, "相同辅助邮箱已有验证码邮件正在处理", key)
	}
	lease := &codeMailLease{store: store, scope: scope, key: key, claimToken: claimToken}
	scope.leases[key] = lease
	return lease, nil
}

func newRecoveryMailboxLeaseBusyError(ctx context.Context, message, key string) error {
	status := AuthStatusRateLimited
	if concreteRecoveryLeaseKeysEnabled(ctx) {
		status = AuthStatusRecoveryMailboxBusy
	}
	return newAuthError(message, status, key)
}

// recoveryCodeMailLeaseKey serializes only flows that can consume the same
// verification message. A known concrete recipient is authoritative; the
// masked Microsoft proof is the conservative fallback when the recipient
// cannot yet be resolved safely.
func recoveryCodeMailLeaseKey(maskedProof string, concreteMailbox ...string) string {
	if mailbox := normalizeRecoveryMailbox(maskedProof); mailbox != "" {
		return mailbox
	}
	mask := normalizeRecoveryMask(maskedProof)
	for _, candidate := range concreteMailbox {
		if mailbox := normalizeRecoveryMailbox(candidate); mailbox != "" {
			if mask != "" && !mailboxMatchesMasked(mask, mailbox) {
				continue
			}
			return mailbox
		}
	}
	return mask
}

// RecoveryLeaseReleaser returns the post-persistence cleanup for leases claimed
// in this protocol scope. A nil result means the flow did not claim a key.
func RecoveryLeaseReleaser(ctx context.Context) func(context.Context) error {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(recoveryLeaseContextKey{}).(*recoveryLeaseScope)
	if scope == nil {
		return nil
	}
	scope.mu.Lock()
	hasLeases := len(scope.leases) > 0
	scope.mu.Unlock()
	if !hasLeases {
		return nil
	}
	return func(operationCtx context.Context) error {
		scope.mu.Lock()
		leases := make([]*codeMailLease, 0, len(scope.leases))
		for _, lease := range scope.leases {
			leases = append(leases, lease)
		}
		scope.mu.Unlock()
		var releaseErr error
		for _, lease := range leases {
			releaseErr = errors.Join(releaseErr, lease.release(operationCtx))
		}
		return releaseErr
	}
}

func (l *codeMailLease) markSent(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if l.scope != nil {
		l.scope.mu.Lock()
		defer l.scope.mu.Unlock()
	}
	if l.sent {
		return newRecoveryMailboxLeaseBusyError(ctx, "相同辅助邮箱已有验证码邮件尚未处理", l.key)
	}
	if l.store == nil {
		l.sent = true
		return nil
	}
	// ponytail: callers that can carry RecoveryLeaseReleaser release after their
	// fenced persistence step; other sent flows conservatively expire by TTL.
	if err := l.store.MarkSent(ctx, l.key, l.claimToken, time.Now().UTC()); err != nil {
		status := AuthStatusRateLimited
		message := "辅助邮箱验证码发送租约已失效"
		if concreteRecoveryLeaseKeysEnabled(ctx) {
			status = AuthStatusRequestError
			message = "辅助邮箱验证码发送租约更新失败"
			if errors.Is(err, ErrRecoveryLeaseOwnershipLost) {
				status = AuthStatusRecoveryMailboxBusy
				message = "辅助邮箱验证码发送租约已失效"
			}
		}
		return wrapAuthError(message, status, err, l.key)
	}
	l.sent = true
	return nil
}

func (l *codeMailLease) releaseIfUnsent(ctx context.Context) {
	if l == nil {
		return
	}
	if l.scope != nil {
		l.scope.mu.Lock()
		sent := l.sent
		l.scope.mu.Unlock()
		if sent {
			return
		}
	} else if l.sent {
		return
	}
	if err := l.release(ctx); err != nil {
		logWarning("释放未发信辅助邮箱租约失败: %v", err)
	}
}

// releaseCompletedCodeMailLease lets the validation-only flow reuse a consumed
// concrete recipient immediately. Mask-only and independent alias leases keep
// their post-persistence/TTL release boundary.
func releaseCompletedCodeMailLease(ctx context.Context, lease *codeMailLease) {
	if lease == nil || !concreteRecoveryLeaseKeysEnabled(ctx) || normalizeRecoveryMailbox(lease.key) == "" {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := lease.release(releaseCtx); err != nil {
		logWarning("释放已处理辅助邮箱验证码租约失败: %v", err)
	}
}

func (l *codeMailLease) release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if l.store != nil {
		if err := l.store.Release(ctx, l.key, l.claimToken); err != nil {
			return err
		}
	}
	if l.scope != nil {
		l.scope.mu.Lock()
		if l.scope.leases[l.key] == l {
			delete(l.scope.leases, l.key)
		}
		l.scope.mu.Unlock()
	}
	return nil
}

func normalizeRecoveryMask(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	local, domain, ok := strings.Cut(address, "@")
	domain = strings.Trim(domain, ".")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || !strings.Contains(local, "*") || strings.ContainsAny(address, " \t\r\n") {
		return ""
	}
	return fmt.Sprintf("%s@%s", local, domain)
}
