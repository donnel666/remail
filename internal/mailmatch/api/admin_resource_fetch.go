package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
)

type adminResourceFetchAcceptedResponse struct {
	TaskID    string                     `json:"taskId"`
	RequestID string                     `json:"requestId"`
	Status    string                     `json:"status"`
	Accepted  int64                      `json:"accepted"`
	Task      adminResourceFetchTaskView `json:"task"`
	Reused    bool                       `json:"reused"`
}

type adminResourceFetchTaskView struct {
	TaskID             string                          `json:"taskId"`
	BizType            string                          `json:"bizType"`
	BizID              uint                            `json:"bizId"`
	Kind               string                          `json:"kind"`
	Status             string                          `json:"status"`
	Attempts           int                             `json:"attempts"`
	MaxAttempts        int                             `json:"maxAttempts"`
	RemainingAttempts  int                             `json:"remainingAttempts"`
	CredentialRevision *uint64                         `json:"credentialRevision"`
	QueuedAt           time.Time                       `json:"queuedAt"`
	StartedAt          *time.Time                      `json:"startedAt"`
	FinishedAt         *time.Time                      `json:"finishedAt"`
	UpdatedAt          time.Time                       `json:"updatedAt"`
	Progress           *adminResourceFetchTaskProgress `json:"progress"`
}

type adminResourceFetchTaskProgress struct {
	Total        int64                           `json:"total"`
	Processed    int64                           `json:"processed"`
	Succeeded    int64                           `json:"succeeded"`
	Skipped      int64                           `json:"skipped"`
	Failed       int64                           `json:"failed"`
	ReasonCounts []adminResourceFetchReasonCount `json:"reasonCounts"`
}

type adminResourceFetchReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

func (h *Handler) PostAdminMicrosoftResourceMessagesFetch(c *gin.Context) {
	h.postAdminMicrosoftResourceFetch(c, domain.ResourceFetchJobFetch)
}

func (h *Handler) PostAdminMicrosoftResourceProjectScan(c *gin.Context) {
	h.postAdminMicrosoftResourceFetch(c, domain.ResourceFetchJobHistory)
}

func (h *Handler) postAdminMicrosoftResourceFetch(c *gin.Context, kind domain.ResourceFetchJobKind) {
	if h == nil || h.mod == nil || h.mod.ResourceFetch == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message":   "Mail service is temporarily unavailable.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	resourceID64, err := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	if err != nil || resourceID64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Idempotency-Key is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	result, err := h.mod.ResourceFetch.Submit(c.Request.Context(), mailmatchapp.ResourceFetchSubmitCommand{
		Kind:           kind,
		ResourceID:     uint(resourceID64),
		OperatorUserID: operatorUserID,
		IdempotencyKey: idempotencyKey,
		RequestID:      middleware.GetRequestID(c),
		Path:           c.FullPath(),
	})
	if err != nil {
		writeAdminResourceFetchError(c, err)
		return
	}
	task := adminResourceFetchTaskResponse(result.Job)
	c.JSON(http.StatusAccepted, adminResourceFetchAcceptedResponse{
		TaskID: task.TaskID, RequestID: result.Job.RequestID, Status: task.Status, Accepted: 1,
		Task: task, Reused: result.Reused,
	})
}

func adminResourceFetchTaskResponse(job domain.ResourceFetchJob) adminResourceFetchTaskView {
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = domain.ResourceFetchDefaultMaxAttempts
	}
	remaining := maxAttempts - job.Attempts
	if remaining < 0 {
		remaining = 0
	}
	credentialRevision := job.ExpectedCredentialRevision
	return adminResourceFetchTaskView{
		TaskID:             "fetch:" + strconv.FormatUint(uint64(job.ID), 10),
		BizType:            "microsoft_resource",
		BizID:              job.ResourceID,
		Kind:               string(job.Kind),
		Status:             string(job.Status),
		Attempts:           job.Attempts,
		MaxAttempts:        maxAttempts,
		RemainingAttempts:  remaining,
		CredentialRevision: &credentialRevision,
		QueuedAt:           job.CreatedAt,
		StartedAt:          job.StartedAt,
		FinishedAt:         job.FinishedAt,
		UpdatedAt:          job.UpdatedAt,
		Progress:           nil,
	}
}

func writeAdminResourceFetchError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, domain.ErrResourceFetchNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrResourceFetchDeleted), errors.Is(err, domain.ErrResourceFetchJobConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource state does not allow mail fetch.", "requestId": requestID})
	case errors.Is(err, domain.ErrResourceFetchIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, domain.ErrResourceFetchCredentialsMissing):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Microsoft mail fetch credentials are incomplete.", "requestId": requestID})
	case errors.Is(err, domain.ErrFetchQueueUnavailable), errors.Is(err, domain.ErrMailServiceUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
