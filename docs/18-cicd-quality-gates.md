# ReMail CI/CD 质量门禁

## 目标

- Pull Request 尽快得到静态检查、前后端测试和 Docker 可构建结果。
- `main` 只有通过全部质量门禁、真实依赖 smoke 后才构建并发布镜像。
- tag 复用前端构建产物生成 release，不重复安装和构建前端。
- 新提交自动取消同分支旧流水线；tag 构建不取消。

## 流程

1. `backend-static`：gofmt、OpenAPI Go 生成漂移、go vet、golangci-lint。
2. `backend-test-shard`：两个并行分片运行真实 Testcontainers MySQL 集成测试；不再启动未被测试使用的 MySQL/Redis/MinIO service。
3. `backend-test`：聚合两个分片，保留稳定的分支保护检查名。
4. `frontend`：pnpm 缓存、OpenAPI TypeScript 漂移、typecheck、Vitest、生产 build；上传一天有效的 `web-dist`。
5. PR：前三项通过后下载 `web-dist`，使用 `Dockerfile.ci` 验证 Docker build，不重复安装 Node 依赖或构建前端。
6. main/tag：下载 `web-dist`，运行 MySQL/Redis/MinIO smoke；通过后使用同一预构建前端生成并推送 GHCR。
7. tag：下载同一 `web-dist`，只构建 Go release 包。
8. main：镜像通过后部署；健康失败自动回滚上一镜像。

以 2026-07-09 成功流水线为基线，旧流程耗时约 7 分 40 秒，其中 backend-test 3 分 31 秒、smoke 1 分 39 秒、image 2 分钟。`Dockerfile.ci` 已在本地冷缓存完成构建（约 82 秒），证明预构建前端路径可用；新流水线最终耗时仍以首次 GitHub Actions 数据为准，不预先宣称达标。

## 本地提交前

```bash
gofmt -w api cmd internal
go generate ./api
go vet ./...
go test ./... -run '^$'
pnpm --dir web generate:api
pnpm --dir web typecheck
pnpm --dir web test
pnpm --dir web build
```

需要验证 migration、事务和并发时，再执行完整 `go test ./...`；该命令需要 Docker。

## 分支保护

至少要求以下检查成功：

- `backend-static`
- `backend-test`
- `frontend`
- `docker`（Pull Request）

main 的 smoke、image、deploy 属于推送后发布门禁，不作为 PR 合并检查。

## Cleanup 故障说明

2026-07-10 的定时 Cleanup 失败发生在 GHCR 清理任务。根因是 jq `any` 把 tags 数组整体传给 `startswith`，在非字符串输入上以状态码 5 退出。当前清理逻辑已进一步简化：GHCR 按更新时间只保留最新 3 个 package versions，旧 tag 不再无限保护；Actions Cache 按最近访问时间只保留最新 3 个。

Cleanup 无法读取 GHCR package 时只发 warning 并退出成功，不应影响主 CI；真正删除失败仍必须失败，避免误以为清理完成。
