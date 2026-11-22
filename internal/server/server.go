// Package server exposes the spatial index over HTTP: a JSON API for queries
// and index structure, and a single-page visualisation that draws the tree's
// node rectangles by level, per-level overlap, and the nodes one query visits.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"ancienttrees/internal/app"
)

// Server holds the live dataset/index state shared by all requests.
type Server struct {
	World *World
}

// World is the mutable application state. Read handlers take RLock, the
// insert/delete handlers take Lock, so concurrent queries never observe a
// half-applied mutation.
type World struct {
	mu    sync.RWMutex
	Data  *app.Dataset
	Store *app.Store
	Opts  app.IndexOptions
	Seed  int64
}

// Handler returns the application's HTTP handler.
func Handler(w *World) http.Handler {
	s := &Server{World: w}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.indexPage)
	mux.HandleFunc("/api/stats", w.read(s.apiStats))
	mux.HandleFunc("/api/trees", w.read(s.apiTrees))
	mux.HandleFunc("/api/nodes", w.read(s.apiNodes))
	mux.HandleFunc("/api/overlap", w.read(s.apiOverlap))
	mux.HandleFunc("/api/query", w.read(s.apiQuery))
	mux.HandleFunc("/api/insert", w.write(s.apiInsert))
	mux.HandleFunc("/api/delete", w.write(s.apiDelete))
	return mux
}

// read wraps a read-only handler in the world's read lock.
func (w *World) read(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.mu.RLock()
		defer w.mu.RUnlock()
		h(rw, r)
	}
}

// write wraps a mutating handler in the world's write lock.
func (w *World) write(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		h(rw, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func floatParam(r *http.Request, name string, def float64) float64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}
