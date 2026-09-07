# Proto 邮箱产品实施方案

> 历史提案。当前修正实现与未完成协议边界见 [26-proto-mailbox-implementation.md](26-proto-mailbox-implementation.md)；不要继续依据本提案中的早期删减与密码交付设想实现。

状态：提案，当前阶段只实现导入闭环、异步状态和可观察的 TODO 验证/历史识别任务。

## 1. 结论

`proto` 作为独立 Provider 垂直模块实现。模块可以复用平台设施和通用商业契约，但不能引用 Microsoft 的领域对象、应用服务、仓储、任务类型、队列、状态含义或数据表。

这里的 `proto` 只表示产品键，不对 Proton Mail 的具体协议作未经确认的假设。当前代码库没有发现 Proto/Proton 的独立协议客户端，因此验证、历史识别和收件适配器先使用 TODO 实现。导入行固定为：

```text
邮箱----密码
```

未完成真实验证时，资源不能进入 `normal`、库存、分配或出售状态。这样导入可以先落库，后续替换验证适配器时不需要改变上层 API、表边界和任务载荷。

## 2. 隔离边界

### 2.1 Proto 自己拥有的内容

新增目录：

```text
internal/proto/domain/
internal/proto/app/
internal/proto/infra/
internal/proto/api/
```

Proto 自己拥有以下事实和逻辑：

- `proto_resources` 子资源模型、凭据 revision、资源状态机和资源查询；资源的全局 ID 使用平台中立的 `email_resources.id`。
- TXT artifact、导入批次、导入明细、文件内查重、数据库查重和已删除资源恢复。
- 验证 dispatcher、验证 task、generation/revision fencing 和 TODO 结果。
- 旧项目识别 task、历史匹配事实和 TODO 结果。
- Proto 邮件协议适配器、邮件抓取、邮件去重、验证码提取和订单投递。
- Proto allocation、order guard、资源锁、项目隔离、供给范围和释放。
- Proto 管理员命令幂等、维护任务、诊断和安全错误。
- Proto API、OpenAPI schema、前端 API client、用户页面和管理员页面。

### 2.2 可以复用但不能带入 Provider 语义的设施

- IAM session、CSRF、权限检查和用户目录。
- MinIO private `FilePort`。
- Redis、Asynq client、worker 生命周期和 background load gate。
- OperationLog、SystemLog、TaskView 的通用写入/查询设施。
- 钱包扣款、退款、订单 token、通用订单状态和项目访问控制。
- 通用分页、表格、筛选、认证和 UI 组件。
- 平台中立的 `email_resources` 资源注册表。它只保存 `id/type/owner/version`，不保存任何 Microsoft 协议字段；复用这个 ID 根不等于复用 Microsoft 业务代码。

复用设施时，调用方只能通过稳定 Port 接入。Proto 代码禁止导入 `internal/mailtransport/infra/msacl`、`internal/core/app/microsoft_*`、`internal/mailmatch` 中仅服务 Microsoft 的实现，也禁止直接查询 `microsoft_*` 表。

### 2.3 商业集成的边界

项目、商品、钱包和订单是平台的通用能力。推荐采用“Provider 自有事实 + 通用商业外壳”的方式：

- `project_products.type=proto` 作为新增商品类型，只增加枚举和 Proto 分支，不改变 Microsoft 分支。
- 订单主表继续复用通用订单状态、钱包流水和订单 token；Proto allocation、凭据投递和邮件履约通过独立 Port 接入。
- 订单不新增 `proto_alloc_id`、`proto_resource_id` 或其他 Provider 专属列。关联链路固定为：

  ```text
  orders.order_no
    -> proto_allocations.order_no
    -> proto_allocations.resource_id
    -> proto_resources.id
    -> email_resources.id (global resource ID, type='proto')
  ```

  `orders.allocation_type` 是已有的通用字段，只需把允许值扩展为 `proto`；这不是新增列。订单读取资源时通过通用 `FindAllocationByOrder(orderNo)` 或等价的 Provider allocation adapter 获取统一 `ResourceID`。
- Proto allocation 事实只写 `proto_allocations`，不写 `microsoft_allocations`、`gmail_allocations` 或 Microsoft legacy 字段。

该边界保证 Proto 不依赖 Microsoft。共享代码只增加 `proto` 的明确分支，现有 Microsoft 行为和数据不变。

### 2.4 为什么不把 `resource_id` 直接写进 `orders`

订单直接保存多态 `resource_id` 会失去数据库外键类型约束：同一个数字可能来自 Microsoft、Gmail、iCloud 或 Proto，订单表无法用一个普通外键保证它指向正确的 Provider。订单还可能在支付前没有 allocation，直接写资源 ID 会引入空值、补偿和回滚问题。

