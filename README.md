# FX Settlement Microservice

A production-grade multi-currency settlement service built in Go — designed to support the kind of cross-border investing infrastructure Atomic runs for partners like Bolt, Aspire, and their European expansion.

## What it does

Handles the full lifecycle of a cross-currency money movement:

```
PENDING → PROCESSING → SETTLED
                    ↘ FAILED
```

For each settlement it records:
- Exact converted amounts (using `decimal` — never `float64` for money)
- The applied rate vs mid-market rate
- Spread cost (Atomic's FX revenue on the trade)
- Full audit trail with timestamps

## Supported currencies

EUR, USD, GBP, CHF, SEK, NOK, DKK, PLN, CZK, HUF — all major European markets.

## Running locally

**Without a database (in-memory — fastest to start):**
```bash
go run ./cmd/server
# Server starts on :8080
```

**With PostgreSQL:**
```bash
docker-compose up
```

**Run all tests:**
```bash
go test ./... -v
```

## API

### Create a settlement
```bash
curl -X POST http://localhost:8080/v1/settlements \
  -H "Content-Type: application/json" \
  -d '{
    "partner_id": "bolt",
    "user_id": "user-123",
    "amount": "1000.00",
    "source_currency": "EUR",
    "target_currency": "USD",
    "direction": "BUY"
  }'
```

Response:
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "partner_id": "bolt",
  "user_id": "user-123",
  "source_amount": "1000",
  "source_currency": "EUR",
  "target_amount": "1090.4250",
  "target_currency": "USD",
  "applied_rate": "1.090425",
  "mid_market_rate": "1.085000",
  "spread_cost": "5.4250",
  "status": "PENDING",
  "created_at": "2026-05-23T10:00:00Z"
}
```

### Process (settle) it
```bash
curl -X POST http://localhost:8080/v1/settlements/{id}/process
```

### Get a settlement
```bash
curl http://localhost:8080/v1/settlements/{id}
```

### List partner settlements
```bash
curl http://localhost:8080/v1/partners/bolt/settlements
```

### Health check (Kubernetes liveness probe)
```bash
curl http://localhost:8080/healthz
```

## Architecture

```
cmd/server/main.go          ← entry point, wires deps, graceful shutdown
internal/
  fx/
    converter.go            ← core FX math (rates, spreads, conversion)
    converter_test.go       ← unit tests including decimal precision test
  settlement/
    service.go              ← business logic, lifecycle state machine
    repository.go           ← MemoryRepository (tests) + PostgresRepository (prod)
    service_test.go         ← service tests with in-memory repo
  api/
    handlers.go             ← HTTP handlers
    handlers_test.go        ← integration tests using httptest
migrations/
  001_create_settlements.sql ← PostgreSQL schema
```

## Key design decisions (for interviews)

**Why `decimal` not `float64`?**
`0.1 + 0.2` in float64 = `0.30000000000000004`. For a 10,000 EUR trade that's a €0.000004 error per trade — small, but it's wrong, and regulators care. `shopspring/decimal` gives exact arithmetic.

**Why a Repository interface?**
The service never imports `database/sql` directly. It talks to a `Repository` interface, which means tests use a fast in-memory implementation with zero infrastructure required. Swap in the Postgres implementation at runtime via environment variable.

**Why graceful shutdown?**
Kubernetes sends SIGTERM before terminating a pod. Without graceful shutdown, in-flight settlement requests get killed mid-transaction. The 30-second window lets them complete before the process exits.

**Why named `Currency` type?**
```go
type Currency string
// This won't compile — source/target are in the wrong order:
converter.Convert("USD", "EUR", amount)  // ← compile error if args are Currency type
```
Named types make the compiler your first line of defense against argument-order bugs.
