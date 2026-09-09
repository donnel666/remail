# Proto 邮箱：本地实现审查与修正实施方案

日期：2026-09-07。状态：分析和实施计划，尚未执行业务代码修正。

后续已按本计划实施修正，结果见 [26-proto-mailbox-implementation.md](26-proto-mailbox-implementation.md)。下文保留修正前的审查快照。

本方案依据当前工作树的实际代码、有效迁移和三路并行分析编写。当前已有未提交的 `internal/proto`、迁移、页面及公共模块改动；它们是待审查实现，不能全部当作已完成能力。原有 `23`、`24` 两份 Proto 文档保留，存在冲突的设计决定以本方案和用户本次明确要求为准。

## 1. 确定的实现边界

用户要求：导入格式固定为 `邮箱----密码`；参考微软全套机制；该复制的资源业务和页面独立复制，不影响成熟模块；下单、分配使用现有统一入口；资源 ID 统一，订单表不新增类型专属列；真实验证和旧项目识别暂用 TODO；其余缺失能力必须明确列出。

采用现有四类邮箱已经使用的集成方式：

```text
Proto 用户/管理资源 API、页面
  └─ Proto 自有导入、凭据、状态、维护、验证、历史扫描和协议实现

现有项目/商品、工作台、OpenAPI、Bot
  └─ 现有 Trade.Checkout
       └─ 现有 Alloc.Allocate
            ├─ Microsoft 分支
            ├─ Domain 分支
            ├─ Gmail 分支
            ├─ iCloud 分支
            └─ 新增 Proto 分支 → Proto 独立分配实现/表
       └─ 原订单、钱包、Token、激活、退款、售后

现有 MailMatch 取码/邮件入口
  └─ Proto 独立抓信适配 → 公共消息、匹配和订单履约契约
```

| 边界 | 做法 | 明确不做 |
|---|---|---|
| Proto 资源业务 | 从微软相应职责复制，放入 `internal/proto`，独立修改 | 不调用微软 validator、凭据服务、history scope、别名逻辑；不读写微软私有表 |
| Proto Web 业务页面 | 复制微软页面及业务子组件，独立类型和 API | 不把微软页面改成 `provider` 参数页面；不直接引用 `admin-microsoft` 业务组件 |
| 公共商业入口 | 原入口增加类型、分派、查询和响应映射 | 不建 `proto_orders`、Proto 钱包、第二个 checkout 或第二套分配入口 |
| 公共基础设施 | 使用现有 IAM、文件存储、Redis/Asynq、背景负载控制、日志和通用 UI | 不另造 Provider 注册框架、队列系统或资源根表 |

允许在 `internal/alloc` 内新增 `proto_*.go` 承载分配实现，公共入口调用它；文件属于公共模块不等于业务互相耦合。新 Proto 代码不调用微软分配分支，老分支不因这次接入重写。

这一取舍接受少量独立业务代码重复，以降低修改微软业务造成回归的风险；公共订单和分配契约继续一致。无需为了减少这些重复对成熟模块做抽象重构。

## 2. 四类邮箱的现有集成方式与统一 ID

| 类型 | 物理资源根/子表 | 逻辑收件地址 | 独立分配事实 |
|---|---|---|---|
| 微软 | `email_resources` + `microsoft_resources` | 主邮箱、显式别名、dot、plus | `microsoft_allocations` |
| 域名 | `email_resources` + `domain_resources` | `generated_mailboxes` | `domain_allocations` |
| Gmail | `email_resources` + `gmail_resources` | main/dot/plus；有本地来源标记 | `gmail_allocations` |
| iCloud | `email_resources` + `icloud_resources` | `icloud_aliases` | `icloud_allocations` |
| Proto 目标 | `email_resources` + `proto_resources` | 本期主邮箱 | `proto_allocations` |

证据：`migrations/00001_initial.sql:172`、`internal/core/infra/resource_repo.go:809`、`internal/gmail/model.go:94`、`internal/gmail/resource_import.go:656`、`internal/icloud/module.go:162`、`internal/icloud/import.go:850`。

`email_resources.id` 已是跨邮箱类型的 MySQL 自增全局资源 ID。Proto 在同一个事务中先插根表，再以所得 ID 插入子表。当前 Proto 这部分方向正确，应修复事务细节，不应另建 ID 体系。

