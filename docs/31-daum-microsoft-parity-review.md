# Daum 与管理员微软邮箱的机制一致性复核

日期：2026-10-17。基准是当前工作区中的 Microsoft 实现，包括其调用链，而不是旧设计文档或页面外观。本轮只读审查业务代码；临时复现文件保存在 `/tmp/daum-ms-parity-review-0ij0e9xz/`。

本文件保留修复前的审查事实。用户后续授权修复后的实现、别名收益规则及验收结果见 [32-daum-parity-fixes.md](32-daum-parity-fixes.md)。

## 结论与验收边界

当前 Daum **不满足“除供应商必要差异外，完全沿用微软机制”的验收要求**。已经复用了管理员页面基础结构、部分邮件面板、ProxyUseCase、IMAP TLS 拨号、商品与交易入口，但资源生命周期、导入、任务、辅助关系和部分财务策略是独立实现。部分实现参考了其他邮箱模块，不能将其描述为微软后端的逐项复制。

差异分为三类：

1. 用户明确要求或供应商协议必要差异：三个固定地址、2/3/5 段凭据、APP 密码、IMAP/POP3、Kakao 账号与已有取件 URL、仅管理员资源管理页。
2. 未事先明确说明的实现选择：限额、调度、状态统计、失败阈值、正文选择、额外锁、编辑入口等。这些不是协议必然限制。
3. 上轮修复引入的安全或可靠性加强：退款证据复核、源订单辅助授权、任务完成后的版本刷新。必须披露；不能为表面一致而删除这些保护，宜统一到共享机制。

以下按多 worker、共享 MySQL/Redis、持续批量导入和在线取件的实际架构判断。本轮没有执行生产修改。

## 首先需要对齐的问题

### R1. 实时收件认定失效及退款的时机不同

- Microsoft：一次抓信最多三次协议尝试；一个完整失败轮次只累计一次不可重试失败。资源计数在 Redis 保留一小时，成功清零，累计三轮才标异常并进入退款。Redis 计数不可用时不把零证据当永久失效。
- Daum：非连接类失败不在当前轮次重试；第一次明确的凭据失效即持久化异常和永久失效分类，随后允许立即退款。
- 影响：相同的首次认证拒绝，两个商品对用户订单的处理时机不同。这是财务策略差异，不是 APP 密码必然要求。
- 对齐方法：共用抓信失败累计、恢复清零和处理阈值；保留 Daum 已有的事务内退款证据复核。
- 证据：`internal/mailmatch/api/fetch_adapter.go:22,86,225`、`internal/mailmatch/api/deps.go:141`；`internal/daum/infra/network.go:88`、`internal/daum/infra/mailbox.go:90`、`api/daum_resource_adapter.go:141`。

### R2. 同一封邮件选择不同正文，可能漏掉验证码

- Microsoft 的 MIME 读取优先 HTML，Daum 优先纯文本。
- 已用同一封 multipart/alternative 邮件复现：纯文本是提示语，HTML 包含 `847291`。Microsoft 正文包含验证码，Daum 正文不包含，因此同一项目规则可能得到不同匹配结果。
- 对齐方法：复用同一 MIME 正文选择和字符集解码规则，不在 Daum 再维护独立优先级。
- 证据：`internal/mailtransport/infra/msacl_mailbox_reader.go:224,257`；`internal/daum/infra/protocol/client.go:470,497`。

### R3. 修改辅助邮箱的权限不同

- Microsoft：辅助修改要求资源写权限及 `mailtransport:binding/write`；只有改供货或凭据才额外检查 `core:resource/operate`。
- Daum：修改 `bindingAddress` 或 `pickupUrl` 还要额外通过 `operate`。
- 影响：同一个具有资源写与辅助写权限的管理员，在微软可以操作，在 Daum 会收到 403。
- 对齐方法：按实际被修改字段使用相同的权限组合，凭据与辅助资料分别判断。
- 证据：`internal/core/api/admin_resource_handler.go:488`；`internal/daum/api/handler.go:576`。

### R4. 缺少解除辅助关联的能力

