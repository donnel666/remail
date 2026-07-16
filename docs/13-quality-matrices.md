# ReMail Go 版设计、测试、验收矩阵

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-07-03 | V1.1 | Codex | 补充同步/异步边界验收项；仅增加异步任务审查维度，不改变既有矩阵策略。 |
| 2026-07-03 | V1.2 | Codex | 纠正代理错误隔离矩阵：运行期和检测失败只置 `abnormal`，`disabled` 仅允许管理员显式操作。 |
| 2026-07-05 | V1.3 | Codex | 补充批量检测异步验收项：批量检测必须由后端批量提交任务，前端不得循环调用单资源检测接口；不改变既有异步边界策略。 |
| 2026-07-10 | V2.0 | Cursor | 按现有实现重做上线红线：永久购买、邮件唯一归属、3/30 天保留、cursor、大规模 SQL 与 300/3000 混压。未实现售后、结算、显式别名远端创建不作为当前版本红线。 |
| 2026-07-10 | V2.1 | Cursor | 增加 Microsoft 显式别名异步创建、上海自然周/年配额、候选预占、远端结果对账和 fencing 验收项。 |
| 2026-07-11 | V2.2 | Codex | 明确显式别名只为公开出售的正常 Microsoft 资源自动补货、成功库存统一归确定性的 `super_admin`，并按当前三 worker-pool 拓扑补充低优验证/别名任务的动态 dispatch 与 execution admission 验收口径。 |
| 2026-07-11 | V2.3 | Codex | 将显式别名补货与 `forSale` 解耦：公开和私有的正常 Microsoft 资源都创建，出售状态切换不影响任务；非正常状态仍在领取和远端调用前被拦截。 |
| 2026-07-12 | V2.4 | Codex | 增加管理员 Microsoft 资源管理专项验收：UI 零删除、真实 API、mock 清除、跨 BC 组合读、显式命令、禁敏和 SQL/P95 证据；不修改 D1–D20、T1–T13、A1–A13 的编号、评分或红线规则。 |
| 2026-07-12 | V2.5 | Codex | 回填管理员 Microsoft 专项阶段性证据：正式契约、migration/Port、含 Orders bounded enrichment 的查询/命令/任务、前端真实 API 和 mock 零引用已有实现；全权限/禁敏/并发、目标规模性能和真实 E2E/视觉/重启证据仍阻塞合并。 |
| 2026-07-12 | V2.6 | Codex | 回填最终稳定工作树的 Go/静态/OpenAPI/前端/mock 门禁并校准专项自评为 D 30、T 13、A 17；T8、T12、A7 保持 0，性能、真实 E2E、视觉与重启演练仍禁止上线。 |
| 2026-07-12 | V2.7 | Codex | 曾回填管理员 API 安全契约、Redis 限流、100 并发与 rollback/panic、前端 9 files/55 tests 和真实 100k/1M 基准；后续 V2.10 已删除专项限流和性能 harness。辅助邮件 generated sort migration 因全仓兼容性测试失败被撤回，体现简单稳定优先。 |
| 2026-07-12 | V2.8 | Codex | 将性能数值统一为参考基线和容量观察，硬门禁聚焦正确性、有界查询、无 N+1 与可诊断性；不再指定缓存命中或把 10M/全部 P95 变绿作为管理员专项评分前提。 |
| 2026-07-12 | V2.9 | Codex | 回填管理员 Microsoft 自动化代码交付结论：Core credential Port、Alias 唯一审计入口、无用索引清理、async accepted/import queue 禁敏已收口；功能/事务/migration、前端 9/56、生成、vet/lint 和零 mock/直连扫描通过。压测不属于本次门禁。 |
| 2026-07-12 | V2.10 | Codex | 进一步落实简单稳定原则：管理员专项删除百万数据性能 harness 和应用内 Redis 管理限流，不再以 P95/压测评分推动缓存、投影表或额外可用性依赖；保留有界分页、无 N+1、普通 EXPLAIN 和必要索引证据，性能问题改由真实运行数据触发。 |
| 2026-07-12 | V2.11 | Codex | 管理员专项并发口径收敛到实际不超过 10 个管理员：保留真实 MySQL 事务/唯一事实/锁序测试，删除固定 100 压力与全仓 race 门槛；同时移除重复列表索引、dead TaskView 查询和 AMR Redis/投影依赖。 |

> 本矩阵用于写之前、写之中、写之后的自审。目标不是堆流程，而是用最少证据证明系统能上线、能维护、能扩展、能重构。

---

## 1. 评分规则

| 分数 | 含义 |
|------|------|
| `0` | 缺失、不可证明、靠口头约定。 |
| `1` | 基本可用，但证据不足或边界有风险。 |
| `2` | 设计清楚、实现简单、证据完整。 |

红线项为 `0` 不允许合并。

---

## 2. 设计矩阵

