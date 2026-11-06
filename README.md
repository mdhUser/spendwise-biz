# spendwise-biz

SpendWise 的核心后端业务仓库。已实现 `ledger` 模块（记账、账户/分类、流水、汇总、节省目标实现表），以及 M2 的 `catalog`（用户品类/商品/SKU/人工报价/个人清单）和 `conversation`（会话、消息、限流）模块。

认证由独立仓库 [`spendwise-auth`](../spendwise-auth) 负责。两个服务共用同一个 **MySQL 8.4** 数据库 `spendwise`：

- `spendwise-auth` 独占 `users`、`identities`、`verification_challenges`、`sessions`、`oauth_states`、`oauth_exchange_codes`，是唯一认证服务。
- `spendwise-biz` 只拥有 ledger 表；`owner_id` / `actor_user_id` 保存 auth 的 `users.id` 字符串（`VARCHAR(191)`），但**没有跨服务外键**。
- biz 不读取 auth 的 `users` / `sessions` 表。M0 起优先本地验签 Ed25519 JWT：启动时加载 `BIZ_AUTH_JWKS_URL`，进程内缓存 5 分钟并用 ETag 条件请求刷新；只有本地验签失败时才回退 `GET {BIZ_AUTH_BASE_URL}/v1/auth/me`（兼容期）。JWT 路径每请求零 auth RTT；响应头 `X-Auth-Method: jwt|opaque` 可用于确认命中路径。
- `/readyz` 同时检查 MySQL、迁移版本和 JWKS 是否已加载。启动时 JWKS 拉取失败会保持 503，直到成功或配置了 `BIZ_AUTH_JWKS_STATIC_PUBLIC_KEY` 兜底公钥。

现有 Next.js/Prisma 应用仍在另一个仓库，继续使用 PostgreSQL；它与本服务只通过 HTTP/OpenAPI 协作。

## 技术栈

- Go 1.26+，标准库 `net/http`
- MySQL 8.4（最低 8.0.30），`database/sql` + `go-sql-driver/mysql`
- `goose` 嵌入迁移（biz 使用独立的 `goose_db_version`；auth 使用 `schema_migrations`）
- UUIDv7（资源 ID 使用 `BINARY(16)`）、`log/slog`
- 模块化单体：`ledger` / `catalog` / `conversation` 业务模块 + `identity` JWT 验签/JWKS 缓存 + `authclient` 旧 token 回退适配器

## 本地启动

需要 Go 1.26+、Docker（用于 MySQL）和 `curl`。推荐直接使用一体化栈（一个 MySQL、auth、biz）：

```bash
docker compose -f compose.integration.yaml up --build
# auth:  http://127.0.0.1:8081
# biz:   http://127.0.0.1:8080
# MySQL: 127.0.0.1:3307 / spendwise
```

如果只想在宿主机跑 Go 进程，复用同一个 MySQL：

```bash
# 只启动共享 MySQL（3307），不要另起 biz compose 里的 3306 MySQL
docker compose -f compose.integration.yaml up -d db

cd ../spendwise-auth
cp .env.example .env
# AUTH_DATABASE_URL 默认已指向 127.0.0.1:3307/spendwise
go run ./cmd/server

cd ../spendwise-biz
cp .env.example .env
# 修改 MYSQL_APP_PASSWORD / MYSQL_MIGRATOR_PASSWORD
# BIZ_MYSQL_DSN / BIZ_MIGRATION_DSN 默认已指向 127.0.0.1:3307/spendwise
go run ./cmd/migrate
go run ./cmd/api
```

默认监听 `http://127.0.0.1:8080`。检查：

```bash
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/readyz
```


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
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["accessToken"])')

# 2. 查看业务服务自动创建的默认账户/分类。
#    -D 打印响应头：X-Auth-Method 应为 jwt（说明没有回调 /v1/auth/me）
curl -s -D - http://127.0.0.1:8080/v1/ledger/accounts -H "Authorization: Bearer $TOKEN"
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
| GET/POST | `/v1/catalog/categories` | 用户品类列表/创建 |
| PATCH/DELETE | `/v1/catalog/categories/{id}` | 更新/删除品类 |
| GET/POST | `/v1/catalog/products` | 商品列表/创建 |
| GET/PATCH/DELETE | `/v1/catalog/products/{id}` | 商品详情/更新/删除 |
| GET/POST | `/v1/catalog/products/{id}/skus` | SKU 列表/创建（可带首次报价） |
| PATCH/DELETE | `/v1/catalog/skus/{id}` | 更新/删除 SKU |
| POST | `/v1/catalog/skus/{id}/quotes` | 追加人工报价快照 |
| GET | `/v1/library` | 批量返回品类/商品/SKU/最新报价 |
| GET/POST | `/v1/library/items` | 个人清单条目 |
| DELETE | `/v1/library/items/{id}` | 删除清单条目 |
| GET/POST | `/v1/conversations` | 会话列表/创建 |
| GET/DELETE | `/v1/conversations/{id}` | 会话详情（含消息）/删除 |
| DELETE | `/v1/conversations` | 删除全部会话 |
| GET/POST | `/v1/conversations/{id}/messages` | 消息列表/追加用户消息 |

