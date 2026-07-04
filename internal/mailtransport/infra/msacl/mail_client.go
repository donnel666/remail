package msacl

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

type EmailObj struct {
	ID               any
	ReceivedAt       string
	Subject          string
	Preview          string
	VerificationCode string
	To               string
	From             string
	Raw              map[string]any
}

type MailboxReader interface {
	List(ctx context.Context, mailbox string, limit int, fuzzy bool) ([]EmailObj, error)
	SearchByContent(ctx context.Context, content string, limit int) ([]EmailObj, error)
}

var mailboxReaderState = struct {
	sync.RWMutex
	reader MailboxReader
}{}

func SetMailboxReader(reader MailboxReader) {
	mailboxReaderState.Lock()
	defer mailboxReaderState.Unlock()
	mailboxReaderState.reader = reader
}

func activeMailboxReader() MailboxReader {
	mailboxReaderState.RLock()
	defer mailboxReaderState.RUnlock()
	return mailboxReaderState.reader
}

type mailWatcherResult struct {
	kind string
	code string
	err  error
}

type MailWatcher struct {
	mailbox   string
	proxy     string
	timeout   int
	seenKeys  map[string]struct{}
	ctx       context.Context
	taskLogID string
	ch        chan mailWatcherResult
}

const codeKeywords = `安全代码|一次性代码|验证码|驗證碼|安全碼|verification code|security code|one-time code|セキュリティ\s*コード|確認コード|보안\s*코드|확인\s*코드|Sicherheitscode|Bestätigungscode|code de sécurité|code de vérification|código de seguridad|código de segurança|код безопасности|код подтверждения|رمز الأمان|رمز التحقق|codice di sicurezza|beveiligingscode|güvenlik kodu|kod bezpieczeństwa|รหัสความปลอดภัย|mã bảo mật|kode keamanan`

var (
	codeContextRe = regexp.MustCompile(`(?is)(?:` + codeKeywords + `)[^\d]{0,30}(\d{4,8})`)
	codeKeywordRe = regexp.MustCompile(`(?is)` + codeKeywords)
	sixDigitRe    = regexp.MustCompile(`(^|[^\d])(\d{6})([^\d]|$)`)
)

func resolveMailProxy(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" || !mailUseProxy {
		if proxy != "" {
			logDebug("邮箱 API 直连, 不使用账号代理")
		}
		return ""
	}
	proxy = normalizeProxy(proxy)
	logDebug("邮箱 API 使用账号代理: scheme=%s", proxyScheme(proxy))
	return proxy
}

func authHeaders() (map[string]string, error) {
	token := strings.TrimSpace(mailAPIKey)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		return nil, newAuthError("未配置 CLOUD_MAIL_JWT/CLOUD_MAIL_API_KEY, 无法从本项目收件", AuthStatusRequestError)
	}
	return map[string]string{"Authorization": token}, nil
}

func postJSON(ctx context.Context, path string, payload map[string]any, proxy string, timeout int) (any, error) {
	headers, err := authHeaders()
	if err != nil {
		return nil, err
	}
	session, err := newPlainSession(ctx, resolveMailProxy(proxy), timeout)
	if err != nil {
		return nil, wrapAuthError(fmt.Sprintf("Cloud Mail 请求异常: %s", err), AuthStatusRequestError, err)
	}
	resp, err := session.Post(mailAPIBase+path, requestOptions{
		Headers: headers,
		JSON:    payload,
	})
	if err != nil {
		return nil, wrapAuthError(fmt.Sprintf("Cloud Mail 请求异常: %s", err), AuthStatusRequestError, err)
	}

	var data any
	if err := resp.JSON(&data); err != nil {
		return nil, newAuthError(fmt.Sprintf("Cloud Mail 返回非 JSON: HTTP %d", resp.StatusCode), AuthStatusRequestError)
	}
	if resp.StatusCode >= 400 {
		return nil, newAuthError(fmt.Sprintf("Cloud Mail 请求失败: HTTP %d, %v", resp.StatusCode, data), AuthStatusRequestError)
	}
	if m := asMap(data); m != nil {
		if _, ok := m["code"]; ok {
			if asInt(m["code"]) != 200 {
				return nil, newAuthError(fmt.Sprintf("Cloud Mail API 失败: %s", asString(m["message"])), AuthStatusRequestError)
			}
			return m["data"], nil
		}
	}
	return data, nil
}

