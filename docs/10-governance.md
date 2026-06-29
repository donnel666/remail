# BC-GOVERNANCE 平台治理上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |

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
| `TaskView` | 从 Asynq/业务任务表聚合出的任务视图，不必是单一聚合 |

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
| 触发拉取/健康检查/候选刷新 | 服务端固定摘要。 |
| 资源重新验证 | 服务端固定摘要。 |

---

## 4. 后台能力模型

后台页面按 Casbin 权限控制，特权用户仍可访问普通用户页面。

| 分组 | 能力 |
|------|------|
| IAM | 用户、角色等级、权限覆盖、邀请码、强制退出。 |
| Core | 项目、商品、邮件规则、资源、别名、自建邮箱域名、邮箱服务器。 |
| Allocation | 分配查询、库存诊断、候选刷新；异常释放经 Trade 服务清理、退款或终止命令进入。 |
| Trade | 订单查询、退款、终止服务、超时扫描、服务清理重试。 |
| Billing | 钱包、流水、充值查账、卡密、提现、供应商结算、人工调账。 |
| Mail | 邮件事实、匹配冲突、重跑匹配、辅助邮箱绑定排障。 |
| Proxy | 资源代理、系统代理、检测、绑定排障、自动禁用记录。 |
| OpenAPI | API Key、服务凭证、请求日志。 |
| Aftersale | 工单认领、改派、驳回、解决、SLA。 |
| Ops | 系统配置、任务、日志、运行健康、公告、通知、帮助。 |

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
| `GET` | `/v1/admin/tasks` | 任务查询。 |
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
