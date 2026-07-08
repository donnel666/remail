# BC-IAM 身份与访问上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-06-30 | V1.1 | Codex | 补充 P1-I1 当前用户直接改密接口和验证码交互规则；不改变 IAM 角色、Casbin 和错误策略。 |
| 2026-07-01 | V1.2 | Codex | 补充供应商申请流程；普通用户申请成为 supplier 只提升角色，不改变资源状态。 |
| 2026-07-06 | V1.3 | Codex | 补充管理员用户查询筛选能力：`GET /v1/admin/users` 支持 `ids` 批量精确查询和 `search` 邮箱/昵称/ID 搜索，用于后台私有项目授权选择；不改变 IAM 角色、Casbin 和用户实体。 |
| 2026-07-07 | V1.4 | Codex | 补充用户侧 aff 邀请链接能力；复用邀请码消费事务，不改变后台邀请码策略；GET 只读、POST 创建或获取，保持 safe method 语义。 |
| 2026-07-08 | V1.5 | Codex | 强制纠偏：移除旧数字权限等级，改为 RBAC `role` + Casbin 权限；新增 `userGroup` 作为权益分组，不参与后台授权。 |

> 通用域。BC-IAM 回答“你是谁、你能做什么”。管理员、供应商、普通用户共用一张用户表。

---

## 1. 定位

| 拥有 | 不拥有 |
|------|--------|
| 用户、登录会话、RBAC 角色、权益分组、Casbin 权限策略、邀请码、首次激活 | 钱包、订单、资源、项目业务状态 |

参考 `new-api`：特权用户拥有非特权用户的全部能力，只增加特权页面和命令。

---

## 2. 实体

| 实体 | 关键字段 |
|------|----------|
| `User` | `email`、`passwordHash`、`enabled`、`role`、`userGroupId`、`tokenVersion`、`lastLoginAt` |
| `UserGroup` | `code`、`name`、`description`、`enabled` |
| `Invite` | `code`、`enabled`、`maxUse/used`、`expireAt` |
| `InviteUse` | 邀请码使用事实 |
| `ThirdPartyIdentity` | 第三方账号绑定 |
| `UserLoginDevice` | 设备指纹和最近登录 |
| `CasbinRule` | Casbin policy 存储 |
| `SupplierApplication` | 普通用户申请 supplier 权限的审核记录 |

RBAC 角色：

| 角色 | 能力 |
|------|------|
| `user` | 普通用户能力。 |
| `supplier` | 拥有 user 全部能力，增加供应商页面。 |
| `admin` | 拥有 user 全部能力，后台运营能力由 Casbin seed 策略授予。 |
| `super_admin` | 拥有 admin 全部能力，系统敏感能力由 Casbin seed 策略授予。 |

`supplier` 和 `admin` 都是特权身份，但数据归属不同。管理员能查看/处理全局数据，不表示拥有供应商收入。

用户分组：

| 分组 | 说明 |
|------|------|
| `normal` | 默认权益分组。 |
| `VIP1/VIP2/...` | 后续用于额度、折扣、资源权益等业务策略。 |

`UserGroup` 只表达权益分组，不表达后台访问权限；后台访问权限只能由 `role` + Casbin policy 决定。

---

## 3. Casbin 使用模型

引入 Casbin，但只解决授权策略，不承载业务规则。

| 内容 | 归属 |
|------|------|
| 菜单、页面、按钮、后台命令权限 | Casbin |
| 角色 baseline 权限 | Casbin seed |
| 单用户权限覆盖 | Casbin policy |
| 订单归属、资源 owner、项目私有授权、钱包归属 | 业务代码/SQL，不进 Casbin |
| 订单状态、退款条件、分配状态 | 领域状态机，不进 Casbin |

Casbin 模型：

```text
sub = user:{userId} 或 role:{role}
obj = permission resource，例如 trade:order
act = read/write/operate/sensitive
eft = allow/deny
```

授权流程：

```text
1. 先校验登录态/API Key 等入口凭证是否有效；pickup 的 `email + token` 只在取件入口校验
2. 菜单、后台页面和特权命令调用 Casbin 检查 permission
3. 需要区分业务身份时读取 RBAC `role`，例如 supplier 是否允许发布资源
4. 最后由业务服务检查数据归属和状态机
```

---

