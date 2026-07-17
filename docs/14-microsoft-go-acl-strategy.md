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
| 2026-07-04 | V1.14 | Codex | 补充 P1-I3 本机收码适配策略：当前系统没有旧 `rt` 的输出文件/外部邮件 API 即时可见性，必须用数据库绑定事实替代已知辅助邮箱记录，并允许收码读取 pending 入站原文和晚到宽限。此为缺失设计补充，不改变 KMSI/Consent/OTP/Token 主流程。 |
| 2026-07-05 | V1.15 | Codex | 补充 P1-I3 Microsoft 验证错误分类细化：保留参考实现的密码、未知邮箱、MFA、Passkey、手机验证、锁定、账号异常、已绑定、OAuth、Graph/IMAP 等内部分类；`request/auth_timeout` 为临时失败，由 Core 任务重试处理。此为缺失诊断补充，不改变资源状态机。 |
| 2026-07-12 | V1.16 | Codex | 补充管理员 RT 刷新、资源级手工 Fetch 和显式别名 schedule 加速入口；三者复用既有 ACL、代理和安全错误分类，并始终在 durable worker 中执行。 |
| 2026-07-12 | V1.17 | Codex | 收敛协议结果写回：管理员 Token/Fetch worker 通过 Core tx-bound `MicrosoftCredentialPort` 保存 revision/version/诊断，消费者 repository 不直连 Core 表；Alias expedite 只保留 receipt/audit 命令入口。 |
| 2026-07-12 | V1.18 | Codex | 明确管理员详情中的 scopes 只表示 ACL requested/configured allowlist：仅 Client ID 与 RT 均已配置时返回，不宣称是 Microsoft 远端实际 granted scopes，也不为此新增持久化或投影表。 |
| 2026-07-16 | V1.19 | Codex | 定稿资源验证、辅助邮箱恢复和显式别名边界：资源成功只由权威 RT 与收件决定；辅助邮箱恢复嵌入 validation/alias 两个任务，掩码直接写 `bindingAddress`，找回密码邮件仅按相同规范化掩码串行。完整契约见 [Microsoft 资源验证、辅助邮箱恢复与显式别名流程](20-microsoft-validation-binding-alias-flow.md)。 |
| 2026-07-16 | V1.20 | Codex | 补充登录授权验证码的恢复能力：登录 flow 与 password-recovery flow 都先按规则推算辅助邮箱；推算成功按完整邮箱精确收码，推算失败才按掩码和实际 recipient 反推，并共用相同掩码租约。 |
| 2026-07-16 | V1.21 | Codex | 定稿 Core Redis-only 验证调度：`pending` 是待分配，`validating` 是已有临时 Redis task；Microsoft/Domain 批量 cursor、单资源执行和 retry 不落 MySQL，完成立即清理。 |
| 2026-07-17 | V1.22 | Codex | 将 Microsoft 验证收件探测与旧项目全量识别解耦：验证每文件夹最多读取一封，成功提交后由独立 MailMatch Asynq 任务全量流式扫描，并复用别名、订单和 Allocation 事实补齐历史使用。 |

> 适用范围：Microsoft 邮箱导入、上传验证、RT 续期、Graph 邮件拉取、辅助邮箱绑定，以及管理员触发的 RT 刷新、资源级 Fetch 和显式别名 schedule 加速后的远端执行。
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
| 资源状态 | BC-CORE | `pending/validating/normal/abnormal/disabled/deleted`；ACL 不拥有状态，`pending` 是待验证，`validating` 是已分配任务。 |
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
| `AliasClient` | 作为既有 MicrosoftAliasCreator 的协议 adapter 执行显式别名远端创建，并返回供应用服务对账的结果；不拥有 schedule、quota、attempt 或 fencing。 |
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
| `proxy` | BC-PROXY 返回的代理配置；Microsoft 资源验证链路必须请求 IPv4。 |
| `requestId` | 任务追踪 ID。 |

输出：

