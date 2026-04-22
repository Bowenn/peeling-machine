// Package capture records HTTP exchanges that pass through the proxy and
// fans them out to subscribers (the SSE stream, tests, etc.).
package capture

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Header is a flat [name, value] pair list preserving order and duplicates.
type Header [][2]string

// Exchange is a single request/response round trip captured by the proxy.
// Body fields are truncated to the store's byte cap; Truncated flags say so.
type Exchange struct {
	ID             uint64    `json:"id"`
	StartedAt      time.Time `json:"started_at"`
	DurationMS     int64     `json:"duration_ms"`
	Scheme         string    `json:"scheme"`
	Method         string    `json:"method"`
	Host           string    `json:"host"`
	Path           string    `json:"path"`
	ReqHeaders     Header    `json:"req_headers"`
	ReqBody        []byte    `json:"req_body,omitempty"`
	ReqTruncated   bool      `json:"req_truncated,omitempty"`
	Status         int       `json:"status"`
	RespHeaders    Header    `json:"resp_headers,omitempty"`
	RespBody       []byte    `json:"resp_body,omitempty"`
	RespTruncated  bool      `json:"resp_truncated,omitempty"`
	Error          string    `json:"error,omitempty"`

	startedMono time.Time
}

// Store is a bounded ring buffer of Exchanges with live subscribers.
type Store struct {
	capacity int

	mu          sync.RWMutex
	ring        []*Exchange
	next        int
	count       int
	byID        map[uint64]*Exchange
	subscribers map[int]chan *Exchange
	subSeq      int

	nextID atomic.Uint64
}

func NewStore(capacity int) *Store {
	if capacity < 1 {
		capacity = 1
	}
	return &Store{
		capacity:    capacity,
		ring:        make([]*Exchange, capacity),
		byID:        make(map[uint64]*Exchange, capacity),
		subscribers: map[int]chan *Exchange{},
	}
}

// Begin creates a new Exchange seeded from the incoming request. Body is not
// read here; the proxy fills ReqBody via SetReqBody before Finish.
func (s *Store) Begin(scheme string, r *http.Request) *Exchange {
	ex := &Exchange{
		ID:          s.nextID.Add(1),
		StartedAt:   time.Now().UTC(),
		startedMono: time.Now(),
		Scheme:      scheme,
		Method:      r.Method,
		Host:        r.Host,
		Path:        r.URL.RequestURI(),
		ReqHeaders:  flattenHeader(r.Header),
	}
	return ex
}

// SetReqBody records the request body, honoring cap.
func (s *Store) SetReqBody(ex *Exchange, body []byte, cap int64) {
	ex.ReqBody, ex.ReqTruncated = truncate(body, cap)
}

// SetResponse records response fields, honoring cap on the body.
func (s *Store) SetResponse(ex *Exchange, resp *http.Response, body []byte, cap int64) {
	ex.Status = resp.StatusCode
	ex.RespHeaders = flattenHeader(resp.Header)
	ex.RespBody, ex.RespTruncated = truncate(body, cap)
}

// SetError stores a transport-level error message on the exchange.
func (s *Store) SetError(ex *Exchange, err error) {
	if err != nil {
		ex.Error = err.Error()
	}
}

// Finish finalizes the exchange, adds it to the ring, and fans out to subscribers.
func (s *Store) Finish(ex *Exchange) {
	ex.DurationMS = time.Since(ex.startedMono).Milliseconds()

	s.mu.Lock()
	if old := s.ring[s.next]; old != nil {
		delete(s.byID, old.ID)
	}
	s.ring[s.next] = ex
	s.byID[ex.ID] = ex
	s.next = (s.next + 1) % s.capacity
	if s.count < s.capacity {
		s.count++
	}
	subs := make([]chan *Exchange, 0, len(s.subscribers))
	for _, ch := range s.subscribers {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ex:
		default:
			// Slow subscriber: drop rather than block the proxy path.
		}
	}
}

// List returns up to limit newest Exchanges, newest first.
func (s *Store) List(limit int) []*Exchange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > s.count {
		limit = s.count
	}
	out := make([]*Exchange, 0, limit)
	idx := s.next - 1
	if idx < 0 {
		idx += s.capacity
	}
	for i := 0; i < limit; i++ {
		if s.ring[idx] == nil {
			break
		}
		out = append(out, s.ring[idx])
		idx--
		if idx < 0 {
			idx += s.capacity
		}
	}
	return out
}

func (s *Store) Get(id uint64) (*Exchange, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ex, ok := s.byID[id]
	return ex, ok
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.ring {
		s.ring[i] = nil
	}
	s.byID = make(map[uint64]*Exchange)
	s.next = 0
	s.count = 0
}

// Subscribe returns a channel receiving every Finish'd Exchange plus a cancel fn.
// Buffer is small on purpose: the proxy must not block on slow consumers.
func (s *Store) Subscribe() (<-chan *Exchange, func()) {
	ch := make(chan *Exchange, 64)
	s.mu.Lock()
	s.subSeq++
	id := s.subSeq
	s.subscribers[id] = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(ch)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}

func flattenHeader(h http.Header) Header {
	out := make(Header, 0, len(h))
	for k, vs := range h {
		for _, v := range vs {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

func truncate(b []byte, cap int64) ([]byte, bool) {
	if cap <= 0 || int64(len(b)) <= cap {
		return b, false
	}
	return b[:cap], true
}
