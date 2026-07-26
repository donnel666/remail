package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

var diagnosticEmailPattern = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-])[a-z0-9._%+\-]*@([a-z0-9.\-]+\.[a-z]{2,})\b`)

type DeliveryPort interface {
	Send(ctx context.Context, message domain.OutboundMessage) error
}

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
	body := fmt.Sprintf("当前余额：￥%s\n预警档位：≤￥%s\n请及时充值，以免影响服务使用。", balance, threshold)
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("balance_warning", recipient, cycle, threshold),
		recipient,
		"ReMail 余额不足预警",
		"余额不足预警",
		"您的账户余额已到达预警档位。",
		body,
		"充值到账后，各档位的预警次数将自动重置。",
	)
}

func RechargeCreditedMessage(recipient, rechargeNo, amount, balance string) domain.OutboundMessage {
	recipient = strings.TrimSpace(recipient)
	body := fmt.Sprintf("充值金额：￥%s\n到账后余额：￥%s\n充值单号：%s", amount, balance, rechargeNo)
	return notificationMessage(
		domain.PurposeSystemNotice,
		messageDigest("recharge_credited", recipient, rechargeNo),
		recipient,
		"ReMail 充值到账通知",
		"充值到账",
		"您的充值已成功到账。",
		body,
		"如对本次充值有疑问，请联系平台管理员。",
	)
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
	body := fmt.Sprintf("登录时间：%s\n登录 IP：%s\n设备：%s", at.In(location).Format("2006-01-02 15:04:05 MST"), clientIP, userAgent)
	return notificationMessage(
		domain.PurposeSecurityNotice,
		messageDigest("login", recipient, sessionID),
		recipient,
		"ReMail 登录通知",
		"账户登录通知",
		"您的 ReMail 账户刚刚完成登录。",
		body,
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
		strings.TrimSpace(content),
		"此邮件由系统自动发送，请勿直接回复。",
	)
}

func verificationCodePlainText(code string) string {
	return fmt.Sprintf("您的 ReMail 邮箱验证码是：%s\r\n验证码 10 分钟内有效。若非本人操作，请忽略本邮件。\r\n\r\nRemail，轻松收码\r\n让闲置邮箱，重新热起来\r\n", code)
}

func verificationCodeHTML(code string) string {
	code = html.EscapeString(code)
	return brandedHTML(
		"ReMail 邮箱验证码",
		"邮箱验证码",
		"请输入下方验证码完成本次操作。",
		fmt.Sprintf(`<div style="font-size:32px;line-height:40px;font-weight:700;color:#111827;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;text-align:center;margin:0 0 20px;">%s</div>`, code),
		"验证码 10 分钟内有效。若非本人操作，请忽略本邮件。",
	)
}

func notificationMessage(purpose domain.OutboundPurpose, idempotencyKey, recipient, subject, heading, intro, content, note string) domain.OutboundMessage {
	return domain.OutboundMessage{
		IdempotencyKey: idempotencyKey,
		Purpose:        purpose,
		To:             recipient,
		Subject:        bodyValue(subject),
		TextBody:       notificationPlainText(heading, intro, content, note),
		HTMLBody:       brandedHTML(subject, heading, intro, notificationPanelHTML(content), note),
	}
}

func notificationPlainText(heading, intro, content, note string) string {
	return fmt.Sprintf("%s\r\n\r\n%s\r\n\r\n%s\r\n\r\n%s\r\n\r\nRemail，轻松收码\r\n让闲置邮箱，重新热起来\r\n", heading, intro, content, note)
}

func notificationPanelHTML(content string) string {
	content = html.EscapeString(strings.TrimSpace(content))
	content = strings.ReplaceAll(content, "\n", "<br>")
	return `<div style="font-size:15px;line-height:24px;color:#374151;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;margin:0 0 20px;">` + content + `</div>`
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