| 字段 | 说明 |
|------|------|
| `email` | 邮箱 |
| `clientId` | Microsoft clientId |
| `refreshToken` | Microsoft RT |
| `bindingAddress` | 辅助邮箱地址权威事实，可空、可为 Microsoft 掩码、可为完整邮箱 |
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
| `scopes` | ACL 请求使用的 OAuth scope allowlist；不是远端 granted scopes 事实。 |
| `purpose` | `validation_history/alias_detect/order_fetch/aftersale_check/manual_fetch` |
| `since` | 拉取起点；全量拉取场景可为空 |
| `inboxLimit/junkLimit` | 文件夹限制 |
| `proxy` | BC-PROXY 返回的代理配置 |
| `requestId` | 任务追踪 ID |

成功返回结构化 `messages[]`，Go 将其转换为 BC-MAILMATCH 的 `Message`。

### 3.4 P1-I3 `ValidateResource`

用途：验证 Microsoft 资源本体是否可用。该流程在 Core 的异步 `ResourceValidation` worker 中调用，HTTP 请求不得同步等待 Microsoft 网络调用。

验证与历史识别分为三个阶段，其中第三阶段是独立异步任务：

| 步骤 | 当前要求 | 是否影响资源健康 |
|------|----------|------------------|
| 1. RT 获取/刷新 | 有 `clientId + refreshToken` 时先用 RT 换 Graph AT；没有可用 RT 时沿用 `rt` 页面流获取 RT。辅助邮箱只在页面授权需要时成为必要步骤，已有 RT 路径中的观察/恢复属于 best-effort。 | 是。最终未获得权威可用 RT 时按错误分类返回；已经成功的 RT 与收件结果不能被后续辅助邮箱失败覆盖。 |
| 2. 轻量收件探测 | 优先用 RT 换 Graph AT，对 `Inbox` 和 `JunkEmail` 各读取至多一封；Graph 不可用时用同一 RT 换 IMAP token 回退 Outlook IMAP，并完成两个文件夹探测。 | 是。Graph 或 IMAP 任一路径读取接口返回正常，即资源验证可成功。 |
| 3. 全量历史识别任务 | Core 在健康结果最终提交前先幂等投递 MailMatch 任务；投递失败保持 `validating` 由现有验证任务重试，worker 只在资源已经 `normal` 后重新读取已提交凭据。任务流式全量扫描 Inbox/Junk，按地址规则识别具体 main/dot/plus/explicit alias，再把识别结果交给 BC-TRADE。BC-TRADE 复用 BC-BILLING 与 BC-ALLOC 现有 Port，先创建或复用别名，再补一笔超级管理员 0 元已过保历史订单。 | 否。该独立任务不参与资源本体是否正常的判断。 |

第三阶段与验证 worker 解耦。历史订单号由 BC-ALLOC 按 `resourceId + projectId + mailboxType + mailboxId` 确定性生成；BC-TRADE 使用既有 Billing 零元扣款、Allocation 和 Order repository 在同一事务内编排，重复验证不会重复创建订单、0 元钱包流水、Allocation 或事件。Allocation 创建后立即为 `released`，订单固定为 `purchase/completed` 且 `afterSaleUntil` 已过期，因此不会参与收件、退款、结算或占用活动库存。BC-ALLOC 按具体主邮箱或别名 ID 查询所有历史 Allocation，只阻止同一邮箱实体再次进入同一项目，不误伤同一主资源下的其他别名；BC-MAILMATCH 仅保存识别和旧兼容事实，不跨域写上述三类事实。

收件策略：

| 项 | 规则 |
|----|------|
| 主路径 | Graph REST API。使用验证 HTTP client/session 发请求，复用 TLS 指纹、代理、超时和安全日志策略。 |
| 回退 | Graph token 或 Graph 拉取失败后，使用 RT 换 IMAP accessToken，再连接 Outlook IMAP 读取 `INBOX` 和垃圾箱。 |
| 文件夹 | 验证阶段必须包含收件箱和垃圾箱；Graph 文件夹使用 `inbox`、`junkemail`。 |
| 分页 | Graph 读取必须跟随 `@odata.nextLink`，直到文件夹读完。 |
| 结果 | 只返回结构化摘要给验证编排；正文、Token、验证码不得进入 Core、验证任务响应或普通日志。管理员授权邮件查询由 MailMatch/MailTransport 的事实查询接口负责，不从本验证结果读取。 |
| 成功 | 第二步 Graph 或 IMAP 任一路径接口正常返回，即本次资源验证可以成功。Graph 路径成功时返回 `graphAvailable=true`；Graph 失败但 IMAP 回退成功时返回 `graphAvailable=false`。 |

