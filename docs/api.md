# Peeling Machine — GUI API Contract (Phase 1)

The Go backend exposes this contract to the React GUI. It is stable for the GUI phase;
changes require updating this file + a capture-store schema bump.

## Transport

- Base URL: `http://localhost:9090` (configurable via `--api-addr`).
- Content type: `application/json; charset=utf-8` unless noted.
- CORS: any `http(s)://localhost:*` / `127.0.0.1:*` origin is accepted.

## Data model

### `Exchange`

```jsonc
{
  "id": 42,                         // monotonically increasing, unique per process
  "started_at": "2026-04-22T18:03:12Z",
  "duration_ms": 184,
  "scheme": "https",                // "http" | "https"
  "method": "GET",
  "host": "example.com:443",
  "path": "/api/users?page=2",
  "req_headers": [["Accept","*/*"], ["User-Agent","curl/8"]],
  "req_body": "base64-or-utf8-bytes",  // JSON-encoded []byte: base64 by default
  "req_truncated": false,
  "status": 200,
  "resp_headers": [["Content-Type","application/json"]],
  "resp_body": "...",
  "resp_truncated": false,
  "error": ""                       // non-empty on upstream failure
}
```

- Headers are ordered `[name, value]` pairs; duplicates preserved.
- Bodies are byte-capped (default 1 MiB). `*_truncated=true` when the cap was hit.
- `error` is present when the upstream round trip failed; `status` will be 0 in that case.

## Endpoints

### `GET /api/exchanges?limit=100`

Newest-first listing of captured exchanges. `limit` default 100.

```jsonc
{ "exchanges": [ /* Exchange, newest first */ ] }
```

### `GET /api/exchanges/{id}`

Full detail for one exchange. `404` if evicted from the ring.

### `DELETE /api/exchanges`

Clears the in-memory buffer. Responds `204 No Content`.

### `GET /api/stream`

Server-Sent Events stream of newly finalized exchanges.

```
event: exchange
data: { ...Exchange JSON... }

```

Reconnect strategy: client decides. Events produced before the client
connected are **not** replayed; use `/api/exchanges` to backfill.

### `GET /api/ca`

Downloads the root CA PEM so the user can install it into their OS/browser
trust store.

Headers:

- `Content-Type: application/x-pem-file`
- `Content-Disposition: attachment; filename="peeling-machine-ca.crt"`

### `GET /api/health`

Liveness probe. Returns `200 OK` with body `ok`.

## Out of scope (future phases)

- WebSocket (SSE is sufficient for unidirectional event fan-out).
- Persistence / replay of historical events past the ring buffer.
- Request editing / breakpoints / replay.
- Upstream proxy chaining — planned via a swap of `internal/transport.RoundTripper`.