```text
orders.order_no + allocation_type
  → allocation_order_guards(order_no, type)
  → 对应 *_allocations.order_no
  → *_allocations.resource_id
  → email_resources.id
  → 对应资源子表同主键行
```

订单表当前没有持久化 `allocation_id`；API 的 `allocationId` 是按订单号查询后补充的投影。旧的 `orders.microsoft_alloc_id/domain_alloc_id` 已由 `00086` 迁移移除。证据：`internal/trade/domain/order.go:91`、`internal/trade/app/ports.go:1981`、`migrations/00086_drop_legacy_order_allocation_ids.sql:158`。

实施约束：

- 不新增 `orders.proto_alloc_id`、`proto_resource_id`、`proto_password`，也不为本次任务新增订单 `resource_id` 列。
- `resourceId` 是全局物理资源身份；`allocationId` 是类型内分配记录 ID，跨类型引用同时带 `allocationType`；alias/mailbox ID 又是另一种身份。
- `proto_resources.id` 不自增；`(id,resource_type)` 外键到 `email_resources(id,type)`。
- `proto_allocations.order_no` 唯一，并通过 `(order_no,guard_type)` 引用公共 Guard。
- 资源列表、导入结果、任务、日志、邮件、库存和分配都使用同一个根 ID；邮箱字符串不能代替资源 ID。
- 订单详情、退款、凭证仍按订单号操作。Proto 详情查关联订单使用公共 `/v1/admin/allocations?type=proto&resourceId=...`。

## 3. 当前本地实现的实际问题

下表是代码审查发现，不将尚未复现的并发路径描述为已发生的生产故障。

| 优先级 | 当前问题与影响 | 修正位置/方向 |
|---|---|---|
| 高 | 命令先独立插入 `accepted`；业务失败/崩溃后，同 key 被当作已经执行而跳过 | `proto/infra/service.go:97`、`proto/api/handler.go:463`；复制微软事务内 reserve → 业务变更 → 审计 → complete，准确区分处理中/已完成/失败 |
| 高 | 幂等查询作用域是 operator+key，数据库唯一键却包含 resource+command，竞争行为不一致 | `00136_proto_mailbox.sql:144`；按复制后的命令契约统一查询、唯一键和请求指纹 |
| 高 | 导入先提交 pending 资源，再由导入 worker 逐个领取验证；崩溃/队列丢失窗口没有 pending dispatcher 接手 | `proto/api/tasks.go:36`、`proto/infra/dispatch.go:23`；复制 pending → enqueue → CAS validating 的完整交接 |
| 高 | Redis enqueue 失败被标记为验证 TODO 并退回 pending，而恢复逻辑只扫 validating | `proto/api/handler.go:499`；区分基础设施失败、未实现结果和真实凭据失败 |
| 高 | 历史 TODO 保持 identifying，但调度器不看同代次维护任务终态，周期重复调度、写日志和版本 | `proto/infra/service.go:322`、`dispatch.go:57`；当前代次 TODO 结束后稳定停留，显式重试再开新代次 |
| 高 | 导入单个长事务中先写 root 后写 child，子表失败被吞掉时可能提交孤立 root，计数还可能同时 imported/failed | `proto/infra/imports.go:153`、`:184`、`service.go:215`；复制微软预校验、分批原子提交和幂等明细机制 |
| 高 | 导入表有 claim_token，但执行模型/worker 没有相应 CAS；dispatcher 可能把已完成状态回写 queued | `00136:98`、`proto/infra/imports.go:293`；真正实施 generation、claim、状态前置条件 |
| 高 | 管理编辑、凭据替换、启停等请求没有 expectedVersion；子表 version 增长却未提供并发保护 | `proto/api/handler.go:733`、Proto OpenAPI DTO；沿微软用根表 version，冲突返回 409 |
| 高 | 普通命令根→子锁，验证/历史结果子→根锁，存在锁序反转 | `proto/infra/query.go:374`、`service.go:231`、`:256`；与统一分配保持根→子顺序 |
| 高 | 商品/分配已接受 Proto，MailMatch 尚无对应作用域、类型与收件分派 | `mailmatch/domain/message.go:13`、`mailmatch/infra/repo.go:460`；补统一取码入口接入，TODO 资源不可售 |
| 中 | `for_sale` 强绑 normal，验证/历史反复清零供货意愿，偏离微软机制 | `00136:80`、`proto/infra/service.go:268`、`:347`；供货意愿和健康状态分开，库存同时检查 |
| 中 | 改邮箱仍保留旧密码，不递增 credential revision | `proto/infra/query.go:262`；复制微软身份变更失效规则；旧账户密码不能继续用于新邮箱 |
| 中 | enable/recover 和 disabled/有活动订单行为没有完整沿用微软；自助下架/公开资源删除范围也与微软不同 | 按微软用户/管理员命令契约分别复制，删除/改身份与已有分配保护一致 |
| 中 | 批量是 HTTP 内循环 IDs，缺 filter 全选、游标、快照边界、token lease、原因聚合 | `proto/api/handler.go:529`、`:870`；复制微软异步批量机制，独立命名空间 |
| 中 | 资源缺后缀/质量/分桶、keyset 分页和独立维度 facets；失败文件字段存在但未生成制品 | `proto/infra/query.go:14`、`:112`、`imports.go`；复制对应列表与导入机制 |
| 中 | multipart 先解析 PostForm/FormFile 才限文件内容，缺整个请求的先行大小限制 | `proto/api/handler.go:244`；复制微软 `MaxBytesReader` 和有限读取 |
| 中 | HasTable 热路径探测为缺表返回空库存/跳过维护；维护状态写入错误又被忽略 | `proto/infra/maintenance.go:52`、`proto/api/tasks.go:95`、Alloc Proto 查询；遵守先迁移启动，结构错误明确失败 |
| 中 | 两个 Web 页面是简版，未复制微软的完整页面结构；导入失败也可能弹成功 | `ProtoEmails.tsx:69`、`AdminProtoEmails.tsx:48`；从微软独立复制后裁剪协议字段 |
| 中 | Proto 关联分配查询错误被显示成没有记录；API 路径用 `as never` 绕过检查 | `AdminProtoEmails.tsx:53`、`proto-api.ts:189`；保留错误状态、完整生成契约 |
| 中 | 公共商品编辑让 Proto 显示可编辑主邮箱权重，却固定提交 1 | `AdminProjects.tsx:276`、`:529`、`:728`；保持旧邮箱表达式，Proto 单独固定 1/0/0 |

