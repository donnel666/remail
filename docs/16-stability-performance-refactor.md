# 稳定性 / 并发 / 性能整改方案(全栈 Review 结论与执行计划)

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-07-09 | V1.0 | Codex | 首版:全栈 Review 结论、P0/P1/P2 问题清单、WP0~WP7 执行计划。五项关键决策已确认;并入前端专项与后端外围模块专项审查结论。 |

> 本文是一次针对首版实现的整改计划,不是新设计。所有改动不得改变既有业务语义
> (订单状态机、一单一资源、钱包扣退款规则、pickup 契约、别名用时写入)。
> 原则:机制简单优先;正确性只依赖 MySQL 事务与唯一约束;展示类数据允许最终一致;
> 不引入新中间件,不拆服务,不造轮子。

---

## 0. 已确认的前提与决策

| # | 决策 | 结论 |
|---|------|------|
| D1 | 部署形态 | **单实例单进程**(ADR-GLOBAL-2)。限流、并发计数、TTL 缓存、启动清零全部用进程内存实现,不引入 Redis 方案。Casbin 多实例同步等问题不适用。 |
| D2 | 订单超时自动退款/到期释放 | **P0 立即实现**(WP1)。当前缺失会导致资金不退、库存单调归零。 |
| D3 | Microsoft 拉取粒度 | **由"按订单"改为"按邮箱资源"聚合去重**(WP3)。同邮箱多个别名订单共享一次 Graph 拉取。 |
| D4 | 邮件原始内容存储 | **停止存储 `raw_source` / `provider_payload`,`raw_body` 截断 64KB**。`.eml` 原件已在 MinIO。 |
| D5 | 数据保留 | api_logs / 幂等键 / ignored 邮件 30 天;其余邮件 180 天;inbound 原件 90 天;fetch 任务终态 14 天;order_events 永久。 |

规模假设:100W 微软主邮箱、500W 显式别名(考虑 1000W 别名)、1W 自建域名、
千万级订单与流水;用户经 OpenAPI 多线程下单与收件。
核心 SLO:下单 P99 < 300ms;邮件到达 → pickup 可见 < 5~10s。

---

## 1. Review 总结论

架构主干正确,不要动:

| 已验证正确的设计 | 说明 |
|------------------|------|
| 下单单事务编排 | 订单创建 → 分配 → 扣款 → 凭证 → 激活在同一 DB 事务(子模块 savepoint 汇入),崩溃即整体回滚,无跨系统不一致。 |
| 锁顺序全局一致 | 资源行(`FOR UPDATE SKIP LOCKED`)→ 钱包行 → 订单行,无锁序反转;外层 8 次死锁抖动重试。 |
| 一单一资源 | `allocation_order_guards` + generated column active 唯一键兜底。 |
| 钱包正确性 | 每用户钱包行锁串行化是余额正确性的必要代价(单用户约 100~200 单/s 上限),**不要**为提吞吐拆预算。 |
| 幂等 | orders 幂等唯一键(subject+key)、billing 幂等表、token OnConflict 三层闭环。 |
| 别名用时写入 | dot / plus / 自建生成邮箱均为分配时 lazy 创建,符合预期。 |
| API Key 明文可重复查看 | 现状已满足要求,不改。 |

已知可接受行为(写入 API 文档即可,不改代码):
余额不足导致订单 `failed` 后,同一幂等键重放返回该 failed 订单(幂等键一次性),
客户端充值后须换新幂等键重试。

---

## 2. 问题清单

### P0(资金 / 业务正确性 / 稳定性)

| 编号 | 位置 | 问题 | 后果 |
|------|------|------|------|
| P0-1 | trade 全模块 | 超时扫描器、到期释放、管理端 refund/terminate 全部未实现;`ReleaseByOrder` 仅在下单补偿路径调用 | 接码单收不到码永远 `active` 不退款;分配永不释放 → active 唯一键永久占位 → **库存单调归零且不可恢复**;违反 INV-T7 |
| P0-2 | `internal/mailmatch/app/ports.go` `ListOrderMail` / `ListOrderMailByServiceToken` | `if !hasSnapshot` 未区分 serviceMode,购买单写入首个快照后永久停止触发拉取 | 购买用户拿不到第二个验证码;违反 INV-M13(仅接码不重复拉取) |
| P0-3 | `internal/openapi/infra/repo.go` `ReleaseAPIKeyRequest` | `active_requests` 递减依赖请求结束 defer,进程崩溃/强杀后永久泄漏,无启动清零、无对账 | 泄漏满并发上限后该 Key 永久 503 |

### P1(并发 / 吞吐 / 延迟)