| 编号 | 设计项 | 达标目标 | 过轻坏味道 | 过重坏味道 | 证据 |
|------|--------|----------|------------|------------|------|
| D1 | 上线目标 | 能说明服务哪条最短闭环 | 先做 CRUD | 为未知未来造平台 | 主线用例 |
| D2 | 上下文归属 | 状态和规则有唯一 BC 拥有 | Handler 到处写规则 | 为小规则新建 BC | DDD 文档 |
| D3 | 聚合边界 | 聚合只维护自身强一致 | 跨域直接写表 | 聚合塞入多个域事实 | 实体图 |
| D4 | 状态机 | 合法/非法/终态/重试清楚 | status 字符串随便改 | 为临时步骤造状态 | 状态图和测试 |
| D5 | SQL 约束 | 关键不变式有唯一/外键/CHECK/条件更新 | 全靠代码判断 | 为所有字段造复杂约束 | migration SQL |
| D6 | API 复用 | 控制台和 SDK 共用接口 | 用户/后台/SDK 三套重复 | 为了 REST 拆过多命令资源 | OpenAPI |
| D7 | 鉴权主体 | Session/API Key 边界清楚；pickup 的 `email + token` 是资源钥匙，不进入通用主体模型 | 靠前端隐藏按钮 | 每个接口手写重复鉴权 | 中间件设计 |
| D8 | 权限模型 | 特权用户继承普通用户能力，Casbin 只做策略 | 管理员另写一套用户接口 | 业务状态写进 Casbin | 权限矩阵 |
| D9 | 错误信息 | 有业务语义且安全 | 全部 invalid parameter | 暴露账号存在性/SQL/上游细节 | 错误样例 |
| D10 | 幂等 | 创建、扣款、退款、分配、重置、拉取幂等 | 重复请求重复写事实 | 查询也做幂等 | 幂等键/唯一约束 |
| D11 | 并发安全 | DB 约束和事务兜底 | 只靠 Redis 锁 | 所有操作都加分布式锁 | 并发设计 |
| D12 | Go 显式事务 | 强一致写在同一个显式 tx 内，失败可回滚 | handler 里裸写多个表 | 外部 HTTP 包进事务 | tx 代码和回滚测试 |
| D13 | 外部系统 | Microsoft/支付/SMTP 在 ACL | 领域直接调 HTTP | 简单调用造中台 | Port/Adapter |
| D14 | 日志诊断 | OperationLog/SystemLog 能排障 | 只能看 Docker 日志 | 普通查询全写操作日志 | 日志字段 |
| D15 | 敏感信息 | 凭据/正文不进响应和普通日志 | 明文到处传 | 过度加密导致不可用 | 禁敏规则 |
| D16 | 后台能力 | 管理员不用 SQL 修业务 | 异常状态只能手改库 | 后台绕过状态机 | 管理命令 |
| D17 | 性能意识 | 代表性查询有索引、EXPLAIN 和 P95 记录 | 上线后才看慢 SQL | 为极端目标过早分库分表、缓存或预聚合 | EXPLAIN/压测 |
| D18 | 可重构性 | 模块内聚、Port 清楚 | 代码耦合到表和 handler | 抽象层太多 | 包依赖图 |
| D19 | API 命名 | 常见短词、能省略先省略、默认不用连接符、无内部实现词 | URI 冷门词/表名/角色前缀/多词硬拼 | 为命名过度拆碎路径 | OpenAPI |
| D20 | 同步/异步边界 | HTTP 同步链路只做快速校验、持久化和任务投递；外部网络、批量处理、可重试耗时检测默认用 Asynq 或等价后台任务；批量检测必须由后端批量提交任务，不允许前端循环请求单资源检测接口 | 耗时任务塞进请求导致超时和慢请求 | 查询也强行异步导致 UX 和一致性变差 | 同步/异步取舍说明、任务状态、日志 |

红线：`D2/D4/D5/D6/D7/D9/D10/D11/D12/D14/D15/D16/D17/D19/D20`。

`D15` 中“正文不进响应”指正文不得进入列表、批量响应、任务、错误、普通日志或无关详情；业务已确认且完成资源/消息关联及权限校验的单封邮件详情可以按需返回正文。验证码可出现在已确认的授权 Orders 行、邮件摘要和单封详情中，但不得进入任务、错误、OperationLog、SystemLog、普通日志或导出。密码、Client ID 原值、RT、AT 和内部任务 token 在任何管理响应中都没有例外。

---

## 3. 错误信息矩阵

错误信息必须能帮助前端和用户定位，但不能帮助攻击者枚举系统。

| 场景 | HTTP | message | 原因 |
|------|------|---------|------|
| 账号不存在或密码错误 | `422` | `Account or password is incorrect.` | 不枚举账号是否存在或密码是否正确。 |
| 图形验证码错误/过期 | `422` | `Captcha is incorrect or expired.` | 验证码错误不涉及账号枚举，可明确提示。 |
| 邮箱验证码错误/过期 | `422` | `Verification code is incorrect or expired.` | 可明确提示。 |
| 未登录/API Key 无效/Token 无效 | `401` | `Authentication is required.` 或 `Credential is invalid or expired.` | 不暴露凭据状态细节。 |
| 权限不足 | `403` | `Permission denied.` | 清楚但不暴露策略细节。 |
| 资源不存在或越权 | `404` | `Resource not found.` | 防止枚举他人资源。 |
| 余额不足 | `422` | `Insufficient balance.` | 业务语义明确。 |
| 订单状态不允许 | `409` 或 `422` | `Current order status does not allow this operation.` | 可定位状态问题，不回显内部状态组合。 |
| 幂等键冲突 | `409` | `Idempotency key conflicts with another request.` | 可指导调用方修复。 |
| 限流 | `429` | `Too many requests.` | 不暴露内部配额细节，响应头可带 Retry-After。 |
| Microsoft/邮件协议不可用 | `502/503` | `Mail service is temporarily unavailable.` | 内部详情写 SystemLog。 |

粒度规则：

| 类型 | 要求 | 示例 |
|------|------|------|
| 不能太笼统 | 可预期业务错误必须说明业务语义，不能让前端只能看到参数错误。 | 验证码错返回 `Captcha is incorrect or expired.`，余额不足返回 `Insufficient balance.` |
| 不能过度暴露 | 会导致枚举、凭据判断、内部结构泄露的细节必须合并。 | 登录失败返回 `Account or password is incorrect.`，不分别提示账号不存在或密码错误。 |
| 可直接提示 | 不涉及账号枚举、权限策略、凭据状态的输入错误可以明确。 | 图形验证码、邮箱验证码、幂等键冲突。 |
| 内外分离 | 对外 `message` 给用户看；内部分类、SQL、上游原文、堆栈只进禁敏日志/诊断字段。 | Microsoft `password` 映射为安全 message，原分类写 SystemLog。 |

禁止：

