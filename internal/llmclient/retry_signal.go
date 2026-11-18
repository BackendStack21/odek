package llmclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// MaxModelAttempts is the SDK's retry ladder for one model call: the first
// attempt plus seven retries, its default. NewSDK sets it explicitly so the
// attempt numbers reported while a call is retried can name the budget, which
// a learn-once fallback lowers (see Attempts).
const MaxModelAttempts = 8

// Retry describes a model call whose next HTTP attempt is starting after a
// failed one.
type Retry struct {
	Attempt     int // 1-based number of the attempt that is starting (2 or more)
	MaxAttempts int // the call's attempt budget (see Attempts)
	Status      int // HTTP status of the failed attempt; 0 when it got no response
}

// Detail renders r for a status line, e.g. "HTTP 503, attempt 2 of 8".
func (r Retry) Detail() string {
	attempt := fmt.Sprintf("attempt %d", r.Attempt)
	if r.MaxAttempts >= r.Attempt {
		attempt += fmt.Sprintf(" of %d", r.MaxAttempts)
	}
	switch {
	case r.Status >= 400:
		return fmt.Sprintf("HTTP %d, %s", r.Status, attempt)
	case r.Status == 0:
		return "no response, " + attempt
	}
	return attempt
}

// Attempts counts the HTTP attempts of one model call. TrackAttempts attaches
// it to the call's context; the SDK transport installed by NewSDK sees every
// request made under that context, so retries the SDK makes on its own (5xx,
// 408 and 429 replies, network errors) are seen as they start. A learn-once
// fallback (the request resent at once after a provider 400 named a part it
// rejects) is not a retry and is not counted. Calls without a tracker are
// not observed.
//
// The budget is how many attempts the call can make. The SDK's ladder has
// MaxModelAttempts requests and a fallback resend takes one of them, so each
// fallback lowers the budget by one. When the SDK gives up streaming and
// resends the call as buffered requests, the buffered requests run a ladder
// of their own after the attempts made so far; the switch shows as a request
// that no longer asks for an event stream after one that did.
type Attempts struct {
	provider string
	onRetry  func(Retry)

	mu         sync.Mutex
	n          int
	budget     int
	lastStatus int
	lastStream bool
}

type attemptsKey struct{}

// TrackAttempts returns ctx carrying a new attempt counter for one call to
// provider. onRetry, when set, runs on the request goroutine as each attempt
// after the first starts; it must not block.
func TrackAttempts(ctx context.Context, provider string, onRetry func(Retry)) (context.Context, *Attempts) {
	a := &Attempts{provider: provider, onRetry: onRetry, budget: MaxModelAttempts}
	return context.WithValue(ctx, attemptsKey{}, a), a
}

// Count reports how many HTTP attempts the call has started.
func (a *Attempts) Count() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// Wrap annotates a failed call's error with the provider and the number of
// attempts made (see AttemptsError). A nil error, a nil counter or a call
// that never reached the transport returns err unchanged.
func (a *Attempts) Wrap(err error) error {
	if a == nil || err == nil {
		return err
	}
	a.mu.Lock()
	n, budget := a.n, a.budget
	a.mu.Unlock()
	if n == 0 {
		return err
	}
	return &AttemptsError{Provider: a.provider, Attempts: n, Budget: budget, Err: err}
}

func (a *Attempts) begin(req *http.Request) {
	stream := strings.Contains(req.Header.Get("Accept"), "text/event-stream")
	a.mu.Lock()
	if a.n > 0 && a.lastStream && !stream {
		// The SDK resends the call as buffered requests: a new ladder.
		a.budget = a.n + MaxModelAttempts
	}
	a.lastStream = stream
	if a.n > 0 && learnOnceStatus(a.lastStatus) {
		// The SDK's learn-once fallback resending the request at once with
		// the rejected part changed: not a retry, as reportAttempt also
		// treats it, so it is neither counted nor signalled, but it used a
		// request of the ladder.
		a.budget--
		a.mu.Unlock()
		return
	}
	a.n++
	r := Retry{Attempt: a.n, MaxAttempts: a.budget, Status: a.lastStatus}
	a.mu.Unlock()
	if r.Attempt > 1 && a.onRetry != nil {
		a.onRetry(r)
	}
}

// learnOnceStatus reports whether a request that follows a reply with this
// status, within one call, is one of the SDK's learn-once fallbacks rather
// than a retry. The SDK backs off and retries transport errors, 408, 429 and
// 5xx replies and streams that failed before any output; it never retries
// another 4xx after a delay. Its learn-once fallbacks (stream_options,
// reasoning_effort, the Responses API, buffered mode) are all triggered by a
// provider 400 and resend at once. So a request after a 4xx other than 408
// and 429 is such a fallback.
func learnOnceStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

func (a *Attempts) end(resp *http.Response, err error) {
	status := 0
	if err == nil && resp != nil {
		status = resp.StatusCode
	}
	a.mu.Lock()
	a.lastStatus = status
	a.mu.Unlock()
}

// AttemptsError is a failed model call annotated with its provider and the
// number of HTTP attempts it made. Its text is the wrapped error's, so logs,
// events and CLI output are unchanged, and errors.Is/As see through it.
type AttemptsError struct {
	Provider string
	Attempts int
	// Budget is the number of attempts the call could make (see Attempts);
	// 0 means MaxModelAttempts.
	Budget int
	Err    error
}

func (e *AttemptsError) Error() string { return e.Err.Error() }
func (e *AttemptsError) Unwrap() error { return e.Err }

// attemptTransport counts the requests made under a context that carries an
// Attempts counter. A request created to follow a redirect belongs to the
// attempt that received the redirect and is not counted.
type attemptTransport struct{ base http.RoundTripper }

func (t attemptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	a, _ := req.Context().Value(attemptsKey{}).(*Attempts)
	if a == nil || req.Response != nil {
		return t.base.RoundTrip(req)
	}
	a.begin(req)
	resp, err := t.base.RoundTrip(req)
	a.end(resp, err)
	return resp, err
}

// CloseIdleConnections forwards to the wrapped transport, so
// http.Client.CloseIdleConnections keeps working.
func (t attemptTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
