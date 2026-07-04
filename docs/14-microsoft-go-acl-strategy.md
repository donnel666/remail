# Microsoft Go ACL 策略

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-07-01 | V1.1 | Codex | 补充 P1-I2 Microsoft TXT 导入辅助邮箱行格式；不改变辅助邮箱绑定实体和状态机归属。 |
| 2026-07-01 | V1.2 | Codex | 补充 P1-I2 ResourceImport artifact 索引；落实原始导入文件和安全失败明细进入 MinIO private bucket 的原设计。 |
| 2026-07-02 | V1.3 | Codex | 补充 P1-I2 ResourceImport 异步状态查询、Asynq 重试边界和导入成功事务幂等要求；不改变辅助邮箱绑定实体和状态机归属。 |
| 2026-07-02 | V1.4 | Codex | 补充 P1-I2 辅助邮箱英文统一为 `bindingAddress`，中文仍称辅助邮箱；对齐 Core 的 `binding` 用途命名。 |
| 2026-07-02 | V1.5 | Codex | 补充 P1-I2 Microsoft TXT 导入错误处理策略：默认错误跳过，错误中止可选；不改变 Microsoft ACL 与 Core 资源边界。 |
| 2026-07-02 | V1.6 | Codex | 补充 P1-I2 Microsoft 导入前端预处理策略：前端减负不改变后端权威校验。 |
| 2026-07-03 | V1.7 | Codex | 补充 Microsoft ACL 使用 BC-PROXY 的直连兜底和 3 次代理尝试预算；不改变 Microsoft ACL 边界。 |
| 2026-07-04 | V1.8 | Codex | 补充 P1-I3 资源验证异步入口：Core 创建 `ResourceValidation` 任务，MailTransport ACL 在 worker 内执行 Microsoft RT 刷新或获取 RT；此为缺失设计补充，不改变 Microsoft ACL 归属。 |
| 2026-07-04 | V1.9 | Codex | 补充 P1-I3 Microsoft ACL 临时失败分类：代理、超时、上游 5xx/429 等请求失败返回 `request`，由 Core 验证任务重试，不直接把资源置异常。此为缺失设计补充，不改变 Microsoft ACL 归属。 |
| 2026-07-04 | V1.10 | Codex | 补充 P1-I3 `rt` 验证流程适配边界：Microsoft 页面交互流程保持不变，只把辅助邮箱输入、收码来源和状态回写适配到当前 MailTransport。此为缺失设计补充，不改变 ACL 核心策略。 |
| 2026-07-04 | V1.11 | Codex | 补充 P1-I3 Microsoft 资源验证三段式流程：RT 获取/刷新、Graph 优先 IMAP 回退收件、项目邮件匹配预留；资源健康成功条件止于第二步收件成功。此为缺失设计补充，不改变资源状态机。 |
| 2026-07-04 | V1.12 | Codex | 补充 P1-I3 Microsoft `graphAvailable` 回写策略：第二步收件成功后记录 Graph 主路径是否可用，供资源页展示和筛选。此为缺失设计补充，不改变 ACL 成功条件。 |
| 2026-07-04 | V1.13 | Codex | 纠正 P1-I3 验证码失败分类和资源状态边界：`code_timeout/code_error` 是确定性绑定失败，不归入 `request` 重试；`deleted` 是 Core 命令终态，不由 ACL 产生。此为缺失设计补充，不改变 Microsoft 页面交互策略。 |

> 适用范围：Microsoft 邮箱导入、上传验证、RT 续期、Graph 邮件拉取、辅助邮箱绑定。
>
> Go 版只有一个主后端运行时。Microsoft 交互能力做成 Go 内部模块，不保留独立协议服务、跨进程调用或第二套部署单元。

---

## 1. 定位

BC-MAILTRANSPORT 内部实现 Microsoft 协议 ACL。它负责 Microsoft 世界的页面流、OAuth、Graph 和错误识别，但不拥有资源状态、订单状态、钱包、项目规则或邮件匹配规则。

