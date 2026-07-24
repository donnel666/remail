# BC-MAILTRANSPORT 邮件传输上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-06-30 | V1.1 | Codex | 补充 SMTP 外发由 BC-MAILTRANSPORT DeliveryPort 承接；IAM 不直接持有 SMTP 协议适配器。 |
| 2026-07-02 | V1.2 | Codex | 补充 P1-I2 辅助邮箱英文统一为 `binding`，中文仍称辅助邮箱；不改变 MailTransport 与 Core 边界。 |
| 2026-07-03 | V1.3 | Codex | 补充默认 SMTP direct 出站和 SMTP 入站的异步任务策略：外发请求只持久化幂等记录并投递 Asynq，worker 解析 MX 直连发信；入站 SMTP 只校验收件资源、落 MinIO 私有原文和入站任务事实，后续处理由 Asynq 承接。此为缺失设计补充，不改变 MailMatch 对邮件归属/项目匹配的拥有权。 |
| 2026-07-04 | V1.4 | Codex | 补充 MailTransport 任务持久化与恢复策略：`OutboundMail/InboundMail` 以 MySQL 为最终事实，Redis/Asynq 仅作执行层；dispatcher 定期恢复 pending/stale 任务，协议失败写 SystemLog。此为缺失设计补充，不改变既有上下文边界。 |
| 2026-07-04 | V1.5 | Codex | 补充外发邮件 DKIM 签名策略：MailTransport infra 在 SMTP DATA 前对最终 RFC822 原文签名，私钥只来自部署 Secret 或本地文件，不进入业务事实和日志。此为缺失设计补充，不改变 IAM/通知业务对 DeliveryPort 的依赖方向。 |
| 2026-07-04 | V1.6 | Codex | 补充 BIMI 品牌 Logo 发布策略：前端 public 固定发布 BIMI 专用 SVG，DNS 仅引用静态 SVG；BIMI 不参与邮件投递和业务判定，只用于支持邮箱客户端品牌展示。此为缺失设计补充，不改变 MailTransport 认证边界。 |
| 2026-07-04 | V1.7 | Codex | 补充 direct SMTP 外发协议策略：默认直连外发使用 Go 标准库 SMTP 会话并强制 IPv4 连接对方 MX，避免运行时默认双栈拨号和第三方客户端实现差异导致投递不稳定。此为缺失设计补充，不改变异步发送、DKIM 或入站策略。 |
| 2026-07-04 | V1.8 | Codex | 补充 P1-I3 资源验证 Port 执行策略：Microsoft/DNS 验证只能由异步 worker 调用，MailTransport 只返回结构化分类和安全文案，不直接拥有资源状态。此为缺失设计补充，不改变 Core 对资源状态的所有权。 |
| 2026-07-04 | V1.9 | Codex | 补充 P1-I3 资源验证临时失败分类：MailTransport 返回 `request` 表示基础设施或上游临时不可用，由 Core 任务重试处理，不直接判定资源异常。此为缺失设计补充，不改变 Core 对资源状态的所有权。 |
| 2026-07-04 | V1.10 | Codex | 补充 P1-I3 Microsoft 辅助邮箱绑定事实落库：导入输入和验证流程使用 `Binding` 状态机，验证码通过本机入站邮件读取。此为缺失设计补充，不改变 Microsoft ACL 交互流程。 |
| 2026-07-04 | V1.11 | Codex | 补充 P1-I3 Microsoft Graph 协议能力返回：验证 Port 在收件成功后返回 Graph 是否可用，Core 仅保存能力事实并用于筛选。此为缺失设计补充，不改变 MailTransport/Core 边界。 |
| 2026-07-04 | V1.12 | Codex | 纠正 P1-I3 入站邮件归属：`InboundMail` 归属到 Core 资源根 `resourceType/resourceId/ownerUserId`，同时支持 Domain 收件和 Microsoft 辅助邮箱收码。此为缺失设计补充，不改变 MailTransport 只保存邮件事实的边界。 |
| 2026-07-04 | V1.13 | Codex | 补充 P1-I3 本机 SMTP 入站可靠性策略：DATA 阶段按配置流式限量读取并记录安全耗时日志；Microsoft 收码读取允许消费已落原文但尚未完成异步 `stored` 标记的入站邮件，并保留晚到宽限窗口。此为缺失设计补充，不改变 MailMatch 边界。 |
| 2026-07-05 | V1.14 | Codex | 补充 P1-I3 Microsoft 验证临时失败分类边界：`request/auth_timeout` 均由 Core 任务重试处理，不作为资源本体异常证据。此为缺失诊断补充，不改变 Core 对资源状态的所有权。 |
| 2026-07-09 | V1.15 | Codex | 补充 P1-WP3 入站消费策略：自建域名入站 worker 读取 MinIO 原文后调用 MailMatch consumer 完成结构化落库和订单匹配，`stored` 表示已完成可读校验与消费尝试。此为缺失实现补充，不改变 MailTransport 不拥有项目匹配规则的边界。 |
| 2026-07-12 | V1.16 | Codex | 补充管理员 Microsoft 运维边界：新增 BindingAdmin/BindingQuery/AuxiliaryMail Query Port，并把“创建显式别名”定义为受配额、admission、fencing 和 reconciliation 约束的 schedule expedite；管理页面可见但不能直接推进协议状态。 |
| 2026-07-12 | V1.17 | Codex | 收敛管理员 RT 刷新与 alias 入口：协议任务继续归 MailTransport，但凭据 scope、revision、Core version 和诊断统一经 tx-bound `MicrosoftCredentialPort` 回到 Core；alias expedite 只保留带 receipt/audit 的唯一命令入口。 |
| 2026-07-12 | V1.18 | Codex | 明确 BindingAdmin 同值更新为 no-op：地址未变化时保留 verified/时间/诊断；owner 或主邮箱同步不触发新的绑定验证。 |
| 2026-07-16 | V1.19 | Codex | 定稿验证、辅助邮箱恢复与显式别名流程：`binding_address` 直接保存空值/掩码/完整地址，masked/system/ready 全部实时派生；validation 与 alias 共用相同掩码租约，只有相同规范化掩码串行。完整契约见 [Microsoft 资源验证、辅助邮箱恢复与显式别名流程](20-microsoft-validation-binding-alias-flow.md)。 |
| 2026-07-16 | V1.20 | Codex | 补充登录授权发码的辅助邮箱识别：所有 masked proof 先走规则推算；可推算时按完整邮箱精确匹配登录验证码，不可推算时才通过实际 recipient 反推；登录和忘记密码发码共用掩码租约。 |
| 2026-07-17 | V1.21 | Codex | 收敛 Microsoft 验证职责：MailTransport 只对 Inbox/Junk 做每文件夹一封的可读性探测；旧项目全量扫描改由验证成功后独立 MailMatch 异步任务执行。 |

