# Microsoft 与 Proto 能力矩阵

> 历史能力矩阵。当前独立复制、公共入口接入及真实协议限制见 [26-proto-mailbox-implementation.md](26-proto-mailbox-implementation.md)。

本表是 Proto 实施时的删减边界。`保留` 表示复制机制但使用 Proto 自己的实现；`删除` 表示当前不建立对应数据/任务/页面；`TODO` 表示先建立独立 Port、任务和状态观察面，但不伪造成功；`待确认` 表示必须取得 Proto 协议资料后再决定。

| 能力 | Microsoft 现状 | Proto 当前事实 | 处理 | 不能遗漏的验收点 |
|---|---|---|---|---|
| 产品标识 | `microsoft` | 没有 | 保留 | 新增 `proto`，不复用 Microsoft 常量语义 |
| 统一资源 ID | `email_resources.id` 是跨 Provider 根 ID | 用户要求统一资源 ID | 保留平台中立根 | Proto 使用 `email_resources.id` + `proto_resources` 同主键，不创建第二套 ID |
| 导入格式 | 2/3/4/5 段，含 binding、client ID、RT | 用户明确为两段 | 保留但重写 parser | 只接受 `邮箱----密码`，额外字段拒绝 |
| 原始 TXT | MinIO private artifact | 同样需要 | 保留 | DB/API/日志不保存原文和密码 |
| 导入批次 | async、skip/abort、逐行结果、幂等、恢复 | 需要 | 保留 | crash recovery、deleted restore、claim fencing |
| 资源表 | `microsoft_resources` | 无 | 独立复制 | `proto_resources`，不读写 `microsoft_resources` |
| 资源状态 | pending/validating/identifying/normal/abnormal/disabled/deleted | 需要 | 保留状态边界 | TODO 不得进入 normal |
| `identifying` | 等待历史项目识别 | 用户要求旧识别先 TODO | 保留状态和 task | 没有真实证据时不造历史关系 |
| generation fence | 有 | 必须有 | 保留 | 旧任务不能覆盖换凭据/删除/禁用后的新状态 |
| credential revision | Microsoft 凭据和 RT 版本 | 只有邮箱密码 | 改名为 Proto credential revision | 任务只传 revision，不传密码 |
| OAuth client ID | 有 | 未发现 | 删除 | 不出现 client ID 字段或 UI |
| refresh token | 有 | 未发现 | 删除 | 不复制 RT、rotated RT、token health |
| Graph API | 有 | 未发现 | 删除 | 不出现 Graph 列、筛选或诊断 |
| Microsoft IMAP fallback | 有 | 协议未知 | TODO/待确认 | 先用 Proto fetch Port，不能把 Graph 改名 |
| Microsoft 网页登录 ACL | 有 | 未发现 | 删除 | 不复制 KMSI/Consent/Identity/OTP/passkey 页面流程 |
| 代理池/IPv4 | Microsoft ACL 使用 | 协议未知 | 待确认 | 只有 Proto 协议确实需要时建立 Proto proxy policy |
| 辅助邮箱 binding | Microsoft 专有 | 未发现 | 删除 | 不建 binding mailbox、recovery lease、掩码推理 |
| 辅助邮箱验证码 | Microsoft 专有 | 未发现 | 删除 | 不出现 auxiliary tab 或 binding 字段 |
| 显式 alias | Microsoft 远端实体 | 未发现 | 删除/待确认 | 不建 alias schedule/attempt，除非协议证据确认 |
| dot alias | Microsoft/Gmail 变体能力 | 未发现 | 删除/待确认 | 初期 mailbox 固定 main |
| plus alias | Microsoft/Gmail 变体能力 | 未发现 | 删除/待确认 | 不建 plus daily quota |
| alias 周/年配额 | Microsoft 专有 | 未发现 | 删除 | 不复制配额和对账 worker |
| alias owner=super_admin | Microsoft 专有 | 未发现 | 删除 | 不复制固定用户 1 语义 |
| suffix picker | Microsoft 域名库存 | Proto 地址直接导入 | 删除 | 页面不显示 suffix tabs/blacklist |
| long-lived/short-lived | Microsoft 商品分类 | 未确认 | 删除，除非产品有该业务 | 导入不增加第三字段 |
| Graph availability facet | Microsoft 专有 | 未发现 | 删除 | API/schema/UI 均无 graph facet |
| suffix blacklist | Microsoft 项目规则 | 未确认 | 删除，除非 Proto 有地址后缀选择 | 不读 Microsoft blacklist 表 |
| 邮箱协议 | Graph/IMAP/Web ACL | 未知 | TODO/待确认 | 明确记录 Proton/Bridge/API/IMAP 选择 |
| 验证 | RT + 收件探测 | 用户允许 TODO | TODO | `validating -> pending`，安全错误可观察 |
| 验证确定失败 | abnormal | 未实现 | 保留契约 | 真实协议接入后才写 abnormal |
| 验证临时失败 | retry/pending | 需要 | 保留 | Asynq retry、最终回 pending |
| 旧项目识别 | Inbox/Junk 全量扫描和历史 allocation | 用户允许 TODO | TODO | 不扫描、不造订单、不造 allocation |
| 项目 | 通用 Project | 需要 | 保留 | 复用项目访问和规则，不复用 Microsoft 业务代码 |
| 商品 | Microsoft Product fields | 需要 proto 商品 | 保留并新增类型 | main weight；dot/plus 权重为 0 |
| 接码 code | Microsoft code order | 是否支持未知 | 待确认/保留接口 | 未启用时 checkout 明确不可用 |
| 购买 purchase | Microsoft purchase order | 仅交付项目的系统长效收件权限 | 复用通用履约 | 资源密码、2FA、应用密码和上游令牌对外只写不读，任何邮箱均不得交付 |
| 钱包扣款 | 通用 Trade/Billing | 需要 | 保留通用设施 | Proto 失败可退款，账务幂等 |
| OrderToken | 通用交易 | 需要 | 保留 | token scope 绑定 Proto 订单 |
| allocation | Microsoft 专用 allocation | 需要 | 独立复制 | `proto_allocations`、项目隔离、active 唯一 |
| 订单关联 | 现有订单按 `order_no` 关联 allocation | 用户要求订单不增加列 | 保留通用关联 | `orders.order_no -> proto_allocations.order_no -> resource_id -> email_resources.id`，不增加 `proto_*_id` |
| 订单直接 resource ID | 不是现有订单模型的一部分 | 不需要 | 删除 | 不在 `orders` 增加多态 `resource_id`；通过 typed allocation 保持外键类型安全 |
| 资源锁 | Microsoft root lock | 需要 | 独立复制 | 先锁资源后写 allocation，冲突重试 |
| public/private | 通用供给能力 | 需要 | 保留 | 仅 normal 可公开 |
| 历史 allocation | Microsoft/ Gmail 有 | TODO 阶段无事实 | TODO | 无证据不创建零金额历史订单 |
| 邮件消息 | MailMatch 通用投影加 provider adapter | Proto 无协议 | 独立 Proto facts | 后续 `proto_messages` 去重和权限读取 |
| 邮件抓取 | Graph/IMAP/SMTP 等 | 未知 | TODO/待确认 | 独立 fetch task、cursor、safe failure |
| 项目规则匹配 | sender/recipient/subject/body | 需要 | 保留通用规则语义 | provider message 进入 Proto matcher |
| 验证码提取 | MailMatch extractor | 未知 | TODO | 不返回假 code |
| 管理列表 | Microsoft facets/owner/date/status | 需要 | 复制页面行为 | Proto API 和页面独立 |
| 管理详情 | basic/orders/aliases/tasks/mails/auxiliary | Proto 无 alias/binding | 改为 basic/orders/tasks/mails | 删除 Microsoft 专属 Tab |
| 凭据替换 | Microsoft password/client/RT | Proto email/password | 保留但重写 | password write-only，递增 revision |
| enable/disable | 通用管理命令 | 需要 | 保留 | disabled 不被普通 validate 隐式启用 |
| publish/unpublish | 通用供给 | 需要 | 保留 | active allocation 和状态保护 |
| operation log | 通用治理 | 需要 | 保留 | `proto.*` 操作类型和安全摘要 |
| system log | 通用治理 | 需要 | 保留 | TODO/队列/fence 分类，不记录密钥 |
| TaskView | Microsoft/Gmail task aggregation | 需要 | 独立 Proto task view | 只显示安全字段 |
| queue | Microsoft/Gmail 各自队列 | Proto 需要独立 | 保留但新建 | `background_proto_*`，不共用 Microsoft queue |
| Redis cursor/lease | Microsoft/Gmail 使用 | 需要 | 保留 | claim token、TTL、compare-delete |
| worker retry | Asynq retry + durable recovery | 需要 | 保留 | 最终错误不丢状态 |
| dashboard | 按产品统计 | 未接入 | 后续保留 | 独立 proto metrics/buckets |
| Web 用户页 | MicrosoftEmails | 需要 Proto 页 | 复制后删减 | `/proto`，无 Graph/suffix/alias |
| Web 管理页 | AdminMicrosoftEmails | 需要 Proto 页 | 复制后删减 | `/admin/proto`，无 RT/binding/alias |
| OpenAPI | Microsoft/Gmail schema | 需要 Proto schema | 独立新增 | `Proto*` schema/path，不手工混用 |
| i18n | 通用翻译 | 需要 | 保留 | Proto 文案不出现 Microsoft 专属名词 |

