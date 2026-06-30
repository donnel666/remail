# ReMail Go 版技术栈与库选型

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-06-30 | V1.1 | Codex | 补充 P1 性能诊断 pprof 入口、慢请求/慢 SQL 日志和自动 CPU profile；不改变业务 API。 |
| 2026-06-30 | V1.2 | Codex | 补充前端 OpenAPI TypeScript client 生成细则；不改变 OpenAPI 单源策略。 |

> 目标：快速迭代、控制复杂度、保持业务边界清楚。
>
> 原则：用成熟库做通用能力，核心业务规则自己写清楚；不为还没证明的问题引入微服务、Kubernetes、MQ、Temporal、GraphQL 或多前端项目。

---

## 1. 总体架构

| 项目 | 决策 |
|------|------|
| 架构形态 | Go 模块化单体。 |
| Web 框架 | Gin。 |
| 前端形态 | React + TypeScript 单控制台。 |
| 部署形态 | 一个 Go 服务嵌入前端静态文件，一个 Docker 镜像。 |
| 数据库 | MySQL 8。 |
| 缓存/任务 | Redis + Asynq。 |
| 对象存储 | MinIO。 |
| Microsoft 复杂流程 | Go MailTransport 内部 ACL。 |
| API 契约 | OpenAPI 作为前端 client 和 SDK 契约源。 |

运行形态：

```text
remail-server        Go + Gin + embedded React dist + Microsoft ACL
mysql                MySQL 8
redis                Redis
minio                MinIO
```

---

## 2. 后端技术栈

| 能力 | 选型 | 说明 |
|------|------|------|
| 语言 | Go 当前稳定版本 | 项目内通过 `go.mod` 和 CI 镜像锁定。 |
| HTTP | Gin | 路由、中间件、参数绑定成熟，贴近 `new-api`。 |
| ORM | GORM | 普通 CRUD 快速开发。 |
| 复杂 SQL | `database/sql` / GORM Raw | 钱包、分配、幂等、条件更新、行锁等关键路径必须可控。 |
| Migration | goose | 简单、Go 生态成熟，禁止改已部署历史 migration。 |
| Redis | `go-redis` | 会话、验证码、缓存、限流、Asynq。 |
| 异步任务 | Asynq | Redis-backed 任务队列，支持后台 worker、重试、延迟和定时任务。 |
| 对象存储 | `minio-go` | 私有上传、附件、导入文件、失败文件。 |
| 配置 | 环境变量 + 本地 `.env` | 不引入配置中心。 |
| 日志 | 标准库 `slog` | JSON 结构化日志，带 requestId。 |
| API 文档/生成 | OpenAPI + `oapi-codegen` + `openapi-typescript`/`openapi-fetch` | `oapi-codegen` 生成 Go 服务端类型；`openapi-typescript` 生成前端 schema，`openapi-fetch` 做薄封装；同一份 OpenAPI 仍是前端 client 和 SDK 契约源。 |
| 参数校验 | Gin binding + validator | DTO 校验集中处理。 |
| 授权策略 | Casbin v2 | 参考 `new-api`，承接角色基线权限和用户级覆盖；数据归属仍由业务代码判断。 |
| 密码哈希 | bcrypt 或 argon2id | 用户密码使用标准慢哈希。 |
| 凭据哈希辅助 | HMAC-SHA256/SHA-256 封装 | 仅用于指纹、前缀、索引或请求指纹；不替代原值保存策略。 |
| 测试 | `testing` + `testify` + `httptest` | 单元和 HTTP handler 测试。 |
| 集成测试 | `testcontainers-go` | MySQL/Redis/MinIO 约束和闭环测试。 |

补充设计：

| 能力 | 选型 | 规则 |
|------|------|------|
| 性能诊断 | 标准库 `net/http/pprof` | 通过独立本地诊断 HTTP server 暴露 `/debug/pprof/`；`PPROF_ADDR` 为空时关闭。裸进程建议绑定 `127.0.0.1:6060`，Docker 内建议绑定 `:6060` 并只映射到宿主机 `127.0.0.1`。该入口不进入 OpenAPI，不作为业务 API 契约。 |
| 慢请求日志 | Gin middleware + `slog` | 参考 `new-api` 的运行日志可见性，所有非健康检查/静态资源请求输出 method/path/status/latency/requestId；超过 `SLOW_REQUEST_THRESHOLD` 输出 `slow http request` warning。日志不得写请求体、Cookie、密码、Token。 |
| 慢 SQL 日志 | GORM logger + `slog` | 超过 `SLOW_SQL_THRESHOLD` 输出 `slow sql` warning，并携带 requestId、耗时、行数和参数化 SQL；SQL 日志必须开启参数过滤，避免把密码、Token、验证码等参数写入日志。 |
| 自动 CPU profile | `runtime/pprof` + CPU monitor | `PPROF_ADDR` 开启时启动 CPU 使用率监视；超过 `PPROF_CPU_THRESHOLD` 自动采样 `PPROF_CPU_PROFILE_DURATION` 并写入 `PPROF_CPU_PROFILE_DIR`，触发和文件路径必须出现在 `docker logs`。 |