以下改动本身属于正确的公共接入，不应整体撤销：商品/分配/订单 Proto 枚举、公共分配 switch、按订单查询和释放分支、统计 UNION、Router 装配、菜单、配置项、队列注册。

特别说明：Trade 恢复查询追加 `LEFT JOIN proto_allocations` 与现有 Gmail/iCloud 接法一致。`cmd/server/main.go:59` 在 Router/Worker 前运行迁移，不能把这个 JOIN 本身判定为“正常部署会破坏旧订单”。应保持迁移顺序和一致的数据库前提，不在各处加跳表兼容。

## 4. 数据库修正设计

以有效迁移后的 schema 为基准，不能机械复制最早 migration 的旧表。

| 表/状态 | 目标职责 |
|---|---|
| `email_resources` | 沿用全局 ID、归属、根 version；仅扩 type 允许值 |
| `proto_resources` | Proto 独立资源、密码、凭据版本、验证代次、健康状态、供货意愿、后缀、质量、分桶、分配时间 |
| `proto_resource_imports` | 导入事实、私有原件/失败制品 key、策略/指纹、计数、generation、dispatch/claim 和时间 |
| `proto_resource_import_items` | 行号、结果/原因、安全错误、根资源 ID；唯一 import+line |
| `proto_command_receipts` | Proto 管理命令事务内幂等，复制微软 reservation/result 契约 |
| `proto_maintenance_runs` | 保留现有独立任务可观察事实，参考 Gmail；不是第二套验证调度事实，终态限制重复执行 |
| `proto_project_history_scan_states` | 独立项目反扫进度，按 project_id 管理代次、游标、重试、计数和时间；不能共写微软的 projects.history_scan_* |
| `proto_allocations` | 独立分配历史，引用统一资源和公共项目商品/Guard；统一 Alloc 读写 |
| `proto_resource_fetch_states` | 若沿资源收件状态模式复制，则保存 Proto 抓取/手动历史操作的独立状态和 revision fence；不会覆盖微软当前抓取状态 |
| 公共 MailMatch 消息/投影表 | 沿 Gmail/iCloud 接法扩 Proto 类型、查询和映射，保留公共去重/投递事实 |

