# icloudvalidate

## 替换一个隐私邮箱

`-replace-private-email` 接受一个完整的 `@icloud.com` 隐私邮箱地址。CMD 登录该账号的 Apple Account 管理页面，依次确认停用、永久删除，再创建一个新的隐私邮箱。每次仅替换指定地址，不接受通配符或批量地址。

```bash
go run ./cmd/icloudvalidate \
  -file /secure/apple-account.txt \
  -state /secure/replace-private-email.json \
  -owner-user-id 1 \
  -replace-private-email old-alias@icloud.com
```

使用现有 CMD 的数据库、Redis 和代理配置。输入文件只放一行账号信息，可使用 `账号----密码----设备接码URL`，也支持原有完整账号格式。已有可用的管理登录检查点会被复用；会话过期时，仅恢复管理登录。接码 URL 沿用公网访问限制，且不携带平台管理 Key。

已入库账号需要先在控制台禁用资源，并结束仍在执行或等待中的导入、Cookie 恢复任务；`-owner-user-id` 必须是资源的实际归属用户。被订单占用的别名会被拒绝。CMD 同步本地别名列表及当前 Apple Account Cookie，旧地址标记为已删除，新地址按现有转发域规则入库；资源保持禁用，由操作者核对后恢复启用。未入库账号仅执行 Apple 端操作。

中断后使用**相同账号输入、相同参数及同一份 `-state` 文件**重跑。检查点保存旧邮箱 ID、新候选地址及执行进度；已停用或已删除的步骤会按远端列表跳过，已完成的替换不会再次创建。创建请求超时或响应丢失时，重跑只查询原候选地址；在无法确认其结果时停止，不重发创建。此时需保留检查点并在 Apple 页面核对该候选地址。明确的 HTTP 4xx 拒绝（408 除外）允许下次重跑提交同一候选地址。

替换模式不支持 `-reset-state`，也不能与转发配置、家庭共享确认或旧版导入选项组合。每次新的替换使用独立的状态文件；同一账号的替换命令通过 Redis 互斥。不要让多个账号的命令并发写同一份状态文件。

### `delete.har` 协议依据

| 步骤 | 请求 | 成功确认 |
| --- | --- | --- |
| 查列表 | `GET /account/manage/email/private` | 读取 `privateEmailList` 和 `inactivePrivateEmailList`，按邮箱地址和 ID 匹配 |
| 停用 | `DELETE /account/manage/email/private/{id}/stop`，无请求体 | HTTP 200 且 `active:false`；再查列表确认停用 |
| 删除 | `DELETE /account/manage/email/private/{id}/remove`，无请求体 | HTTP 200 且 `active:false`；再查列表确认地址消失 |
| 生成候选 | `POST /account/manage/email/private/add`，请求体 `{}` | 保存返回的候选地址 |
| 完成创建 | `PUT /account/manage/email/private/add/complete` | 提交保存的地址及 `label:"ReMail"`，再查列表确认该地址处于启用状态 |

以上请求发往 `appleid.apple.com`，复用管理登录 Cookie、API Key、`scnt` 及原有 Apple 网页请求头。HAR 的 `exists:false` 也会出现在仍存在的邮箱上，不能单独作为已删除的依据。HAR 没有包含新建过程，新建步骤复用仓库中现有实现。

永久删除后旧地址不再收信，新建得到的是不同地址；删除与新建之间可能因网络或 Apple 限流中断。HAR 和状态文件含会话信息，应保存在私有路径，禁止提交到仓库。