现有模型已经把 allocation 作为订单履约事实：`order_no` 唯一标识订单，Provider allocation 用自己的表保存资源 ID。Proto 沿用这一模式，既能保持真正的类型外键，又不增加订单表列。API 返回的 `allocationId` 是通过 `order_no` 查询后的组合结果，不代表 `orders` 表有 Provider 专属 ID 列。

当前代码库已经有这套基础：`migrations/00001_initial.sql` 的 `email_resources` 是共享资源根，`migrations/00086_drop_legacy_order_allocation_ids.sql` 移除了订单上的 Microsoft/Domain allocation ID，`migrations/00083_icloud.sql` 明确规定订单只按唯一 `order_no` 关联 allocation；`internal/alloc/domain/allocation.go` 的 `UnifiedAllocation` 已经包含 `Type`、`ID`、`OrderNo` 和 `ResourceID`。Proto 应接入这些通用契约，不再设计第三种订单关联方式。

## 3. 目标流程

```text
上传 email----password TXT
  -> private artifact
  -> proto_resource_imports(processing)
  -> proto:resource_import 异步 worker
  -> 严格两段解析 / 文件内查重 / 库内查重
  -> proto_resources(pending)
  -> proto validation dispatcher
  -> validating
  -> TODO 验证结果回 pending
  -> 后续真实验证成功后进入 identifying
  -> proto:history（当前为安全 TODO）
  -> TODO/真实历史识别
  -> 后续真实识别成功后 normal
  -> 商品库存
  -> proto allocation
  -> 通用订单扣款和 OrderToken
  -> Proto mail fetch / code match / purchase delivery
  -> 完成、退款或释放
```

当前 TODO 阶段的实际结果是 `pending`，不是 `normal`。管理员列表必须显示“待验证/TODO”，库存接口必须过滤掉 `pending`、`validating`、`identifying`、`abnormal`、`disabled` 和 `deleted`。

## 4. Proto 状态机

### 4.1 资源状态

```text
导入或重新验证 -> pending
pending -> validating                 dispatcher 成功领取
validating -> pending                 TODO、临时错误、队列失败、租约恢复
validating -> identifying             真实验证权威成功
identifying -> normal                 真实历史识别成功
validating -> abnormal                真实验证给出确定性凭据失败
normal -> disabled                    管理员禁用
pending/abnormal/disabled -> deleted  管理员逻辑删除（无活动 allocation）
disabled -> pending                   管理员启用后重新验证
deleted -> pending                    管理员恢复后重新验证
```

### 4.2 设计约束

- `normal` 是可以进入库存的唯一健康状态。
- `identifying` 在未来真实历史识别完成前不能分配。
- TODO 验证不得伪造健康凭据、收件证据或 `normal` 状态。
- TODO 历史识别不得创建假历史订单、假 allocation 或假邮件命中。
- `disabled` 和 `deleted` 不允许普通 Validate 隐式恢复。
- 所有状态变更检查 `owner_user_id`、`status`、`validation_generation` 和 `credential_revision`。
- 管理员换密码、删除、恢复、禁用或再次验证会递增 generation/revision，使旧任务 no-op。

### 4.3 导入批次状态

```text
accepting -> processing -> imported
                         -> failed
```

`processing` 批次需要 claim token、generation、attempt、started/finished 时间和安全错误。worker 崩溃后 dispatcher 可以恢复过期 claim；终态再次收到同一 task 必须 no-op。

## 5. 数据库设计

迁移从当前最新 `00135` 之后开始，建议按以下顺序拆分。实际文件名以开发时仓库的最新版本为准，不能修改已有 Microsoft migration。

### 5.0 统一资源 ID（采用现有资源根）

项目已经有平台级 `email_resources` 根表，`id` 是所有邮箱资源类型共享的全局 ID。Proto 不再创建第二套 `proto_email_resources` 或自己的 ID 序列，而是在同一事务中写入：

```text
email_resources(id = R, type = 'proto', owner_user_id = U)
proto_resources(id = R, resource_type = 'proto', ...)
```

规则：

- `email_resources.id` 由数据库全局生成，Microsoft、Gmail、iCloud、Domain、Proto 不会出现相同 ID。
- `proto_resources.id` 是主键，同时通过 `(id, resource_type)` 外键指向 `email_resources(id, type)`；如果 Proto 表冗余 `owner_user_id`，使用 `(id, resource_type, owner_user_id)` 外键保证 owner 与根表一致。
- 邮箱唯一性仍由 `proto_resources.email` 负责；不同 Provider 可以拥有相同字符串邮箱，不能把邮箱字符串当作全局资源 ID。
- 删除、恢复、转移 owner、换凭据和导入恢复都必须在同一事务内更新根表与 Proto 子表。
- 资源列表和分配查询必须同时检查 `email_resources.type='proto'`，防止把其他 Provider 的同数值 ID 当成 Proto 资源。

