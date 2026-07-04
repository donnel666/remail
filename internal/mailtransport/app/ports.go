package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"

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

func verificationCodePlainText(code string) string {
	return fmt.Sprintf("您的 ReMail 邮箱验证码是：%s\r\n验证码 10 分钟内有效。若非本人操作，请忽略本邮件。\r\n\r\nRemail，轻松收码\r\n让闲置邮箱，重新热起来\r\n", code)
}

func verificationCodeHTML(code string) string {
	code = html.EscapeString(code)
	logo := html.EscapeString(logoDataURI())
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ReMail 邮箱验证码</title>
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
              <h1 style="font-size:22px;line-height:30px;font-weight:700;color:#111827;margin:0 0 10px;">邮箱验证码</h1>
              <p style="font-size:15px;line-height:24px;color:#4b5563;margin:0 0 24px;">请输入下方验证码完成本次操作。</p>
              <div style="font-size:32px;line-height:40px;font-weight:700;color:#111827;background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px;padding:18px 20px;text-align:center;margin:0 0 20px;">%s</div>
              <p style="font-size:14px;line-height:22px;color:#6b7280;margin:0;">验证码 10 分钟内有效。若非本人操作，请忽略本邮件。</p>
            </td>
          </tr>
          <tr>
            <td style="border-top:1px solid #e5e7eb;padding:18px 32px 22px;background:#fbfcfd;">
              <p style="font-size:14px;line-height:22px;font-weight:700;margin:0 0 4px;">
                <span class="remail-title" style="position:relative;display:inline-block;color:#111827;white-space:nowrap;">
                  <span style="position:relative;z-index:1;">
                    <span class="remail-brand-gradient" style="color:#c6533c;background:linear-gradient(90deg,#8a4a34 0%%,#c6533c 50%%,#f4513b 100%%);-webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent;">Remail</span><span style="color:#111827;">，轻松收码</span>
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
</html>`, logo, code)
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
