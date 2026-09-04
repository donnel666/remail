package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
)

var diagnosticEmailPattern = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-])[a-z0-9._%+\-]*@([a-z0-9.\-]+\.[a-z]{2,})\b`)

type DeliveryPort interface {
	Send(ctx context.Context, message domain.OutboundMessage) error
}

type notificationDetail struct {
	Label string
	Value string
}

var (
	verificationCodeContentTemplate  = template.Must(template.New("verification-code").Parse(`<div style="font-size:32px;line-height:40px;font-weight:700;color:#111827;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;text-align:center;margin:0 0 20px;">{{.}}</div>`))
	notificationPanelContentTemplate = template.Must(template.New("notification-panel").Funcs(template.FuncMap{"lines": mailTextLines}).Parse(`<div style="font-size:15px;line-height:24px;color:#374151;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;margin:0 0 20px;white-space:pre-wrap;mso-spacerun:yes;word-break:break-word;">{{range $index, $line := lines .}}{{if $index}}<br>{{end}}{{$line}}{{end}}</div>`))
	notificationTableContentTemplate = template.Must(template.New("notification-table").Funcs(template.FuncMap{"lines": mailTextLines}).Parse(`<table aria-label="通知详情" width="100%" cellpadding="0" cellspacing="0" style="width:100%;border-collapse:separate;border-spacing:0;border:1px solid #e5e7eb;border-bottom:0;border-radius:8px;overflow:hidden;margin:0 0 20px;">{{range .}}<tr><th scope="row" align="left" valign="top" width="34%" style="border-bottom:1px solid #e5e7eb;background:#f9fafb;color:#4b5563;font-size:14px;line-height:22px;font-weight:600;padding:13px 16px;word-break:break-word;">{{.Label}}</th><td align="left" valign="top" style="border-bottom:1px solid #e5e7eb;color:#111827;font-size:15px;line-height:22px;padding:13px 16px;white-space:pre-wrap;mso-spacerun:yes;word-break:break-word;">{{range $index, $line := lines .Value}}{{if $index}}<br>{{end}}{{$line}}{{end}}</td></tr>{{end}}</table>`))
)

func VerificationCodeMessage(recipient, code string) domain.OutboundMessage {
	code = bodyValue(code)
	recipient = strings.TrimSpace(recipient)
	return domain.OutboundMessage{
		IdempotencyKey: messageDigest(domain.PurposeVerificationCode, recipient, code),
		Purpose:        domain.PurposeVerificationCode,
		To:             recipient,
		Subject:        "ReMail 邮箱验证码",
		TextBody:       verificationCodePlainText(code),
		HTMLBody:       verificationCodeHTML(code),
	}
}

func BalanceWarningMessage(recipient, balance, threshold string, cycle uint64) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	details := []notificationDetail{
		{Label: "当前积分", Value: balance + " 积分"},
		{Label: "预警档位", Value: "≤" + threshold + " 积分"},
		{Label: "建议操作", Value: "请及时充值，以免影响服务使用。"},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("balance_warning", recipient, cycle, threshold),
		recipient,
		"ReMail 余额不足预警",
		"余额不足预警",
		"您的账户余额已到达预警档位。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"充值到账后，各档位的预警次数将自动重置。",
	)
}

func ProjectApplicationMessage(recipient string, projectID, applicantUserID uint, projectName, targetPlatform, eventID string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	details := []notificationDetail{
		{Label: "项目 ID", Value: fmt.Sprint(projectID)},
		{Label: "项目名称", Value: oneLine(projectName, 120)},
		{Label: "目标平台", Value: oneLine(targetPlatform, 120)},
		{Label: "申请人 ID", Value: fmt.Sprint(applicantUserID)},
		{Label: "审批状态", Value: "待审批"},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("project_application", recipient, projectID, applicantUserID, eventID),
		recipient,
		"ReMail 新项目申请待审批",
		"新项目申请待审批",
		"有用户提交了项目申请，请及时审批。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"请登录管理后台，在项目管理中完成审批。",
	)
}

func SystemLoadAlertMessage(recipient, hostname, episodeID string, threshold int, cpuPercent, memoryPercent float64, memoryValid bool, observedAt time.Time) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	hostname = oneLine(hostname, 240)
	episodeID = oneLine(episodeID, 128)
	if hostname == "" {
		hostname = "未知服务器"
	}
	if episodeID == "" {
		episodeID = observedAt.UTC().Format(time.RFC3339Nano)
	}
	memoryUsage := "采集失败"
	if memoryValid {
		memoryUsage = fmt.Sprintf("%.1f%%", memoryPercent)
	}
	observedTime := observedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05 MST")
	details := []notificationDetail{
		{Label: "服务器", Value: hostname},
		{Label: "告警阈值", Value: fmt.Sprintf("≥%d%%", threshold)},
		{Label: "当前 CPU", Value: fmt.Sprintf("%.1f%%", cpuPercent)},
		{Label: "当前内存", Value: memoryUsage},
		{Label: "检测时间", Value: observedTime},
		{Label: "建议操作", Value: "请尽快检查高占用进程、数据库慢查询和后台任务队列。"},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("system_load_alert", hostname, episodeID, threshold, recipient),
		recipient,
		fmt.Sprintf("ReMail 系统负载告警（%d%%）", threshold),
		"系统负载告警",
		"服务器 CPU 使用率已连续 10 分钟处于过载状态，当前达到或超过告警阈值。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"CPU 连续达到 80% 满 10 分钟后才通知；同一轮连续过载仅通知一次；任一次低于 80% 或 CPU 采样无效后重新计时。",
	)
}

func RechargeCreditedMessage(recipient, rechargeNo, amount, balance string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	details := []notificationDetail{
		{Label: "充值积分", Value: amount + " 积分"},
		{Label: "到账后积分", Value: balance + " 积分"},
		{Label: "充值单号", Value: rechargeNo},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("recharge_credited", recipient, rechargeNo),
		recipient,
		"ReMail 充值到账通知",
		"充值到账",
		"您的充值已成功到账。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"如对本次充值有疑问，请联系平台管理员。",
	)
}

func LeaderboardRewardMessage(recipient, businessDate string, rank, score int, amount string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	details := []notificationDetail{
		{Label: "结算日期", Value: businessDate},
		{Label: "排行榜名次", Value: fmt.Sprintf("第 %d 名", rank)},
		{Label: "成功订单数", Value: fmt.Sprint(score)},
		{Label: "奖励积分", Value: amount + " 积分"},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("leaderboard_reward", businessDate, rank, recipient),
		recipient,
		"ReMail 排行榜奖励到账通知",
		"排行榜奖励到账",
		"恭喜您获得今日成功订单排行榜奖励。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"奖励已发放至您的消费钱包，此邮件由系统自动发送。",
	)
}

func LotteryWinnerMessage(recipient string, lotteryID uint, title, amount string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	title = oneLine(title, 120)
	if title == "" {
		title = "ReMail 抽奖"
	}
	details := []notificationDetail{
		{Label: "抽奖活动", Value: title},
		{Label: "奖励积分", Value: formatLotteryReward(amount) + " 积分"},
	}
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("lottery_winner", lotteryID, recipient),
		recipient,
		"ReMail 抽奖奖励到账通知",
		"抽奖奖励到账",
		"恭喜您获得本次抽奖奖励。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"奖励已发放至您的消费钱包，此邮件由系统自动发送。",
	)
}

func formatLotteryReward(value string) string {
	trimmed := strings.TrimSpace(value)
	amount, err := money.Parse(trimmed)
	if err != nil {
		return trimDecimalZeros(trimmed)
	}
	sign := ""
	if amount.IsNegative() {
		sign = "-"
		amount = amount.Abs()
	}
	units := []struct {
		threshold decimal.Decimal
		suffix    string
	}{
		{threshold: decimal.NewFromInt(1_000_000_000), suffix: "B"},
		{threshold: decimal.NewFromInt(1_000_000), suffix: "M"},
		{threshold: decimal.NewFromInt(1_000), suffix: "K"},
	}
	for index, unit := range units {
		if amount.LessThan(unit.threshold) {
			continue
		}
		compact := amount.Div(unit.threshold).Round(1)
		if compact.GreaterThanOrEqual(decimal.NewFromInt(1000)) && index > 0 {
			unit = units[index-1]
			compact = amount.Div(unit.threshold).Round(1)
		}
		return sign + trimDecimalZeros(compact.StringFixed(1)) + unit.suffix
	}
	// Keep defensive formatting for legacy/direct callers; never round a non-integer award in a notice.
	return sign + trimDecimalZeros(amount.StringFixed(money.Scale))
}

func trimDecimalZeros(value string) string {
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	if value == "" || value == "-0" {
		return "0"
	}
	return value
}

func LoginNotificationMessage(recipient, sessionID, clientIP, userAgent string, at time.Time) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	clientIP = oneLine(clientIP, 64)
	userAgent = oneLine(userAgent, 240)
	if clientIP == "" {
		clientIP = "未知"
	}
	if userAgent == "" {
		userAgent = "未知设备"
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	loginTime := at.In(location).Format("2006-01-02 15:04:05 MST")
	details := []notificationDetail{
		{Label: "登录时间", Value: loginTime},
		{Label: "登录 IP", Value: clientIP},
		{Label: "设备", Value: userAgent},
	}
	return notificationMessage(
		domain.PurposeSecurityNotice,
		messageDigest("login", recipient, sessionID),
		recipient,
		"ReMail 登录通知",
		"账户登录通知",
		"您的 ReMail 账户刚刚完成登录。",
		notificationDetailsText(details),
		notificationTableContentTemplate,
		details,
		"若非本人操作，请立即修改密码并联系平台管理员。",
	)
}

func AnnouncementMessage(recipient string, announcementID int64, title, content string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	title = bodyValue(title)
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("announcement", announcementID, recipient),
		recipient,
		"ReMail 系统公告："+title,
		title,
		"平台发布了新的系统公告。",
		content,
		notificationPanelContentTemplate,
		content,
		"此邮件由系统自动发送，请勿直接回复。",
	)
}

func verificationCodePlainText(code string) string {
	return fmt.Sprintf("您的 ReMail 邮箱验证码是：%s\r\n验证码 10 分钟内有效。若非本人操作，请忽略本邮件。\r\n\r\nRemail，轻松收码\r\n让闲置邮箱，重新热起来\r\n", code)
}

func verificationCodeHTML(code string) string {
	htmlBody, err := BrandedHTML(
		"ReMail 邮箱验证码",
		"邮箱验证码",
		"请输入下方验证码完成本次操作。",
		verificationCodeContentTemplate,
		code,
		"验证码 10 分钟内有效。若非本人操作，请忽略本邮件。",
	)
	if err != nil {
		slog.Error("render verification code email HTML failed", "error", err)
	}
	return htmlBody
}

func notificationMessage(purpose domain.OutboundPurpose, idempotencyKey, recipient, subject, heading, intro, textContent string, contentTemplate *template.Template, contentData any, note string) domain.OutboundMessage {
	htmlBody, err := BrandedHTML(subject, heading, intro, contentTemplate, contentData, note)
	if err != nil {
		slog.Error("render notification email HTML failed", "purpose", purpose, "error", err)
	}
	return domain.OutboundMessage{
		IdempotencyKey: idempotencyKey,
		Purpose:        purpose,
		To:             recipient,
		Subject:        bodyValue(subject),
		TextBody:       notificationPlainText(heading, intro, textContent, note),
		HTMLBody:       htmlBody,
	}
}

func notificationPlainText(heading, intro, content, note string) string {
	return fmt.Sprintf("%s\r\n\r\n%s\r\n\r\n%s\r\n\r\n%s\r\n\r\nRemail，轻松收码\r\n让闲置邮箱，重新热起来\r\n", heading, intro, content, note)
}

func notificationDetailsText(details []notificationDetail) string {
	lines := make([]string, len(details))
	for i, detail := range details {
		lines[i] = detail.Label + "：" + detail.Value
	}
	return strings.Join(lines, "\n")
}

func mailTextLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n")
}

// BrandedHTML renders ordinary caller data through its content template, then adds the shared email frame.
func BrandedHTML(documentTitle, heading, intro string, contentTemplate *template.Template, contentData any, note string) (string, error) {
	if contentTemplate == nil {
		return "", fmt.Errorf("render branded email content: template is nil")
	}
	var contentHTML strings.Builder
	if err := contentTemplate.Execute(&contentHTML, contentData); err != nil {
		return "", fmt.Errorf("render branded email content: %w", err)
	}
	return brandedHTML(documentTitle, heading, intro, contentHTML.String(), note), nil
}

func brandedHTML(documentTitle, heading, intro, contentHTML, note string) string {
	documentTitle = html.EscapeString(documentTitle)
	heading = html.EscapeString(heading)
	intro = html.EscapeString(intro)
	note = html.EscapeString(note)
	logo := html.EscapeString(logoDataURI())
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    @keyframes sweep-shine {
      0%% { background-position: 200%% 0; }
      100%% { background-position: -200%% 0; }
    }
    .remail-title {
      position: relative;
      display: inline-block;
      color: #111827;
      white-space: nowrap;
    }
    .remail-brand-gradient {
      color: #c6533c;
      background: linear-gradient(90deg, #8a4a34 0%%, #c6533c 50%%, #f4513b 100%%);
      -webkit-background-clip: text;
      background-clip: text;
      -webkit-text-fill-color: transparent;
    }
    .remail-hot-gradient {
      color: #ff5a3d;
      background: linear-gradient(90deg, #ff7a1a 0%%, #ff5a3d 50%%, #ff3d73 100%%);
      -webkit-background-clip: text;
      background-clip: text;
      -webkit-text-fill-color: transparent;
      font-weight: 700;
    }
    .shine-text {
      display: block !important;
      position: absolute !important;
      top: 0;
      right: 0;
      bottom: 0;
      left: 0;
      z-index: 2;
      color: transparent;
      background: linear-gradient(90deg, transparent 0%%, transparent 40%%, rgba(255, 255, 255, 0.9) 50%%, transparent 60%%, transparent 100%%);
      background-size: 200%% 100%%;
      -webkit-background-clip: text;
      background-clip: text;
      -webkit-text-fill-color: transparent;
      animation: sweep-shine 4s linear infinite;
      pointer-events: none;
      white-space: nowrap;
    }
    .remail-shine-bar {
      background: linear-gradient(90deg, #ff7a1a 0%%, #ff5a3d 50%%, #ff3d73 100%%);
    }
  </style>
</head>
<body style="margin:0;background:#f6f7f9;color:#111827;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#f6f7f9;margin:0;padding:32px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="width:100%%;max-width:520px;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;">
          <tr>
            <td class="remail-shine-bar" style="height:4px;background:linear-gradient(90deg,#ff7a1a 0%%,#ff5a3d 50%%,#ff3d73 100%%);"></td>
          </tr>
          <tr>
            <td style="padding:32px 32px 28px;">
              <img src="%s" alt="Remail" width="48" height="50" style="display:block;width:48px;height:50px;border:0;margin:0 0 18px;">
              <h1 style="font-size:22px;line-height:30px;font-weight:700;color:#111827;margin:0 0 10px;">%s</h1>
              <p style="font-size:15px;line-height:24px;color:#4b5563;margin:0 0 24px;">%s</p>
              %s
              <p style="font-size:14px;line-height:22px;color:#6b7280;margin:0;">%s</p>
            </td>
          </tr>
          <tr>
            <td style="border-top:1px solid #e5e7eb;padding:18px 32px 22px;background:#fbfcfd;">
              <p style="font-size:14px;line-height:22px;font-weight:700;margin:0 0 4px;">
                <span class="remail-title" style="position:relative;display:inline-block;color:#111827;white-space:nowrap;">
                  <span style="position:relative;z-index:1;">
                    <span class="remail-brand-gradient" style="color:#c6533c;background:linear-gradient(90deg,#8a4a34 0%%,#c6533c 50%%,#f4513b 100%%);-webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent;">Remail</span><span class="remail-suffix-fallback" style="color:#111827;-webkit-text-fill-color:#111827;background:none;">，轻松收码</span>
                  </span>
                  <span class="shine-text" aria-hidden="true" style="display:none;color:transparent;">Remail，轻松收码</span>
                </span>
              </p>
              <p style="font-size:13px;line-height:20px;color:#6b7280;margin:0;">让闲置邮箱，重新<span class="remail-hot-gradient" style="color:#ff5a3d;background:linear-gradient(90deg,#ff7a1a 0%%,#ff5a3d 50%%,#ff3d73 100%%);-webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent;font-weight:700;">热</span>起来</p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`, documentTitle, logo, heading, intro, contentHTML, note)
}

func oneLine(value string, maxLen int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return value
}

func bodyValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func messageDigest(parts ...any) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprint(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func safeDiagnostic(value string) string {
	value = bodyValue(value)
	value = diagnosticEmailPattern.ReplaceAllString(value, "$1***@$2")
	const maxLen = 240
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}
