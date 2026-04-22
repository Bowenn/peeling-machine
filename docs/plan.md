# Peeling Machine — Build Plan

> Snapshot of the plan drafted at session start. Phase 1 is now implemented;
> Phases 2–3+ remain as written. Keep this file as a historical reference of
> intent — the shipped architecture is documented in `/CLAUDE.md`, and the
> GUI-facing contract in `docs/api.md`.

## Context

Peeling Machine is a Charles-like local HTTP/HTTPS debugging proxy. It runs as a MITM proxy with its own CA, intercepts `CONNECT` tunnels, decrypts TLS, logs traffic, and exposes it to a UI. The repo was empty at the start of planning (only `README.md`, `LICENSE`, `.gitignore` for Go).

**Stack decisions (confirmed with user):**
- Go backend (`net/http` + `crypto/tls`, no MITM library)
- React SPA in the browser, talking to the Go server over REST + SSE on `localhost`
- Future phases will support chaining to upstream proxies (incl. self-built), so the upstream transport must be a swappable interface from day one

**Session strategy:** Split. This plan covers **Phase 1 (Go core + HTTP API)** in the current session. A separate session will build the React GUI against the stable API contract frozen at the end of this session. The UI is not implemented here — only its contract.

## Why split sessions

- The proxy core (CA, CONNECT, TLS, capture) is complex and self-contained; context stays focused.
- A fresh session for the GUI starts from a stable OpenAPI-style contract instead of shifting ground.
- Backend can be driven by `curl` / `httpie` end-to-end before any UI exists, which catches contract bugs early.

## Phase 1 — Go Core (this session) — ✅ shipped

### Proposed layout

```
peeling-machine/
├── cmd/peeling-machine/main.go     # CLI entrypoint: flags, wires everything
├── internal/
│   ├── ca/                         # root CA generation + on-demand leaf certs
│   ├── proxy/                      # HTTP/HTTPS proxy, CONNECT handler
│   ├── transport/                  # pluggable upstream transport (direct today, chained proxy later)
│   ├── capture/                    # in-memory ring buffer of Exchange records
│   ├── api/                        # REST + SSE HTTP server for the GUI
│   └── config/                     # paths, ports, flags
├── go.mod
└── CLAUDE.md                       # already exists; update as architecture solidifies
```

### Milestones (in build order)

**M1 — Root CA + leaf cert factory** (`internal/ca`)
- `LoadOrCreate(dir string) (*CA, error)` — reads `~/.peeling-machine/ca.{crt,key}`; generates a new 10-year RSA-2048 CA if missing.
- `LeafFor(host string) (*tls.Certificate, error)` — signs a short-lived leaf with SAN for `host`; memoize in an LRU to avoid re-signing each connection.
- CLI subcommand: `peeling-machine ca export` prints the CA cert path so the user can install it into OS / browser trust stores.

**M2 — HTTP(S) proxy server** (`internal/proxy`)
- One `http.Server` listening on `:8080` (configurable).
- `ServeHTTP`: if method is `CONNECT`, hijack the connection, send `200 OK`, then perform a TLS handshake as server using `ca.LeafFor(host)` with a `GetCertificate` callback so SNI drives cert selection.
- Wrap the now-decrypted `net.Conn` in a new `http.Server` (or manual `http.ReadRequest` loop) to parse the inner plaintext requests.
- For plain HTTP (no CONNECT) serve directly.
- Forward via `transport.RoundTripper`; capture request + response via `capture.Store` before returning.

**M3 — Pluggable upstream transport** (`internal/transport`)
- Interface `RoundTripper` (matches `http.RoundTripper` signature) — today backed by `&http.Transport{}` going direct to origin.
- Future chained-proxy support slots in by swapping the implementation; nothing else changes.