- `invalid parameter` 覆盖所有业务错误。
- `password is incorrect` 单独提示密码错误。
- SQL、堆栈、上游原文、Token、RT、邮件正文进入 message。
- 动态拼接邮箱、订单号、资源 ID 到对外 message；这些写日志。

---

## 4. 测试矩阵

| 编号 | 测试项 | 必测内容 | 证据 |
|------|--------|----------|------|
| T1 | 状态机 | 合法流转、非法流转、终态拒绝、重试路径 | 单元测试 |
| T2 | 领域规则 | 权重、窗口、项目规则、永久购买与退款释放 | 单元测试 |
| T3 | API 契约 | HTTP 状态、错误体、OpenAPI、鉴权主体、路径命名 | Handler/契约测试 |
| T4 | SQL 约束 | 唯一、外键、CHECK、条件更新 | Testcontainers MySQL |
| T5 | 幂等 | 同 key 重放、不同指纹冲突 | 集成测试 |
| T6 | 并发 | 钱包扣款、分配抢占、卡密兑换、提现冻结、API Key 下单 | 并发集成测试 |
| T7 | Go 事务 | 同一用例所有写仓储绑定同一 tx，错误/panic 回滚，外部失败不留坏账/坏状态 | 集成测试 |
| T8 | 性能 | 关键查询无明显 N+1、索引命中、P95 有记录 | EXPLAIN/压测 |
| T9 | 权限 | Session/API Key/Casbin/scope，以及 pickup `email + token` 资源钥匙边界 | HTTP 测试 |
| T10 | 日志 | 高风险 OperationLog、上游失败 SystemLog、禁敏 | 日志断言 |
| T11 | 外部失败 | Microsoft ACL、支付、SMTP、MinIO、Redis 失败 | adapter 测试 |
| T12 | E2E | 最短上线主线和关键失败闭环 | E2E 报告 |
| T13 | 同步/异步任务 | 同步入口只验证并投递任务；后台任务覆盖成功、确定性失败、基础设施失败、内部重试和幂等/重复投递边界 | handler/usecase/worker 测试 |

红线：`T1/T3/T4/T5/T6/T7/T8/T9/T10/T12/T13`。

---

## 5. SQL 验收矩阵

| 模块 | 必须证明的 SQL 能力 |
|------|--------------------|
| IAM | email 唯一、首次激活并发只成功一次、邀请码次数原子递增。 |
| Core | 资源根/子表类型一致、listed 项目名唯一、别名配额唯一和并发、生成邮箱唯一、自建邮箱域名和 MailServer owner 一致。 |
| Alloc | 一个订单只能一个分配、main/alias/mailbox allocated 唯一、释放条件更新。 |
| Billing | 钱包行锁、流水不可变、余额非负、卡密兑换并发唯一、提现冻结不重复。 |
| Trade | orderNo 唯一、幂等键唯一、分配外键二选一、状态条件更新。 |
| MailMatch | 同资源 Message-ID 去重、唯一订单归属、历史项目关系幂等、收件人/状态/时间索引。 |
| Proxy | 代理池和 URL 唯一、binding key + ip 有效绑定唯一、expireAt/status 索引、错误计数条件更新。 |
| OpenAPI | API Key/OrderToken 前缀索引、幂等键唯一、并发占用释放可恢复。 |
| Governance | 日志索引、任务状态 claim 索引、配置 key 唯一。 |

每个模块合并前必须给出 migration 文件和至少一个 Testcontainers MySQL 约束测试。

SQL 证据必须包含：

| 证据 | 要求 |
|------|------|
| DDL | migration 文件名、表、唯一键、外键、CHECK、核心索引清单。 |
| 关键 SQL | 钱包、分配、幂等、任务 claim、窗口查询等手写 SQL 必须列出核心语句或测试覆盖点。 |
| 约束测试 | 至少覆盖唯一冲突、外键拒绝、余额不负、重复分配、幂等冲突中的本模块关键项。 |
| EXPLAIN | 列表、详情、窗口查询、日志查询、任务 claim 等高频查询必须记录使用的索引名和扫描行数级别。 |
| 漂移策略 | 当前未发布首版统一为 `00001_initial.sql`；该初始化基线一旦部署，后续结构修正只能新增 migration，不再覆写历史。 |

---

## 6. 性能观察与验收矩阵

以下数值先按单机 6C/16G 作为默认参考基线，用于发现回归和容量风险，实际上线门槛按真实预估峰值、数据分布和适度余量确定。

| 指标 | 参考目标 |
|------|----------|
| 登录/当前用户 | P95 < 150ms |
| 项目列表/详情 | P95 < 200ms |
| 下单同步阶段 | P95 < 800ms，不含外部邮件拉取 |
| 钱包查询 | P95 < 200ms |
| 订单列表 | P95 < 300ms，必须有索引和 limit/page 方案 |
| 邮件列表 | P95 < 300ms，按 orderNo 链路和时间索引查询 |
| API Key 鉴权 | P95 参考值 < 50ms；优先保证查询有界且命中常规索引，是否使用缓存由真实瓶颈决定 |
| 代理获取 | P95 < 80ms，命中绑定或最少绑定选择必须走索引 |
| Asynq 任务失败可见性 | 任务失败 5 秒内可在后台任务/日志中查到 |
| 慢 SQL | 无未解释的全表扫描；核心查询必须有 EXPLAIN 证据 |
| 数据库连接 | 连接池、超时和慢 SQL 阈值有配置；压测期间无连接耗尽 |
| Redis/Asynq | Redis 操作 P95 有记录；任务积压、重试、失败可观测 |
| 同步/异步取舍 | 每个外部网络或批量入口必须说明是否同步等待、为何允许等待、最大耗时预算、失败可见性和后台任务降级策略 |
| 系统资源 | 压测报告包含 CPU、内存、DB CPU、Redis 内存和错误率 |

