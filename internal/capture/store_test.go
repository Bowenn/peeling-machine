package capture

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fakeReq(method, url string) *http.Request {
	r := httptest.NewRequest(method, url, nil)
	return r
}

func finish(s *Store, method, url string) *Exchange {
	ex := s.Begin("http", fakeReq(method, url))
	s.Finish(ex)
	return ex
}

func TestStoreRingWrap(t *testing.T) {
	s := NewStore(3)
	for i := 0; i < 5; i++ {
		finish(s, "GET", "http://example.com/")
	}
	got := s.List(0)
	if len(got) != 3 {
		t.Fatalf("want 3 entries after wrap, got %d", len(got))
	}
	// Newest first.
	if got[0].ID < got[2].ID {
		t.Fatalf("expected newest first: %+v", got)
	}
}

func TestStoreGetAfterEviction(t *testing.T) {
	s := NewStore(2)
	first := finish(s, "GET", "http://a/")
	finish(s, "GET", "http://b/")
	finish(s, "GET", "http://c/")
	if _, ok := s.Get(first.ID); ok {
		t.Fatalf("evicted entry should be gone from index")
	}
}

func TestSubscribeReceivesFinish(t *testing.T) {
	s := NewStore(10)
	ch, cancel := s.Subscribe()
	defer cancel()

	go finish(s, "GET", "http://example.com/")

	select {
	case ex := <-ch:
		if ex.Method != "GET" {
			t.Fatalf("wrong method: %s", ex.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber event")
	}
}

func TestSubscribeCancel(t *testing.T) {
	s := NewStore(10)
	ch, cancel := s.Subscribe()
	cancel()
	// second cancel must not panic.
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("expected closed channel after cancel")
	}
}

func TestClear(t *testing.T) {
	s := NewStore(4)
	finish(s, "GET", "http://a/")
	finish(s, "GET", "http://b/")
	s.Clear()
	if got := s.List(0); len(got) != 0 {
		t.Fatalf("clear did not empty: %d", len(got))
	}
}