这只共享“资源身份注册”这一平台事实，不共享 Microsoft 状态、字段、仓储、任务或协议。

对应 migration 只扩展通用允许值，不增加订单列：

- `email_resources.type` 增加 `proto`。
- `project_products.type`、`orders.product_type` 增加 `proto`。
- `allocation_order_guards.type`、`orders.allocation_type` 增加 `proto`。
- 如果 Proto 接入通用 MailMatch projection，再把 `mailmatch_messages.resource_type` 和 fetch job 的 allocation/resource type CHECK 增加 `proto`；Proto 专属消息表则不需要改这些表。
- `orders` 不新增 `proto_alloc_id`、`proto_resource_id`、`resource_id` 或任何 Provider 专属外键。

### 5.1 `proto_resources`

Proto 资源主表，使用统一资源 ID 作为主键，不使用 `microsoft_resources` 或 `gmail_resources`。

| 字段 | 说明 |
|---|---|
| `id` | 全局 Proto 资源 ID，等于 `email_resources.id` |
| `resource_type` | 固定值 `proto`，用于类型外键和防串表 |
| `owner_user_id` | 资源所有者快照；必须与根表一致 |
| `email` | 规范化邮箱，唯一 |
| `password` | 私有凭据；只在服务端内部读取，禁止 API/日志/任务回显 |
| `credential_revision` | 凭据版本，换密码递增 |
| `status` | `pending/validating/identifying/normal/abnormal/disabled/deleted` |
| `for_sale` | 是否公开供给；只有 `normal` 才能为 true |
| `validation_generation` | 验证 fence |
| `validation_failures` | 连续确定性失败次数 |
| `validation_request_id` | 最近请求关联 ID |
| `last_safe_error` | 安全诊断，不写密码、token、原文响应 |
| `last_checked_at` | 最近验证时间 |
| `alloc_bucket` | 分桶查询键 |
| `last_allocated_at` | LRU/轮换时间 |
| `version` | 管理编辑乐观锁 |
| `created_at/updated_at` | 审计时间 |

约束和索引：

- `UNIQUE(email)`，邮箱按同一规范化规则比较。
- `PRIMARY KEY(id)`、`UNIQUE(id,resource_type,owner_user_id)`，并建立到 `email_resources` 的复合外键。
- `(owner_user_id,status,created_at,id)`。
- `(status,for_sale,alloc_bucket,last_allocated_at,id)`。
- `(validation_generation,credential_revision,status,id)` 用于 fence 查询。
- 状态 CHECK 不包含 Microsoft 的 `graphAvailable`、`longLived`、`binding` 或 alias 状态。

密码是否需要静态加密，必须复用项目现有 secret/KMS 能力；若当前没有通用能力，应在 Proto 凭据实现中单独封装，不能复制 Microsoft token 字段或把密码放进日志。

### 5.2 `proto_resource_imports`

| 字段 | 说明 |
|---|---|
| `id` | 导入 ID |
| `operator_user_id` / `owner_user_id` | 操作人和资源归属人 |
| `source_object_key` | MinIO private artifact key |
| `failure_object_key` | 私有失败明细 CSV，可为空 |
| `file_name` | 清理后的文件名 |
| `status` | `accepting/processing/imported/failed` |
| `error_strategy` | `skip/abort` |
| `accepted/imported/restored/skipped/failed` | 计数 |
| `request_id` / `idempotency_key` / `request_fingerprint` | 幂等和追踪 |
| `generation` / `claim_token` / `attempts` | worker fence 和恢复 |
| `started_at/finished_at/created_at/updated_at` | 时间 |
| `last_safe_error` | 安全错误 |

`proto_resource_import_items` 保存 `line_number`、`outcome`、`category`、`safe_message`、资源 ID；邮箱只允许在私有失败 artifact 中保留，接口返回时必须掩码。任何表、普通日志和 Asynq payload 都不能保存密码。

### 5.2.1 `proto_command_receipts`

资源发布、删除、验证、历史识别、启停和凭据替换使用 Proto 自有命令回执表按 `(operator,resource,command,idempotencyKey)` 去重。重复请求复用原命令事实，不向 Microsoft 的 command receipt 表写入数据。

