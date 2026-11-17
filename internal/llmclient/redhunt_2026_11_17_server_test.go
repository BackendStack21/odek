package llmclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// serverRoundTripper answers each request with the next status in statuses
// (an entry of 0 fails the request with a transport error).
type serverRoundTripper struct {
	mu       sync.Mutex
	statuses []int
	calls    int
	closed   bool
}

func (s *serverRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statuses[s.calls%len(s.statuses)]
	s.calls++
	if status == 0 {
		return nil, errors.New("dial tcp: connection refused")
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (s *serverRoundTripper) CloseIdleConnections() { s.closed = true }

func serverRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRED_AttemptTransportReportsEachRetryOfATrackedCall(t *testing.T) {
	base := &serverRoundTripper{statuses: []int{503, 0, 200}}
	rt := attemptTransport{base: base}
	var retries []Retry
	ctx, attempts := TrackAttempts(context.Background(), "fixture", func(r Retry) { retries = append(retries, r) })
	for range 3 {
		if resp, err := rt.RoundTrip(serverRequest(t, ctx)); err == nil {
			resp.Body.Close()
		}
	}
	if attempts.Count() != 3 {
		t.Fatalf("Count = %d, want 3", attempts.Count())
	}
	want := []Retry{{Attempt: 2, MaxAttempts: MaxModelAttempts, Status: 503}, {Attempt: 3, MaxAttempts: MaxModelAttempts, Status: 0}}
	if fmt.Sprint(retries) != fmt.Sprint(want) {
		t.Fatalf("retries = %+v, want %+v", retries, want)
	}
	if got := retries[0].Detail(); got != "HTTP 503, attempt 2 of 8" {
		t.Fatalf("Detail = %q", got)
	}
	if got := retries[1].Detail(); got != "no response, attempt 3 of 8" {
		t.Fatalf("Detail = %q", got)
	}
}

func TestRED_AttemptTransportIgnoresUntrackedCallsAndRedirects(t *testing.T) {
	base := &serverRoundTripper{statuses: []int{500}}
	rt := attemptTransport{base: base}
	resp, err := rt.RoundTrip(serverRequest(t, context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var retries []Retry
	ctx, attempts := TrackAttempts(context.Background(), "fixture", func(r Retry) { retries = append(retries, r) })
	if resp, err := rt.RoundTrip(serverRequest(t, ctx)); err == nil {
		resp.Body.Close()
	}
	follow := serverRequest(t, ctx)
	follow.Response = &http.Response{StatusCode: http.StatusTemporaryRedirect}
	if resp, err := rt.RoundTrip(follow); err == nil {
		resp.Body.Close()
	}
	if attempts.Count() != 1 || len(retries) != 0 {
		t.Fatalf("a redirect follow-up was counted as a retry: count=%d retries=%+v", attempts.Count(), retries)
	}
	if base.calls != 3 {
		t.Fatalf("base transport saw %d requests, want 3", base.calls)
	}
	rt.CloseIdleConnections()
	if !base.closed {
		t.Fatal("CloseIdleConnections was not forwarded")
	}
}

func TestRED_AttemptsWrapKeepsTheErrorIntact(t *testing.T) {
	var none *Attempts
	api := &sdk.APIError{Provider: "fixture", Status: 500, Message: "boom", Retryable: true}
	if got := none.Wrap(api); got != error(api) {
		t.Fatalf("nil counter changed the error: %v", got)
	}
	_, idle := TrackAttempts(context.Background(), "fixture", nil)
	if got := idle.Wrap(api); got != error(api) {
		t.Fatalf("a call that never reached the transport changed the error: %v", got)
	}
	ctx, attempts := TrackAttempts(context.Background(), "fixture", nil)
	if resp, err := (attemptTransport{base: &serverRoundTripper{statuses: []int{500}}}).RoundTrip(serverRequest(t, ctx)); err == nil {
		resp.Body.Close()
	}
	if attempts.Wrap(nil) != nil {
		t.Fatal("Wrap(nil) must stay nil")
	}
	wrapped := fmt.Errorf("iteration 0: %w", attempts.Wrap(fmt.Errorf("%w (%w)", api, context.DeadlineExceeded)))
	var gotAPI *sdk.APIError
	if !errors.As(wrapped, &gotAPI) || gotAPI.Status != 500 || !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatalf("errors.Is/As lost the cause: %v", wrapped)
	}
	if wrapped.Error() != "iteration 0: llm: fixture: HTTP 500: boom [retryable] (context deadline exceeded)" {
		t.Fatalf("raw text changed: %q", wrapped.Error())
	}
	var call *AttemptsError
	if !errors.As(wrapped, &call) || call.Attempts != 1 || call.Provider != "fixture" {
		t.Fatalf("AttemptsError = %+v", call)
	}
}

func TestRED_DescribeFailurePhrasesProviderFailures(t *testing.T) {
	refused := fmt.Errorf("llm: retry exhausted (8 attempts): %w", &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}})
	server500 := &sdk.APIError{Provider: "fixture", Status: 500, Message: "stub upstream failure: the provider is unavailable", Retryable: true}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"exhausted server error": {
			fmt.Errorf("iteration 0: %w", &AttemptsError{Provider: "fixture", Attempts: 8, Err: server500}),
			"provider fixture returned HTTP 500: stub upstream failure: the provider is unavailable (retries exhausted after 8 attempts)",
		},
		"rejected request, one attempt": {
			fmt.Errorf("iteration 2: %w", &AttemptsError{Provider: "openai", Attempts: 1, Err: &sdk.APIError{Provider: "openai", Status: 401, Message: "invalid api key"}}),
			"provider openai returned HTTP 401: invalid api key",
		},
		"retried then rejected": {
			&AttemptsError{Provider: "fixture", Attempts: 2, Err: &sdk.APIError{Provider: "fixture", Status: 400, Message: "bad input"}},
			"provider fixture returned HTTP 400: bad input (after 2 attempts)",
		},
		"retries cut by the request time limit": {
			&AttemptsError{Provider: "fixture", Attempts: 3, Err: fmt.Errorf("%w (%w)", server500, context.DeadlineExceeded)},
			"provider fixture returned HTTP 500: stub upstream failure: the provider is unavailable (stopped retrying after 3 attempts: request time limit reached)",
		},
		"rate limited": {
			&sdk.RateLimitError{APIError: sdk.APIError{Provider: "gw", Status: 429, Message: "slow down"}, Attempts: 8},
			"provider gw returned HTTP 429: slow down (rate limited; retries exhausted after 8 attempts)",
		},
		"no provider message, provider from the call": {
			&AttemptsError{Provider: "local", Attempts: 1, Err: &sdk.APIError{Status: 502}},
			"provider local returned HTTP 502",
		},
		"unreachable provider": {
			fmt.Errorf("iteration 0: %w", &AttemptsError{Provider: "local", Attempts: 8, Err: refused}),
			"could not reach provider local: connection refused (retries exhausted after 8 attempts)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := DescribeFailure(tc.err)
			if !ok || got != tc.want {
				t.Fatalf("DescribeFailure = %q, %v\nwant %q", got, ok, tc.want)
			}
		})
	}
}