| 编号 | 位置 | 问题 |
|------|------|------|
| P1-1 | `internal/openapi/infra/repo.go` `AcquireAPIKeyRequest` | 每个 OpenAPI 请求 = 事务 + api_keys 单行 `SELECT FOR UPDATE` + UPDATE,请求结束再 UPDATE + api_logs INSERT。同 Key 所有并发请求在一行上排队两次;限流/并发计数不需要事务级强一致 |
| P1-2a | `internal/platform/platform.go` `initAsynqServer` | 全局任务并发 6(default 队列 5),邮件拉取是纯 IO 等待,全站每秒仅 1~3 个拉取任务,收件延迟雪崩 |
| P1-2b | `mailmatch_fetch_jobs.active_order_no` 唯一键 | 拉取按订单去重:同一主邮箱 10 个别名订单 = 10 次并发拉同一个收件箱,上游浪费 10 倍并触发微软风控 |
| P1-2c | `internal/mailmatch/infra/repo.go` `LoadDomainInboundMessages` + `internal/mailtransport/app/inbound.go` `Process` | 自建邮件是 SMTP push 到手的,却做成 pull:每次轮询重复读 MinIO、重复解析 MIME;入站 worker 只做 `MarkStored` |
| P1-2d | `internal/mailmatch/app/ports.go` `regexMatch` / `extractByBodyRules` | 正则每条邮件 × 每条规则 × 每次请求重新 `regexp.Compile`,pickup 是最高频接口 |
| P1-2e | `internal/mailmatch/infra/repo.go` `ListOrderMessages` | GORM 全列 `Find`,每次拉 120 条含 `raw_body`/`raw_source`/`provider_payload` 大字段;后两者对匹配与响应无用 |
| P1-2f | `internal/mailmatch/app/ports.go` 常量 | fetch 任务卡死恢复窗口 10min(`staleFetchRunningThreshold`),接码窗口一般只有几分钟,一次 worker 被杀即错过整个窗口 |
| P1-3 | `GET /v1/pickup` | 无任何限流,脚本必然多线程轮询;每次 3~5 个查询 + 可能开事务 |
| P1-4 | `internal/core/infra/resource_validation_repo.go` `MarkRunning`/`ClaimDispatchable` | `running` 僵死任务重派时 `attempts` 不递增 → **无限重派永不终态**;且 dispatcher 入队无 TaskID 去重 |
| P1-5 | `internal/core/infra/resource_validation_queue.go` | 校验任务 asynq 超时 3min,小于微软授权+代理重试+Graph+IMAP 全流程时长,超时即制造 P1-4 的僵尸任务 |
| P1-6 | `internal/mailtransport/infra/microsoft_mail_fetch.go` `outlookIMAPClient.FetchAll` | IMAP 回退直连 `outlook.office365.com:993`,忽略 `req.ProxyURL`,暴露源 IP 且与 Graph 路径行为不一致 |
| P1-7 | `internal/mailtransport/infra/msacl/auth_flow.go` `sessionDCMu` | 全局 `map[*Session][2]int` 只增不减,长期运行内存泄漏 |
| P1-8 | `internal/mailtransport/infra/inbound_smtp_server.go` | SMTP 入站无连接上限;`Data` 内同步 MinIO 上传 + DB 写 + 逐条入队,高并发入站占满 goroutine/连接池 |

### P2(规模化后的 DB 主负载与治理)

