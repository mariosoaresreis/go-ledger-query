# go-ledger-query

The **read side** of a CQRS ledger system.  
It consumes domain events from Kafka, materialises read-model projections into PostgreSQL, and serves low-latency query responses over HTTP.

```
Kafka (ledger.transactions)
        │
        ▼
    Consumer          ← at-least-once, per-message retry + DLQ
        │
    Dispatcher        ← routes by EventType, OTEL spans
        │
 ProjectionStore      ← atomic upserts inside pgx transaction
        │
   PostgreSQL         ← balance_views · transaction_views · entry_views
        ▲
    QueryService      ← application layer, OTEL spans
        ▲
   HTTP Handler       ← Gin, Prometheus metrics, typed error → status code
```

---

## Architecture

The service is built on **Hexagonal Architecture** (Ports & Adapters):

| Layer | Package | Role |
|---|---|---|
| Domain | `internal/domain` | Value types, event contracts, typed errors — zero framework deps |
| Ports | `internal/ports` | Interfaces only: `QueryService`, `ReadRepository`, `Transactor`, `ProjectionStore`, `EventDispatcher` |
| Application | `internal/application` | Use-case logic, OTEL trace spans |
| Adapters | `internal/adapters/postgres` | `pgxpool`-backed repo, `RunInTx` for atomic projection writes |
| Adapters | `internal/adapters/kafka` | Consumer (retry + DLQ) + Dispatcher (`json.RawMessage` single-unmarshal) |
| Handler | `internal/handler` | Gin HTTP handler, 404 vs 500 via `errors.Is(domain.ErrNotFound)` |
| Observability | `internal/observability` | Prometheus custom registry, OTEL tracer setup |

---

## API

All endpoints are **read-only**.

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/accounts/:id/balance` | Current projected balance for an account |
| `GET` | `/v1/accounts/:id/entries?page=1&page_size=50` | Paginated ledger entries for an account |
| `GET` | `/v1/accounts/:id/transactions?page=1&page_size=50` | Paginated transactions (debit or credit) |
| `GET` | `/v1/transactions/:id` | Single transaction by ID |
| `GET` | `/health` | Liveness probe |
| `GET` | `/ready` | Readiness probe (pings Postgres) |
| `GET` | `/metrics` | Prometheus metrics (OpenMetrics format) |

### Response examples

```jsonc
// GET /v1/accounts/{uuid}/balance → 200
{
  "account_id": "a1b2c3d4-...",
  "owner_id":   "e5f6...",
  "currency":   "USD",
  "balance":    "1234.56"
}

// GET /v1/accounts/{uuid}/entries → 200
{
  "items": [
    {
      "id": "...", "account_id": "...", "transaction_id": "...",
      "type": "DEBIT", "amount": "100.00",
      "balance_before": "200.00", "balance_after": "100.00",
      "currency": "USD", "description": "payment", "created_at": "..."
    }
  ],
  "page": 1,
  "page_size": 50
}

// 404 — not found
{ "error": "balance a1b2c3...: not found" }

// 500 — infrastructure error
{ "error": "..." }
```

---

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | ✅ | — | PostgreSQL DSN (`postgres://user:pass@host:5432/db`) |
| `KAFKA_BROKER` | ✅ | — | Kafka broker address (`host:port`) |
| `PORT` | ❌ | `8081` | HTTP listen port |
| `GIN_MODE` | ❌ | `release` | Gin mode (`debug` / `release`) |
| `OTEL_TRACES` | ❌ | off | Set to `stdout` to enable trace export to stdout |

---

## Running Locally

### With Docker Compose (full CQRS stack)

```bash
# Start the query side + Kafka + Postgres (projection DB)
docker compose up query postgres-query kafka zookeeper

# Start everything including the command service
docker compose up --build
```

The query service listens on **http://localhost:8081**.

### Without Docker

```bash
export DATABASE_URL="postgres://ledger:ledger@localhost:5433/ledger_query"
export KAFKA_BROKER="localhost:9092"

# Apply migrations
psql $DATABASE_URL -f migrations/001_init.sql

# Run
go run ./cmd/api
```

### Build binary

```bash
make build
# �� bin/ledger-query
```

---

## Testing

### Unit tests (no Docker required)

```bash
go test ./internal/application/... ./internal/handler/... ./internal/adapters/kafka/... -v -race
```

Covers:

| Package | Tests |
|---|---|
| `application` | Service normalisation, error wrapping, delegation |
| `handler` | 200 / 400 / 404 / 500 HTTP status mapping |
| `adapters/kafka` | Dispatcher routing, transactional `onTxPosted`, `onTxReversed`, DLQ no-ops |

### Integration tests (Docker required — Testcontainers)

```bash
go test -tags=integration ./internal/adapters/postgres/... -v -timeout 120s
```

Spins up a `postgres:16-alpine` container, applies `migrations/001_init.sql`, and exercises the real SQL layer:

