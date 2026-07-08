# BC-OPENAPI 开放凭证上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-07-08 | V1.1 | Codex | P1-I8 补充/修正：OrderToken 作为 pickup 服务凭证事实，不再作为通用 Bearer 鉴权主体。 |
| 2026-07-08 | V1.2 | Codex | 按产品设计纠正 API Key 展示边界：当前用户凭证管理列表可返回明文；补充 API Key 额度、不限 RPM 和软删除语义。 |

> 通用域。BC-OPENAPI 负责 API Key、OrderToken、请求入口保护和日志，不拥有订单服务数据。

---

## 1. 定位

两类凭证：

| 凭证 | 绑定 | 用途 |
|------|------|------|
| `ApiKey` | `userId` | 让 SDK/脚本以用户身份调用被开放的统一业务 API。 |
| `OrderToken` | `orderNo` | 订单服务凭证事实；外部只通过 `pickup(email + token)` 读取绑定订单的邮件/验证码。 |

重要决策：SDK 不需要后端另做一套接口。API Key 通过鉴权中间件调用同一批 `/v1/**` 业务 API；OpenAPI 文档标记哪些操作允许 API Key。

---

## 2. 实体

| 实体 | 字段 |
|------|------|
| `ApiKey` | `keyId`、`keyPrefix`、`plain`、`userId`、`enabled`、`rateLimit`、`concurrency`、`quotaLimit/quotaUsed`、`expireAt`、`lastUsedAt` |
| `OrderToken` | `tokenId`、`tokenPrefix`、`plain`、`orderNo`、`enabled`、`expireAt`、`disabledAt`、`disabledReason` |
| `ApiLog` | `principalType`、`principalId`、`path`、`method`、`idempotencyKey`、`httpStatus`、`durationMs`、`requestId` |

API Key 和 OrderToken 按原值保存；授权凭证管理接口可重复查看明文。普通日志、错误响应、导出文件禁敏；非凭证管理列表默认只显示前缀。

API Key 限制补充设计：

| 字段 | 规则 |
|------|------|
| `rateLimit` | `null` 表示不限制 RPM；正整数表示每分钟请求上限。 |
| `concurrency` | 正整数，省略时使用系统默认并发上限。 |
| `quotaLimit` | `null` 表示不限制总请求额度；正整数表示该 Key 可消费的总请求次数。 |
| `quotaUsed` | 鉴权通过并进入业务入口前原子递增；不得超过 `quotaLimit`。 |

---

## 3. 鉴权中间件

统一业务 API 支持登录态和 API Key 两类主体。OrderToken 不进入通用鉴权中间件，避免把取件能力扩散成第二套用户权限模型。

| 主体 | Header/Cookie | 说明 |
|------|---------------|------|
| Session | HttpOnly Cookie | 控制台用户。 |
| API Key | `Authorization: Bearer rk-...` 或 `X-API-Key` | SDK/脚本，以 `userId` 身份调用允许开放的接口。 |

中间件职责：

```text
识别凭证
按提交的完整 API Key 明文与数据库保存值做等值校验，rk- 只是生成前缀，不作为鉴权策略分支
校验用户启用/凭证启用/过期
校验该接口是否允许该 principalType
限流和并发占用
注入 Principal 到上下文
请求结束释放并发占用
写 ApiLog
```

---

## 4. 不变式

| 编号 | 规则 |
|------|------|
| INV-O1 | API Key 只能代表所属用户，不授予管理员特权。 |
| INV-O2 | API Key 能调用哪些接口由 OpenAPI 标记和中间件控制，不能默认开放全部接口。 |
| INV-O3 | API Key 下单必须带幂等键，同 Key + 同幂等键不产生第二个订单。 |
| INV-O4 | OrderToken 只能通过 pickup handler 校验；校验成功后只读取绑定 `orderNo` 且与 `email` 匹配的服务结果。 |
| INV-O5 | 服务结束时 Trade 必须同步禁用 OrderToken。 |
| INV-O6 | 购买邮箱正常服务长期有效，Token 不因质保到期自动过期。 |
| INV-O7 | API Key 和 Token 明文不得进入普通日志和错误响应。 |
| INV-O8 | 限流或并发超限必须在进入业务域前拒绝。 |

---

## 5. Port

| Port | 方向 | 职责 |
|------|------|------|
| `OrderTokenPort` | 入站自 BC-TRADE | 签发、禁用、重置订单服务凭证。 |
| `AuthPort` | 入站自 HTTP 中间件 | 校验 API Key。 |
| `ReadPort` | 出站到 BC-MAILMATCH | 服务凭证读取订单邮件/验证码。 |

---

## 6. API 设计

凭证管理接口：

| 方法 | URI | 说明 |
|------|-----|------|
| `POST` | `/v1/apikeys` | 创建 API Key，必须幂等，返回明文。 |
| `GET` | `/v1/apikeys` | 当前用户 API Key 列表，返回明文，用于个人设置页直接复制。 |
| `GET` | `/v1/apikey-usage` | 当前用户 API Key 使用聚合，只返回请求次数和 Key 数量，不返回明文。 |
| `GET` | `/v1/apikeys/{keyId}` | 授权详情，返回明文。 |
| `PATCH` | `/v1/apikeys/{keyId}` | 启停、限流、并发、额度、过期时间。 |
| `DELETE` | `/v1/apikeys/{keyId}` | 软删除 API Key；列表/详情/鉴权不可再使用，历史订单事实保留外键引用。 |
| `GET` | `/v1/orders/{orderNo}/token` | 查看订单服务凭证详情，授权时返回明文。 |
| `POST` | `/v1/orders/{orderNo}/token/reset` | 重置服务凭证，必须幂等，返回新明文。 |

后台：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/admin/apikeys` | 管理员查询 API Key。 |
| `GET` | `/v1/admin/apikeys/{keyId}` | 授权详情，返回明文。 |
| `PATCH` | `/v1/admin/apikeys/{keyId}` | 调整启停、限流、并发、过期时间。 |
| `GET` | `/v1/admin/tokens` | 服务凭证查询。 |
| `GET` | `/v1/admin/tokens/{tokenId}` | 授权详情，返回明文。 |
| `PATCH` | `/v1/admin/tokens/{tokenId}` | 禁用。 |
| `POST` | `/v1/admin/orders/{orderNo}/token/reset` | 管理员重置订单服务凭证。 |
| `GET` | `/v1/admin/logs/api` | API 请求日志查询。 |

SDK 可调用接口示例：

| 方法 | URI | 说明 |
|------|-----|------|
| `POST` | `/v1/orders` | API Key 下单。 |
| `GET` | `/v1/orders` | API Key 查询自己的订单。 |
| `GET` | `/v1/orders/{orderNo}` | API Key 查询自己的订单详情。 |

取件接口：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/pickup?email={email}&token={token}` | 资源钥匙读取邮件 6 元素；内部按 singleflight 提交异步收件任务。 |

---

## 7. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-OAPI-1 | SDK 复用统一业务 API | 避免 `/open` 和控制台接口重复开发。 |
| ADR-OAPI-2 | API Key 是用户自动化身份 | 业务规则仍由对应业务域判断。 |
| ADR-OAPI-3 | OrderToken 绑定 `orderNo` | 持有者通过 pickup 读取该订单服务结果，不作为通用 Bearer principal。 |
| ADR-OAPI-4 | 凭据原值保存 | 授权接口需要重复展示明文。 |
| ADR-OAPI-5 | 限流/并发在中间件完成 | 业务域只处理已通过入口保护的命令。 |
