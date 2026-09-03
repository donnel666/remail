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

QQ 群管理身份不再依赖 OneBot 自动识别。请在插件配置中手工填写
`qq_group_owner_id`，并把所有管理员 QQ 号逐项填入 `qq_group_admin_ids`。这两项统一应用于
当前插件连接的所有已授权 QQ 群：只有这些账号被 @时才触发群管理代接，工作日报也只会
私聊发送给 `qq_group_owner_id`。配置为空或格式不是正整数 QQ 号时，插件不会猜测群主、
不会回退到群成员资料，也不会把日报发送给自动识别出的账号。
多个白名单群使用不同管理成员时，在 `qq_group_management` 中按
`群号|群主QQ号|管理员QQ号1,管理员QQ号2` 逐项配置；分群配置优先于全局配置。

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
不含 QQ/TG 身份参数的 AI 工具；当前项目价格使用专用 `remail_project_prices` 工具，发送者只从 AstrBot 当前事件注入。机器人自己的
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

QQ OneBot 白名单群中的普通成员 @手工配置的群主或管理员时，插件会按
`qq_group_owner_id` 和 `qq_group_admin_ids` 核对身份，并主动唤醒红夜处理原问题。红夜沿用已配置的人格、会话上下文和 ReMail 工具，
答复后会自然提醒用户以后直接联系红夜，非必要不要打扰群主或管理员。群主、管理员、
机器人自身发送的消息以及只 @普通成员的消息不会触发；管理员 QQ 号不会进入模型提示。

## 群聊触发

普通群聊不会触发 LLM 意图识别或 FAE 回复。用户必须明确 @红夜，或者由普通成员 @群主/
管理员触发上面的群管理代接；随后插件才调用一次独立意图分类。分类为 ReMail 时进入完整
FAE；@群主/管理员的非 ReMail 内容静默忽略，@红夜的非 ReMail 内容只回复服务范围，不进入
FAE。分类失败时，@红夜会提示暂时无法判断而不会误称问题无关，@群主/管理员继续静默。
显式 `/help`、`/项目`、`/诊断` 等插件命令仍直接处理。
未艾特、未发送命令的群消息只参与已启用的消息审核和工作日报收集，不会调用意图模型。
插件会在完成日报候选收集后明确停止这类事件，因此即使 AstrBot 开启群聊全量唤醒，红夜也
不会继续进入主 Agent 插话。
插件只在内存中保留同一会话最近 10 分钟内、经过脱敏且已确认属于 ReMail 的上一条消息，
用于判断“那多久”“还是不行”等省略追问；不会把完整群历史交给意图分类器。
追问缓存同时按群和发送者隔离，不会承接其他群成员的问题。正式 FAE 请求会清空 AstrBot
群会话历史、引用内容、群图片、音频和文件，只保留当前发送者本轮文本、已脱敏的同一发送者
上一条问题、ReMail 工具以及知识库结果，避免模型复述其他成员昵称、时间线或截图。

## ReAct 与最终润色

红夜会在主 Agent 中按 ReAct 方式补齐事实，并在事实足够时提前结束。AstrBot 4.27.x 的工具
循环轮数及硬上限完全跟随全局 `provider_settings.max_agent_step`；插件不覆盖、不校验也不阻止
该配置。需要调整轮数时，直接在 AstrBot WebUI 的模型提供商设置中修改“工具调用轮数上限”，
并按 AstrBot 的配置生效方式重载或重启。

主 Agent 完成工具循环后只产生事实草稿。插件随后使用当前会话的同一 Provider 进行一次
无工具、无历史上下文的最终润色，再执行群号、无关价格库存、兑换渠道和推测性原因过滤后
发送。润色前会把代码块、API 路径、URL 和关键数值替换为不可改写标记；润色结果必须完整
还原这些标记、保持事实状态一致且确实是 assistant 响应，否则回退原事实草稿。最终文本会
同步写回 AstrBot Agent 会话历史，确保下一轮看到的内容与用户收到的内容一致。群聊出口还会
确定性隐藏邮箱、订单号、验证码、账号和凭证，私聊保留当前用户可见信息。该流程要求非流式
输出，插件会为红夜的群聊艾特和私聊 LLM 请求关闭流式响应。
订单诊断是例外：一旦诊断工具返回安全 `projectName` 和 `message`，最终文本直接由这两个
事实生成，不再允许润色阶段新增第二个项目、改写原因或转录邮件内容。

