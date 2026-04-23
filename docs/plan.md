# Peeling Machine — Build Plan

> Living roadmap. Phase 1 (Go core + API) and Phase 2 (React GUI, embedded
> via `//go:embed`) are shipped. Phases 3a – 8 below are the prioritized
> future work. Keep `CLAUDE.md` as the source of truth for the shipped
> architecture and `docs/api.md` as the GUI contract; this file tracks
> intent and sequencing.
>
> **Priority tiers**
>
> | Tier | Meaning |
> | --- | --- |
> | **P0** | Ship next — small, high-value, unblocks trust in the tool |
> | **P1** | Core backbone, in the chosen feature order |
> | **P2** | Quality-of-life / power-user UI |
> | **P3** | Larger side-quests, schedule after P1+P2 |

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

## Phase 2 — React GUI — ✅ shipped

Built in a second session against the frozen `docs/api.md` contract:

- Live request list (SSE-driven) with host / method / status filtering.
- Detail pane with Request/Response tabs, headers table, and a body viewer
  that pretty-prints JSON and hex-dumps binary payloads.
- "Download CA" button hitting `/api/ca`, "Clear" hitting `DELETE /api/exchanges`.
- Dev: Vite + React + TS, pnpm, Vite dev server on `:5173` proxies `/api`
  to `:9090`.
- Prod: `web/embed.go` uses `//go:embed all:dist`; `internal/api` serves
  the SPA at non-`/api/*` paths with `index.html` fallback and strict
  404s on `/assets/*`. `go build` produces a single self-contained binary.

## Phase 3a — CA-download safety interstitial — ✅ shipped

The MITM CA is an attack primitive — installing it grants this process the
ability to decrypt TLS from anything that trusts it. The GUI currently ships
a plain `<a download>` link; that's too low-friction for the consequences.

**Scope**

- React modal on the `Download CA` button. Two required checkboxes before
  the real download fires:
  1. "I understand installing this CA lets Peeling Machine decrypt all
     TLS traffic from this machine."
  2. "I will NOT install this CA on devices I do not own, and will
     uninstall it when finished."
- Link to OS-specific uninstall steps (Keychain / Certificate Manager /
  Android user-trust store).
- New `## Security` section in `README.md` with the same warnings.
- CLI `peeling-machine ca export` prints a one-line `WARN:` banner to
  stderr before printing the cert path.
- `/api/ca` itself stays a raw PEM — no token — so scripted flows like
  `curl --cacert $(peeling-machine ca export) ...` keep working.

**Files**

- `web/src/components/Toolbar.tsx` — swap anchor for a button that opens a modal.
- `web/src/components/CaDownloadModal.tsx` *(new)* — the two-checkbox gate.
- `README.md` — new `## Security` section.
- `cmd/peeling-machine/main.go` — stderr banner in `runCA.export`.

## Phase 3b — Upstream proxy chaining — ✅ shipped

The `transport.RoundTripper` seam was built for this since Phase 1.
`internal/transport/transport.go` only exposes `Direct()`; this phase adds
`HTTP` and `SOCKS5` implementations, plus CLI flags to pick one.

**Scope**

- `transport.HTTP(upstreamURL string) RoundTripper` — `http.Transport`
  with `Proxy: http.ProxyURL(u)`. Supports `http://` and `https://` upstream
  proxies, including basic auth in the URL.
- `transport.SOCKS5(addr string, auth *proxy.Auth) RoundTripper` — uses
  `golang.org/x/net/proxy` to build the dialer, wired into
  `http.Transport.DialContext`.
- CLI flags: `--upstream-http=http://user:pass@host:port` and
  `--upstream-socks5=host:port`. Mutually exclusive; `--upstream-http`
  wins with a warning if both are set.
- `internal/proxy` is untouched — it already holds only a `RoundTripper`.
- Tests: two `httptest` servers (origin + HTTP upstream); assert the
  request arrives at the upstream with the full target URL.

