package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestHTTPUpstreamForwardsAbsoluteURL verifies that HTTP() sends requests to
// the upstream in absolute-form so the upstream sees the original target URL.
// This is how proxy chaining is supposed to work on the wire.
func TestHTTPUpstreamForwardsAbsoluteURL(t *testing.T) {
	// The upstream proxy: records the request URL it received, then forwards
	// to the real origin so the response still flows back.
	var (
		mu         sync.Mutex
		seenReqURI string
		seenHost   string
		seenAuth   string
	)
	client := &http.Client{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenReqURI = r.RequestURI
		seenHost = r.Host
		seenAuth = r.Header.Get("Proxy-Authorization")
		mu.Unlock()

		// Forward: build an outbound request from the absolute URI we received.
		out, err := http.NewRequest(r.Method, r.RequestURI, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "proxy-") {
				continue
			}
			for _, v := range vs {
				out.Header.Add(k, v)
			}
		}
		resp, err := client.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer upstream.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin", "yes")
		_, _ = w.Write([]byte("hello-from-origin"))
	}))
	defer origin.Close()

	// Put basic auth credentials in the upstream URL — stdlib should turn them
	// into a Proxy-Authorization header on the request the upstream sees.
	uURL, _ := url.Parse(upstream.URL)
	authedUpstream := &url.URL{Scheme: uURL.Scheme, Host: uURL.Host, User: url.UserPassword("alice", "secret")}
	rt, err := HTTP(authedUpstream.String())
	if err != nil {
		t.Fatalf("HTTP(): %v", err)
	}

	target := origin.URL + "/thing?x=1"
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-from-origin" {
		t.Fatalf("body: %q", body)
	}
	if resp.Header.Get("X-Origin") != "yes" {
		t.Fatalf("missing upstream round-trip: headers=%v", resp.Header)
	}

	mu.Lock()
	defer mu.Unlock()
	if seenReqURI != target {
		t.Fatalf("upstream saw RequestURI=%q, want %q", seenReqURI, target)
	}
	originURL, _ := url.Parse(origin.URL)
	if seenHost != originURL.Host {
		t.Fatalf("upstream saw Host=%q, want %q", seenHost, originURL.Host)
	}
	if !strings.HasPrefix(seenAuth, "Basic ") {
		t.Fatalf("upstream saw Proxy-Authorization=%q, want a Basic token", seenAuth)
	}
}

func TestHTTPRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"only-spaces", "   "},
		{"unsupported-scheme", "ftp://example.com"},
		{"missing-host", "http://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := HTTP(tc.in); err == nil {
				t.Fatalf("HTTP(%q) = nil error, want error", tc.in)
			}
		})
	}
}

func TestSOCKS5RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"only-spaces", "   "},
		{"wrong-scheme", "http://host:1080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SOCKS5(tc.in); err == nil {
				t.Fatalf("SOCKS5(%q) = nil error, want error", tc.in)
			}
		})
	}
}

func TestSOCKS5AcceptsBareHostPortAndURL(t *testing.T) {
	// No real SOCKS5 server here — we just verify the constructor succeeds
	// and produces a RoundTripper. Wire-level behaviour is covered by the
	// x/net/proxy library's own tests.
	for _, addr := range []string{"127.0.0.1:1080", "socks5://127.0.0.1:1080", "socks5://u:p@127.0.0.1:1080"} {
		rt, err := SOCKS5(addr)
		if err != nil {
			t.Fatalf("SOCKS5(%q): %v", addr, err)
		}
		if rt == nil {
			t.Fatalf("SOCKS5(%q): nil RoundTripper", addr)
		}
	}
}
