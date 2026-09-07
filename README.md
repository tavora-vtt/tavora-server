# tavora-server

The Tavora VTT engine. A single Go binary that serves the API, the WebSocket gateway, the
asset pipeline and, in a release build, the embedded web client.

Design: [concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md).

> Links to `tavora-docs` point at a repository that is currently private, so they resolve
> only for members of the organisation. The design rationale will open up with it.

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
| `TAVORA_DEV_UNSAFE_TICKETS` | unset | `1` opens an unauthenticated ticket endpoint. Development only |
| `TAVORA_PROTOCOL_JSON` | unset | `1` allows `/ws?format=json`, a readable encoding for debugging |

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

## Realtime gateway

`internal/transport/ws` implements the protocol from
[concept doc 04](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/04-realtime-protocol.md):
one socket, three lanes, per-world hub.

| Lane | Carries | Under pressure |
| --- | --- | --- |
| Control | Handshake, errors, ping, resync | Never dropped |
| Document | Intents, acks, events | Queued, then the session is closed with a resync instruction |
| Ephemeral | Cursors, drag previews | Coalesced by key, newest wins |

A client connects by exchanging a short-lived single-use ticket over HTTP, then sends
`hello` with the sequence it last saw. The session joins the hub **before** the welcome is
written, so nothing published in between is lost; the replay is prepended ahead of anything
that arrived during catch-up, and the writer drops any event whose sequence it has already
sent.

The ticket endpoint is closed by default and returns 501. `TAVORA_DEV_UNSAFE_TICKETS=1`
opens it and logs a warning at startup, because until authentication lands in M1 it would
hand a session to anyone who asks.

`document.patch` is the first real intent and runs the whole path from
[concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md):
authorize, apply, append the event, fan out. Authorization currently goes through
`AllowAllAuthorizer`, which is deliberately named so it is greppable and cannot be mistaken
for a permission model. The real one arrives with M1.

The wire format is Protobuf, generated from `tavora-protocol`. `/ws?format=json` switches
the same endpoint to a readable JSON encoding of the same frames, for reading traffic in
devtools; it is off unless `TAVORA_PROTOCOL_JSON=1`.

That JSON is deliberately not canonical Protobuf JSON: `protojson` base64-encodes `bytes`
fields, and payloads are `bytes`, so the readable mode would render exactly the part you
want to read as base64. Both encodings sit behind one `Codec` interface over one internal
frame model, and a contract test asserts they decode to the same frame for every frame
type. A cursor frame is 62 bytes as Protobuf and 147 as JSON.

## Status

Milestone M0. HTTP surface, graceful shutdown, the storage port with both backends, and the
WebSocket gateway with lanes, backpressure and reconnect catch-up are in place. The scene
model and permissions land next.

## Licence

AGPL-3.0-or-later. The SDK that packages compile against is Apache-2.0 and lives in
[tavora-sdk](https://github.com/tavora-vtt/tavora-sdk). See
[ADR 0006](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0006-licensing.md).