| 编号 | 位置 | 问题 | 修法摘要 |
|------|------|------|----------|
| P2-a | `internal/billing/infra/repo.go` `GetOrCreateWalletSummary` | 每次钱包页 `SUM(-amount)` + `COUNT` 扫该用户全部消费流水 | wallets 表加累计列,扣款事务内累加(WP4) |
| P2-b | `internal/core/infra/resource_repo.go` `Facets` | 12+ 个独立 COUNT,每个带 JOIN,每次资源列表全跑 | 合并为条件聚合 + 短 TTL 缓存(WP4) |
| P2-c | `internal/alloc/infra/repo.go` `GetInventoryStats` / `GetProductInventoryTotals` | 每次选项目触发十几个聚合(buyer/public 各一遍、按后缀 GROUP BY 全量资源表) | 30s TTL 进程缓存;正确性由分配事务兜底(WP4) |
| P2-d | `internal/trade/infra/repo.go` `applyOrderFilter` 等 | `LIKE '%q%'` 双端通配,千万行全扫 | 订单号/邮箱改前缀匹配(WP5) |
| P2-e | `web/src/hooks/use-block-paged-list.ts` + `maxResourceListLimit=10000` | 10k 块 + OFFSET 深分页,百万行时扫百万索引项 | seek 分页 `WHERE id < ? ORDER BY id DESC`(WP5+WP7) |
| P2-f | 全库 | api_logs / idempotency_keys / mailmatch_messages / inbound_mails / mailmatch_fetch_jobs 无保留策略 | 每日清理任务(WP6) |
| P2-g | `internal/platform/platform.go` | `SetMaxOpenConns(25)` 硬编码偏小,与 asynq/HTTP/SMTP 并发不匹配 | 150 起步走配置(WP5) |
| P2-h | `internal/alloc/infra/repo.go` `TouchMicrosoftAllocated` | 每次分配额外 UPDATE 候选诊断表(读模型,刷新任务会重建) | 删除该 UPDATE(WP5) |
| P2-i | `internal/core/infra/resource_import_repo.go` / `resource_repo.go` / `resource_validation_repo.go` | 批量导入、filter 批量删除、filter 批量建校验任务均为单个大事务,长时间持锁 | 按 1000 行分块短事务,照抄 publish 的分块模板(WP5) |
| P2-j | `internal/mailtransport/infra/inbound_mail_repo.go` `updateStatus` | `WHERE id = ?` 无状态机条件,并发时状态可倒退 | 加 `AND status IN (...)`(WP5) |
| P2-k | `internal/proxy/infra/proxy_repo.go` `selectResourceProxy` | 资源池选代理无 `FOR UPDATE`(system 池有),并发绑定重试放大 | 补 `FOR UPDATE SKIP LOCKED`(WP5) |
| P2-l | `internal/iam/app/captcha.go` `VerifyCaptcha` | Get → 比较 → Delete 三步非原子,并发可双花 | Redis `GETDEL`(WP5) |
| P2-m | `internal/core/infra/project_repo.go` `List` | 每行两个 correlated COUNT 子查询 | 改 JOIN 聚合或删除(已有 batch load)(WP5) |
| P2-n | `internal/iam/api/router.go` `FetchSession` | 每个认证请求 Redis + MySQL(含 Preload)双读 | 用户摘要短 TTL 缓存(WP4,可选) |

### PF(前端,并入 WP0 / WP7)

| 编号 | 位置 | 问题 |
|------|------|------|
| PF-1 | `web/src/pages/Dashboard.tsx` `loadOrderDetail` | `verificationCode: item.verificationCode` 用本地空值覆盖服务端已有验证码,UI 永远"等待中"并持续轮询 |
| PF-2 | `web/src/pages/Dashboard.tsx` `handleFetchOrderMail` | 验证码取"第一封带码邮件"(`find`),非按 `receivedAt` 最新 |
| PF-3 | `web/src/pages/Dashboard.tsx` `handleCreateOrder` | 批量下单每单新幂等键,部分失败(如超时未知结果)后重试会整批重下 |
| PF-4 | `web/src/pages/workbench/mailbox-client.tsx` | 轮询刷新后选中邮件被重置回第一封,打断阅读 |
| PF-5 | `fetch-control.tsx` + `order-panel.tsx` + `mailbox-client.tsx` | 同一订单可同时挂两个 FetchControl 并行轮询;无 in-flight 锁,慢响应下请求堆叠 + 竞态覆盖 |
| PF-6 | `web/src/pages/Dashboard.tsx` `refreshOrders` | 工作台订单硬编码 `limit=100`,第 101 单起不可见 |
| PF-7 | `web/src/pages/Dashboard.tsx` / `Pickup.tsx` | 异步请求无 AbortController/序号防护,快速切换时旧响应覆盖新状态 |
| PF-8 | `web/src/lib/api-client.ts` | 前端超时 600s,服务端 WriteTimeout 30s |
| PF-9 | `web/src/pages/Dashboard.tsx` `orderServiceState` | `purchase+completed → read_expired`、`refunded/failed → activation_timeout` 映射误导 |
| PF-10 | `web/src/lib/resources-api.ts` `waitForResourceImport` | 导入轮询不可取消,关弹窗后仍 1s×120 次请求 |

---

## 3. 执行计划

> 顺序:WP0 → WP1 → WP2 → WP3 → WP4 → WP5 → WP6 → WP7。
> 每个 WP 独立提测、独立上线。迁移文件编号从 00016 起。

### WP0 一行级热修(1 天)

后端:

1. **修 P0-2(购买单停止拉取)** — `internal/mailmatch/app/ports.go`:
   `listOrderMailByScope` 第三个返回值改名 `snapshotExists`;两处调用方触发条件改为
   `if !(scope.ServiceMode == "code" && snapshotExists)`。服务端 cooldown 已兜底,不会拉取风暴。
2. **修 P0-3 应急(启动清零)** — `cmd/server/main.go` 在 `RunMigrations` 之后:
   `UPDATE api_keys SET active_requests = 0 WHERE active_requests > 0`。
   (WP2 上线后该列弃用,语句保留无害。)