### 3.5 管理员异步任务入口

管理员按钮不增加第二套 Microsoft 实现，只在各事实所有者创建或复用 durable task，再由 worker 调用本节已有 ACL 方法：

| 管理员能力 | 任务与事实所有者 | ACL 复用 | 边界 |
|------------|------------------|----------|------|
| 刷新 RT | MailTransport 持有协议执行 task/receipt；Core 仍独占凭据 revision、root version 与安全诊断事实。 | worker 调用 `RefreshToken`，继续使用同一 `AuthClient`、OAuth token endpoint、IPv4 代理路线和 rotated RT 规则。 | worker 通过 tx-bound `MicrosoftCredentialPort` 读取内部 scope 并提交成功/失败结果；MailTransport repository 不直接查询或更新 Core 表，HTTP/queue/log 不返回 RT/AT。 |
| 拉取邮件 | MailMatch 创建/复用资源级 manual fetch task，并拥有 Message 与 single-flight 事实。 | worker 通过 MailTransport Port 调用 `FetchMessages(purpose=manual_fetch)`，结构化结果交回 MailMatch 幂等落库。 | ACL 不保存邮件业务事实；rotated RT 通过同一 Core `MicrosoftCredentialPort` 在任务完成短事务中保存，MailMatch repository 不直写 Core 表；正文由受控单封接口提供。 |
| 创建显式别名 | MailTransport 的 alias schedule/attempt 是事实所有者。管理员只走带 receipt/OperationLog 的 `AliasExpediteCommand`，把既有 schedule 提前或复用 active attempt。 | 真正远端执行继续调用既有 `MicrosoftAliasCreator/AliasClient` 和统一 HTTPClient。 | 不保留无审计 schedule 写旁路，不直接创建 alias，不绕过周/年 quota、execution admission、候选预占、reconciliation 或 fencing；`forSale` 切换仍不影响正常资源的 schedule。 |

三个 HTTP 入口都只做鉴权、状态校验、durable fact 与 OperationLog 持久化，成功提交返回 `202 Accepted`。同一资源同一任务类型已有 active task 时返回其 taskId，不重复发起外部调用；Redis/Asynq 暂时不可用时由 durable dispatcher 恢复。业务确定性失败写任务安全结果，临时网络/代理/429/5xx 按既有分类重试并写 SystemLog。

---

## 4. 辅助邮箱绑定

辅助邮箱绑定通过 Go 进程内调用完成。Go ACL 直接调用 MailTransport 应用服务或 Port 完成以下动作：

| 动作 | 语义 |
|------|------|
| `AllocateBinding` | 为 Microsoft 授权选择或复用辅助邮箱，开启一次绑定尝试。 |
| `ResolveBinding` | Microsoft 页面提示已绑定时先按系统规则和资源同前缀规则推算；当前流程仍需验证码时，推算成功后按完整邮箱精确收码，推算失败时使用登录授权或忘记密码验证码邮件的实际 recipient 反推。 |
| `MarkBindingStatus` | 回写 `code_sent/verified/timeout/failed/expired`。 |
| `WaitBindingCode` | 等待辅助邮箱验证码邮件。 |

验证码、密码、RT、accessToken 不得出现在 ACL/绑定执行任务响应、普通日志、SystemLog 或 OperationLog 中。管理员 UI 已确认的 Orders/邮件摘要和授权单封邮件查询可由事实所有者返回验证码，授权单封详情可返回正文；这类查询不从 ACL 任务响应取值，也不改变本节的协议禁敏边界。

P1-I3 适配规则：