- Microsoft 编辑支持清空辅助地址，通过 nullable 字段解除关联。
- Daum 已绑定 URL 时地址只读，空 URL 表示保留；后端最终始终要求有效邮箱，无法像微软那样把关联清空。
- 影响：错误绑定或失效链接只能替换，无法撤销本地关联。
- 对齐方法：复制微软的“不提供／设置／明确清除”三种输入语义，清除关系时保留原邮箱、订单和 Token。
- 证据：`web/src/pages/admin-microsoft/microsoft-modals.tsx:517`、`internal/core/api/admin_resource_handler.go:994`；`web/src/pages/admin-daum/daum-modals.tsx:131,163`、`internal/daum/infra/bindings.go:107,130`。

### R5. 导入规模和客户端恢复机制被缩减

- Microsoft 管理页允许最多 512 MiB 文件，超过阈值自动按最多 99 MB 拆分，最多三个并发上传，记录各批幂等键、importId 和完成状态。后端单请求默认 100 MiB，受全局设置及硬上限约束。
- Daum 前后端硬封顶 32 MiB，整份内容单次上传，没有对应客户端分批续传。后台任务的内部恢复不等于这个客户端能力已经存在。
- 影响：相同大库存文件需要在 Daum 手工拆分；全局导入限制的配置作用也不同。
- 对齐方法：复用微软的拆分、批次状态和上传调度，只替换解析格式和 API。
- 证据：`web/src/pages/admin-microsoft/microsoft-modals.tsx:48`、`web/src/pages/resources/microsoft-import-batches.ts:22,91`、`internal/core/api/handler.go:41`；`web/src/pages/resources/import-daum-emails-modal.tsx:23,177`、`internal/daum/api/handler.go:395`。

### R6. 历史识别的范围、超时和并发不同

- Microsoft 读取 Inbox 与 Junk，并要求这两类目录完整；Daum 全历史会遍历所有可选择目录，包括归档、已发送等。
- Microsoft 历史超时读取 `imap_full_history_timeout_minutes`，最高两小时；Daum 外层仍有固定 15 分钟截止，即使协议层读取同名设置，调成 30 分钟也不能突破外层。
- Daum 另有固定 16 分钟的账号级 Redis 锁，验证、实时收件和历史互斥；Microsoft 共享实时租约默认两分钟且有续租，历史路径没有这一道跨操作独占锁。
- 影响：识别依据、配置有效范围、长历史扫描期间在线取件可用性不同。更广范围和更严格互斥都是此前的实现选择。
- 对齐方法：明确并复用相同的历史目录范围、超时来源和任务互斥规则；保留凭据修订检查。
- 证据：`internal/mailtransport/infra/microsoft_mail_fetch.go:688,825,914`、`internal/mailmatch/app/project_history_scan.go:259`、`internal/mailmatch/app/ports.go:42,1079,1377`；`internal/daum/infra/protocol/client.go:154`、`internal/daum/infra/history.go:19,208`、`internal/daum/infra/mailbox.go:17,35,74`、`internal/daum/infra/validation.go:66`。

### R7. 已接入代理，却缺少微软的代理诊断

- Microsoft 详情展示代理 ID、地址、IP 版本、状态、出口 IP、到期时间；Daum 的 DTO 和详情未提供对应信息。
- 影响：Daum 已依赖代理，但管理员不能从邮箱详情定位绑定、到期或出口问题。
- 对齐方法：复用微软的代理摘要查询和展示，仅按 Daum 账号键查询。
- 证据：`web/src/pages/admin-microsoft/microsoft-detail-sheet.tsx:134`；`web/src/pages/admin-daum/daum-detail-sheet.tsx:70`、`internal/daum/infra/network.go:31`。

后续用户要求 Daum 默认服务器 IPv4 直连，生产装配已移除 Daum 的代理服务及绑定查询；此项保留为审查时的历史记录，当前 Daum 详情不查询无关代理绑定。

## 网络与收件：不能笼统称为完全一致