> 支撑域。BC-MAILTRANSPORT 封装协议细节，只提供结构化结果，不做项目匹配和订单判断。

---

## 1. 定位

| 拥有 | 不拥有 |
|------|--------|
| SMTP/IMAP/Graph/Microsoft ACL、外发邮件状态、辅助邮箱绑定、SMTP 入站配置、Microsoft 远端别名 schedule/attempt/reconciliation | 项目邮件规则、主邮箱邮件归属、订单服务状态、资源可分配状态、Core 显式别名库存事实、代理池选择规则 |

`MailServer` 和自建邮箱域名可用性归 BC-CORE；本上下文只使用连接和协议能力。

Microsoft 通讯需要代理时，BC-MAILTRANSPORT 通过 BC-PROXY 的 `ProxyPort` 获取本次代理；资源代理异常时按代理池规则降级到系统代理。MailTransport 不直接维护代理绑定、错误次数和轮转策略。

管理员 Microsoft 页面通过 MailTransport 管理查询和 Core 命令编排调用本上下文的 Query/Command Port。页面能展示辅助邮箱地址、绑定状态、收码邮件摘要和别名 schedule 安全摘要，并能提交绑定输入修改和别名 expedite；一期不自动暴露 mock-only 的 attempt 字段/列表。这些能力不把 Binding 或 Microsoft 页面流迁入 Core，也不允许管理 API 直接写状态列。

---

## 2. 实体

### 2.1 `OutboundMail`

| 字段 | 含义 |
|------|------|
| `id` | 外发邮件 ID |
| `idempotencyKey` | 幂等键 |
| `requestHash` | `purpose/sender/recipient/subject/body` 的请求指纹；同 key 不同指纹必须拒绝。 |
| `purpose` | `verification_code/system_notification/security_notice` |
| `recipient/sender` | 收发件人 |
| `subject/body` | 内容 |
| `status` | `pending/sending/sent/failed` |
| `retries` | 重试次数 |
| `failureReason` | 安全失败原因 |
| `sentAt` | 发送时间 |