## 4. 首次激活

空用户表时，后台进入激活页。

| API | 规则 |
|-----|------|
| `GET /v1/activation` | 返回是否需要激活，不返回本机文件路径或密码。 |
| `POST /v1/activation` | 仅当用户表为空时创建首个 `super_admin`，密码由操作人提交。 |

激活必须在数据库事务中串行化；一旦存在任意用户，激活接口返回 `409 Conflict`。

---

## 5. 认证规则

| 场景 | 规则 |
|------|------|
| 登录 | `POST /v1/sessions`，成功设置 HttpOnly Cookie，返回当前用户概要。 |
| 登出 | `DELETE /v1/sessions/current`。 |
| 当前用户 | `GET /v1/me`。 |
| 改密码 | 成功后递增 `tokenVersion`，清理旧 Session。 |
| 禁用用户 | 递增 `tokenVersion`，清理旧 Session 和 API Key 缓存。 |
| 图形验证码 | `POST /v1/captchas` 创建，答案不进响应和日志。 |
| 邮箱验证码 | `POST /v1/email/code` 发送，发送幂等，验证码不进日志。 |

认证错误：

| 场景 | HTTP | message |
|------|------|---------|
| 未登录 | `401` | `Authentication is required.` |
| 账号或密码错误 | `422` | `Account or password is incorrect.` |
| 图形验证码错误 | `422` | `Captcha is incorrect or expired.` |
| 邮箱验证码错误 | `422` | `Verification code is incorrect or expired.` |
| 权限不足 | `403` | `Permission denied.` |

---

## 6. 不变式

| 编号 | 规则 |
|------|------|
| INV-I1 | `email` 全局唯一。 |
| INV-I2 | 登录和 API Key 访问要求 `User.enabled=true`。 |
| INV-I3 | 禁用、改密、强制退出必须递增 `tokenVersion` 并清理会话。 |
| INV-I4 | 不存在数字权限等级；后台权限由 RBAC `role` + Casbin policy 表达，权益由 `UserGroup` 表达。 |
| INV-I5 | 特权用户拥有普通用户基础能力，不能要求管理员使用另一套用户接口。 |
| INV-I6 | 邀请码使用必须原子递增，不能并发突破次数。 |
| INV-I7 | 权限变更必须写 OperationLog，并刷新 Casbin enforcer/cache。 |
| INV-I8 | 首次激活只允许发生一次。 |
| INV-I9 | 同一用户同时只能有一个 `reviewing` 供应商申请。审批通过只提升角色，不自动发布任何资源。 |

---

## 7. Port

| Port | 方向 | 职责 |
|------|------|------|
| `PermissionPort` | 入站自全域 | 判断用户是否具备某权限。 |
| `UserPort` | 入站自全域 | 查询用户启用状态、RBAC 角色和权益分组。 |
| `SessionPort` | 入站自管理命令 | 清理用户会话和权限缓存。 |

---

## 8. API 设计

认证：

| 方法 | URI | 说明 |
|------|-----|------|
| `POST` | `/v1/sessions` | 登录。 |
| `DELETE` | `/v1/sessions/current` | 登出。 |
| `GET` | `/v1/me` | 当前用户。 |
| `POST` | `/v1/captchas` | 创建图形验证码。 |
| `POST` | `/v1/email/code` | 发送邮箱验证码。 |
| `POST` | `/v1/users` | 注册用户。 |
| `POST` | `/v1/password/reset/request` | 创建找回密码请求。 |
| `POST` | `/v1/password/reset` | 执行重置。 |
| `GET` | `/v1/activation` | 查询系统是否需要首次激活。 |
| `POST` | `/v1/activation` | 首次激活系统并创建超级管理员。 |

补充设计：

| 方法 | URI | 说明 |
|------|-----|------|
| `PATCH` | `/v1/password` | 当前登录用户修改密码；补足 P1-I1 “改密”验收项，成功后仍按原规则递增 `tokenVersion` 并清理旧 Session。 |
| `GET` | `/v1/me/invite` | 当前用户读取自己的 aff 邀请码；未创建时返回 `404 Resource not found.`。 |
| `POST` | `/v1/me/invite` | 当前用户创建或获取自己的 aff 邀请码；前端拼接为 `/register?aff={code}`。 |

用户 aff 邀请补充设计：