不重新建立已废弃的 `resource_validation_jobs/batches` 和管理批量 command 表。微软验证任务/批量游标已经迁到 Redis；证据是 `00022`、`00031`。也不照搬旧 `project_history_scan_jobs`：`00032` 已将其替换。

`microsoft_resource_project_matches` 是旧兼容表，当前历史识别通过 Alloc/Trade 写历史使用事实后清理它。Proto 不必先造一张平行 matches 表作为分配依据；沿同样历史分配事实设计，确有独立证据查询需求时才增加必要存储。

`proto_resources` 核心字段：`id/resource_type/email_address/email_domain/password/credential_revision/credential_updated_at/status/validation_generation/validation_failures/last_safe_error/quality_score/alloc_bucket/for_sale/last_allocated_at/created_at/updated_at`。归属和并发版本以根表为权威；不要无必要维护第二套 owner/version。保留冗余列时必须有明确事务与外键一致性约束，历史分配中的归属快照也不能妨碍合法恢复。

索引按微软当前查询形态复制裁剪：邮箱唯一；根类型外键；状态调度 `(status,updated_at,id)`；供给/状态、后缀、分桶/最近分配/质量；导入 dispatch/generation 索引；导入行唯一；分配 order_no 唯一、项目资源历史查询、资源状态查询、商品项目复合外键。

分桶参考 `00046` 的 2048 桶与 `SMALLINT UNSIGNED`，不是旧 256 桶。Proto 主邮箱同项目活动唯一约束和历史排除语义沿用微软主邮箱，跨不同项目的使用不能误改为全局永久独占。

通用表只扩展必要 CHECK/允许值：`email_resources`、`project_products`、`orders`、`allocation_order_guards`，以及接入时涉及的 MailMatch 类型约束；不增订单资源列，不改旧 provider 数据。迁移已应用则追加修正 migration；未应用草稿才可修草稿。回滚有 Proto 业务数据时应拒绝直接删表。

## 5. 导入、状态机和异步任务

导入流程完整复制：

```text
权限/owner/请求大小/幂等检查
 → 私有文件原件 + processing/pending 导入事实
 → Proto 导入 dispatcher
 → generation/claim CAS 的 worker
 → 两段解析、去重、skip/abort 预检查
 → 分批短事务写 root + Proto 子表 + 行结果 + 进度
 → 完成/安全失败制品
 → 唤醒 Proto 验证 dispatcher（资源已经是 pending）
```

两段格式：邮箱规范化；密码保留有效空白；兼容 BOM/CRLF/空行；拒绝空密码、非法邮箱、超限/无效 UTF-8 和额外 `----` 字段；失败输出不包含原始密码行。前后端规则一致。已删除邮箱重导入保留资源 ID、更新凭据代次并恢复为私有；同账号非删除重复按微软策略处理。

微软先预校验，再按每 1000 行短事务提交；abort 不等于整个超大文件全局事务。实现与 UI 必须说明部分已提交批次的真实结果，不能用“全部失败”掩盖已经导入的资源。

目标资源状态：

```text
导入/显式重试/启用/恢复 → pending
pending → validating                 入队与 CAS 交接完成
validating → identifying             真实验证成功，安排历史识别
identifying → normal                 历史事实提交完成
validating → pending / abnormal      临时失败重试 / 确定失败或业务重试耗尽
非 deleted → disabled                管理员禁用，阻止新分配
disabled → pending                   启用
deleted → pending                    恢复，保持私有
```

规则必须同时成立：

- `for_sale` 是供货意愿；健康状态决定资格。公有可用要求 normal+for_sale，私有可用要求 normal+买家归属+私有；非 normal 不因供货标记为真而有库存。
- 显式重验创建新 generation；换凭据增加 revision/generation；改邮箱清旧凭据。结果提交复核 status/generation/revision，并固定根→子锁序。
- disable 与 delete 不是同一种操作。禁用阻止新分配，沿微软已有订单语义；删除/改身份检查活动分配。enable 只接 disabled，recover 只接 deleted。
- TODO 不是凭据失败，也不是成功。验证 TODO 结束后保持 pending，识别 TODO 保持 identifying，维护结果可标记现有 `uncertain` 并记录明确原因；同代次 dispatcher 不重复领取已经因 TODO 结束的执行。用户重新执行时产生新代次。
- 不生成假历史匹配、历史订单、0 元账务，也不强制 normal。完成真实协议前，TODO 资源的自有/公有可用库存均为 0，所有统一下单入口一致处理。