说明：列表接口可以使用简单 `page/pageSize`，不为了旧全量列表口径坚持全量返回。快速上线不等于允许大表全量扫。

性能证据不要求一次性达到大规模系统标准，也不要求为全部参考值变绿引入额外机制；但每个模块上线前必须留下“测了什么、数据量多大、P95 多少、瓶颈在哪里”的记录。没有数据的性能结论视为 0 分。

---

## 7. Go 事务验收矩阵

Go 项目没有 Spring `@Transactional` 这种默认强约束，事务必须作为显式工程纪律验收。任何跨表强一致写路径，如果没有事务证据，`D12/T7/A6` 按 0 分处理。

| 场景 | 必须证明 | 兜底要求 |
|------|----------|----------|
| 下单 | 订单创建、幂等记录、钱包扣款、分配绑定、事件记录在正确事务边界内 | 任一步失败不能留下扣款无订单、订单无分配、重复事件 |
| 退款 | 订单状态、退款流水、钱包余额、服务凭证禁用一致 | 重复退款不重复入账 |
| 分配抢占 | 候选占用、allocation 创建、资源使用状态一致 | 并发下唯一约束和条件更新兜底 |
| 钱包扣款/冻结 | 钱包行锁、流水、余额桶更新一致 | 错误和 panic 全回滚 |
| 卡密兑换 | 卡密次数、兑换事实、钱包入账一致 | 并发兑换不突破次数 |
| 任务 claim | 任务状态、lease、执行记录一致 | 多 worker 不重复 claim |
| 服务凭证重置 | 旧 token 禁用、新 token 创建、幂等记录一致 | 幂等重放返回同一结果 |

代码验收规则：

| 规则 | 要求 |
|------|------|
| tx 显式传递 | Application Service 开启事务后，仓储必须使用同一个 `tx`/`context` 中的 DB handle，禁止中途回到全局 `db`。 |
| tx wrapper | 统一封装 `WithTx(ctx, fn)` 或等价能力，负责 begin/commit/rollback/panic rollback。 |
| 短事务 | 事务内只做数据库强一致写，不做 Microsoft HTTP、SMTP、MinIO、支付、长时间邮件拉取。 |
| 外部调用 | 外部调用前后通过任务表、状态字段、outbox 或后续命令衔接，不能把网络调用包进事务。 |
| 锁顺序 | 钱包、订单、分配等多行锁路径要有固定锁顺序，避免死锁扩大。 |
| 错误返回 | 任何 repo 错误都必须让 tx 回滚；不能 catch 后继续 commit。 |
| 测试 | 每条资金/分配/订单事务路径至少有一个“中途失败回滚”集成测试。 |

事务证据必须包含：

| 证据 | 要求 |
|------|------|
| 正向事务测试 | 成功后多表事实一致。 |
| 失败回滚测试 | mock 或 test hook 让事务中段失败，断言前置写入已回滚。 |
| panic 回滚测试 | `WithTx` 层覆盖 panic rollback。 |
| 仓储 tx 绑定测试 | 关键仓储在事务内不能使用全局 DB；可通过接口约束、测试桩或代码审查证明。 |
| 外部调用边界 | 证明事务内没有 Microsoft/支付/MinIO/SMTP 网络调用。 |
| 死锁/重试策略 | 对可能发生死锁的高并发路径，说明重试次数、错误 message 和日志。 |

---

## 8. 并发安全矩阵

| 场景 | 并发目标 | 兜底机制 |
|------|----------|----------|
| API Key 100 并发下单 | 不重复订单、不重复扣款、不突破并发限制 | 幂等表、并发计数、订单唯一键 |
| 同资源分配抢占 | 同一 main/alias/mailbox 不被同项目重复占用 | 唯一索引、条件插入 |
| 钱包扣款 | 余额不负，流水和余额一致 | `SELECT FOR UPDATE` 或条件更新 |
| 卡密兑换 | 同一卡密次数不超限 | 条件更新、唯一兑换事实 |
| 提现申请 | 同一额度不能重复提现/转消费 | 钱包锁、提现冻结流水 |
| 服务凭证重置 | 旧 Token 禁用，新 Token 唯一，幂等重放一致 | 事务、幂等键 |
| 资源代理绑定 | 同一 key 100 并发只创建一个有效绑定，不同 key 优先落到绑定数最少的代理 | 唯一索引、事务、行锁或条件插入 |
| 代理错误隔离 | 运行期和检测失败只置 `abnormal`，`disabled` 只能由管理员显式操作；成功使用或检测成功清零错误计数 | 条件更新、状态机、只选择 `normal` |
| 任务 claim | 同一任务只被一个 worker 执行 | 状态条件更新、lease |

并发验收要求：

| 项目 | 要求 |
|------|------|
| 测试形态 | 使用 Testcontainers MySQL/Redis 或等价真实依赖，不只 mock。 |
| 断言 | 断言最终业务事实，不只断言无 panic，例如余额、流水、分配、订单、任务执行次数。 |
| 兜底 | Redis 锁、进程锁只能作为保护层，必须有数据库唯一约束、条件更新或事务兜底。 |
| 重放 | 幂等接口必须覆盖同 key 重放、同 key 不同请求体冲突、并发重放。 |
| 日志 | 并发冲突、限流、任务失败要能在 SystemLog/请求日志/任务视图定位。 |

---

## 9. 模块验收矩阵

