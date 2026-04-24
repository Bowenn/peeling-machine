package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

// ruleFile is the on-disk JSON schema. See docs/proxies.md for details.
type ruleFile struct {
	Rules []ruleEntry `json:"rules"`
}

type ruleEntry struct {
	Match ruleMatch `json:"match"`
	Via   string    `json:"via"`
}

type ruleMatch struct {
	Host string `json:"host"`
}

// Rule is one compiled entry in a Ruleset. rt is resolved from `via` at load
// time so each request only costs a pattern match, not a transport rebuild.
type Rule struct {
	HostGlob string
	Via      string
	rt       RoundTripper
}

// Ruleset dispatches each request to the first matching rule's RoundTripper,
// falling through to Default when nothing matches. Default is Direct() unless
// a rule explicitly matches "*" and routes everything.
type Ruleset struct {
	Rules   []Rule
	Default RoundTripper
}

// RoundTrip implements the RoundTripper interface by host-glob dispatch.
func (rs *Ruleset) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	for i := range rs.Rules {
		if hostGlobMatch(rs.Rules[i].HostGlob, host) {
			return rs.Rules[i].rt.RoundTrip(r)
		}
	}
	return rs.Default.RoundTrip(r)
}

// hostGlobMatch matches a hostname against a pattern. "*" matches anything,
// "*.example.com" matches "foo.example.com" (but not "example.com" itself —
// the "*." requires at least one sub-label), and exact hostnames match
// themselves. Matching is case-insensitive.
func hostGlobMatch(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" {
		return false
	}
	// path.Match treats '*' as matching any run of non-'/' characters, which
	// is exactly the glob semantics we want for hostnames (dots included).
	ok, err := path.Match(pattern, host)
	if err != nil {
		return false
	}
	return ok
}

// LoadRuleset reads and compiles a rules JSON file. Any syntax error or
// unsupported `via` scheme fails the whole load — partial rulesets are never
// applied, so a broken edit can't silently route traffic the wrong way.
func LoadRuleset(path string) (*Ruleset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules: %w", err)
	}
	return parseRuleset(raw)
}

func parseRuleset(raw []byte) (*Ruleset, error) {
	var file ruleFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	rs := &Ruleset{Default: Direct()}
	for i, entry := range file.Rules {
		if strings.TrimSpace(entry.Match.Host) == "" {
			return nil, fmt.Errorf("rule %d: match.host is required", i)
		}
		if _, err := path.Match(entry.Match.Host, "probe"); err != nil {
			return nil, fmt.Errorf("rule %d: invalid host glob %q: %w", i, entry.Match.Host, err)
		}
		rt, err := resolveVia(entry.Via)
		if err != nil {
			return nil, fmt.Errorf("rule %d: via %q: %w", i, entry.Via, err)
		}
		rs.Rules = append(rs.Rules, Rule{HostGlob: entry.Match.Host, Via: entry.Via, rt: rt})
	}
	return rs, nil
}

// resolveVia turns a `via` string into a RoundTripper. Recognises "direct",
// http(s)://, and socks5[h]:// schemes.
func resolveVia(via string) (RoundTripper, error) {
	via = strings.TrimSpace(via)
	if via == "" {
		return nil, errors.New("via is empty")
	}
	switch {
	case strings.EqualFold(via, "direct"):
		return Direct(), nil
	case strings.HasPrefix(via, "http://"), strings.HasPrefix(via, "https://"):
		return HTTP(via)
	case strings.HasPrefix(via, "socks5://"), strings.HasPrefix(via, "socks5h://"):
		return SOCKS5(via)
	default:
		return nil, fmt.Errorf("unsupported via scheme in %q (want direct, http[s]://, or socks5[h]://)", via)
	}
}

// WatchedRules wraps a Ruleset with a poll-based file watcher. The active
// ruleset is swapped atomically on successful reload; a failed reload logs
// and keeps the previous ruleset so a broken edit can't blackhole traffic.
type WatchedRules struct {
	cur      atomic.Pointer[Ruleset]
	path     string
	interval time.Duration
	log      *slog.Logger

	stop chan struct{}

	// mtime of the file at the last successful load. Compared on each poll
	// tick so we only re-read on actual changes.
	lastMod atomic.Int64
}

// RulesFromFile performs the initial load (fail-fast on error) and starts
// the background watcher goroutine. The caller keeps the returned value for
// the life of the process; Stop ends the watcher cleanly.
func RulesFromFile(filePath string, log *slog.Logger) (*WatchedRules, error) {
	if log == nil {
		log = slog.Default()
	}
	rs, err := LoadRuleset(filePath)
	if err != nil {
		return nil, err
	}
	w := &WatchedRules{
		path:     filePath,
		interval: 2 * time.Second,
		log:      log,
		stop:     make(chan struct{}),
	}
	w.cur.Store(rs)
	if info, err := os.Stat(filePath); err == nil {
		w.lastMod.Store(info.ModTime().UnixNano())
	}
	go w.watch()
	return w, nil
}

// RoundTrip delegates to the currently-active ruleset.
func (w *WatchedRules) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.cur.Load().RoundTrip(r)
}

// Current returns the ruleset that would handle the next request. Primarily
// useful in tests.
func (w *WatchedRules) Current() *Ruleset { return w.cur.Load() }

// Reload forces a re-read of the file outside the normal poll cycle. Used by
// tests; the watcher goroutine calls it internally on mtime changes.
func (w *WatchedRules) Reload() error {
	rs, err := LoadRuleset(w.path)
	if err != nil {
		return err
	}
	w.cur.Store(rs)
	if info, err := os.Stat(w.path); err == nil {
		w.lastMod.Store(info.ModTime().UnixNano())
	}
	return nil
}

// Stop ends the watcher goroutine. The active ruleset stays usable afterwards.
func (w *WatchedRules) Stop() {
	select {
	case <-w.stop:
		// already stopped
	default:
		close(w.stop)
	}
}

func (w *WatchedRules) watch() {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.checkAndReload()
		}
	}
}

func (w *WatchedRules) checkAndReload() {
	info, err := os.Stat(w.path)
	if err != nil {
		// The file was moved or removed. Don't flap — keep the current
		// ruleset and log once. A future successful stat will pick up
		// whatever replaces the file.
		w.log.Warn("proxies config stat failed, keeping current ruleset",
			"path", w.path, "err", err)
		return
	}
	mod := info.ModTime().UnixNano()
	if mod == w.lastMod.Load() {
		return
	}
	if err := w.Reload(); err != nil {
		w.log.Warn("proxies config reload failed, keeping previous ruleset",
			"path", w.path, "err", err)
		return
	}
	w.log.Info("proxies config reloaded", "path", w.path, "rules", len(w.cur.Load().Rules))
}
