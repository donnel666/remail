# Proto 原生 PKL 会话与维护命令

日期：2026-09-08。

## 决策：PKL 是数据库中的会话凭据

用户要求验证任务生成 PKL，取件复用 PKL，作用类似微软 RT。`proton.py` 默认使用 `protonmail-api-client 2.4.3` 的 WEB 登录，并不是此前 Go 客户端的直接登录路径。

选择在 **Proto 自己的协议边界**调用短生命周期 Python 子进程：复用已验证的 WEB 登录、cookie 和 PKL 数据结构，而不再自行补写一套 WEB 登录实现。旧 Go 客户端保留用于对照以及既有 v1 会话取件，不修改微软、苹果、Gmail 的客户端。

- 验证：现有任务领取及 revision/generation 检查 → WEB 登录、服务器证明和密钥校验 → 原生 PKL → 原有 AES-GCM 加密 → `proto_sessions.payload` → 原有历史识别任务。
- 取件：读取该资源的加密 PKL 快照 → Python 内存恢复 → 收件；不传密码、不重新登录、不落本地 `.pkl`。
- 刷新：Python 不自行刷新或循环重试 401。Go 在原来的 60 秒操作／90 秒数据库租约内完成刷新并持久化新 PKL，再继续读取；并发读者复用已轮换会话。
- 新会话载体为 v2；旧 v1 JSON 会话仍可用旧 Go 取件路径。外层加密格式、HKDF 用途、资源 ID／凭据 revision 的 AAD 都不改变。
- **本次没有新表、新迁移或订单字段。** PKL 内容在现有 BLOB 中加密保存，不进入资源 DTO、队列载荷、命令参数或日志。轮换 `SESSION_SECRET` 仍需要重新验证 Proto 会话。

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

旧于 PKL 实现的镜像不能识别新 v2 会话；回滚时不要把它当成 Proto 会话格式也已回滚。其他邮箱模块不使用这些会话数据。

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

# 提交正式验证：worker 才负责将 PKL 加密持久化到数据库
proto -mode validate -resource-id 12345 -operator-user-id 1 -apply -json

# 提交旧项目识别任务
proto -mode history -resource-id 12345 -operator-user-id 1 -apply -json

# 从数据库会话读取少量邮件；只输出计数，不输出内容
proto -mode fetch -resource-id 12345 -operator-user-id 1 -since 24h -limit 20 -apply -json
```

数据库模式沿用 `MYSQL_DSN`，正式维护另使用已有 Redis／系统设置／代理配置；取件需要 `SESSION_SECRET`。CLI 不执行迁移、不启动 HTTP 服务或后台 worker。`validate/history` 输出 `queued` 只是入队成功，不是业务完成，之后用 `inspect` 核对维护状态。可传 `-version` 确认资源版本。

`login` 是对照诊断，其 PKL 仅留在进程内；需要持久化时使用正式 `validate` 任务。`fetch -apply` 可能刷新并回写 PKL或请求会话恢复，因此不是纯只读命令。没有有效代理绑定时必须显式选择 `-direct` 或 `-proxy-env`，不会悄悄换出口。

## 本次受控对照

使用同一条数据库凭据、同一个已绑定代理，且不修改数据库：

- Python WEB 全新登录成功，生成原生 PKL；再启动独立进程从该 PKL 取件成功，没有再次传密码。
- 旧 Go 直接登录在 `auth` 阶段收到 **HTTP 422 / API Code 8002**，被旧逻辑归为 `invalid_credentials`。该对照证明不能把这次失败直接解释为用户密码填错；没有逐项 A/B 隔离哪一个 WEB／direct 差异触发上游拒绝。
- 首轮“0 封”只表示当次最近 24 小时的 Inbox/Junk 查询结果，不是邮箱总数。用户发送测试邮件后，无时间过滤的上游列表返回 All Mail／Inbox 各 7 封、Junk 0 封，其中只有新测试邮件在最近 24 小时内；两个目录中的同一封邮件不能重复计数。
- 使用同次登录生成的 PKL，在另一个进程中执行正式 helper 的取件路径，成功匹配用户指定的发件人和主题，并解密出预期验证码。此次只解密最新 1 封指定测试邮件，`complete=false` 是主动设置数量上限的预期结果，不表示已经实号验收所有历史邮件。
- 没有保存明文 PKL 文件，也没有修改生产业务记录、在旧镜像下手写会话记录或触发批量任务。数据库加密保存、刷新租约、revision/generation 栅栏由本地定向测试覆盖；线上仍需部署新镜像后运行正式验证任务，才会生成持久会话。

离线 Alpine 依赖环境的 20 项 Python 测试全部通过，包括真实 PGP 密钥／密文、原生 PKL 恢复、MIME 和完整历史失败保护；Go 定向测试覆盖加密存储、刷新持久化和维护命令。上线后对该测试资源提交一次验证，确认 PKL 记录与历史任务状态，再从数据库会话执行取件确认部署集成。