| 能力 | 归属 | 说明 |
|------|------|------|
| 页面登录流 | Go Microsoft ACL | 账号检查、密码提交、KMSI、Consent、MFA/手机验证识别、token polling。 |
| OAuth/Graph | Go Microsoft ACL | 用 RT 换 AT，必要时保存 rotated RT，调用 Graph 拉取邮件。 |
| 辅助邮箱绑定 | Go Microsoft ACL + MailTransport 实体 | 选择辅助邮箱、等待验证码、记录绑定状态。 |
| 代理选择 | BC-PROXY | 资源代理 7 天绑定、系统代理兜底、IPv4/IPv6 选择和错误异常隔离。 |
| 上游错误识别 | Go Microsoft ACL | 把密码错误、账号异常、MFA、手机验证、锁号等收敛成稳定内部错误分类。 |
| 资源状态 | BC-CORE | `pending/normal/abnormal/disabled`。 |
| 邮件事实 | BC-MAILMATCH | Graph/IMAP/SMTP 返回的结构化邮件落为 `Message`。 |

Go 禁止：

| 禁止项 | 原因 |
|--------|------|
| 拆出独立 Microsoft 协议服务 | 会重新形成双运行时、双部署和双错误体系。 |
| 在领域层解析 Microsoft 页面 | 协议细节只能在 MailTransport ACL。 |
| 把 Microsoft 页面状态扩散成资源状态 | KMSI、Consent、MFA 等只做任务诊断，不进入领域枚举。 |
| 回退到 Basic Auth 验证 Microsoft | 业务验证路径必须统一。 |
| 把 Microsoft TXT 行格式暴露成 HTTP 契约 | 该格式只能作为导入文件内部格式。 |

---

## 2. Go 实现组件

包建议：

```text
internal/mailtransport/infra/microsoft
```

| 组件 | 职责 |
|------|------|
| `AuthClient` | 获取 RT、刷新 AT、处理 rotated RT。 |
| `PageFlow` | 实现页面流，处理登录页面、表单、跳转和 token polling。 |
| `GraphClient` | 用 AT 拉取 Graph 邮件，返回结构化 message DTO。 |
| `BindingCoordinator` | 选择辅助邮箱、等待验证码、更新绑定状态。 |
| `ErrorClassifier` | 把页面/Graph/网络错误映射为内部分类和安全文案。 |
| `HTTPClient` | 使用 ProxyPort 返回的代理或直连路线，统一超时、cookie jar、重试、TLS/HTTP2 设置、attempt 和 requestId。 |

推荐库：

| 能力 | Go 库 |
|------|-------|
| HTTP 基础 | `net/http` |
| Cookie | `net/http/cookiejar` + `golang.org/x/net/publicsuffix` |
| HTML 解析 | `github.com/PuerkitoBio/goquery` |
| OAuth 辅助 | `golang.org/x/oauth2`，必要时手写 token 请求 |
| Graph 调用 | 优先直接 HTTP 调 Graph REST；SDK 只在明显减少复杂度时引入 |
| TLS 指纹 | 默认不用；确有上游限制时封装 `utls` 到 `HTTPClient`，不泄露到业务层 |
| 测试 HTTP | `httptest` + 自定义 `RoundTripper` fixture |

不默认引入浏览器自动化。只有当 HTTP 页面流无法稳定实现时，才通过 ADR 评估 Rod/Chromedp 等方案；一期不把浏览器作为基础依赖。

---

## 3. Go 模块方法

Microsoft ACL 是 Go 进程内模块，通过 Port/Adapter 暴露方法。

### 3.1 `AcquireToken`

用途：用 `email + password` 获取 `clientId + refreshToken`。

输入：

| 字段 | 说明 |
|------|------|
| `resourceId` | Go 资源 ID，用于追踪。 |
| `email` | Microsoft 邮箱。 |
| `password` | Microsoft 邮箱密码，原值只在 ACL 内部使用。 |
| `proxy` | BC-PROXY 返回的代理配置，辅助邮箱绑定必须是 IPv4。 |
| `requestId` | 任务追踪 ID。 |

