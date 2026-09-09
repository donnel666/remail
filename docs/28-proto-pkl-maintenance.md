# Proto 原生 PKL 会话与维护命令

日期：2026-09-08。

2026-09-09 更新：新增 Base64 PKL／裸用户名导入；应用只读写明文会话，不保留密文读取兼容。存量密文另做一次性受控转换。

## 导入现成的 PKL

一行一条，可在同一文件中混用：

```text
email----password
email----password----base64(PKL)
username----password
username----password----base64(PKL)
```

- 第一段可为完整邮箱地址或裸用户名；裸用户名自动补 `@proton.me`，已有 `@` 的地址不会替换后缀。补全后统一校验和去重，例如 `name` 与 `name@proton.me` 视为同一账号。密码的首尾空格保留；第三段允许首尾空白，内部不能折行或有空白。
- 第三段是原生 PKL 文件内容的标准 Base64，要求规范 padding，解码后最多 8 MiB。不是文件路径、JSON 会话或 URL-safe Base64。前端只校验格式与大小；服务端流式预检，不同时保留整批 PKL 的解码副本。
- 导入时逐行解码，与新建／恢复资源在同一事务中明文保存到 `proto_sessions.payload`。JSON 中的 `pkl` 使用 Base64 表示二进制，不做二次加密。先保存缺少 UID／地址／指纹的待验证数据，不能被普通取件或分配当成有效会话；导入事务内不运行 Python 或网络请求。
- 验证任务复用导入的 PKL，安全反序列化后只读查询 Proton 用户、地址和密钥，验证归属、私钥、指纹及签名。成功后保存完整 v2 会话并进入原旧项目识别流程。首次验证不发送密码，不自动刷新导入令牌，也不在失败后自动回退密码登录；过期／错误 PKL 明确验证失败。需要重新用密码登录时，通过原凭据更新入口清除旧会话并重新验证。
- 沿用原来的重复跳过、删除后恢复、skip/abort、分块提交和重放规则。已有非删除资源不会被新导入覆盖；跨分块的基础设施失败仍保留此前已提交行。数据库、备份和原私有导入 TXT 都含敏感凭据，必须限制访问，不能公开下载。
- CMD 的 `login -file/-stdin` 同样识别第三段，仅做诊断、不落库；带 PKL 只能使用 Python 引擎。正式持久化使用管理员导入流程，普通用户权限不因此开放。

## 决策：PKL 是数据库中的会话凭据

用户要求验证任务生成 PKL，取件复用 PKL，作用类似微软 RT。`proton.py` 默认使用 `protonmail-api-client 2.4.3` 的 WEB 登录，并不是此前 Go 客户端的直接登录路径。

选择在 **Proto 自己的协议边界**调用短生命周期 Python 子进程：复用已验证的 WEB 登录、cookie 和 PKL 数据结构，而不再自行补写一套 WEB 登录实现。旧 Go 客户端保留用于对照以及既有 v1 会话取件，不修改微软、苹果、Gmail 的客户端。

- 验证：现有任务领取及 revision/generation 检查 → WEB 登录或导入 PKL 验证、服务器证明和密钥校验 → 原生 PKL → 明文 JSON 写入 `proto_sessions.payload` → 原有历史识别任务。
- 取件：读取该资源的 PKL 快照 → Python 内存恢复 → 收件；不传密码、不重新登录、不落本地 `.pkl`。
- 刷新：Python 不自行刷新或循环重试 401。Go 在原来的 60 秒操作／90 秒数据库租约内完成刷新并持久化新 PKL，再继续读取；并发读者复用已轮换会话。
- 新会话载体为 v2；已转换成明文存储的旧 v1 会话仍可用旧 Go 取件路径，这不代表兼容旧密文。存储统一为普通 JSON，包含原会话字段及 `resourceId / credentialRevision`，读取时仍核对资源和凭据版本；不依赖应用加密密钥。
- **本次没有新表、SQL 结构迁移或订单字段。** 存量密文需要一次性数据格式转换；新写入和刷新的 PKL 在现有 BLOB 中明文保存，不进入资源 DTO、队列载荷、命令参数或日志。Proto 不再读取或注入 `SESSION_SECRET`；全局该配置仍用于系统其他功能，不可一并删除。

