package api

import (
	"net/http"
	stdmail "net/mail"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

type BotUserIDResolver func(*gin.Context) (uint, bool)

type BotCodeDiagnosisRequest struct {
	Email     string `json:"email"`
	ProjectID uint   `json:"projectId"`
}

type BotCodeDiagnosisResponse struct {
	Result    string `json:"result"`
	Reason    string `json:"reason"`
	Action    string `json:"action"`
	RequestID string `json:"requestId"`
}

type botDiagnosisHandler struct {
	service *mailmatchapp.BotDiagnosisService
	userID  BotUserIDResolver
}

func (h botDiagnosisHandler) PostCodeDiagnosis(c *gin.Context) {
	requestID := middleware.GetRequestID(c)
	if h.userID == nil || h.service == nil {
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "manual_support_required",
			"诊断服务暂时不可用。", "请稍后重试；持续失败时请联系人工客服。")
		return
	}
	userID, ok := h.userID(c)
	if c.IsAborted() || c.Writer.Written() {
		return
	}
	if !ok {
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "manual_support_required",
			"诊断服务暂时不可用。", "请稍后重试；持续失败时请联系人工客服。")
		return
	}
	if userID == 0 {
		writeBotDiagnosis(c, http.StatusOK, "binding_required",
			"当前机器人账号尚未绑定 remail 账号。", "请先在私聊中绑定 remail 账号。")
		return
	}
	var req BotCodeDiagnosisRequest
	if err := bindPickupBatchJSON(c, &req); err != nil || !validBotCodeDiagnosisRequest(req) {
		writeBotDiagnosis(c, http.StatusBadRequest, "invalid_request",
			"请求参数不正确。", "请提供有效的邮箱和项目编号。")
		return
	}
	result, err := h.service.DiagnoseCode(c.Request.Context(), userID, req.Email, req.ProjectID)
	if err != nil {
		platform.Logger(c.Request.Context()).Error("bot code diagnosis failed", "error", err)
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "manual_support_required",
			"诊断服务暂时不可用。", "请稍后重试；持续失败时请联系人工客服。")
		return
	}
	c.JSON(http.StatusOK, BotCodeDiagnosisResponse{
		Result: result.Result, Reason: result.Reason, Action: result.Action, RequestID: requestID,
	})
}

func validBotCodeDiagnosisRequest(req BotCodeDiagnosisRequest) bool {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if req.ProjectID == 0 || email == "" || len(email) > 254 {
		return false
	}
	address, err := stdmail.ParseAddress(email)
	return err == nil && strings.EqualFold(address.Address, email)
}

func writeBotDiagnosis(c *gin.Context, status int, result, reason, action string) {
	c.JSON(status, BotCodeDiagnosisResponse{
		Result: result, Reason: reason, Action: action, RequestID: middleware.GetRequestID(c),
	})
}