**M4 — Capture store** (`internal/capture`)
- `Exchange` struct: id, started_at, duration, scheme, method, host, path, req headers, req body (byte cap), resp status, resp headers, resp body (byte cap), error.
- `Store`: ring buffer of N (default 1000) exchanges + `Subscribe() <-chan Exchange` for live streaming. In-memory only in Phase 1; persistence is Phase 3.
- Bodies captured up to a size cap (e.g. 1 MiB) with a `truncated` flag — matches what the UI will need.

**M5 — HTTP API for the GUI** (`internal/api`)
- Served on a separate port (`:9090`) so the proxy port only speaks proxy protocol.
- `GET /api/exchanges` — list recent (paged).
- `GET /api/exchanges/{id}` — full detail incl. bodies.
- `GET /api/stream` — Server-Sent Events: one event per new `Exchange`.
- `DELETE /api/exchanges` — clear buffer.
- `GET /api/ca` — download root CA cert (convenience for the Trust step).
- CORS: allow `http://localhost:*` so the React dev server works.
- Freeze this shape in a short `docs/api.md` — the GUI session will read it.

**M6 — CLI wrapper** (`cmd/peeling-machine/main.go`)
- Flags: `--proxy-addr`, `--api-addr`, `--ca-dir`, `--buffer-size`.
- Subcommands: `run` (default), `ca export`.
- Graceful shutdown on `SIGINT`.

### Dependencies to add

- Standard library covers everything listed above.
- `github.com/hashicorp/golang-lru/v2` for the leaf cert cache (optional — a simple `sync.Map` is fine for v1).
- `github.com/stretchr/testify` only if we need richer assertions; otherwise stdlib `testing` suffices.

### Verification (end of Phase 1)

1. `go build ./...` and `go test ./...` green.
2. `peeling-machine ca export` prints the CA path; import it into Keychain (macOS) and trust for SSL.
3. Run `peeling-machine run`, then:
   - `curl -x http://localhost:8080 http://example.com` — plain HTTP flows through and appears in capture.
   - `curl -x http://localhost:8080 https://example.com` — HTTPS MITM succeeds after CA trust.
4. `curl http://localhost:9090/api/exchanges` returns both captures above.
5. `curl -N http://localhost:9090/api/stream` streams new events as more traffic flows.
6. Unit tests: CA round-trip (generate → load → sign leaf → verify chain), CONNECT handler using two in-process `httptest` servers, capture-store ring-buffer wrap and subscribe delivery.

### Explicit non-goals for Phase 1

- No React code, no Wails, no desktop packaging.
- No persistence (SQLite/BoltDB) — in-memory only.
- No upstream proxy chaining (interface is there; only direct impl).
- No request editor, breakpoints, or replay.
- No WebSocket — SSE is enough for a one-way event stream.

## Phase 2 — React GUI (separate future session)

Planned scope, captured here so Phase 1's API covers it:
- Live request list (SSE-driven), filterable by host/method/status.
- Detail pane: request/response headers + body with syntax highlighting.
- "Download CA" button that hits `/api/ca`.
- "Clear" button that hits `DELETE /api/exchanges`.
- Dev: Vite + React + TS; prod: served by the Go `api` package from an embedded `fs.FS`.

Bring `docs/api.md` from Phase 1 into the new session as the source of truth.

## Phase 3+ — Future (not in scope now)

- Pluggable upstream proxies (HTTP/SOCKS5, user-built) via `transport.RoundTripper`.
- Persistent capture store.
- Request replay / editor / breakpoints.
- Rewrite rules, throttling.

## Files to create in Phase 1

- `go.mod`, `cmd/peeling-machine/main.go`
- `internal/ca/{ca.go,ca_test.go}`
- `internal/proxy/{proxy.go,connect.go,proxy_test.go}`
- `internal/transport/transport.go`
- `internal/capture/{store.go,store_test.go}`
- `internal/api/{server.go,stream.go,server_test.go}`
- `internal/config/config.go`
- `docs/api.md` (the contract the next session will read)
- Update `CLAUDE.md` architecture section once package layout lands.