| 项目 | Microsoft | Daum | 判断 |
|---|---|---|---|
| 代理获取与账号绑定 | ProxyUseCase，规范化邮箱键、资源代理与系统备用、失败服务器排除 | 审查时复用同一服务；用户后续要求默认直连，已移除代理获取及健康反馈 | 用户明确要求的路由差异，微软策略不变 |
| IP 用途 | 验证 IPv4，实时 IPv4，历史 IPv6；网页绑定还有 Binding 用途 | 审查时历史使用 IPv6；经 IPv4/IPv6 代理实测后，用户要求验证、实时及历史全部默认服务器 IPv4 直连 | 使用 tcp4，共用 TLS 校验和超时，无网页绑定流程 |
| IMAP TLS | `netdial.DialTLS`、Go TLS、最低 TLS 1.2、证书验证、SNI、imap ALPN | 同一代码；POP3 使用 pop3 ALPN、Daum 主机名 | 邮件协议层共用，目标及 ALPN 必须按协议改变 |
| 网页及 Graph 指纹 | 浏览器／API HTTP 客户端使用 tls-client 浏览器 profile、Cookie、HTTP 头；Graph API 使用 Chrome profile | 没有这些 HTTP 调用，仅 IMAP/POP3 | 不应声称浏览器指纹也完全一致 |
| 无代理时 | IMAP 拒绝空代理；Graph／网页路径可接收 Direct 配置继续请求 | 当前所有 Daum 操作默认直连，不依赖代理池是否存在 | 用户明确授权的后续差异；取消终止后续尝试，微软策略不变 |
| 连接尝试预算 | 实时循环固定最多 3 次，ProxyUseCase 内再受配置约束；验证读取配置 | 当前验证每任务 1 次直连，实时及历史每轮最多 3 次；不使用 `max_proxy_attempts` | 验证外层仍由 `resource_validation_max_failures` 限制失败轮次，默认 3 轮 |
| 服务端错误的本轮重试 | 实时适配器可以继续当前轮次的剩余尝试 | 仅 transport 错误在本轮换代理重试，provider_unavailable 等直接交上层 | 重试机制不同 |
| 增量边界 | 遇到已知最新邮件停止向旧邮件读取；全局按时间排序和上限截取 | 过滤已知邮件后继续找旧的未知邮件；协议客户端上限按目录计算 | 已复现边界差异，会改变抓取范围及开销 |
| 原始邮件限制 | 当前 IMAP 路径没有 Daum 这项 8 MiB 拒绝条件 | 超过 8 MiB 返回失败，验证／历史也可能受影响 | Daum 自加限制，非上游协议要求 |
| 管理员全量抓信落库 | 本轮完整抓取成功后进入 ingest | OnMessages 收到一批就带版本检查落库；协议当前逐封回调 | 中途失败时 Daum 已落库部分仍保留，微软该轮尚未 ingest |
| 断线后流式重试 | 历史累计器支持 OnReset，可清空累计结果重试 | 已开始流式输出就不在当前网络循环重放 | 另一项独立的失败恢复策略 |
| 无 Message-ID 去重 | 使用现有 provider 身份／兜底键 | 新增原始 MIME 规范化 SHA-256，跨 IMAP/POP3 复用 | 为双协议添加的实现，微软没有相同分支 |

证据：`internal/mailmatch/api/fetch_adapter.go:86,156,291`；`internal/mailtransport/infra/microsoft_mail_fetch.go:382,644,856,1218`；`internal/mailtransport/infra/msacl/session.go:179,195,220`；`internal/daum/infra/network.go:31,48,60,88`；`internal/daum/infra/protocol/client.go:27,180,192,390,400`；`internal/mailmatch/app/resource_fetch.go:338,369`；`internal/mailmatch/app/daum_resource_fetch.go:29,55`；`api/daum_resource_adapter.go:104`。

## 管理员页面双向差异

### 微软已有，Daum 缺少或入口不同

