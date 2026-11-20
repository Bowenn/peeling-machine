package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bowen/peeling-machine/internal/capture"
)

// Bigger than the old hard ceiling (10 MiB) so truncation-on-forward shows up.
const bigBody = 11 << 20

// tlsRig is a proxy whose upstream trusts the given TLS origin, plus a client
// that trusts the proxy's CA. ForceAttemptHTTP2 mirrors transport.baseTransport.
type tlsRig struct {
	store     *capture.Store
	client    *http.Client
	proxyAddr string
	pool      *x509.CertPool
}

func newTLSRig(t *testing.T, origin *httptest.Server, bodyCap int64) *tlsRig {
	t.Helper()
	_, store, rootCA, _ := newTestProxy(t)
	pool := x509.NewCertPool()
	pool.AddCert(rootCA.Cert)
	pool.AddCert(origin.Certificate())

	srv := New("", rootCA, store, &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}, bodyCap, nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.serve))
	t.Cleanup(ts.Close)
	pu, _ := url.Parse(ts.URL)

	return &tlsRig{
		store: store,
		client: &http.Client{Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
		proxyAddr: pu.Host,
		pool:      pool,
	}
}

// plainRig is a proxy for plain-HTTP origins with the given body cap.
func newPlainClient(t *testing.T, bodyCap int64) (*capture.Store, *http.Client) {
	t.Helper()
	_, store, rootCA, _ := newTestProxy(t)
	srv := New("", rootCA, store, &http.Transport{}, bodyCap, nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.serve))
	t.Cleanup(ts.Close)
	pu, _ := url.Parse(ts.URL)
	return store, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
}

// waitForCapture polls until n exchanges are recorded; Finish can land a hair
// after the client has its response.
func waitForCapture(t *testing.T, store *capture.Store, n int) []*capture.Exchange {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exs := store.List(0); len(exs) >= n {
			return exs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d captures, got %d", n, len(store.List(0)))
	return nil
}

// A GET with no body must reach an HTTP/2 origin as a body-less request
// (HEADERS with END_STREAM). Sending an empty DATA frame instead makes some
// CDNs reset the stream mid-response.
func TestHTTPSGetWithoutBodyReachesH2OriginWithoutBody(t *testing.T) {
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "want HTTP/2, got "+r.Proto, http.StatusHTTPVersionNotSupported)
			return
		}
		if r.ContentLength != 0 {
			http.Error(w, "GET arrived with a body stream, ContentLength="+strconv.FormatInt(r.ContentLength, 10), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()

	rig := newTLSRig(t, origin, 1<<20)
	resp, err := rig.client.Get(origin.URL + "/asset.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

// truncatingOrigin promises 100 bytes and sends 10, then hangs up.
func truncatingOrigin(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n0123456789")
}

// If the upstream body breaks mid-way, the client must see a failure, not a
// complete-looking 200 with a short body.
func TestUpstreamBodyErrorIsNotMaskedAsSuccess(t *testing.T) {
	t.Run("https", func(t *testing.T) {
		origin := httptest.NewTLSServer(http.HandlerFunc(truncatingOrigin))
		defer origin.Close()
		rig := newTLSRig(t, origin, 1<<20)
		assertBodyReadFails(t, rig.client, origin.URL+"/x")
	})
	t.Run("http", func(t *testing.T) {
		origin := httptest.NewServer(http.HandlerFunc(truncatingOrigin))
		defer origin.Close()
		_, client := newPlainClient(t, 1<<20)
		assertBodyReadFails(t, client, origin.URL+"/x")
	})
}

func assertBodyReadFails(t *testing.T, client *http.Client, u string) {
	t.Helper()
	resp, err := client.Get(u)
	if err != nil {
		return // failing at the header stage is also a correct outcome
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("client read a complete-looking %d response (%d bytes); want a read error", resp.StatusCode, len(body))
	}
}

// Server-sent events / chunked streams must reach the client as they are
// produced, not after the origin finishes.
func TestStreamingResponseIsForwardedIncrementally(t *testing.T) {
	handler := func(release chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			_, _ = io.WriteString(w, "data: last\n\n")
		}
	}
	check := func(t *testing.T, client *http.Client, u string, release chan struct{}) {
		t.Helper()
		defer close(release)
		// The whole request runs in a goroutine: a buffering proxy blocks
		// before response headers, not just before the body.
		got := make(chan string, 1)
		go func() {
			resp, err := client.Get(u)
			if err != nil {
				got <- "error: " + err.Error()
				return
			}
			defer resp.Body.Close()
			line, _ := bufio.NewReader(resp.Body).ReadString('\n')
			got <- line
		}()
		select {
		case line := <-got:
			if line != "data: first\n" {
				t.Fatalf("first line: %q", line)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("first event not delivered while origin is still streaming")
		}
	}

	t.Run("https", func(t *testing.T) {
		release := make(chan struct{})
		origin := httptest.NewTLSServer(handler(release))
		defer origin.Close()
		check(t, newTLSRig(t, origin, 1<<20).client, origin.URL+"/events", release)
	})
	t.Run("http", func(t *testing.T) {
		release := make(chan struct{})
		origin := httptest.NewServer(handler(release))
		defer origin.Close()
		_, client := newPlainClient(t, 1<<20)
		check(t, client, origin.URL+"/events", release)
	})
}

// Bodies above any capture cap must still be forwarded byte-for-byte; only
// the stored copy is capped.
func TestLargeResponseIsForwardedIntact(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), bigBody/16)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer origin.Close()

	const capBytes = 1024
	rig := newTLSRig(t, origin, capBytes)
	resp, err := rig.client.Get(origin.URL + "/big.bin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("client got %d bytes, want %d", len(body), len(payload))
	}
	ex := waitForCapture(t, rig.store, 1)[0]
	if len(ex.RespBody) != capBytes || !ex.RespTruncated {
		t.Fatalf("capture: len=%d truncated=%v, want len=%d truncated=true", len(ex.RespBody), ex.RespTruncated, capBytes)
	}
}

func TestLargeRequestBodyIsForwardedIntact(t *testing.T) {
	payload := bytes.Repeat([]byte("upload!!"), bigBody/8)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = fmt.Fprint(w, n)
	}))
	defer origin.Close()

	const capBytes = 1024
	rig := newTLSRig(t, origin, capBytes)
	resp, err := rig.client.Post(origin.URL+"/upload", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != strconv.Itoa(len(payload)) {
		t.Fatalf("origin received %s bytes, want %d", got, len(payload))
	}
	ex := waitForCapture(t, rig.store, 1)[0]
	if len(ex.ReqBody) != capBytes || !ex.ReqTruncated {
		t.Fatalf("capture: len=%d truncated=%v, want len=%d truncated=true", len(ex.ReqBody), ex.ReqTruncated, capBytes)
	}
}