### 存储决策与一次性历史数据转换

按当前业务要求，与微软 RT 一样采用明文持久化，不引入新的密钥管理系统。收益是新会话可以独立于应用密钥恢复和迁移；代价是拥有数据库或备份读取权限的人可以获取 PKL，必须依靠访问控制、备份保护和输出脱敏限制暴露。

- 应用只解析明文 JSON；AES／HKDF／AAD 解密分支和 Proto 专属密钥注入已删除，不提供双格式读取、读时回填或自然转换。普通读取不更新数据库。旧首字节 `1` 的密文会返回会话不可用，取件恢复可能清除它并重新排队验证，因此必须先完成受控转换，不能靠新应用自行修复格式。
- 一次性操作先保存受控密文备份，再使用加密时的原 `SESSION_SECRET`、原资源 ID／凭据 revision 的 AAD 解开历史载荷。原密钥只用于这次操作，不重新引入应用运行时。没有正确的原密钥就不能恢复密文，不以删除会话或密码重验冒充转换成功。
- 目标是平铺 JSON：在原 session 字段同一层加入数值型 `resourceId` 和 `credentialRevision`，完整保留 `version / uid / accessToken / refreshToken / expiresAt / userKeys / addresses` 以及存在的 `pkl / keyFingerprint`。不能只保存原解密 JSON、只保存 PKL 或把原字段嵌套到 `session` 下。session 的 `version` 是内容版本，不是表的行 version；转换不改变业务凭据 revision、validation generation 或资源状态，也不触发登录／验证／历史识别。
- 转换后逐条核对 JSON、资源 ID／revision 与原 session 内容一致，并确认不存在漏转密文；操作日志只记录数量和安全结果，不输出密钥、账号、session 或 PKL。备份、数据库及其日志按敏感凭据保护。
- 本次按用户确认的测试场景安排转换窗口。从转换开始到 review 完成并部署明文版本前，不运行任何 Proto 操作，包括导入、取件、刷新、验证、历史识别和已排队的后台任务。应用不会自动阻止旧镜像读取新格式，必须保证此窗口内没有 Proto 任务实际执行。

2026-09-09 已在用户确认的测试窗口通过 SSH 完成一次性转换：5 条密文会话全部转为明文 JSON，独立查库确认密文剩余 0 条，资源 ID／凭据 revision 和 PKL 元数据均校验通过。原密文已保存为服务器上的 root-only 备份。更新事务耗时约 4 毫秒，只更新 `payload`，表行 version、revision、更新时间、资源状态和分配记录均未改变；没有执行 DDL、显式表锁、登录、验证或取件，也没有停止应用。此时旧镜像仍在运行，必须继续保持 Proto 无操作，直到部署明文版本完成；后续新写入不应使用本次旧备份直接覆盖。

代价是新增 Proto 专用 Python 运行环境及每次协议调用的进程开销。当前调试阶段优先隔离和可核验性；若未来进程启动成为实际瓶颈，再评估复用进程，不预先增加服务或工作池。

## 安全与完整性边界

- 账号、密码、代理及 PKL 只通过进程管道传递；子进程不继承数据库、Redis 或 `SESSION_SECRET` 等应用环境。
- PKL 只允许 SDK 的 primitive 字典结构，拒绝 GLOBAL、REDUCE、类构造、persistent ID、循环及尾随载荷；不能把外部任意 pickle 当成可信配置执行。
- 保留 Proton 固定公钥的 modulus 验签、constant-time ServerProof 检查、地址密钥和签名校验。
- TLS 正常校验；固定 Proton HTTPS 域名和按操作划分的接口允许列表。人机验证、额外账户操作或 2FA 停止等待人工处理，不自动解题、反复试密码或注销其他会话。
- HTTP 429、408、5xx 优先按临时失败处理，不能因附带 8002／6003 就把账号判为永久凭据错误。
- 错误只暴露安全的 `stage / category / httpStatus / apiCode`。原始响应、UID、token、PKL、密钥、密码、邮件正文及带认证信息的代理 URL 不进入 CMD 输出。
- 收件继续扫描 Inbox/Junk，保留时间窗、known IDs、原始 To 数量、精确地址范围、正文解密及附件排除。完整历史扫描不使用普通取件的数量上限。
- 保护上限：PKL 8 MiB，单协议行 32 MiB，单次辅助进程输出 128 MiB／100,000 条唯一邮件；超限明确失败，不宣告历史识别完整。需要超大邮箱时应增加可恢复分页游标，而不是放开无界内存。

