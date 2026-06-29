# BC-MAILMATCH 邮件匹配与真实服务上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |

> 核心域。BC-MAILMATCH 保存邮件事实，按项目规则识别订单服务结果。协议收发不在本上下文内。

---

## 1. 定位

| 问题 | 说明 |
|------|------|
| 收到了什么邮件？ | `Message` 保存结构化邮件事实。 |
| 邮件属于哪个订单？ | 先按收件人定位有效分配，再按项目规则过滤。 |
| 能不能给验证码？ | `BODY` 规则命中后写 `verificationCode`。 |
| 如何对外服务？ | 登录用户或 OrderToken 按 `orderNo` 读取过滤后的邮件/验证码。 |

真实服务不是底层邮箱代理。读取必须以 `orderNo -> Order -> Allocation -> MailRule` 为边界，禁止返回底层邮箱全部原始邮件。

---

## 2. 聚合：`Message`

| 字段 | 含义 |
|------|------|
| `id` | 邮件 ID |
| `emailResourceId` | 统一资源 ID |
| `recipient` | 收件人 |
| `sender` | 发件人 |
| `subject` | 主题 |
| `rawBody` | 正文原文 |
| `verificationCode` | 验证码快捷字段 |
| `receivedAt` | 接收时间 |
| `messageIdHeader` | Message-ID |
| `status` | `received/matched/ignored` |
| `matchDiagnostic` | 安全诊断摘要 |

不建正文拆表，不建邮件-订单匹配关系表。`Message` 不持久化 `orderNo`，订单作用域实时通过分配和项目规则定位。

---

## 3. 状态机

```mermaid
stateDiagram-v2
    [*] --> received: 收件落库
    received --> matched: 唯一命中订单和项目规则
    received --> ignored: 无有效订单或规则不命中
    matched --> [*]
    ignored --> [*]
```

多个订单同时命中时保持 `received`，写 SystemLog 和诊断字段，等待修正规则后重跑匹配。

---

## 4. 匹配流程

```mermaid
flowchart TD
    M[Message received] --> R[按 recipient 定位 allocated 分配]
    R -->|无候选| I[ignored]
    R -->|有候选| O[读取订单服务窗口]
    O --> P[读取项目商品和邮件规则]
    P --> F[执行 MailFilteringStrategy]
    F -->|0 个命中| I
    F -->|1 个命中| C[提取 verificationCode]
    F -->|多个命中| K[保持 received + 冲突诊断]
    C --> S[matched]
    S --> T[通知 MatchResultPort]
```

收件人定位：

| 分配类型 | 规则 |
|----------|------|
| Microsoft 主邮箱 | 分配 `mailbox=main`，用资源主邮箱匹配。 |
| Microsoft 显式别名 | 分配 `email=recipient`。 |
| Microsoft 点别名 | 分配 `email=recipient`。 |
| Microsoft 加号别名 | 分配 `email=recipient`。 |
| 自建邮箱 | 自建分配 `email=recipient`。 |

邮件规则：

| 规则类型 | 解释 |
|----------|------|
| `recipient` | `exact/dot/plus` 等内置策略。 |
| `sender` | 正则匹配发件人。 |
| `subject` | 正则匹配主题。 |
| `body` | 正则匹配正文，捕获组 1 作为验证码；无捕获组用整段匹配。 |

`Product.dotWeight > 0` 时，`recipient=dot` 不应让主邮箱吞掉点别名邮件；点别名必须以已分配 `dot` 命中。plus 同理。

---

## 5. 不变式