### 5.3 `proto_maintenance_runs`

统一记录 Proto 的 validation、history、fetch 维护事实，替代把任务状态塞入 Microsoft 表。

字段：`id`、`resource_id`、`kind`、`status`、`validation_generation`、`credential_revision`、`attempts`、`max_attempts`、`request_id`、`last_safe_error`、`queued_at`、`started_at`、`finished_at`、`created_at`、`updated_at`。

`kind` 初期为 `validation`、`history`，后续可增加 `fetch`；`status` 为 `pending/processing/normal/abnormal/canceled`。TaskView 只读取安全字段。

### 5.4 `proto_history_matches`

真实历史识别启用后保存：`resource_id`、`project_id`、`product_id`、`mailbox`、`email`、`first_matched_at`、`last_matched_at`、`evidence_count`、`generation`、`created_at`。当前 TODO 不插入记录。

唯一键建议为 `(resource_id,project_id,mailbox,email)`。没有证据时不创建历史 allocation/order。

### 5.5 `proto_allocations`

| 字段 | 说明 |
|---|---|
| `id` | allocation ID |
| `order_no` | 订单号，唯一 |
| `project_id` / `product_id` | 项目商品 |
| `resource_id` | 全局资源 ID，引用 `proto_resources.id` / `email_resources.id` |
| `service_mode` | `code/purchase` |
| `supply_scope` | `owned/public` |
| `mailbox` | 初期固定 `main` |
| `email` | 本次交付地址 |
| `status` | `allocated/released` |
| `cost_points_snapshot` | 供给成本快照 |
| `created_at/released_at` | 生命周期 |

约束：

- `UNIQUE(order_no)`。
- `order_no` 是订单与 Provider allocation 的唯一关联键；订单表不保存 Proto allocation ID 或 resource ID。
- `guard_type='proto'`，通过已有 `allocation_order_guards(order_no,type)` 建立订单幂等和类型约束。
- active main allocation 的 `(resource_id,project_id)` 唯一，保证项目历史隔离。
- `mailbox` 初期只能是 `main`；不创建 dot/plus/explicit alias 表。
- `status=allocated` 才占用资源；释放后保留历史事实。
- 事务内先锁资源，再检查候选和订单 guard，最后写 allocation。

### 5.6 订单与统一资源的关联约束

不新增 `proto_order_guards`，直接扩展现有 `allocation_order_guards.type` 的允许值为 `proto`。每笔 Proto allocation 在同一事务内先创建 `(order_no,'proto')` guard，再写 `proto_allocations`；重复 checkout 通过 `order_no` 和 guard 幂等返回已有 allocation。

通用订单查询的关系为：

```sql
SELECT o.order_no, o.allocation_type, pa.resource_id
FROM orders AS o
JOIN proto_allocations AS pa ON pa.order_no = o.order_no
JOIN email_resources AS er
  ON er.id = pa.resource_id AND er.type = 'proto'
WHERE o.order_no = ? AND o.allocation_type = 'proto';
```

跨 Provider 管理列表继续使用现有 allocation union；Proto 只新增一个 `proto_allocations` union 分支，不改变 `orders` 表结构。

### 5.7 Proto 邮件事实

协议确定后创建：

- `proto_messages`：provider message ID、dedupe key、resource ID、folder、sender、recipients、subject、body、received_at、raw/private object reference、verification code。
- `proto_fetch_jobs`：订单/管理员抓取任务、generation、attempt、cursor、计数和安全错误。
- `proto_resource_fetch_states`：单资源 single-flight、cursor、last success/failure 和 cooldown。
- `proto_order_delivery_heads`：订单最新邮件/验证码投影。

不要先把 Proto 邮件写入 Microsoft 专用 projection。协议适配器完成后再通过 `ProtoMailMatchPort` 给通用订单履约返回安全的匹配结果。

## 6. 导入实现

### 6.1 解析规则

每个非空行必须满足：

```text
exactly 2 fields separated by ----
email----password
```

校验：

1. 去除 UTF-8 BOM、首尾空白和 CRLF。
2. 不接受空邮箱、空密码、额外字段或含多个分隔符的模糊行。
3. 复用仓库现有邮箱规范化和格式校验；不要复用 Microsoft 的四/五段语义。
4. 每行和总文件大小受限，拒绝超长行。
5. 文件内按规范化邮箱查重，保留首行并记录后续行 skipped。

### 6.2 接受请求

用户和管理员都采用 multipart 上传，HTTP 只做大小、文件名、策略和 idempotency 校验，然后：