Proto 队列和 Redis key 独立命名；Asynq server、后台负载 gate 和监控继续共用。任务至少覆盖 import、validation dispatcher/worker、validation batch、admin bulk、validated history、project history dispatcher/worker，以及完整收件接入所需任务。任务 payload 只带 ID、代次、revision、claim/cursor 和 request ID，不带密码或源文件正文。

复制的是实际执行保障：

- enqueue/状态 CAS 双向交接；worker 早于状态更新到达时正确补做领取。
- 基础设施重试与业务失败次数分开；最终失败有释放/终态更新。
- 批量 IDs/filter、固定 throughID 边界、afterID 游标、TTL token lease、compare-renew/delete、完成/跳过/失败原因统计。
- task timeout、去重周期、延迟和重试按任务类别配置；dispatcher 采用短周期与短超时，不把所有任务统一为 30 分钟。
- provider 自己的并发限制和 `BackgroundExecutionGate`，启动唤醒与退出清理。
- Redis 暂时不可用、导入提交后进程退出、过期任务、新凭据覆盖等恢复路径明确可测。

不能无依据宣称微软已有“Redis 全部清空后所有 running/identifying 自动恢复”。Proto 自己的异常恢复应依持久资源/任务事实明确实现，不能仅复制一句注释。

## 6. 旧项目识别的两条链路

1. 资源验证成功后，按该资源全历史识别项目使用情况。
2. 项目新增、审批或规则修改后，按该项目反扫已有资源。

另包含管理员单项、选中项和按筛选全量重识别。第二条不能漏，也不能复用微软在 `projects.history_scan_*` 上的进度。

TODO 只替代真实协议/识别结果取得；请求入口、独立任务、状态观察、代次、取消、重试和历史写入契约要设计完整。真实实现时在事务外抓取邮件，提交前复核凭据 revision 和项目规则快照，再通过 Proto 历史分配分支及现有 Trade 历史入口产生 released allocation/历史订单事实。提交成功才允许 identifying→normal。没有项目 scope 也应按真实识别流程给出完整结果。

参考：`core/app/validation.go:572`、`core/app/project.go:632`、`mailmatch/app/project_history_scan.go:210`、`mailmatch/infra/history_match_repo.go:34`。微软 scope 内固定 `pp.type='microsoft'`，必须复制为 Proto 自己的 scope，不能传一个名称就复用微软实现。

## 7. 公共模块的最小完整接入清单

| 模块 | 必须覆盖的接入点 | 保持的稳定机制 |
|---|---|---|
| Core 项目商品 | 类型、申请/重提/管理员创建审批编辑、批量商品、默认价格/供货价、code/purchase 开关、时间窗口、facets、Proto 反扫 hook | 商品 ID 保留、逻辑禁用、项目授权/上下架、旧订单快照 |
| Alloc | 统一 Allocate 的 Proto 分支；候选/根锁/复查/Guard/创建；按订单/资源/项目/类型查找；批量查询、活动地址、释放 | 原事务与重试、根锁顺序、其他四类分配算法 |
| 库存 | 私有/公有供给、供应者状态角色、同项目历史排除、跨项目使用、总量与可用量、工作台/商品批量库存 | 现有统一库存读模型和刷新机制；code/purchase 不把同一库存翻倍 |
| Trade | 类型/selector、单笔和批量下单、Console/API Key/Bot、订单投影/facets、恢复、清理 | 原订单状态机、唯一幂等、扣款/退款、Token、激活/质保/超时 |
| MailMatch | Proto 类型/约束；order/单笔 pickup/批量 pickup scope；exact recipient；抓信 Port；消息去重、投影、delivery head、重放 | 原取码入口、订单Token授权、项目规则、消息存储与投递契约 |
| 管理邮件/诊断 | Proto 列表/详情/手动收件/匹配诊断、资源查订单；Bot 诊断表映射和错误分类 | 通用权限和安全正文展示，原 provider 分支 |
| 永久不可用处理 | Proto 凭据失效判定和资源变更，按 Proto 资源找受影响订单，调用公共退款/释放/Token 禁用 | 不调用微软专属退款函数，不改统一退款状态机 |
| Billing/售后 | 供应商分配数/成功率 UNION 补 Proto；售后订单与邮件证据能关联 | 钱包、流水、工单和退款系统复用，不新增 Proto 财务/售后表 |
| 治理 | OperationLog/SystemLog 的 Proto 事实；TaskView source/bizType/状态/详情；必要的保留清理 | 公共日志/任务入口和权限 |
| Dashboard/监控 | 总量/可用量、订单/取码/激活统计、成功率耗时、队列/资源指标标签 | 公共页面及聚合入口，避免单独的 Proto 监控页 |
| 设置/定价 | 独立默认售价/供货价/服务开关、Proto 产品倍率、队列权重/容量、导入批量限制 | 现有配置验证与订单价格快照机制 |
| 装配/API | 新 Proto Module/Routes/Handlers、背景 gate、独立 history/fetch adapter；公共枚举 | 原认证、CSRF、资源权限、限流和 Router 生命周期 |
| OpenAPI/Bot | 公共项目/订单/批量订单/库存接入；资源管理开放接口按微软能力单列 Proto 适配，核对真实权限范围 | 不把 `/open/resources` 微软 handler 直接改指 Proto；不另建商业入口 |
| 生成契约 | YAML、Go 生成、Web schema、公共 OpenAPI JSON、前端类型和文案同步 | 使用现有生成流程，不用 cast 隐藏不匹配 |