3. **正则缓存** — `internal/mailmatch/app/ports.go`:
   `var regexCache sync.Map`(pattern → `*regexp.Regexp`,编译失败缓存 nil 占位),
   `regexMatch` / `extractByBodyRules` 全部走 `compileCached(pattern)`。`*regexp.Regexp` 并发安全。
4. **fetch 卡死恢复提速** — 先把 `internal/mailmatch/infra/fetch_queue.go`
   `mailmatchFetchTaskTimeout` 3min → 60s,再把 `internal/mailmatch/app/ports.go`
   `staleFetchRunningThreshold` 10min → 90s。顺序不可反(stale 阈值必须 > 任务超时,避免双跑)。
5. **修 P1-4/P1-5(校验任务僵尸)** — `internal/core/infra/resource_validation_repo.go`:
   `MarkRunning` 对"从 stale running 恢复"的分支 `attempts = attempts + 1`
   (与 mailmatch `ClaimFetchJobRunning` 同款写法);
   `resource_validation_queue.go` 超时 3min → 15min,并给 Enqueue 加
   `asynq.TaskID("validation:"+jobID)` 去重。

前端(对应 PF-1/2/3/8):

6. `web/src/lib/api-client.ts`:`apiRequestTimeoutMs` 600_000 → 60_000。
7. `Dashboard.tsx` `loadOrderDetail`:
   `verificationCode: detail.verificationCode ?? item.verificationCode`(服务端优先)。
8. `Dashboard.tsx` `handleFetchOrderMail`:验证码改为按 `receivedAt` 降序取第一个带码项。
9. `Dashboard.tsx` `handleCreateOrder` 重写(同时解决串行慢与重试重下):
   - 一次点击生成 `batchId = generateIdempotencyKey()`;
   - 每单幂等键 = `` `${batchId}:${index}` ``;
   - `sessionStorage` 记录 `batchId + quantity + 已成功 index`;失败重试只补未成功下标,复用同键;
   - 发送用并发 ≤5 的 `Promise.allSettled` 分批,聚合成功数/失败原因统一 Toast;
   - 全部成功后清 sessionStorage,`refreshOrders()` + `loadProjectInventory()` 保持。

验收:购买单出码后仍持续收到新码;kill -9 重启后 API Key 立即可用;
pickup 火焰图中 `regexp.Compile` 消失;下单请求超时后重试不产生重复订单;
校验任务不再出现 `running` 无限重派。

### WP1 交易生命周期补全(P0,3~5 天)

**目标**:实现 INV-T7/T8 与读取期结束回收;补管理端命令。全部动作 DB-local,
单订单单短事务,幂等靠状态机 WHERE 条件。

1. **迁移 `00016_trade_service_expiry.sql`**:
   ```sql
   ALTER TABLE orders
       ADD INDEX idx_orders_status_receive_until (status, receive_until),
       ADD INDEX idx_orders_status_after_sale (status, after_sale_until);
   ```
2. **trade repo 新增方法**(`internal/trade/infra/repo.go`,模板照抄 `MarkFailed`):
   - `RefundActiveOrder(ctx, orderNo, refundTxID, refundAmount, reason)`:
     `lockOrder` → 校验 `status='active'` → UPDATE `status='refunded', refund_tx_id, refund_amount, version+1`
     (WHERE 带 status,RowsAffected≠1 → `ErrOrderStateConflict`)→ append `order.refunded` 事件。
     注意 `chk_orders_refund_state`:refunded 必须带 refund_tx_id。
   - `CompleteExpiredOrder(ctx, orderNo, reason)`:`active → completed`,不动金额,append 事件。
   - `CloseActiveOrder(ctx, orderNo, reason)`:`active → closed`(供管理端 terminate)。
   - `MarkServiceCleanup(ctx, orderNo, status)`:更新 `service_cleanup_status`。
   - 四条扫描查询(只读无锁):
     ```sql
     -- (a) 接码超时
     SELECT order_no FROM orders WHERE status='active' AND service_mode='code' AND receive_until < ? LIMIT 200;
     -- (b) 购买激活超时
     SELECT order_no FROM orders WHERE status='active' AND service_mode='purchase' AND activated_at IS NULL AND receive_until < ? LIMIT 200;
     -- (c) 购买质保到期
     SELECT order_no FROM orders WHERE status='active' AND service_mode='purchase' AND activated_at IS NOT NULL AND after_sale_until < ? LIMIT 200;
     -- (d) 接码读取期结束、待清理
     SELECT order_no FROM orders WHERE status IN ('completed','refunded') AND service_mode='code'
       AND service_cleanup_status='none' AND after_sale_until < ? LIMIT 200;
     ```