## 明确属于 Microsoft、初期不应复制的能力

1. OAuth client ID、refresh token、rotated refresh token、token expiry、Graph available 和 token refresh worker。
2. Microsoft 登录网页 ACL：KMSI、Consent、Identity、OTP、Passkey、MFA、手机验证、密码找回和页面状态分类。
3. Microsoft 辅助邮箱 binding：掩码地址、proof picker、验证码反推、recovery lease、binding mailbox 表和辅助邮箱邮件页。
4. 显式 alias 创建、alias schedule/attempt、周/月/年配额、对账、固定 super admin owner。
5. dot/plus alias 派生、plus 日限额、alias mailbox 分类和 suffix inventory。
6. Outlook/Hotmail 域名白名单、Microsoft suffix tabs、suffix blacklist、long-lived/short-lived 和 Graph 筛选。
7. Microsoft 专用代理、IPv4、Graph/IMAP fallback 诊断和上游页面错误分类。
8. Microsoft 旧项目收件人分类规则本身。Proto 只复制“历史扫描任务和匹配事实”这个机制，地址分类需按 Proto 协议重新定义。

## 必须由产品确认的 Proto 能力

| 问题 | 当前默认 | 确认后影响 |
|---|---|---|
| `proto` 是否指 Proton Mail | 不假设，只使用产品键 | 决定连接器、域名、协议和错误分类 |
| 密码类型 | 导入值称为 password，语义未定 | 网页密码、Bridge 密码和应用密码的验证路径完全不同 |
| 协议 | TODO | IMAP/Bridge/API/Web 选择决定 fetch、cursor 和代理 |
| 2FA | 未知 | 可能需要独立密钥字段和凭据替换 API |
| 历史邮件读取 | 未知 | 决定 identifying 是否可实现 |
| code 服务 | 未知 | 决定 code product、服务窗口和 MailMatch 接入 |
| purchase 服务 | 仅交付项目的系统长效收件权限 | 不涉及账号交付，资源 password 等敏感数据对外只写不读 |
| alias/多地址 | 未知 | 只有确认后才建 alias/domain 事实表 |
| 自定义域名 | 未知 | 决定 inventory/suffix 选择是否存在 |
| 公开出售 | 默认保留通用开关 | 仅出售指定项目的系统收件服务，不转交账号或底层凭据 |
| 账号生命周期和保修 | 默认复制通用商品窗口 | 需要协议可验证、可交付后再开放 |

