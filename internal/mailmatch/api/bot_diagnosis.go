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
	Email string `json:"email"`
}

type BotCodeDiagnosisResponse struct {
	Message     string `json:"message"`
	ProjectID   uint   `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
}

type botDiagnosisHandler struct {
	service *mailmatchapp.BotDiagnosisService
	userID  BotUserIDResolver
}

func (h botDiagnosisHandler) PostCodeDiagnosis(c *gin.Context) {
	if h.userID == nil || h.service == nil {
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "诊断服务暂时不可用，请稍后重试。", 0, "")
		return
	}
	userID, ok := h.userID(c)
	if c.IsAborted() || c.Writer.Written() {
		return
	}
	if !ok {
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "诊断服务暂时不可用，请稍后重试。", 0, "")
		return
	}
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok {
		writeBotDiagnosis(c, http.StatusUnauthorized, "身份验证失败。", 0, "")
		return
	}
	if userID == 0 {
		writeBotDiagnosis(c, http.StatusOK, "当前账号尚未绑定 ReMail，请先完成绑定。", 0, "")
		return
	}
	var req BotCodeDiagnosisRequest
	if err := bindPickupBatchJSON(c, &req); err != nil || !validBotCodeDiagnosisRequest(req, identity.Scene) {
		writeBotDiagnosis(c, http.StatusBadRequest, "请提供有效的订单邮箱。", 0, "")
		return
	}
	result, err := h.service.DiagnoseCode(c.Request.Context(), userID, req.Email)
	if err != nil {
		platform.Logger(c.Request.Context()).Error("bot code diagnosis failed", "error", err)
		writeBotDiagnosis(c, http.StatusServiceUnavailable, "诊断服务暂时不可用，请稍后重试。", 0, "")
		return
	}
	message := strings.TrimSpace(result.Reason + " " + result.Action)
	writeBotDiagnosis(c, http.StatusOK, message, result.ProjectID, result.ProjectName)
}

func validBotCodeDiagnosisRequest(req BotCodeDiagnosisRequest, scene string) bool {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if (scene != middleware.BotScenePrivate && scene != middleware.BotSceneGroup) || email == "" || len(email) > 254 {
		return false
	}
	address, err := stdmail.ParseAddress(email)
	return err == nil && strings.EqualFold(address.Address, email)
}

func writeBotDiagnosis(c *gin.Context, status int, message string, projectID uint, projectName string) {
	c.JSON(status, BotCodeDiagnosisResponse{Message: message, ProjectID: projectID, ProjectName: projectName})
}
