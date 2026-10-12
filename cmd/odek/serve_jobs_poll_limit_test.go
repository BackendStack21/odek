package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The WebUI polls /api/jobs every 3s per tab. Several tabs on a deleted
// session must not spend the shared session-lookup budget that real session
// detail and token-bootstrap lookups depend on.
func TestJobsPollingRejectsDoNotStarveSessionLookups(t *testing.T) {
	store := newTestSessionStore(t)
	sessionLookupLimiter.reset()
	jobsLookupLimiter.reset()
	t.Cleanup(sessionLookupLimiter.reset)
	t.Cleanup(jobsLookupLimiter.reset)

	const ip = "192.0.2.9:5555"
	deleted := fmt.Sprintf("20260101-%032x", 7)
	// 5 tabs, one poll per 3s for a minute.
	for tab := 0; tab < 5; tab++ {
		for poll := 0; poll < 20; poll++ {
			req := httptest.NewRequest(http.MethodGet, "/api/jobs?session_id="+deleted, nil)
			req.RemoteAddr = ip
			rec := httptest.NewRecorder()
			handleJobsList(store, nil)(rec, req)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fmt.Sprintf("20260101-%032x", 8), nil)
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("session lookup was rate limited by jobs polling")
	}
}