状态机：

```mermaid
stateDiagram-v2
    [*] --> pending
    pending --> sending: worker claim
    sending --> sent: 成功
    sending --> pending: 可重试失败
    sending --> failed: 不可重试/超过上限
```

### 2.2 `Binding`

| 字段 | 含义 |
|------|------|
| `id` | 绑定 ID |
| `resourceId` | Microsoft 资源 ID |
| `bindingAddress` | 地址权威事实；允许为空、Microsoft 掩码或完整邮箱，分类由格式和当前系统 binding 域名实时派生 |
| `microsoftEmail` | 待验证 Microsoft 邮箱 |
| `status` | `pending/code_sent/verified/timeout/failed/expired` |
| `purpose` | 用途 |
| `codeMsgId` | 验证码邮件 ID |
| `category/message` | 内部安全诊断，不作为公开 API 响应码 |
| `selectedAt/expireAt/verifiedAt` | 时间 |

状态机：

```mermaid
stateDiagram-v2
    [*] --> pending
    pending --> code_sent
    code_sent --> verified
    code_sent --> timeout
    code_sent --> failed
    pending --> expired
```

`verified/timeout/failed/expired` 是终态。Microsoft 授权流程发起新尝试时可以复用唯一绑定记录并重置为 `pending`，这是新尝试，不是终态回退。

### 2.3 `InboundSetting`

| 字段 | 含义 |
|------|------|
| `receiveEnabled` | 入站开关 |
| `maxSizeBytes` | 单封最大大小 |
| `blackSenders/blackSubjects` | 简单拒收规则 |
| `retentionDays/retentionCount` | 自建邮件保留策略 |
| `timeoutMs` | 连接超时 |

### 2.4 `InboundMail`

| 字段 | 含义 |
|------|------|
| `id` | 入站邮件任务 ID |
| `envelopeFrom` | SMTP MAIL FROM，安全存储信封地址 |
| `recipient` | 单个 SMTP RCPT TO；多收件人拆成多条任务事实，共用同一原文 objectKey |
| `resourceType/resourceId/ownerUserId` | RCPT 阶段解析出的资源根和 owner；`resourceType=domain` 表示自建域名或生成邮箱，`resourceType=microsoft` 表示 Microsoft 辅助邮箱收码 |
| `sourceObjectKey` | MinIO private bucket 中的 RFC822 原文 |
| `status` | `pending/processing/stored/failed` |
| `failureReason` | 安全失败摘要 |

状态机：

```mermaid
stateDiagram-v2
    [*] --> pending
    pending --> processing: worker claim
    processing --> stored: 原文可读且等待 MailMatch 消费
    processing --> failed: 原文不可读/任务不可恢复
```

补充约束：SMTP `DATA` 仍只负责保存原文和创建 `InboundMail(pending)`，不得同步等待项目匹配。自建域名入站 worker 在 `processing` 阶段读取 MinIO 原文并调用 MailMatch consumer；MailTransport 只负责传递原文和任务状态，不拥有项目规则、订单归属或验证码交付语义。`stored` 表示原文可读且消费尝试完成；消费失败按任务 retry/final 进入 `pending/failed`。

### 2.5 Microsoft 显式别名调度事实

Microsoft 远端显式别名创建的既有 schedule、attempt、配额预占、fencing 和 uncertain reconciliation 归 MailTransport，因为它们描述协议执行而非可分配库存。确认成功后的 `ExplicitAlias` 仍是 BC-CORE 的资源能力事实；MailTransport 通过 Core `ExplicitAliasRecordPort` 在成功短事务内登记或收敛该事实，不能让 schedule/attempt DTO 直接充当库存。本期沿用现有内部事实，不因 mock 草稿新增 attempt 字段；HTTP 只返回已确认 UI 需要的周/年已创建数、周/年限额和下次运行时间，未展示的 schedule 状态、安全错误和 attempt 字段不自动进入一期契约。

后台 dispatcher 始终为所有 `status=normal` 的 Microsoft 资源自动补货，不受 `forSale` 影响。管理员 expedite 只把该资源已有 schedule 提前到可调度时间并唤醒 dispatcher；schedule 缺失时由正常 dispatcher 的既有建档流程补齐，管理命令不创建第二套 schedule。已有 active/uncertain attempt 时复用原任务，达到自然周/年配额时返回冲突，绝不直接在 HTTP 请求中调用 Microsoft。