ORM 使用规则：

| 场景 | 规则 |
|------|------|
| 普通查询/后台 CRUD | GORM。 |
| 钱包扣款/退款/提现 | 手写事务 SQL、行锁、条件更新。 |
| 分配抢占 | 手写唯一约束、条件更新、事务。 |
| 幂等表 | 手写唯一键和状态更新。 |
| 批量任务 claim | 手写 SQL，保证并发 worker 不重复认领。 |

---

## 3. 前端技术栈

| 能力 | 选型 | 说明 |
|------|------|------|
| 框架 | React | 单控制台。 |
| 语言 | TypeScript | 类型约束和 API client 生成。 |
| 构建 | Rsbuild | 对齐 `new-api` 思路，工程化简单。 |
| 路由 | TanStack Router | 类型化路由和权限路由。 |
| 请求状态 | TanStack Query | 缓存、刷新、失效、重试。 |
| 表格 | TanStack Table | 管理系统核心表格能力。 |
| 表单 | React Hook Form | 复杂表单性能和校验更稳。 |
| 校验 | Zod | 与表单、API DTO 配合。 |
| 样式 | Tailwind CSS | 快速构建控制台 UI。 |
| 组件 | shadcn/ui + Radix/Base UI | 组件可控，避免重型后台框架绑死。 |
| 状态 | Zustand | 少量本地 UI 状态。 |
| 图标 | lucide-react | 管理工具按钮和菜单图标。 |
| 图表 | Recharts | 运营图表够用。 |
| API client | OpenAPI 生成 | 不手写重复 DTO 和请求函数。 |
| E2E | Playwright | 登录、下单、后台操作主线验证。 |

前端 API client 补充设计：

| 项 | 规则 |
|----|------|
| 契约源 | `api/openapi.yaml` 是唯一源；前端不得手写与 OpenAPI 重复的请求/响应 DTO。 |
| 类型生成 | 使用 `openapi-typescript` 生成 `web/src/lib/openapi/schema.ts`，该文件只由生成命令更新。 |
| 请求封装 | 使用 `openapi-fetch` 基于生成的 `paths` 发起请求；业务模块只允许保留很薄的 wrapper，用于 baseUrl、cookie credentials、CSRF header 和错误归一化。 |
| CI 校验 | OpenAPI 变更后必须重新生成 Go/TS 产物，并用 diff 检查生成物是否同步。 |

前端项目只有一个：

```text
web
```

禁止拆成：

```text
web-user
web-admin
web-supplier
```

菜单、路由和按钮由后端 RBAC 权限驱动；前端隐藏按钮只是体验优化，最终权限以后端判定为准。

---

## 4. 认证与权限

| 能力 | 选型 |
|------|------|
| 控制台登录态 | HttpOnly Cookie Session |
| Session 存储 | Redis，必要时 DB 会话表兜底 |
| CSRF | SameSite Cookie + CSRF Token |
| 角色等级 | `user < supplier < admin < super_admin`，特权用户天然拥有低权限用户基础能力 |
| 细粒度权限 | Casbin v2 + GORM adapter |
| 数据归属 | 业务表 `userId/supplierId/ownerUserId` + scope 查询 |
| API Key | Header Bearer 或 `X-API-Key` |
| OrderToken | Bearer Token |

权限判断分两层：

| 层 | 说明 |
|----|------|
| RBAC | 判断用户能不能做这类动作。 |
| 数据 scope | 判断用户能不能看/操作这条数据。 |

Casbin 使用边界：

| 做 | 不做 |
|----|------|
| 管理端菜单、页面、按钮、敏感命令权限。 | 不把订单归属、项目私有授权、资源 owner、钱包归属塞进 Casbin。 |
| 内置角色 baseline：user/supplier/admin/super_admin。 | 不用 Casbin 写复杂业务状态机。 |
| 支持管理员给单个用户增加或移除特定后台权限。 | 不让前端提交权限判断结果。 |
| 权限变更后刷新 enforcer/cache。 | 不绕过业务应用服务直接改表。 |

特权继承规则：

```text
supplier 拥有 user 的全部控制台能力，并增加供应商页面。
admin 拥有 user 的全部控制台能力，并增加后台页面。
super_admin 拥有 admin 的全部能力，并增加系统级敏感页面。
```

这意味着管理员不需要一套“管理员版用户接口”。同一控制台页面或 API 可以按 `scope=mine/owned/all` 返回不同范围；后台敏感命令才单独挂管理员权限。

---

## 5. 异步任务

主选 Asynq。

| 场景 | 任务类型 |
|------|----------|
| Microsoft 导入解析 | 普通队列任务 |
| Microsoft 验证/RT 续期 | 重试任务 + 延迟任务 |
| 邮件拉取 | 普通队列任务 |
| 订单超时退款 | 延迟任务或定时扫描任务 |
| 接码读取期结束结算 | 延迟任务或定时扫描任务 |
| 购买质保到期结算 | 延迟任务或定时扫描任务 |
| 售后自动检测 | 普通队列任务 |
| 提现/充值查账 | 普通队列任务 |
| 导入失败文件生成 | 普通队列任务 |

