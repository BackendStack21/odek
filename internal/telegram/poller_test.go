package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestNewPoller_defaults
// ---------------------------------------------------------------------------

func TestNewPoller_defaults(t *testing.T) {
	bot := &Bot{Token: "test:token", log: NewNopLogger()}
	p := NewPoller(bot)

	if p.Bot != bot {
		t.Errorf("NewPoller().Bot = %p, want %p", p.Bot, bot)
	}
	if p.Offset != 0 {
		t.Errorf("NewPoller().Offset = %d, want 0", p.Offset)
	}
	if p.Interval != 1*time.Second {
		t.Errorf("NewPoller().Interval = %v, want 1s", p.Interval)
	}
	if p.Timeout != 30 {
		t.Errorf("NewPoller().Timeout = %d, want 30", p.Timeout)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_withUpdates – offset advances past max update ID
// ---------------------------------------------------------------------------

func TestPoller_Poll_withUpdates(t *testing.T) {
	// Start a test server that returns two updates.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request method and path.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/bottest:token/getUpdates" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Decode the request body to verify offset/timeout are passed through.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		offset, _ := body["offset"].(float64)
		timeout, _ := body["timeout"].(float64)

		if offset != 0 {
			t.Errorf("expected offset=0, got %v", offset)
		}
		if timeout != 0 {
			t.Errorf("expected timeout=0 (overridden for test), got %v", timeout)
		}

		// Return two updates with IDs 42 and 100.
		resp := GetUpdatesResponse{
			OK: true,
			Result: []Update{
				{ID: 42},
				{ID: 100},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	// Override Timeout to 0 so the test doesn't actually wait.
	p.Timeout = 0

	ctx := context.Background()
	updates, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll(ctx) returned error: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[0].ID != 42 {
		t.Errorf("updates[0].ID = %d, want 42", updates[0].ID)
	}
	if updates[1].ID != 100 {
		t.Errorf("updates[1].ID = %d, want 100", updates[1].ID)
	}

	// Verify offset advanced past the max ID (100 → 101).
	if p.Offset != 101 {
		t.Errorf("p.Offset = %d, want 101 (maxID 100 + 1)", p.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_emptyResponse – timeout case, no updates
// ---------------------------------------------------------------------------

func TestPoller_Poll_emptyResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GetUpdatesResponse{
			OK:     true,
			Result: []Update{},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	p.Timeout = 0

	ctx := context.Background()
	updates, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll(ctx) returned error: %v", err)
	}

	if len(updates) != 0 {
		t.Fatalf("expected 0 updates (empty/timeout response), got %d", len(updates))
	}

	// Offset should remain unchanged on empty result.
	if p.Offset != 0 {
		t.Errorf("p.Offset = %d, want 0 (unchanged on empty response)", p.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_contextCancelled – returns ctx.Err()
// ---------------------------------------------------------------------------

func TestPoller_Poll_contextCancelled(t *testing.T) {
	bot := &Bot{
		Token:   "test:token",
		BaseURL: "http://127.0.0.1:1", // will not be reached
		Client:  &http.Client{Timeout: 5 * time.Second},
	}

	p := NewPoller(bot)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	updates, err := p.Poll(ctx)
	if err == nil {
		t.Fatal("expected error due to cancelled context, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if updates != nil {
		t.Errorf("expected nil updates on cancelled context, got %v", updates)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_contextDeadlineExceeded – returns ctx.Err()
// ---------------------------------------------------------------------------

func TestPoller_Poll_contextDeadlineExceeded(t *testing.T) {
	bot := &Bot{
		Token:   "test:token",
		BaseURL: "http://127.0.0.1:1", // will not be reached
		Client:  &http.Client{Timeout: 5 * time.Second},
	}

	p := NewPoller(bot)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Hour))
	defer cancel()

	updates, err := p.Poll(ctx)
	if err == nil {
		t.Fatal("expected error due to deadline exceeded, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if updates != nil {
		t.Errorf("expected nil updates on deadline exceeded, got %v", updates)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_offsetAdvanceInSequence – multiple polls advance offset
// ---------------------------------------------------------------------------

func TestPoller_Poll_offsetAdvanceInSequence(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		offset, _ := body["offset"].(float64)

		var resp GetUpdatesResponse
		resp.OK = true

		switch callCount {
		case 1:
			if offset != 0 {
				t.Errorf("call %d: expected offset=0, got %v", callCount, offset)
			}
			resp.Result = []Update{{ID: 5}, {ID: 10}}
		case 2:
			if offset != 11 {
				t.Errorf("call %d: expected offset=11, got %v", callCount, offset)
			}
			resp.Result = []Update{{ID: 20}}
		case 3:
			if offset != 21 {
				t.Errorf("call %d: expected offset=21, got %v", callCount, offset)
			}
			resp.Result = []Update{} // empty = timeout
		default:
			t.Errorf("unexpected call count: %d", callCount)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	p.Timeout = 0
	ctx := context.Background()

	// First poll: two updates (IDs 5 and 10) → offset should become 11.
	updates, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("first poll failed: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("first poll: expected 2 updates, got %d", len(updates))
	}
	if p.Offset != 11 {
		t.Fatalf("after first poll: offset = %d, want 11", p.Offset)
	}

	// Second poll: one update (ID 20) → offset should become 21.
	updates, err = p.Poll(ctx)
	if err != nil {
		t.Fatalf("second poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("second poll: expected 1 update, got %d", len(updates))
	}
	if p.Offset != 21 {
		t.Fatalf("after second poll: offset = %d, want 21", p.Offset)
	}

	// Third poll: empty response → offset should stay at 21.
	updates, err = p.Poll(ctx)
	if err != nil {
		t.Fatalf("third poll failed: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("third poll: expected 0 updates, got %d", len(updates))
	}
	if p.Offset != 21 {
		t.Fatalf("after third poll (empty): offset = %d, want 21 (unchanged)", p.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_updatesOutOfOrder – offset uses max ID
// ---------------------------------------------------------------------------

func TestPoller_Poll_updatesOutOfOrder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GetUpdatesResponse{
			OK: true,
			Result: []Update{
				{ID: 200},
				{ID: 5},
				{ID: 150},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	p.Timeout = 0

	ctx := context.Background()
	updates, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll(ctx) returned error: %v", err)
	}

	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(updates))
	}

	// Max ID is 200 → offset should be 201.
	if p.Offset != 201 {
		t.Errorf("p.Offset = %d, want 201 (maxID 200 + 1)", p.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_singleUpdate – offset advances correctly for one update
// ---------------------------------------------------------------------------

func TestPoller_Poll_singleUpdate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GetUpdatesResponse{
			OK: true,
			Result: []Update{
				{ID: 99},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	p.Timeout = 0

	ctx := context.Background()
	updates, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll(ctx) returned error: %v", err)
	}

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if p.Offset != 100 {
		t.Errorf("p.Offset = %d, want 100 (99 + 1)", p.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestPoller_Poll_serverError – error propagated from GetUpdates
// ---------------------------------------------------------------------------

func TestPoller_Poll_serverError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Send a raw JSON response that the Bot.doJSON method will parse.
		// doJSON checks a generic apiResp struct with OK, Result, Description, ErrorCode.
		raw := map[string]any{
			"ok":          false,
			"description": "Conflict: terminated by other getUpdates request",
			"error_code":  409,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		if err := json.NewEncoder(w).Encode(raw); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	bot := &Bot{
		Token:   "test:token",
		BaseURL: ts.URL + "/bottest:token",
		Client:  ts.Client(),
		log:     NewNopLogger(),
	}

	p := NewPoller(bot)
	p.Timeout = 0

	ctx := context.Background()
	_, err := p.Poll(ctx)
	if err == nil {
		t.Fatal("expected error from server, got nil")
	}
	// Verify offset was NOT advanced on error.
	if p.Offset != 0 {
		t.Errorf("offset should remain 0 on error, got %d", p.Offset)
	}
}
