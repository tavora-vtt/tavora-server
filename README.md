# tavora-server

The Tavora VTT engine. A single Go binary that serves the API, the WebSocket gateway, the
asset pipeline and, in a release build, the embedded web client.

Design: [concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md).

## Run it

```
go run ./cmd/tavora
curl localhost:30000/healthz
curl localhost:30000/readyz
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `TAVORA_SERVER_BIND` | `0.0.0.0:30000` | Listen address |
| `TAVORA_STORAGE_DRIVER` | `sqlite` | `sqlite` or `postgres` |
| `TAVORA_STORAGE_DSN` | `data/tavora.db` | File path, or a PostgreSQL connection string |

The default install has no external dependency: SQLite through a cgo-free driver, so the
static binary from [ADR 0001](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0001-server-language.md)
survives.

## Storage

One `Store` port, two backends, one conformance suite. Any behavioural difference between
SQLite and PostgreSQL is an adapter bug, never a documented backend characteristic. See
[ADR 0002](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0002-storage-engine.md).

| Package | Role |
| --- | --- |
| `internal/storage` | The port: documents, events, patches, JSON queries |
| `internal/storage/sqlstore` | The shared SQL implementation, parameterised by a dialect |
| `internal/storage/sqlite` | SQLite dialect and schema |
| `internal/storage/postgres` | PostgreSQL dialect and schema |
| `internal/storage/storetest` | The conformance suite, run by both backends |

Engine data lives in relational columns, system data in a JSON column, both in the same
transaction. The JSON query surface is deliberately narrow, because anything richer is a
sign the field wants a declared index.

### Running the suite

SQLite needs nothing. PostgreSQL is skipped unless a DSN is present:

```
docker run --rm -d --name tavora-pg -e POSTGRES_PASSWORD=tavora -e POSTGRES_DB=tavora \
  -p 55433:5432 postgres:17-alpine

TAVORA_TEST_POSTGRES_DSN='postgres://postgres:tavora@127.0.0.1:55433/tavora?sslmode=disable' \
  go test ./... -race
```

Each subtest gets its own PostgreSQL schema, so the suite is parallel-safe and leaves
nothing behind.

Setting `TAVORA_TEST_REQUIRE_POSTGRES=1` turns a missing DSN into a failure rather than a
skip. CI sets it, so a broken service container cannot quietly halve the coverage that
ADR 0002 depends on.

## Status

Milestone M0. HTTP surface, graceful shutdown, and the storage port with both backends are
in place. The WebSocket gateway and the world hub land next.

## Licence

AGPL-3.0-or-later. The SDK that packages compile against is Apache-2.0 and lives in
[tavora-sdk](https://github.com/tavora-vtt/tavora-sdk). See
[ADR 0006](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0006-licensing.md).
