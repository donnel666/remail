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

type adminLogFacetsResponse struct {
	System    int64 `json:"system"`
	Operation int64 `json:"operation"`
}

type adminSystemLogListResponse struct {
	Items  []adminSystemLogResponse `json:"items"`
	Total  int64                    `json:"total"`
	Facets adminLogFacetsResponse   `json:"facets"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}

type adminSystemLogResponse struct {
	ID        uint64    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Category  string    `json:"category"`
	RequestID string    `json:"requestId"`
	Level     string    `json:"level"`
	Module    string    `json:"module"`
	EventType string    `json:"eventType"`
	BizType   string    `json:"bizType"`
	BizID     string    `json:"bizId"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail"`
}

type adminOperationLogListResponse struct {
	Items  []adminOperationLogResponse `json:"items"`
	Total  int64                       `json:"total"`
	Facets adminLogFacetsResponse      `json:"facets"`
	Offset int                         `json:"offset"`
	Limit  int                         `json:"limit"`
}

type adminOperationLogResponse struct {
	ID             uint64    `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	Category       string    `json:"category"`
	RequestID      string    `json:"requestId"`
	OperatorUserID uint      `json:"operatorUserId"`
	Operator       string    `json:"operator"`
	OperationType  string    `json:"operationType"`
	ResourceType   string    `json:"resourceType"`
	ResourceID     string    `json:"resourceId"`
	Path           string    `json:"path"`
	Result         string    `json:"result"`
	SafeSummary    string    `json:"safeSummary"`
}

type adminLogCleanupResponse struct {
	Removed int64 `json:"removed"`
}

func adminSystemLogListDTO(result *governanceapp.AdminSystemLogListResult) adminSystemLogListResponse {
	items := make([]adminSystemLogResponse, len(result.Items))
	for i := range result.Items {
		items[i] = adminSystemLogResponse{
			ID: result.Items[i].ID, CreatedAt: result.Items[i].CreatedAt,
			Category: governanceapp.AdminLogCategorySystem, RequestID: result.Items[i].RequestID,
			Level: result.Items[i].Level, Module: result.Items[i].Module, EventType: result.Items[i].EventType,
			BizType: result.Items[i].BizType, BizID: result.Items[i].BizID,
			Message: result.Items[i].Message, Detail: result.Items[i].Detail,
		}
	}
	return adminSystemLogListResponse{
		Items: items, Total: result.Total, Facets: adminLogFacetsDTO(result.Facets), Offset: result.Offset, Limit: result.Limit,
	}
}

func adminOperationLogListDTO(result *governanceapp.AdminOperationLogListResult) adminOperationLogListResponse {
	items := make([]adminOperationLogResponse, len(result.Items))
	for i := range result.Items {
		items[i] = adminOperationLogResponse{
			ID: result.Items[i].ID, CreatedAt: result.Items[i].CreatedAt,
			Category: governanceapp.AdminLogCategoryOperation, RequestID: result.Items[i].RequestID,
			OperatorUserID: result.Items[i].OperatorUserID, Operator: result.Items[i].Operator,
			OperationType: result.Items[i].OperationType, ResourceType: result.Items[i].ResourceType,
			ResourceID: result.Items[i].ResourceID, Path: result.Items[i].Path,
			Result: result.Items[i].Result, SafeSummary: result.Items[i].SafeSummary,
		}
	}
	return adminOperationLogListResponse{
		Items: items, Total: result.Total, Facets: adminLogFacetsDTO(result.Facets), Offset: result.Offset, Limit: result.Limit,
	}
}

func adminLogFacetsDTO(facets governanceapp.AdminLogFacets) adminLogFacetsResponse {
	return adminLogFacetsResponse{System: facets.System, Operation: facets.Operation}
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
