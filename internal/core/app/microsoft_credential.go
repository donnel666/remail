package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
)

var (
	ErrMicrosoftCredentialNotFound = errors.New("core: microsoft credential resource not found")
	ErrMicrosoftCredentialDeleted  = errors.New("core: microsoft credential resource deleted")
	ErrMicrosoftCredentialChanged  = errors.New("core: microsoft credential revision changed")
)

// MicrosoftCredentialScope is private in-process data for Microsoft protocol
// work. It must never be serialized into HTTP responses, task payloads, or
// logs.
type MicrosoftCredentialScope struct {
	ResourceID         uint
	Status             string
	EmailAddress       string
	ClientID           string
	RefreshToken       string
	CredentialRevision uint64
}

type MicrosoftTokenRefreshSuccess struct {
	ResourceID                 uint
	ExpectedCredentialRevision uint64
	ClientID                   string
	RefreshToken               string
	RequestID                  string
	Now                        time.Time
}

type MicrosoftTokenRefreshFailure struct {
	ResourceID                 uint
	ExpectedCredentialRevision uint64
	SafeError                  string
	RequestID                  string
}

type MicrosoftFetchRefreshTokenRotation struct {
	ResourceID                 uint
	ExpectedCredentialRevision uint64
	RefreshToken               string
	Now                        time.Time
}

// MicrosoftCredentialPort is the narrow Core-owned boundary used by other
// bounded contexts for Microsoft protocol work.
type MicrosoftCredentialPort interface {
	LockMicrosoftCredentialScope(ctx context.Context, resourceID uint) (*MicrosoftCredentialScope, error)
	MaxMicrosoftResourceID(ctx context.Context) (uint, error)
	FindNextMicrosoftCredentialScope(ctx context.Context, afterID, maxID uint) (*MicrosoftCredentialScope, error)
	ApplyMicrosoftTokenRefreshSuccess(ctx context.Context, update MicrosoftTokenRefreshSuccess) error
	ApplyMicrosoftTokenRefreshFailure(ctx context.Context, update MicrosoftTokenRefreshFailure) error
	ApplyMicrosoftFetchRefreshToken(ctx context.Context, update MicrosoftFetchRefreshTokenRotation) error
}

type MicrosoftCredentialRepository interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
	LockAdminMicrosoft(ctx context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error)
	MaxMicrosoftResourceID(ctx context.Context) (uint, error)
	FindNextMicrosoft(ctx context.Context, afterID, maxID uint) (*domain.MicrosoftResource, error)
	SaveAdminMicrosoft(ctx context.Context, root *domain.EmailResource, resource *domain.MicrosoftResource, expectedVersion uint64) error
}

// MicrosoftCredentialService keeps credential revisions, Core root versions,
// and protocol diagnostics inside the Core consistency boundary. If the caller
// supplies a transaction through context, the repository joins that transaction.
type MicrosoftCredentialService struct {
	repo MicrosoftCredentialRepository
}

func NewMicrosoftCredentialService(repo MicrosoftCredentialRepository) *MicrosoftCredentialService {
	return &MicrosoftCredentialService{repo: repo}
}

func (s *MicrosoftCredentialService) LockMicrosoftCredentialScope(ctx context.Context, resourceID uint) (*MicrosoftCredentialScope, error) {
	if s == nil || s.repo == nil || resourceID == 0 {
		return nil, ErrMicrosoftCredentialNotFound
	}
	var scope *MicrosoftCredentialScope
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		_, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return microsoftCredentialError(err)
		}
		scope = microsoftCredentialScope(resource)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scope, nil
}

func (s *MicrosoftCredentialService) MaxMicrosoftResourceID(ctx context.Context) (uint, error) {
	if s == nil || s.repo == nil {
		return 0, ErrMicrosoftCredentialNotFound
	}
	return s.repo.MaxMicrosoftResourceID(ctx)
}

func (s *MicrosoftCredentialService) FindNextMicrosoftCredentialScope(ctx context.Context, afterID, maxID uint) (*MicrosoftCredentialScope, error) {
	if s == nil || s.repo == nil || maxID == 0 || afterID >= maxID {
		return nil, nil
	}
	resource, err := s.repo.FindNextMicrosoft(ctx, afterID, maxID)
	if err != nil {
		return nil, microsoftCredentialError(err)
	}
	return microsoftCredentialScope(resource), nil
}