func mailDomain() (string, error) {
	if len(mailDomains) == 0 {
		return "", newAuthError("未配置 Cloud Mail 域名", AuthStatusRequestError)
	}
	index := mailDomainIndex
	if index < 0 {
		index = 0
	}
	if index >= len(mailDomains) {
		index = len(mailDomains) - 1
	}
	return mailDomains[index], nil
}

func createTempMailbox(ctx context.Context, accountEmail string, preferredBindingAddress string) (string, error) {
	if err := contextOrBackground(ctx).Err(); err != nil {
		return "", wrapAuthError(fmt.Sprintf("生成辅助邮箱取消: %s", err), AuthStatusRequestError, err)
	}
	if preferred := strings.ToLower(strings.TrimSpace(preferredBindingAddress)); preferred != "" {
		logInfo("使用导入指定辅助邮箱")
		logDebug("辅助邮箱地址: %s", preferred)
		return preferred, nil
	}
	logInfo("生成辅助邮箱")
	mailbox, err := deterministicAuxiliaryAddress(accountEmail)
	if err != nil {
		return "", err
	}
	logDebug("辅助邮箱地址: %s", mailbox)
	return mailbox, nil
}

func normalizeEmail(row map[string]any) EmailObj {
	text := asString(row["text"])
	if text == "" {
		text = stripHTML(asString(row["content"]))
	}
	return EmailObj{
		ID:               firstNonNil(row["emailId"], row["email_id"]),
		ReceivedAt:       firstString(row["createTime"], row["create_time"]),
		Subject:          asString(row["subject"]),
		Preview:          text,
		VerificationCode: asString(row["code"]),
		To:               firstString(row["toEmail"], row["to_email"]),
		From:             firstString(row["sendEmail"], row["send_email"]),
		Raw:              row,
	}
}

func mailList(ctx context.Context, mailbox, proxy string, limit int, fuzzy bool) ([]EmailObj, error) {
	if reader := activeMailboxReader(); reader != nil {
		return reader.List(ctx, mailbox, limit, fuzzy)
	}
	return cloudMailList(ctx, mailbox, proxy, limit, fuzzy)
}

func cloudMailList(ctx context.Context, mailbox, proxy string, limit int, fuzzy bool) ([]EmailObj, error) {
	toEmail := mailbox
	if fuzzy && !strings.Contains(mailbox, "@") {
		toEmail = mailbox + "%"
	}
	size := limit
	if size <= 0 {
		size = 5
	}
	if size > 50 {
		size = 50
	}
	payload := map[string]any{
		"type": 0,
		"size": size,
		"num":  1,
	}
	if toEmail != "" {
		payload["toEmail"] = toEmail
	}
	data, err := postJSON(ctx, "/api/public/emailList", payload, proxy, 20)
	if err != nil {
		return nil, err
	}
	rows := asSlice(data)
	if rows == nil {
		logWarning("邮箱列表返回非预期格式: %T", data)
		return nil, nil
	}
	out := make([]EmailObj, 0, len(rows))
	for _, row := range rows {
		if m := asMap(row); m != nil {
			out = append(out, normalizeEmail(m))
		}
	}
	return out, nil
}

func mailListByContent(ctx context.Context, content, proxy string, limit int) ([]EmailObj, error) {
	if reader := activeMailboxReader(); reader != nil {
		return reader.SearchByContent(ctx, content, limit)
	}
	return cloudMailListByContent(ctx, content, proxy, limit)
}

func cloudMailListByContent(ctx context.Context, content, proxy string, limit int) ([]EmailObj, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}
	if !strings.Contains(content, "%") {
		content = "%" + content + "%"
	}
	size := limit
	if size <= 0 {
		size = 20
	}
	if size > 50 {
		size = 50
	}
	data, err := postJSON(ctx, "/api/public/emailList", map[string]any{
		"content": content,
		"type":    0,
		"size":    size,
		"num":     1,
	}, proxy, 20)
	if err != nil {
		return nil, err
	}
	rows := asSlice(data)
	if rows == nil {
		logWarning("邮箱正文搜索返回非预期格式: %T", data)
		return nil, nil
	}
	out := make([]EmailObj, 0, len(rows))
	for _, row := range rows {
		if m := asMap(row); m != nil {
			out = append(out, normalizeEmail(m))
		}
	}
	return out, nil
}

func searchMailboxes(ctx context.Context, query, proxy string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	emails, err := mailList(ctx, query, proxy, 50, true)
	if err != nil {
		return nil
	}
	var mailboxes []string
	seen := map[string]struct{}{}
	for _, email := range emails {
		if email.To != "" {
			if _, ok := seen[email.To]; !ok {
				seen[email.To] = struct{}{}
				mailboxes = append(mailboxes, email.To)
			}
		}
	}
	return mailboxes
}