| 编号 | 验收项 | 达标标准 |
|------|--------|----------|
| A1 | DDD 一致 | 代码、SQL、API 和 DDD 文档一致。 |
| A2 | Migration | SQL 可在空库执行，已部署历史不改。 |
| A3 | SQL 约束 | 核心不变式有数据库兜底。 |
| A4 | API 契约 | HTTP 状态、错误体、鉴权、OpenAPI 正确。 |
| A5 | 状态机 | 合法/非法/终态测试完整。 |
| A6 | 事务/幂等/并发 | 关键写路径有 Go 显式事务、幂等和并发安全证据。 |
| A7 | 性能 | 核心查询 EXPLAIN 和 P95 有证据。 |
| A8 | 错误信息 | 有业务语义且安全。 |
| A9 | 日志 | OperationLog/SystemLog 可查且禁敏。 |
| A10 | 后台能力 | 管理员不需要 SQL 修业务状态。 |
| A11 | SDK 复用 | API Key 调统一业务 API，无重复后端实现。 |
| A12 | 文档同步 | 状态、API、SQL、错误策略变更同步文档。 |
| A13 | 同步/异步边界 | 耗时外部调用、批量处理和可重试任务有同步/异步论证；异步任务有状态、日志、重试或不重试原因。 |

红线：`A2/A3/A4/A5/A6/A7/A8/A9/A10/A13`。

---

## 10. 合并前结论模板

```text
模块/功能：
所属 BC：
服务主线：

设计矩阵：
- 红线项是否全部非 0：
- 主要取舍：
- 是否引入不必要复杂度：

SQL 证据：
- migration：
- 唯一/外键/CHECK/索引：
- Testcontainers 约束测试：
- EXPLAIN：

Go 事务证据：
- tx 边界：
- 失败回滚测试：
- panic rollback：
- 外部调用是否在事务外：

并发与性能：
- 幂等测试：
- 并发测试：
- P95/压测结果：

错误与日志：
- 业务错误 message 样例：
- 安全性说明：
- OperationLog/SystemLog 证据：

API/SDK：
- OpenAPI 变更：
- 是否复用统一业务 API：
- 是否新增重复接口：

结论：
- 是否允许合并：
- 遗留风险：
```

---

## 11. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-QM-1 | SQL、性能、并发纳入验收 | 这些直接影响上线稳定性。 |
| ADR-QM-2 | 错误信息必须有业务语义 | 前端和用户需要定位，但不能泄露攻击信息。 |
| ADR-QM-3 | SDK 复用作为验收项 | 避免重新走接口重复建设。 |
| ADR-QM-4 | 快速上线不等于无约束 | 边界、SQL 和并发兜底是后续可维护的基础。 |
| ADR-QM-5 | Go 显式事务单列验收 | Go 没有注解事务兜底，必须靠 tx wrapper、测试和代码审查证明事务性。 |

---

## 12. 当前版本实际验收清单

### 12.1 当前范围

必须验收：IAM、资源/项目、分配、钱包、订单、API Key、MailMatch、Microsoft Fetch、Microsoft 显式别名异步创建、自建 SMTP、Proxy、Governance 和现有 Web 页面。

不作为本版红线：售后工单、供应商结算/提现、在线支付、多活部署。相关代码不得伪造“已完成”状态，前端不得展示可点击占位入口。

### 12.2 功能与一致性

- [ ] 接码唯一命中后完成订单；读取期结束释放 Allocation、禁用 Token。
- [ ] 接码超时无交付头时只退款一次；存在交付头时绝不退款。
- [ ] purchase 质保结束可变为 `completed`，但 Allocation、Token、收新邮件和读取最近消息继续有效。
- [ ] purchase 管理员退款/终止后释放 Allocation 并禁用 Token。
- [ ] 项目下架只阻止新订单，已有订单继续使用项目最新邮件规则。
- [ ] 多项目规则同时命中时 `matched_order_id` 为空，任一 Pickup 均不可读取该消息，诊断可查。
- [ ] 同幂等键重放保持相同业务结果；不同请求指纹返回 409。
- [ ] API Key 创建、列表、详情均允许 owner 查看明文；普通日志不得出现 Key、Token、RT、密码或正文。
- [ ] Microsoft 验证完成全量 Inbox/Junk 扫描；旧项目关系按 `(resource_id, project_id)` 幂等写入，匹配失败不改变资源健康，后续分配排除同项目资源。
- [ ] Microsoft 显式别名 dispatcher 无条件持续运行，为全部 `status=normal` 的公开和私有资源创建；`forSale` 切换不得暂停 schedule、取消 attempt 或阻止远端请求，非 `normal` 状态仍须在领取和远端调用前被拦截；按 Asia/Shanghai 自然周最多 2 个、自然年最多 10 个，运行中与结果不确定的候选预占额度，确认失败后释放，次年自动恢复。
- [ ] 每条 `explicit_aliases.owner_user_id` 必须是成功事务中按 `users.id ASC` 确定并共享锁定的第一个 `role=super_admin`；不得继承主资源供应商 owner，不得为空，也不得降级给普通 admin。历史记录迁移和重复 alias 再确认都必须收敛到该 owner；系统没有 `super_admin` 时成功落库整体回滚。
- [ ] 交易走同步请求路径即时成交；接码拉取额外获得 32-worker 专用实时池，长任务占满共享池时也保证有 worker 可用。
- [ ] 异步执行分为 32-worker 接码实时池、64-worker 邮件交付/default 前台池和 32-worker 验证/显式别名后台池；后台两队列都有积压时按 3:1 调度，单队列可借满空闲容量。
- [ ] 资源验证 dispatcher 必须保证全库 `validating` 临时分配水位不超过当前自适应 execution window，不能因周期唤醒持续堆积 Redis；显式别名仍从 durable schedule 领取，并在发起外部请求前经过同一 execution admission。

### 12.3 数据保留

- [ ] Microsoft `mailmatch_messages` 超过 3 天可持续批删。
- [ ] Domain `mailmatch_messages`、`inbound_mails` 和 MinIO `.eml` 超过 30 天可持续清理。
- [ ] delivery head 的正文被删除后仍保留交付事实，且不会阻断批删。
- [ ] order、order_events、allocations、wallet_transactions 长期保留。
- [ ] allocation_daily_usages 保留 14 天；终态任务/SystemLog 按 14–30 天清理。
- [ ] 成功 API/Pickup 请求不逐条写 MySQL 日志。