---

## 3. ACL 能力

| 能力 | 所属业务语义 |
|------|--------------|
| `acquireToken` | BC-CORE Microsoft 上传验证。 |
| `refreshToken` | BC-CORE 已有 RT 验证和 RT 续期。 |
| `fetch` | BC-MAILMATCH 拉取邮件事实。 |
| SMTP 入站 | 自建域名收件后交给 MailMatch。 |
| SMTP 外发 | IAM 验证码、通知邮件、安全提醒。 |
| IMAP 拉取 | 普通邮箱或自建邮箱拉取。 |
| DNS 检查 | 自建邮箱域名和邮箱服务器诊断。 |

Microsoft Go ACL 策略详见 `14-microsoft-go-acl-strategy.md`。

---

## 4. 邮件拉取与保留

Microsoft 拉取用途必须显式传入：

| 用途 | 时间范围 |
|------|----------|
| `validation_history` | 验证阶段历史邮件识别，全量。 |
| `alias_detect` | 验证阶段别名识别，全量。 |
| `order_fetch` | 默认最近一个月。 |
| `manual_fetch` | 默认最近一个月，管理员可在安全范围内指定。 |
| `aftersale_check` | 默认最近一个月。 |

自建邮件保留策略：

- 最近一个月邮件不删除。
- 总数不超过 100 封不删除。
- 总数超过 100 时，只删除一个月以前的邮件。

---

## 5. 不变式

| 编号 | 规则 |
|------|------|
| INV-MT1 | 协议失败必须写任务诊断或 SystemLog，不能只留容器日志。 |
| INV-MT2 | 普通日志和错误响应不得包含密码、RT、accessToken、验证码、邮件正文。 |
| INV-MT3 | SMTP 入站交给 MailMatch 时必须解析出 `emailResourceId`，解析失败应拒收或写失败诊断，不静默成功。 |
| INV-MT4 | 外发邮件必须幂等，验证码发送不得重复发送不可控。 |
| INV-MT5 | Microsoft ACL 网络配置、代理或页面解析失败时 fail closed，不能静默降级。 |
| INV-MT6 | 辅助邮箱绑定 Query/排障接口只读；只有独立 `BindingAdminPort` 可以设置或清除绑定输入，且不能人工写 `verified/code_sent/timeout/failed` 等协议结果。 |
| INV-MT7 | Microsoft 资源验证链路的获取 RT、刷新 AT、Graph 拉取和辅助邮箱绑定必须通过 BC-PROXY 请求 IPv4 代理；资源/系统代理都不可用时才按代理池规则直连兜底。 |
| INV-MT8 | 辅助邮箱摘要不得返回正文，但可以返回 UI 已确认的验证码安全展示；普通日志、任务响应、错误和导出不得返回正文或验证码。Microsoft 密码/RT/AT、RFC822 objectKey 或上游原文没有响应例外；辅助邮箱完整地址只对具备 binding read 权限的管理员可见，完成 resource/message 关联校验的授权单封详情可以返回 UI 已确认的解析正文和验证码。 |
| INV-MT9 | Alias expedite 不新增配额、不替换 uncertain candidate、不绕过资源状态/admission/fencing；HTTP 只持久化/唤醒任务并返回 `202`。 |
| INV-MT10 | Binding 输入与 Core 资源编辑需要原子保存时，MailTransport repository 必须使用调用方 tx-bound context；事务内不得执行 Microsoft、SMTP、MinIO 或其他网络调用。 |
| INV-MT11 | 登录授权或忘记密码准备向 masked proof 发码前，必须先执行规则推算并取得共享规范化掩码租约；推算成功按完整邮箱精确收码，只有推算失败才按掩码和实际 recipient 反推。 |

---

## 6. Port

