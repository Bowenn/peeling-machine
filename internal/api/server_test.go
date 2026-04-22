package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bowen/peeling-machine/internal/ca"
	"github.com/bowen/peeling-machine/internal/capture"
)

func newAPI(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	c, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := capture.NewStore(100)
	s := New("", store, c)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func finishFake(store *capture.Store, method string) {
	r := httptest.NewRequest(method, "http://example.com/p", nil)
	ex := store.Begin("http", r)
	store.Finish(ex)
}

func TestListAndClear(t *testing.T) {
	s, ts := newAPI(t)
	finishFake(s.Store, "GET")
	finishFake(s.Store, "POST")

	resp, err := http.Get(ts.URL + "/api/exchanges")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Exchanges []capture.Exchange `json:"exchanges"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Exchanges) != 2 {
		t.Fatalf("want 2, got %d", len(body.Exchanges))
	}

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/exchanges", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil || dresp.StatusCode != 204 {
		t.Fatalf("clear: %v %v", err, dresp)
	}
	dresp.Body.Close()

	resp2, err := http.Get(ts.URL + "/api/exchanges")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	_ = json.NewDecoder(resp2.Body).Decode(&body)
	if len(body.Exchanges) != 0 {
		t.Fatalf("want 0 after clear, got %d", len(body.Exchanges))
	}
}

func TestCADownload(t *testing.T) {
	_, ts := newAPI(t)
	resp, err := http.Get(ts.URL + "/api/ca")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Fatalf("expected PEM cert, got %q", string(b)[:80])
	}
}

func TestStreamSSE(t *testing.T) {
	s, ts := newAPI(t)

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %s", ct)
	}

	// Give the subscriber goroutine a moment to register before we push.
	time.Sleep(50 * time.Millisecond)
	go finishFake(s.Store, "GET")

	br := bufio.NewReader(resp.Body)
	done := make(chan string, 1)
	go func() {
		var data strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				data.WriteString(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
			}
			if line == "\n" && data.Len() > 0 {
				done <- data.String()
				return
			}
		}
	}()

	select {
	case payload := <-done:
		if !strings.Contains(payload, `"method":"GET"`) {
			t.Fatalf("unexpected payload: %s", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}

func TestCORSPreflight(t *testing.T) {
	_, ts := newAPI(t)
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/exchanges", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("CORS header: %q", got)
	}
}