func TestRED_DescribeFailureLeavesOtherErrorsToTheCaller(t *testing.T) {
	for name, err := range map[string]error{
		"nil":                     nil,
		"local error":             errors.New("persist session: disk full"),
		"cancelled":               &AttemptsError{Provider: "p", Attempts: 2, Err: fmt.Errorf("%w (%w)", &sdk.APIError{Provider: "p", Status: 500, Retryable: true}, context.Canceled)},
		"timeout, no reply":       &AttemptsError{Provider: "p", Attempts: 1, Err: context.DeadlineExceeded},
		"network error, no call":  &url.Error{Op: "Post", URL: "http://x", Err: syscall.ECONNREFUSED},
		"unclassified call error": &AttemptsError{Provider: "p", Attempts: 1, Err: errors.New("llm: stream idle timeout")},
	} {
		if got, ok := DescribeFailure(err); ok {
			t.Errorf("%s: DescribeFailure = %q, want no description", name, got)
		}
	}
}

func TestRED_DescribeFailureTreatsTheProviderMessageAsUntrusted(t *testing.T) {
	bidi := string(rune(0x202e))
	err := &sdk.APIError{Provider: "p", Status: 400,
		Message: "key sk-proj-abcdefghijklmnopqrstuvwxyz123456 rejected " + bidi + "evil\nSECOND LINE " + strings.Repeat("x", 500)}
	got, ok := DescribeFailure(err)
	if !ok {
		t.Fatal("no description")
	}
	for _, bad := range []string{"sk-proj-abcdefghijklmnopqrstuvwxyz123456", bidi, "SECOND LINE", "\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("description %q contains %q", got, bad)
		}
	}
	if escaped := "\\" + "u202e"; !strings.Contains(got, escaped) {
		t.Errorf("bidi override not shown escaped: %q", got)
	}
	long := &sdk.APIError{Provider: "p", Status: 400, Message: strings.Repeat("y", 1000)}
	if got, _ := DescribeFailure(long); len([]rune(got)) > maxFailureMessageRunes+64 || !strings.HasSuffix(got, "…") {
		t.Errorf("long provider message not shortened: %d runes", len([]rune(got)))
	}
}

// NewSDK installs the counting transport: a tracked call through the real SDK
// reports the retry the SDK makes after a 503.
func TestRED_NewSDKCountsSDKRetries(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"busy"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	c, err := Dial("", "m", "k", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var retries []Retry
	ctx, attempts := TrackAttempts(context.Background(), c.ProviderID(), func(r Retry) { retries = append(retries, r) })
	if _, err := c.Call(ctx, nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if attempts.Count() != 2 || len(retries) != 1 || retries[0].Status != 503 || retries[0].Attempt != 2 {
		t.Fatalf("count=%d retries=%+v", attempts.Count(), retries)
	}
}