| Test | What it verifies |
|---|---|
| `TestUpsertAndGetBalance` | Insert + read round-trip, decimal precision |
| `TestUpsertBalance_UpdatesBalance` | `ON CONFLICT DO UPDATE` path |
| `TestGetBalance_NotFound` | Returns `domain.ErrNotFound` (not a raw string) |
| `TestUpsertAndGetTransaction` | Tx insert + read |
| `TestUpsertTransaction_UpdatesStatus` | `POSTED → REVERSED` via upsert |
| `TestGetTransaction_NotFound` | Typed not-found error |
| `TestUpsertEntryAndListEntries` | Entry insert + paginated query |
| `TestUpsertEntry_IdempotentDoNothing` | `ON CONFLICT DO NOTHING` idempotency |
| `TestListTransactions` | Debit/credit account filter + pagination |

### All unit tests (quick CI gate)

```bash
make test
```

---

## Kafka Topics

| Topic | Direction | Description |
|---|---|---|
| `ledger.transactions` | consumed | Domain events from the command service |
| `ledger.transactions.DLQ` | produced | Messages that failed after 5 consecutive retries |

### Consumer behaviour

- **At-least-once** delivery: offsets committed only after successful dispatch.
- **Retry policy**: up to 5 attempts per message with linear back-off (`200ms × attempt`).
- **Dead-letter queue**: after 5 failures the raw message is forwarded to `ledger.transactions.DLQ` with a `dlq-metadata` header (original partition, offset, error, timestamp), then the offset is committed so the partition makes progress.
- **Poison pills**: messages that fail JSON parsing are logged and skipped immediately (offset committed) — they never trigger retries.

### Supported event types

| `type` field | Handler |
|---|---|
| `account.created` | Seeds `balance_views` with zero balance |
| `transaction.posted` | Atomically upserts `transaction_views`, `entry_views`, and `balance_views` in a single DB transaction |
| `transaction.reversed` | Updates existing `transaction_views` status to `REVERSED` |
| `transaction.failed` | No-op (no projection) |
| anything else | Silently skipped (forward-compatible) |

---

## Observability

### Prometheus (`GET /metrics`)

| Metric | Type | Labels |
|---|---|---|
| `ledger_query_http_requests_total` | Counter | `method`, `path`, `status` |
| `ledger_query_http_request_duration_seconds` | Histogram | `method`, `path` |
| `ledger_query_kafka_events_processed_total` | Counter | `event_type` |
| `ledger_query_kafka_events_failed_total` | Counter | `event_type` |
| `ledger_query_kafka_events_dlq_total` | Counter | `event_type` |

Go runtime and process metrics are included automatically.

### OpenTelemetry tracing

Trace spans are emitted on every hot path:

- `application.GetBalance` / `ListEntries` / `GetTransaction` / `ListTransactions`
- `kafka.Dispatch` (with `event.type` and `event.aggregate_id` attributes)

The global tracer is a **no-op by default** (zero overhead). To enable stdout export during development:

```bash
OTEL_TRACES=stdout go run ./cmd/api
```

To wire a production OTLP exporter, replace the `stdouttrace` exporter in `internal/observability/tracing.go` with `otlptracegrpc`.

---

## Project Layout

```
go-ledger-query/
├── cmd/api/               # main — composition root, wires all layers
├── internal/
│   ├── domain/            # value types, event contracts, typed errors
│   ├── ports/             # interface definitions (hexagonal boundary)
│   ├── application/       # use-case service (OTEL spans)
│   ├── adapters/
│   │   ├── kafka/         # consumer (retry/DLQ) + dispatcher
│   │   └── postgres/      # DBTX repo, RunInTx, typed errors
│   ├── handler/           # Gin HTTP handler
│   ├── mocks/             # generated mocks (go.uber.org/mock)
│   └── observability/     # Prometheus + OTEL setup
├── migrations/
│   └── 001_init.sql       # idempotent schema (IF NOT EXISTS)
├── terraform/
│   ├── modules/           # vpc · eks · rds · msk
│   └── envs/prod/         # production environment
├── scripts/deploy.sh
├── docker-compose.yml
├── Dockerfile             # multi-stage, scratch final image
└── Makefile
```

---

## Infrastructure

Terraform modules under `terraform/` provision the production environment on AWS:

| Module | Resource |
|---|---|
| `vpc` | VPC, subnets, NAT gateway |
| `eks` | Kubernetes cluster (EKS) |
| `rds` | PostgreSQL (RDS) for projections |
| `msk` | Apache Kafka (MSK) |

```bash
# Plan
make tf-plan TF_ENV=prod IMAGE_TAG=abc1234

# Apply
make tf-apply TF_ENV=prod IMAGE_TAG=abc1234

# Deploy (build + push image + rolling restart)
make deploy TF_ENV=prod
```

---

## Tech Stack

| Concern | Library |
|---|---|
| HTTP | `github.com/gin-gonic/gin` |
| PostgreSQL | `github.com/jackc/pgx/v5` |
| Kafka | `github.com/segmentio/kafka-go` |
| Decimals | `github.com/shopspring/decimal` |
| Logging | `go.uber.org/zap` |
| Metrics | `github.com/prometheus/client_golang` |
| Tracing | `go.opentelemetry.io/otel` |
| Mocks | `go.uber.org/mock` |
| Integration tests | `github.com/testcontainers/testcontainers-go` |