| 对接点 | 当前系统策略 |
|--------|--------------|
| 导入输入 | Core 解析 TXT 中的 `bindingAddress` 后，只通过 Port 交给 MailTransport 记录 `MicrosoftBindingMailbox(pending)`；不写入 `microsoft_resources`，不进入 Core 资源状态机。 |
| 页面流使用 | `AcquireToken` 页面流遇到 AddProof/Identity/OTP 时，优先使用 `MicrosoftBindingMailbox.bindingAddress`；没有输入记录时先读取 Microsoft Email proof，有 proof 就保存完整地址或掩码，只有确认没有 proof 且需要新增绑定时才使用原 `rt` 确定性生成规则。 |
| 收码来源 | `WaitBindingCode` 的轮询、去重和验证码提取逻辑沿用 `rt`；底层 `MailboxReader` 在本项目中读取 `inbound_mails + MinIO private RFC822`，不再依赖旧项目 API。 |
| 入站接收 | SMTP RCPT 阶段只使用完整 `microsoft_binding_mailboxes.binding_address` 解析 Microsoft 资源和 owner；掩码不能用于精确收件，必须先推算或恢复。 |
| 状态回写 | 辅助邮箱确认成功回写 `verified`；验证码超时回写 `timeout`；确定性绑定执行失败回写 `failed`；这些状态只用于 MailTransport 排障，不代表资源验证成功或失败，也不改变 Core 资源状态枚举。 |

旧 `rt` 通过输出文件和进程内 `knownAuxiliary` 记录解决“掩码辅助邮箱 -> 真实辅助邮箱”的反查。本项目不得恢复文件侧通道，Microsoft proof 的掩码必须直接写入 `bindingAddress`；只有确认账号没有 proof、即将执行 AddProof 时，才在 Microsoft 发码前把确定性生成的完整系统地址写成 `MicrosoftBindingMailbox(pending)`。入口处理不得清空已有完整事实。AddProof 页面在当前系统中属于绑定流程，不应先尝试跳过后再处理验证码；否则 Microsoft 可能已经发码，而系统还没有进入绑定收码提交路径。

登录授权 RT 的 Email proof/OTP 页面也是辅助邮箱恢复入口。ACL 在触发登录验证码前必须先保存掩码并运行确定性规则：如果得到唯一完整候选，就先申请该掩码租约、快照精确邮箱并直接匹配新验证码；如果无法推算，才在同一租约下快照系统域名/掩码范围，通过登录邮件的实际 SMTP recipient 反推完整地址。password-recovery 只是另一种发码/确认渠道，不能拥有不同的租约、匹配或持久化规则。

`rt` 的 KMSI/Consent/Identity/OTP/Token polling 主流程、错误分类、验证码提取规则和代理尝试预算不得因为接入当前系统而重写。允许变化的只有输入来源、AddProof 绑定优先级、收码读取实现、状态持久化和安全诊断输出。

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
| `password` | 密码错误 | `422` | `Microsoft account password is incorrect.` |
| `unknown_mailbox` | 账号不存在或辅助邮箱恢复路径不可用 | `422` | `Microsoft account does not exist or recovery mailbox is not supported.` |
| `mfa` | 需要 Authenticator 验证 | `422` | `Microsoft account requires authenticator verification.` |
| `passkey` | 需要 Passkey | `422` | `Microsoft account requires passkey verification.` |
| `phone` | 需要手机验证 | `422` | `Microsoft account requires phone verification.` |
| `locked` | 账号锁定 | `422` | `Microsoft account is locked.` |
| `account_abnormal` | 账号受限或需要恢复 | `422` | `Microsoft account is restricted or requires recovery.` |
| `already_bound` | 辅助邮箱已被其他恢复信息绑定 | `409` | `Microsoft account is already bound to another recovery mailbox.` |
| `code_timeout` | 辅助邮箱验证码超时 | `422` | `Auxiliary mailbox verification code was not received in time.` |
| `code_error` | 辅助邮箱验证码错误或过期 | `422` | `Auxiliary mailbox verification code is incorrect or expired.` |
| `oauth_invalid_grant` | RT 无效或过期 | `422` | `Microsoft refresh token is invalid or expired.` |
| `oauth_client` | OAuth client 无效或不允许 | `422` | `Microsoft OAuth client is invalid or not allowed.` |
| `oauth_permission` | OAuth 权限不可用 | `422` | `Microsoft OAuth permission is not available.` |
| `missing_token` | 收件所需 token 不完整 | `422` | `Microsoft mail fetch credentials are incomplete.` |
| `graph_unauthorized` | Graph access token 未授权或过期 | `422` | `Microsoft Graph access token is unauthorized or expired.` |
| `graph_forbidden` | Graph 邮箱权限不可用 | `422` | `Microsoft Graph mailbox permission is not available.` |
| `imap_auth_failed` | IMAP XOAUTH2 认证失败 | `422` | `Microsoft IMAP authentication failed.` |
| `unknown` | 未识别的 Microsoft 页面或授权状态 | `422` | `Microsoft authorization failed with unknown status.` |
| `auth_timeout` | 授权轮询超时 | `503` | `Microsoft authorization timed out.` |
| `request` | 代理、网络、上游 429/5xx 或协议请求临时失败 | `502/503` | `Microsoft mail service is temporarily unavailable.` |

