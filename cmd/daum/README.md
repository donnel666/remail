# Daum 收件诊断 CMD

独立测试单个 Daum 账号的 IMAP/POP3 登录与读取，不依赖数据库、Redis 或运行中的 ReMail 服务。

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o daum ./cmd/daum
./daum -file /path/to/account.txt -protocol imap -limit 3 -timeout 45s
./daum -file /path/to/account.txt -protocol pop3 -limit 3 -timeout 45s
```

TXT 只放一个账号，支持以下格式：

```text
邮箱----APP密码
邮箱----登录密码----APP密码
邮箱----登录密码----APP密码----remail取件URL----Kakao账号邮箱
```

只使用主邮箱和 APP 密码登录。登录密码不用作协议密码，第四、第五段作为独立附加资料忽略；CMD 不访问取件 URL，也不要求链接中的邮箱与 Kakao 账号邮箱相同。这个诊断入口不调用平台导入流程。

省略 `-file` 时，从标准输入读取一行，收到换行后执行。凭据不要放在命令参数中；保存 TXT 时应限制文件权限，例如 `chmod 600 /path/to/account.txt`。交互输入可使用不回显的 shell 读取：

```bash
read -rs daum_credential_line
printf '\n'
printf '%s\n' "$daum_credential_line" | ./daum -protocol imap
unset daum_credential_line
```

输出 JSON 包含 `success`、`mailbox`、`protocol`、`network`（`direct` 或 `proxy`）、`elapsed`、`messageCount` 和邮件摘要。失败提供安全的 `category`，退出码为 1；输入错误退出码为 2。空邮箱读取成功时数量为 0，不能据此证明新邮件已经投递。

命令读取邮件时不改变已读状态、不删除邮件、不发送邮件、不写平台数据库。输出不包含原始密码、邮件正文或取件链接，主题中的常见数字验证码会遮蔽。

默认通过 IPv4 直接 TCP/TLS 连接做独立诊断。需要检查代理链路时，用 `-proxy-env` 指定保存代理 URL 的环境变量名，支持 HTTP/HTTPS CONNECT 和 SOCKS5/5h；不会自动读取 `HTTP_PROXY`，也不会输出代理 URL 或认证信息：

```bash
read -rs DAUM_CHECK_PROXY
export DAUM_CHECK_PROXY
./daum -protocol imap -proxy-env DAUM_CHECK_PROXY
unset DAUM_CHECK_PROXY
```

CMD 复用 Daum 后端协议客户端，以及微软 IMAP 共用的代理/TLS 拨号代码，校验服务端证书，最低 TLS 1.2，并按实际协议发送 `imap` 或 `pop3` ALPN。网页请求的 User-Agent、Cookie 和浏览器指纹不适用于 IMAP/POP3。

按用户授权，平台 Daum 验证、实时收件及历史读取默认使用服务器 IPv4 直连，不获取代理或反馈代理健康；微软策略保持不变。CMD 不加载平台数据库或代理池，只检查明确选择的 IPv4 直连或环境变量代理；显式代理诊断失败时不会自动切换直连。正文与微软共用 MIME 解析，优先 HTML，并已移除 Daum 原先独有的 8 MiB 拒绝条件。IMAP 读取 Inbox/Junk，保留只读语义。