1. 将原 TXT 保存到 MinIO private bucket。
2. 建立 `proto_resource_imports(processing)`。
3. 写入安全 OperationLog。
4. 将 `proto:resource_import` 放入 Proto 专用队列。
5. 返回 `202` 和导入 ID。

不得在 HTTP 请求内验证邮箱、登录协议或创建资源明细。

### 6.3 worker

worker 读取 private artifact，逐行执行：

1. 解析和规范化。
2. 检查本批次重复。
3. 锁定 Proto 邮箱唯一键。
4. 非 deleted 同邮箱：按 `skip|abort` 处理冲突。
5. deleted 同邮箱：复用原 ID，覆盖 owner/password，清除 `for_sale` 和错误，重置 generation，置 `pending`。
6. 新资源：创建 `pending`。
7. 每行明细和资源写入在同一事务内完成。
8. 批次计数和终态更新与资源写入同事务提交。
9. 成功后唤醒 Proto validation dispatcher。

基础设施错误回滚事务并重试；最终失败保持资源事实一致，批次写 `failed` 和安全错误。导入 worker 不验证凭据，不创建历史关系。

## 7. 验证 TODO

### 7.1 Port

```go
type ProtoValidationPort interface {
    Validate(ctx context.Context, input ProtoValidationInput) ProtoValidationResult
}
```

`ProtoValidationInput` 只在进程内传递邮箱和密码；不进入任务 payload、日志或 API。任务 payload 只包含：

```json
{"resourceId":123,"ownerUserId":7,"validationGeneration":4,"expectedCredentialRevision":2,"requestId":"..."}
```

### 7.2 当前 worker 行为

当前实现返回明确的安全分类：

```text
category: proto_validation_todo
safeMessage: Proto validation is not implemented yet.
temporary: false
```

worker 以短事务检查 fence，将 `validating -> pending`，写 `last_safe_error` 和 maintenance run，返回成功删除临时任务。它不写 `normal`、不写 `abnormal`、不创建 history task、不刷新 token。

真实协议实现后，只有权威确定性失败才写 `abnormal`；网络、代理、Bridge、API 限流和队列故障回 `pending` 并按 Asynq retry 规则处理。

### 7.3 调度和 fencing

- Redis cursor 负责批量领取；MySQL 只保存资源状态和 generation。
- `pending -> validating` 必须带 generation 和 credential revision。
- 服务重启时把过期 `validating` 恢复为 `pending`。
- enqueue 失败时按相同 fence 回滚。
- 旧 task 发现 owner/type/status/generation/revision 不匹配时 no-op。
- task retention 为 0；不把凭据写入 Redis。

## 8. 旧项目识别 TODO

### 8.1 目标接口

```go
type ProtoHistoryScanPort interface {
    Scan(ctx context.Context, input ProtoHistoryScanInput) ProtoHistoryScanResult
}
```

验证真实成功后，Core/Proto 事务先进入 `identifying`，再投递 `proto:validated_history_scan`。任务只传资源 ID、owner、generation、credential revision 和 request ID。

### 8.2 当前 TODO 行为

当前没有真实验证成功路径，因此不会自动进入 `identifying`。管理员手动提交 history 时：

- 建立 `proto_maintenance_runs(kind=history)`。
- worker 返回 `proto_history_todo`。
- 不读取远端邮箱。
- 不写 `proto_history_matches`。
- 不创建历史订单、allocation 或零金额钱包流水。
- 资源保持 `identifying`（若此前已由未来验证实现置入），由管理员看到 TODO 状态并可重试。

真实实现后再复制 Microsoft 的事务边界：规则快照、全量/增量游标、消息去重、项目匹配、历史 allocation/order 幂等和 `identifying -> normal` 同事务提交。

## 9. 队列和任务

在 `internal/platform/queues.go` 增加独立队列，并在 `platform.go` 分配唯一 worker tier：

| 队列 | 任务类型 | 用途 |
|---|---|---|
| `background_proto_import` | `proto:resource_import`、`proto:resource_import_dispatcher` | 导入批次 |
| `background_proto_validation` | `proto:validate`、`proto:validation_dispatcher` | 验证 |
| `background_proto_history` | `proto:history`、`proto:history_dispatcher` | 旧项目识别 |
| （后续） | `proto:resource_fetch`、`proto:fetch_dispatcher` | 协议确认后的管理员/订单邮件抓取 |

每个队列必须同时完成：

- `AllQueueNames` 注册。
- worker tier 和权重配置。
- topology test，确保恰好一个 tier 服务。
- task handler 注册和错误分类。
- background load/admission 配置。
- 启动恢复和 dispatcher seed。

