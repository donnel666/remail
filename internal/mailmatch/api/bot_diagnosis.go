package api

import (
	"net/http"
	stdmail "net/mail"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/botdisplay"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

type BotUserResolution struct {
	UserID    uint
	Bound     bool
	Available bool
}

type BotUserIDResolver func(*gin.Context) (BotUserResolution, bool)

type BotCodeDiagnosisRequest struct {
	Email string `json:"email"`
}

type BotCodeDiagnosisResponse struct {
	Message            string `json:"message"`
	BindingRequired    bool   `json:"bindingRequired,omitempty"`
	AccountUnavailable bool   `json:"accountUnavailable,omitempty"`
	ProjectID          uint   `json:"projectId,omitempty"`
	ProjectName        string `json:"projectName,omitempty"`
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
	user, ok := h.userID(c)
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
	if !user.Bound {
		c.JSON(http.StatusOK, BotCodeDiagnosisResponse{
			Message: botdisplay.BindingRequiredMessage, BindingRequired: true,
		})
		return
	}
	if !user.Available {
		c.JSON(http.StatusOK, BotCodeDiagnosisResponse{
			Message: botdisplay.BindingUnavailableMessage, AccountUnavailable: true,
		})
		return
	}
	var req BotCodeDiagnosisRequest
	if err := bindPickupBatchJSON(c, &req); err != nil || !validBotCodeDiagnosisRequest(req, identity.Scene) {
		writeBotDiagnosis(c, http.StatusBadRequest, "请提供有效的订单邮箱。", 0, "")
		return
	}
	result, err := h.service.DiagnoseCode(c.Request.Context(), user.UserID, req.Email)
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
