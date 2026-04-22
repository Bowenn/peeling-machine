package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bowen/peeling-machine/internal/ca"
	"github.com/bowen/peeling-machine/internal/capture"
	"github.com/bowen/peeling-machine/internal/transport"
)

func newTestProxy(t *testing.T) (*Server, *capture.Store, *ca.CA, string) {
	t.Helper()
	c, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	store := capture.NewStore(100)
	srv := New("127.0.0.1:0", c, store, transport.Direct(), 1<<20, nil)
	// Start on an ephemeral port via a custom listener.
	ts := httptest.NewUnstartedServer(http.HandlerFunc(srv.serve))
	ts.Start()
	t.Cleanup(ts.Close)
	return srv, store, c, ts.URL
}

func TestPlainHTTPProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin", "yes")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello"))
	}))
	defer origin.Close()

	_, store, _, proxyURL := newTestProxy(t)
	pu, _ := url.Parse(proxyURL)

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(origin.URL + "/greet")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Fatalf("body: %q", string(body))
	}
	if resp.Header.Get("X-Origin") != "yes" {
		t.Fatalf("header missing")
	}
	if len(store.List(0)) != 1 {
		t.Fatalf("want 1 capture, got %d", len(store.List(0)))
	}
	ex := store.List(0)[0]
	if ex.Method != "GET" || ex.Status != 200 || !strings.HasSuffix(ex.Path, "/greet") {
		t.Fatalf("bad exchange: %+v", ex)
	}
	if string(ex.RespBody) != "hello" {
		t.Fatalf("resp body: %q", ex.RespBody)
	}
}

func TestHTTPSConnectIntercept(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer origin.Close()

	_, store, rootCA, proxyURL := newTestProxy(t)
	pu, _ := url.Parse(proxyURL)

	// Trust both our CA (for the forged leaf) and the origin's self-signed cert
	// (so the proxy's upstream transport can talk to it).
	pool := x509.NewCertPool()
	pool.AddCert(rootCA.Cert)
	pool.AddCert(origin.Certificate())

	// Rebuild the proxy's upstream transport with the trust pool it needs.
	srv := New("", rootCA, store, &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}, 1<<20, nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.serve))
	defer ts.Close()
	pu, _ = url.Parse(ts.URL)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin.URL+"/vault", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("https via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secret" {
		t.Fatalf("body: %q", body)
	}
	exs := store.List(0)
	if len(exs) != 1 {
		t.Fatalf("want 1 capture, got %d", len(exs))
	}
	if exs[0].Scheme != "https" {
		t.Fatalf("scheme: %q", exs[0].Scheme)
	}
	if string(exs[0].RespBody) != "secret" {
		t.Fatalf("captured body: %q", exs[0].RespBody)
	}
}
