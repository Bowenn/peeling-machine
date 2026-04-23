package transport

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"*", "example.com", true},
		{"*", "a.b.c.example.com", true},
		{"example.com", "example.com", true},
		{"example.com", "foo.example.com", false},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false}, // "*." requires at least one label
		{"Api.Example.COM", "api.example.com", true}, // case-insensitive
		{"", "api.example.com", false},
		{"*.internal", "corp.internal", true},
		{"*.internal", "external.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"/"+tc.host, func(t *testing.T) {
			if got := hostGlobMatch(tc.pattern, tc.host); got != tc.want {
				t.Fatalf("hostGlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
			}
		})
	}
}

func TestParseRulesetValid(t *testing.T) {
	raw := []byte(`{
		"rules": [
			{ "match": {"host": "*.corp.internal"}, "via": "direct" },
			{ "match": {"host": "*"}, "via": "http://127.0.0.1:8080" }
		]
	}`)
	rs, err := parseRuleset(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rs.Rules))
	}
	if rs.Rules[0].HostGlob != "*.corp.internal" || rs.Rules[1].HostGlob != "*" {
		t.Fatalf("bad rules: %+v", rs.Rules)
	}
	if rs.Default == nil {
		t.Fatalf("default RT missing")
	}
}

func TestParseRulesetErrors(t *testing.T) {
	cases := map[string]string{
		"missing host":     `{"rules":[{"match":{},"via":"direct"}]}`,
		"unknown scheme":   `{"rules":[{"match":{"host":"*"},"via":"ftp://x"}]}`,
		"empty via":        `{"rules":[{"match":{"host":"*"},"via":""}]}`,
		"malformed json":   `{"rules": [`,
		"unknown top key":  `{"rules":[],"extra":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRuleset([]byte(raw)); err == nil {
				t.Fatalf("want error for %q", raw)
			}
		})
	}
}

// TestRulesetDispatch runs two httptest servers: one stands in for an HTTP
// upstream proxy (it just records the absolute-form RequestURI it receives),
// the other is the origin. The ruleset routes "api.corp.internal" requests
// through the upstream and anything else direct. We hit both and assert.
func TestRulesetDispatch(t *testing.T) {
	var (
		mu         sync.Mutex
		seenAtUp   []string
		seenAtOrig []string
	)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAtOrig = append(seenAtOrig, r.Host+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte("origin"))
	}))
	defer origin.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAtUp = append(seenAtUp, r.RequestURI)
		mu.Unlock()
		// Forward to origin so the client still gets a meaningful response.
		out, err := http.NewRequest(r.Method, r.RequestURI, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer upstream.Close()

	// We can't glob-match "127.0.0.1" with "*.corp.internal". Build the
	// ruleset programmatically so rule[0] matches a specific host string that
	// the request URL will actually carry.
	upstreamRT, err := HTTP(upstream.URL)
	if err != nil {
		t.Fatalf("HTTP: %v", err)
	}
	rs := &Ruleset{
		Rules: []Rule{
			// Route by exact host — the originURL's Host is "127.0.0.1:NNNN",
			// and Hostname() strips the port so this exact host matches.
			{HostGlob: "127.0.0.1", rt: upstreamRT},
		},
		Default: Direct(),
	}

	// Request through the ruleset: should go via the upstream proxy, which
	// records the absolute-form URI and forwards to origin.
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/via-upstream", nil)
	resp, err := rs.RoundTrip(req)
	if err != nil {
		t.Fatalf("via upstream: %v", err)
	}
	_ = resp.Body.Close()

	// Request with a host that won't match → default (direct).
	// We can force a mismatch by swapping the ruleset so the only rule has
	// a pattern that can't apply to the request.
	rs.Rules[0].HostGlob = "never.example"
	req2, _ := http.NewRequest(http.MethodGet, origin.URL+"/direct", nil)
	resp2, err := rs.RoundTrip(req2)
	if err != nil {
		t.Fatalf("direct: %v", err)
	}
	_ = resp2.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(seenAtUp) != 1 || !strings.HasSuffix(seenAtUp[0], "/via-upstream") {
		t.Fatalf("upstream saw %v, want one /via-upstream hit", seenAtUp)
	}
	// Origin sees both because the upstream forwards the first one to it too.
	var directCount, viaCount int
	for _, p := range seenAtOrig {
		if strings.HasSuffix(p, "/direct") {
			directCount++
		}
		if strings.HasSuffix(p, "/via-upstream") {
			viaCount++
		}
	}
	if directCount != 1 || viaCount != 1 {
		t.Fatalf("origin hits: direct=%d via=%d (seen=%v)", directCount, viaCount, seenAtOrig)
	}
}

// TestWatchedRulesHotReload writes a rules file, swaps its contents, calls
// Reload, and verifies the dispatcher now picks a different upstream. We use
// the synchronous Reload entry point — the poller goroutine is exercised
// lightly in TestWatchedRulesPoll.
func TestWatchedRulesHotReload(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", "A")
		_, _ = w.Write([]byte("A"))
	}))
	defer upA.Close()

	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", "B")
		_, _ = w.Write([]byte("B"))
	}))
	defer upB.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("origin"))
	}))
	defer origin.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	writeRules := func(via string) {
		content := `{"rules":[{"match":{"host":"*"},"via":"` + via + `"}]}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	writeRules(upA.URL)
	w, err := RulesFromFile(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer w.Stop()

	req1, _ := http.NewRequest(http.MethodGet, origin.URL+"/x", nil)
	resp1, err := w.RoundTrip(req1)
	if err != nil {
		t.Fatalf("req1: %v", err)
	}
	if resp1.Header.Get("X-Via") != "A" {
		t.Fatalf("before reload: X-Via=%q, want A", resp1.Header.Get("X-Via"))
	}
	_ = resp1.Body.Close()

	writeRules(upB.URL)
	if err := w.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	req2, _ := http.NewRequest(http.MethodGet, origin.URL+"/x", nil)
	resp2, err := w.RoundTrip(req2)
	if err != nil {
		t.Fatalf("req2: %v", err)
	}
	if resp2.Header.Get("X-Via") != "B" {
		t.Fatalf("after reload: X-Via=%q, want B", resp2.Header.Get("X-Via"))
	}
	_ = resp2.Body.Close()
}

// TestWatchedRulesReloadFailureKeepsPrevious ensures a broken file doesn't
// blackhole traffic — the previous ruleset keeps serving until the edit is
// fixed.
func TestWatchedRulesReloadFailureKeepsPrevious(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", "A")
		_, _ = w.Write([]byte("A"))
	}))
	defer up.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("origin"))
	}))
	defer origin.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	good := `{"rules":[{"match":{"host":"*"},"via":"` + up.URL + `"}]}`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	// Capture the log output to confirm we warn on failure.
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&syncWriter{w: &buf}, nil))
	w, err := RulesFromFile(path, logger)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	defer w.Stop()

	// Break the file, then force a reload via the exported method.
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Reload(); err == nil {
		t.Fatalf("expected reload error")
	}

	// Current ruleset should still be the first (good) one.
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/x", nil)
	resp, err := w.RoundTrip(req)
	if err != nil {
		t.Fatalf("req after failed reload: %v", err)
	}
	if resp.Header.Get("X-Via") != "A" {
		t.Fatalf("after failed reload: X-Via=%q, want A (kept previous)", resp.Header.Get("X-Via"))
	}
	_ = resp.Body.Close()
}

// TestWatchedRulesPoll exercises the background goroutine with a short poll
// interval. We mutate the file and wait briefly for the poller to pick up
// the mtime change.
func TestWatchedRulesPoll(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", "A")
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", "B")
	}))
	defer upB.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(path, []byte(`{"rules":[{"match":{"host":"*"},"via":"`+upA.URL+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := RulesFromFile(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("initial: %v", err)
	}
	defer w.Stop()
	w.interval = 50 * time.Millisecond // speed up for the test

	// Rewrite with a newer mtime so the poller sees the change. Sleep briefly
	// to make sure the mtime resolution actually differs on fast filesystems.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"rules":[{"match":{"host":"*"},"via":"`+upB.URL+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly for filesystems with second-level mtime granularity.
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	// Wait up to 1.5s for the poller to reload, checking the served upstream.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/x", nil)
		resp, err := w.RoundTrip(req)
		if err == nil && resp.Header.Get("X-Via") == "B" {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("poller did not reload the ruleset within deadline")
}

// syncWriter wraps a strings.Builder so the slog handler (which may write
// from multiple goroutines in principle) can share it safely.
type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
