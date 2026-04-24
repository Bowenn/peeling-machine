# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Peeling Machine is a local HTTP/HTTPS debugging proxy. It MITM-intercepts TLS using its own generated root CA, records every request/response exchange, and exposes them to a UI over a REST + SSE API on a second port.

The codebase splits along two deliberately separated boundaries:

- **Go core** (this repo): proxy, CA, capture store, API. Lives on two localhost ports.
- **React GUI** (`web/`): Vite + React + TypeScript. Built artifacts in `web/dist` are embedded into the Go binary (`//go:embed` in `web/embed.go`) and served by `internal/api` at non-`/api/*` paths. Consumes the API contract in `docs/api.md`.

## Common Commands

```bash
# Build everything
go build ./...

# Run the full test suite
go test ./...

# Run a single package's tests
go test ./internal/proxy

# Run a single test by name
go test ./internal/proxy -run TestHTTPSConnectIntercept

# Run the proxy (defaults: proxy :8080, API :9090, CA under ~/.peeling-machine)
go run ./cmd/peeling-machine

# Print root CA cert path (for installing into OS/browser trust store)
go run ./cmd/peeling-machine ca export

# Vet
go vet ./...

# GUI: first-time install + build (produces web/dist that the Go binary embeds)
cd web && pnpm install && pnpm run build

# GUI dev loop (Vite on :5173 with /api proxied to the Go server on :9090)
cd web && pnpm run dev
```

The GUI uses pnpm; do not commit `package-lock.json` or `yarn.lock` (both are gitignored in `web/`).

## Architecture — the big picture

The proxy is one `http.Server` on `:8080`; HTTPS is handled by hijacking `CONNECT`:

1. `cmd/peeling-machine` wires together `ca → capture → transport → proxy → api` and runs two goroutines (proxy port + API port).
2. `internal/ca` — root CA is generated on first run under `~/.peeling-machine`; leaf certs are signed on demand (one ECDSA leaf key reused across all hosts) and cached in a `sync.Map`. The leaf key is re-generated per process — it never touches disk.
3. `internal/proxy` — the MITM seam:
   - **Plain HTTP** flows through `handleHTTP`: clone request, strip hop-by-hop headers, forward via `transport.RoundTripper`, capture request + response, copy back.
   - **HTTPS** flows through `handleConnect`: hijack the TCP conn, write `200 Connection Established`, TLS-handshake as server using `ca.LeafFor` through a `GetCertificate` callback driven by SNI, then loop `http.ReadRequest` off the decrypted `*tls.Conn` and forward each one. This keeps the port speaking proxy protocol while parsing cleartext HTTP internally.
   - Hop-by-hop headers (`Connection`, `Proxy-Connection`, `Upgrade`, etc.) are stripped in both directions per RFC 7230 §6.1.
4. `internal/transport` — pluggable upstream. `Direct()` goes straight to origin; `HTTP(url)` chains through an upstream HTTP proxy (basic auth via URL userinfo → `Proxy-Authorization`); `SOCKS5(addr)` dials through a SOCKS5 proxy via `golang.org/x/net/proxy` wired as `DialContext`. `RulesFromFile(path, log)` loads a JSON rules file (schema in `docs/proxies.md`) whose entries map host globs to `via` strings (`direct` / `http[s]://…` / `socks5[h]://…`), then wraps the ruleset with a poll-based hot-reload watcher — a broken edit logs and keeps the previous ruleset. CLI precedence: `--proxies-config` > `--upstream-http` > `--upstream-socks5` > direct. The rest of the code holds only `RoundTripper`, so future chain types stay a one-line swap.
5. `internal/capture` — bounded ring buffer of `Exchange` records (default 1000) plus a fanout `Subscribe()` channel. Proxy calls `Begin → SetReqBody → SetResponse → Finish`; `Finish` pushes to subscribers with a non-blocking send so a slow SSE client can't stall the proxy path.
6. `internal/api` — separate `http.Server` on `:9090` (so the proxy port only speaks proxy protocol). Endpoints are frozen in `docs/api.md`. This server also mounts the embedded SPA at `/`: non-`/api/*` paths are served from `web.DistFS()`, with a fallback to `index.html` so the React app owns client-side routes. `assets/*` keeps strict 404s — hashed asset names must not be rewritten.
7. `web/` — Vite + React + TypeScript GUI. `web/embed.go` uses `//go:embed all:dist` to bake the built app into the Go binary. A committed stub `web/dist/index.html` ensures `go build` works before the first `npm run build`; it is overwritten by every real build.