| 编号 | 规则 |
|------|------|
| INV-M1 | 同一资源下同一 `messageIdHeader` 不重复落库。 |
| INV-M2 | 入站邮件必须带 `emailResourceId`，不能传空资源。 |
| INV-M3 | 匹配前必须先按收件人定位有效分配，禁止全项目扫描。 |
| INV-M4 | 匹配只读 `listed` 项目和启用规则。 |
| INV-M5 | 当前策略要求的规则类型缺失时过滤失败。 |
| INV-M6 | 进入 `matched` 必须唯一命中一个有效订单服务。 |
| INV-M7 | 邮件匹配不直接修改订单状态，只通知交易域。 |
| INV-M8 | 真实服务读取必须按订单窗口和项目规则过滤。 |
| INV-M9 | 服务凭证读取只信任 BC-OPENAPI 已校验的 `orderNo`。 |
| INV-M10 | 原始正文只在授权详情接口返回，列表不批量返回正文。 |

---

## 6. 领域服务与 Port

| 服务 | 职责 |
|------|------|
| `MessageService` | 邮件落库、去重、状态更新。 |
| `FilterStrategy` | 按宽松/严格策略执行规则。 |
| `MatchingService` | 定位分配、匹配规则、提取验证码、通知交易。 |
| `ReadService` | 订单作用域读取邮件和验证码。 |
| `FetchService` | 为订单触发协议拉取并落库。 |
| `HealthService` | 售后自动检测收件能力。 |

| Port | 方向 | 职责 |
|------|------|------|
| `InboundPort` | 入站自 BC-MAILTRANSPORT | 接收 SMTP/IMAP/Graph 结构化邮件并落库。 |
| `FetchPort` | 出站到 BC-MAILTRANSPORT | 按订单和用途拉取邮件。 |
| `AllocationPort` | 出站到 BC-ALLOC | 按收件人/订单查分配。 |
| `MailRulePort` | 出站到 BC-CORE | 查询项目规则。 |
| `OrderAccessPort` | 出站到 BC-TRADE | 校验订单服务窗口和归属。 |
| `MatchResultPort` | 出站到 BC-TRADE | 通知邮件匹配结果。 |
| `ReadPort` | 入站自 BC-OPENAPI | 服务凭证读取订单结果。 |
| `HealthPort` | 入站自 BC-AFTERSALE | 售后检测邮箱是否可收件。 |

---

## 7. REST API 设计

邮件读取接口由控制台和 SDK 共用：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/orders/{orderNo}/messages` | 订单邮件摘要列表。 |
| `GET` | `/v1/orders/{orderNo}/messages/{messageId}` | 单封邮件详情，可返回正文。 |
| `GET` | `/v1/orders/{orderNo}/code` | 最新验证码。 |
| `POST` | `/v1/orders/{orderNo}/fetch` | 创建邮件拉取任务，必须幂等。 |

鉴权中间件支持三类主体：

| 主体 | 可访问范围 |
|------|------------|
| Session | 当前登录用户自己的订单；管理员可按 `scope=all` 查看。 |
| API Key | API Key 所属用户的订单；受限流、并发和幂等保护。 |
| OrderToken | 只能访问 Token 绑定的 `orderNo`，不能越权访问其他订单。 |

后台：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/admin/messages` | 全局邮件事实筛选。 |
| `GET` | `/v1/admin/messages/{messageId}` | 单封邮件诊断详情。 |
| `POST` | `/v1/admin/messages/{messageId}/rematch` | 重跑匹配。 |
| `GET` | `/v1/admin/messages/conflicts` | 查看冲突邮件。 |
| `POST` | `/v1/admin/orders/{orderNo}/fetch` | 管理员创建订单拉取任务。 |
| `POST` | `/v1/admin/orders/{orderNo}/check` | 管理员创建健康检查任务。 |

---

## 8. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-MM-1 | 只建 `Message` | 一期足够表达邮件事实和真实服务。 |
| ADR-MM-2 | 先收件人定位，再规则过滤 | 避免全项目扫描和数据泄露。 |
| ADR-MM-3 | 不持久化邮件-订单关系 | 订单作用域可实时通过分配和规则定位，减少同步点。 |
| ADR-MM-4 | 服务凭证读取以 `orderNo` 为边界 | Token 是 Bearer 资源凭证，MailMatch 不再要求登录用户。 |
| ADR-MM-5 | 多订单命中不交付 | 避免规则冲突导致错误验证码交付。 |