当前已明确遗漏：MailMatch Proto 全链、项目反扫 hook、永久失效补偿、Billing 供应商统计、Proto 产品倍率。证据分别在 `mailmatch/infra/repo.go:460`、`core/app/project.go:632`、`trade/app/ports.go:2230`、`billing/infra/repo.go:267`、`systemsettings/runtimeconfig/defaults.go:13`。

不把文档愿景当作代码：现有供应商冻结结算完整编排未在此轮代码追踪中找到；Proto 要对齐现有真实账务能力，不借这次工作新建整套结算系统。

## 8. Web 独立复制清单

| 微软源文件/目录 | Proto 文件/目录 |
|---|---|
| `pages/MicrosoftEmails.tsx` | `pages/ProtoEmails.tsx` |
| `pages/AdminMicrosoftEmails.tsx` | `pages/AdminProtoEmails.tsx` |
| `resources/import-microsoft-emails-modal.tsx` | `resources/import-proto-emails-modal.tsx` |
| `resources/microsoft-import-preprocess.ts` | `resources/proto-import-preprocess.ts` |
| `resources/model.ts` 中微软业务模型与状态展示 | 独立 `resources/proto-model.ts` 及必要状态组件 |
| `admin-microsoft/admin-microsoft-types.ts` | `admin-proto/admin-proto-types.ts` |
| `admin-microsoft/microsoft-meta.tsx` | `admin-proto/proto-meta.tsx` |
| `admin-microsoft/microsoft-modals.tsx` | `admin-proto/proto-modals.tsx` |
| `admin-microsoft/microsoft-maintenance-modal.tsx` | `admin-proto/proto-maintenance-modal.tsx` |
| `admin-microsoft/microsoft-bulk-maintenance-modal.tsx` | `admin-proto/proto-bulk-maintenance-modal.tsx` |
| `admin-microsoft/microsoft-detail-sheet.tsx` | `admin-proto/proto-detail-sheet.tsx` |
| `admin-microsoft/use-admin-microsoft-allocation-page.ts` | `admin-proto/use-admin-proto-allocation-page.ts` |
| `lib/resources-api.ts` 中微软方法 | `lib/proto-api.ts` 用户资源方法 |
| `lib/admin-microsoft-api.ts` | `lib/admin-proto-api.ts` |

继续使用通用 CardPro/CardTable、日期筛选、分页 hook、用户选择器、认证、供应商申请、公共 API client。已有 Gmail/iCloud 对微软展示组件的引用不构成本次继续耦合的理由，也不要求顺便重构旧页面。

用户页面：搜索/后缀/日期/状态/公开私有筛选、facets、分块游标分页、共享页大小、选中和筛选全量操作、供应商门槛、导入弹窗、单项/批量验证、发布和私有资源删除、加载/错误/空状态、移动端。

管理员页面：归属用户筛选选择、完整编辑/凭据替换、version 并发冲突、启停/发布/私有化/删除/恢复、IDs/filter 批量、导入进度和结果分页。详情有基本信息、关联订单、任务、邮件四部分；只剔除明确微软专属的内容。

必须复制的交互细节：

