// Package api exposes the capture store to the GUI over HTTP: JSON endpoints
// plus a Server-Sent Events stream of new exchanges. Lives on its own port
// (default :9090) so the proxy port only speaks proxy protocol.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bowen/peeling-machine/internal/ca"
	"github.com/bowen/peeling-machine/internal/capture"
)

type Server struct {
	Addr  string
	Store *capture.Store
	CA    *ca.CA

	http *http.Server
}

func New(addr string, store *capture.Store, c *ca.CA) *Server {
	return &Server{Addr: addr, Store: store, CA: c}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/exchanges", s.listExchanges)
	mux.HandleFunc("DELETE /api/exchanges", s.clearExchanges)
	mux.HandleFunc("GET /api/exchanges/{id}", s.getExchange)
	mux.HandleFunc("GET /api/stream", s.streamExchanges)
	mux.HandleFunc("GET /api/ca", s.downloadCA)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return withCORS(mux)
}

func (s *Server) ListenAndServe() error {
	s.http = &http.Server{Addr: s.Addr, Handler: s.Handler()}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) listExchanges(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exchanges": s.Store.List(limit),
	})
}

func (s *Server) clearExchanges(w http.ResponseWriter, r *http.Request) {
	s.Store.Clear()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getExchange(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ex, ok := s.Store.Get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, ex)
}

func (s *Server) downloadCA(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="peeling-machine-ca.crt"`)
	_, _ = w.Write(s.CA.CertPEM)
}

func (s *Server) streamExchanges(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.Store.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case ex, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ex)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: exchange\ndata: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// withCORS allows any localhost / 127.0.0.1 origin so the React dev server
// on a sibling port can hit the API. Nothing sensitive is exposed here.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLocalOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	return strings.HasPrefix(origin, "http://localhost") ||
		strings.HasPrefix(origin, "http://127.0.0.1") ||
		strings.HasPrefix(origin, "https://localhost") ||
		strings.HasPrefix(origin, "https://127.0.0.1")
}
