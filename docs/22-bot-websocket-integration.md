# Bot WebSocket 集成

## 决策

ReMail 在现有 HTTP Bot API 之外提供 `GET /v1/bot/ws`。AstrBot 主动建立长连接；
同一连接承载心跳、受限 Bot API 请求/响应，以及可恢复的主动推送事件。HTTP API
继续保留；仅在帧尚未发送且连接未建立时插件才回退 HTTP，发送后响应丢失不会盲目
重放写请求。

该模式面向每个机器人实例少量连接。当前每把 Bot System Key 每个 ReMail 实例最多
4 条连接；只有承担主动推送的连接读取持久游标。并发订阅增长到现有实现无法承载时，
应改为共享 broker 广播，而不是继续提高单实例轮询数量。

## 连接与认证

```http
GET /v1/bot/ws HTTP/1.1
Connection: Upgrade
Upgrade: websocket
X-System-Key: sk_...
X-Bot-Channel: qq
```

- Key 必须是系统设置创建的 `purpose=bot` Key。
- Key 的 `platform + subjectNamespace + allowedGroupIds` 由 ReMail 系统设置固定，
  客户端帧不能覆盖。
- `X-Bot-Channel` 只能由 AstrBot 插件从当前适配器生成。服务端先鉴权 Key，再要求该
  渠道与 Key 的 `platform` 类型一致；QQ Key 用于 Telegram 或反向使用均返回 401。
- QQ namespace 的 `subject` 只能是机器人从当前事件解析出的真实正整数 QQ 号，不能
  使用 OpenID，也不能接受用户或 LLM 参数覆盖。ReMail 将 QQ 号作为对应渠道的第三方
  登录标识明文保存；QQ namespace 由 Key 配置判定，不硬编码某个
  AstrBot 平台或适配器名称。
- Telegram namespace 的 `subject` 只能是事件提供的正整数用户 ID；群聊 Chat ID 必须
  是非零整数，并命中该 Telegram Key 的群白名单。
- 完整 Key 只放在 AstrBot 的 ReMail 插件配置，不放入聊天或 URL；配置文件权限必须
  仅允许 AstrBot 运行账号读取。
- 已连接会话会定期复核 Key；Key 被删除或失效后，连接会关闭并停止主动事件。
- 非本机 ReMail 地址必须使用 `wss://`。
- 服务端最大接收帧为 16 KiB、最大内部响应为 2 MiB；每把 Key 允许每秒 10 帧、
  短时突发 30 帧，每连接最多 4 个并行请求。60 秒没有应用帧会关闭连接，单次写入
  最多等待 10 秒。

连接成功后服务端发送：

```json
{
  "type": "hello",
  "heartbeatSeconds": 20,
  "platform": "qq",
  "subjectNamespace": "qq:main"
}
```

## 心跳

客户端每 20 秒发送：

```json
{"type":"ping","id":"heartbeat-id"}
```

服务端返回：

```json
{"type":"pong","id":"heartbeat-id"}
```

10 秒未收到 pong 时，AstrBot 关闭连接并按 1、2、4…30 秒退避重连。

## 请求与响应

用户身份只能由 AstrBot 当前事件填入 `subject` 和 `scene`；当
`scene=group` 时，群号只能取自 `event.get_group_id()` 并填入 `groupId`。
私聊帧不携带 `groupId`，用户命令和 LLM 工具都没有身份或群号参数。
服务端将帧转换为内部 HTTP 请求，并沿用握手时已经与 Key 类型核对过的渠道，使原有
Bot Key、每平台用户、私聊限制、输入校验和错误脱敏继续生效。普通 HTTP 模式同样发送
`X-Bot-Channel`，群聊再发送 `X-Bot-Group`，私聊不发送群号。

```json
{
  "type": "request",
  "id": "request-id",
  "method": "GET",
  "path": "/v1/bot/projects",
  "subject": "123456789",
  "scene": "group",
  "groupId": "123456789",
  "query": {"search":"GitHub","limit":"20"}
}
```

```json
{
  "type": "response",
  "id": "request-id",
  "status": 200,
  "body": {"items":[],"total":0,"offset":0,"limit":20}
}
```

连接只允许调用已发布的 `/v1/bot/**` 业务路径；管理接口、任意 URL 和 WebSocket
端点本身不能通过请求帧转发。群聊项目请求强制使用公共视图，只有私聊才解析绑定并
返回个性化项目/库存。