输出：

| 字段 | 说明 |
|------|------|
| `email` | 邮箱 |
| `clientId` | Microsoft clientId |
| `refreshToken` | Microsoft RT |
| `bindingAddress` | 使用的辅助邮箱，可空 |
| `category` | 失败时的内部错误分类 |
| `message` | 安全错误文案 |

### 3.2 `RefreshToken`

用途：已有 `clientId + refreshToken` 时刷新 AT，并在上游返回时带回新 RT。

规则：

| 场景 | 成功条件 |
|------|----------|
| 已有 RT 上传验证 | 有 `accessToken`；有新 RT 则保存，没有则保留旧 RT。 |
| RT 续期任务 | 必须有新 `refreshToken`。 |
| Graph 拉取前刷新 | 有 `accessToken` 即可；rotated RT 必须同步回 Core。 |

### 3.3 `FetchMessages`

用途：用 Microsoft 凭据拉取 Graph 邮件。

输入：

| 字段 | 说明 |
|------|------|
| `resourceId` | 资源 ID |
| `emailAddress` | Microsoft 邮箱地址 |
| `clientId` | Microsoft clientId |
| `refreshToken` | 当前 RT |
| `scopes` | OAuth scopes |
| `purpose` | `validation_history/alias_detect/order_fetch/aftersale_check/manual_fetch` |
| `since` | 拉取起点；全量拉取场景可为空 |
| `inboxLimit/junkLimit` | 文件夹限制 |
| `proxy` | BC-PROXY 返回的代理配置 |
| `requestId` | 任务追踪 ID |

成功返回结构化 `messages[]`，Go 将其转换为 BC-MAILMATCH 的 `Message`。

### 3.4 P1-I3 `ValidateResource`

用途：验证 Microsoft 资源本体是否可用。该流程在 Core 的异步 `ResourceValidation` worker 中调用，HTTP 请求不得同步等待 Microsoft 网络调用。

验证分三步：

| 步骤 | 当前要求 | 是否影响资源健康 |
|------|----------|------------------|
| 1. RT 获取/刷新 | 有 `clientId + refreshToken` 时先用 RT 换 Graph AT；没有 RT 时沿用 `rt` 页面流获取 RT，辅助邮箱绑定策略不改。 | 是。获取/刷新失败按错误分类返回。 |
| 2. 收全部邮件 | 优先用 RT 换 Graph AT 调 Microsoft Graph，分页读取 `Inbox` 和 `JunkEmail`；Graph 不可用时用同一 RT 换 IMAP token 回退 Outlook IMAP。 | 是。Graph 或 IMAP 任一路径读取接口返回正常，即资源验证可成功。 |
| 3. 项目匹配与关系插入 | 将第二步结构化邮件交给后续 BC-MAILMATCH/Project 规则，匹配系统项目并插入业务关系。 | 否。该步骤是业务增强，不参与资源本体是否正常的判断。 |

当前 P1-I3 只必须完成前两步。第三步必须在代码中保留扩展点并在文档中记录技术债，等 Project 与 MailMatch 模块具备项目规则、邮件匹配和关系落库能力后再接入。第三步失败、未实现或无匹配结果，不得把 Microsoft 资源置为 `abnormal`。

收件策略：

| 项 | 规则 |
|----|------|
| 主路径 | Graph REST API。使用验证 HTTP client/session 发请求，复用 TLS 指纹、代理、超时和安全日志策略。 |
| 回退 | Graph token 或 Graph 拉取失败后，使用 RT 换 IMAP accessToken，再连接 Outlook IMAP 读取 `INBOX` 和垃圾箱。 |
| 文件夹 | 验证阶段必须包含收件箱和垃圾箱；Graph 文件夹使用 `inbox`、`junkemail`。 |
| 分页 | Graph 读取必须跟随 `@odata.nextLink`，直到文件夹读完。 |
| 结果 | 只返回结构化摘要给验证编排；正文、Token、验证码不得进入 Core、响应或普通日志。 |
| 成功 | 第二步 Graph 或 IMAP 任一路径接口正常返回，即本次资源验证可以成功。Graph 路径成功时返回 `graphAvailable=true`；Graph 失败但 IMAP 回退成功时返回 `graphAvailable=false`。 |