为确保工具名和中间结果不对外显示，AstrBot 全局 `show_tool_use_status` 与
`show_tool_call_result` 必须同时关闭。插件会在每次 LLM 请求前校验；任一选项开启时会安全
拒绝本轮请求，不会进入 Agent 工具循环。

## 积分计费规则

插件会向每次红夜 LLM 请求追加稳定的公开计费规则：普通用户的接码和购买邮箱订单都从
ReMail 消费积分余额扣款，余额不足时必须先充值积分或兑换积分兑换码。
`https://catfk.com/shop/aishop6` 因手续费更低，是积分兑换码首选购买地址；
`https://pay.ldxp.cn/shop/aishop6` 仅作为备选。两个地址都只用于购买积分兑换码，用户
仍需回到 ReMail 兑换积分后再下单，它们不是邮箱订单的直接支付链接。当前充值方式、活动、
汇率和手续费继续以 FAQ、公告或 ReMail Web 充值页面为准。

所有项目接码价和购买邮箱价的单位都是 ReMail 积分，不是人民币。模型询问当前价格时必须
调用 `remail_project_prices`；该工具支持一次传入 `icloud,microsoft,domain` 等多个产品类型，
返回 `unit=ReMail积分`、`codePricePoints` 和 `purchasePricePoints`。`remail_projects.search`
只用于单个项目名称或目标平台关键词，禁止把多个邮箱类型或整句问题拼成一次搜索。

为防止旧 FAQ、公告或 AstrBot 知识库覆盖该顺序，插件会关闭 ReMail LLM 回复的流式输出，
并在发送前检查最终文本。只要回答包含 pay.ldxp，插件就统一输出 catfk 首选、pay.ldxp
备选和“兑换积分后回 ReMail 下单”的标准说明；不含 pay.ldxp 的回答保持原样。

插件同时向红夜追加稳定的服务模式规则：接码是标准 10 分钟的短期单次服务，购买邮箱是
可持续收件和接码的长效服务，标准质保 24 小时且质保不是使用期限。最终答复会无条件移除
TG群、QQ群、群号、加群及群推广内容；用户没有询问价格库存时，也会移除模型从旧 FAQ、
公告、知识库或历史中带出的价格库存。`/公告` 是直接命令回复，不经过这层 FAE 答复过滤。

## 工作日报

开启 `feedback_enabled` 后，插件只在 QQ OneBot 的 ReMail System Key 白名单群中收集反馈。用户可用
`/反馈 内容`、`/建议 内容` 显式提交；普通非命令、非 FAE 唤醒的群消息只会在通过
`GET /v1/bot/context` 鉴权后，以脱敏、截断后的文本作为工作日报候选，不会逐条回复或逐条
调用模型。每个 AstrBot 机器人实例、每个 QQ 群分别保存。

FAE 已尝试 ReMail 常见问题、API 文档和诊断工具仍无法可靠回答群内问题时，应调用无
业务参数的 `remail_record_unresolved` Tool。Tool 重新校验当前事件和群白名单，只记录
当前问题，再把“已记录并反馈研发”的安全结果交给 LLM，由 LLM 结合当前对话自然告知
用户，不向模型返回内部状态或原始数据。若 AstrBot 配置了显式 Tool 白名单，需要把本插件
实际使用的工具全部加入白名单，至少包括新增的 `remail_project_prices` 以及项目、库存、FAQ、
公告、API 文档和诊断工具；模型和 Provider 必须支持 function calling。AstrBot 默认可能显示
工具调用状态必须按上面的隐私要求关闭，否则插件会拒绝进入 Agent 工具循环。

插件按 `feedback_report_time` 配置的北京时间（`Asia/Shanghai`，`HH:MM`）每天生成一份
工作日报，并私聊投递给手工设置的 QQ 群主；存在 `qq_group_management` 分群配置时优先使用，
否则使用 `qq_group_owner_id`。插件不再自动识别群主，
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