| 能力 | Daum 当前处理 | 性质与证据 |
|---|---|---|
| 大文件拆分及批次续传 | 32 MiB 单文件单请求 | 非协议缺项，见 R5 |
| 一次编辑提交资料＋供货＋凭据 | 供货、凭据要另外操作 | 能力存在，但操作组合与原子性不同。MS `microsoft-modals.tsx:520,629,652`；Daum `daum-modals.tsx:125,191` |
| 主账号地址纠错 | UI 只读，后端拒绝改成另一个规范化账号 | 自加限制。MS `microsoft-modals.tsx:575`；Daum `daum-modals.tsx:150`、`internal/daum/infra/query.go:300` |
| 解除辅助关联 | 只能保留或替换 | 实际缺项，见 R4 |
| 代理诊断 | 未返回／展示 | 实际缺项，见 R7 |
| 基本信息页最新任务摘要 | 必须切换任务标签查看 | 自主简化。MS `microsoft-detail-sheet.tsx:221`；Daum `daum-detail-sheet.tsx:70` |
| OAuth、RT、Graph 健康／到期／刷新 | 无对应字段和任务 | Microsoft 特有；APP 密码接收不应伪造这些能力 |
| 动态别名创建、周／年额度、dot／plus | 仅两个固定显式别名，核对合并进验证 | 符合用户明确的 Daum 地址规则，不应添加无依据的远端创建机制 |

上表前端短路径均相对 `web/src/pages/admin-microsoft/` 或 `web/src/pages/admin-daum/`。

### Daum 已有，微软管理员页没有同等入口

| 能力 | 原因与实现 |
|---|---|
| 独立 Kakao 账号、已有取件 URL、由 URL 得到实际辅助地址 | 用户要求；独立字段和关联表，不把第五段误当实际辅助邮箱 |
| IMAP／POP3 选择及 APP 密码 | 用户要求和供应商协议适配；完整历史仍转 IMAP |
| 精确归属人选择器筛选 | 自主增强；`AdminDaumEmails.tsx:176`，MS 对应页 `:185` 主要依赖搜索 |
| 导入逐行结果及失败 CSV 面板 | 自主增强；`web/src/pages/resources/daum-import-result-panel.tsx:26,80`，MS 管理导入主要展示拆分批次进度 |
| 列表上的当前批任务进度条 | 自主增强；`AdminDaumEmails.tsx:1191`、`daum-bulk-task-progress.tsx:24`，MS 页面主要用结果提示及 API 等待 |
| 任务终态、首次即终态、409、迟到响应的资源刷新 | 上轮缺陷修复；Daum `daum-detail-sheet.tsx:390`，MS `microsoft-detail-sheet.tsx:754` 没有同等父详情终态刷新逻辑 |
| 主邮件页完整地址过滤器与主／别名标记 | 自主展示增强；同一 ResourceMailsPanel，Daum 未设置微软使用的 hideMailboxMeta。MS `microsoft-detail-sheet.tsx:1626`；Daum `daum-detail-sheet.tsx:697` |

### 同名交互及配置的额外差异

- 后缀：Microsoft 用独立标签和主账号实际后缀统计；Daum 用筛选菜单并匹配固定别名，一个账号会匹配三个后缀。三地址统计语义有业务理由，菜单布局变化不是协议要求。
- 错误中止：Microsoft 管理页把原文交给服务端导入；Daum 前端预检失败可以直接阻止建立批次。后者因此没有这次服务器批次的失败下载。证据：MS `microsoft-modals.tsx:236`；Daum `import-daum-emails-modal.tsx:158`。
- 禁用所选：Microsoft 状态批处理同步完成；Daum 统一进入异步 bulk，受理不代表状态已改变。证据：`internal/core/api/admin_resource_handler.go:615`；`web/src/lib/admin-daum-api.ts:98`。
- 验证／扫描项目：Microsoft 这两个入口不传前端版本；Daum 统一要求 version，可能返回 409。证据：`web/src/lib/admin-microsoft-api.ts:265,405`；`web/src/lib/admin-daum-api.ts:85`。
- 协议筛选：Daum 后端接受 protocol，前端尚无对应筛选项，无法按 IMAP/POP3 筛选后批量维护。证据：`internal/daum/api/handler.go:270`；`web/src/pages/admin-daum/admin-daum-types.ts:34`。
- 部分共用 IMAP 参数仍放在“微软邮箱运维”页面，当前也会影响 Daum，页面名称没有说明真实作用范围。证据：`web/src/pages/system-settings/microsoft-ops.tsx:31`；`internal/daum/infra/protocol/client.go:40`。

## 导入、状态、任务和数据库的进一步对照