---

## 4. 辅助邮箱绑定

辅助邮箱绑定通过 Go 进程内调用完成。Go ACL 直接调用 MailTransport 应用服务或 Port 完成以下动作：

| 动作 | 语义 |
|------|------|
| `AllocateBinding` | 为 Microsoft 授权选择或复用辅助邮箱，开启一次绑定尝试。 |
| `ResolveBinding` | Microsoft 页面提示已绑定时，根据掩码恢复真实辅助邮箱。 |
| `MarkBindingStatus` | 回写 `code_sent/verified/timeout/failed/expired`。 |
| `WaitBindingCode` | 等待辅助邮箱验证码邮件。 |

验证码、密码、RT、accessToken 不得出现在响应、普通日志、SystemLog 或 OperationLog 中。

P1-I3 适配规则：

| 对接点 | 当前系统策略 |
|--------|--------------|
| 导入输入 | Core 解析 TXT 中的 `bindingAddress` 后，只通过 Port 交给 MailTransport 记录 `MicrosoftBindingMailbox(pending)`；不写入 `microsoft_resources`，不进入 Core 资源状态机。 |
| 页面流使用 | `AcquireToken` 页面流遇到 AddProof/Identity/OTP 时，优先使用 `MicrosoftBindingMailbox.bindingAddress`；没有输入记录时，继续使用原 `rt` 确定性辅助邮箱生成规则。 |
| 收码来源 | `WaitBindingCode` 的轮询、去重和验证码提取逻辑沿用 `rt`；底层 `MailboxReader` 在本项目中读取 `inbound_mails + MinIO private RFC822`，不再依赖旧项目 API。 |
| 入站接收 | SMTP RCPT 阶段必须能通过 `microsoft_binding_mailboxes.binding_address` 解析到 Microsoft 资源和 owner，使 Microsoft 发来的验证码进入当前入站任务。 |
| 状态回写 | 验证成功回写 `verified`；验证码超时回写 `timeout`；确定性绑定失败回写 `failed`；这些状态只用于 MailTransport 排障，不改变 Core 资源状态枚举。 |

`rt` 的 Microsoft 页面交互顺序、KMSI/Consent/Identity/Proofs 处理、错误分类、验证码轮询和代理尝试策略不得因为接入当前系统而重写。允许变化的只有输入来源、收码读取实现、状态持久化和安全诊断输出。

---

## 5. 导入格式

Microsoft TXT 导入支持四种行格式：

```text
email----password
email----password----辅助邮箱
email----password----clientId----refreshToken
email----password----clientId----refreshToken----辅助邮箱
```

处理规则：

| 行格式 | Go 导入动作 | 后续验证 |
|--------|-------------|----------|
| `email----password` | 创建资源，保存 email/password，状态 `pending` | 调 `AcquireToken`。 |
| `email----password----辅助邮箱` | 创建资源，保存 email/password，状态 `pending`；辅助邮箱作为绑定输入传给 MailTransport，不进入 Core 资源表。 | 调 `AcquireToken`，辅助邮箱绑定状态由 MailTransport 记录。 |
| `email----password----clientId----refreshToken` | 创建资源，保存四个字段，状态 `pending` | 调 `RefreshToken` 检查 RT 可用性，不重跑密码授权。 |
| `email----password----clientId----refreshToken----辅助邮箱` | 创建资源，保存四个资源本体字段，状态 `pending`；辅助邮箱作为绑定输入传给 MailTransport，不进入 Core 资源表。 | 调 `RefreshToken`，辅助邮箱绑定状态由 MailTransport 记录。 |

