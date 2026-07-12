package api

import (
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
)

type adminTaskListResponse struct {
	Items     []adminTaskViewResponse `json:"items"`
	Total     int64                   `json:"total"`
	Succeeded int64                   `json:"succeeded"`
	Offset    int                     `json:"offset"`
	Limit     int                     `json:"limit"`
}

type adminTaskViewResponse struct {
	TaskID             string                     `json:"taskId"`
	BizType            string                     `json:"bizType"`
	BizID              uint64                     `json:"bizId"`
	Kind               string                     `json:"kind"`
	Status             string                     `json:"status"`
	Attempts           int                        `json:"attempts"`
	MaxAttempts        int                        `json:"maxAttempts"`
	RemainingAttempts  int                        `json:"remainingAttempts"`
	CredentialRevision *uint64                    `json:"credentialRevision"`
	QueuedAt           time.Time                  `json:"queuedAt"`
	StartedAt          *time.Time                 `json:"startedAt"`
	FinishedAt         *time.Time                 `json:"finishedAt"`
	UpdatedAt          time.Time                  `json:"updatedAt"`
	Progress           *adminTaskProgressResponse `json:"progress"`
}

type adminTaskProgressResponse struct {
	Total        int64                          `json:"total"`
	Processed    int64                          `json:"processed"`
	Succeeded    int64                          `json:"succeeded"`
	Skipped      int64                          `json:"skipped"`
	Failed       int64                          `json:"failed"`
	ReasonCounts []adminTaskReasonCountResponse `json:"reasonCounts"`
}

type adminTaskReasonCountResponse struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

func adminTaskListDTO(result *governanceapp.AdminTaskListResult) adminTaskListResponse {
	items := make([]adminTaskViewResponse, len(result.Items))
	for i := range result.Items {
		items[i] = adminTaskDTO(result.Items[i])
	}
	return adminTaskListResponse{
		Items:     items,
		Total:     result.Total,
		Succeeded: result.Succeeded,
		Offset:    result.Offset,
		Limit:     result.Limit,
	}
}

func adminTaskDTO(task governanceapp.AdminTaskView) adminTaskViewResponse {
	remaining := task.MaxAttempts - task.Attempts
	if remaining < 0 {
		remaining = 0
	}
	response := adminTaskViewResponse{
		TaskID:             task.TaskID(),
		BizType:            task.BizType,
		BizID:              task.BizID,
		Kind:               task.Kind,
		Status:             task.Status,
		Attempts:           task.Attempts,
		MaxAttempts:        task.MaxAttempts,
		RemainingAttempts:  remaining,
		CredentialRevision: task.CredentialRevision,
		QueuedAt:           task.QueuedAt,
		StartedAt:          task.StartedAt,
		FinishedAt:         task.FinishedAt,
		UpdatedAt:          task.UpdatedAt,
	}
	if task.Progress != nil {
		reasons := make([]adminTaskReasonCountResponse, len(task.Progress.ReasonCounts))
		for i := range task.Progress.ReasonCounts {
			reasons[i] = adminTaskReasonCountResponse{
				Reason: task.Progress.ReasonCounts[i].Reason,
				Count:  task.Progress.ReasonCounts[i].Count,
			}
		}
		response.Progress = &adminTaskProgressResponse{
			Total:        task.Progress.Total,
			Processed:    task.Progress.Processed,
			Succeeded:    task.Progress.Succeeded,
			Skipped:      task.Progress.Skipped,
			Failed:       task.Progress.Failed,
			ReasonCounts: reasons,
		}
	}
	return response
}