登录和公开身份接口仍必须合并账号不存在、密码错误，避免账号枚举。资源验证诊断只展示给资源 owner 或管理员，可以保留上述安全分类文案以便排障，但不得写入密码、RT、accessToken、验证码、邮箱正文、Microsoft 原始响应或页面片段。`request/auth_timeout` 是临时失败，必须由 Core `ResourceValidation` 任务重试处理，耗尽后只失败任务、不把资源置为 `abnormal`。`code_timeout/code_error` 可以作为本次辅助邮箱执行结果，但已有权威 RT 且收件成功时不得使资源验证失败；只有它实际阻止密码授权、导致最终没有获得可收件 RT 时，验证才因“无可用 RT”失败。

代理错误必须上报 BC-PROXY。资源代理失败时，允许本次业务按代理池规则获取系统代理重试；同一业务链路最多尝试 3 次代理路线。达到尝试预算或资源/系统代理都不可用时，ProxyPort 返回 `direct=true` 的系统直连路线，HTTPClient 必须禁用代理继续执行；直连失败属于上游或本机网络失败，不得上报为代理失败计数，内部详情写 SystemLog。

IP 版本规则：

| 场景 | 策略 |
|------|------|
| 获取 RT/刷新 AT | 强制 `ipv4`。 |
| Graph 邮件拉取 | 强制 `ipv4`，沿用同一次验证链路的代理路线。 |
| 辅助邮箱绑定 | 强制 `ipv4`。 |

管理员 RT 刷新、资源 Fetch 和 alias expedite 后的远端执行不得新增错误枚举或直连旁路。它们必须经过同一个 `ErrorClassifier`、`HTTPClient` 和 BC-PROXY 路线预算：

| 场景 | 对外与内部处理 |
|------|----------------|
| 任务提交 | 只返回 `202` 和安全任务视图，不把尚未发生的 Microsoft 结果伪装为同步成功。 |
| 确定性 Microsoft 失败 | 复用本节稳定分类，任务进入失败终态；TaskView 只展示安全 message。 |
| 临时网络、代理、429/5xx | 复用 `request/auth_timeout`、三次代理路线与直连兜底策略，由 worker 受控重试。 |
| 未知上游响应 | 对外使用安全 502/503 或任务安全诊断；原始页面、响应头/体和凭据不得进入响应、OperationLog 或未禁敏 SystemLog。 |

管理员权限、CSRF、资源存在性和状态冲突在调用 ACL 前由所属 API/Application Service 处理；ACL 不感知管理员角色，也不把 `404/409` 业务判断混入协议错误分类。

---

## 7. 状态边界

Microsoft 页面状态不是 Go 领域状态。

Go 资源状态只保留：