| Port | 方向 | 职责 |
|------|------|------|
| `ValidationPort` | 入站自 BC-CORE | 上传验证、已有 RT 校验、RT 续期。 |
| `MailTransportFetchPort` | 入站自 BC-MAILMATCH | 按订单或资源管理用途拉取结构化邮件。 |
| `MicrosoftCredentialPort` | 出站到 BC-CORE | 管理员 RT worker 在短事务中锁定内部 credential scope，并把刷新成功/失败结果交给 Core 保存；MailTransport repository 不直接读写 Core 表，秘密不进入 HTTP、queue、TaskView 或日志。 |
| `DeliveryPort` | 入站自 BC-GOVERNANCE/BC-IAM | 外发邮件。 |
| `InboundPort` | 出站到 BC-MAILMATCH | SMTP 入站邮件落库。 |
| `BindingCodeWaitPort` | 出站到 BC-MAILMATCH | Microsoft ACL 等待辅助邮箱验证码。 |
| `ProxyPort` | 出站到 BC-PROXY | 获取 Microsoft 通讯代理并上报代理成功/失败。 |
| `BindingQueryPort` | 入站自 Core 概要/ MailTransport 管理 API | 按 resourceId 或管理筛选读取唯一权威的 bindingAddress、执行状态、时间和安全诊断；不返回验证码或原文。 |
| `BindingAdminPort` | 入站自 Core 管理 Facade | 在短事务内设置/替换/清除 bindingAddress 输入；只对完整 active 地址维护精确唯一性，掩码不占用 concrete 唯一槽位；owner 转移时同步收敛当前 Binding owner，但不改写历史 InboundMail owner；不允许人工推进协议结果状态。 |
| `AuxiliaryMailQueryPort` | 入站自 MailTransport 管理 API | 只在 resourceId 对应 Binding 作用域内分页读取辅助邮箱收码邮件摘要，并按 messageId 授权读取正文/验证码；永不返回 objectKey。 |
| `AliasQueryPort` | 入站自 Core aliases 管理查询 | 读取显式别名 schedule 安全摘要，供 Core 与自身 alias inventory 组合；一期不发布内部 attempt DTO。 |
| `AliasExpediteCommand` | 管理 HTTP 入口 | 先创建/复用幂等 receipt 并写 OperationLog，再提前 schedule 和唤醒 dispatcher；这是唯一写入口，内部复用 active attempt，保持配额、admission、fencing 和 reconciliation。 |
| `ExplicitAliasRecordPort` | 出站到 BC-CORE | 远端确认成功后登记/收敛 Core 显式别名库存事实。 |

P1-I3 补充：

| Port | 方向 | 职责 |
|------|------|------|
| `ResourceValidationPort` | 入站自 BC-CORE worker | 验证 Microsoft RT/获取 RT、自建域名 MX；只返回结构化结果，不直接修改资源状态。 |

`ResourceValidationPort` 返回的 `request/auth_timeout` 分类只表示协议请求、代理、DNS、授权轮询超时或上游临时不可用。该分类由 Core 的 `ResourceValidation` 任务重试处理，不能作为资源账号或域名本体异常的证据。Microsoft 资源只有在无法获得权威可用 RT，或最终 RT 发生确定性收件失败时才允许 Core 写 `abnormal`；辅助邮箱掩码、外部域名、恢复超时、验证码错误和 Binding 持久化失败本身都不是资源异常证据。

P1-I3 Microsoft 验证流程使用同一个 `Binding` 实体承接辅助邮箱事实。Core 导入 TXT 时只把 `bindingAddress` 作为输入交给 MailTransport，MailTransport 写入 `pending` 绑定记录；完整 active 地址只能归属一个 Microsoft 资源，避免 SMTP RCPT 精确归属歧义，掩码不占用 concrete 地址唯一槽位。验证 worker 进入 Microsoft 页面流后复用该地址；没有输入时先读取 Microsoft Email proof，只有确认没有 proof 且页面流要求新增绑定时才生成系统地址。SMTP 入站只使用完整地址解析资源并写入 `InboundMail(resourceType=microsoft)`；掩码必须先推算或恢复为完整地址，`BindingCodeWaitPort` 再读取本机 RFC822 原文并沿用 ACL 的验证码提取规则。

管理员修改辅助邮箱不是验证状态覆盖。`BindingAdminPort.SetAddress` 先校验空值/掩码/完整地址格式，只对完整 active 地址校验唯一性，并检查当前是否存在运行中的绑定/验证尝试；发生冲突返回 `409`。规范化后的地址与当前值相同时必须 no-op，保留 `verified`、验证时间和安全诊断；只有实际替换或清除地址才开启新一轮输入并置为 `pending`。owner 或主邮箱变化只同步当前 Binding 的身份字段，不重置地址协议状态。替换成功保留旧 InboundMail/审计事实，清除地址也不删除历史收码邮件。若同一个管理保存请求还修改 Core email/owner/凭据，Core 先完成全部业务校验并开启短事务，再通过 tx-bound context 调用 `BindingAdminPort`，任一写失败整体回滚。成功与失败 OperationLog 只记录固定安全摘要。