## Key invariants

- **Two ports, two roles.** `:8080` is a proxy listener and must never serve API traffic; `:9090` is the API and must never accept proxy requests. Keep handlers cleanly separated.
- **Capture path must not block on consumers.** `capture.Store.Finish` fans out with `select { case ch <- ex: default: }` — preserve that. A hung SSE client must not back up the proxy.
- **The `transport.RoundTripper` seam is the only place upstream proxy chaining plugs in.** Don't leak `http.Transport` into `internal/proxy`.
- **Body handling has two caps.** `BodyCap` (default 1 MiB) is what gets stored + shown. There is also a hard ceiling (10× BodyCap or 10 MiB, whichever is larger) when reading upstream to prevent OOM on huge responses; the difference between the two is what marks `truncated=true`.

## API contract

`docs/api.md` is the contract between this Go core and the Phase 2 React GUI. Treat it as a schema: changes there should be paired with changes to `internal/api` and `internal/capture.Exchange`.

## Testing conventions

- `internal/ca`: verify root is `IsCA`, reload produces identical cert, leaf chains to root and is cached.
- `internal/capture`: ring wrap, eviction from `byID`, subscribe delivery, cancel-is-idempotent.
- `internal/proxy`: two `httptest` servers (origin + proxy) with a shared trust pool — this is how HTTPS interception is exercised without a real CA install.
- `internal/api`: hit endpoints through `httptest.Server`; SSE tests need a short `time.Sleep` before pushing to let the subscriber register.
- `internal/transport`: `HTTP()` is exercised with an "upstream" `httptest` server that records the absolute-form RequestURI and forwards to a second origin server — proves the chain and that URL userinfo becomes `Proxy-Authorization`. `SOCKS5()` is tested at the constructor level (input parsing / error cases); wire-level SOCKS5 behavior is covered by `golang.org/x/net/proxy`. `Ruleset` / `WatchedRules` tests cover glob matching, JSON schema (happy path + all error cases with `DisallowUnknownFields`), first-match-wins dispatch via two `httptest` upstreams, synchronous `Reload()`, reload-failure-keeps-previous, and the poll goroutine end-to-end (short interval + explicit mtime bump so the test is portable across filesystems with coarse mtime granularity).

## GUI notes

- The React app is a single-page dashboard: toolbar (filter / method / clear / CA download / SSE status), request list, detail pane with Request/Response tabs, headers table, and body viewer that auto-pretty-prints JSON and hex-dumps binary payloads.
- `src/useLiveExchanges.ts` combines `GET /api/exchanges` for backfill with an `EventSource` on `/api/stream`; de-duplicates by `id` in case the two overlap.
- Bodies from the API are base64-encoded `[]byte`. `src/body.ts` decodes to UTF-8 when possible, falling back to a hex preview.
- In dev, Vite proxies `/api` → `:9090` (see `web/vite.config.ts`), so the browser sees a single origin either way (served by Go in prod, proxied by Vite in dev).

## Out of scope (current phase)

No persistence, no request replay / breakpoints / rewrite rules. These are tracked in the plan file, not implemented here. Phases 3a–3c are shipped: CA-download interstitial, single-upstream chaining (HTTP + SOCKS5), and JSON rule-based per-host dispatch with hot reload. Phase 4 (SQLite persistence) is the next P1 backbone item.
