# spendwise-biz

SpendWise 的核心后端业务仓库。首期实现 `ledger` 模块：用户记账、账户/分类、支出/收入/退款流水、汇总，以及「开支节省目标实现表」。

现有 Next.js/Prisma 应用仍在另一个仓库，继续使用 PostgreSQL；本服务使用独立的 **MySQL 8.4** 数据库 `spendwise_biz`，二者只通过 HTTP/OpenAPI 协作。

## 技术栈

- Go 1.26+，标准库 `net/http`
- MySQL 8.4（最低 8.0.30），`database/sql` + `go-sql-driver/mysql`
- `goose` 嵌入迁移
- Argon2id 密码、UUIDv7（`BINARY(16)`）、`log/slog`
- 模块化单体：`identity` 共享内核 + `ledger` 首期模块

## 本地启动

需要 Go 1.26+、Docker（用于 MySQL）和 `curl`。

```bash
cp .env.example .env
# 修改 .env 中的 MYSQL_ROOT_PASSWORD、MYSQL_APP_PASSWORD、BIZ_INVITE_PEPPER
# 同时把 BIZ_MYSQL_DSN / BIZ_MIGRATION_DSN 里的密码改成分别与
# MYSQL_APP_PASSWORD / MYSQL_MIGRATOR_PASSWORD 一致
# BIZ_INVITE_PEPPER 至少 32 个字符

docker compose up -d db
# 首次初始化会执行 db/init/01-users.sh，创建 biz_migrator 并收回 biz_app 的 DDL 权限
# 如果 mysql_data 卷已存在，需要手动执行该脚本或重建卷
go run ./cmd/migrate
go run ./cmd/admin invite create -uses 20 -ttl 720h
go run ./cmd/api
```

也可以直接：

```bash
docker compose up --build
```

然后在另一个终端生成邀请码：

```bash
docker compose run --rm biz /app/admin invite create -uses 20 -ttl 720h
```

默认监听 `http://127.0.0.1:8080`。检查：

```bash
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/readyz
```

## 快速验证

```bash
# 1. 注册（使用上一步输出的邀请码）
curl -s -X POST http://127.0.0.1:8080/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"correct horse battery staple","displayName":"Demo","inviteCode":"SW-XXXX-XXXX-XXXX-XXXX"}'

# 2. 登录并保存 token
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"correct horse battery staple"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

# 3. 查看注册时自动创建的账户/分类
curl -s http://127.0.0.1:8080/v1/ledger/accounts -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:8080/v1/ledger/categories -H "Authorization: Bearer $TOKEN"

# 4. 记一笔支出（categoryId/accountId 用上一步返回的 ID）
curl -s -X POST http://127.0.0.1:8080/v1/ledger/transactions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-tx-0001' \
  -d '{"type":"expense","amountFen":230000,"occurredAt":"2026-11-04T12:30:00+08:00","categoryId":"...","accountId":"...","note":"午饭"}'

# 5. 建目标
curl -s -X POST http://127.0.0.1:8080/v1/ledger/saving-goals \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-goal-0001' \
  -d '{"name":"每月少花 500 元","startPeriod":"2026-11","scope":{"kind":"all"},"baseline":{"kind":"fixed","baselineFen":300000},"targetSavingFen":50000}'

# 6. 查看实现表
curl -s "http://127.0.0.1:8080/v1/ledger/saving-goals/<goal-id>/achievements?from=2026-01&to=2026-12" \
  -H "Authorization: Bearer $TOKEN"
```

## API

所有业务接口都需要 `Authorization: Bearer <token>`；业务创建接口还要求 `Idempotency-Key`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/v1/auth/register` | 邀请码注册 |
| POST | `/v1/auth/login` | 登录 |
| POST | `/v1/auth/logout` | 撤销当前会话 |
| GET | `/v1/auth/me` | 当前用户/会话 |
| POST | `/v1/auth/password` | 修改密码 |
| DELETE | `/v1/auth/account` | 注销账号（密码 + `confirm=DELETE`） |
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

约定：

- 金额一律整数分；API 字段 `amountFen`、`baselineFen`、`savedFen` 等。
- 时间使用带时区的 RFC3339；数据库存 UTC，业务期间按 `Asia/Shanghai`。
- 期间使用 `YYYY-MM`；日期筛选使用 `YYYY-MM-DD`（闭区间）。
- 更新接口必须带当前 `version`；冲突返回 `409 VERSION_CONFLICT`。
- 幂等重放返回 HTTP 200 + `Idempotent-Replay: true`；同 key 不同请求返回 `409 IDEMPOTENCY_MISMATCH`。

完整契约见：

- `api/openapi/auth.yaml`
- `api/openapi/ledger.yaml`

## 模块结构

```text
cmd/api        HTTP 服务
cmd/migrate    goose 迁移
cmd/admin      邀请码等运营命令
internal/identity  账号/会话/邀请（共享内核）
internal/ledger    记账/目标/实现表（首期模块）
internal/httpserver 路由装配、鉴权、日志、恢复
internal/platform   config/db/httpx/apperr/id/migrate
migrations/identity identity 表
migrations/ledger   ledger 表
migrations/shared   跨模块索引等共享迁移
```

后续核心业务按 `internal/<module>/{domain,app,store,http}` 扩展；模块间不允许直接读写对方表或跨模块 JOIN。MySQL 中表按模块划分所有权，后续模块使用模块前缀。

## 当前限制

- 首期只实现 `identity` + `ledger`；购物编排、商品目录、平替/核算等尚未迁入。
- 未实现邮件/短信找回密码、CSV 导入、转账、多币种、共享账本。
- 进程内没有入口 IP 限流；登录/注册的入口限流应放在 BFF/反向代理，Go 侧已实现 Argon2 并发闸门与账户阶梯锁定。
- 幂等采用同事务占位行：并发同 key 会等待首个请求提交后重放；`IDEMPOTENCY_IN_PROGRESS` 只在异常遗留 pending 行时出现，不是常规路径。
- 未实现账号注销的“最近重认证”限制；账号注销需要当前密码。
- 标签目前只存取，不做筛选索引。
- MySQL 没有部分索引，软删除行仍占索引空间；后续按数据量加归档/清理。
- 目标实现表读时计算，不冻结已结束月份；补记历史流水会重算过去月份。

## 开发检查

```bash
go test ./...
go vet ./...
go build ./...
```

清理过期会话/幂等记录（可由 cron 调用）：

```bash
go run ./cmd/admin cleanup -session-retention 720h
```

## 安全说明

- `.env` 不提交；生产使用独立 `biz_migrator` 账号执行迁移，应用账号 `biz_app` 只有 DML 权限。
- Go 服务不要直接暴露公网；由 Next.js BFF 代理并持有 HttpOnly Cookie。
- 生产 MySQL 需开启备份/binlog，并单独演练恢复；现有 PostgreSQL 备份不覆盖本服务。