func searchMailboxesByContent(ctx context.Context, content, proxy string) []string {
	emails, err := mailListByContent(ctx, content, proxy, 50)
	if err != nil {
		return nil
	}
	var mailboxes []string
	seen := map[string]struct{}{}
	for _, email := range emails {
		if email.To != "" {
			if _, ok := seen[email.To]; !ok {
				seen[email.To] = struct{}{}
				mailboxes = append(mailboxes, email.To)
			}
		}
	}
	return mailboxes
}

func extractCodeFromEmail(email EmailObj) string {
	haystack := email.Subject + " " + email.Preview
	code := strings.TrimSpace(email.VerificationCode)
	if code != "" && codeKeywordRe.MatchString(haystack) {
		return code
	}
	if code != "" {
		logDebug("跳过缺少验证码上下文的 API code: id=%v subject=%s", email.ID, email.Subject)
	}
	if match := codeContextRe.FindStringSubmatch(haystack); len(match) > 1 {
		return match[1]
	}
	if !codeKeywordRe.MatchString(haystack) {
		logDebug("跳过非验证码邮件: id=%v subject=%s", email.ID, email.Subject)
		return ""
	}
	if match := sixDigitRe.FindStringSubmatch(haystack); len(match) > 2 {
		return match[2]
	}
	return ""
}

func mailMessageKey(email EmailObj) string {
	if email.ID != nil {
		return fmt.Sprintf("id:%v", email.ID)
	}
	preview := email.Preview
	if len(preview) > 120 {
		preview = preview[:120]
	}
	return strings.Join([]string{email.ReceivedAt, email.Subject, preview}, "|")
}

func snapshotMailboxKeys(ctx context.Context, mailbox, proxy string) map[string]struct{} {
	emails, err := mailList(ctx, mailbox, proxy, 20, false)
	if err != nil {
		return map[string]struct{}{}
	}
	keys := map[string]struct{}{}
	for _, email := range emails {
		keys[mailMessageKey(email)] = struct{}{}
	}
	return keys
}

func mailWaitCode(ctx context.Context, mailbox, proxy string, timeout int, seenKeys map[string]struct{}) (string, error) {
	ctx = contextOrBackground(ctx)
	if timeout <= 0 {
		timeout = mailPollTimeout
	}
	pollInterval := normalizedMailPollInterval()
	if seenKeys == nil {
		seenKeys = map[string]struct{}{}
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollCount := 0
	errorCount := 0
	var lastError error

	logInfo("开始轮询邮箱验证码 (超时 %ds)", timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", wrapAuthError(fmt.Sprintf("辅助邮箱验证码轮询取消: %s", err), AuthStatusRequestError, err)
		}
		pollCount++
		emails, err := mailList(ctx, mailbox, proxy, 5, false)
		if err != nil {
			errorCount++
			lastError = err
			logWarning("轮询 #%d: 邮箱 %s 请求失败: %s", pollCount, mailbox, err)
			if err := sleepContext(ctx, time.Duration(pollInterval)*time.Second); err != nil {
				return "", wrapAuthError(fmt.Sprintf("辅助邮箱验证码轮询取消: %s", err), AuthStatusRequestError, err)
			}
			continue
		}
		logDebug("轮询 #%d: 邮箱 %s 收到 %d 封邮件", pollCount, mailbox, len(emails))
		for _, email := range emails {
			if _, ok := seenKeys[mailMessageKey(email)]; ok {
				continue
			}
			if code := extractCodeFromEmail(email); code != "" {
				logDebug("邮箱验证码已收到")
				return code, nil
			}
		}
		if err := sleepContext(ctx, time.Duration(pollInterval)*time.Second); err != nil {
			return "", wrapAuthError(fmt.Sprintf("辅助邮箱验证码轮询取消: %s", err), AuthStatusRequestError, err)
		}
	}

	code, finalErr := finalMailboxCodeCheck(ctx, mailbox, proxy, seenKeys, "超时前最终检查")
	if finalErr != nil {
		errorCount++
		lastError = finalErr
		logWarning("最终检查邮箱 %s 请求失败: %s", mailbox, finalErr)
	} else if code != "" {
		return code, nil
	}

	if grace := normalizedMailLateArrivalGrace(); grace > 0 {
		graceDeadline := time.Now().Add(time.Duration(grace) * time.Second)
		gracePoll := 1 * time.Second
		for time.Now().Before(graceDeadline) {
			if err := sleepContext(ctx, gracePoll); err != nil {
				return "", wrapAuthError(fmt.Sprintf("辅助邮箱验证码轮询取消: %s", err), AuthStatusRequestError, err)
			}
			pollCount++
			code, err := finalMailboxCodeCheck(ctx, mailbox, proxy, seenKeys, "晚到宽限检查")
			if err != nil {
				errorCount++
				lastError = err
				logWarning("晚到宽限检查邮箱 %s 请求失败: %s", mailbox, err)
				continue
			}
			if code != "" {
				logInfo("晚到宽限检查收到邮箱验证码")
				return code, nil
			}
		}
	}

	if lastError != nil && errorCount >= pollCount {
		return "", lastError
	}
	logError("邮箱验证码超时 (%ds, 轮询 %d 次)", timeout, pollCount)
	logDebug("邮箱验证码超时邮箱: %s", mailbox)
	return "", newAuthError("辅助邮箱验证码超时未收到", AuthStatusCodeTimeout)
}

