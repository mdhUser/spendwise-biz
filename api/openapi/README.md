# OpenAPI sources

`spendwise-biz/api/openapi/` is the contract source of truth for the Go API
(see target architecture §5.1). Contracts are split by module so ownership is
visible in review:

| File | Owner | Status |
| --- | --- | --- |
| `ledger.yaml` | ledger | existing M1 contract |
| `catalog.yaml` | catalog | M2 |
| `conversation.yaml` | conversation | M2 |
| `runs.yaml` | conversation/jobs | M3 |
| `spendwise.yaml` | umbrella (external `$ref`s) | M3 |

Auth lives in the separate `spendwise-auth` repository at
`api/openapi/auth.yaml`.

Rules:

- `/v1/*` is the service-internal path; public traffic is `/api/v1/*` after the
  ingress rewrite.
- JSON is camelCase. Auth keeps legacy snake_case fields during the M0-M6
  compatibility window.
- Money is integer fen (`*Fen`), never floating point.
- Create endpoints require `Idempotency-Key`; the OpenAPI documents it on each
  operation rather than as a global parameter.
- Generated Go/TS code is checked in later (M3+); until then these files are the
  reviewable contract and are validated by module handler tests.