- 用户页和管理页按各自微软基线保留选择行为；用户页可跨翻页保留已选，管理页翻页清空。不要统一改造旧行为。
- 微软页面没有交互式列排序器；复制稳定后端顺序和游标规则，不把不存在的功能说成已具备。
- 导入失败与部分成功分别反馈，不能只凭轮询结束就弹成功；结果明细可翻页，后台继续与再次进入可查询。
- 用户导入按原人机验证流程；管理员按权限。提交禁重、AbortSignal、过时响应保护、切换资源取消旧请求。
- 关联订单查询失败显示错误；不能显示“无分配”。任务活跃时轮询，离开停止。
- 邮件正文显式打开；HTML 保持 sandbox iframe，不直接注入页面。
- TODO 用准确状态和原因展示，不显示验证通过/无历史/可用库存。
- 中英文覆盖全部字段、状态、错误、结果及空状态；API/日志均不回显原始密码。

公共 Web 仅追加 Proto：`App.tsx`、navigation、Projects/apply-project-modal/AdminProjects、Dashboard/workbench、AdminOrders、定价/后台任务设置、管理仪表盘和 i18n。监控和日志页面已有动态类型/队列渲染，直接接入事实即可。

Proto 主邮箱权重固定 `1/0/0`，不要给用户一个修改后被忽略的输入。Gmail 现有权重表达式维持原样，Proto 单独分支处理。

## 9. 微软有、Proto 当前没有或不能直接复制的能力

| 微软能力 | Proto 本期处理与差异说明 |
|---|---|
| client ID、Refresh Token、OAuth scope、RT 过期/刷新 | 不复制微软字段和协议实现；两段导入不要求这些值 |
| Graph/微软 IMAP fallback、网页登录 ACL | 不提供同名虚假能力；真实 Proto 协议独立接入 |
| 辅助邮箱、proof mask、恢复/OTP、恢复租约 | 当前无 Proto 对应事实；删微软实现与专属页签，明确未支持 |
| 显式 alias 创建/同步/配额/对账 | 本期主邮箱，无对应 Proto 远端协议；不复制微软别名子系统 |
| dot/plus 派生、微软 plus 每日配额 | 本期不宣称支持，主邮箱分配不生成这些地址；不据此断言 Proto 厂商永久不支持别名 |
| Outlook/Hotmail 白名单、微软项目后缀黑名单 | 不照搬；Proto 后缀来自实际导入地址，若需限制用 Proto 自己的规则 |
| 邮箱后缀列、后缀筛选、质量分、供货/归属、分桶 | 保留通用机制，不能因本地简版没写就删除 |
| 微软长效/短效 | 需区分运营分类与微软凭据寿命。不能照搬 RT 派生判断，也不能仅以两段导入就认定没有运营分类；列为明确差异，在 Proto 等价业务未确定前不展示虚假的寿命保证 |
| 代理池 | 基础设施可用；不照搬微软协议的代理绑定/恢复策略，按 Proto 真实协议需要接入 |
| 真实验证、真实旧项目识别 | 用户明确允许 TODO；请求、队列、状态和观察面保留 |
| 真实收信/取码 | 当前仓库未发现 Proto 协议适配，属于必须解决的事实缺口；不是用户授权新增的第三个 TODO。完整方案保留独立适配和公共履约接入，开售前完成真实验证 |
| purchase 交付 | 邮箱+项目绑定的系统收件 Token/提取链接+激活/质保语义。所有邮箱资源敏感数据对外只写不读，任何模式都不得交付密码、2FA、应用密码或上游令牌 |

`proto` 暂作为确定的产品键；不凭名称假定某个厂商、Bridge、IMAP/API 协议已经可用。收信协议资料是后续真实协议实现所需输入，不影响当前完成资源/任务/公共接入设计。

原 `docs/23` 中“只做导入，其余履约/运营以后做”、向买方返回 Proto 密码、把整套商业服务都归 Proto 私有的描述需要修正。原 `docs/24` 中笼统删除后缀/长短效的处理也不足，应采用上表逐项边界。

## 10. 实施顺序与完成标准