func normalizedMailPollInterval() int {
	if mailPollInterval <= 0 {
		return 2
	}
	return mailPollInterval
}

func normalizedMailLateArrivalGrace() int {
	if mailLateArrivalGrace < 0 {
		return 0
	}
	return mailLateArrivalGrace
}

func finalMailboxCodeCheck(ctx context.Context, mailbox, proxy string, seenKeys map[string]struct{}, label string) (string, error) {
	emails, err := mailList(ctx, mailbox, proxy, 10, false)
	if err != nil {
		return "", err
	}
	logDebug("%s: 邮箱 %s 收到 %d 封邮件", label, mailbox, len(emails))
	for _, email := range emails {
		if _, ok := seenKeys[mailMessageKey(email)]; ok {
			continue
		}
		if code := extractCodeFromEmail(email); code != "" {
			logDebug("%s收到邮箱验证码", label)
			return code, nil
		}
		logDebug("%s未提取验证码: id=%v subject_len=%d preview_len=%d", label, email.ID, len(email.Subject), len(email.Preview))
	}
	return "", nil
}

func startCodeWatcher(ctx context.Context, mailbox, proxy string, timeout int, seenKeys map[string]struct{}) *MailWatcher {
	if timeout <= 0 {
		timeout = mailPollTimeout
	}
	w := &MailWatcher{
		mailbox:   mailbox,
		proxy:     proxy,
		timeout:   timeout,
		seenKeys:  seenKeys,
		ctx:       contextOrBackground(ctx),
		taskLogID: getTaskLogID(),
		ch:        make(chan mailWatcherResult, 1),
	}
	go w.run()
	return w
}

func (w *MailWatcher) run() {
	previous := setTaskLogID(w.taskLogID)
	defer setTaskLogID(previous)
	code, err := mailWaitCode(w.ctx, w.mailbox, w.proxy, w.timeout, w.seenKeys)
	if err != nil {
		w.ch <- mailWatcherResult{kind: "error", err: err}
		return
	}
	w.ch <- mailWatcherResult{kind: "code", code: code}
}

func (w *MailWatcher) getCode(timeout int) (string, error) {
	if timeout <= 0 {
		timeout = w.defaultCodeWaitTimeout()
	}
	select {
	case result := <-w.ch:
		if result.kind == "code" {
			return result.code, nil
		}
		if _, ok := result.err.(*AuthError); ok {
			return "", result.err
		}
		return "", newAuthError(fmt.Sprintf("辅助邮箱收码失败: %s", result.err), AuthStatusRequestError)
	case <-time.After(time.Duration(timeout) * time.Second):
		return "", newAuthError("辅助邮箱验证码超时未收到", AuthStatusCodeTimeout)
	case <-w.ctx.Done():
		return "", wrapAuthError(fmt.Sprintf("辅助邮箱验证码轮询取消: %s", w.ctx.Err()), AuthStatusRequestError, w.ctx.Err())
	}
}

func (w *MailWatcher) defaultCodeWaitTimeout() int {
	if w == nil {
		return normalizedMailPollInterval() + normalizedMailLateArrivalGrace() + 5
	}
	return w.timeout + normalizedMailPollInterval() + normalizedMailLateArrivalGrace() + 5
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstString(values ...any) string {
	for _, value := range values {
		if s := asString(value); s != "" {
			return s
		}
	}
	return ""
}