3. **trade app 新增 `ExpireService`** 编排(单订单):
   - **(a) 接码超时**:**先查 `mailmatch_order_snapshots` 是否已有该单快照**——
     存在说明码已命中只是通知失败,改走 `NotifyMatchedCode` 补交付,**绝不退款**
     (否则出现"拿到码还退款"的反向资损)。无快照 → 单事务:
     `wallet.RefundConsumer`(幂等键沿用 `"order:"+orderNo+":refund"`,与下单补偿共键,天然一单最多一退)
     → `RefundActiveOrder` → `allocation.ReleaseByOrder` → `tokens.DisableOrderToken`
     → `MarkServiceCleanup('succeeded')`。释放/禁 Token 失败:退款照常提交,
     置 `partial_failure` + SystemLog,由 (d) 扫描重试清理。
   - **(b) 购买激活超时**:`CompleteExpiredOrder`,不退款、不释放、不禁 Token(INV-T8)。
   - **(c) 购买质保到期**:`CompleteExpiredOrder`,同样不释放。
     (供应商结算 freeze→credit 的触发点在此预留调用位;结算模块单独排期。)
   - **(d) 接码清理**:`ReleaseByOrder` + `DisableOrderToken` + `MarkServiceCleanup('succeeded')`;
     `ErrAllocationNotFound` 视为已清理。
4. **扫描器**:`internal/trade/api/tasks.go`,单 goroutine + `time.Ticker(30s)`
   (单进程,无需 asynq/SKIP LOCKED),持可取消 context,挂到 router cleanup。
   每轮依次跑 (a)~(d),每单独立事务,单个失败记 SystemLog 继续。
5. **管理端命令**(admin 权限 `trade:order operate`,casbin 规则已存在;写 OperationLog):
   - `POST /v1/admin/orders/:orderNo/refund`(reason + Idempotency-Key 必填)
     → 复用 (a) 的退款+清理函数,允许 `active/completed` 起点(completed 即售后退款);
   - `POST /v1/admin/orders/:orderNo/terminate`(reason 必填)→ `CloseActiveOrder` + 释放 + 禁 Token。
6. **测试**(`internal/trade/api/expiry_mysql_test.go`):
   1 分钟窗口接码单不投邮件 → 扫描后 `refunded` + 回款流水 + allocation `released` + token 禁用 + 事件完整;
   已有快照的 active 接码单 → 不退款走补交付;购买激活超时 → `completed` 不退款分配保持;
   重复扫描 → 无第二笔退款流水。

### WP2 OpenAPI 鉴权内存化(P1,2~3 天,破坏性)

**目标**:API Key 认证零事务、零热点行写。正确性损失仅为崩溃时丢失 ≤5s 的 quota 计数与 api_logs。

1. **新组件** `internal/openapi/app/keycache.go`:
   ```go
   type keyState struct {
       meta       atomic.Pointer[KeyMeta] // id/userID/enabled/expireAt/rate/concurrency/quotaLimit/ownerRole/ownerEnabled
       sem        chan struct{}           // cap = ConcurrencyLimit
       window     slidingWindow           // 60s 内存滑动窗口(互斥锁+两桶计数)
       quotaBase  int64                   // 加载时 DB quota_used
       quotaDelta atomic.Int64
       loadedAt   time.Time
   }
   ```
   TTL 30s;Key 增删改时 `InvalidateAll()`(epoch+1,变更低频,最简单且绝对正确);
   owner 禁用生效延迟 ≤ TTL,注释写明。
2. **鉴权流程**:`BeginAPIKeyRequest` 改为返回 `release func()`:
   校验 → `select { case sem <- struct{}{}: default: return 503 }` → 窗口限流 →
   `quotaBase+quotaDelta >= limit` 判超额(允许个位数超卖)→ `quotaDelta.Add(1)`。
   `release = <-sem`,defer 执行,**进程内存天然无泄漏**。
   `auth.go` `LoadAPIKey` 适配;`FinishAPIKeyRequest` 删除。
3. **异步落库** `internal/openapi/infra/flush.go`:
   每 5s 逐 Key `UPDATE api_keys SET quota_used = quota_used + ?, last_used_at = ?`;
   api_logs 投带缓冲 channel(cap 10k,满则丢弃并计数),每 5s 或 500 条批量 INSERT
   (repo 新增 `CreateAPILogs`);进程退出时 flush 一次。
4. **弃用列**:`active_requests`/`window_started_at`/`window_request_count` 停止读写
   (列保留,展示接口返回内存值,后续维护窗口删列)。
   删除 `AcquireAPIKeyRequest`/`ReleaseAPIKeyRequest` 事务实现。