任务载荷禁止包含：密码、access token、refresh token、原始 TXT、邮件正文、原始代理 URL。

## 10. 项目商品、库存和分配

### 10.1 商品

新增 `ProductTypeProto = "proto"`，商品配置继续使用通用字段：

- `code_enabled/code_price/code_supplier_price/code_window_minutes`。
- `purchase_enabled/purchase_price/purchase_supplier_price/activation_window_minutes/warranty_minutes`。
- `main_weight` 初期只能为正值；`dot_weight=0`、`plus_weight=0`。
- 不显示 Microsoft suffix selector、long-lived、Graph、binding 或 alias 权重。

在真实协议完成前，管理员可以配置商品，但库存恒为 0；checkout 必须返回 `insufficient_inventory` 或 `provider_not_ready`，不能绕过状态领取 pending 资源。

### 10.2 候选和库存

Proto 候选查询只读 `email_resources`（类型过滤为 `proto`）、`proto_resources` 和 `proto_allocations`：

- `status=normal`。
- `for_sale=true` 或买方拥有资源时按供给策略过滤。
- `identifying`、`pending`、`validating`、`abnormal`、`disabled`、`deleted` 全部排除。
- 先按 `alloc_bucket/last_allocated_at` 分桶，再在事务中重新锁定并校验。
- 项目历史命中或历史 allocation 存在时跳过该资源。
- `main` 资源每个项目只能有一个 active allocation。

### 10.3 订单履约

推荐的共享交易外壳只负责：项目商品校验、幂等订单、钱包扣款、服务窗口、退款和通用订单状态。Proto adapter 负责：

- 创建/释放 `proto_allocations`。
- 读取凭据并生成受权限保护的购买交付。
- 创建 Proto code service token。
- 调用 Proto fetch/match 返回验证码。
- provider 错误分类和退款原因。

订单进入 `active` 时只写已有通用 `allocation_type='proto'` 和 `delivery_email`；allocation ID、resource ID 通过 `order_no` 查询，不向 `orders` 增加列。

`purchase` 只有在产品明确允许向买方交付邮箱密码时启用。密码只能出现在 checkout 成功响应和授权订单详情，不出现在订单列表、资源列表、导出、日志或 task view。

对应的代码契约改动是增加枚举和 adapter 分支，而不是增加订单字段：

- `trade/domain` 增加 `ProductTypeProto`、`AllocationTypeProto`。
- `alloc/domain` 增加 `AllocationTypeProto`、`ProtoAllocation`；已有 `UnifiedAllocation.ResourceID` 直接承载全局资源 ID。
- `alloc/app` 增加 Proto 候选查询、资源锁、历史隔离、创建/释放 allocation 和 `FindAllocationByOrder` 的 Proto 实现。
- `trade/app`、`trade/infra` 增加 `allocation_type='proto'` 的 checkout、active、refund、cleanup、订单详情和 allocation union 分支。
- `api/openapi.yaml` 和生成文件增加 `proto` 枚举；`orders` 表 migration 只更新 CHECK 约束，不增加列。

## 11. MailMatch 接入

Proto 自己实现：

```go
type ProtoMailFetchPort interface {
    Fetch(ctx context.Context, scope ProtoFetchScope) ([]ProtoFetchedMessage, ProtoCursor, error)
}

type ProtoMailMatchPort interface {
    FetchOrderMail(ctx context.Context, orderNo string) (ProtoDelivery, error)
}
```

初始实现返回 `proto_mail_fetch_todo`，不制造验证码。协议确定后再选择 IMAP、Bridge、API 或其他方式；密码字段含义也必须在协议确认后固定，不能把 Microsoft OAuth/Graph/refresh token 直接改名复用。

未来完整流程：抓取 -> provider message ID 去重 -> 私有正文存储/脱敏投影 -> 项目 sender/recipient/subject/body 规则匹配 -> 订单 scope 过滤 -> 验证码提取 -> pickup token 返回。

## 12. API 和权限

### 12.1 用户 API

建议前缀：`/v1/proto/resources`。

- `GET /v1/proto/resources`：列表、分页、后端筛选、facets。
- `POST /v1/proto/resources/imports`：multipart，`file`、`errorStrategy`，必须带 `Idempotency-Key`。
- `GET /v1/proto/resources/imports/{importId}`：异步进度。
- `GET /v1/proto/resources/imports/{importId}/items`：逐行安全结果和跳过原因。
- `POST /v1/proto/resources/validations`：批量 validation。
- `POST /v1/proto/resources/{resourceId}/validate`：单资源验证。
- `POST /v1/proto/resources/{resourceId}/publish`、`/unpublish`（供应商权限）。
- `DELETE /v1/proto/resources/{resourceId}`：有活动 allocation 时拒绝。