| 规则 | 说明 |
|------|------|
| 关系归属 | IAM 只拥有邀请码和 `InviteUse` 使用事实，不处理返佣金额。 |
| 类型隔离 | `Invite` 增加 `inviteKind=admin/referral`；后台邀请码列表和操作只处理 `admin`，用户 aff 链接只生成 `referral`。 |
| 注册入口 | 前端使用 `aff` URL 参数，但后端注册仍提交 `inviteCode`，不把外部 URL 命名扩散到领域模型。 |
| 并发约束 | 每个用户最多一个 `referral` 邀请码，由数据库唯一约束兜底；邀请码消费仍按 INV-I6 原子递增。 |

验证码补充设计：

| 场景 | 规则 |
|------|------|
| 发送注册邮箱验证码 | `POST /v1/email/code` 必须提交图形验证码，图形验证码只控制发邮件动作。 |
| 注册用户 | `POST /v1/users` 必须提交邮箱验证码，邮箱验证码控制最终注册动作。 |
| 发送找回密码邮箱验证码 | `POST /v1/password/reset/request` 必须提交图形验证码，图形验证码只控制发邮件动作。 |
| 执行找回密码 | `POST /v1/password/reset` 必须提交邮箱验证码，邮箱验证码控制最终重置动作。 |
| 图形验证码题目 | 使用九九乘法表范围内的简单四则运算，运算符使用数学符号 `+`、`−`、`×`、`÷`。 |

供应商申请补充设计：

| 方法 | URI | 说明 |
|------|-----|------|
| `POST` | `/v1/supplier-applications` | 当前普通用户提交供应商申请，只提交申请理由。 |
| `GET` | `/v1/supplier-applications/current` | 查询当前用户最新供应商申请，用于“出售”按钮分流。 |

`SupplierApplication` 状态：

```text
reviewing
approved
rejected
canceled
```

普通用户点击资源“出售”时，如果没有 `reviewing` 申请，则提交申请理由；如果已有 `reviewing` 申请，则前端提示“供应商申请正在审核中”。管理员审批通过后将申请人 RBAC `role` 设置为 `supplier`。审批通过不改变任何 Microsoft 资源的 `forSale`，用户仍需在资源页主动发布出售。

后台：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/admin/users` | 用户查询；支持 `ids` 批量精确查询和 `search` 邮箱/昵称/ID 搜索。 |
| `PATCH` | `/v1/admin/users/{userId}` | 启停、RBAC 角色、权益分组变更。 |
| `GET` | `/v1/admin/user-groups` | 用户权益分组列表。 |
| `POST` | `/v1/admin/user-groups` | 创建用户权益分组。 |
| `PATCH` | `/v1/admin/user-groups/{groupId}` | 更新用户权益分组名称、描述和启停状态。 |
| `POST` | `/v1/admin/users/{userId}/sessions/revoke` | 强制退出。 |
| `GET` | `/v1/admin/permissions` | 权限目录。 |
| `GET` | `/v1/admin/users/{userId}/permissions` | 用户权限矩阵。 |
| `PUT` | `/v1/admin/users/{userId}/permissions` | 保存用户权限覆盖。 |
| `GET` | `/v1/admin/invites` | 邀请码查询。 |
| `POST` | `/v1/admin/invites` | 创建邀请码。 |
| `PATCH` | `/v1/admin/invites/{code}` | 启停/调整邀请码。 |
| `GET` | `/v1/admin/supplier-applications` | 供应商申请列表。 |
| `POST` | `/v1/admin/supplier-applications/{applicationId}/approve` | 审批通过供应商申请，将申请人提升为 supplier。 |
| `POST` | `/v1/admin/supplier-applications/{applicationId}/reject` | 驳回供应商申请，必须记录安全审核原因。 |

---

## 9. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-IAM-1 | 单一用户表 + RBAC 角色 | 管理员和供应商不是另一类账号。 |
| ADR-IAM-2 | 引入 Casbin | 管理权限矩阵和用户覆盖用成熟库，避免自研。 |
| ADR-IAM-3 | Casbin 不做数据归属 | 项目授权、订单归属、钱包归属是业务规则，应由业务域控制。 |
| ADR-IAM-4 | 特权继承低权限能力 | 符合 `new-api` 思路，减少重复用户/管理员接口。 |