以下均为已确认差异；与 R5、R6 重复的容量／锁／超时不再另算。

| 机制 | Microsoft | Daum | 性质、影响和证据 |
|---|---|---|---|
| TXT 字段格式 | 2/3/4/5 段；三段中的第三段是辅助邮箱，长格式包含 Client ID/RT | 用户指定的 2/3/5 段 APP 密码格式 | 用户明确要求，必须保留。MS `internal/core/infra/txt_parser.go:27`；Daum `internal/daum/infra/parser.go:26` |
| 通用解析限制 | 没有同等 BOM 处理、整行 8 KiB 限制 | 去 BOM、整行 8 KiB、密码 UTF-8/控制字符检查 | 自主强化；通用输入安全规则可共用，不应简单删除校验。双方 parser 同上 |
| 已删除账号跨归属恢复 | 管理员重新导入可用新归属人恢复 | 在 deleted 判断前即拒绝不同归属 | A 已删账号导入给 B，结果不同。MS `internal/core/infra/resource_repo.go:775,899`；Daum `internal/daum/infra/service.go:173` |
| 恢复时未提供的凭据 | 按当前输入重写，缺 RT 可清空旧值 | 两段恢复保留登录密码、Kakao、辅助资料等未提供值 | Daum 为短格式做的保留策略，应明确披露。MS `resource_repo.go:775`；Daum `service.go:188` |
| accepted 计数 | 有效且去重后的可导入行 | 所有非空行，包括格式错误、重复行 | 3 条有效＋2 条错误，分别是 3 和 5。MS `internal/core/app/ports.go:586`；Daum `internal/daum/infra/imports.go:205` |
| 服务端 abort 诊断 | 遇到首错返回并记录首错 | 先扫描所有行，汇总错误，再拒绝写资源 | 都不写错误批次，但失败文件和计数不同。MS `txt_parser.go:56`、`ports.go:489`；Daum `imports.go:205,272` |
| 幂等导入重放顺序 | 先查已有批次，再决定是否存原件 | 先存新原件，识别重放后删除多余文件 | 存储故障时，已成功请求的重放在 Daum 也可能失败。MS `ports.go:310`；Daum `internal/daum/api/handler.go:446`、`imports.go:108` |
| 导入基础设施失败 | 重试耗尽后推进代次、回 pending，不消费业务导入次数 | 领取即增加 attempts，固定最大 3 次后批次 failed | 暂时存储故障的恢复能力不同。MS `internal/core/api/tasks.go:250`、`internal/core/app/ports.go:899`；Daum `imports.go:131,174` |
| 普通批量显式 ID 上限 | `admin_resource_bulk_max_ids`，最高 1000；专门验证批量另有自己的限制 | 固定 10000，不读同一设置 | 将全局上限改为 200 后，Daum 仍可接受 500。MS `internal/core/app/admin_resource_bulk.go:149`；Daum `internal/daum/infra/bulk.go:98` |
| 筛选批量范围冻结时点 | worker 第一页确定资源 ID 上界 | HTTP 提交时确定上界及匹配数量 | 排队期间新增资源是否被纳入不同。MS `admin_resource_bulk.go:280`；Daum `bulk.go:166` |
| 无操作的批量结果 | 已是目标状态计 skipped/already_target | 无错误就计 affected，即使未改变状态 | 重复发布 100 个公开账号，分别是 affected=0 与 100。MS `admin_resource_bulk.go:346,531`；Daum `bulk.go:372` |
| 批量事务边界 | 普通状态页按页事务 | 每行独立命令事务，整页结束才存进度 | 第 50 行故障时，Daum 前 49 行可能已提交而进度仍在页首。双方同上 |
| 手动验证重置诊断 | 重置验证状态／代次，保留旧质量分等信息 | 质量分清零，最近检查时间清空，且要求前端 version | 展示状态与命令受理条件不同。MS `internal/core/domain/resource.go:179`；Daum `internal/daum/infra/query.go:400`、`commands.go:68` |
| validating 的含义 | 成功入队后即进入 validating | worker 真正开始才进入 validating | Daum 为重复入队竞态所作修复，应先统一安全的队列状态含义。MS `internal/core/infra/resource_validation_redis_repo.go:435`；Daum `validation.go:52` |
| 验证调度窗口 | 配置 `validation_dispatch_maximum`，默认 128、最高 10000 | 调度方法固定封顶 100 | 同一容量设置不能一致控制。MS `internal/core/api/tasks.go:72`；Daum `internal/daum/infra/dispatch.go:15` |
| 未知验证错误的默认重试 | 按类别黑名单重算 Retryable，未列出通常继续预算 | 采用协议 Failure.Retryable；未知协议拒绝可第一次就异常 | 同为默认 3 次预算，触发规则仍不同。MS `internal/core/app/validation.go:571,691`；Daum `internal/daum/infra/validation.go:126` |
| 历史执行预算 | 队列 MaxRetry=3，即首次＋最多 3 次重试；任务默认 20 分钟且可配 | 维护运行 MaxAttempts=3；历史队列任务固定 30 分钟 | 执行次数和超时含义不同。MS `internal/mailmatch/infra/fetch_queue.go:153`；Daum `internal/daum/app/service.go:85`、`internal/daum/infra/maintenance.go:71` |
| 项目历史断点 | 过程游标主要随 Asynq，projects 保存状态；恢复新代次可能重扫 | 独立表保存 after_id/through_id，逐资源提交断点 | Daum 多了持久恢复能力，未直接复制 MS。MS `internal/mailmatch/infra/project_history_scan_repo.go:177`；Daum `internal/daum/infra/project_history.go:19,230` |
| 项目历史失败预算范围 | 当前扫描代次累计，整个项目完成后清零 | 每成功处理一个资源就清零 | 多个不同资源各偶发一次错误时结果不同。MS `project_history_scan_repo.go:193`；Daum `project_history.go:230` |
| 历史收件人证据 | 单一 To 收件人；不以 Cc/Bcc 或多 To 形成同等历史事实 | 汇总本账号三个地址的 To/Cc/Bcc，逐地址记录 | 仅抄送或多收件人旧邮件可让 Daum 库存被标已使用，MS 不同；这是业务规则差异。MS `internal/mailmatch/app/history_match.go:123`、`project_history_scan.go:553`；Daum `internal/daum/infra/history.go:309` |