`GET /v1/bot/context` 是无业务数据的事件来源门禁。AstrBot 在返回本地或公开缓存内容
（接口文档、公告、常见问题）前也必须先调用它；群内执行 `/绑定` 时也先验证
Key 的群号白名单，通过后才提示改用私聊。

诊断命令使用“邮箱 + 用户问题描述”。服务端只接收邮箱，并始终附带由 Bot 身份解析出的
用户 ID，从该用户自己的订单反查公开项目 ID/名称；用户描述只交给 AstrBot AI，邮箱
不能扩大查询范围。给模型的安全诊断不包含邮箱、订单号、验证码、邮件内容、凭证、
内部状态名或拉取实现。

## 主动推送事件

新版订阅使用一个 `topics` 数组和一套全局游标：

```json
{
  "type": "subscribe",
  "id": "subscription-id",
  "topics": [
    "project.launched",
    "leaderboard.settled",
    "system.notice.updated",
    "system.announcement.updated",
    "email.discount.updated",
    "project.price.updated"
  ],
  "after": "2026-08-30T12:00:00Z",
  "afterId": 0
}
```

服务端确认订阅：

```json
{
  "type": "subscribed",
  "id": "subscription-id",
  "topics": ["project.launched", "leaderboard.settled"],
  "cursor": {"after":"2026-08-30T12:00:00Z","afterId":"0"}
}
```

旧客户端仍可只发送 `topic: "project.launched"`；单主题确认帧同时返回 `topic`。
AstrBot 插件升级时会同时发送旧 `topic` 和新 `topics`，旧服务端忽略未知的
`topics` 后继续提供项目上线事件。事件帧统一为：

```json
{
  "type": "event",
  "topic": "project.launched",
  "cursor": {"after":"2026-08-30T12:01:00Z","afterId":"9223372036854776801"},
  "data": {
    "project": {"id":124,"name":"Example"}
  }
}
```

支持的主题及 `data` 白名单如下。服务端和客户端都不得把数据库模型或任意 JSON
直接作为聊天文本：

| topic | 允许客户端读取的 `data` 字段 |
| --- | --- |
| `project.launched` | `project.id/name/description` |
| `leaderboard.settled` | `businessDate`、`settledAt`、`items[].rank/name/successCount/rewardAmount` |
| `system.notice.updated` | `notice` |
| `system.announcement.updated` | `announcements[].title/content` |
| `email.discount.updated` | `message` |
| `project.price.updated` | `projectId`、`name`、`message` |

AstrBot 为每个 `unified_msg_origin` 单独保存同一套全局 `(after, afterId)` 游标，旧版
项目游标继续读取。只有目标确认发送成功后才推进该目标游标；任一目标失败就断开连接，
重连时从所有目标的最旧游标重新订阅。因此语义是至少一次：成功目标可能遇到重放但会
按自己的游标跳过，不会因其他目标失败而漏过事件。客户端原样持久化并回传服务端给出
的全局 `afterId`，不得把它解释为项目 ID 或每主题序号。服务端用十进制字符串发送
`afterId`，避免 JavaScript JSON 数字丢失 `uint64` 精度；服务端仍兼容旧客户端回传
JSON 整数，新客户端应原样保存字符串。

客户端 renderer 只读取上表字段并限制条数与总长度，未知 topic 和未知字段不输出；
输出前还会清除邮箱地址、凭证、System Key、授权令牌及含凭证的数据库 URL。QQ 使用
NapCat / OneBot v11 对应的 `aiocqhttp` 适配器主动发送群消息。

新目标没有游标时不发送 `after`，由 ReMail 在订阅确认帧中返回当前服务端游标，
不使用 AstrBot 主机时钟。项目上线通知不新增项目字段、专用上线时间或补发状态；通知
属于尽力而为。所有订阅主题继续共享一条服务端事件顺序。

## 反向代理

反向代理必须为 `/v1/bot/ws` 保留 `Upgrade`、`Connection`、`X-System-Key` 和
`X-Bot-Channel` 请求头，关闭响应缓冲，并将空闲超时设置为大于 60 秒。TLS 在代理
终止时，代理到 ReMail 的网络也必须处于可信私网。
