# spendwise-biz

SpendWise 的核心后端业务仓库。首期实现 `ledger` 模块：用户记账、账户/分类、支出/收入/退款流水、汇总，以及「开支节省目标实现表」。

认证由独立仓库 [`spendwise-auth`](../spendwise-auth) 负责。两个服务共用同一个 **MySQL 8.4** 数据库 `spendwise`：

- `spendwise-auth` 独占 `users`、`identities`、`verification_challenges`、`sessions`、`oauth_states`、`oauth_exchange_codes`，是唯一认证服务。
- `spendwise-biz` 只拥有 ledger 表；`owner_id` / `actor_user_id` 保存 auth 的 `users.id` 字符串（`VARCHAR(191)`），但**没有跨服务外键**。
- biz 不读取 auth 的 `users` / `sessions` 表。每个业务请求都通过 `GET {BIZ_AUTH_BASE_URL}/v1/auth/me` 校验 Bearer token：auth 返回 401 时 biz 返回 401，auth 5xx/网络错误时 biz 返回 `503 AUTH_UNAVAILABLE`。

现有 Next.js/Prisma 应用仍在另一个仓库，继续使用 PostgreSQL；它与本服务只通过 HTTP/OpenAPI 协作。

## 技术栈

- Go 1.26+，标准库 `net/http`
- MySQL 8.4（最低 8.0.30），`database/sql` + `go-sql-driver/mysql`
- `goose` 嵌入迁移（biz 使用独立的 `goose_db_version`；auth 使用 `schema_migrations`）
- UUIDv7（资源 ID 使用 `BINARY(16)`）、`log/slog`
- 模块化单体：`ledger` 业务模块 + `authclient` 认证适配层

## 本地启动

需要 Go 1.26+、Docker（用于 MySQL）和 `curl`。先启动认证服务：

```bash
cd ../spendwise-auth
cp .env.example .env
docker compose up --build
# auth: http://127.0.0.1:8081，MySQL: 127.0.0.1:3307
```

再启动业务服务：

```bash
cp .env.example .env
# 修改 .env 中的 MYSQL_ROOT_PASSWORD、MYSQL_APP_PASSWORD、MYSQL_MIGRATOR_PASSWORD
# 把 BIZ_MYSQL_DSN / BIZ_MIGRATION_DSN 的密码改成与上面一致
# 默认 BIZ_AUTH_BASE_URL=http://127.0.0.1:8081

docker compose up -d db
# 首次初始化会执行 db/init/01-users.sh，创建 biz_migrator 并收回 biz_app 的 DDL 权限
# 如果 mysql_data 卷已存在，需要手动执行该脚本或重建卷
go run ./cmd/migrate
go run ./cmd/api
```

默认监听 `http://127.0.0.1:8080`。检查：

```bash
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/readyz
```

如果希望一个 compose 栈同时跑 auth + biz + 共享 MySQL，使用：

```bash
docker compose -f compose.integration.yaml up --build
```

该文件使用同一个 `spendwise` 数据库，并把 `BIZ_AUTH_BASE_URL` 指向 `http://auth:8081`。仅用于本地联调。

## 快速验证

```bash
# 1. 从 spendwise-auth 获取 token（本地 AUTH_EXPOSE_DEV_CODES=true）
START=$(curl -s -X POST http://127.0.0.1:8081/v1/auth/email/start \
  -H 'Content-Type: application/json' -d '{"email":"demo@example.com"}')
CID=$(python3 -c 'import sys,json; print(json.load(sys.stdin)["challenge_id"])' <<<"$START")
CODE=$(python3 -c 'import sys,json; print(json.load(sys.stdin)["dev_code"])' <<<"$START")
TOKEN=$(curl -s -X POST http://127.0.0.1:8081/v1/auth/email/verify \
  -H 'Content-Type: application/json' \
  -d "{\"challenge_id\":\"$CID\",\"code\":\"$CODE\"}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')

# 2. 查看业务服务自动创建的默认账户/分类
curl -s http://127.0.0.1:8080/v1/ledger/accounts -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:8080/v1/ledger/categories -H "Authorization: Bearer $TOKEN"

# 3. 记一笔支出（categoryId/accountId 用上一步返回的 ID）
curl -s -X POST http://127.0.0.1:8080/v1/ledger/transactions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-tx-0001' \
  -d '{"type":"expense","amountFen":230000,"occurredAt":"2026-11-04T12:30:00+08:00","categoryId":"...","accountId":"...","note":"午饭"}'

# 4. 建目标
curl -s -X POST http://127.0.0.1:8080/v1/ledger/saving-goals \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-goal-0001' \
  -d '{"name":"每月少花 500 元","startPeriod":"2026-11","scope":{"kind":"all"},"baseline":{"kind":"fixed","baselineFen":300000},"targetSavingFen":50000}'

# 5. 查看实现表
curl -s "http://127.0.0.1:8080/v1/ledger/saving-goals/<goal-id>/achievements?from=2026-01&to=2026-12" \
  -H "Authorization: Bearer $TOKEN"
```

