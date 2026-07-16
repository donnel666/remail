package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type taskViewRepoStub struct {
	exists  bool
	items   []AdminTaskView
	total   int64
	success int64
	found   *AdminTaskView
	err     error
}

func (s *taskViewRepoStub) MicrosoftResourceExists(context.Context, uint) (bool, error) {
	return s.exists, s.err
}

func (s *taskViewRepoStub) DomainResourceExists(context.Context, uint) (bool, error) {
	return s.exists, s.err
}

func (s *taskViewRepoStub) ListForMicrosoftResource(context.Context, AdminTaskListFilter) ([]AdminTaskView, int64, int64, error) {
	return s.items, s.total, s.success, s.err
}

func (s *taskViewRepoStub) ListForDomainResource(context.Context, AdminTaskListFilter) ([]AdminTaskView, int64, int64, error) {
	return s.items, s.total, s.success, s.err
}

func (s *taskViewRepoStub) FindByRef(context.Context, AdminTaskRef) (*AdminTaskView, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.found == nil {
		return nil, ErrAdminTaskNotFound
	}
	return s.found, nil
}

func TestParseAdminTaskRefRequiresQualifiedNumericSource(t *testing.T) {
	ref, err := ParseAdminTaskRef("token:42")
	require.NoError(t, err)
	require.Equal(t, AdminTaskSourceToken, ref.Source)
	require.Equal(t, uint64(42), ref.ID)
	require.Equal(t, "token:42", ref.String())

	for _, value := range []string{"42", "validation:42", "token:", "unknown:1", "token:0", "token:1:2", "token:secret"} {
		_, err := ParseAdminTaskRef(value)
		require.ErrorIs(t, err, ErrInvalidAdminTaskQuery, value)
	}
}

func TestAdminTaskQueryServiceListUsesStableDefaults(t *testing.T) {
	now := time.Now().UTC()
	repo := &taskViewRepoStub{
		exists: true,
		items: []AdminTaskView{{
			Ref:       AdminTaskRef{Source: AdminTaskSourceToken, ID: 9},
			BizType:   AdminTaskBizMicrosoftResource,
			BizID:     7,
			Kind:      AdminTaskKindToken,
			Status:    AdminTaskStatusQueued,
			QueuedAt:  now,
			UpdatedAt: now,
		}},
		total: 1,
	}
	service := NewAdminTaskQueryService(repo)
	result, err := service.List(context.Background(), AdminTaskListFilter{
		BizType: AdminTaskBizMicrosoftResource,
		BizID:   7,
	})
	require.NoError(t, err)
	require.Equal(t, AdminTaskDefaultLimit, result.Limit)
	require.Len(t, result.Items, 1)
	require.Equal(t, "token:9", result.Items[0].TaskID())

	_, err = service.List(context.Background(), AdminTaskListFilter{BizType: AdminTaskBizMicrosoftResource, BizID: 7, Status: "done"})
	require.ErrorIs(t, err, ErrInvalidAdminTaskQuery)
}

func TestAdminTaskQueryServiceDoesNotReturnPartialResults(t *testing.T) {
	service := NewAdminTaskQueryService(&taskViewRepoStub{exists: true, err: errors.New("database unavailable")})
	_, err := service.List(context.Background(), AdminTaskListFilter{
		BizType: AdminTaskBizMicrosoftResource,
		BizID:   11,
	})
	require.ErrorIs(t, err, ErrAdminTaskUnavailable)
}