### 12.4 并发与故障

- [ ] 同幂等键 100 并发只产生 1 个订单、1 次扣款、1 个 Allocation。
- [ ] 同一 main/alias/dot/plus 100 并发不超卖；数据库 active unique key 是最终兜底。
- [ ] 钱包并发扣款余额不为负；单用户吞吐串行化属于正确性成本。
- [ ] MarkFailed 中途失败时整个 Checkout 回滚，不留下 pending 脏单。
- [ ] cleanup partial_failure 无需人工即可被生命周期扫描恢复。
- [ ] NotifyMatchedCode 首次失败后由 delivery head 补偿推进。
- [ ] Fetch 仍按 email_resource_id 单飞；Redis/worker 重启后 durable job 可重派。
- [ ] 重复验证不会重复创建旧项目关系；历史项目资源在候选查询与行锁重校验两处均被排除。
- [ ] 显式别名远端响应丢失、落库失败或 worker 过期后只对账并重试同一候选；旧 fencing token 不得覆盖新 worker，周/年额度不得超限。

管理员 Microsoft 专项已经补充的并发与事务证据如下；这些证据只关闭对应路径，不能替代上面订单、钱包等全局清单：

- [x] 同一同步状态命令及相同 Idempotency-Key 在预期最多 10 个管理员同时提交下，只产生一个 command receipt、一次版本变化和一条 Core OperationLog。
- [x] 管理员 import、filter bulk 在最多 10 个同时提交下各只创建一个 durable fact；bulk 只写一条命令级 OperationLog。
- [x] Validation、Token Refresh、Alias Expedite、Resource Fetch 在最多 10 个同时提交下各只创建一个 active job/schedule 结果；重复请求按各自契约复用 task 或 durable receipt。
- [x] 原子 Edit 在 Binding Port error 和 panic 时回滚 root/subtype、validation、receipt 与 OperationLog；Enable 在 OperationLog failure 时回滚状态、validation 与 receipt。
- [x] bulk page 中段失败时资源状态与 checkpoint/progress 同事务回滚；Token/Alias 审计失败和 Fetch 完成期 SystemLog 失败均有对应回滚证据。
- [x] Alloc 已有管理员先锁 root 与 Allocation 先锁 root 两个方向的锁序测试，并验证 active guard 不存在 check-then-act 窗口。
- [x] Import/Bulk 仅对已复现的 MySQL 1205/1213 使用最多 3 次短事务重试；同场景验证最终只有一个 durable fact，失败页与 checkpoint/progress 保持同事务。
- [ ] 同资源普通 Edit 不同 version/内容、Delete 与 Allocation create 的完整双向 barrier 仍待补；压测、全仓 `go test -race` 和 lock-wait 指标不作为管理员功能完成条件。

### 12.5 SQL 与性能

执行入口：

- 数据生成：`cmd/benchseed`
- SQL 计划：`scripts/perf/explain.sql`
- 混合压测：`scripts/perf/k6.js`
- 操作说明：`scripts/perf/README.md`

容量 profile 可生成：100 万 Microsoft 主资源、独立 profile 的 1000 万 explicit_aliases、1 万 Domain（Domain 可用真实导入数据）、1000 万 orders/allocations/wallet_transactions、3000 万 order_events，以及按 3/30 天窗口生成的消息。上述 profile 用于容量观察和重审触发，不要求为了达到极致 P95 预先引入缓存、物化统计或双写；管理员低频查询优先保持简单、稳定、可诊断。

参考压测场景与硬不变式：

- [ ] 300 下单/s + 3000 Pickup/s 持续 15 分钟作为容量参考：记录下单/Pickup P95、P99、错误率和资源占用，不把该固定档位套到低频管理员页面。
- [ ] 600/6000 持续 5 分钟只在真实容量规划需要时做余量观察；任何档位都不能出现重复业务事实、连接耗尽或不可恢复的 lock wait timeout。
- [ ] 非业务 5xx、超时和饱和点有记录；具体阈值按预计峰值和上线环境定稿，参考值为 <0.1%。
- [ ] Microsoft 模拟 Graph 到可见 P95 ≤10 秒；SMTP 到可见 P95 ≤2 秒。
- [ ] 候选选择、active key、Token+scope、matched_order_id、生命周期、保留 DELETE、cursor 查询均保存 `EXPLAIN ANALYZE`。

管理员 Microsoft 是低频管理用例，本次不维护独立的百万数据压测 harness，也不以固定 P95 推动缓存、物化统计、额外索引或跨域双写。合并时只要求能直接证明复杂度受控的证据：服务端 page limit、稳定排序、无 N+1 的查询次数断言、关键 SQL 普通 `EXPLAIN` smoke，以及列表响应直接复用 facets。真实运行出现慢 SQL 后，再以实际 SQL、数据分布和调用频率做局部评审。

因此专项 T8/A7 只按“已有有界查询和普通执行计划证据”保守评 1，不把历史开发机压测数字作为当前实现或生产容量承诺。性能优化必须能说明收益高于新增状态、缓存一致性和维护成本。

### 12.6 前端与契约

- [ ] 同一订单的面板与邮箱弹窗共用一个自动轮询和一个 in-flight 请求。
- [ ] 自动轮询严格服从 `nextFetchAllowedAt`，页面隐藏时暂停。
- [ ] purchase 首码和质保结束后仍可刷新；Domain 明确显示 SMTP push。
- [ ] 邮件列表只取摘要，用户打开邮件时才读取正文。
- [ ] 快速切换 serviceMode/筛选时旧响应不能覆盖新状态。
- [ ] 资源验证完成后自动刷新状态并显示安全错误。
- [ ] 导航不展示未实现页面；在线支付入口隐藏，卡密兑换保留。
- [ ] `api/openapi.yaml` 为 Session/管理员 HTTP 契约源；Go、TypeScript 生成后 CI diff 必须为空。独立 public spec 只描述公开接口，按其生成脚本和路由/enum 契约测试校验，不反向定义管理员契约。
- [ ] 前端至少运行 `pnpm typecheck`、`pnpm test`、`pnpm build`。

