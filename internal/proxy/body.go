package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Bodies are streamed between client and origin; only a capped copy is kept
// for capture. Memory per exchange is bounded by BodyCap regardless of how
// large the real body is.

// capBuffer keeps the first limit bytes written to it and discards the rest.
// Writes never fail, so it is safe as the side of a TeeReader.
type capBuffer struct {
	buf   []byte
	limit int64
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.limit - int64(len(c.buf)); room > 0 {
		if int64(len(p)) < room {
			room = int64(len(p))
		}
		c.buf = append(c.buf, p[:room]...)
	}
	return len(p), nil
}

// captureLimit is what a capBuffer must hold so that capture.truncate(buf,
// cap) can tell "exactly cap bytes" from "more than cap bytes".
func captureLimit(cap int64) int64 { return cap + 1 }

// errRecorder remembers the first non-EOF error from the wrapped reader, so
// callers can tell an upstream read failure from a client write failure.
type errRecorder struct {
	r   io.Reader
	err error
}

func (e *errRecorder) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}

type readCloser struct {
	io.Reader
	io.Closer
}

// prepareRequestBody peeks up to the capture limit of r's body for capture and
// rewires r.Body so the full body is still forwarded. A request with no body
// gets http.NoBody: a non-nil empty reader would make the HTTP/2 transport
// open a request body stream (HEADERS without END_STREAM + empty DATA), which
// some CDNs answer by resetting the stream mid-response.
func prepareRequestBody(r *http.Request, cap int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		r.Body = http.NoBody
		r.ContentLength = 0
		return nil, nil
	}
	orig := r.Body
	head, err := io.ReadAll(io.LimitReader(orig, captureLimit(cap)))
	if err != nil {
		orig.Close()
		return nil, err
	}
	switch {
	case len(head) == 0:
		orig.Close()
		r.Body = http.NoBody
		r.ContentLength = 0
	case int64(len(head)) < captureLimit(cap):
		// Whole body fits in the peek; the original is drained.
		orig.Close()
		r.Body = io.NopCloser(bytes.NewReader(head))
		r.ContentLength = int64(len(head))
	default:
		// Larger than the cap: replay the peeked head, then stream the rest.
		// ContentLength stays as the client sent it (-1 means chunked).
		r.Body = readCloser{io.MultiReader(bytes.NewReader(head), orig), orig}
	}
	return head, nil
}

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

// stripHopByHopKeepUpgrade is stripHopByHop for a protocol-upgrade exchange:
// Upgrade and Connection are the handshake itself and must be forwarded
// (RFC 7230 §6.7). Connection is reduced to just "Upgrade".
func stripHopByHopKeepUpgrade(h http.Header) {
	upgrade := h.Values("Upgrade")
	stripHopByHop(h)
	for _, v := range upgrade {
		h.Add("Upgrade", v)
	}
	h.Set("Connection", "Upgrade")
}

// isUpgrade reports whether h asks for a protocol switch (e.g. WebSocket).
func isUpgrade(h http.Header) bool {
	if h.Get("Upgrade") == "" {
		return false
	}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// bodyAllowed mirrors net/http's rule: 1xx, 204 and 304 never carry a body,
// and neither does any response to HEAD.
func bodyAllowed(method string, status int) bool {
	if method == http.MethodHead {
		return false
	}
	return !(status >= 100 && status <= 199) && status != http.StatusNoContent && status != http.StatusNotModified
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// splice pipes bytes both ways until either side ends, then closes both.
// clientR carries any bytes the client already sent past the handshake.
func splice(client io.ReadWriteCloser, clientR io.Reader, upstream io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, clientR); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	client.Close()
	upstream.Close()
	<-done
}