数据库也没有逐表复制：

| Microsoft 当前事实存放处 | Daum 当前对应 | 必须说明的结构差异 |
|---|---|---|
| `email_resources`＋`microsoft_resources` | 同一根表＋`daum_resources` | 根 ID 共享；Daum 子表额外有 APP 密码、协议、Kakao、failure_category、自己的归属和 version 等字段 |
| `resource_imports`／`resource_import_items` | `daum_resource_imports`／`daum_resource_import_items` | 独立表，failed_count、dispatch_attempts、逐行 failed 及计数语义不同 |
| `admin_resource_command_receipts` | `daum_command_receipts` | 都做幂等，但复合键、subject/operation 与 resource_id/command 的结构不同 |
| 资源行上的验证状态 | `daum_maintenance_runs` | Daum 额外保存逐次验证／历史运行记录；不是 MS 同名运行表的复制 |
| `projects.history_scan_*` | `daum_project_history_scan_states` | Daum 额外的专用状态表和持久游标 |
| `explicit_aliases`、动态别名及 dot/plus 事实 | 两条固定 `daum_explicit_aliases` | 用户固定地址规则需要不同模型；没有照建无意义的 RT／动态额度或旧兼容表 |
| 微软绑定及恢复事实 | `daum_binding_mailboxes`＋`kakao_email` | Daum 本地授权关联不是微软远端绑定验证的等价事实 |
| `microsoft_allocations` | `daum_allocations` | Daum 额外有 owner_user_id、service_mode、cost_points_snapshot，来自其他邮箱分配模型；实际结算仍用公共订单／guard 快照 |

结构证据：`internal/core/infra/resource_import_repo.go:19,52`、`internal/core/infra/admin_resource_repo.go:43`、`internal/mailmatch/infra/project_history_scan_repo.go:18`、`internal/governance/infra/task_view_repo.go:553,1411`；`internal/daum/infra/service.go:34`、`imports.go:23,56`、`commands.go:18`、`maintenance.go:44`、`project_history.go:19`；`internal/alloc/infra/repo.go:195` 对比 `internal/alloc/infra/daum.go:16`。