认证接口（`/v1/auth/*`）全部由 `spendwise-auth` 提供，biz 不再暴露。

约定：

- 金额一律整数分；API 字段 `amountFen`、`baselineFen`、`savedFen` 等。
- 时间使用带时区的 RFC3339；数据库存 UTC，业务期间按 `Asia/Shanghai`。
- 期间使用 `YYYY-MM`；日期筛选使用 `YYYY-MM-DD`（闭区间）。
- 更新接口必须带当前 `version`；冲突返回 `409 VERSION_CONFLICT`。
- 幂等重放返回 HTTP 200 + `Idempotent-Replay: true`；同 key 不同请求返回 `409 IDEMPOTENCY_MISMATCH`。
- 认证服务不可用时返回 `503 AUTH_UNAVAILABLE`，客户端应稍后重试而不是重新登录。

完整契约见 `api/openapi/ledger.yaml`；认证契约见 `../spendwise-auth/api/openapi/auth.yaml`。

### M0 认证验证

```bash
# JWKS 应返回带 kid 的 Ed25519 公钥和 ETag
curl -i http://127.0.0.1:8081/v1/auth/jwks

# JWT 路径：X-Auth-Method: jwt，且 auth 日志中不应出现对应的 /v1/auth/me
curl -i http://127.0.0.1:8080/v1/ledger/accounts -H "Authorization: Bearer $TOKEN"

# 旧 opaque token 路径仍可用（回退）：X-Auth-Method: opaque
curl -i http://127.0.0.1:8080/v1/ledger/accounts -H "Authorization: Bearer $LEGACY_OPAQUE_TOKEN"

# 刷新并轮换 refresh token（浏览器用 Cookie；命令行可直接传 body token）
curl -i -X POST http://127.0.0.1:8081/v1/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refreshToken":"<refresh-token>"}'
```

生产必须设置 `BIZ_AUTH_JWKS_URL`、`BIZ_AUTH_ISSUER=spendwise-auth`、`BIZ_AUTH_AUDIENCE=spendwise-api`，并可选配置与 auth 活动公钥一致的 `BIZ_AUTH_JWKS_STATIC_PUBLIC_KEY` 兜底。JWKS 缓存 TTL 用 `BIZ_AUTH_JWKS_CACHE_TTL` 调整（默认 5m）。

## 模块结构

```text
cmd/api        HTTP 服务
cmd/migrate    goose 迁移（仅 ledger）
cmd/admin      清理过期幂等记录
internal/identity   JWT 本地验签、JWKS 5m 缓存 + ETag、静态公钥兜底
internal/authclient 旧 opaque token 回退（GET /v1/auth/me）
internal/ledger     记账/目标/实现表
internal/catalog    用户品类/商品/SKU/报价/清单
internal/conversation 会话/消息（M3 在其上增加 run 与 SSE）
internal/platform/ratelimit MySQL 固定窗口限流（多副本共享）
internal/httpserver 路由装配、JWT-first 鉴权、/readyz、默认数据、日志、恢复
internal/platform   config/db/httpx/apperr/id/migrate/ratelimit
docs/data-ownership.md  表所有权与模块边界（唯一登记处）
migrations/ledger       ledger 表
migrations/catalog      catalog_* 表
migrations/conversation conv_* 表
migrations/platform     rate_buckets / async_jobs
```

后续核心业务按 `internal/<module>/{domain,app,store,http}` 扩展；模块间不允许直接读写对方表或跨模块 JOIN。MySQL 中表按模块划分所有权，后续模块使用模块前缀。

## 当前限制

- M2 已迁入 catalog/conversation 的核心 CRUD 与限流；异步 run、worker、SSE、provider/LLM 在 M3 迁入。
- M1 已固化模块边界与表所有权，见 [`docs/data-ownership.md`](docs/data-ownership.md)。
- `catalog_products`/`catalog_offers` 为公共目录预留，M2 不暴露写接口；匿名用户数据默认不迁移。
- 商品/SKU 删除为硬删除（同模块级联），与旧 Prisma 行为一致；会话 30 天过期。
- 未实现 CSV 导入、转账、多币种、共享账本。
- 业务进程内没有入口 IP 限流；限流应放在 BFF/反向代理。
- 兼容期仍保留 opaque `/v1/auth/me` 回退；计划在旧 token 自然过期后删除（M6）。
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
BIZ_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/spendwise_test?parseTime=true&loc=UTC' go test ./internal/httpserver -v
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
