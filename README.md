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
| `TAVORA_PROTOCOL_JSON` | unset | `1` allows `/ws?format=json`, a readable encoding for debugging |
| `TAVORA_SECURE_COOKIES` | unset | `1` forces the `Secure` flag when a proxy terminates TLS |

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

`document.patch` is the first real intent and runs the whole path from
[concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md):
authorize, apply, append the event, fan out. Authorization, mutation and projection all
happen inside one transaction, so what a recipient is shown is consistent with what was
committed.

## Authentication

A fresh install has no accounts and says so at startup. `POST /api/setup` creates the first
administrator and then closes permanently.

| Route | Does |
| --- | --- |
| `GET /api/setup` | Whether the install still needs its first account |
| `POST /api/setup` | Create the first administrator, once |
| `POST /api/auth/login` | Sign in, sets the session cookie |
| `POST /api/auth/logout` | Ends this session |
| `GET /api/auth/me` | The signed-in identity |
| `POST /api/session/ticket` | A short-lived single-use ticket for the WebSocket |

Passwords are hashed with Argon2id at the OWASP baseline of 19 MiB, two passes, which is
cheap enough for a Raspberry Pi and expensive enough to matter.

**Session tokens are stored hashed.** The database holds the SHA-256 of the token, never
the token, so a database leak does not hand over live sessions. There is a test that fails
if the raw token ever becomes a lookup key.

**Sign-in does not reveal whether an account exists.** An unknown username still burns a
full Argon2 verification against a decoy hash, and both cases return byte-identical
responses. Two tests hold that, one at the service and one at the HTTP layer.

Failed attempts are counted per username and per address, with a lockout that also applies
to the correct password once tripped. Changing a password ends every session of that user.

The ticket endpoint needs a signed-in user and a membership in the requested world, and it
stamps the role the world records. A server administrator who is not a member of a world
gets a 403.

## Permissions

`internal/core/perm` is the pure part: roles, ownership levels, field visibility, and one
resolution function with no I/O in it, which is what makes the matrix exhaustively
testable. `internal/core/access` is the part that reads inputs from storage and applies it.

Three layers resolve in order, most specific first: the world role, then per-document
ownership (explicit user entry, document default, folder default, world default), then
field visibility from the system schema.

Two properties are worth stating because they are easy to get wrong.

**Redaction removes paths rather than nulling them**, so absence is indistinguishable from
non-existence, and a player never learns that a field exists.

**Fan-out is per recipient, and the projection is precomputed.** The intent handler
resolves every member's view inside the transaction and hands the hub a map from user to
frame. The hub goroutine then does map lookups and never touches storage, which is what
keeps fan-out from blocking on I/O.

The role comes from `world_members`, never from the ticket. A ticket that claims `gm` for a
user the world records as a player connects as a player, and a ticket for a non-member is
refused. There is a test for each.

Catch-up is redacted the same way. Replaying raw event payloads would have leaked, because
the event log stores the patch that was requested rather than the view a recipient is
entitled to, so replay projects the current document instead.

The wire format is Protobuf, generated from `tavora-protocol`. `/ws?format=json` switches
the same endpoint to a readable JSON encoding of the same frames, for reading traffic in
devtools; it is off unless `TAVORA_PROTOCOL_JSON=1`.

That JSON is deliberately not canonical Protobuf JSON: `protojson` base64-encodes `bytes`
fields, and payloads are `bytes`, so the readable mode would render exactly the part you
want to read as base64. Both encodings sit behind one `Codec` interface over one internal
frame model, and a contract test asserts they decode to the same frame for every frame
type. A cursor frame is 62 bytes as Protobuf and 147 as JSON.

## Status

M0 is complete and M1 is under way. HTTP surface, graceful shutdown, the storage port with
both backends, the WebSocket gateway with lanes, backpressure and reconnect catch-up,
authentication, and the permission model with field-level redaction are in place. World and
membership management, then the scene model, land next.

## Licence

AGPL-3.0-or-later. The SDK that packages compile against is Apache-2.0 and lives in
[tavora-sdk](https://github.com/tavora-vtt/tavora-sdk). See
[ADR 0006](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0006-licensing.md).