5. **测试**:单 Key 并发 50 / QPS 2000,api_keys 行零锁等待;并发上限精确 503;
   限流/quota 429;kill -9 重启即恢复。

### WP3 收件链路重构(P1,4~6 天,核心体验)

1. **asynq 扩容拆队列** — `internal/platform/platform.go`:
   ```go
   Concurrency: 128,
   Queues: map[string]int{"mailfetch": 8, "default": 3, "mailtransport": 1},
   ```
   拉取与 dispatcher 任务入 `mailfetch`。DB 池同步调大(WP5)。
2. **拉取按邮箱资源聚合(D3,破坏性)** — 迁移 `00017_mailmatch_fetch_by_resource.sql`:
   ```sql
   ALTER TABLE mailmatch_fetch_jobs
       DROP INDEX idx_mailmatch_fetch_jobs_active_order,
       DROP COLUMN active_order_no,
       ADD COLUMN active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
           CASE WHEN status IN ('pending','queued','running') THEN email_resource_id ELSE NULL END
       ) STORED,
       ADD UNIQUE INDEX idx_mailmatch_fetch_jobs_active_resource (active_resource_id);

   CREATE TABLE mailmatch_resource_fetch_states (
       email_resource_id BIGINT UNSIGNED PRIMARY KEY,
       last_job_id BIGINT UNSIGNED NULL,
       last_status VARCHAR(32) NOT NULL DEFAULT '',
       last_submitted_at DATETIME NULL,
       last_success_at DATETIME NULL,
       last_received_at DATETIME NULL,
       cooldown_until DATETIME NULL,
       last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
       created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
       updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
       CONSTRAINT fk_mm_resource_states_resource FOREIGN KEY (email_resource_id) REFERENCES email_resources(id) ON DELETE CASCADE
   ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

   DROP TABLE mailmatch_order_fetch_states;
   ```
   `SubmitFetch` 的去重/cooldown/`sinceAt` 全部按 `email_resource_id` 维度;
   `order_no` 列仅记录触发者供诊断。`ProcessFetch` 主体不变
   (`fetchedMessageToDomain` 本就按收件人匹配该邮箱**所有**活跃订单)。
   pickup 响应 `fetch` 字段从资源态映射,JSON 结构不变。
3. **自建域名邮件 push 化**:
   - mailmatch 新增 `IngestInboundMail(ctx, emailResourceID, recipient, envelopeFrom, raw []byte, receivedAt)`,
     内部复用 `parseInboundFetchedMessage`/`readMIMEBody`,再走从 `ProcessFetch` 抽出的
     `ingestMessages(ctx, []FetchedMessage)`(= 匹配 + `UpsertMessages` + 快照 + `NotifyMatchedCode`);
   - `mailtransport` `InboundService.Process`:`ClaimProcessing` 成功后读 MinIO →
     调 `InboundConsumerPort.IngestInboundMail` → 成功 `MarkStored`;失败按现有 retry/final;
     Port 在 `api/router.go` 装配(mailmatch 模块创建后 `mailMod.SetInboundConsumer(...)`);
   - 删除 `LoadDomainInboundMessages` 与 `fetchMessages` 的 domain 分支;
     domain 订单 `SubmitFetch` 返回 `Accepted=false, Reason="push_only"`。
4. **pickup 瘦身与限流**:
   - `ListOrderMessages` 显式列(排除 `raw_source`/`provider_payload`);`messageScanLimit` 120 → 40;
   - 项目规则 + `loose_match` 进程 TTL 缓存 10s(用 WP4 memcache 助手);
   - pickup 进程内限流:`map[tokenPrefix]*rate.Limiter`(`golang.org/x/time/rate`,
     1 QPS / burst 3,LRU 上限 10 万条),超限 429 + `Retry-After`。
5. **消息减脂(D4)**:`messageModelFromDomain` 不再写 `raw_source`/`provider_payload`,
   `raw_body` 按字节截断 64KB(UTF-8 边界回退);迁移 `00018`:
   `ALTER TABLE mailmatch_messages DROP COLUMN raw_source, DROP COLUMN provider_payload;`
6. **Microsoft 协议路径顺手修(P1-6/P1-7/P1-8)**:
   - IMAP 回退经 `req.ProxyURL` 拨号(SOCKS5/HTTP CONNECT),无代理可用时明确失败并记 SystemLog,不静默直连;
   - msacl `sessionDCMu`:interval 改存 `Session` 结构体字段,删全局 map;
   - SMTP 入站:补连接上限(信号量包 `Accept`)与 shutdown context;
     孤儿 MinIO 对象由 WP6 GC 兜底,不做补偿事务。