没有导入指定 `bindingAddress` 时，worker 必须先检查 Microsoft 是否已经存在 Email proof：有 proof 就把完整地址或掩码直接写入 `bindingAddress` 并按规则分类；只有确认没有 proof、需要新绑定时，才按 ACL 确定性规则生成系统辅助邮箱并在 Microsoft 发码前写入 `MicrosoftBindingMailbox(pending)`，确保 SMTP `RCPT TO` 可以解析资源归属。完整地址、掩码和系统域名资格的判断不依赖 Binding 执行状态，详见 [Microsoft 资源验证、辅助邮箱恢复与显式别名流程](20-microsoft-validation-binding-alias-flow.md)。

P1-I3 Microsoft 资源验证的资源健康判断止于“RT 可用且收件接口可读”。MailTransport 必须先完成 RT 获取或刷新，再优先通过 Graph 对收件箱和垃圾箱各读取至多一封做可读性探测；Graph 失败时使用同一 RT 换 IMAP token 回退 Outlook IMAP，并同样完成两个文件夹的轻量探测。Graph 或 IMAP 任一路径读取正常，即 `ResourceValidationPort` 可返回成功。Port 同时返回 `graphAvailable`：Graph 路径成功为 `true`，IMAP 回退成功为 `false`。该字段是协议能力事实，不是资源状态。全量历史读取与项目识别不在 MailTransport 验证调用内执行；Core 在健康结果最终提交前先幂等创建 BC-MAILMATCH 独立异步任务，投递失败保持 `validating` 重试，任务仅在资源进入 `normal` 后读取已提交凭据。识别结果再由 BC-TRADE 通过既有 BC-BILLING/BC-ALLOC Port 落为订单和分配事实，MailTransport 与 MailMatch 都不得直接写这些表。

密码登录授权 RT 自身触发 Email proof/OTP 时，也必须调用共享辅助邮箱识别。worker 先用系统确定性规则和资源同前缀规则匹配掩码：得到唯一完整地址时，在发码前取得该掩码租约、快照精确邮箱并直接匹配本轮登录验证码；无法推算时，才快照系统域名/掩码范围，并从新邮件实际 recipient 反推完整地址。登录授权与忘记密码只是不同的 Microsoft 发码和确认接口，使用同一个租约、基线、邮件归属和持久化规则。

---

## 7. API 设计

