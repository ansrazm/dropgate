# DropGate

A small Go API (PostgreSQL + MongoDB) for limited-quantity releases ("drops"). Buyers
**claim** units, the stock is **held** for a fixed time, and the hold is either
confirmed or released. The point of the project is correctness under
concurrency: no oversell, safe retries.

> Status: drops, claims, idempotency, concurrency tests, and a MongoDB event log.
> Confirm/release and the expiry worker are next.

## Run it

Requires Go 1.27+ and Docker.

```bash
docker compose up -d db mongo
go mod tidy                      # first time only, creates go.sum
MONGODB_URI=mongodb://localhost:27018 go run ./cmd/api   # migrations run on start
```

Config (environment variables): `DATABASE_URL`
(default `postgres://dropgate:dropgate@localhost:5434/dropgate?sslmode=disable`),
`PORT` (default `8080`), and optional `MONGODB_URI` / `MONGODB_DB` (default `dropgate`).
Without `MONGODB_URI` the API runs on PostgreSQL alone and the event log is off.

## Tests

The tests need a real Postgres (they check locking behaviour, so a mock would
prove nothing):

```bash
docker compose up -d db mongo
TEST_DATABASE_URL="postgres://dropgate:dropgate@localhost:5434/dropgate?sslmode=disable" \
TEST_MONGODB_URI="mongodb://localhost:27018" \
  go test -race -count=1 ./...
```

What they prove:

- 200 concurrent buyers vs. 10 units: exactly 10 succeed, the rest get `SOLD_OUT`.
- The same `Idempotency-Key` sent 8 times at once creates one claim and takes stock once.
- A rejected second claim from the same buyer leaves stock untouched (rollback).
- After every scenario: `remaining + units held or confirmed = total`.

## API

```bash
# create a drop (opens_at / closes_at are RFC 3339)
curl -XPOST localhost:8080/api/drops -d '{
  "title": "Limited sneaker drop", "total_qty": 10, "price_idr": 750000,
  "opens_at": "2026-11-03T00:00:00Z", "closes_at": "2026-11-03T12:00:00Z"
}'

# claim one unit (Idempotency-Key is required)
curl -XPOST localhost:8080/api/drops/<id>/claims \
  -H 'Idempotency-Key: 6f1c...' -d '{"buyer_ref": "ani@example.com", "qty": 1}'
```

| Method | Path | Notes |
|---|---|---|
| POST / GET | `/api/drops`, `/api/drops/{id}` | create, list, read remaining stock |
| POST | `/api/drops/{id}/claims` | 201 held, 200 replay, 409 `SOLD_OUT` / `ALREADY_CLAIMED`, 422 `DROP_NOT_OPEN` / `QTY_EXCEEDS_LIMIT` / `IDEMPOTENCY_KEY_REUSED` |
| GET | `/api/drops/{id}/events?limit=50` | newest events of a drop from MongoDB (503 `EVENT_LOG_DISABLED` if not configured) |
| GET | `/healthz` | liveness |

## Design notes

- **The guard is in SQL.** A claim runs `UPDATE drops SET remaining = remaining - $qty WHERE ... AND remaining >= $qty AND now() BETWEEN opens_at AND closes_at`.
  Postgres re-checks the condition under the row lock, so concurrent claims are serialized per drop and cannot oversell.
- **One transaction per claim.** The stock decrement and the claim INSERT commit or roll back together.
- **Idempotency** is a unique `(drop_id, idempotency_key)`. A replay returns the original claim (200); a key reused for a different request is rejected (422).
- **Trade-off:** all claims on one drop queue on one row lock. That is fine for the target scale and keeps correctness simple; sharding a drop's counter would be the next step if it ever mattered.
- **PostgreSQL is the source of truth, MongoDB is only a log.** Every claim outcome
  (`claim.created`, `claim.replayed`, `claim.rejected` with a reason) is written to an append-only
  `events` collection. Event shapes differ per type, the log needs no transaction with stock, and
  its write load stays off the claim row lock. A JSONB table in Postgres would also work; Mongo was a
  deliberate choice for the document model.
- **The log can never break a claim.** Events go through a bounded in-memory queue and are inserted in
  batches by one goroutine. If Mongo is slow or down, events are dropped and counted, not retried
  inside the request. Trade-off: the log is best-effort, so it is for audit and debugging, never for
  deciding stock. Old events expire automatically (TTL index, 30 days).
- The database clock (`now()`) decides whether a drop is open, never the app server's.

## Roadmap

- [x] Schema, migrations (embedded, run on start), drops, claims, idempotency, concurrency tests
- [x] MongoDB event log for claim outcomes (best-effort, batched, TTL)
- [ ] Confirm and release endpoints (and their events)
- [ ] Expiry worker (`FOR UPDATE SKIP LOCKED`) returning expired holds to the drop
- [ ] Confirm-vs-expiry race test
- [ ] Dockerfile, full `docker compose up`, GitHub Actions CI
- [ ] Small frontend (drop page, countdown, claim/confirm)
- [ ] Live deploy and a k6 load-test result in this README
