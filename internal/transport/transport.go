// Package transport abstracts the upstream HTTP round-tripper used by the proxy.
// Phase 1 only ships the direct implementation; future phases plug in chained
// upstream proxies (HTTP, SOCKS5, user-built) by returning a different RoundTripper.
package transport

import (
	"crypto/tls"
	"net/http"
	"time"
)

// RoundTripper is the minimal interface the proxy needs to send a request upstream.
type RoundTripper interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

// Direct returns a RoundTripper that connects straight to the origin server.
func Direct() RoundTripper {
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}