## 部署

`Dockerfile` 和生产 CI 使用的 `Dockerfile.ci` 均打包 `/usr/local/bin/proto`，并在 `/opt/proto-venv` 安装锁定的 Python 依赖。`PROTO_PYTHON_BIN` 指向该解释器；不设置时使用 `python3`。不安装未使用的 CAPTCHA 图像处理依赖，也不向运行镜像加入 pip 或编译器。

镜像构建执行无网络账号操作的依赖初始化检查；CI 的独立 `proto-client` job 运行 PKL 安全及协议 fixture。缺少解释器时不开放新的 Proto 分配；缺少模块、异常协议输出等保持可重试／未完成状态，不能误报密码错误。

生产当前已有 136–138 的表结构，无需为 PKL 再迁移。新协议代码必须随镜像部署后才会由验证 worker 使用；**只提交验证任务，不会让旧镜像自动获得 PKL 实现**。既有部署仍有其原来的服务停止／启动步骤，本次没有修改该流程。

旧于 PKL 实现的镜像不能识别 v2 会话；只支持加密存储的旧镜像也不能读取转换后的明文记录。完成一次性转换、核对和 review 后，按现有停止／启动流程统一更新 Proto API 和 worker，再恢复 Proto 操作；不混跑新旧实例。写入新格式后不要直接回滚至只读密文的旧版本，修复镜像必须支持明文。若确需回到旧镜像，须在停止 Proto 操作后另行评估备份及转换后的新写入，不能认为回滚镜像也会回滚数据格式，也不能盲目用旧备份覆盖新会话。其他邮箱模块不使用这些会话数据。

## 验证重试与失败终态

验证任务的失败不等于邮箱凭据已永久失效。此前资源保持 `pending`，当前维护任务却已经 `uncertain` 并停止调度，页面因此看起来一直在等待。

- 可重试失败统一遵循 `Failure.Retryable` 和 `resource_validation_max_failures`，不再额外用类别白名单漏掉桥接 `protocol` 错误。配置为 3 表示最多执行 3 次；尚有预算时递增 generation，下一轮由原调度器领取。
- 每次确定失败的验证任务记录为 `failed`。`action_required` 必须人工处理，不自动重试；只有明确的 `invalid_credentials / identity_mismatch` 才写入资源物理 `abnormal` 并撤销会话。
- 选择复用现有维护事实：物理 `pending` 资源若存在相同资源 ID、generation 和 credential revision 的 `validation` 任务终态 `failed / uncertain`，Proto 的列表、详情、状态筛选和统计统一返回只读状态 `validation_failed`（“验证失败”）。它不写入数据库的资源状态列，也不会进入“待验证”筛选结果。
- 不选择给所有错误写 `abnormal`：这会改变既有退款语义。也不增加数据库状态／字段：现有维护记录已足以判定流程是否停止，因而无需 DDL、数据回填或修改退款扫描器。
- 旧版本留下的当前轮 `pending + uncertain` 会自动按该规则展示，无需修改历史记录。管理员明确重新验证时，原有命令递增 generation 并清零失败预算，旧终态不再影响新一轮。普通读取不会重新登录或自动复活已耗尽的任务。
- 桥接失败记录固定安全原因，例如 `bridge_decode`、`bridge_missing_result`、`bridge_worker_exit`、`bridge_limit`、`bridge_session_metadata`。维护记录的安全文案同样区分这些原因；不再以统一的 “invalid or oversized” 文案暗示 PKL 一定超大。日志与返回值不包含原始输出、stderr、账号或 PKL。

上述重试与状态投影变化仅作用于 Proto，不改变 PKL 刷新与订单分配、退款规则。API 增加了状态枚举值，部署后需要刷新浏览器加载支持 `validation_failed` 的新页面，旧标签页不保证能识别该值。只读状态本身不会改变钱款状态；本次会话存储格式的独立部署／回滚限制见上节。

