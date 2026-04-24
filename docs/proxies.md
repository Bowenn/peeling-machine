# Per-host upstream proxy rules

`--proxies-config=<path>` tells Peeling Machine to dispatch each request to a
different upstream based on the target host. The file is a JSON document that
is live-reloaded — edit it and new requests pick up the new rules within a
couple of seconds, no restart needed.

When `--proxies-config` is set it **overrides** the single-upstream flags
`--upstream-http` and `--upstream-socks5`; the rules file is the complete
routing table.

## Schema

```json
{
  "rules": [
    { "match": { "host": "*.corp.internal" }, "via": "http://corp-proxy:8080" },
    { "match": { "host": "api.example.com" }, "via": "direct" },
    { "match": { "host": "*" },                "via": "socks5://127.0.0.1:1080" }
  ]
}
```

- `rules` — evaluated top to bottom, **first match wins**. Order is
  meaningful.
- `match.host` — a hostname glob matched against the request URL's hostname
  (port stripped, case-insensitive). Supported forms:
  - exact host: `api.example.com`
  - single wildcard: `*` — matches any hostname
  - subdomain wildcard: `*.example.com` — matches `foo.example.com` and
    `a.b.example.com`, but **not** `example.com` itself (the `*.` requires
    at least one label)
  - any `path.Match`-compatible glob (dots in hostnames are treated as
    literal characters, so `*` spans across `.`)
- `via` — where to send a matched request:
  - `"direct"` — no upstream, connect to the origin directly. Useful as an
    exception above a catch-all SOCKS5/HTTP rule.
  - `"http://[user:pass@]host:port"` — chain through an HTTP proxy; basic
    auth in the URL is forwarded as `Proxy-Authorization`.
  - `"https://[user:pass@]host:port"` — same, TLS between us and the
    upstream.
  - `"socks5://[user:pass@]host:port"` or `"socks5h://…"` — chain through a
    SOCKS5 proxy.

If no rule matches, requests fall through to a direct connection. You can
make this explicit (and catch misconfigurations earlier) by ending the list
with a catch-all `"host": "*"` rule.

Unknown top-level keys and unknown scheme prefixes cause a load failure —
the proxy either starts with a fully valid config or refuses to start at
all. Same goes for the hot reload: a broken edit logs a warning and keeps
the **previous** ruleset serving, so you can't blackhole traffic with a
typo.

## Hot reload

A background goroutine polls the file's modification time every two
seconds. On a change:

- If the new file parses and every `via` resolves, the active ruleset is
  swapped atomically.
- If it doesn't, the previous ruleset keeps serving and the failure is
  logged. Fix the file and save again; the next poll picks it up.

Stat failures (file temporarily missing during an atomic rename, say) are
logged once and tolerated — the previous ruleset still serves requests.

## Interaction with other flags

```
--proxies-config      ← highest precedence; overrides the two below
--upstream-http       ← single HTTP upstream for everything
--upstream-socks5     ← single SOCKS5 upstream for everything
(none)                ← direct to origin
```

Setting both `--proxies-config` and one of the single-upstream flags emits
a warning at startup and uses `--proxies-config`.
