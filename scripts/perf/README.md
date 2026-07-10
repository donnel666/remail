# ReMail 性能验收

所有命令只允许在隔离的 benchmark 数据库执行。

## 1. 生成规模数据

按依赖顺序执行：

```bash
go run ./cmd/benchseed -profile resources -count 1000000
go run ./cmd/benchseed -profile aliases -count 10000000 -resources 1000000
go run ./cmd/benchseed -profile orders -count 10000000 -resources 1000000
go run ./cmd/benchseed -profile messages -count 5000000 -resources 1000000 -orders 10000000
```

默认每批 1000 行，可用 `-batch` 调整。脚本可重入，使用固定 benchmark ID 区间和 `INSERT IGNORE`。

`orders` profile 每单生成独立钱包流水和 3 条订单事件，并为最多 20 万个资源生成 active code 订单、Allocation 与 Pickup Token；其余订单作为 completed 历史事实。`messages` profile 将时间分布到最近 5 天，覆盖正常读取与 3 天保留清理计划。

## 2. SQL 计划

```bash
mysql "$MYSQL_DSN" < scripts/perf/explain.sql
```

将结果记录到验收报告，至少包含 MySQL 版本、CPU/内存/磁盘、表行数、chosen key、actual rows 和 actual time。未执行前只能标记“待验证”，不能写成已达标。

## 3. HTTP 混合压测

复制真实测试订单凭证列表。3000 Pickup/s 场景至少准备 3000 个不同 Token，避免把单 Token 1 QPS 限流误判为数据库吞吐：

```json
[
  {"email":"mailbox@example.com","token":"st_example"}
]
```

保存为 `scripts/perf/pickup-fixtures.json`，该文件不得提交。

目标负载：

```bash
k6 run \
  -e BASE_URL=http://127.0.0.1:8080 \
  -e API_KEYS="$API_KEY_1,$API_KEY_2,$API_KEY_3" \
  -e PROJECT_ID=1 \
  -e PRODUCT_ID=1 \
  -e PICKUP_FIXTURES=scripts/perf/pickup-fixtures.json \
  scripts/perf/k6.js
```

2 倍余量：

```bash
k6 run -e MULTIPLIER=2 -e DURATION=5m \
  -e BASE_URL=http://127.0.0.1:8080 \
  -e API_KEYS="$API_KEY_1,$API_KEY_2,$API_KEY_3,$API_KEY_4,$API_KEY_5,$API_KEY_6" \
  -e PROJECT_ID=1 \
  -e PRODUCT_ID=1 \
  -e PICKUP_FIXTURES=scripts/perf/pickup-fixtures.json \
  scripts/perf/k6.js
```

通过标准：

- 目标负载：下单 P95 ≤ 300ms、P99 ≤ 800ms。
- Pickup 摘要 P95 ≤ 50ms、P99 ≤ 100ms。
- 非业务 5xx < 0.1%，无重复订单、重复扣款或重复分配。
- Microsoft 模拟 Graph 到 Pickup 可见 P95 ≤ 10 秒。
- SMTP 注入到 Pickup 可见 P95 ≤ 2 秒。
- 连接池无持续等待，MySQL 无 lock wait timeout。

每个压测 API Key 的并发上限需设为 100，并确保总 RPM 覆盖目标下单速率；业务型 `422/429` 已标记为预期响应，不计入非业务 5xx 失败率。
