# peeling-machine

Peeling Machine is a developer tool for capturing and inspecting local HTTP/HTTPS network traffic. It runs as a local MITM proxy with its own custom Certificate Authority (CA), intercepting, decrypting, and logging requests from browsers and mobile devices for easy API debugging and network analysis.

## Status

**Phase 1 — Go core + HTTP API.** The proxy is working end-to-end:

- HTTP and HTTPS traffic routed through the proxy is captured with full headers and bodies.
- Captures are exposed over a REST + Server-Sent Events API on a second port, ready for the upcoming React GUI.
- Root CA is auto-generated on first run; per-host leaf certs are signed on demand.

A React GUI (Phase 2) and chained upstream proxies, persistence, and request replay (Phase 3+) are planned. See [`docs/plan.md`](docs/plan.md).

## Quick start

Requires Go 1.22+ (tested on 1.26).

```bash
# Build
go build ./...

# Run (proxy on :8080, API on :9090, CA stored in ~/.peeling-machine)
go run ./cmd/peeling-machine

# Print the root CA path so you can install it into your OS / browser trust store
go run ./cmd/peeling-machine ca export
```

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
internal/api          REST + SSE server for the GUI
internal/config       flag defaults
docs/api.md           frozen GUI contract
docs/plan.md          build plan / roadmap
CLAUDE.md             architecture notes & invariants for future work
```

## Development

```bash
go test ./...                                          # full suite
go test ./internal/proxy                               # one package
go test ./internal/proxy -run TestHTTPSConnectIntercept  # one test
go vet ./...
```

## License

See [`LICENSE`](LICENSE).