规则：

- Asynq 负责执行、重试、延迟和 worker 并发。
- MySQL 业务表仍然是最终事实来源。
- 关键任务必须有业务任务表或实体诊断字段，不能只依赖 Redis 任务可见性。
- 任务失败写 SystemLog，管理员能在控制台 retry/cancel。

---

## 6. 文件与对象存储

| 文件 | 存储 |
|------|------|
| Microsoft 导入原始文件 | MinIO private bucket |
| 导入失败明细 | MinIO private bucket |
| 工单附件 | MinIO private bucket |
| 帮助文章图片 | public bucket 或受控代理 |
| 导出文件 | MinIO private bucket，短期有效 |

业务表只保存 object key、文件名、MIME、大小和归属，不保存本地路径。

---

## 7. Microsoft 与邮件协议

| 能力 | 选型 |
|------|------|
| Microsoft 登录/RT/Graph | Go MailTransport Microsoft ACL |
| Microsoft 通讯代理 | BC-PROXY 提供资源池绑定和系统池兜底 |
| Microsoft HTTP | `net/http`、`cookiejar`、必要时封装 `utls` |
| Microsoft HTML 解析 | `goquery` |
| Microsoft OAuth | `golang.org/x/oauth2` 或受控手写 token 请求 |
| Microsoft Graph | 优先直接 HTTP 调 Graph REST |
| SMTP 入站 | 成熟 Go SMTP server 库或受控协议组件 |
| SMTP 外发 | 成熟 Go SMTP client |
| IMAP 拉取 | 成熟 Go IMAP client |
| DNS 检查 | 成熟 Go DNS 库 |

边界：

- Go 负责业务状态、任务、凭据持久化、Microsoft 页面流、Graph/token 和错误分类映射。
- Go 通过 BC-PROXY 获取 Microsoft 通讯代理，代理 URL 按凭据禁敏规则处理。
- Microsoft 页面状态机只存在于 MailTransport ACL，不进入领域状态枚举。
- Microsoft 交互能力做成 Go 内部模块，用 fixture 驱动实现和回归。

---

## 8. 部署与 CI/CD

| 能力 | 选型 |
|------|------|
| 构建 | GitHub Actions |
| 镜像 | Docker 多阶段构建 |
| 镜像仓库 | GHCR |
| 部署 | Docker Compose + SSH 触发 |
| 静态资源 | React build 后由 Go `embed` 进二进制 |
| 健康检查 | `/healthz`、`/readyz` |

CI 基线：

```text
go test ./...
go vet ./...
golangci-lint run
pnpm install
pnpm typecheck
pnpm build
docker build
```

部署脚本只做部署，不做业务修库。数据库结构漂移必须通过新的 migration 解决。

---

## 9. 明确不选

| 不选 | 原因 |
|------|------|
| Spring Boot | 新项目按 Go 快速迭代路线。 |
| Next.js | 管理控制台不需要 SSR。 |
| 多前端项目 | 会重新制造用户端/后台端重复接口。 |
| 微服务 | 当前没有独立扩缩容和团队边界需求。 |
| Kubernetes | 运维成本超过当前收益。 |
| Kafka/RocketMQ | Asynq + MySQL 任务事实足够。 |
| Temporal | 长流程编排能力过重。 |
| GraphQL | REST + OpenAPI 更利于 SDK 和后台对接。 |
| 独立 Microsoft 协议服务 | Go 版只有一个后端运行时，Microsoft 交互能力由 Go ACL 模块承接。 |
| 字段级对称加密 | 凭据需要重复使用/展示，密钥轮转和排障成本过高。 |

---

## 10. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-TECH-1 | Go + Gin 作为后端基础 | 简单、部署轻、贴合 `new-api` 形态。 |
| ADR-TECH-2 | React + Rsbuild 单控制台 | 降低前端项目数量和接口重复。 |
| ADR-TECH-3 | GORM + 手写 SQL 混用 | CRUD 快，关键资金/分配路径可控。 |
| ADR-TECH-4 | Asynq 作为异步任务组件 | Redis 已是基础依赖，任务队列简单够用。 |
| ADR-TECH-5 | OpenAPI 作为契约源 | 前端 client 和 SDK 从同一契约生成，避免手写三套。 |
| ADR-TECH-6 | Go embed 前端 dist | 一个镜像部署，降低调试和上线复杂度。 |
| ADR-TECH-7 | Microsoft 复杂流程由 Go ACL 承接 | 减少双运行时和部署复杂度，Microsoft 交互能力做成 Go 模块。 |
| ADR-TECH-8 | 引入 Casbin，但只做授权策略 | 管理端权限矩阵和 per-user override 用库解决；业务数据归属和状态机仍由领域代码负责。 |
