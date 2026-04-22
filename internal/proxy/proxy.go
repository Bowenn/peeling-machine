// Package proxy implements the MITM HTTP(S) proxy. Plain HTTP flows through
// ServeHTTP directly; HTTPS is intercepted by hijacking CONNECT tunnels,
// forging a leaf certificate via the CA, and then parsing cleartext HTTP
// off the decrypted TLS connection in a loop.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/bowen/peeling-machine/internal/ca"
	"github.com/bowen/peeling-machine/internal/capture"
	"github.com/bowen/peeling-machine/internal/transport"
)

type Server struct {
	Addr    string
	CA      *ca.CA
	Store   *capture.Store
	Upstream transport.RoundTripper
	BodyCap int64
	Logger  *slog.Logger

	http *http.Server
}

func New(addr string, c *ca.CA, store *capture.Store, up transport.RoundTripper, bodyCap int64, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Addr:    addr,
		CA:      c,
		Store:   store,
		Upstream: up,
		BodyCap: bodyCap,
		Logger:  log,
	}
}

func (s *Server) ListenAndServe() error {
	s.http = &http.Server{
		Addr:    s.Addr,
		Handler: http.HandlerFunc(s.serve),
		// ErrorLog intentionally unset; errors surface via logger in handlers.
	}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handleHTTP(w, r)
}

// handleHTTP forwards a plain HTTP proxy request and records the exchange.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	ex := s.Store.Begin("http", r)
	body, err := readAndReplace(r, s.BodyCap)
	if err != nil {
		s.Store.SetError(ex, err)
		s.Store.Finish(ex)
		http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.Store.SetReqBody(ex, body, s.BodyCap)

	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHopByHop(out.Header)

	resp, err := s.Upstream.RoundTrip(out)
	if err != nil {
		s.Store.SetError(ex, err)
		s.Store.Finish(ex)
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, rerr := readCapped(resp.Body, s.BodyCap)
	if rerr != nil {
		s.Store.SetError(ex, rerr)
	}
	s.Store.SetResponse(ex, resp, respBody, s.BodyCap)
	s.Store.Finish(ex)

	stripHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// handleConnect hijacks the client connection, terminates TLS using a forged
// leaf cert, then drives a cleartext HTTP loop against the origin.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	// r.Host is "host:port" (the tunnel target). Keep it with the port so
	// upstream dials succeed; SNI (below) drives per-host cert selection.
	targetAuthority := r.Host
	host, _, err := net.SplitHostPort(targetAuthority)
	if err != nil {
		host = targetAuthority
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		s.Logger.Error("hijack", "err", err)
		return
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	tlsConf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := chi.ServerName
			if name == "" {
				name = host
			}
			return s.CA.LeafFor(name)
		},
	}
	tlsConn := tls.Server(clientConn, tlsConf)
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		s.Logger.Debug("tls handshake", "host", host, "err", err)
		return
	}
	defer tlsConn.Close()

	s.serveDecrypted(r.Context(), tlsConn, targetAuthority)
}

// serveDecrypted reads cleartext HTTP requests in a loop off the decrypted
// conn, forwards each, and writes responses back. authority is host:port.
func (s *Server) serveDecrypted(ctx context.Context, conn net.Conn, authority string) {
	br := bufio.NewReader(conn)
	for {
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetReadDeadline(dl)
		}
		req, err := http.ReadRequest(br)
		if err != nil {
			if err != io.EOF {
				s.Logger.Debug("read decrypted req", "authority", authority, "err", err)
			}
			return
		}
		req.URL.Scheme = "https"
		req.URL.Host = authority
		req.RequestURI = ""
		req = req.WithContext(ctx)

		ex := s.Store.Begin("https", req)
		body, berr := readAndReplace(req, s.BodyCap)
		if berr != nil {
			s.Store.SetError(ex, berr)
			s.Store.Finish(ex)
			return
		}
		s.Store.SetReqBody(ex, body, s.BodyCap)

		stripHopByHop(req.Header)
		resp, err := s.Upstream.RoundTrip(req)
		if err != nil {
			s.Store.SetError(ex, err)
			s.Store.Finish(ex)
			writeBadGateway(conn, err)
			return
		}

		respBody, rerr := readCapped(resp.Body, s.BodyCap)
		resp.Body.Close()
		if rerr != nil {
			s.Store.SetError(ex, rerr)
		}
		s.Store.SetResponse(ex, resp, respBody, s.BodyCap)
		s.Store.Finish(ex)

		stripHopByHop(resp.Header)
		if err := writeResponse(conn, resp, respBody); err != nil {
			return
		}
		if resp.Close || req.Close {
			return
		}
	}
}

// --- helpers ---

// hopByHop headers per RFC 7230 §6.1. Not forwarded.
var hopByHop = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func stripHopByHop(h http.Header) {
	for _, k := range hopByHop {
		h.Del(k)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// readAndReplace drains the request body up to a hard ceiling, stores a copy
// for capture, and replaces r.Body so the round-tripper can re-send it.
func readAndReplace(r *http.Request, cap int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	// Hard ceiling is 10x body cap or 10MiB, whichever is larger, to bound memory.
	ceiling := cap * 10
	if ceiling < 10<<20 {
		ceiling = 10 << 20
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, ceiling))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	return buf, nil
}

func readCapped(r io.Reader, cap int64) ([]byte, error) {
	ceiling := cap * 10
	if ceiling < 10<<20 {
		ceiling = 10 << 20
	}
	return io.ReadAll(io.LimitReader(r, ceiling))
}

func writeResponse(w io.Writer, resp *http.Response, body []byte) error {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp.Write(w)
}

func writeBadGateway(w io.Writer, err error) {
	msg := fmt.Sprintf("peeling-machine: upstream failed: %v", err)
	_, _ = fmt.Fprintf(w,
		"HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(msg), msg,
	)
}
