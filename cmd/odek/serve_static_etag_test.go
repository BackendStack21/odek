package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRED_Serve_StaticETagComputedOncePerVariant(t *testing.T) {
	h := handleStatic("tok-123")
	before := staticETagComputes.Load()
	var etag string
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if i == 0 {
			etag = rr.Header().Get("ETag")
		} else if rr.Header().Get("ETag") != etag {
			t.Fatal("etag changed between identical requests")
		}
		rr = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", etag)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotModified {
			t.Fatalf("conditional request got %d", rr.Code)
		}
	}
	if got := staticETagComputes.Load() - before; got != 1 {
		t.Fatalf("etag computed %d times for one variant", got)
	}
	// The token-bearing variant has different bytes, hence its own etag.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/?token=tok-123", nil))
	if rr.Header().Get("ETag") == etag {
		t.Fatal("token variant must not share the anonymous etag")
	}
}
