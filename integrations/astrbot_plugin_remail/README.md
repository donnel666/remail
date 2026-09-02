# astrbot_plugin_remail

通用 ReMail 能力插件。QQ、Telegram 两个渠道使用同一套
`/v1/bot/**` 接口；平台用户身份只取自当前 AstrBot 事件，不接受聊天参数。

## 安装

要求 AstrBot `>=4.27.4`。本插件目前位于 ReMail 单体仓库的子目录，不能把整个
ReMail 仓库 URL 当成 AstrBot 插件仓库安装。请将本目录复制为
`AstrBot/data/plugins/astrbot_plugin_remail`，或仅压缩本目录后在 AstrBot WebUI 上传，
然后重载插件。AstrBot 会根据本目录的 `requirements.txt` 检查依赖。

## 配置

在 ReMail 系统设置中创建 `purpose=bot` 的 System Key，并选择机器人类型。QQ 类型固定
为 `platform=qq`、`namespace=qq:main`；Telegram 类型固定为
`platform=telegram`、`namespace=telegram:main`。AstrBot 插件只提供
`qq_system_key` 和 `telegram_system_key` 两项：当前只使用 QQ 时仅填写 QQ Key，接入
Telegram 后再填写 TG Key。插件按当前事件的 AstrBot 适配器选择 Key，用户不能选择
渠道。主动推送复用第一把已配置的 Key，目标填写 `/sid` 显示的 `UMO` 到
`launch_destinations`。QQ 和 Telegram 不能填写同一把 Key；非本机 `base_url` 必须使用
HTTPS。

配置 Schema 已按 AstrBot 当前在线文档设置 `secret: true`；支持该字段的新版 WebUI
会遮罩 Key，4.27.4/4.27.5 会忽略该显示属性但仍可正常读取配置。所有版本都会把 Key
明文保存在 `data/config/astrbot_plugin_remail_config.json`。只允许 AstrBot 运行账号
读取该文件和 `data/config` 目录，不要把它提交到版本库，也不要截图分享插件配置页。

群聊调用还必须在 ReMail Bot System Key 中配置允许的群号。插件只从
AstrBot 当前事件生成 `X-Bot-Channel` 并从 `get_group_id()` 读取群号。ReMail 先鉴权
System Key，再要求渠道与 Key 类型一致；类型不符统一返回 401。HTTP 群聊传入
`X-Bot-Group`，WebSocket 传入 `groupId`；私聊不传群号，用户命令和 LLM 工具也没有
渠道或群号参数。
接口文档、公告和常见问题虽是公开内容，插件仍会先调用
`GET /v1/bot/context` 验证当前事件来源。未授权群不会得到这些本地或缓存回复。

QQ namespace 的 `subject` 必须是机器人从当前事件解析出的真实正整数 QQ 号；ReMail
不接受 QQ OpenID，也不允许用户命令或 LLM 参数填写、覆盖 QQ 号。因此 QQ 必须使用
NapCat/OneBot 对应的 AstrBot `aiocqhttp` 适配器；`qq_official` 和
`qq_official_webhook` 只提供用户/群 OpenID，不满足绑定与QQ群白名单要求。ReMail 把
QQ 号作为对应渠道的第三方登录标识明文保存。渠道和 namespace 只由
通过认证的 System Key 类型决定，不接受聊天参数或请求字段覆盖。

Telegram namespace 的 `subject` 同样必须是事件提供的正整数用户 ID；群聊 Chat ID
必须是非零整数（群和超级群通常为负数），并命中该 Telegram Key 的群白名单。

身份映射完全取自 AstrBot 官方事件 API：`aiocqhttp` 将 OneBot 的 `sender.user_id`
作为 QQ号、`group_id` 作为QQ群号；Telegram 将 `from_user.id` 作为 TG 用户 ID、
`chat.id` 作为 TG 群 Chat ID。Telegram Topic 事件中的 `chatId#threadId` 会归一成
`chatId`，因此群白名单按真实 TG 群配置。QQ 事件只使用 `qq_system_key`，Telegram
事件只使用 `telegram_system_key`；ReMail 再从 Key 类型取得渠道和 namespace，所以
同一套 Bot API 可以兼容两种渠道，两类身份也不会共用绑定命名空间。