### 12.7 CI/CD 最终门禁

- [ ] `backend-static` 覆盖 gofmt、OpenAPI 生成漂移、go vet、golangci-lint。
- [ ] `backend-test` 聚合两个并行 Testcontainers 分片，任一失败均阻止后续发布。
- [ ] `frontend` 覆盖 TypeScript 契约漂移、typecheck、Vitest、生产 build，并产出唯一 `web-dist`。
- [ ] PR 必须通过 Dockerfile.ci 构建；main/tag 必须通过真实 MySQL/Redis/MinIO smoke 后才允许发布镜像。
- [ ] 同分支旧流水线自动取消；tag 不取消；所有长任务有 timeout。
- [ ] GHCR 和 Actions Cache 都只保留最近 3 个版本，Cleanup dry-run 可审查且删除错误必须失败。

### 12.8 管理员 Microsoft 资源管理专项验收

本节是既有 D1–D20、T1–T13、A1–A13 的专项证据清单，不新增评分编号，也不放宽任何红线。详细 UI/API/BC/Port/权限/日志/测试追踪以 [21-admin-microsoft-resource-matrices.md](21-admin-microsoft-resource-matrices.md) 为准。

| 类别 | 必须通过的专项验收 | 主要证据 |
|------|--------------------|----------|
| UI 零删除 | 每个已确认可见能力都有 `UI-AMR-*`：主列表、后缀 Tabs、搜索、创建时间、五组交叉筛选、单项/选中项/全部匹配动作、三个弹窗、七个详情 Tab、四个手工任务，以及桌面/移动布局、加载、空状态、Toast、确认、分页和选择清理；数量、文案和交互不能因后端实现而删改。 | UI 保留矩阵、runtime contract、桌面/移动组件测试、E2E 截图或录像。 |
| 真实 API | 每个 UI-ID 都由已进入 `api/openapi.yaml` 的真实查询、命令或 durable task 支撑；管理员 Go、TypeScript 生成后无漂移，独立 public spec 不包含管理员路径，mock DTO 不作为契约源。 | OpenAPI diff、Handler/契约测试、前端生成类型引用、public admin-path 零检查。 |
| mock 清除 | 生产和测试代码均不再 import 管理员 Microsoft mock；不存在生产 mock fallback、随机数据或刷新后回到内存状态；有价值的 mock 断言迁移为契约、adapter、组件或 E2E 测试。 | `rg` 零引用、构建产物检查、真实数据库 E2E。 |
| 跨 BC 组合读 | 管理页面不形成新业务域或复制资源、owner、绑定、分配、订单、邮件、任务事实；按事实所有者通过批量 Query Port 或直接源表的有界只读查询组合读取，列表无 N+1，七个 Tab 按需分页/懒加载。 | 包依赖审查、Query Port 测试、SQL 查询计数、分页/取消旧请求测试。 |
| 显式命令 | Validate、Enable、Disable、Publish、Unpublish、Delete、Recover、ReplaceCredentials、RefreshToken、ExpediteAliasSchedule、FetchMail 均进入事实所有者的 Application Service；不得用任意 `PATCH status` 或后台直接更新跨域表替代。active Allocation 必须阻止 email/owner/Delete，同时反向证明 Disable、Unpublish、凭据、qualityScore、longLived 和 binding 输入不会被通用 guard 误阻断。 | 状态机测试、active Allocation 正反向测试、Handler 路由审查、OperationLog 断言。 |
| 原子编辑 | 一个编辑保存中的基础字段、owner、binding 输入、可选完整凭据、OperationLog 和验证任务事实处于明确的单一短事务；任一步错误或 panic 全回滚，Microsoft 外部调用只在提交后由 worker 执行。 | 正向/失败/panic rollback、tx 绑定与 dispatcher 恢复测试。 |
| 批量与幂等 | 选中项使用 `selection.mode=ids`，全部匹配使用 `selection.mode=filter` 并记录接收时资源 ID 高水位；前端不拉全量 ID、不循环单资源 API；大批量分块执行、同命令重放幂等且只写命令级 OperationLog。 | Handler/usecase/worker、并发重放、批量日志行数和高水位测试。 |
| 异步边界 | 验证、RT 刷新、alias schedule 加速、资源 Fetch 和大批量动作只在 HTTP 中校验、落 durable fact 并返回 `202`；同资源同类 active job 复用，Redis/Asynq 短暂失败可由 dispatcher 恢复。 | T13 测试、active-job 唯一约束、worker 重试/确定性失败/恢复测试。 |
| 权限与审计 | 每个读写入口有 Casbin 权限和 CSRF 证据；owner 资格、资源状态和 active Allocation 由领域校验；单项/批量命令写 OperationLog，外部失败写 SystemLog，单封敏感正文读取有定向审计且摘要列表不刷屏。 | 权限/CSRF HTTP 测试、日志字段和数量断言。 |
| 禁敏与安全错误 | 任何响应、任务视图、OperationLog、SystemLog、Toast 安全文案均不包含密码、Client ID 原值、RT、AT、RFC822 objectKey、claim/dispatch token、代理凭据或上游原始页面/响应；正文只有通过资源/消息关联及读取权限校验的单封邮件详情可以返回，验证码只允许出现在 UI 已确认的授权 Orders 行、邮件摘要和单封详情中，正文/验证码均不得进入任务、错误、日志或导出。不存在/越权用安全 404，临时上游失败用安全 502/503。 | JSON key 递归扫描、日志扫描、Orders/邮件摘要与单封正文授权测试、错误分类/MinIO 失败测试。 |
| 邮件按需读取 | 主邮箱和辅助邮箱列表只返回摘要；仅在选中单封邮件时读取正文，切换资源/Tab 后旧响应不得覆盖新状态；辅助邮箱事实继续归 MailTransport。 | API payload 大小、summary/detail 契约、AbortSignal/stale response、受控正文读取测试。 |
| SQL 与性能 | 资源列表、交叉 facets、owner/binding 批量补充和关键 Tab 查询必须有 page limit、稳定排序、查询次数与普通 `EXPLAIN` 证据；本专项不要求压测或固定 P95。 | SQL/索引清单、执行计划 smoke、查询次数和分页测试。 |
| 数据迁移 | owner 转移相关约束区分当前 Binding owner 与 Validation/Inbound/OperationLog 历史 owner 快照；只新增 migration，不修改已部署历史 migration；空库、升级、唯一/FK/CHECK 路径通过。 | migration、Testcontainers 空库/升级/约束测试。 |
| 全链路完成 | 七个详情 Tab、四个手工任务和三个弹窗全部接真实数据；页面刷新、服务/worker 重启和任务重派后结果仍来自数据库及受控对象存储，不依赖进程内 mock。 | 最小管理员 Microsoft E2E、重启/重派演练、前端 typecheck/test/build。 |

