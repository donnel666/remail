package app

import (
	"context"
	"errors"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

type ProtoResourceFetchRepository interface {
	AssertProtoResourceFetchFence(context.Context, uint, uint64, uint64) error
	CompleteProtoResourceFetch(context.Context, uint, uint64, uint64, int, int, int, time.Time, *governancedomain.SystemLog) error
}

func (uc *resourceTaskUseCase) processProtoResourceFetch(ctx context.Context, job domain.ResourceFetchJob, scope domain.ResourceFetchScope) error {
	repo, ok := uc.adminRepo.(ProtoResourceFetchRepository)
	if !ok || uc.messages == nil || uc.messages.protoFetch == nil {
		return uc.cancelResourceFetch(ctx, job, "Proto mail service is not configured.", "resource_unavailable")
	}
	fetched, err := uc.messages.fetchProtoMessages(ctx, FetchMessagesRequest{
		Scope: OrderScope{
			AllocationType: domain.ResourceTypeProto, EmailResourceID: job.ResourceID,
			Recipient: scope.EmailAddress, CredentialRevision: job.ExpectedCredentialRevision,
		},
		UntilAt: dereferenceTime(job.UntilAt, uc.now()), RequestID: job.RequestID, FullHistory: true,
	})
	if err != nil {
		return uc.protoResourceFetchFailure(ctx, job, err)
	}
	fence := func(txCtx context.Context) error {
		return repo.AssertProtoResourceFetchFence(txCtx, job.ResourceID, job.Generation, job.ExpectedCredentialRevision)
	}
	stored, matched, _, err := uc.messages.ingestFetchedMessagesForResourcesWithFence(
		ctx, fetched.Messages, domain.ResourceTypeProto, []uint{job.ResourceID}, fence,
	)
	if err != nil {
		return uc.protoResourceFetchFailure(ctx, job, err)
	}
	if fetched.CommitCursor != nil {
		if err := fetched.CommitCursor(ctx, fence); err != nil {
			return uc.protoResourceFetchFailure(ctx, job, err)
		}
	}
	err = repo.CompleteProtoResourceFetch(ctx, job.ResourceID, job.Generation, job.ExpectedCredentialRevision,
		len(fetched.Messages), stored, matched, uc.now(),
		resourceFetchSystemLog(job, "info", "resource_fetch_succeeded", "Proto resource mail fetch completed.", ""))
	if err != nil {
		return uc.protoResourceFetchFailure(ctx, job, err)
	}
	return nil
}

func (uc *resourceTaskUseCase) protoResourceFetchFailure(ctx context.Context, job domain.ResourceFetchJob, err error) error {
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if errors.Is(err, domain.ErrResourceFetchCredentialChanged) || errors.Is(err, domain.ErrResourceFetchDeleted) || errors.Is(err, domain.ErrResourceFetchNotFound) {
		return uc.cancelResourceFetch(ctx, job, "Proto resource changed while mail fetch was running.", "credential_changed")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
	var failure *MailFetchFailure
	if errors.As(err, &failure) {
		return uc.retryResourceFetch(ctx, job, "Proto mail fetch failed.", failure.Category, failure.Retryable, err)
	}
	return uc.retryResourceFetch(ctx, job, "Proto mail service is temporarily unavailable.", "request", true, err)
}
