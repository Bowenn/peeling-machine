# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Peeling Machine is a local HTTP/HTTPS debugging proxy. It MITM-intercepts TLS using its own generated root CA, records every request/response exchange, and exposes them to a UI over a REST + SSE API on a second port.

The codebase splits along two deliberately separated boundaries:

- **Go core** (this repo): proxy, CA, capture store, API. Lives on two localhost ports.
- **React GUI** (future, separate session): consumes the API contract frozen in `docs/api.md`.

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
```

## Architecture — the big picture

The proxy is one `http.Server` on `:8080`; HTTPS is handled by hijacking `CONNECT`:

1. `cmd/peeling-machine` wires together `ca → capture → transport → proxy → api` and runs two goroutines (proxy port + API port).
2. `internal/ca` — root CA is generated on first run under `~/.peeling-machine`; leaf certs are signed on demand (one ECDSA leaf key reused across all hosts) and cached in a `sync.Map`. The leaf key is re-generated per process — it never touches disk.
3. `internal/proxy` — the MITM seam:
   - **Plain HTTP** flows through `handleHTTP`: clone request, strip hop-by-hop headers, forward via `transport.RoundTripper`, capture request + response, copy back.
   - **HTTPS** flows through `handleConnect`: hijack the TCP conn, write `200 Connection Established`, TLS-handshake as server using `ca.LeafFor` through a `GetCertificate` callback driven by SNI, then loop `http.ReadRequest` off the decrypted `*tls.Conn` and forward each one. This keeps the port speaking proxy protocol while parsing cleartext HTTP internally.
   - Hop-by-hop headers (`Connection`, `Proxy-Connection`, `Upgrade`, etc.) are stripped in both directions per RFC 7230 §6.1.
4. `internal/transport` — pluggable upstream. `Direct()` returns a stdlib `http.Transport`; future phases will add chained upstream proxies (HTTP, SOCKS5, user-built) by swapping this interface. The rest of the code holds only `RoundTripper`.
5. `internal/capture` — bounded ring buffer of `Exchange` records (default 1000) plus a fanout `Subscribe()` channel. Proxy calls `Begin → SetReqBody → SetResponse → Finish`; `Finish` pushes to subscribers with a non-blocking send so a slow SSE client can't stall the proxy path.
6. `internal/api` — separate `http.Server` on `:9090` (so the proxy port only speaks proxy protocol). Endpoints are frozen in `docs/api.md`; the GUI session reads that file as its source of truth.

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

## Out of scope (current phase)

No React UI, no persistence, no chained upstream proxies, no request replay / breakpoints / rewrite rules. These are tracked in the plan file, not implemented here.
