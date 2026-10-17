package app

import (
	"context"
	"errors"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

// DaumMailFetchPort reads credentials inside the Daum implementation. Only
// the global resource ID and credential revision cross this boundary. Permanent
// failures are fenced and persisted by Daum before this port returns them.
type DaumMailFetchPort interface {
	FetchDaumMessages(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error)
}

type PermanentDaumFetchFailure struct {
	ResourceID         uint
	CredentialRevision uint64
	OrderNo            string
	RequestID          string
	Category           string
	SafeMessage        string
}

type PermanentDaumFetchFailurePort interface {
	HandlePermanentDaumFetchFailure(context.Context, PermanentDaumFetchFailure) error
}

func (uc *UseCase) SetDaumMailFetchPort(port DaumMailFetchPort) {
	if uc != nil {
		uc.daumFetch = port
	}
}

func (uc *UseCase) SetPermanentDaumFetchFailurePort(port PermanentDaumFetchFailurePort) {
	if uc != nil {
		uc.daumFailures = port
	}
}

func (uc *UseCase) fetchDaumMessages(ctx context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	if uc == nil || uc.daumFetch == nil {
		return nil, domain.ErrMailServiceUnavailable
	}
	if request.Scope.AllocationType != domain.ResourceTypeDaum || request.Scope.EmailResourceID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	result, err := uc.daumFetch.FetchDaumMessages(ctx, request)
	if err != nil {
		var failure *MailFetchFailure
		if errors.As(err, &failure) && !failure.Retryable && uc.daumFailures != nil {
			err = errors.Join(err, uc.daumFailures.HandlePermanentDaumFetchFailure(ctx, PermanentDaumFetchFailure{
				ResourceID: request.Scope.EmailResourceID, CredentialRevision: request.Scope.CredentialRevision,
				OrderNo: request.Scope.OrderNo, RequestID: request.RequestID,
				Category: failure.Category, SafeMessage: failure.SafeMessage,
			}))
		}
		return nil, err
	}
	if result == nil {
		return nil, domain.ErrMailServiceUnavailable
	}
	for i, message := range result.Messages {
		if message.ResourceType != domain.ResourceTypeDaum || message.EmailResourceID != request.Scope.EmailResourceID {
			return nil, domain.ErrInvalidRequest
		}
		if message.CredentialRevision != 0 && message.CredentialRevision != request.Scope.CredentialRevision {
			return nil, domain.ErrResourceFetchCredentialChanged
		}
		result.Messages[i].CredentialRevision = request.Scope.CredentialRevision
	}
	return result, nil
}

func (uc *UseCase) assertDaumCredentialRevision(ctx context.Context, resourceID uint, revision uint64) error {
	repo, ok := uc.repo.(interface {
		AssertDaumCredentialRevision(context.Context, uint, uint64) error
	})
	if !ok {
		return domain.ErrMailServiceUnavailable
	}
	return repo.AssertDaumCredentialRevision(ctx, resourceID, revision)
}