默认 `transport_mode=websocket`，会为每把
Key 主动连接 `/v1/bot/ws`，每 20 秒发送心跳，并在同一连接中复用 Bot API 请求和
接收项目上线、排行榜结算、系统通知/公告、邮箱折扣和项目价格更新。连接中断会自动
重连，命令请求在连接尚未建立时回退 HTTP；所有主题共用全局游标，并按通知目标分别
持久化，重连后从各目标最旧游标补发。某个目标发送失败时不会推进该目标游标。
需要兼容旧服务端时可显式设置 `transport_mode=http`，但 HTTP 模式只提供命令接口，
不提供主动推送。

`launch_destinations` 是主动推送的 `unified_msg_origin` 目标列表。插件只按各主题公开
DTO 的字段白名单生成消息，不会直接输出事件 JSON、未知字段、原始数据、凭证或邮箱
地址。项目上线通知属于尽力而为，不为它新增项目字段、上线时间表或专用补发状态。

插件提供 `/help`（兼容 `/帮助`、`/remail帮助`）、`/公告`、`/常见问题`、`/接口文档`、`/项目`、`/库存`、`/排行榜`、
`/排行榜奖励`、`/绑定`、`/绑定状态`、`/个人信息`、`/解绑` 和 `/诊断`（兼容 `/接码排查`、`/查码`）。同一能力也注册为
不含 QQ/TG 身份参数的 AI 工具，发送者只从 AstrBot 当前事件注入。机器人自己的
内部知识库继续在 AstrBot 中维护，不写入 ReMail；API 文档工具会读取并缓存 ReMail
公开 `/openapi.json`，只向模型返回与问题最相关的有界接口和 schema 片段。
`/help` 无论从私聊还是白名单群调用，帮助内容都只私聊发送给当前用户，不在群里回复；
Telegram 用户需要先主动私聊机器人 `/start`，否则平台可能拒绝主动私聊。
`/个人信息` 无论从私聊还是白名单群调用，余额、分组、角色和升级进度都只私聊当前发送者。

群聊和私聊都使用 `/诊断 邮箱 原因`。ReMail 根据当前发送者绑定的用户 ID 和邮箱
反查该用户自己的最近订单与公开项目，不接受用户提供项目 ID；邮箱不能用于查询其他
用户的数据。插件只把用户描述和 ReMail 返回的安全项目/诊断信息交给 AstrBot AI，
不会把邮箱、订单号、验证码、邮件内容或凭证放进模型提示词或最终回复。

## 新人欢迎

启用 `welcome_enabled` 并设置 `welcome_text` 后，新成员进入已授权群时，插件会先校验
当前群来源，再使用平台消息组件 @该成员并发送欢迎文本。未授权群、机器人自身加入事件
或空欢迎文本不会触发消息。欢迎文本最多发送 2000 个字符。

## 自动批准加群

启用 `auto_approve_join_requests` 后，插件只处理 QQ OneBot 白名单群中用户主动发起的加群
申请。NapCat 成功返回申请人的 QQ 等级且达到 `minimum_qq_level` 时自动批准；等级不足、
资料缺失、来源鉴权失败或批准动作失败时均保持待审核，不会自动拒绝。群邀请事件不参与
自动批准。机器人必须具有对应 QQ 群的审批权限。

## 群消息审核

`keyword_blacklist_enabled` 启用后，QQ OneBot 白名单群中包含
`keyword_blacklist` 任一关键词的消息会被撤回。关键词先做 Unicode 规范化，再按不区分
大小写的包含关系匹配；空关键词会被忽略。

`url_whitelist_enabled` 启用后，只允许 `url_whitelist_domains` 中的域名及其子域名。名单
为空时会撤回所有检测到 HTTP(S) URL 的消息。审核覆盖普通文本、Markdown、分享卡片和
JSON/XML 卡片中的当前消息内容，不检查回复引用的旧消息，也不把图片、语音、视频和文件
自身的下载地址当成用户发布的 URL。未授权群不会被插件撤回消息；机器人必须具有对应群
的消息撤回权限。撤回失败不会禁言、踢人或追加其他处罚。