**Files**

- `internal/transport/transport.go` — add `HTTP()`, `SOCKS5()`.
- `internal/transport/transport_test.go` *(new)*.
- `cmd/peeling-machine/main.go`, `internal/config/config.go` — new flags.
- `go.mod` — adds `golang.org/x/net/proxy`.

## Phase 3c — Custom proxy rules via JSON config — ✅ shipped

Lets users define upstream chains per-host without recompiling. Plugin
binaries are explicitly deferred — Go plugins are platform-fragile and
JSON rules cover ~90% of the use case.

**Scope**

- `--proxies-config=<path>` loading a JSON file of the form:
  ```json
  {
    "rules": [
      { "match": { "host": "*.corp.internal" }, "via": "http://corp-proxy:8080" },
      { "match": { "host": "*" }, "via": "socks5://127.0.0.1:1080" }
    ]
  }
  ```
- `transport.Rules(ruleset)` — a `RoundTripper` that dispatches per request
  based on host glob. Falls through to direct on no match.
- File watcher (poll-based is fine for v1; `fsnotify` optional) so edits
  take effect without restart.
- Plugin (Go `.so`) support is deferred as a future item.

**Files**

- `internal/transport/rules.go` *(new)* + tests.
- `cmd/peeling-machine/main.go`, `internal/config/config.go` — `--proxies-config` flag.
- `docs/proxies.md` *(new)* — config schema.

## Phase 4 — Persistent capture store — [P1, 2 sessions]

Moves captures from the in-memory ring to SQLite using `modernc.org/sqlite`
(no CGO — keeps the "single binary" invariant). Required foundation for
Phase 6 replay, which needs history that survives restarts.

**Scope**

- Extract a `Store` interface in `internal/capture`. Keep the current
  in-memory impl as `RingStore`; add `SQLiteStore`.
- `--store={ring|sqlite}` flag (default `ring` — Phase 1/2 tests untouched).
- `--store-path ~/.peeling-machine/captures.db` when `--store=sqlite`.
- Schema:
  `exchanges(id, started_at, duration_ms, scheme, method, host, path, status, error, req_headers_json, resp_headers_json, req_body, resp_body, req_truncated, resp_truncated)`.
- API: `docs/api.md` stays stable. `GET /api/exchanges` gains an optional
  `before_id=N` cursor for scrolling large histories — additive, no break.
- No migration between ring and SQLite (ring is ephemeral by definition).

**Files**

- `internal/capture/store.go` — extract interface.
- `internal/capture/sqlite.go` *(new)* + tests.
- `internal/api/server.go` — accept `before_id` param.
- `go.mod` — adds `modernc.org/sqlite`.
- `docs/api.md` — document the cursor param.

## Phase 5 — Advanced filters + alternate views — [P2, 1–2 sessions]

Pure GUI work. Phases 4's persistent history makes richer exploration
worth building.

**Scope**

- Richer filter bar: method, status-range chips (2xx/3xx/4xx/5xx plus
  exact), host glob, path contains, time window, "has body", "errored
  only". State URL-synced so a filter is a shareable link.
- View switcher in the toolbar:
  - **List** — the current view.
  - **Timeline / waterfall** — horizontal lanes per host (or method), bars
    scaled by `duration_ms`, aligned on `started_at`.
  - **Domain-grouped** — collapsible tree `host → path → exchanges`, with
    count and error-badge rollups per group.
- All views share one filtered dataset via a single hook; switching views
  is instant.

**Files**

- `web/src/views/` *(new)* — `ListView.tsx`, `TimelineView.tsx`, `DomainGroupedView.tsx`.
- `web/src/useFilteredExchanges.ts` *(new)* — pulls filter + view logic out of `App.tsx`.
- `web/src/components/FilterBar.tsx` *(new)* — replaces inline filter inputs in `Toolbar.tsx`.

## Phase 6 — Replay / Editor / Breakpoints — [P1, 2–3 sessions]

