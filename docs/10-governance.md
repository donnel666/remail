# BC-GOVERNANCE 平台治理上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-07-12 | V1.1 | Codex | 补充管理员 Microsoft 资源的 TaskView 聚合、命令级 OperationLog、外部失败 SystemLog、批量审计和敏感邮件正文读取审计；不改变业务任务事实归属。 |
| 2026-07-12 | V1.2 | Codex | 对齐当前模块化单体：TaskView 可用一个直接源表、只读、有界的 SQL `UNION` 查询组合同库任务事实，不建立投影表、不提前建设空转 TaskSourcePort，且绝不接管来源任务写入。 |

> 通用域。治理域提供配置、日志、通知、内容运营、任务可观测和后台命令入口，不直接拥有业务状态机。

---

## 1. 定位

| 拥有 | 不拥有 |
|------|--------|
| 系统配置、公告、通知、帮助、操作日志、系统日志、异步任务视图、文件存储入口、运行健康 | 用户身份、订单状态、钱包余额、资源状态 |

后台不是跨域改表工具。后台动作必须调用拥有该状态的业务上下文服务。

---

## 2. 实体

| 实体 | 字段/状态 |
|------|-----------|
| `Setting` | `key`、`value`、`valueType`、`group`、`enabled` |
| `Announcement` | `type`、`title`、`content`、`enabled`、`publishAt/expireAt`、`sortOrder` |
| `HelpArticle` | `slug`、`title`、`content`、`parentId`、`enabled`、`sortOrder` |
| `Notification` | `userId`、`type`、`title`、`content`、`refType/refId`、`readAt` |
| `SystemLog` | `level`、`module`、`eventType`、`requestId`、`bizType/bizId`、`message`、`detail` |
| `OperationLog` | `operatorUserId`、`operationType`、`type/resourceId`、`path`、`result`、`safeSummary` |
| `TaskView` | 从 Asynq/各 BC durable 任务事实聚合出的只读任务视图；统一展示标识、业务关联、类型、状态、进度、安全诊断和时间，不复制任务状态机。 |

---

## 3. 日志规则

| 日志 | 用途 | 要求 |
|------|------|------|
| OperationLog | 谁做了什么高风险动作 | 管理命令成功/失败都写；由服务端生成安全摘要。 |
| SystemLog | 系统发生了什么 | 任务失败、上游失败、协议失败、异常补偿写入。 |
| 运行日志 | 进程诊断 | `slog` JSON，带 requestId，不写敏感值。 |

需要业务原因的动作：

| 场景 | 原因字段 |
|------|----------|
| 项目驳回/重复处理 | 需要。 |
| 工单驳回 | 需要。 |
| 退款、终止服务 | 需要。 |
| 钱包人工加扣款 | 需要。 |

不需要请求方提交原因的动作：

| 场景 | 说明 |
|------|------|
| 禁用/启用 API Key | 服务端固定摘要。 |
| 重置服务凭证 | 服务端固定摘要。 |
| 触发拉取/健康检查 | 服务端固定摘要。 |
| 资源重新验证 | 服务端固定摘要。 |

管理员 Microsoft 资源管理的日志补充规则：

| 场景 | 规则 |
|------|------|
| 单资源命令 | Validate、Enable、Disable、Publish、Unpublish、Delete、Recover、ReplaceCredentials、RefreshToken、ExpediteAliasSchedule、FetchMail 成功和失败都写一条 OperationLog；记录 operator、resourceId、command、result、requestId 和安全摘要。 |
| 批量命令 | `ids/filter` 一次提交只写命令级 OperationLog，记录 selection 模式、过滤摘要、接收时高水位、预计/实际影响数和 taskId；不得为大批量资源逐条写日志。 |
| 外部或 worker 失败 | Microsoft、Graph、IMAP、代理、Redis/Asynq、MinIO 失败写 SystemLog，关联 `bizType/bizId/taskId/requestId`；detail 必须先禁敏。 |
| 邮件摘要查询 | 普通分页/搜索不逐条写 OperationLog，避免审计表被高频读放大。 |
| 敏感正文读取 | 管理员读取单封主邮箱或辅助邮箱正文时写定向读取审计，记录 operator、资源/消息标识、入口、结果和 requestId，不记录正文、搜索词、验证码或 objectKey。 |

任何日志和任务视图都不得包含密码、Client ID 原值、RT、AT、验证码、邮件正文、RFC822 objectKey、claim/dispatch token、代理凭据或 Microsoft 原始页面/响应。上游原始错误必须先经 ACL 分类和禁敏，再进入受控 SystemLog 诊断字段；对外只返回安全 message。

---

## 4. 后台能力模型

后台页面按 Casbin 权限控制，特权用户仍可访问普通用户页面。

| 分组 | 能力 |
|------|------|
| IAM | 用户、RBAC 角色、权限覆盖、邀请码、强制退出。 |
| Core | 项目、商品、邮件规则、资源、别名、自建邮箱域名、邮箱服务器。 |
| Allocation | 分配查询、库存诊断、历史项目排除；异常释放经 Trade 服务清理、退款或终止命令进入。 |
| Trade | 订单查询、退款、终止服务、超时扫描、服务清理重试。 |
| Billing | 钱包、流水、充值查账、卡密、提现、供应商结算、人工调账。 |
| Mail | 邮件事实、匹配冲突、重跑匹配、辅助邮箱绑定排障。 |
| Proxy | 资源代理、系统代理、检测、绑定排障、异常检测和管理员禁用记录。 |
| OpenAPI | API Key、服务凭证、请求日志。 |
| Aftersale | 工单认领、改派、驳回、解决、SLA。 |
| Ops | 系统配置、任务、日志、运行健康、公告、通知、帮助。 |

