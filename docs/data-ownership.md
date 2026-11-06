# SpendWise 数据所有权（M1）

本文件是 `spendwise-biz`（演进为 `spendwise-api`）的表所有权登记处，也是
`spendwise-target-architecture.md` §6.2 的落地清单。新增表必须先登记在这里，
再写迁移；CI/评审发现越界（例如 core 迁移改 `users`）应直接拒绝。

## 1. 数据库与账号

- 单 MySQL 8.4 实例，单 database `spendwise`，字符集 `utf8mb4`，排序
  `utf8mb4_0900_ai_ci`，InnoDB，`DATETIME(3)` 存 UTC。
- 账号按角色隔离（本地 compose 与生产一致）：

| 账号 | 权限 | 使用者 |
| --- | --- | --- |
| `auth_svc` / `spendwise_auth` | 仅 auth 表 DML | `spendwise-auth` |
| `core_svc` / `biz_app` | 仅 core 表 DML | `spendwise-api` / `spendwise-worker` |
| `spendwise_migrator` / `biz_migrator` | 迁移 Job 的 DDL + DML | `cmd/migrate` |
| 只读副本账号 | 只读 | 报表/后台，不服务用户请求 |

应用账号不得执行 DDL。`owner_id` 保存 `auth.users.id` 字符串，是值引用，
不是物理外键；biz 不读取 `users` / `sessions` / `refresh_tokens`。

## 2. 表所有权

### auth（`spendwise-auth` 独占，biz 只读 id 值）

| 表 | 说明 |
| --- | --- |
| `users` | 正式身份；`id` 是 core 的 `owner_id` |
| `identities` | 邮箱/手机/微信绑定 |
| `verification_challenges` | 验证码挑战 |
| `sessions` | 会话与旧 opaque access token 哈希 |
| `refresh_tokens` | 轮换 refresh token、重用检测 |
| `oauth_states` | 微信 OAuth state |
| `oauth_exchange_codes` | OAuth 一次性交换码 |

### ledger（`spendwise-api`）

| 表 | 迁移目录 |
| --- | --- |
| `accounts`、`categories`、`transactions`、`idempotency_records`、`audit_events`、`saving_goals`、`saving_goal_terms`、`saving_goal_term_categories` | `migrations/ledger/` |

### catalog（`spendwise-api`）

| 表 | 说明 |
| --- | --- |
| `catalog_user_categories` | 用户品类 |
| `catalog_user_products` | 用户 SPU / 清单商品 |
| `catalog_user_skus` | 用户 SKU，`attributes` JSON |
| `catalog_quote_snapshots` | 人工/平台报价快照，金额整数分，允许 `NULL` 表示未知 |
| `catalog_user_items` | 个人清单条目（可重建数据） |
| `catalog_products`、`catalog_offers`、`catalog_platform_listings` | 公共目录/平台映射（M2 预留，先不写） |

迁移目录：`migrations/catalog/`。

### conversation（`spendwise-api`）

| 表 | 说明 |
| --- | --- |
| `conv_conversations` | 会话；`owner_id` 隔离，30 天过期 |
| `conv_messages` | 消息；按会话归属，不跨模块 JOIN |
| `conv_search_runs` | 平替/推荐 run；`(owner_id, request_key)` 幂等唯一键 |
| `conv_substitution_assessments` | 平替评估结果 |
| `conv_field_evidence`、`conv_assessment_evidence` | 证据链（M3 预留） |
| `legacy_search_runs`、`legacy_search_evidence` | 旧链路兼容表（暂不迁匿名数据） |

迁移目录：`migrations/conversation/`。

### platform（`spendwise-api` / `spendwise-worker`）

| 表 | 说明 |
| --- | --- |
| `rate_buckets` | 进程间共享的限流窗口（MySQL 行锁，不使用 Redis） |
| `async_jobs` | `SKIP LOCKED` 任务队列：run、cleanup、export、delete |
| `feature_flags` | 按域切流开关（M3 预留） |
| `deletion_requests` | 注销请求与冷静期（M3/M4 预留） |

迁移目录：`migrations/platform/`。

## 3. 迁移流

- 一个 core goose 版本表：`goose_db_version`（biz/api/worker 共用）。
- 模块目录独立，但版本号在整个 core 流内全局递增：
  - `ledger`：`20261105...`
  - `catalog`：`20261106...`
  - `conversation`：`20261107...`
  - `platform`：`20261108...`
- `internal/platform/migrate.Modules` 是唯一执行顺序清单；`cmd/migrate` 与集成测试
  共用同一 runner，迁移期间持有 `GET_LOCK('spendwise_biz_migrations')`。
- 迁移文件必须带 `-- +goose Up`；破坏性变更单独发布并先快照。
- 不在模块 A 的迁移里改模块 B 的表。跨模块数据通过 `app.Service` 或 Job 协作，
  禁止直接 JOIN。

## 4. 模块边界（Go 包）

```text
internal/ledger     domain/app/store/http，拥有 ledger_* 表
internal/catalog    domain/app/store/http，拥有 catalog_* 表
internal/conversation domain/app/store/http，拥有 conv_* 表
internal/jobs       queue/worker，拥有 async_jobs（platform）
internal/identity   JWT/JWKS 验签（无数据库所有权）
internal/authclient 旧 opaque /me 回退（无数据库所有权）
internal/platform   config/db/httpx/apperr/id/migrate/ratelimit/clock
```

依赖方向：`http → app → domain`；`store` 实现 `app` 定义的端口；
`platform` 可被所有层引用。模块对外只暴露 `app.Service`/DTO，不泄漏
`sql.DB`、`sql.Tx` 或行模型。

## 5. 不变量

- 金额：`BIGINT` 整数分；未知报价为 `NULL`，绝不把运费当 0 或编造价格。
- 时间：DB `DATETIME(3)` UTC；业务期间 `Asia/Shanghai`；API RFC3339。
- ID：UUIDv7，`BINARY(16)`；对外字符串。
- 幂等：创建类 POST 必带 `Idempotency-Key`；重放 200 + `Idempotent-Replay: true`。
- 归属：所有 store 方法第一个参数是 `ownerID`，SQL 必须带 `owner_id = ?`。
- 匿名用户/会话/消息默认**不迁移**（目标架构 §6.2.1 ADR）。
