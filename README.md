# tavora-server

The Tavora VTT engine. A single Go binary that serves the API, the WebSocket gateway, the
asset pipeline and, in a release build, the embedded web client.

Design: [concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md).

## Run it

```
go run ./cmd/tavora
curl localhost:30000/healthz
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `TAVORA_SERVER_BIND` | `0.0.0.0:30000` | Listen address |

## Status

Milestone M0. The HTTP surface and graceful shutdown are in place. Storage, the WebSocket
gateway and the world hub land next. Structure and the dependency rule are described in
[concept doc 02](https://github.com/tavora-vtt/tavora-docs/blob/main/concept/02-architecture.md):
`transport` depends on `core`, `core` depends only on ports it declares, and adapters
depend on those ports.

## Licence

AGPL-3.0-or-later. The SDK that packages compile against is Apache-2.0 and lives in
[tavora-sdk](https://github.com/tavora-vtt/tavora-sdk). See
[ADR 0006](https://github.com/tavora-vtt/tavora-docs/blob/main/adr/0006-licensing.md).