管理员 Microsoft 资源的 `TaskView` 是跨上下文管理读模型，不是新的任务聚合。任务事实及重试规则仍由命令所有者维护：

| TaskView 类型 | 事实所有者 | 统一业务关联 |
|---------------|------------|--------------|
| import/validation/token refresh/bulk resource command | Core | `bizType=microsoft_resource`、`bizId=resourceId`；批量任务另带安全 selection 摘要。 |
| alias schedule/attempt | MailTransport | 同一 Microsoft resource 业务关联，并保留 quota、attempt 和 fencing 的安全状态。 |
| manual resource fetch | MailMatch | 同一 Microsoft resource 业务关联，并保留资源级 single-flight 结果。 |

TaskView 只做状态翻译和分页筛选，稳定状态并集为 `queued/running/succeeded/failed/uncertain/canceled`；某类任务不支持取消时不产生 `canceled`，任何来源都不能把取消或结果未知伪装成成功。来源 BC 的 taskId、幂等键、lease、attempt、fencing 和重试策略保持权威；Governance 不通过任务重试/取消接口直接更新其他 BC 表，而是调用对应 Command Port。管理员 Microsoft TaskView 的必需来源暂时不可用时，当前 Tab 返回安全 `503` 并允许重试，不用旧缓存或不完整集合伪造成功状态。

TaskView 对外 `taskId` 使用 source-qualified 或等价全局唯一的安全字符串，避免多个来源自增 ID 冲突；它不能包含表名、claim/lease/fencing token，也不能让通用 Task API 绕过来源 Command Port 修改任务事实。

项目当前是共享数据库的模块化单体。TaskView 基础设施可以通过一个明确标识、直接读取源表、只读且有界的 SQL `UNION` 查询组合读取各任务事实；这是管理员诊断读模型的窄例外，不等于 Governance 拥有来源任务，不建立投影表，也不允许通过该 repository 更新其他 BC 表。若未来任务事实迁出同库，再以来源 Query Port 替换该查询组合，不提前建设空转适配层。

---

## 5. 文件存储

| 文件 | 规则 |
|------|------|
| 私有文件 | 存 MinIO private bucket，只返回受控读取接口。 |
| 公开文件 | 可走 public bucket 或静态代理。 |
| 业务表 | 只保存 objectKey、文件名、MIME、大小、归属。 |
| 失败文件 | 只保存安全诊断，不写密码/RT/Token/正文。 |

---

## 6. Port

| Port | 方向 | 职责 |
|------|------|------|
| `SystemLogPort` | 入站自全域 | 写系统日志。 |
| `OperationLogPort` | 入站自全域 | 写操作日志。 |
| `NotificationPort` | 入站自全域 | 创建站内通知。 |
| `FilePort` | 入站自全域 | 保存/读取私有文件。 |
| `DeliveryPort` | 出站到 BC-MAILTRANSPORT | 外发通知邮件。 |
| `CommandPort` | 出站到业务域 | 后台命令入口规范，不直接改表。 |
| `TaskQueryPort` | 入站自后台组合查询 | 按 `bizType/bizId/kind/status` 分页读取统一 TaskView。 |

---

## 7. API 设计

内容运营：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/announcements` | 生效公告。 |
| `GET` | `/v1/help/articles` | 帮助文章列表；支持 `view=tree` 返回帮助树。 |
| `GET` | `/v1/help/articles/{slug}` | 帮助详情。 |
| `GET` | `/v1/me/notifications` | 当前用户通知。 |
| `PATCH` | `/v1/me/notifications/{id}` | 标记已读。 |
| `POST` | `/v1/me/notifications/read` | 全部已读。 |

后台：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET/POST/PUT/DELETE` | `/v1/admin/announcements` | 公告管理。 |
| `GET/POST/PUT/DELETE` | `/v1/admin/help/articles` | 帮助文章管理。 |
| `GET/POST` | `/v1/admin/notifications` | 通知查询和发送。 |
| `GET` | `/v1/admin/logs/system` | 系统日志。 |
| `GET` | `/v1/admin/logs/operations` | 操作日志。 |
| `GET/PUT` | `/v1/admin/settings` | 系统配置。 |
| `GET` | `/v1/admin/health` | 运行健康。 |
| `GET` | `/v1/admin/tasks` | 任务查询；支持 `bizType/bizId/kind/status`，管理员 Microsoft 详情使用 `bizType=microsoft_resource` 和资源 ID。 |
| `POST` | `/v1/admin/tasks/{taskId}/retry` | 任务重试。 |
| `POST` | `/v1/admin/tasks/{taskId}/cancel` | 任务取消。 |

---

## 8. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-GOV-1 | 管理员是 User + 权限 | 不建独立管理员实体。 |
| ADR-GOV-2 | 后台不直改业务表 | 避免绕过领域状态机。 |
| ADR-GOV-3 | 操作日志和系统日志分离 | 分别回答“谁做了什么”和“系统发生了什么”。 |
| ADR-GOV-4 | 私有文件只经受控接口读取 | 避免 MinIO objectKey 泄露。 |
| ADR-GOV-5 | TaskView 只组合各 BC 的 durable 任务事实 | 后台需要统一诊断视图，但任务状态机、幂等、重试和 fencing 必须继续由事实所有者控制。 |