// echoUpgradeOrigin accepts an Upgrade: websocket handshake and then echoes
// raw bytes. After 101 the proxy is protocol-agnostic, so no WS framing needed.
func echoUpgradeOrigin(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "upgrade headers missing", http.StatusBadRequest)
		return
	}
	conn, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_, _ = io.Copy(conn, brw)
}

func upgradeRequest(target, host string) string {
	return "GET " + target + " HTTP/1.1\r\nHost: " + host +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
}

func assertUpgradedEcho(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("handshake status %d: %s", resp.StatusCode, body)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q err=%v", buf, err)
	}
}

func TestWebSocketUpgradeIsTunneled(t *testing.T) {
	t.Run("https", func(t *testing.T) {
		origin := httptest.NewTLSServer(http.HandlerFunc(echoUpgradeOrigin))
		defer origin.Close()
		rig := newTLSRig(t, origin, 1<<20)
		ou, _ := url.Parse(origin.URL)

		raw, err := net.Dial("tcp", rig.proxyAddr)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.WriteString(raw, "CONNECT "+ou.Host+" HTTP/1.1\r\nHost: "+ou.Host+"\r\n\r\n")
		cresp, err := http.ReadResponse(bufio.NewReader(raw), nil)
		if err != nil || cresp.StatusCode != 200 {
			t.Fatalf("CONNECT: %v %v", cresp, err)
		}
		conn := tls.Client(raw, &tls.Config{RootCAs: rig.pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
		_, _ = io.WriteString(conn, upgradeRequest("/ws", ou.Host))
		assertUpgradedEcho(t, conn, bufio.NewReader(conn))

		ex := waitForCapture(t, rig.store, 1)[0]
		if ex.Status != http.StatusSwitchingProtocols {
			t.Fatalf("captured status %d, want 101", ex.Status)
		}
	})
	t.Run("http", func(t *testing.T) {
		origin := httptest.NewServer(http.HandlerFunc(echoUpgradeOrigin))
		defer origin.Close()
		_, store, _, proxyURL := newTestProxy(t)
		pu, _ := url.Parse(proxyURL)
		ou, _ := url.Parse(origin.URL)

		conn, err := net.Dial("tcp", pu.Host)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, upgradeRequest(origin.URL+"/ws", ou.Host))
		assertUpgradedEcho(t, conn, bufio.NewReader(conn))

		ex := waitForCapture(t, store, 1)[0]
		if ex.Status != http.StatusSwitchingProtocols {
			t.Fatalf("captured status %d, want 101", ex.Status)
		}
	})
}