1. **收敛草稿和数据库边界。** 保留已经正确的根 ID、统一入口、类型注册；清理无关 Gmail/公共表达式改写；修正 Proto 事务/幂等/版本/归属、状态和索引设计。迁移按是否已应用决定追加还是修草稿。
2. **复制完整资源闭环。** 两段导入、私有制品、分批处理、恢复、CRUD、版本命令、异步 IDs/filter 批量、日志和安全查询。完成真正的 dispatcher/claim，不以字段存在代替机制。
3. **验证和历史 TODO 闭环。** 独立队列和代次；导入可唤醒；手动重试可用；TODO 有终态观察且不循环；资源和项目两个历史入口完整。没有成功证据时无可用库存。
4. **补齐统一商业和邮件接入。** 项目商品/定价/库存/分配/订单/Token/补偿/售后/统计逐项完成；Proto 抓信适配进入公共 MailMatch。缺协议时准确标记尚不能真实履约，不能宣称全链上线。
5. **复制 Web 和同步契约。** 用户/管理完整页面及拆分组件，保留公共工作台下单；YAML/生成类型/API client/国际化一致。
6. **做针对性验收。** 用户已明确不需要完整测试；只运行与改动直接相关的检查和必要的旧入口回归，不因新增 Proto 扩大到完整测试套件。

完成定义分开：机制实现完成，要求除允许的两个 TODO 外，上述流程/页面/公共适配完整；真实售卖可用，要求真实验证、历史识别和收信适配得到实际验证。前一个状态不能冒充后一个。

## 11. 针对性验收清单

- 根 ID/子表同主键、跨类型隔离、根子事务原子性；订单无新增 provider 列；有业务数据时不可直接破坏性回滚。
- 两段解析、密码空白、BOM/CRLF、长度/UTF-8、重复/已删除恢复、skip/abort、分批重试、行结果计数和脱敏失败制品。
- 同幂等键并发、同 key 不同 payload、业务失败后恢复、expectedVersion 冲突、根→子锁序、邮箱/凭据变更后旧任务无效。
- 导入提交后退出、enqueue 失败、CAS 竞争、旧 claim、批量游标/筛选边界、重试耗尽；TODO 不假成功、不循环重跑。
- 供货意愿跨验证保留；disabled/deleted/未识别资源无库存；用户/管理员权限、公开资源与活动订单保护沿微软语义。
- 同项目不重复分配、跨项目按微软规则复用、释放保留历史、公有/私有库存和 Code/Purchase 不重复计算。
- 统一单笔/批量/API Key/Bot 入口、幂等扣款、余额不足、Token/激活失败、退款和释放补偿；旧订单使用快照。
- 项目商品 ID 不重建；规则变更有 Proto 独立反扫；旧资源识别不能写微软进度和历史表。
- 真实收信接入后的单笔/批量取码、Token/资源/收件人/项目隔离、消息去重、投影重放、永久不可用补偿。
- Web 导入失败反馈、进度/分页、版本冲突、批量范围、请求取消、关联查询错误、TODO 状态、HTML 隔离和中英文。
- 原四类邮箱的直接相关分配/库存/项目编辑契约回归；不为 Proto 改写原算法。

## 12. 本轮已做验证与环境清理

业务代码、已有 migration 和原有 Proto 文档均未修改。本轮仅新增本审查计划。

已执行的检查：

- `go test ./... -run '^$'`：全仓编译检查通过，未运行完整测试套件。
- Proto 全包，以及 Alloc/Core/Trade/Dashboard/Governance 的 app、Platform、RuntimeConfig 相关单元测试通过。
- 隔离 MySQL 中：Proto migration/类型分配约束，Microsoft 主邮箱并发分配、Gmail 统一库存、iCloud 库存、Domain 并发分配五个针对性测试通过。
- `pnpm typecheck` 通过。
- 五个相关前端测试文件合计 13 通过、1 失败；失败为 `project-gmail-variant-contract.test.ts:23` 的源码表达式匹配，加入 Proto 后改写了 Gmail 原表达式。它说明已有契约检查未通过，不能单凭此断定 Gmail 运行时行为损坏；Proto 权重输入与固定提交不一致另有代码证据。

这些检查不覆盖上文所有并发、失败恢复和真实协议缺口，不能用“能编译/单测通过”宣布功能完整。

用户要求不跑完整测试并清理全部本地容器、volumes、自定义网络后，没有继续扩大测试。已核对本地 Docker `default` context（`unix:///var/run/docker.sock`）：容器 0、volumes 0、自定义网络 0；测试容器已自动清理，仅剩内置 bridge/host/none 网络。无需进一步删除。
