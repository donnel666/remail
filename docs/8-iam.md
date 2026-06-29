# BC-IAM 身份与访问上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |

> 通用域。BC-IAM 回答“你是谁、你能做什么”。管理员、供应商、普通用户共用一张用户表。

---

## 1. 定位

| 拥有 | 不拥有 |
|------|--------|
| 用户、登录会话、角色等级、Casbin 权限策略、邀请码、首次激活 | 钱包、订单、资源、项目业务状态 |

参考 `new-api`：特权用户拥有非特权用户的全部能力，只增加特权页面和命令。

---

## 2. 实体

| 实体 | 关键字段 |
|------|----------|
| `User` | `email`、`passwordHash`、`enabled`、`roleLevel`、`tokenVersion`、`lastLoginAt` |
| `Invite` | `code`、`enabled`、`maxUse/used`、`expireAt` |
| `InviteUse` | 邀请码使用事实 |
| `ThirdPartyIdentity` | 第三方账号绑定 |
| `UserLoginDevice` | 设备指纹和最近登录 |
| `CasbinRule` | Casbin policy 存储 |

角色等级：

| 角色 | 等级 | 能力 |
|------|------|------|
| `user` | 10 | 普通用户能力。 |
| `supplier` | 20 | 拥有 user 全部能力，增加供应商页面。 |
| `admin` | 80 | 拥有 user 全部能力，增加后台运营页面。 |
| `super_admin` | 100 | 拥有 admin 全部能力，增加系统敏感页面。 |

`supplier` 和 `admin` 都是特权身份，但数据归属不同。管理员能查看/处理全局数据，不表示拥有供应商收入。

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
1. 先校验登录态/API Key/OrderToken 是否有效
2. 再按 roleLevel 判断基础访问，例如 user 页面、admin 页面
3. 对特权命令调用 Casbin 检查 permission
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
| INV-I4 | `roleLevel` 只表达基础特权等级，细粒度后台权限由 Casbin 表达。 |
| INV-I5 | 特权用户拥有普通用户基础能力，不能要求管理员使用另一套用户接口。 |
| INV-I6 | 邀请码使用必须原子递增，不能并发突破次数。 |
| INV-I7 | 权限变更必须写 OperationLog，并刷新 Casbin enforcer/cache。 |
| INV-I8 | 首次激活只允许发生一次。 |

---

## 7. Port

| Port | 方向 | 职责 |
|------|------|------|
| `PermissionPort` | 入站自全域 | 判断用户是否具备某权限。 |
| `UserPort` | 入站自全域 | 查询用户启用状态和角色等级。 |
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

后台：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/admin/users` | 用户查询。 |
| `PATCH` | `/v1/admin/users/{userId}` | 启停、基础资料、角色等级变更。 |
| `POST` | `/v1/admin/users/{userId}/sessions/revoke` | 强制退出。 |
| `GET` | `/v1/admin/permissions` | 权限目录。 |
| `GET` | `/v1/admin/users/{userId}/permissions` | 用户权限矩阵。 |
| `PUT` | `/v1/admin/users/{userId}/permissions` | 保存用户权限覆盖。 |
| `GET` | `/v1/admin/invites` | 邀请码查询。 |
| `POST` | `/v1/admin/invites` | 创建邀请码。 |
| `PATCH` | `/v1/admin/invites/{code}` | 启停/调整邀请码。 |

---

## 9. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-IAM-1 | 单一用户表 + 角色等级 | 管理员和供应商不是另一类账号。 |
| ADR-IAM-2 | 引入 Casbin | 管理权限矩阵和用户覆盖用成熟库，避免自研。 |
| ADR-IAM-3 | Casbin 不做数据归属 | 项目授权、订单归属、钱包归属是业务规则，应由业务域控制。 |
| ADR-IAM-4 | 特权继承低权限能力 | 符合 `new-api` 思路，减少重复用户/管理员接口。 |