P1-I2 阶段只补充 TXT 解析格式和 Core 资源创建。辅助邮箱“已分配、已发码、已验证、失败、过期”等状态必须由辅助邮箱绑定实体表达，不允许塞进 `microsoft_resources` 或 Core 资源状态枚举。

导入 HTTP 契约使用 `multipart/form-data` 的 `file` 字段上传 TXT 文件，并允许提交 `errorStrategy=skip|abort`。默认 `skip`，用户侧文案为“错误跳过”；`abort` 用户侧文案为“错误中止”。前端可以用同一套四种 `----` 行格式规则预处理上传内容：`skip` 时过滤行格式错误和文件内重复，`abort` 时直接拦截首个行级错误并不上传；该预处理只用于减少无效上传和 worker 压力，不替代后端解析、查重和事务唯一约束。原始导入文件进入 MinIO private bucket；失败明细只能包含行号、邮箱、安全错误分类和安全消息。Core 只保存 `ResourceImport` 安全索引，不保存原始文件内容。HTTP 层只落 MinIO private bucket、创建 `ResourceImport(processing)` 并投递 Asynq，返回 `202 Accepted`；实际解析、查重、资源创建和失败明细生成由后端 Asynq worker 异步执行。`skip` 对行格式错误、文件内重复、已有未删除邮箱等行级错误写入失败明细并继续导入有效行，最终 `ResourceImport(imported)` 的 `importedCount` 只统计实际写入资源数；`abort` 在首个行级错误处写入失败明细并把任务置为 `ResourceImport(failed)`。确定性业务失败写入失败明细和终态后不再重试；基础设施失败交给 Asynq 重试，耗尽重试后写安全失败摘要。资源创建和 `ResourceImport(imported)` 必须在同一个数据库事务中完成，重复投递遇到 `imported/failed` 终态直接 no-op。前端只能通过安全状态接口查询 `processing/imported/failed`、导入数量和安全错误摘要，不允许读取 MinIO objectKey。

---

## 6. 错误分类映射

Go Microsoft ACL 返回 `category` 作为内部错误分类。对外不返回业务响应码，只按统一 API 规范映射为 HTTP 状态码和安全 `message`；需要排障时，把分类、requestId、resourceId 和禁敏后的诊断写入 SystemLog 或资源诊断字段。

| 错误分类 | 业务含义 | 对外 HTTP | 对外 message 原则 |
|----------|----------|-----------|--------------------|
| `password` | 密码错误 | `422` | `Microsoft account or password is incorrect.` |
| `unknown` | 账号不存在或不可用 | `422` | `Microsoft account or password is incorrect.` |
| `abnormal` | 账号异常 | `422` | `Microsoft account requires manual review.` |
| `mfa` | 需要 MFA | `422` | `Microsoft account requires additional verification.` |
| `phone` | 需要手机验证 | `422` | `Microsoft account requires additional verification.` |
| `passkey` | 需要 Passkey | `422` | `Microsoft account requires additional verification.` |
| `locked` | 账号锁定 | `422` | `Microsoft account is currently unavailable.` |
| `bound` | 辅助邮箱已绑定 | `409` | `Binding mailbox is already bound.` |
| `code_timeout` | 验证码超时 | `422` | `Verification code is incorrect or expired.` |
| `code_error` | 验证码错误 | `422` | `Verification code is incorrect or expired.` |
| `auth_timeout` | 授权超时 | `503` | `Microsoft authorization timed out. Please try again later.` |
| `request` | 上游请求失败 | `502/503` | `Microsoft mail service is temporarily unavailable.` |

账号不存在、密码错误必须合并为同一类对外文案，避免账号枚举；验证码错误或超时可以直接说明验证码错误或过期。`code_timeout/code_error` 是本次绑定验证码流程的确定性失败，不能归入 `request` 触发基础设施重试。

代理错误必须上报 BC-PROXY。资源代理失败时，允许本次业务按代理池规则获取系统代理重试；同一业务链路最多尝试 3 次代理路线。达到尝试预算或资源/系统代理都不可用时，ProxyPort 返回 `direct=true` 的系统直连路线，HTTPClient 必须禁用代理继续执行；直连失败属于上游或本机网络失败，不得上报为代理失败计数，内部详情写 SystemLog。