func (s *MicrosoftCredentialService) ApplyMicrosoftTokenRefreshSuccess(ctx context.Context, update MicrosoftTokenRefreshSuccess) error {
	return s.mutate(ctx, update.ResourceID, update.ExpectedCredentialRevision, func(resource *domain.MicrosoftResource) (bool, error) {
		if resource.Status == domain.MicrosoftStatusDeleted {
			return false, ErrMicrosoftCredentialDeleted
		}
		now := credentialTime(update.Now)
		credentialsChanged := false
		if value := strings.TrimSpace(update.ClientID); value != "" && value != resource.ClientID {
			resource.ClientID = value
			credentialsChanged = true
		}
		if value := strings.TrimSpace(update.RefreshToken); value != "" && value != resource.RefreshToken {
			resource.RefreshToken = value
			credentialsChanged = true
		}
		if credentialsChanged {
			resource.CredentialRevision++
			resource.CredentialUpdatedAt = now
		}
		resource.LastSafeError = ""
		resource.TokenLastRefreshedAt = &now
		resource.TokenLastRequestID = strings.TrimSpace(update.RequestID)
		return true, nil
	})
}

func (s *MicrosoftCredentialService) ApplyMicrosoftTokenRefreshFailure(ctx context.Context, update MicrosoftTokenRefreshFailure) error {
	return s.mutate(ctx, update.ResourceID, update.ExpectedCredentialRevision, func(resource *domain.MicrosoftResource) (bool, error) {
		// A diagnostic cannot revive or otherwise mutate a deleted resource.
		// The durable task owner still records its own terminal result.
		if resource.Status == domain.MicrosoftStatusDeleted {
			return false, nil
		}
		resource.LastSafeError = strings.TrimSpace(update.SafeError)
		resource.TokenLastRequestID = strings.TrimSpace(update.RequestID)
		return true, nil
	})
}

func (s *MicrosoftCredentialService) ApplyMicrosoftFetchRefreshToken(ctx context.Context, update MicrosoftFetchRefreshTokenRotation) error {
	return s.mutate(ctx, update.ResourceID, update.ExpectedCredentialRevision, func(resource *domain.MicrosoftResource) (bool, error) {
		if resource.Status == domain.MicrosoftStatusDeleted {
			return false, ErrMicrosoftCredentialDeleted
		}
		refreshToken := strings.TrimSpace(update.RefreshToken)
		if refreshToken == "" || refreshToken == strings.TrimSpace(resource.RefreshToken) {
			return false, nil
		}
		resource.RefreshToken = refreshToken
		resource.CredentialRevision++
		resource.CredentialUpdatedAt = credentialTime(update.Now)
		return true, nil
	})
}

func (s *MicrosoftCredentialService) mutate(
	ctx context.Context,
	resourceID uint,
	expectedCredentialRevision uint64,
	apply func(*domain.MicrosoftResource) (bool, error),
) error {
	if s == nil || s.repo == nil || resourceID == 0 || apply == nil {
		return ErrMicrosoftCredentialNotFound
	}
	return s.repo.WithTx(ctx, func(txCtx context.Context) error {
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return microsoftCredentialError(err)
		}
		if resource.CredentialRevision != expectedCredentialRevision {
			return ErrMicrosoftCredentialChanged
		}
		changed, err := apply(resource)
		if err != nil || !changed {
			return err
		}
		if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, root.Version); err != nil {
			return microsoftCredentialError(err)
		}
		return nil
	})
}

func microsoftCredentialScope(resource *domain.MicrosoftResource) *MicrosoftCredentialScope {
	if resource == nil {
		return nil
	}
	return &MicrosoftCredentialScope{
		ResourceID:         resource.ID,
		Status:             string(resource.Status),
		EmailAddress:       resource.EmailAddress,
		ClientID:           resource.ClientID,
		RefreshToken:       resource.RefreshToken,
		CredentialRevision: resource.CredentialRevision,
	}
}

func microsoftCredentialError(err error) error {
	switch {
	case errors.Is(err, domain.ErrResourceNotFound):
		return ErrMicrosoftCredentialNotFound
	case errors.Is(err, domain.ErrResourceVersionConflict):
		return ErrMicrosoftCredentialChanged
	default:
		return err
	}
}

func credentialTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
