package app

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

func TestSystemMessagesReuseVerificationFrameAndEscapeContent(t *testing.T) {
	loadAlert := SystemLoadAlertMessage("user@example.com", `<script>alert(1)</script>`, "episode-1", 95, 97.2, 63.4, true, time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC))
	structured := []struct {
		message domain.OutboundMessage
		labels  []string
	}{
		{BalanceWarningMessage("user@example.com", "400.00", "500.00", 2), []string{"当前积分", "预警档位", "建议操作"}},
		{ProjectApplicationMessage("admin@example.com", 42, 7, `<img src=x onerror=alert(1)>`, "github.com\r\nevil", "request-1"), []string{"项目 ID", "项目名称", "目标平台", "申请人 ID", "审批状态"}},
		{RechargeCreditedMessage("user@example.com", "<img src=x onerror=alert(1)>\r\nRC2", "10000.00", "10400.00"), []string{"充值积分", "到账后积分", "充值单号"}},
		{LeaderboardRewardMessage("user@example.com", "2026-07-28", 1, 12, "8000.00"), []string{"结算日期", "排行榜名次", "成功订单数", "奖励积分"}},
		{LoginNotificationMessage("user@example.com", "session-1", "203.0.113.8", "Browser", time.Unix(0, 0)), []string{"登录时间", "登录 IP", "设备"}},
		{loadAlert, []string{"服务器", "告警阈值", "当前 CPU", "当前内存", "检测时间", "建议操作"}},
	}
	for _, item := range structured {
		message := item.message
		require.Contains(t, message.HTMLBody, `class="remail-shine-bar"`)
		require.Contains(t, message.HTMLBody, "Remail，轻松收码")
		require.Contains(t, message.HTMLBody, `<table aria-label="通知详情"`)
		require.NotEmpty(t, message.TextBody)
		require.Len(t, message.IdempotencyKey, 64)
		for _, label := range item.labels {
			require.Contains(t, message.HTMLBody, ">"+label+"</th>")
			require.Contains(t, message.TextBody, label+"：")
		}
	}
	require.Equal(t, domain.PurposeSecurityNotice, structured[4].message.Purpose)
	require.Equal(t, domain.PurposeSystemNotice, structured[5].message.Purpose)
	require.NotContains(t, structured[1].message.HTMLBody, "<img src=x")
	require.Contains(t, structured[1].message.HTMLBody, "&lt;img src=x onerror=alert(1)&gt;")
	require.Contains(t, structured[1].message.HTMLBody, "github.com evil")
	require.NotContains(t, structured[2].message.HTMLBody, "<img src=x")
	require.Contains(t, structured[2].message.HTMLBody, "&lt;img src=x onerror=alert(1)&gt;<br>RC2")
	require.Contains(t, structured[2].message.TextBody, `<img src=x onerror=alert(1)>`)
	require.NotContains(t, structured[5].message.HTMLBody, `<script>`)
	require.Contains(t, structured[5].message.HTMLBody, `&lt;script&gt;alert(1)&lt;/script&gt;`)
	require.Contains(t, structured[5].message.HTMLBody, `≥95%`)
	require.Contains(t, structured[5].message.HTMLBody, `97.2%`)
	require.Contains(t, structured[5].message.HTMLBody, `63.4%`)
	require.Contains(t, structured[5].message.HTMLBody, `2026-07-30 20:00:00 Asia/Shanghai`)
	require.Equal(t, loadAlert.IdempotencyKey, SystemLoadAlertMessage("user@example.com", `<script>alert(1)</script>`, "episode-1", 95, 97.2, 63.4, true, time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)).IdempotencyKey)
	require.NotEqual(t, loadAlert.IdempotencyKey, SystemLoadAlertMessage("user@example.com", `<script>alert(1)</script>`, "episode-2", 95, 97.2, 63.4, true, time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)).IdempotencyKey)

	announcement := AnnouncementMessage("user@example.com", 7, `公告 <img src=x onerror=alert(1)>`, "  安全内容\r\n<script>alert(1)</script>\n")
	require.NotContains(t, announcement.HTMLBody, `<table aria-label="通知详情"`)
	require.NotContains(t, announcement.HTMLBody, "<script>")
	require.NotContains(t, announcement.HTMLBody, "<img src=x")
	require.Contains(t, announcement.HTMLBody, "公告 &lt;img src=x onerror=alert(1)&gt;")
	require.Contains(t, announcement.HTMLBody, ">  安全内容<br>&lt;script&gt;alert(1)&lt;/script&gt;<br></div>")
	require.Contains(t, announcement.HTMLBody, "white-space:pre-wrap")
	require.Contains(t, announcement.HTMLBody, "mso-spacerun:yes")
	require.True(t, strings.HasPrefix(announcement.Subject, "ReMail 系统公告："))

	verification := VerificationCodeMessage("user@example.com", "123456")
	require.NotContains(t, verification.HTMLBody, `<table aria-label="通知详情"`)
	require.Contains(t, verification.HTMLBody, "123456")

}

func TestBrandedHTMLReturnsRenderErrors(t *testing.T) {
	badTemplate := template.Must(template.New("bad").Parse(`{{.Missing}}`))

	htmlBody, err := BrandedHTML("title", "heading", "intro", badTemplate, struct{}{}, "note")
	require.Empty(t, htmlBody)
	require.ErrorContains(t, err, "render branded email content")

	htmlBody, err = BrandedHTML("title", "heading", "intro", nil, nil, "note")
	require.Empty(t, htmlBody)
	require.ErrorContains(t, err, "template is nil")
}

func TestNotificationRenderFailureFallsBackToPlainText(t *testing.T) {
	originalPanel := notificationPanelContentTemplate
	notificationPanelContentTemplate = nil
	t.Cleanup(func() { notificationPanelContentTemplate = originalPanel })

	message := AnnouncementMessage("user@example.com", 7, "公告", "仍然可以读取的正文")
	require.Empty(t, message.HTMLBody)
	require.Contains(t, message.TextBody, "仍然可以读取的正文")

	originalVerification := verificationCodeContentTemplate
	verificationCodeContentTemplate = nil
	t.Cleanup(func() { verificationCodeContentTemplate = originalVerification })

	verification := VerificationCodeMessage("user@example.com", "123456")
	require.Empty(t, verification.HTMLBody)
	require.Contains(t, verification.TextBody, "123456")
}
