// Package transport abstracts the upstream HTTP round-tripper used by the proxy.
// Direct() goes straight to the origin; HTTP() chains through an upstream HTTP
// proxy; SOCKS5() dials through a SOCKS5 proxy. The proxy code only sees the
// RoundTripper interface, so switching chains is a one-line change at startup.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// RoundTripper is the minimal interface the proxy needs to send a request upstream.
type RoundTripper interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

// Direct returns a RoundTripper that connects straight to the origin server.
func Direct() RoundTripper {
	return baseTransport()
}

// HTTP returns a RoundTripper that chains through an HTTP (or HTTPS) upstream
// proxy. upstreamURL must be a full URL like "http://user:pass@host:port" or
// "https://host:port". Basic auth credentials in the URL are forwarded via
// Proxy-Authorization by the stdlib.
func HTTP(upstreamURL string) (RoundTripper, error) {
	if strings.TrimSpace(upstreamURL) == "" {
		return nil, errors.New("upstream URL is empty")
	}
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported upstream scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("upstream URL is missing host")
	}
	tr := baseTransport()
	tr.Proxy = http.ProxyURL(u)
	return tr, nil
}

// SOCKS5 returns a RoundTripper that dials all upstream connections through a
// SOCKS5 proxy. addr accepts either a bare "host:port" or a full
// "socks5://[user:pass@]host:port" URL; basic auth in the URL form is honored.
func SOCKS5(addr string) (RoundTripper, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, errors.New("socks5 address is empty")
	}
	u, err := parseSOCKS5URL(addr)
	if err != nil {
		return nil, err
	}
	d, err := xproxy.FromURL(u, xproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("build socks5 dialer: %w", err)
	}

	var dialContext func(ctx context.Context, network, address string) (net.Conn, error)
	if cd, ok := d.(xproxy.ContextDialer); ok {
		dialContext = cd.DialContext
	} else {
		// Fallback for dialers without native context support: ignore the
		// context's deadline/cancel. The SOCKS5 dialer in x/net/proxy does
		// implement ContextDialer, so this is defensive.
		dialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return d.Dial(network, address)
		}
	}

	tr := baseTransport()
	tr.Proxy = nil
	tr.DialContext = dialContext
	return tr, nil
}

func parseSOCKS5URL(addr string) (*url.URL, error) {
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return nil, fmt.Errorf("parse socks5 URL: %w", err)
		}
		if u.Scheme != "socks5" && u.Scheme != "socks5h" {
			return nil, fmt.Errorf("unsupported socks5 scheme %q", u.Scheme)
		}
		if u.Host == "" {
			return nil, errors.New("socks5 URL is missing host")
		}
		return u, nil
	}
	return &url.URL{Scheme: "socks5", Host: addr}, nil
}

func baseTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}