IP 版本规则：

| 场景 | 策略 |
|------|------|
| 辅助邮箱绑定 | 强制 `ipv4`。 |
| Graph 邮件拉取 | 默认 `auto`，允许 `ipv6`。 |
| 获取 RT/刷新 AT | 默认 `auto`，调用方可指定 `ipv4/ipv6`。 |

---

## 7. 状态边界

Microsoft 页面状态不是 Go 领域状态。

Go 资源状态只保留：

```text
pending
normal
abnormal
disabled
deleted
```

`deleted` 是 Core 用户删除私有资源命令写入的终态，不由 Microsoft ACL 验证流程产生。KMSI、Consent、MFA、手机验证、页面跳转、Graph 错误都只能作为 ACL 结果和诊断返回，不进入资源状态枚举。

---

## 8. 事务和异步边界

Microsoft 页面流和 Graph 请求是外部网络调用，不能放进数据库事务。

| 步骤 | 规则 |
|------|------|
| 创建验证任务 | HTTP 入口只短事务写 `ResourceValidation(queued)` 和 OperationLog，投递 Asynq 后返回 `202 Accepted`。 |
| 执行 Microsoft ACL | 事务外执行，带 requestId 和超时。 |
| 成功保存凭据 | 短事务保存 clientId/RT、更新资源状态、写事件。 |
| 失败记录诊断 | 短事务写安全诊断和 SystemLog。 |
| rotated RT | 拉取或刷新成功后必须在短事务内保存新 RT。 |

已有 `clientId + refreshToken` 的资源验证优先走 OAuth token endpoint 刷新 AT；成功且上游返回新 RT 时保存 rotated RT，没有新 RT 则保留旧 RT。只有 `email + password` 的资源验证走 `AcquireToken` 页面流。页面流实现必须封装在 MailTransport ACL 内部，Core 只能感知稳定分类和安全文案。

Microsoft ACL 的 `request` 分类只表示代理、网络、超时、上游限流或服务不可用等临时请求失败。Core 收到该分类后只能让 `ResourceValidation` 任务在 `maxAttempts` 内重试或等待 dispatcher 恢复；达到上限后只把验证任务置为 `failed`，不能把 Microsoft 资源状态写成 `abnormal`。密码错误、MFA、账号锁定、账号异常、验证码错误或超时等确定性分类才是资源本体或绑定流程异常证据。

---

## 9. 测试要求

| 测试 | 要求 |
|------|------|
| 页面流 fixture | 沉淀 Microsoft HTML/JSON 样本，Go 用 `RoundTripper` 回放。 |
| 错误分类 | 密码错误、未知账号、MFA、手机验证、锁号、验证码超时、Graph 失败都有分类测试。 |
| 禁敏 | 密码、RT、AT、验证码、邮件正文不进入日志、错误响应、失败文件。 |
| rotated RT | Graph/token 返回新 RT 时必须保存；无新 RT 时保留旧 RT。 |
| 超时重试 | 网络超时、429、5xx 有受控重试和最终失败诊断。 |
| 辅助邮箱 | code_sent、verified、timeout、failed、expired 状态机完整。 |

---

## 10. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-MSACL-1 | Microsoft 复杂流程由 Go ACL 承接 | 新项目只有一个 Go 后端，减少双运行时和部署复杂度。 |
| ADR-MSACL-2 | Microsoft 交互逻辑用 Go fixture 驱动实现 | 保留已验证业务认知，同时把实现收敛到 Go 模块。 |
| ADR-MSACL-3 | 页面状态不进入领域状态 | 避免 Microsoft 临时页面语义污染核心模型。 |
| ADR-MSACL-4 | Microsoft ACL 只做进程内模块 | 它是协议适配器，不是业务 API 或独立服务。 |
| ADR-MSACL-5 | 外部网络调用不进数据库事务 | 避免长事务、锁等待和不可控回滚。 |
