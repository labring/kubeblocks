package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofHandler(t *testing.T) {
	handler := newPprofHandler()
	for _, path := range []string{
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/goroutine",
		"/debug/pprof/heap",
		"/debug/pprof/mutex",
		"/debug/pprof/symbol",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: got status %d, want %d", path, rr.Code, http.StatusOK)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /metrics on the pprof mux: got status %d, want %d", rr.Code, http.StatusNotFound)
	}
}
