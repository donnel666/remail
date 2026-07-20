# ReMail 生产运行手册

## 部署基线

- 应用保持单活；MySQL、Redis、MinIO 可以独立部署和备份。
- 雷池 WAF 独立部署，业务站点反代到宿主机 `http://127.0.0.1:8080`；应用 Compose 不监听 80/443。
- 首次切换由运维人员手动停止并删除旧 Nginx 容器，CI 不清理 Compose orphan。
- 生产必须设置 `APP_ENV=production`、`SESSION_SECURE=true`。
- 生产必须设置 Cloudflare Turnstile 的 `TURNSTILE_SITE_KEY`、`TURNSTILE_SECRET_KEY`；测试环境默认使用 Cloudflare 官方 always-pass 测试密钥，生产拒绝使用测试密钥启动。
- 生产必须把 `TRUSTED_PROXIES` 设置为应用容器实际看到的反向代理精确 IP/CIDR；反向代理必须覆盖客户端自带的 `X-Forwarded-For`，只传递规范客户端地址。
- `PPROF_ADDR` 只能绑定 `localhost` 或回环 IP，禁止公网监听。
- 雷池对 `/pickup`、`/v1/pickup` 关闭或脱敏 query 日志，并禁用验证码、JS Challenge 等会改变 API 响应的挑战。
- 雷池必须阻止公网访问 `/metrics`，上传限制不得低于 100 MB，并传递 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`。

## Cloudflare Turnstile

1. Cloudflare 控制台进入 **Turnstile**，创建 widget，hostname 只填写 `remail.aishop6.com`。
2. Widget mode 选择 **Managed**；前端分别使用 `login`、`register_email_code`、`password_reset_code` action。
3. 把 Site Key 和 Secret Key 分别写入 GitHub Actions secrets：`TURNSTILE_SITE_KEY`、`TURNSTILE_SECRET_KEY`。
4. 后端只返回公开 Site Key；Secret Key 只存在于生产环境变量，不进入前端、响应或日志。
5. 发布后验证登录、注册发码、找回密码发码；重复提交同一 token 必须返回 `422`。

## 监控与告警

Prometheus 从宿主机抓取 `http://127.0.0.1:8080/metrics`；雷池 WAF 不向公网暴露该端点。首次部署必须确认抓取成功和指标名称存在。首版只保留能直接指导排障的指标：

- `remail_http_requests_total`、`remail_http_request_duration_seconds`
- `remail_db_open_connections`、`remail_db_in_use_connections`、`remail_db_wait_count_total`
- `remail_business_events_total`
- `remail_mail_visible_duration_seconds`
- `remail_task_queue_wait_seconds`

建议告警：

- 5 分钟 5xx 比例超过 1%。
- Checkout `failed`（非余额/库存业务失败）持续增长。
- DB in-use 长期达到连接池 90%，或 wait count 持续增长。
- Microsoft 邮件可见 P95 超过 10 秒，自建域名超过 2 秒。
- cleanup、retention、mail fetch 出现持续失败。

## MySQL 备份与恢复

- 开启 binlog，使用 ROW 格式并设置满足磁盘预算的保留期。
- 每天执行一次物理全量备份；备份文件写到独立存储，不与 MySQL 数据盘共用生命周期。
- 每月至少在隔离环境做一次恢复演练：恢复最近全量备份，再重放 binlog 到指定时间点。
- 恢复后检查：迁移版本、订单/钱包抽样一致性、活跃 Allocation 唯一约束、最近 Pickup。
- 未完成恢复演练的备份不算有效备份。

## Redis 与 MinIO

- Redis 开启 AOF。资源 validation 是可丢失、可重新提交的临时任务，MySQL 只保留资源状态；其他声明为 durable 的 Asynq 任务仍以各自 MySQL 事实为准。
- MinIO 为自建域名 `.eml` 提供 30 天短期存储；数据库行先删除，对象删除失败由 orphan GC 重试。
- MinIO 不承担订单、钱包和分配事实备份。

## 安全停机

收到 SIGTERM 后依次：

1. HTTP 停止接收新请求并等待在途请求。
2. 停止生命周期、保留和邮件调度器。
3. 停止 Asynq worker。
4. flush API Key quota。
5. 关闭 Redis 和 MySQL 连接。

任一步超过 30 秒应记录错误并继续退出，禁止无限等待。

## 日常容量检查

- 每周记录 orders、order_events、allocations、wallet_transactions 的行数和索引大小。
- 每日确认 Microsoft 消息仅保留 3 天、Domain 消息及 `.eml` 仅保留 30 天。
- 若 300 下单/s、3000 Pickup/s 目标负载已达标，不提前做分区、分库或新增缓存层。