## 商品、库存、交易及辅助授权的进一步对照

| 机制 | Microsoft | Daum | 影响与对齐方向 |
|---|---|---|---|
| 异常账号退款资格 | 活动、已扣款、未退款订单对应 abnormal 即可进入退款扫描 | 还需三种结构化永久 failure_category 之一 | 网络验证耗尽等异常的订单结局不同，须统一哪些故障应补偿；不是只统一退款金额公式 |
| 退款事务的资源复查 | 选出订单后不再以同等证明锁住并复查资源代次／凭据／原因 | 钱包锁后、订单锁前持有根和子资源锁，复核证明及活动分配 | Daum 是安全加强；应把保护共用到微软，不应删除 |
| 已扣款订单恢复履约 | 微软在该共享检查处直接放行 | 重新检查 normal、APP 密码、原分配完整地址和别名可用性 | 扣款后账号停用时，一边可能继续激活，另一边拒绝并补偿；安全对齐宜共用检查 |
| 重新分配时的服务模式 | 微软分配函数不重复检查当前 CodeEnabled/PurchaseEnabled | Daum 再查当前开关与当前供货价 | 既有订单遇管理员关服务，是否继续履约不同；需统一订单快照与当前配置的优先级 |
| 冷库存缓存 | 微软可先返回缺货，随机冷快照不选后缀 | Daum 允许落到真实查询，随机／缺后缀明细时回查数据库 | 重启或清缓存后下单可用性、查询负载不同；这是借用其他邮箱策略的选择 |
| 候选桶搜索 | 固定扩大有限窗口，可能遗漏窗口外库存；不按普通类型重试全流程 | 桶探测后有一次不限定桶查询，并参与通用缺货／冲突重试 | 稀疏库存命中率和并发负载不同，不能只因共用权重选择器就称分配完全一致 |
| 历史重放完整性 | 已有历史分配通常直接跳过 | 复查确定历史订单号、分配和订单身份，缺事实时报冲突 | Daum 多了完整性保护；人工修复或旧半成品数据下流程不同 |
| dot/plus 权重与日额度 | 受支持且有额度／衍生实体 | 拒绝 dot/plus，只有 main/alias 三个固定地址 | 用户固定地址规则的差异，不能凭空增加未验证地址 |
| 后缀列表 | 微软运行时白名单 | 三个固定后缀 | 用户明确范围；额外可配置性未实现 |
| 专属维护与历史兼容禁售条件 | RT 刷新、资源历史任务、旧账号级项目封锁等条件 | 账号状态、APP 密码、逐完整地址历史 | 不应复制不存在的 RT 状态或微软旧表；但需统一维护期间何时可售的业务结果 |

证据：`internal/trade/infra/repo.go:1034` 对比 `internal/trade/infra/daum.go:17,40`；`internal/trade/app/proto_fulfillment.go:30`、`daum_fulfillment.go:16`；`internal/alloc/app/allocation.go:329,507,619,1304,1991,1999,2524` 对比 `internal/alloc/app/daum.go:107,124`、`daum_history.go:54`；`internal/trade/app/daum_history.go:116`；`internal/alloc/infra/repo.go:25,33` 对比 `internal/alloc/infra/daum.go:74,81,156`。

辅助邮箱是必须单独说明的授权差异：

- **Microsoft 的绑定参与 SMTP 入站归属。** 命中微软绑定的邮件可直接归档为该微软资源；辅助页读取这一原始入站作用域。Daum 不接管已有取件链接的邮箱、订单或入站归属。
- **Daum 的 URL 仅代表源订单授权。** 列表和正文必须满足源订单匹配规则、Token、活动分配和窗口；不能扩大为整个物理收件箱权限。若源项目规则不匹配 Kakao，Kakao 邮件即使到了物理邮箱，也不会出现在 Daum 辅助页。这与微软原始辅助入站箱不等价。
- **撤销后的历史访问不同。** Microsoft 已归档辅助邮件不依赖原取件 Token；Daum 每次复核当前关系，源 Token／订单／绑定撤销或资源删除后，不能继续通过这条关联读取历史邮件。
- **关联状态不代表相同事实。** Microsoft 的 pending/code_sent/verified 等服务于远端验证和恢复；Daum 的 linked/unlinked/conflict/unavailable 只表示本地关联授权。当前没有 Kakao 自动登录、远端绑定验证、改绑或密码找回实现。
- 保持原取件链接和权限边界是用户要求及安全需要。若要与微软一样查看所有 Kakao 辅助邮件，应有合适的源项目或独立的完整邮箱授权；不能取消源订单限制来“对齐”。