### 12.2 管理 API

建议前缀：`/v1/admin/proto/resources`。

- 列表、facets、owner 筛选、状态筛选、日期筛选、搜索。
- 导入、导入进度和逐行结果。
- 单个/批量 validate、history、enable、disable、publish、unpublish、delete、recover。
- 详情、维护任务、订单/allocation（通过通用 allocation 查询）、mail（协议完成后）、凭据替换。
- `GET /v1/admin/proto/resources/{resourceId}/maintenance`：验证/历史维护运行的安全状态。
- 批量选择支持 `ids` 和后端 `filter`，不把全量 ID 放入请求体。
- 所有写操作要求 `Idempotency-Key`；编辑要求 `version` 乐观锁。

权限建议新增 `proto:resource/read|write|operate`，项目/订单/钱包使用已有通用权限。OperationLog 的 `operationType`、`resourceType` 使用 `proto_*` 命名，不能伪装成 Microsoft。

### 12.3 OpenAPI 生成链

修改顺序：

1. `api/openapi.yaml` 增加 Proto path/schema/enum。
2. 运行仓库已有 codegen，更新 `api/openapi.gen.go`。
3. 更新 `web/src/lib/openapi/schema.ts` 和 `web/public/openapi.json`。
4. 为 Proto API 增加安全合同测试、枚举测试、密码不泄漏测试。

不要手工把 Proto schema 拼到 Microsoft schema；独立 schema 名称使用 `Proto*`。

## 13. Web 页面

新增：

```text
web/src/pages/ProtoEmails.tsx
web/src/pages/AdminProtoEmails.tsx
web/src/pages/proto/proto-detail-sheet.tsx
web/src/pages/proto/proto-modals.tsx
web/src/pages/proto/proto-meta.tsx
web/src/lib/proto-resources-api.ts
web/src/lib/admin-proto-api.ts
```

### 13.1 用户页

复制 Microsoft 的列表、导入、批量操作、轮询和分页交互，调整为：

- 列：邮箱、状态、私有/公开、验证诊断、创建时间、操作。
- 删除 Suffix、Long-lived、Graph、显式 alias、辅助邮箱和 Microsoft 专用筛选。
- 导入说明明确显示 `邮箱----密码`。
- 导入弹窗保留粘贴/TXT、`skip|abort`、批次进度和失败行下载；不显示密码。
- `pending` 显示验证 TODO，`normal` 才显示可发布/可分配。
- 不添加“立即验证成功”或绕过验证的按钮。

### 13.2 管理员页

复制 Microsoft 管理列表和详情结构，保留：

- basic、orders、tasks、mails（协议完成后）四类 Tab。
- 导入、编辑、替换凭据、validate、enable/disable、publish/unpublish、delete/recover。
- owner、版本、状态、维护任务、安全错误和审计信息。

删除：

- Graph/token health、OAuth scope、RT refresh。
- auxiliary/binding/recovery lease。
- explicit/dot/plus alias 和 alias quota。
- Outlook suffix、long-lived、Microsoft MFA/passkey/OTP 文案。

路由：`/proto`、`/admin/proto`。导航、页面权限、i18n、空状态和移动端布局一起接入。页面 API 文件不能调用 `resources-api.ts` 的 Microsoft endpoint 或 `admin-microsoft-api.ts`。

## 14. 观测、安全和治理

- OperationLog：导入接受、验证请求、history 请求、发布、删除、恢复、凭据替换、批量命令。
- SystemLog：队列不可用、worker fence 失效、TODO 分类、临时故障和最终失败。
- TaskView：只展示 Proto task kind、状态、重试次数、资源 ID、request ID 和安全错误。
- metrics：`proto_import_*`、`proto_validation_*`、`proto_history_*`、`proto_fetch_*`、`proto_allocation_*`、`proto_order_*`，不复用 Microsoft metric label。
- 所有错误响应使用安全分类；不能把上游页面、邮件正文、密码、token、代理凭据返回给用户。
- 资源列表、导出、数据库查询和审计日志都默认不返回密码。
- MinIO source/failure artifact 使用 private bucket、短期访问授权和 retention policy。

## 15. 测试和验收

### 15.1 单元和契约

