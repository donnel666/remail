package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
)

// CoreTaskQueryAdapter is the Governance anti-corruption adapter for Core's
// consumer-defined TaskQueryPort. It exposes only bounded safe summaries.
type CoreTaskQueryAdapter struct {
	tasks *governanceapp.AdminTaskQueryService
}

func NewCoreTaskQueryAdapter(tasks *governanceapp.AdminTaskQueryService) *CoreTaskQueryAdapter {
	return &CoreTaskQueryAdapter{tasks: tasks}
}

func (a *CoreTaskQueryAdapter) GetRecentByResourceID(ctx context.Context, resourceID uint, limit int) ([]coreapp.AdminTaskSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > governanceapp.AdminTaskMaxLimit {
		limit = governanceapp.AdminTaskMaxLimit
	}
	result, err := a.tasks.List(ctx, governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizMicrosoftResource,
		BizID:   resourceID,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]coreapp.AdminTaskSummary, len(result.Items))
	for i := range result.Items {
		items[i] = coreapp.AdminTaskSummary{
			TaskID:             result.Items[i].TaskID(),
			Kind:               result.Items[i].Kind,
			Status:             result.Items[i].Status,
			CredentialRevision: result.Items[i].CredentialRevision,
			UpdatedAt:          result.Items[i].UpdatedAt,
		}
	}
	return items, nil
}

var _ coreapp.TaskQueryPort = (*CoreTaskQueryAdapter)(nil)