管理端只读/排障接口：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/admin/bindings` | 按资源、Microsoft 邮箱、辅助邮箱、状态筛选；传 `resourceId` 时为辅助邮箱 Tab 返回当前 Binding 和邮件摘要分页。 |
| `GET` | `/v1/admin/bindings/{id}` | 单条详情。 |
| `GET` | `/v1/admin/bindings/messages/{messageId}?resourceId={resourceId}` | 按需读取一封已解析辅助邮箱邮件正文；resourceId/binding/message 必须关联，最终路径由 OpenAPI 评审固定。 |
| `GET` | `/v1/admin/resources/{resourceId}/aliases` | Core 与 MailTransport 组合显式/点/加号 alias inventory 和自动补货 schedule 安全摘要；一期不返回内部 attempt 列表/字段。 |

管理端显式命令：

| 方法 | URI | 说明 |
|------|-----|------|
| `PATCH` | `/v1/admin/resources/{resourceId}` | Core 原子编辑契约可以携带 `bindingAddress`；Core 通过 `BindingAdminPort` 写入，MailTransport 不直接接收资源字段。 |
| `POST` | `/v1/admin/resources/{resourceId}/aliases` | Alias expedite：只提前/唤醒 durable schedule，返回 `202` 任务；达到配额、存在不可替换 attempt 或状态冲突时按 `409/422` 返回。 |

Microsoft ACL 只做 Go 进程内模块。辅助邮箱选择、掩码解析、状态回写和验证码等待都是 Go 进程内 Port/Application Service 调用。

---

## 8. 默认 SMTP 收发策略

### 8.1 外发

系统默认外发模式为 `direct`：

- `DeliveryPort.Send` 把完整 SMTP payload 临时写入独立 Redis key，TTL 由系统设置 `smtp_outbound_payload_ttl_minutes` 控制、默认 5 分钟、允许 1–1440 分钟，且不得小于外发任务超时；随后投递 `mailtransport:outbound_send` Asynq 引用任务。HTTP/IAM 不同步等待真实 SMTP 网络发送，外发通知不写 `outbound_mails` 或其他 MySQL 状态表。
- Redis key 由 `idempotencyKey` 的 SHA-256 摘要派生；key 存活期间，同 key 同 payload 视为重复任务，同 key 不同 payload 返回幂等冲突。payload 被清理或 TTL 到期后不再保留历史幂等事实。
- `mailtransport:outbound_send` 的 Asynq payload 只携带不敏感的哈希引用，不携带收件人、正文或验证码。worker 发送成功或最终失败后都主动删除 Redis payload；进程崩溃、kill 等无法执行清理的异常由配置 TTL 自动回收。
- Asynq 任务级 `MaxRetry` 固定为 0、`Retention` 固定为 0；入队时快照 `smtp_task_retry_count`，允许 0–20、默认 3 次，控制可重试 SMTP/基础设施失败的内部重试次数，已入队任务不会被后续配置变更重新解释，永久 SMTP 拒绝不重试。外发任务超时必须覆盖每次发送尝试的 30 秒预算与线性退避总和；最终失败只打印禁敏日志，不保存成功/失败记录。
- Microsoft Token 刷新和别名维护任务在入队时快照当前 OAuth/代理尝试预算与恢复码等待时间，并扩展 Asynq timeout/Unique TTL；默认配置仍保持原有 2 分钟和 20 分钟。
- `mailtransport:outbound_dispatch` 只作为滚动部署期间旧任务类型的兼容 no-op，不再扫描或恢复 MySQL 外发记录。
- worker 执行真实发送：按收件人域名解析 MX，使用 `mx.aishop6.com` 作为 HELO，使用 `no-reply@aishop6.com` 作为发件人，直连对方 MX 的 25 端口。
- direct SMTP 外发必须使用 Go 标准库 `net/smtp.Client` 执行 `EHLO -> STARTTLS(如对方支持) -> MAIL FROM -> RCPT TO -> DATA`，网络连接必须显式使用 `tcp4`。不得依赖默认 `tcp` 双栈拨号或第三方 SMTP 客户端内部拨号策略；这属于协议适配层约束，不上浮到 IAM/通知模板。
- 启用 `SMTP_DKIM_ENABLED=true` 时，worker 必须先生成最终 RFC822 原文，再由 MailTransport infra 使用 DKIM 私钥签名后写入 SMTP `DATA`；签名必须发生在协议适配层，IAM、验证码和通知模板不得直接感知 DKIM。
- DKIM 私钥只能通过 `SMTP_DKIM_PRIVATE_KEY_FILE` 或部署 Secret 注入，不得持久化到 `OutboundMail`、SystemLog、普通日志或 Git；配置错误必须启动失败或任务失败，不能静默降级成未签名发送。
- 测试阶段默认 selector 为 `mx`，`SMTP_DKIM_ALGORITHM=ed25519-sha256` 时 DNS 必须发布 `mx._domainkey.aishop6.com TXT "v=DKIM1; k=ed25519; p=<raw-public-key-base64>"`。
- `SMTP_MODE=relay` 仅作为显式配置的外部中继模式；默认测试环境不得再依赖 mailbux。
- worker 仅输出安全运行日志；普通日志和 SystemLog 不得打印邮件正文、验证码、SMTP 密码或完整收件人地址。
- MailTransport 使用独立 Asynq 队列 `mailtransport`，权重低于默认业务队列；SMTP 直连、入站原文校验等外部网络任务不能挤压资源导入、代理检测等后台任务。

### 8.2 入站

系统默认入站监听部署为宿主机 `25 -> server:2525`：

- SMTP `RCPT TO` 阶段必须解析到可接收的 Domain 资源或生成邮箱；解析失败返回 SMTP 550。
- SMTP `DATA` 阶段只做原文读取、MinIO private bucket 保存、`InboundMail(pending)` 创建和 Asynq 投递；不得同步执行 MailMatch 项目匹配。原文和 `InboundMail` 事实落库后，单条 enqueue 失败只能写 SystemLog 并依赖 dispatcher 恢复，不能向对端返回临时失败诱发重复投递。
- SMTP `DATA` 读取必须受 `SMTP_INBOUND_MAX_MESSAGE_BYTES` 限制，超过限制返回 552，不进入 MinIO、MySQL 或异步任务。接收端应把 DATA 限量流式转入临时文件或等价流式缓冲，再由私有文件端口写入 MinIO，避免整封邮件常驻内存。接收成功和慢接收日志只允许记录远端地址、收件人数、字节数和耗时，不记录邮件正文、验证码或完整收件人地址。
- 多个收件人拆成多条 `InboundMail` 任务事实，共用同一个 RFC822 原文 objectKey。
- Asynq worker 先校验原文对象可读；`resourceType=domain` 时调用 MailMatch consumer 完成结构化消息落库和匹配，再把任务置为 `stored`。`resourceType=microsoft` 辅助邮箱验证邮件仍供 Microsoft 绑定验证码流程消费。
- `mailtransport:inbound_dispatch` 定期扫描 `pending` 和 stale `processing` 记录并补投递，读取原文失败按 Asynq 尝试次数恢复为 `pending` 或最终 `failed`，失败路径必须写 SystemLog。
- Microsoft 辅助邮箱收码属于验证流程内的快速消费场景，`BindingCodeWaitPort` 可以读取 `pending/processing/stored` 入站事实对应的 private RFC822 原文；`stored` 只表示 MailMatch 后续消费前的异步可读确认，不应成为验证码读取的前置条件。收码轮询默认短间隔，并在超时边界保留小的晚到宽限窗口，以吸收 SMTP 入站与 Microsoft 页面流之间的秒级抖动。

### 8.3 DNS

开发/测试默认 DNS：

| 类型 | 名称 | 值 | 代理 |
|------|------|-----|------|
| A | `mx` | 测试服务器公网 IPv4 | DNS only |
| AAAA | `mx` | 测试服务器公网 IPv6 | DNS only |
| MX | `@` | `mx.aishop6.com`，优先级 `10` | 不适用 |
| TXT | `@` | `v=spf1 mx ip4:<测试服务器公网 IPv4> ip6:<测试服务器公网 IPv6> -all` | 不适用 |
| TXT | `mx._domainkey` | `v=DKIM1; k=ed25519; p=<raw-public-key-base64>` | 不适用 |
| TXT | `_dmarc` | `v=DMARC1; p=none; rua=mailto:no-reply@aishop6.com` | 不适用 |
| TXT | `default._bimi` | `v=BIMI1; l=https://remail.aishop6.com/.well-known/bimi/logo.svg; a=` | 不适用 |