The marquee debugging feature. Split backend + GUI like Phases 1 and 2.
Benefits from Phase 3a (safety), Phase 4 (persistence), and Phase 5 (views).

**Scope — backend**

- `POST /api/exchanges/{id}/replay` — re-issue the captured request through
  the current upstream transport; record a new `Exchange` linked via
  `replay_of uint64`.
- `POST /api/replay` — ad-hoc request synthesis with a full body
  (method, url, headers, body).
- Breakpoint API: `GET/POST /api/breakpoints` with match rules
  (host/path/method). On match, proxy pauses the request and fans out a
  `breakpoint` SSE event; GUI releases via
  `POST /api/breakpoints/{pause_id}/release`.
- `capture.Exchange` gains `replay_of uint64` and `paused bool`.

**Scope — GUI**

- Detail-pane "Replay" button (re-issue as-is).
- "Edit & Replay" modal: method, URL, headers table, body editor.
- Breakpoints panel with match rules; paused-request badge in the list.

**Files**

- `internal/proxy` — per-request pause/resume channel plumbing.
- `internal/api/replay.go`, `breakpoints.go` *(new)* + tests.
- `web/src/components/ReplayModal.tsx`, `BreakpointsPanel.tsx` *(new)*.
- `docs/api.md` — new endpoints + SSE event types.

## Phase 7 — Rewrite rules + throttling — [P1, 1–2 sessions]

A middleware layer inside `internal/proxy` that runs before forwarding.

**Scope**

- `--rewrites=<path>` flag pointing at a rules file; each entry is
  `(match, actions)`.
  - Match: method / host / path / header.
  - Actions: `set-header`, `remove-header`, `rewrite-url`, `delay=200ms`,
    `return-status=503`, `mock-body=…`.
- Hot-reload via the same watcher approach as Phase 3c.
- `GET/PUT /api/rewrites` for live editing from the GUI.
- GUI "Rules" tab with a table editor.

## Phase 8 — Mobile pairing web page — [P3, 1 session]

Scoped to a web page only (no companion native app) per decision on
2026-04-23.

**Scope**

- `GET /pair` (served by `internal/api`) renders a page with:
  - A QR encoding `{proxy: "<lan-ip>:8080", ca_sha256: "…", ca_url: "http://<lan-ip>:9090/api/ca"}`.
  - The host's detected LAN IP, with a picker if multi-homed.
  - Per-platform install instructions (iOS Settings flow, Android
    user-trust flow, etc.) rendered from a lookup table.
- QR generated server-side via `github.com/skip2/go-qrcode` (zero-dep).
- Server-rendered Go template is preferred over a client-side route to
  keep the pairing page reachable even when the SPA bundle is broken.

**Files**

- `internal/api/pair.go` *(new)*.
- Optional: `web/src/pages/Pair.tsx` if a client-side version is later preferred.

A native companion mobile app that consumes the QR and automates proxy +
CA install is **considered but deferred** — it's a full second product
and can be revisited once the server-side pairing experience is proven.

## Files created in Phase 1 (historical)

- `go.mod`, `cmd/peeling-machine/main.go`
- `internal/ca/{ca.go,ca_test.go}`
- `internal/proxy/{proxy.go,connect.go,proxy_test.go}`
- `internal/transport/transport.go`
- `internal/capture/{store.go,store_test.go}`
- `internal/api/{server.go,stream.go,server_test.go}`
- `internal/config/config.go`
- `docs/api.md` — the contract Phase 2 built against.
- Updated `CLAUDE.md` architecture section.

## Files created in Phase 2 (historical)

- `web/` — Vite + React + TS app (pnpm), components under `web/src/components`.
- `web/embed.go` — `//go:embed all:dist`, exposes `DistFS()`.
- `internal/api/server.go` — added SPA handler with index fallback.
- Updated `CLAUDE.md` and `README.md` to document the GUI workflow.
