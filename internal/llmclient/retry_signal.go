package llmclient

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// MaxModelAttempts is the attempt budget of one model call: the first attempt
// plus seven retries, the SDK's default ladder. NewSDK sets it explicitly so
// the attempt numbers reported while a call is retried can name the budget.
const MaxModelAttempts = 8

// Retry describes a model call whose next HTTP attempt is starting after a
// failed one.
type Retry struct {
	Attempt     int // 1-based number of the attempt that is starting (2 or more)
	MaxAttempts int // the call's attempt budget
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
// it to the call's context; the SDK transport installed by NewSDK counts every
// request made under that context, so retries the SDK makes on its own (5xx,
// 408 and 429 replies, network errors, learn-once fallbacks) are seen as they
// start. Calls without a tracker are not observed.
type Attempts struct {
	provider string
	onRetry  func(Retry)

	mu         sync.Mutex
	n          int
	lastStatus int
}

type attemptsKey struct{}

// TrackAttempts returns ctx carrying a new attempt counter for one call to
// provider. onRetry, when set, runs on the request goroutine as each attempt
// after the first starts; it must not block.
func TrackAttempts(ctx context.Context, provider string, onRetry func(Retry)) (context.Context, *Attempts) {
	a := &Attempts{provider: provider, onRetry: onRetry}
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
	n := a.Count()
	if err == nil || n == 0 {
		return err
	}
	return &AttemptsError{Provider: a.provider, Attempts: n, Err: err}
}

func (a *Attempts) begin() {
	a.mu.Lock()
	a.n++
	r := Retry{Attempt: a.n, MaxAttempts: MaxModelAttempts, Status: a.lastStatus}
	a.mu.Unlock()
	if r.Attempt > 1 && a.onRetry != nil {
		a.onRetry(r)
	}
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
	Err      error
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
	a.begin()
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
