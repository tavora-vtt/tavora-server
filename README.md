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

## Worlds and joining

| Route | Who | Does |
| --- | --- | --- |
| `POST /api/worlds` | Any signed-in user | Create a world, the creator becomes its game master |
| `GET /api/worlds` | Any signed-in user | The worlds you are a member of, with your role |
| `GET /api/worlds/{id}/members` | Members | Who is in the world |
| `PUT /api/worlds/{id}/members/{userId}` | Game master | Change a role |
| `DELETE /api/worlds/{id}/members/{userId}` | Game master | Remove a member |
| `POST /api/worlds/{id}/invites` | Game master | Create an invite link |
| `GET /api/worlds/{id}/invites` | Game master | List invites and their use counts |
| `DELETE /api/worlds/{id}/invites/{inviteId}` | Game master | Revoke an invite |
| `GET /api/invites/{token}` | Anyone | Preview: world title, role, whether it is still valid |
| `POST /api/invites/{token}/accept` | Anyone | Join, creating an account if needed |

Invite tokens are stored hashed, like session tokens, and the plaintext is returned exactly
once to the game master who created it.

**A password is optional when accepting an invite.** Doc 10 puts it plainly: most players
should never have a password on this server. Accepting creates the account, issues the
session cookie, and that cookie is the credential. An account with no password cannot sign
in through the login route at all, which the service already enforced before this feature
existed.

Accepting is one transaction: the account, the membership and the use count either all
land or none do. A test drives a single-use invite twice and asserts that the second
attempt leaves no orphan account behind.

A game master cannot demote or remove themselves, because a world with no game master
cannot be repaired through the API.

Slugs are derived from the title when none is given, and a derived slug that collides gets
a numeric suffix rather than an error. A slug the user typed and that is taken is a 409,
because they chose it.

## Characters

`GET/POST /api/worlds/{id}/actors` lists and creates character sheets, and
`PUT .../actors/{id}/access` sets who may read or edit one. A new sheet belongs to the
game master who created it and is `limited` to everyone else, so players see a name and
nothing more until they are given the sheet.

Editing goes through the ordinary `document.patch` intent, which means sheets inherit the
whole path for free: authorization, the event log, per-recipient redaction and live
fan-out. `canEdit` on the listing is the resolved grant, not a guess, and the server
refuses a patch from someone who only has read access regardless of what the client shows.

## Walls and line of sight

Walls belong to a scene and carry `blocksSight`, `blocksMovement`, `blocksSound` and a door
flag. `internal/core/vision` is the geometry: segment intersection, no I/O, seven tests.

**A token a player cannot see is not sent to that player.** When a token moves, fan-out
resolves each recipient's viewpoints, meaning the tokens they own on that scene, and drops
the recipient entirely if no viewpoint has a clear line to the target. The client cannot
reveal what it was never told, which is the property
[concept doc 04](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/04-realtime-protocol.md)
asks for and the one a client-side fog of war cannot provide.

Three rules keep it from being punitive. Staff always see everything. A scene with no
sight-blocking walls filters nothing. And a viewer who owns no token on the scene is not
blinded, because they have no viewpoint to reason from; they watch like an observer. Each
has a test, as does an open door letting sight through.

This is the conservative server-side check doc 04 describes, not the client's rendering.
The pretty per-pixel fog stays a client concern and is still to come.

The same filter runs on the REST token listing, not only on fan-out. Leaving it on one path
would have meant the socket hid a token that a plain `GET` handed over.

`scene.door.toggle` flips a door and then pushes each member the set of tokens they can now
see, so opening a door reveals what was behind it without anyone reloading. Closing it takes
them away again.

## Combat

`combat.start` gathers the tokens on a scene, rolls initiative for each and stores the
order; `combat.next` advances the turn and wraps into the next round; `combat.end` clears
it. All three are staff only and all three fan out, so nobody has to be told whose turn it
is.

The engine owns the tracker, the round and turn counters and the order. What initiative
*means* is a per-combat `formula` that defaults to `1d20` and is the seam a game system
will fill, per [concept doc 07](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/07-game-systems.md).
Systems whose initiative is not rolled will declare a manual ordering there.

