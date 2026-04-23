# peeling-machine

Peeling Machine is a developer tool for capturing and inspecting local HTTP/HTTPS network traffic. It runs as a local MITM proxy with its own custom Certificate Authority (CA), intercepting, decrypting, and logging requests from browsers and mobile devices for easy API debugging and network analysis.

## Status

**Phases 1–2 shipped.** The proxy, the REST + SSE API, and the React GUI are all in place:

- HTTP and HTTPS traffic routed through the proxy is captured with full headers and bodies.
- Captures are exposed over a REST + Server-Sent Events API on a second port.
- Root CA is auto-generated on first run; per-host leaf certs are signed on demand.
- A React + TypeScript GUI (in `web/`) is embedded into the Go binary via `//go:embed` and served from the API port — `go build` ships a single self-contained binary.

Chained upstream proxies, persistence, and request replay (Phase 3+) are still planned. See [`docs/plan.md`](docs/plan.md).

## Quick start

Requires Go 1.22+ (tested on 1.26) and Node 20+ with [pnpm](https://pnpm.io) for a first-time GUI build.

```bash
# Build the React GUI once (produces web/dist, which the Go binary embeds).
cd web && pnpm install && pnpm run build && cd ..

# Build the single self-contained Go binary.
go build ./...

# Run (proxy on :8080, API + GUI on :9090, CA stored in ~/.peeling-machine)
go run ./cmd/peeling-machine

# Open the GUI:
open http://localhost:9090/

# Print the root CA path so you can install it into your OS / browser trust store
go run ./cmd/peeling-machine ca export
```

The repo ships a placeholder `web/dist/index.html` so `go build ./...` works before the first `pnpm run build`; rebuild the Go binary after every GUI build to pick up the new bundle.

### Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--proxy-addr` | `:8080` | proxy listen address |
| `--api-addr` | `:9090` | REST + SSE listen address |
| `--ca-dir` | `~/.peeling-machine` | where the root CA lives |
| `--buffer-size` | `1000` | in-memory capture ring size |
| `--body-cap` | `1048576` | per-body byte cap for captures |

## Routing traffic through the proxy

```bash
# Plain HTTP
curl -x http://localhost:8080 http://example.com/

# HTTPS (after trusting the CA, or pass --cacert)
curl --cacert "$(go run ./cmd/peeling-machine ca export)" \
     -x http://localhost:8080 https://example.com/
```

For a browser, set HTTP/HTTPS proxy to `localhost:8080` and import the CA (downloadable at `http://localhost:9090/api/ca`) into the OS / browser trust store.

## Security

Peeling Machine's root CA is an **attack primitive**. Any device that trusts it will accept forged certificates for **any** hostname from this process, which lets Peeling Machine decrypt every TLS connection on that device — not just traffic you intentionally route through the proxy.

Before you install the CA, understand:

- **Install only on devices you own.** Installing this CA on a machine you don't own is indistinguishable from an attacker planting a MITM certificate.
- **Uninstall when finished.** Treat the CA like a debug-only backstage pass, not a permanent trust anchor. The private key lives in `~/.peeling-machine/ca.key` — anyone with access to that file and to your machine's network can decrypt TLS from any device that trusts the CA.
- **The GUI enforces this with a two-checkbox interstitial** before the browser download; the `ca export` CLI prints a `WARN:` banner on stderr. `/api/ca` itself stays an unauthenticated raw PEM so scripted flows like `curl --cacert "$(peeling-machine ca export)" ...` keep working — the friction lives in the UX, not the endpoint.

### Uninstalling the CA

| Platform | How |
| --- | --- |
| macOS | Keychain Access → System/login → delete the `Peeling Machine` certificate. [Apple docs](https://support.apple.com/guide/keychain-access/remove-a-certificate-kyca3004/mac) |
| Windows | `certmgr.msc` → Trusted Root Certification Authorities → Certificates → delete. [Microsoft docs](https://learn.microsoft.com/en-us/windows-hardware/drivers/install/trusted-root-certification-authorities-certificate-store) |
| Linux | Remove the PEM from `/usr/local/share/ca-certificates/` (Debian/Ubuntu) or `/etc/pki/ca-trust/source/anchors/` (RHEL/Fedora), then `update-ca-certificates` / `update-ca-trust`. [Ubuntu docs](https://ubuntu.com/server/docs/security-trust-store) |
| iOS | Settings → General → VPN & Device Management → remove the profile; also turn off full trust in Settings → General → About → Certificate Trust Settings. [Apple docs](https://support.apple.com/guide/iphone/install-or-remove-configuration-profiles-iph6c493b19/ios) |
| Android | Settings → Security → Encryption & credentials → User credentials → remove. [Google docs](https://support.google.com/pixelphone/answer/2844832) |

## API

The GUI contract is frozen in [`docs/api.md`](docs/api.md). Key endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/exchanges?limit=100` | newest-first list of captured exchanges |
| `GET` | `/api/exchanges/{id}` | full detail for one exchange |
| `DELETE` | `/api/exchanges` | clear the in-memory buffer |
| `GET` | `/api/stream` | Server-Sent Events — one event per new exchange |
| `GET` | `/api/ca` | download the root CA as PEM |
| `GET` | `/api/health` | liveness probe |

```bash
# Follow new traffic live:
curl -N http://localhost:9090/api/stream
```

## Architecture at a glance

Two listeners, one process:

- `:8080` is the proxy. Plain HTTP flows through a forwarding handler; HTTPS hijacks the `CONNECT` tunnel, TLS-terminates with an SNI-driven forged leaf cert, and then parses cleartext HTTP off the decrypted connection.
- `:9090` serves the REST + SSE API so the proxy port only ever speaks proxy protocol.

Layout:

```
cmd/peeling-machine   CLI entrypoint, wires everything together
internal/ca           root CA generate-or-load + on-demand leaf certs
internal/proxy        HTTP + HTTPS MITM proxy, CONNECT handler
internal/transport    pluggable upstream RoundTripper (direct today, chained proxies later)
internal/capture      bounded ring buffer + fanout of Exchange records
internal/api          REST + SSE server for the GUI; also serves the embedded SPA
internal/config       flag defaults
web/                  Vite + React + TypeScript GUI; web/dist is //go:embed-ed
docs/api.md           frozen GUI contract
docs/plan.md          build plan / roadmap
CLAUDE.md             architecture notes & invariants for future work
```

## Development

```bash
go test ./...                                          # full Go suite
go test ./internal/proxy                               # one package
go test ./internal/proxy -run TestHTTPSConnectIntercept  # one test
go vet ./...

# GUI dev loop — Vite on :5173 with /api proxied to the Go server on :9090
cd web && pnpm run dev
```

The Vite dev server proxies `/api/*` (REST + SSE) to `localhost:9090`, so you run the Go binary and `pnpm run dev` side by side. For production, `pnpm run build` writes `web/dist` which the Go binary picks up on its next `go build`.

## License

See [`LICENSE`](LICENSE).