```text
pending
validating
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
| 批量提交验证 | HTTP 只创建 Redis cursor task；cursor 分页把 Microsoft/Domain 资源置 `pending`，当前页完成即删除并只保留下一页。不得同步扫描、COUNT 或批量更新大表。 |
| 分配验证执行 | dispatcher 只领取 `pending`，同一短事务置 `validating` 后投递单资源 Redis task。总 `validating` 数不得超过自适应执行窗口，防止百万资源形成无界 Redis backlog。 |
| 执行 Microsoft ACL | 事务外执行，带 requestId 和超时。 |
| 成功保存凭据 | 短事务保存 clientId/RT、更新资源状态、写事件。 |
| 验证后历史识别 | 健康结果最终提交前先幂等投递独立 `validated_microsoft_history_scan`，投递失败沿用 validation retry；任务只在资源已为 `normal` 后使用 `background_project_history` 队列全量流式扫描。rotated RT 与规则快照按 credential revision/rule snapshot fencing 校验，识别结果经 BC-TRADE 调用现有 BC-BILLING/BC-ALLOC Port，在同一短事务内提交别名、0 元历史订单和 released Allocation。 |
| 失败记录诊断 | 短事务写安全诊断和 SystemLog。 |
| rotated RT | 拉取或刷新成功后必须在短事务内保存新 RT。 |
| 管理员 RT 刷新/资源 Fetch | HTTP 只创建或复用 durable task；worker 在事务外调用 ACL，随后以 tx-bound `MicrosoftCredentialPort` 让 Core 保存 RT/revision/诊断，并与各自任务完成事实保持同一短事务。 |
| 管理员 alias expedite | HTTP 只通过带 receipt/audit 的唯一命令入口提前既有 schedule 或复用 active attempt；远端创建仍由 alias worker 在事务外执行并按 fencing 完成/对账。 |

已有 `clientId + refreshToken` 的资源验证优先走 OAuth token endpoint 刷新 AT；成功且上游返回新 RT 时保存 rotated RT，没有新 RT 则保留旧 RT。只有 `email + password` 的资源验证走 `AcquireToken` 页面流。页面流实现必须封装在 MailTransport ACL 内部，Core 只能感知稳定分类和安全文案。

Microsoft ACL 的 `request` 分类只表示代理、网络、超时、上游限流或服务不可用等临时请求失败。Core 收到该分类后只让同一 Asynq task 在最大重试次数内重试，期间资源保持 `validating`；最终仍无健康结论时先恢复 `pending`，再让 task 成功消费并从 Redis 删除，不能写成 `abnormal`，不能进入 archived，也不能留下没有 active task 的 `validating`。密码错误、MFA、账号锁定和账号异常只有在导致无法获得权威可用 RT 时才是资源失败证据；验证码错误或超时只描述辅助邮箱执行结果，不能推翻已经完成的 RT refresh 和收件成功。

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
| 管理员 RT 刷新 | HTTP 返回 202、active task 去重、rotated/no-rotated RT、确定性失败和 dispatcher 恢复均有测试。 |
| 管理员资源 Fetch | 资源级 single-flight、重复投递幂等、Graph/IMAP 失败、Message 去重及摘要/正文禁敏边界有测试。 |
| 管理员 alias expedite | 只提前 schedule 或复用 attempt；周/年额度、admission、预占、对账、旧 fencing token 和 `forSale` 解耦测试继续通过。 |
| 统一路径 | 三类管理员任务断言复用相同 ProxyPort、HTTPClient、ErrorClassifier 和安全 SystemLog，不存在同步外部调用或绕过代理的 adapter。 |

---

## 10. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-MSACL-1 | Microsoft 复杂流程由 Go ACL 承接 | 新项目只有一个 Go 后端，减少双运行时和部署复杂度。 |
| ADR-MSACL-2 | Microsoft 交互逻辑用 Go fixture 驱动实现 | 保留已验证业务认知，同时把实现收敛到 Go 模块。 |
| ADR-MSACL-3 | 页面状态不进入领域状态 | 避免 Microsoft 临时页面语义污染核心模型。 |
| ADR-MSACL-4 | Microsoft ACL 只做进程内模块 | 它是协议适配器，不是业务 API 或独立服务。 |
| ADR-MSACL-5 | 外部网络调用不进数据库事务 | 避免长事务、锁等待和不可控回滚。 |
| ADR-MSACL-6 | 管理员 RT、Fetch、alias 加速复用既有 ACL 与 durable worker | 管理入口只改变任务调度，不应产生第二套协议、代理、错误或配额实现。 |
