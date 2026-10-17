package app

import (
	"context"
	"errors"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type daumStreamTaskRepo struct {
	*resourceFetchProcessRepoStub
	canceled, failed  bool
	cleanupContextErr error
}

func (r *daumStreamTaskRepo) AssertDaumResourceFetchFence(_ context.Context, _ uint, _ uint64, revision uint64) error {
	r.fenced++
	r.revision = revision
	return r.fenceErr
}
func (r *daumStreamTaskRepo) CompleteDaumResourceFetch(_ context.Context, _ uint, _ uint64, revision uint64, fetched, stored, matched int, _ time.Time, _ *governancedomain.SystemLog) error {
	r.completed = true
	r.revision = revision
	r.fetched, r.stored, r.matched = fetched, stored, matched
	return nil
}
func (r *daumStreamTaskRepo) MarkResourceFetchCanceled(ctx context.Context, _ uint, _ uint64, _ string, _ time.Time, _ *governancedomain.SystemLog) error {
	r.canceled = true
	r.cleanupContextErr = ctx.Err()
	return nil
}
func (r *daumStreamTaskRepo) MarkResourceFetchFailure(ctx context.Context, _ uint, _ uint64, _ string, _ bool, _ time.Time, _ *governancedomain.SystemLog) (bool, error) {
	r.failed = true
	r.cleanupContextErr = ctx.Err()
	return false, nil
}
func (r *daumStreamTaskRepo) ReleaseResourceFetchInfrastructureFailure(ctx context.Context, _ uint, _ uint64, _ string, _ *governancedomain.SystemLog) (bool, error) {
	r.failed = true
	r.cleanupContextErr = ctx.Err()
	return false, nil
}

func newDaumStreamTask(t *testing.T, fetch daumFetchFunc) (*resourceTaskUseCase, *daumStreamTaskRepo, *appendFenceRepoStub) {
	t.Helper()
	repo := &daumStreamTaskRepo{resourceFetchProcessRepoStub: &resourceFetchProcessRepoStub{job: domain.ResourceFetchJob{ResourceType: domain.ResourceTypeDaum, ResourceID: 91, Generation: 3, ExpectedCredentialRevision: 7}}}
	facts := &appendFenceRepoStub{matchingRepoStub: &matchingRepoStub{}}
	messages := NewUseCase(facts, nil, nil, nil)
	messages.SetDaumMailFetchPort(fetch)
	return NewAdminResourceFetchUseCase(repo, nil, nil, messages, nil).task, repo, facts
}
func daumStreamMessage(id string) FetchedMessage {
	return FetchedMessage{EmailResourceID: 91, ResourceType: domain.ResourceTypeDaum, CredentialRevision: 7, Recipient: "one@hanmail.net", Sender: "sender@example.test", Subject: "message", Body: "body", MessageIDHeader: id, ReceivedAt: time.Now()}
}

func TestDaumAdminFetchCommitsOnlyAfterCompleteRead(t *testing.T) {
	var facts *appendFenceRepoStub
	task, repo, stored := newDaumStreamTask(t, func(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
		require.True(t, request.FullHistory)
		require.Nil(t, request.OnMessages)
		require.Empty(t, request.Scope.Recipient)
		require.Zero(t, facts.appended)
		return &FetchMessagesResult{Messages: []FetchedMessage{daumStreamMessage("one"), daumStreamMessage("two")}}, nil
	})
	facts = stored
	require.NoError(t, task.processDaumResourceFetch(context.Background(), repo.job, domain.ResourceFetchScope{}))
	require.True(t, repo.completed)
	require.Equal(t, 2, repo.fetched)
	require.Equal(t, 2, repo.stored)
	require.Equal(t, uint64(7), repo.revision)
	require.True(t, facts.appendInTx)
	require.Positive(t, repo.fenced)
}

func TestDaumAdminFetchRejectsPartialOrStaleResultsBeforeIngest(t *testing.T) {
	for _, kind := range []string{"resource", "type", "revision", "partial", "fence"} {
		t.Run(kind, func(t *testing.T) {
			task, repo, facts := newDaumStreamTask(t, func(_ context.Context, request FetchMessagesRequest) (*FetchMessagesResult, error) {
				require.Nil(t, request.OnMessages)
				message := daumStreamMessage("one")
				switch kind {
				case "resource":
					message.EmailResourceID++
				case "type":
					message.ResourceType = domain.ResourceTypeProto
				case "revision":
					message.CredentialRevision++
				}
				result := &FetchMessagesResult{Messages: []FetchedMessage{message}}
				if kind == "partial" {
					return result, &MailFetchFailure{Category: "incomplete_history", Retryable: true, Cause: errors.New("fixture partial read")}
				}
				return result, nil
			})
			if kind == "fence" {
				repo.fenceErr = domain.ErrResourceFetchCredentialChanged
			}
			require.NoError(t, task.processDaumResourceFetch(context.Background(), repo.job, domain.ResourceFetchScope{}))
			require.False(t, repo.completed)
			require.Zero(t, facts.appended)
			require.True(t, repo.canceled || repo.failed)
			require.NoError(t, repo.cleanupContextErr)
		})
	}
}