The document write and its event share one transaction, so a turn that was announced is a
turn that was committed.

## Chat and dice

`chat.post` carries what a person typed. `chat.roll` carries an expression, and the server
resolves it: parse, roll from `crypto/rand`, and record every die including the ones a
`kh`/`dl` modifier dropped, so a chat card shows the roll instead of asserting a number.
A client never generates a die face that matters.

The dice engine lives in `internal/core/dice` and supports counts, faces, flat modifiers
and keep/drop selection, with a budget of 200 dice per expression. It is deliberately the
**only** implementation: the client displays results and never rolls, so there is no second
source of truth to diverge. [ADR 0001](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0001-server-language.md)
plans to replace it with the shared TypeScript engine running in the script runtime, and
until that lands this Go implementation stands alone rather than beside one.

Generated messages travel as descriptors, not sentences: a roll produces
`{key: "core.chat.rolled", params: {...}}` and the client renders it in the reader's
language, per [concept doc 08](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/08-i18n-and-accessibility.md).
Free text a person typed stays text and is never translated. A test asserts that a
generated message carries no pre-rendered text.

A staff-only roll is an audience-restricted event: it is not sent to player sockets at all.
A player who asks for a staff audience is silently downgraded to public rather than
refused, and there is a test for each half.

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

Catch-up also runs the line-of-sight check, which it did not at first. Redaction alone was
not enough: a player reconnecting replayed every token that had moved, including the ones
behind a wall, because sight was filtered on live fan-out only. Both paths now ask the same
question, and a test connects a player after the move to prove it.

The wire format is Protobuf, generated from `tavora-protocol`. `/ws?format=json` switches
the same endpoint to a readable JSON encoding of the same frames, for reading traffic in
devtools; it is off unless `TAVORA_PROTOCOL_JSON=1`.

That JSON is deliberately not canonical Protobuf JSON: `protojson` base64-encodes `bytes`
fields, and payloads are `bytes`, so the readable mode would render exactly the part you
want to read as base64. Both encodings sit behind one `Codec` interface over one internal
frame model, and a contract test asserts they decode to the same frame for every frame
type. A cursor frame is 62 bytes as Protobuf and 147 as JSON.

## Assets

`POST /api/worlds/{worldId}/assets` takes one multipart file and gives back an asset. What
it does with the file is the point: the format is decided by magic bytes, the image is
decoded and encoded again from pixels, and the SHA-256 of the re-encoded bytes becomes the
storage key. So the filename, the declared content type and every metadata segment take
part in no decision and reach no output. An SVG named `portrait.png` is refused with 415,
a JPEG's comment segment does not survive, and the same map uploaded twice is stored once.

Binary content lives behind a `Blobs` port, on local disk by default under
`TAVORA_BLOB_ROOT`. Keys are hashes, so no user-controlled path component ever reaches the
filesystem, and the disk adapter refuses anything that is not one. The database stores
metadata only, plus a `variants` object recording the thumbnail produced in the same pass.

Serving happens on `/assets/{worldId}/{assetId}`, which requires world membership and sets
`nosniff`, a `default-src 'none'; sandbox` policy and a same-site resource policy, so an
upload that somehow carries markup cannot run as a document. `TAVORA_WORLD_QUOTA_BYTES`
caps what one world may hold.

Re-encoding is to PNG and JPEG, not yet to KTX2 with Basis. The reasoning, and what that
costs, is in
[ADR 0009](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0009-asset-pipeline-first-cut.md).

## Status

M0 is complete and M1 is under way. HTTP surface, graceful shutdown, the storage port with
both backends, the WebSocket gateway with lanes, backpressure and reconnect catch-up,
authentication, world and membership management with invite links, and the permission model
with field-level redaction are in place. So is the scene model: scenes, tokens, walls,
server-side line of sight, doors that reveal and hide what is behind them, combat order,
and the asset pipeline that puts real maps and portraits on the table.

## Licence

AGPL-3.0-or-later. The SDK that packages compile against is Apache-2.0 and lives in
[tavora-sdk](https://github.com/tavora-vtt/tavora-sdk). See
[ADR 0006](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0006-licensing.md).
