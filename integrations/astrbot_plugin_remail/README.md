# astrbot_plugin_remail

通用 ReMail 能力插件。QQ、Telegram、Discord 等平台都使用同一套
`/v1/bot/**` 接口；平台用户身份只取自当前 AstrBot 事件，不接受聊天参数。

## 配置

要求 AstrBot `>=4.27.4`。在 ReMail 系统设置为每个机器人实例创建
`purpose=bot` 的 System Key，然后把完整 Key 放进 AstrBot 进程环境变量；插件配置
只保存环境变量名，避免稳定版 AstrBot 的普通配置输入框显示明文 Key。例如：

```bash
export REMAIL_BOT_QQ_SYSTEM_KEY='sk_...'
```

在 `platform_system_keys` 中填写唯一的 AstrBot `platform id` 和
`REMAIL_BOT_QQ_SYSTEM_KEY`。事件请求必须精确命中 platform id，不会回退使用其他平台
的 Key。主动推送使用 `launch_system_key_env`，目标填写 AstrBot
`unified_msg_origin`。非本机 `base_url` 必须使用 HTTPS。

群聊调用还必须在 ReMail Bot System Key 中配置允许的群号。插件只从
AstrBot 当前事件的 `get_group_id()` 读取群号，HTTP 传入 `X-Bot-Group`，
WebSocket 传入 `groupId`；私聊不传该字段，用户命令和 LLM 工具也没有群号参数。
接口文档、公告和常见问题虽是公开内容，插件仍会先调用
`GET /v1/bot/context` 验证当前事件来源。未授权群不会得到这些本地或缓存回复。

QQ namespace 的 `subject` 必须是机器人从当前事件解析出的真实正整数 QQ 号；ReMail
不接受 QQ OpenID，也不允许用户命令或 LLM 参数填写、覆盖 QQ 号。如果所用适配器的
事件只能提供 OpenID，该适配器不能用于 QQ 账号绑定。ReMail 把该 QQ 号作为第三方
登录标识，在 `provider_user_id` 中明文保存。是否属于 QQ namespace 由 System Key
配置决定，不把 AstrBot `platform` 名称硬编码为某个适配器。

默认 `transport_mode=websocket`，会为每把
Key 主动连接 `/v1/bot/ws`，每 20 秒发送心跳，并在同一连接中复用 Bot API 请求和
接收项目上线、排行榜结算、系统通知/公告、邮箱折扣和项目价格更新。连接中断会自动
重连，命令请求在连接尚未建立时回退 HTTP；所有主题共用全局游标，并按通知目标分别
持久化，重连后从各目标最旧游标补发。某个目标发送失败时不会推进该目标游标。
需要兼容旧服务端时可显式设置 `transport_mode=http`，但 HTTP 模式只提供命令接口，
不提供主动推送。

WebSocket 订阅同时携带旧版 `topic=project.launched` 和新版 `topics` 数组，因此可以
平滑连接只支持项目上线的旧服务端。`launch_destinations` 仍是所有主动推送的
`unified_msg_origin` 目标列表。插件只按各主题公开 DTO 的字段白名单生成消息，不会
直接输出事件 JSON、未知字段、原始数据库内容、凭证或邮箱地址。项目上线通知复用
现有操作日志，属于尽力而为，不为它新增项目字段、上线时间表或专用补发状态。

插件提供 `/公告`、`/常见问题`、`/接口文档`、`/项目`、`/库存`、`/排行榜`、
`/排行榜奖励`、`/绑定`、`/绑定状态`、`/解绑` 和 `/查码`。同一能力也注册为
不含 QQ/TG 身份参数的 AI 工具，发送者只从 AstrBot 当前事件注入。机器人自己的
内部知识库继续在 AstrBot 中维护，不写入 ReMail；API 文档工具会读取并缓存 ReMail
公开 `/openapi.json`，只向模型返回与问题最相关的有界接口和 schema 片段。

## 敏感命令

`/绑定 邮箱 密码` 只允许私聊。插件加载时会统一脱敏 AstrBot 的消息概要，日志
只能出现 `/绑定 [REDACTED]`；命令 handler 会立即停止事件传播，密码不会进入
LLM 或会话历史。生产环境仍应关闭 trace/debug 原始消息日志。

QQ Official 是否允许主动群消息取决于 AstrBot/腾讯适配器；需要可靠主动广播时优先
使用明确支持 `send_by_session` 的 aiocqhttp/OneBot 适配器。绑定命令支持
`/绑定 邮箱 密码` 与 `/bind 邮箱 密码`，密码可以包含空格。