验收:同一主邮箱 10 个别名活跃订单一个周期只有 1 条 fetch job / 1 次 Graph 调用;
自建域名 SMTP 投递 → pickup 可见 < 3s;pickup P99 命中快照 < 20ms / 扫消息 < 60ms;
pickup 超频 429;IMAP 回退流量走代理。

### WP4 读模型缓存与钱包累计列(P2,2 天)

1. **通用 TTL 缓存助手** `internal/platform/memcache.go`(约 40 行):
   `TTLCache[K comparable, V any]`,`RWMutex + map[K]entry{v, expireAt}`,惰性过期,无后台 goroutine。
2. **库存缓存**:`GetInventoryStats` / `GetProductInventoryTotals` 外包 30s TTL
   (key = projectID|buyerUserID;buyer=0 公共池共享)。下单正确性由分配事务实时校验,与展示值无关。
3. **facets 合并 + 缓存**:`Facets` 重写为每资源类型一条条件聚合
   (`COUNT(*)`, `SUM(status='normal')`, `SUM(for_sale=0)`, `SUM(long_lived=1)`, `SUM(graph_available=1)` ...
   复用 `listQuery` 的 WHERE 去掉对应组自身筛选),suffix/TLD 保持单条 GROUP BY;
   整体 10s TTL(key = owner + filter 指纹)。
4. **钱包累计列** — 迁移 `00019`:
   ```sql
   ALTER TABLE wallets
       ADD COLUMN total_spend DECIMAL(18,2) NOT NULL DEFAULT 0.00,
       ADD COLUMN spend_count BIGINT UNSIGNED NOT NULL DEFAULT 0;
   UPDATE wallets w LEFT JOIN (
       SELECT user_id, COALESCE(SUM(-amount),0) s, COUNT(*) c
       FROM wallet_transactions WHERE balance_bucket='consumer' AND direction='out' GROUP BY user_id
   ) t ON t.user_id = w.user_id
   SET w.total_spend = COALESCE(t.s,0), w.spend_count = COALESCE(t.c,0);
   ```
   `createConsumerTransaction` 的 out 分支同事务累加(钱包行本就已锁);
   `GetOrCreateWalletSummary` 删 SUM/COUNT 直读列。
5. (可选)会话用户摘要 5s TTL 缓存,减少每请求 MySQL 读(P2-n)。

### WP5 配置、SQL 与事务细节(1 天)

1. 连接池走配置:`MYSQL_MAX_OPEN_CONNS` 默认 150、idle 50;
   部署文档注明 MySQL `max_connections >= 300`、`innodb_buffer_pool_size` = 内存 60~70%。
2. 搜索前缀化:trade `applyOrderFilter` 与 billing `applyTransactionFilter` 的
   like 参数 `"%"+q+"%"` → `q+"%"`(订单号/流水号/邮箱前缀语义足够;资源页模糊搜索保留双端)。
3. **资源列表 seek 分页(破坏性,配 WP7-3)**:`GET /v1/resources` 增 `afterId`,
   传入时忽略 offset:`WHERE email_resources.id < ? ORDER BY id DESC LIMIT ?`
   (排序由 `created_at DESC,id DESC` 改纯 `id DESC`,自增 id 与创建时间单调一致);
   响应加 `nextAfterId`。total 与 facets 不变。
4. `TouchMicrosoftAllocated` 删除对 `microsoft_routing_candidates` 的 UPDATE。
5. **大事务分块(P2-i)**:导入 `createMicrosoftBatchTx`、filter 批量删除
   `deleteMicrosoftByFilterWithLog`、filter 批量建校验任务 `CreateBatchWithLog`
   全部改 1000 行/块独立短事务(照抄 `publishMicrosoftByFilterWithLog` 模板);
   导入进度累计写 `imported_count`,块间可重入。
6. `inbound_mails.updateStatus` 加 `AND status IN (...)` 状态机条件(P2-j)。
7. `selectResourceProxy` 补 `FOR UPDATE SKIP LOCKED`(P2-k)。
8. captcha `VerifyCaptcha` 改 Redis `GETDEL` 原子取删(P2-l)。
9. `project_repo.List` 删两个 correlated COUNT 子查询(P2-m)。
10. `cmd/server/main.go`:`ListenAndServe` 错误改经 error channel 由 main 统一 graceful shutdown,
    不在 goroutine 内 `os.Exit`。

### WP6 数据保留(D5,1 天)

`internal/governance/api/tasks.go` 单 goroutine,每天 04:00(Asia/Shanghai);
每规则循环 `DELETE ... LIMIT 5000` 至 0 行,轮间 sleep 200ms;每轮 SystemLog 汇总:

