package icloud

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

func (h *handler) testLivenessSMTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	s := h.service
	if s == nil || s.deviceRedis == nil || s.operationLogs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "SMTP testing is temporarily unavailable."})
		return
	}
	servers, err := runtimeconfig.ParseICloudLivenessSMTPServers(runtimeconfig.String(runtimeconfig.ICloudLivenessSMTPServersKey, "[]"))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Save a valid SMTP configuration before testing."})
		return
	}
	index := slices.IndexFunc(servers, func(server runtimeconfig.ICloudLivenessSMTPServer) bool { return server.ID == c.Param("serverId") })
	if index < 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "SMTP service not found. Save the configuration before testing."})
		return
	}
	server := servers[index]
	if strings.TrimSpace(server.Password) == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Save the SMTP password or authorization code before testing."})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	interval := runtimeconfig.Duration(runtimeconfig.ICloudLivenessSMTPIntervalKey, iCloudLivenessSMTPSendInterval, time.Second, 1)
	lease, wait, err := s.reserveICloudLivenessSMTPSend(ctx, interval)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "SMTP testing is temporarily unavailable."})
		return
	}
	if wait > 0 {
		c.Header("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Wait for the SMTP sending interval before testing again."})
		return
	}
	defer s.finishICloudLivenessSMTPSend(ctx, lease, interval)
	userID, _ := middleware.GetCurrentUserID(c)
	if err := s.operationLogs.Create(ctx, &governancedomain.OperationLog{
		OperatorUserID: userID, OperationType: "icloud.smtp.test", ResourceType: "smtp_service", ResourceID: server.ID,
		Path: c.FullPath(), Result: "success", RequestID: middleware.GetRequestID(c),
		SafeSummary: "Requested one SMTP test email to the configured sender mailbox; delivery result is reported by the response.",
	}); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "SMTP testing is temporarily unavailable."})
		return
	}
	cfg := mailinfra.SMTPConfig{Addr: server.Addr, Username: server.Username, Password: server.Password, From: server.From}
	var sender mailapp.SenderPort = mailinfra.NewSMTPDelivery(cfg)
	if s.newLivenessSMTP != nil {
		sender = s.newLivenessSMTP(cfg)
	}
	err = sender.Send(ctx, maildomain.OutboundMessage{
		Purpose: maildomain.PurposeSystemNotice, From: server.From, To: server.From,
		Subject:  "ReMail SMTP configuration test",
		TextBody: "这是一封 ReMail SMTP 配置测试邮件。收到此邮件说明该 SMTP 可以完成发信，无需回复。此测试不代表 iCloud 别名测活成功。",
	})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": smtpTestFailureMessage(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted": true, "recipient": server.From})
}

func smtpTestFailureMessage(err error) string {
	var unaccepted *mailapp.OutboundNotAcceptedError
	if !errors.As(err, &unaccepted) {
		return "SMTP delivery result is unconfirmed. Check the receiving mailbox before retrying."
	}
	// Classify locally; upstream replies can contain credentials and must not be returned.
	diagnostic := strings.ToLower(err.Error())
	switch {
	case strings.Contains(diagnostic, "mail from failed"):
		return "SMTP rejected the sender address. Check that the sender matches the authenticated mailbox."
	case strings.Contains(diagnostic, "rcpt to failed"):
		return "SMTP rejected the test recipient. Check that the configured sender mailbox can receive mail."
	case strings.Contains(diagnostic, "data failed"), strings.Contains(diagnostic, "data close failed"):
		return "SMTP rejected the test message. Check the provider's sending restrictions or rate limits."
	case strings.Contains(diagnostic, "tls"), strings.Contains(diagnostic, "certificate"):
		return "SMTP TLS negotiation failed. Check the server address, port and certificate."
	case strings.Contains(diagnostic, "smtp auth failed"), strings.Contains(diagnostic, "dial failed: 535"), strings.Contains(diagnostic, "does not support smtp auth"):
		return "SMTP authentication failed. Check the username and password or authorization code."
	case strings.Contains(diagnostic, "dial failed"):
		return "SMTP connection failed. Check the server address, port and network access."
	default:
		return "SMTP did not accept the test email. Check the SMTP settings before retrying."
	}
}
