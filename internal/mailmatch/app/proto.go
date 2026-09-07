package app

import (
	"context"
	"errors"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

// ProtoMailFetchPort reads credentials inside the Proto implementation. Only
// the global resource ID and credential revision cross this boundary.
type ProtoMailFetchPort interface {
	FetchProtoMessages(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error)
}

type PermanentProtoFetchFailure struct {
	ResourceID         uint
	CredentialRevision uint64
	OrderNo            string
	RequestID          string
	Category           string
	SafeMessage        string
}

type PermanentProtoFetchFailurePort interface {
	HandlePermanentProtoFetchFailure(context.Context, PermanentProtoFetchFailure) error
}

func (uc *UseCase) SetProtoMailFetchPort(port ProtoMailFetchPort) {
	if uc != nil {
		uc.protoFetch = port
	}
}

func (uc *UseCase) SetPermanentProtoFetchFailurePort(port PermanentProtoFetchFailurePort) {
	if uc != nil {
		uc.protoFailures = port
	}
}

func (uc *UseCase) fetchProtoMessages(ctx context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
	if uc == nil || uc.protoFetch == nil {
		return nil, domain.ErrMailServiceUnavailable
	}
	if request.Scope.AllocationType != domain.ResourceTypeProto || request.Scope.EmailResourceID == 0 {
		return nil, domain.ErrInvalidRequest
	}
	result, err := uc.protoFetch.FetchProtoMessages(ctx, request)
	if err != nil {
		var failure *MailFetchFailure
		if errors.As(err, &failure) && !failure.Retryable && uc.protoFailures != nil {
			err = errors.Join(err, uc.protoFailures.HandlePermanentProtoFetchFailure(ctx, PermanentProtoFetchFailure{
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
		if message.ResourceType != domain.ResourceTypeProto || message.EmailResourceID != request.Scope.EmailResourceID {
			return nil, domain.ErrInvalidRequest
		}
		result.Messages[i].CredentialRevision = request.Scope.CredentialRevision
	}
	return result, nil
}

func (uc *UseCase) assertProtoCredentialRevision(ctx context.Context, resourceID uint, revision uint64) error {
	repo, ok := uc.repo.(interface {
		AssertProtoCredentialRevision(context.Context, uint, uint64) error
	})
	if !ok {
		return domain.ErrMailServiceUnavailable
	}
	return repo.AssertProtoCredentialRevision(ctx, resourceID, revision)
}
