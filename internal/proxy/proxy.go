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
	"errors"
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
// The response body is streamed to the client (flushed per read) while a
// capped copy is kept for capture.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	ex := s.Store.Begin("http", r)
	reqBody, err := prepareRequestBody(r, s.BodyCap)
	if err != nil {
		s.Store.SetError(ex, err)
		s.Store.Finish(ex)
		http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.Store.SetReqBody(ex, reqBody, s.BodyCap)

	out := r.Clone(r.Context())
	out.RequestURI = ""
	upgrade := isUpgrade(r.Header)
	if upgrade {
		stripHopByHopKeepUpgrade(out.Header)
	} else {
		stripHopByHop(out.Header)
	}

	resp, err := s.Upstream.RoundTrip(out)
	if err != nil {
		s.Store.SetError(ex, err)
		s.Store.Finish(ex)
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	if upgrade && resp.StatusCode == http.StatusSwitchingProtocols {
		s.tunnelUpgradeHTTP(w, ex, resp)
		return
	}
	defer resp.Body.Close()

	captured := resp.Header.Clone()
	stripHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	cb := &capBuffer{limit: captureLimit(s.BodyCap)}
	rec := &errRecorder{r: io.TeeReader(resp.Body, cb)}
	_, _ = io.Copy(flushWriter{w: w, rc: http.NewResponseController(w)}, rec)
	s.finishCapture(ex, resp.StatusCode, captured, cb.buf, rec.err)
	if rec.err != nil {
		// Upstream broke mid-body. Abort the client connection so it sees a
		// failure instead of a complete-looking short response.
		panic(http.ErrAbortHandler)
	}
}

// tunnelUpgradeHTTP completes a plain-HTTP protocol upgrade (ws://): relay
// the 101 to the client, then splice raw bytes both ways.
func (s *Server) tunnelUpgradeHTTP(w http.ResponseWriter, ex *capture.Exchange, resp *http.Response) {
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		s.Store.SetError(ex, errors.New("upstream 101 response body is not writable"))
		s.Store.Finish(ex)
		http.Error(w, "upstream upgrade failed", http.StatusBadGateway)
		return
	}
	s.finishCapture(ex, resp.StatusCode, resp.Header.Clone(), nil, nil)

	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		upstream.Close()
		s.Logger.Error("hijack for upgrade", "err", err)
		return
	}
	if err := writeHandshake(conn, resp); err != nil {
		conn.Close()
		upstream.Close()
		return
	}
	splice(conn, brw.Reader, upstream)
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
		reqBody, berr := prepareRequestBody(req, s.BodyCap)
		if berr != nil {
			s.Store.SetError(ex, berr)
			s.Store.Finish(ex)
			return
		}
		s.Store.SetReqBody(ex, reqBody, s.BodyCap)

		upgrade := isUpgrade(req.Header)
		if upgrade {
			stripHopByHopKeepUpgrade(req.Header)
		} else {
			stripHopByHop(req.Header)
		}
		resp, err := s.Upstream.RoundTrip(req)
		if err != nil {
			s.Store.SetError(ex, err)
			s.Store.Finish(ex)
			writeBadGateway(conn, err)
			return
		}

		if upgrade && resp.StatusCode == http.StatusSwitchingProtocols {
			upstream, ok := resp.Body.(io.ReadWriteCloser)
			if !ok {
				resp.Body.Close()
				err := errors.New("upstream 101 response body is not writable")
				s.Store.SetError(ex, err)
				s.Store.Finish(ex)
				writeBadGateway(conn, err)
				return
			}
			s.finishCapture(ex, resp.StatusCode, resp.Header.Clone(), nil, nil)
			if err := writeHandshake(conn, resp); err != nil {
				upstream.Close()
				return
			}
			// br may already hold bytes the client sent after the handshake.
			splice(conn, br, upstream)
			return
		}

		if !s.writeStreamedResponse(conn, req, resp, ex) {
			return
		}
	}
}

// writeStreamedResponse relays resp to the decrypted client conn as HTTP/1.1,
// streaming the body (chunked when the length is unknown) while capturing a
// capped copy. It reports whether the connection can serve another request.
func (s *Server) writeStreamedResponse(conn net.Conn, req *http.Request, resp *http.Response, ex *capture.Exchange) bool {
	defer resp.Body.Close()
	captured := resp.Header.Clone()
	stripHopByHop(resp.Header)

	cb := &capBuffer{limit: captureLimit(s.BodyCap)}
	rec := &errRecorder{r: io.TeeReader(resp.Body, cb)}
	out := &http.Response{
		Status:        resp.Status,
		StatusCode:    resp.StatusCode,
		Proto:         "HTTP/1.1", // client side is always HTTP/1.1, whatever upstream spoke
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        resp.Header,
		ContentLength: resp.ContentLength,
		Body:          io.NopCloser(rec),
		Request:       req,
		Close:         req.Close,
	}
	if !bodyAllowed(req.Method, resp.StatusCode) {
		out.Body = http.NoBody
		if out.ContentLength < 0 {
			out.ContentLength = 0
		}
	} else if resp.ContentLength < 0 {
		out.TransferEncoding = []string{"chunked"}
	}

	// Writes go straight to the TLS conn, so each chunk reaches the client
	// as soon as upstream produces it (SSE, long-poll, streamed downloads).
	werr := out.Write(conn)
	s.finishCapture(ex, resp.StatusCode, captured, cb.buf, rec.err)
	// On an upstream read error the response is incomplete (no final chunk /
	// short Content-Length); closing the conn makes the client see a failure.
	return werr == nil && rec.err == nil && !out.Close
}

// finishCapture records the response half of an exchange and publishes it.
func (s *Server) finishCapture(ex *capture.Exchange, status int, hdr http.Header, body []byte, upErr error) {
	s.Store.SetResponse(ex, &http.Response{StatusCode: status, Header: hdr}, body, s.BodyCap)
	if upErr != nil {
		s.Store.SetError(ex, upErr)
	}
	s.Store.Finish(ex)
}

// --- helpers ---

// flushWriter flushes after every write so streamed bodies reach the client
// as they arrive.
type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.rc.Flush()
	}
	return n, err
}

// writeHandshake relays an upstream 101 Switching Protocols to the client.
func writeHandshake(w io.Writer, resp *http.Response) error {
	h := resp.Header.Clone()
	stripHopByHopKeepUpgrade(h)
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	_ = h.Write(&b)
	b.WriteString("\r\n")
	_, err := w.Write(b.Bytes())
	return err
}

func writeBadGateway(w io.Writer, err error) {
	msg := fmt.Sprintf("peeling-machine: upstream failed: %v", err)
	_, _ = fmt.Fprintf(w,
		"HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(msg), msg,
	)
}