- parser 只接受两段，拒绝 Microsoft 的 3/4/5 段格式。
- 邮箱规范化、空密码、额外分隔符、BOM、超长行。
- 文件内/数据库内重复、deleted 恢复、skip/abort。
- 导入幂等、claim recovery、重复 task no-op。
- `pending -> validating -> pending` TODO 验证，不会写 `normal`。
- generation/revision fence 拒绝旧任务写回。
- 状态过滤保证 TODO 资源不进入库存。
- Proto task payload 不含 password/token/raw content。
- Proto API 响应和日志不泄漏密码。
- Proto queue topology 恰好一个 worker tier。
- allocation 项目隔离、active 唯一约束、释放和订单 guard。

### 15.2 MySQL/Redis 集成

- 并发同邮箱导入只有一个资源。
- deleted 恢复和新导入并发不覆盖新凭据。
- worker 崩溃后 import/validation/history 可恢复。
- Redis dispatcher 游标和 lease 过期恢复。
- 管理员禁用/换凭据与 worker 并发时旧结果 no-op。
- Proto 表不产生任何 Microsoft 表写入。

### 15.3 前端

- `ProtoEmails` 和 `AdminProtoEmails` 页面合同测试。
- import modal 只显示 `邮箱----密码`。
- 删除 Microsoft 字段后没有空列、错误筛选或死按钮。
- 状态、批量操作、轮询、移动端和权限失败路径。

### 15.4 验收门槛

在真实协议未接入前：

1. 可以上传并异步完成导入。
2. 可以查看每行结果和安全失败原因。
3. 可以发起验证并看到 TODO，资源仍是 `pending`。
4. 库存、分配、购买和接码不会领取 Proto 资源。
5. `email_resources.id`、`proto_resources.id`、`proto_allocations.resource_id` 和订单 `order_no` 关联一致。
6. `orders` 表没有新增任何 Proto 专属列；Microsoft 既有测试、迁移和运行路径不变。

真实协议接入后再增加：验证成功、历史识别、normal 库存、code/purchase 履约和退款验收。

## 16. 分阶段实施

### P0：冻结边界和契约

- 确认 `proto` 是否是 Proton Mail，以及账号密码是网页登录密码、Bridge 密码还是应用密码。
- 确认是否支持 2FA、IMAP/Bridge/API、历史邮件读取、购买交付、公开出售、别名和自定义域名。
- 合并能力矩阵，冻结不复制 Microsoft binding/alias/Graph/token 的决定。

### P1：独立资源和导入

- 新迁移、`email_resources.type='proto'` 注册、Proto domain/model/repository，以及通用 `product_type/allocation_type` 的 `proto` CHECK 扩展。
- private artifact、异步 import、skip/abort、幂等、恢复和安全错误。
- 用户/管理员导入 API、OpenAPI、页面和队列。
- 交付 `pending` 资源列表；不接真实验证。

### P2：验证 TODO 和管理闭环

- 独立 validation dispatcher、Redis cursor、fence、维护任务和 TaskView。
- TODO adapter 明确回 `pending`，不能进入库存。
- 管理员编辑、换凭据、启停、删除、恢复、批量命令和审计。

### P3：历史识别 TODO 和商品骨架

- 独立 history task、状态和 TODO 结果。
- `ProductTypeProto`、项目商品校验、库存为零的 Proto inventory API。
- 用户/管理员页面的商品和库存展示。

### P4：真实协议和邮件履约

- 先写协议契约测试，再实现 `ProtoValidationPort`、`ProtoMailFetchPort` 和 `ProtoHistoryScanPort`。
- 实现消息去重、项目规则匹配、验证码提取、订单 token 和 after-sale。
- 只在真实验证证据和历史识别成功后开放 normal、allocation、code/purchase。

### P5：运营和扩展

- dashboard、库存刷新、SLO、告警、限流、协议 cursor、重试和密钥轮换。
- 只有确认 Proto 支持 alias/辅助邮箱/多地址后，才新增对应事实表和任务；不提前复制 Microsoft 表。

## 17. 发布、回滚和故障处理

发布顺序：迁移 -> 后端模块 -> worker queue tier -> API/OpenAPI -> web -> feature flag 开启导入 -> 验收 -> 开启真实协议商品。

回滚顺序：先关闭 Proto 写入口和商品，再停止 Proto worker，保留已写入的 Proto 表和 artifact，最后回滚 web/API。不要回滚或修改 Microsoft migration；Proto down migration 只有在所有 Proto 资源、导入、allocation、订单和 artifact 均清空后才允许执行，默认不提供线上 destructive down。

故障规则：

- MinIO/Redis/DB 短暂不可用：保留 durable 状态，重试或 dispatcher 恢复。
- Proto 协议临时错误：回 `pending`，不自动 `abnormal`。
- 凭据确定性失败：`abnormal`，等待管理员处理。
- TODO：安全可观察、可重试、不会制造库存和订单事实。
