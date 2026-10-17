package app

import (
	"context"
	"errors"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

type DaumResourceFetchRepository interface {
	AssertDaumResourceFetchFence(context.Context, uint, uint64, uint64) error
	CompleteDaumResourceFetch(context.Context, uint, uint64, uint64, int, int, int, time.Time, *governancedomain.SystemLog) error
}

func (uc *resourceTaskUseCase) processDaumResourceFetch(ctx context.Context, job domain.ResourceFetchJob, _ domain.ResourceFetchScope) error {
	repo, ok := uc.adminRepo.(DaumResourceFetchRepository)
	if !ok || uc.messages == nil || uc.messages.daumFetch == nil {
		return uc.cancelResourceFetch(ctx, job, "Daum mail service is not configured.", "resource_unavailable")
	}
	fence := func(txCtx context.Context) error {
		return repo.AssertDaumResourceFetchFence(txCtx, job.ResourceID, job.Generation, job.ExpectedCredentialRevision)
	}
	fetched, err := uc.messages.fetchDaumMessages(ctx, FetchMessagesRequest{
		Scope:   OrderScope{AllocationType: domain.ResourceTypeDaum, EmailResourceID: job.ResourceID, CredentialRevision: job.ExpectedCredentialRevision},
		UntilAt: dereferenceTime(job.UntilAt, uc.now()), RequestID: job.RequestID, FullHistory: true,
	})
	if err != nil {
		return uc.daumResourceFetchFailure(ctx, job, err)
	}
	if fetched == nil {
		return uc.daumResourceFetchFailure(ctx, job, domain.ErrMailServiceUnavailable)
	}
	for _, message := range fetched.Messages {
		if message.ResourceType != domain.ResourceTypeDaum || message.EmailResourceID != job.ResourceID {
			return uc.daumResourceFetchFailure(ctx, job, domain.ErrInvalidRequest)
		}
		if message.CredentialRevision != job.ExpectedCredentialRevision {
			return uc.daumResourceFetchFailure(ctx, job, domain.ErrResourceFetchCredentialChanged)
		}
	}
	stored, matched, _, err := uc.messages.ingestFetchedMessagesForResourcesWithFence(ctx, fetched.Messages, domain.ResourceTypeDaum, []uint{job.ResourceID}, fence)
	if err != nil {
		return uc.daumResourceFetchFailure(ctx, job, err)
	}
	if fetched.CommitCursor != nil {
		if err := fetched.CommitCursor(ctx, fence); err != nil {
			return uc.daumResourceFetchFailure(ctx, job, err)
		}
	}
	err = repo.CompleteDaumResourceFetch(ctx, job.ResourceID, job.Generation, job.ExpectedCredentialRevision, len(fetched.Messages), stored, matched, uc.now(), resourceFetchSystemLog(job, "info", "resource_fetch_succeeded", "Daum resource mail fetch completed.", ""))
	if err != nil {
		return uc.daumResourceFetchFailure(ctx, job, err)
	}
	return nil
}

func (uc *resourceTaskUseCase) daumResourceFetchFailure(ctx context.Context, job domain.ResourceFetchJob, err error) error {
	if errors.Is(err, domain.ErrResourceFetchInvalidClaim) {
		return nil
	}
	if errors.Is(err, domain.ErrResourceFetchCredentialChanged) || errors.Is(err, domain.ErrResourceFetchDeleted) || errors.Is(err, domain.ErrResourceFetchNotFound) {
		return uc.cancelResourceFetch(ctx, job, "Daum resource changed while mail fetch was running.", "credential_changed")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return uc.releaseResourceFetchInfrastructure(ctx, job.ResourceID, job.Generation, err)
	}
	var failure *MailFetchFailure
	if errors.As(err, &failure) {
		return uc.retryResourceFetch(ctx, job, "Daum mail fetch failed.", failure.Category, failure.Retryable, err)
	}
	return uc.retryResourceFetch(ctx, job, "Daum mail service is temporarily unavailable.", "request", true, err)
}