专项合并结论必须在第 10 节模板中同时附上 UI 保留矩阵、mock 零引用、OpenAPI 生成漂移、跨 BC Port/有界只读查询组合、敏感字段扫描、列表/facets/Tab 分页与普通 EXPLAIN 证据；任一证据缺失时，对应既有 D/T/A 项仍按原评分规则处理。

当前专项证据快照（2026-07-12；详细测试名见 [21-admin-microsoft-resource-matrices.md](21-admin-microsoft-resource-matrices.md) 第 13–17 节）：

| 类别 | 当前证据 | 当前结论 |
|------|----------|----------|
| 静态/自动化门禁 | `gofmt -l api cmd internal` 无输出，`go vet ./...`、`golangci-lint run`（0 issues）、`git diff --check` 通过；Core credential、Token/Fetch/Alias、00009 migration 和受影响 API/app 功能/事务测试通过 | 本次自动化代码交付门禁完成；不把已中止的长时间混合源码分片或额外压测写成必要证据 |
| UI/前端 | 111 个 UI-ID、3 弹窗、7 Tab、4 任务仍在 runtime contract；`/admin/microsoft` 必须指向真实 lazy page；adapter 使用 generated types；`pnpm typecheck`、Vitest、`pnpm build` 已实跑通过；组件测试覆盖懒加载、active-task polling、凭据成对校验/不回显、异步受理元数据、AbortSignal、desktop 940px 与 mobile 100% SideSheet | 组件/runtime 层已接线；这些测试不是浏览器 E2E 或截图/视觉回归，真实数据库 Playwright、桌面/移动/明暗视觉作为后续发布证据保留 |
| 正式契约/mock | Q01–Q12/C01–C18 已进入 `api/openapi.yaml`；async accepted 固定 `taskId/requestId/status/accepted/reused`，import queue 不序列化 object key/claim token；管理员 Go/TS 生成哈希零漂移，mock 文件删除且 exact scan 零引用/零 fallback | 管理员生成漂移、异步受理、queue 禁敏和 mock 清理门禁已通过；完整运行时角色/权限组合与 502/503 parity 仍独立待验 |
| 边界/后端 | Core 管理 query/command/import/bulk/validation、IAM Owner、Alloc guard/Orders bounded enrichment、Governance TaskView、MailTransport Binding/辅助邮件/Token/Alias、MailMatch message/Fetch 已按事实所有者或直接源表的有界只读查询组合落盘；Token/Fetch 经 Core credential Port，Alias 只有受审计入口 | 没有新 Admin BC、跨域直写、投影表或 Alias 管理旁路；Q01–Q12/C01–C18 运行契约已覆盖 UI 能力 |
| 事务/幂等 | 原子编辑、Binding error/panic、OperationLog/SystemLog failure、Core credential caller-tx、Token/Fetch rotation/fencing、Alias receipt/audit rollback 均有自动化证据 | 核心短事务、durable single-flight、关键失败注入和跨域同 tx 已具备；额外并发压力、全仓 race 和 lock-wait 指标属于后续风险驱动验证，不是本次门禁 |
| 权限/禁敏 | catalog/default policy、route middleware、write-only schema、safe DTO、message/binding relation 404、正文定向审计及多项 canary tests 已有；AMR OpenAPI 保留 401/403 等真实业务错误，不声明应用内 429，管理员同步路径不依赖 Redis 限流 | OpenAPI 安全契约验证 cookieAuth、401/403、CSRF/Idempotency-Key 和敏感字段边界；全 C/Q 运行时角色权限组合、仓库级 response/log/queue/Toast 递归扫描待补 |
| SQL/性能 | 管理列表复用既有索引；migration 00009/00011 只保留 alias、dispatcher、active unique、FK resource/job 和 Orders resource page 等真实查询或约束需要的索引；专项性能 harness、重复列表索引、无效辅助排序索引和管理员 `FORCE INDEX` 已删除 | 本次不执行或要求压测，优先简单实现、事务正确和可维护性；真实慢 SQL 再触发局部优化 |
| E2E/运维 | AbortSignal、dispatcher recovery 的若干专项 tests 已有 | 无真实数据库 Playwright、视觉、页面刷新、API/worker 重启/重派演练；T12 仍为 0 |

按既有评分规则，当前专项保守自评为 D `30/40`、T `14/26`、A `18/26`。T8/A7 因已有 page limit、查询次数和普通 EXPLAIN 证据评 1；不使用压测数字把该项抬到 2。T12 的浏览器/重启证据仍为 0，因此本文只能支持“代码实现与自动化测试/lint 交付完成”的结论，不能据此宣告人工浏览器验收或生产上线。最终发布评审仍须用 CI/E2E/运维 artifact 重新填写第 10 节模板。