BIMI 只引用 `/.well-known/bimi/logo.svg` 的静态 SVG Tiny-PS 文件，不能引用邮件模板内联图片、PNG、base64 或 SPA fallback 页面。测试阶段可先发布记录验证文件可访问，但邮箱客户端稳定展示通常要求 SPF/DKIM/DMARC 全部通过，并把 DMARC 从 `p=none` 升级到 `p=quarantine` 或 `p=reject` 后再观察。部分邮箱客户端还会叠加 VMC/CMC、品牌资料或联系人头像策略，因此 BIMI 记录正确不等于所有客户端都会立即展示 Logo。

---

## 9. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-MT-1 | 协议全封装为 ACL | 核心域不直接依赖 Graph/IMAP/SMTP/Microsoft 页面流。 |
| ADR-MT-2 | Microsoft 复杂流程由 Go ACL 承接 | 新项目只有一个 Go 后端运行时，避免双部署。 |
| ADR-MT-3 | 邮箱服务器归 Core | 服务器在线状态影响资源可分配性。 |
| ADR-MT-4 | 辅助邮箱 Query 只读排障 | 管理员需要查状态，但 Query 接口不能推进协议状态；输入修正只能走独立 BindingAdmin 命令。 |
| ADR-MT-5 | 默认 SMTP 直连出站 + 入站原文异步落库 | 邮件端口由本系统控制，避免外部中继不可用；SMTP 会话只做快速可证明的接收，耗时和可重试处理进入 Asynq。 |
| ADR-MT-6 | Binding 管理分为 Query 与显式 Admin Command | 管理员需要看见并修正输入，但不能通过后台直接伪造 Microsoft 协议结果。 |
| ADR-MT-7 | 管理员“创建显式别名”采用 expedite 语义 | 保留既有 UI 操作，同时复用自动补货的配额、任务、fencing 和 uncertain 对账，避免第二条远端创建路径。 |