## 群管理代接

QQ OneBot 白名单群中的普通成员 @群主或管理员时，插件会核对发送者和被 @成员的当前
群角色，并主动唤醒红夜处理原问题。红夜沿用已配置的人格、会话上下文和 ReMail 工具，
答复后会自然提醒用户以后直接联系红夜，非必要不要打扰群主或管理员。群主、管理员、
机器人自身发送的消息以及只 @普通成员的消息不会触发；管理员 QQ 号不会进入模型提示。

## 主动技术支持

白名单群成员无需 @机器人。对尚未唤醒且不是显式命令的群文本，插件先通过当前 AstrBot
对话模型执行一次独立意图分类；只有模型严格返回 `REMAIL` 时，才主动唤醒红夜沿用当前
Persona、会话上下文和 ReMail 工具答复并引导排查，其他结果或分类失败均静默忽略。已经
@机器人或触发群管理代接的消息不会重复分类。

分类前只保留经过脱敏、截断的当前消息，不发送 QQ/TG ID、完整邮箱、订单号、验证码、
密码、API Key、Token 或群历史；分类请求不携带工具和会话上下文。普通群消息会增加一次
模型调用，判定为 ReMail 咨询的消息会再进行一次正式答复调用。

## 工作日报

开启 `feedback_enabled` 后，插件只在 QQ OneBot 的 ReMail System Key 白名单群中收集反馈。用户可用
`/反馈 内容`、`/建议 内容` 显式提交；普通非命令、非 FAE 唤醒的群消息只会在通过
`GET /v1/bot/context` 鉴权后，以脱敏、截断后的文本作为工作日报候选，不会逐条回复或逐条
调用模型。每个 AstrBot 机器人实例、每个 QQ 群分别保存。

FAE 已尝试 ReMail 常见问题、API 文档和诊断工具仍无法可靠回答群内问题时，应调用无
业务参数的 `remail_record_unresolved` Tool。Tool 重新校验当前事件和群白名单，只记录
当前问题，再把“已记录并反馈研发”的安全结果交给 LLM，由 LLM 结合当前对话自然告知
用户，不向模型返回内部状态或原始数据。若 AstrBot 配置了显式 Tool 白名单，需要把这个
Tool 加入白名单；模型和 Provider 必须支持 function calling。AstrBot 默认可能显示
工具调用状态，如不希望用户看到该状态，可在全局设置中关闭 `show_tool_use_status`。

插件按 `feedback_report_time` 配置的北京时间（`Asia/Shanghai`，`HH:MM`）每天生成一份
工作日报，并私聊投递给对应 QQ 群主。插件通过 AstrBot `event.get_group()` 自动识别群主，
不提供 Telegram 工作日报。发送失败时不会回退到来源群、`launch_destinations` 或其他接收人，而是保留数据并
每五分钟重试，插件重启后也会补发。发送与插件 KV 之间没有事务，因此极端崩溃窗口下
同一日期的工作日报可能重复一次，但不会为了避免重复而静默丢失。

反馈条目和工作日报不保存发送者 QQ号、昵称、原始邮箱、订单号、验证码、密码、System
Key、数据库地址或 ReMail 原始响应；路由元数据只保存投递所需的群号和群主私聊 UMO，
工作日报会向该群主显示来源群号。单条候选、每日条数、模型提示词和工作日报都有上限。
如果使用外部大模型，只有经过上述脱敏和截断的候选文本会在配置时间汇总时发送给该模型。

## 敏感命令

`/绑定 邮箱 密码` 只允许私聊。插件加载时会在 AstrBot 日志、内容安全预处理和消息链
读取前统一脱敏为 `/绑定 [REDACTED]`；命令 handler 会立即停止事件传播，密码不会进入
LLM 或会话历史。生产环境仍应关闭适配器可能提供的 trace/debug 原始消息日志。

主动群消息是否可用取决于平台适配器；QQ 使用支持 `send_by_session` 的
aiocqhttp/OneBot。绑定命令支持 `/绑定 邮箱 密码` 与 `/bind 邮箱 密码`，密码可以
包含空格。