## CMD 使用

以下资源 ID／操作人 ID 只是示例，先换成已确认的目标。默认不执行远程登录、写入或入队，不提供全量遍历。

```bash
# 默认只读检查资源及维护记录
proto -resource-id 12345 -json

# 仅对这一资源做 Python 登录诊断；使用现有有效代理绑定，不改数据库
proto -mode login -resource-id 12345 -apply -json

# 同一目标的旧 Go 登录对照，不改数据库
proto -mode login -resource-id 12345 -engine go -apply -json

# 单个 TXT 物理行；默认仅校验格式
proto -mode login -file /secure/proto-account.txt -line 1 -json

# 明确执行时，代理从已配置的环境变量读取，不写进命令行
proto -mode login -file /secure/proto-account.txt -line 1 -proxy-env PROTO_PROXY_URL -apply -json

# 提交正式验证：worker 负责将验证后的 PKL 明文持久化到数据库
proto -mode validate -resource-id 12345 -operator-user-id 1 -apply -json

# 提交旧项目识别任务
proto -mode history -resource-id 12345 -operator-user-id 1 -apply -json

# 从数据库会话读取少量邮件；只输出计数，不输出内容
proto -mode fetch -resource-id 12345 -operator-user-id 1 -since 24h -limit 20 -apply -json
```

数据库模式沿用 `MYSQL_DSN`，正式维护另使用已有 Redis／系统设置／代理配置；Proto CLI 只读取明文会话，不读取或注入 `SESSION_SECRET`，不兼容密文。全局会话密钥仍由系统其他功能使用。CLI 不执行数据转换或 SQL 迁移、不启动 HTTP 服务或后台 worker。`validate/history` 输出 `queued` 只是入队成功，不是业务完成，之后用 `inspect` 核对维护状态。可传 `-version` 确认资源版本。

`login` 是对照诊断，其 PKL 仅留在进程内；需要持久化时使用正式 `validate` 任务。`fetch -apply` 可能刷新并回写 PKL或请求会话恢复，因此不是纯只读命令。没有有效代理绑定时必须显式选择 `-direct` 或 `-proxy-env`，不会悄悄换出口。

## 2026-09-08 受控对照（历史记录，当时仍为加密存储）

使用同一条数据库凭据、同一个已绑定代理，且不修改数据库：

- Python WEB 全新登录成功，生成原生 PKL；再启动独立进程从该 PKL 取件成功，没有再次传密码。
- 旧 Go 直接登录在 `auth` 阶段收到 **HTTP 422 / API Code 8002**，被旧逻辑归为 `invalid_credentials`。该对照证明不能把这次失败直接解释为用户密码填错；没有逐项 A/B 隔离哪一个 WEB／direct 差异触发上游拒绝。
- 首轮“0 封”只表示当次最近 24 小时的 Inbox/Junk 查询结果，不是邮箱总数。用户发送测试邮件后，无时间过滤的上游列表返回 All Mail／Inbox 各 7 封、Junk 0 封，其中只有新测试邮件在最近 24 小时内；两个目录中的同一封邮件不能重复计数。
- 使用同次登录生成的 PKL，在另一个进程中执行正式 helper 的取件路径，成功匹配用户指定的发件人和主题，并解密出预期验证码。此次只解密最新 1 封指定测试邮件，`complete=false` 是主动设置数量上限的预期结果，不表示已经实号验收所有历史邮件。
- 没有保存明文 PKL 文件，也没有修改生产业务记录、在旧镜像下手写会话记录或触发批量任务。数据库加密保存、刷新租约、revision/generation 栅栏由本地定向测试覆盖；线上仍需部署新镜像后运行正式验证任务，才会生成持久会话。

离线 Alpine 依赖环境的 20 项 Python 测试全部通过，包括真实 PGP 密钥／密文、原生 PKL 恢复、MIME 和完整历史失败保护；Go 定向测试覆盖加密存储、刷新持久化和维护命令。上线后对该测试资源提交一次验证，确认 PKL 记录与历史任务状态，再从数据库会话执行取件确认部署集成。