| 表 | 条件 | 保留 |
|----|------|------|
| `api_logs` | created_at < 30 天前 | 30 天 |
| `idempotency_keys` | 同上(`idx_idempotency_created`) | 30 天 |
| `mailmatch_messages` | `status='ignored'` 且 received_at < 30 天前 | 30 天 |
| `mailmatch_messages` | received_at < 180 天前 | 180 天 |
| `mailmatch_fetch_jobs` | 终态且 updated_at < 14 天前 | 14 天 |
| `inbound_mails` | created_at < 90 天前,先删 MinIO 对象再删行;顺带 GC 无 DB 记录的孤儿对象 | 90 天 |
| `order_events` | 不删 | 永久 |

### WP7 前端(2~3 天)

1. WP0 已含:超时 60s、验证码覆盖/取最新、批量下单幂等重试。
2. **工作台轮询单例化(PF-4/5/7)**:
   - 轮询上提到 Dashboard,按 `orderNo` 单例调度(同一订单同时只有一个 in-flight
     `readPickupMail`;弹窗打开时 accordion 内实例不再发请求);
   - 每次请求带序号 token,响应落地前校验仍为当前 orderNo/email,过期丢弃;
     `Pickup.tsx` 同理;
   - `FetchControl` 改纯展示倒计时(单一模块级 ticker),`autoEnabled=false` 时不跑 interval;
   - `mailbox-client.tsx` 选中逻辑:仅 `email` 变化时重置;刷新后若 `selectedMessageId`
     仍存在则保留。
3. **块分页适配 seek(P2-e,配 WP5-3)**:`use-block-paged-list.ts`
   `loadBlock(offset, limit)` → `loadBlock(cursor {afterId?}, limit)`,块缓存记录每块
   `nextAfterId`;五个使用方只改 loadBlock 适配层,分页 UI 不变。
4. **订单列表(PF-6)**:工作台 `refreshOrders` 加"加载更多"(offset 翻页,每页 100),
   或按当前 serviceMode 过滤拉取;创建/取信后增量 merge 保持。
5. **状态映射(PF-9)**:`orderServiceState` 为 `completed(purchase)`/`refunded`/`failed`
   增加独立 ServiceState 与文案(展示层修正,不动后端语义)。
6. **导入轮询可取消(PF-10)**:`waitForResourceImport` 接受 `AbortSignal`,
   Modal unmount 时 abort;busy 时 Cancel 改"后台继续"提示。
7. 对 429 响应静默处理,读 `Retry-After` 顺延下次轮询。

---

## 4. 明确不做清单

| 项 | 理由 |
|----|------|
| Redis 限流 / 分布式锁 | 单进程内存更简单更快;正确性从不依赖限流层 |
| 钱包分桶 / 余额预算拆分 | 单用户钱包行串行是正确性特性,吞吐足够 |
| 下单事务改 saga / 消息驱动 | 单 DB 事务是当前架构最大优点 |
| 邮件正文拆表 / 分库分表 | 减脂 + 保留策略后单表规模可控,现有索引足够 |
| 字段级加密、APIKEY 一次性显示 | 与 ADR-GLOBAL-5 一致,现状已满足 |
| MinIO 写入补偿事务 | 孤儿对象由 WP6 定期 GC 兜底,足够 |
| 前端引入 TanStack Query 全面改造 | 收益不抵改造面;先用请求序号+单例轮询解决竞态;未使用依赖(react-query/zustand)从 package.json 移除 |
| Casbin 多实例同步 | 单进程部署,不适用 |

---

## 5. 上线与回归

1. 顺序:WP0(热修)→ WP1(资金正确性)→ WP2 → WP3(破坏性迁移,选低峰)→ WP4~WP7。
2. WP3 迁移前提:fetch 任务队列排空(停写 30s 即可,首版数据量小)。
3. 回归重点:
   - 下单幂等(同键重放/换键)、余额不足/库存不足的 failed 提交语义;
   - 接码全链路:下单 → 投递 → 匹配 → 快照 → 完成 → 读取期结束释放;
   - 超时链路:接码退款(含"已有快照不退款走补交付")、购买激活超时完成、质保到期完成;
   - OpenAPI 限流/并发/quota 三种 429/503;pickup 429;
   - 崩溃恢复:kill -9 后 Key 可用、running fetch 90s 内重派、校验任务不无限重派;
   - 前端:批量下单中断重试不重复、慢响应不覆盖新选中订单。
4. 观测:沿用 pprof + SlowSQLThreshold;WP2/WP3 上线后各压测一轮
   (下单 P99、pickup P99、单邮箱多订单拉取次数)留存基线。
