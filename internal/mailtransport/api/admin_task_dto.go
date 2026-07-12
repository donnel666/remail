package api

import (
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type adminMailTransportTaskAcceptedResponse struct {
	TaskID    string                             `json:"taskId"`
	RequestID string                             `json:"requestId"`
	Status    string                             `json:"status"`
	Accepted  int64                              `json:"accepted"`
	Task      adminMailTransportTaskViewResponse `json:"task"`
	Reused    bool                               `json:"reused"`
}

type adminMailTransportTaskViewResponse struct {
	TaskID             string                                  `json:"taskId"`
	BizType            string                                  `json:"bizType"`
	BizID              uint64                                  `json:"bizId"`
	Kind               string                                  `json:"kind"`
	Status             string                                  `json:"status"`
	Attempts           int                                     `json:"attempts"`
	MaxAttempts        int                                     `json:"maxAttempts"`
	RemainingAttempts  int                                     `json:"remainingAttempts"`
	CredentialRevision *uint64                                 `json:"credentialRevision"`
	QueuedAt           time.Time                               `json:"queuedAt"`
	StartedAt          *time.Time                              `json:"startedAt"`
	FinishedAt         *time.Time                              `json:"finishedAt"`
	UpdatedAt          time.Time                               `json:"updatedAt"`
	Progress           *adminMailTransportTaskProgressResponse `json:"progress"`
}

type adminMailTransportTaskProgressResponse struct {
	Total        int64                                       `json:"total"`
	Processed    int64                                       `json:"processed"`
	Succeeded    int64                                       `json:"succeeded"`
	Skipped      int64                                       `json:"skipped"`
	Failed       int64                                       `json:"failed"`
	ReasonCounts []adminMailTransportTaskReasonCountResponse `json:"reasonCounts"`
}

type adminMailTransportTaskReasonCountResponse struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

func adminMailTransportTaskAcceptedDTO(result *mailapp.AdminTaskAcceptedResult) adminMailTransportTaskAcceptedResponse {
	task := adminMailTransportTaskDTO(result.Task)
	return adminMailTransportTaskAcceptedResponse{
		TaskID: task.TaskID, RequestID: result.RequestID, Status: task.Status, Accepted: 1,
		Task: task, Reused: result.Reused,
	}
}

func adminMailTransportTaskDTO(task governanceapp.AdminTaskView) adminMailTransportTaskViewResponse {
	maxAttempts := task.MaxAttempts
	if maxAttempts < 0 {
		maxAttempts = 0
	}
	remainingAttempts := maxAttempts - task.Attempts
	if remainingAttempts < 0 {
		remainingAttempts = 0
	}
	response := adminMailTransportTaskViewResponse{
		TaskID:             task.TaskID(),
		BizType:            task.BizType,
		BizID:              task.BizID,
		Kind:               task.Kind,
		Status:             task.Status,
		Attempts:           task.Attempts,
		MaxAttempts:        maxAttempts,
		RemainingAttempts:  remainingAttempts,
		CredentialRevision: task.CredentialRevision,
		QueuedAt:           task.QueuedAt,
		StartedAt:          task.StartedAt,
		FinishedAt:         task.FinishedAt,
		UpdatedAt:          task.UpdatedAt,
		Progress:           nil,
	}
	if task.Progress != nil {
		reasonCounts := make([]adminMailTransportTaskReasonCountResponse, len(task.Progress.ReasonCounts))
		for i := range task.Progress.ReasonCounts {
			reasonCounts[i] = adminMailTransportTaskReasonCountResponse{
				Reason: task.Progress.ReasonCounts[i].Reason,
				Count:  task.Progress.ReasonCounts[i].Count,
			}
		}
		response.Progress = &adminMailTransportTaskProgressResponse{
			Total:        task.Progress.Total,
			Processed:    task.Progress.Processed,
			Succeeded:    task.Progress.Succeeded,
			Skipped:      task.Progress.Skipped,
			Failed:       task.Progress.Failed,
			ReasonCounts: reasonCounts,
		}
	}
	return response
}