## API

所有业务接口都需要 `Authorization: Bearer <token>`；token 由 `spendwise-auth` 签发。业务创建接口还要求 `Idempotency-Key`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/v1/ledger/accounts` | 账户列表/创建 |
| PATCH/DELETE | `/v1/ledger/accounts/{id}` | 更新/归档账户 |
| GET/POST | `/v1/ledger/categories` | 分类列表/创建 |
| PATCH/DELETE | `/v1/ledger/categories/{id}` | 更新/归档分类 |
| GET/POST | `/v1/ledger/transactions` | 流水列表/创建 |
| GET/PATCH/DELETE | `/v1/ledger/transactions/{id}` | 详情/更新/软删 |
| POST | `/v1/ledger/transactions/{id}/restore` | 恢复 30 天内软删流水 |
| GET | `/v1/ledger/summary` | 按分类/月/日汇总 |
| GET/POST | `/v1/ledger/saving-goals` | 目标列表/创建 |
| GET/PATCH/DELETE | `/v1/ledger/saving-goals/{id}` | 详情/更新/归档 |
| POST | `/v1/ledger/saving-goals/{id}/terms` | 新增条款（下一期起） |
| GET | `/v1/ledger/saving-goals/{id}/achievements` | 实现表 |
| GET | `/v1/ledger/saving-goals/overview` | 当前月多目标概览 |

认证接口（`/v1/auth/*`）全部由 `spendwise-auth` 提供，biz 不再暴露。

约定：

- 金额一律整数分；API 字段 `amountFen`、`baselineFen`、`savedFen` 等。
- 时间使用带时区的 RFC3339；数据库存 UTC，业务期间按 `Asia/Shanghai`。
- 期间使用 `YYYY-MM`；日期筛选使用 `YYYY-MM-DD`（闭区间）。
- 更新接口必须带当前 `version`；冲突返回 `409 VERSION_CONFLICT`。
- 幂等重放返回 HTTP 200 + `Idempotent-Replay: true`；同 key 不同请求返回 `409 IDEMPOTENCY_MISMATCH`。
- 认证服务不可用时返回 `503 AUTH_UNAVAILABLE`，客户端应稍后重试而不是重新登录。

完整契约见 `api/openapi/ledger.yaml`。

## 模块结构

```text
cmd/api        HTTP 服务
cmd/migrate    goose 迁移（仅 ledger）
cmd/admin      清理过期幂等记录
internal/authclient 调用 spendwise-auth /v1/auth/me
internal/ledger     记账/目标/实现表
internal/httpserver 路由装配、Bearer 校验、默认数据、日志、恢复
internal/platform   config/db/httpx/apperr/id/migrate
migrations/ledger   ledger 表
```

后续核心业务按 `internal/<module>/{domain,app,store,http}` 扩展；模块间不允许直接读写对方表或跨模块 JOIN。MySQL 中表按模块划分所有权，后续模块使用模块前缀。

## 当前限制

- 首期只实现 `ledger`；购物编排、商品目录、平替/核算等尚未迁入。
- 未实现 CSV 导入、转账、多币种、共享账本。
- 业务进程内没有入口 IP 限流；限流应放在 BFF/反向代理。
- 幂等采用同事务占位行：并发同 key 会等待首个请求提交后重放；`IDEMPOTENCY_IN_PROGRESS` 只在异常遗留 pending 行时出现，不是常规路径。
- 标签目前只存取，不做筛选索引。
- MySQL 没有部分索引，软删除行仍占索引空间；后续按数据量加归档/清理。
- 目标实现表读时计算，不冻结已结束月份；补记历史流水会重算过去月份。
- 默认账户/分类由 biz 在用户首个已认证请求时懒创建（进程内每用户一次）；若创建失败只记录日志，下次请求重试。

## 开发检查

```bash
go test ./...
go vet ./...
go build ./...
```

真实 MySQL 集成测试：

```bash
BIZ_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/spendwise_test?parseTime=true&loc=UTC' go test ./internal/httpserver -run TestMySQLIntegration -v
```

清理过期幂等记录（可由 cron 调用）：

```bash
go run ./cmd/admin cleanup
```

## 安全说明

- `.env` 不提交；生产使用独立 `biz_migrator` 账号执行迁移，应用账号 `biz_app` 只有 DML 权限。
- 生产 MySQL 中 auth 与 biz 共用 `spendwise` 数据库，但使用不同账号；auth 表由 auth 迁移独占，biz 不建 `users` / `sessions` / `invites`，也不加跨服务外键。
- Go 服务不要直接暴露公网；由 Next.js BFF 代理并持有 HttpOnly Cookie。
- 生产 MySQL 需开启备份/binlog，并单独演练恢复；现有 PostgreSQL 备份不覆盖本服务。