证据：`internal/mailtransport/infra/inbound_resolver.go:32`、`auxiliary_mail_repo.go:38,123,169,202,245`；`internal/mailtransport/app/auxiliary_pickup.go:62`；`internal/mailmatch/infra/order_message_page.go:15`。

## 已复用且本轮未发现相反证据的共同机制

- 管理员列表基础筛选、ID 倒序分页、移动布局、资源启停／供货／删除恢复这些操作集合，以及公共关联订单入口。
- 主／辅助邮件显示复用 ResourceMailsPanel；显示组件相同不代表上游 MIME 解析和辅助授权范围相同。
- 私有原件存储、异步导入、1000 行导入数据库分块、主要资源状态、凭据修订与旧任务隔离；验证成功进入 identifying，完整历史提交后进入 normal。
- 商品价格和接码／购买窗口字段、统一报价和订单、钱包和服务 Token。初始默认价相同：接码售价／供货价 8/5、购买 10/7；配置键独立，后续改微软价格不会自动改 Daum。
- 后缀加权公式、private_first 供应范围顺序、指定后缀缺货不换后缀、批量随机先确定一次后缀。库存缓存与候选查找的不同已另列。
- 实际供应商来自 email_resources 根归属，并写入共同的 allocation_order_guards 快照；别名 owner=1 不等于把供货收入支付给用户 1。
- 无码接码到期退款释放；购买按既有激活／质保到期规则完成，未激活不自动等于退款；Token 窗口、冻结／解冻和按订单 PayAmount 原额退款使用同一主干。
- 释放不会清掉该项目地址的历史使用事实；历史订单为零金额。前面列出的失效资格和历史识别差异，仍会改变最终是否进入这些共同流程。

证据：`internal/core/app/project.go:30`；`internal/alloc/app/allocation.go:476`；`internal/alloc/infra/supplier.go:12`；`internal/trade/app/ports.go:1540,2285,2498,2580,3172`；`internal/trade/app/supplier_settlement.go:77,181,247`。

## 建议的对齐原则

通用页面、导入、状态、任务、权限、库存搜索、失败累计及业务终态应采用同一实现或同一套行为契约，供应商适配只处理账号格式、认证、协议、固定地址映射等必要部分。自主简化与增强应在实施前明确列出，不能由“参考微软”推导为可任意改变。

退款证明、授权边界、凭据隔离等安全保护应保留；如果需要双方完全一致，应让共享流程共同具备这些保护。OAuth/RT、动态别名和微软历史兼容表不应机械复制；Kakao 远端能力没有实现就应明确说没有。

审查结论：先对齐 R1–R7，以及历史收件人证据、导入恢复和批量执行语义，再进行同输入双侧结果的契约验收。目前不能以“已经完全与微软一致”发布。

## 本轮实际验证

- 四个临时测试均通过，分别证明双方对相同 MIME 输入的正文优先级不同，以及双方在最新邮件已知时的增量边界不同。它们是差异复现，不是产品修复后的通过证明。
- 运行命令：`go test -overlay /tmp/daum-ms-parity-review-0ij0e9xz/overlay.json ./internal/daum/infra/protocol ./internal/mailtransport/infra -run '^TestReview(Daum|Microsoft)' -count=1 -v`。
- 其余结论根据双方实际调用链、参数和存储查询核对。没有用“调用了同一个入口”替代后续分支的行为核对。
- 本轮未重新登录真实邮箱、未发送邮件、未跑生产压力测试或生产迁移。上一轮的真实收件成功不能证明本文列出的机制已经一致。