## 隔离验收清单

- `rg` 检查 `internal/proto` 不导入 Microsoft package。
- Proto SQL allowlist 不包含 `microsoft_*`、`explicit_aliases`、`dot_aliases`、`plus_aliases`、`microsoft_binding_*`。
- Proto 只允许通过 `email_resources` 的中立根 ID 做身份关联；不读取 Microsoft typed table，也不把 `email_resources` 的 ID 当作 Provider 业务状态。
- Proto task type、queue name、metric name 全部以 `proto` 前缀或 `background_proto_` 前缀命名。
- Proto worker 的 payload 快照不含 password、token、raw TXT 或 mail body。
- Proto repository 的写操作只能触碰 Proto 表和明确的通用审计/项目/订单接口。
- 统一资源 ID 测试通过：不同 Provider 的资源 ID 全局唯一，Proto allocation 的 `resource_id` 必须指向 `email_resources.type='proto'`。
- `orders` schema diff 不包含任何 `proto_*_id` 或 `resource_id` 新列；只允许更新通用 `product_type/allocation_type` 的 `proto` 枚举约束。
- Microsoft 既有测试、队列拓扑、migration checksum 和 API 合同全部通过。
- 删除 Proto feature flag 后，Microsoft 导入、验证、分配、订单和 Web 页面行为与上线前一致。
