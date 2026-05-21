package telegram

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// FallbackTransport is an http.RoundTripper that tries multiple Telegram API
// endpoints in order, falling back to the next on failure.
type FallbackTransport struct {
	PrimaryURL   string
	FallbackURLs []string
	Timeout      time.Duration
	Client       *http.Client
}

// NewFallbackTransport creates a FallbackTransport with the given fallback
// URLs. The primary URL defaults to https://api.telegram.org and the timeout
// defaults to 30 seconds.
func NewFallbackTransport(fallbackURLs []string) *FallbackTransport {
	ft := &FallbackTransport{
		PrimaryURL:   "https://api.telegram.org",
		FallbackURLs: fallbackURLs,
		Timeout:      30 * time.Second,
	}
	ft.Client = &http.Client{
		Timeout:   ft.Timeout,
		Transport: ft,
	}
	return ft
}

// allURLs returns the primary URL followed by all fallback URLs in a single
// slice for iteration.
func (ft *FallbackTransport) allURLs() []string {
	urls := make([]string, 0, 1+len(ft.FallbackURLs))
	urls = append(urls, ft.PrimaryURL)
	urls = append(urls, ft.FallbackURLs...)
	return urls
}

// RoundTrip implements http.RoundTripper. It tries the request against each
// configured URL (primary first, then fallbacks) and returns the first
// successful response or a combined error.
func (ft *FallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return ft.tryURLs(req)
}

// Do tries the request against each configured URL (primary first, then
// fallbacks) and returns the first successful response or a combined error.
func (ft *FallbackTransport) Do(req *http.Request) (*http.Response, error) {
	return ft.tryURLs(req)
}

// tryURLs is the shared implementation for both RoundTrip and Do.
func (ft *FallbackTransport) tryURLs(req *http.Request) (*http.Response, error) {
	basePath := req.URL.Path
	if req.URL.RawPath != "" {
		basePath = req.URL.RawPath
	}
	// Include query parameters in the attempt.
	rawQuery := req.URL.RawQuery

	// Use a dedicated HTTP client that does NOT use this transport
	// to prevent infinite recursion when RoundTrip calls tryURLs.
	directClient := &http.Client{
		Timeout: ft.Timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	firstErr := error(nil)
	for _, base := range ft.allURLs() {
		baseURL, err := url.Parse(base)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("invalid base URL %q: %w", base, err)
			}
			continue
		}

		// Clone the request and modify its URL.
		clone := req.Clone(req.Context())
		clone.URL = &url.URL{
			Scheme:   baseURL.Scheme,
			Host:     baseURL.Host,
			Path:     basePath,
			RawPath:  basePath,
			RawQuery: rawQuery,
		}
		// Ensure the Host header matches.
		clone.Host = baseURL.Host

		resp, err := directClient.Do(clone)
		if err == nil {
			return resp, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("telegram: all endpoints failed: %w", firstErr)
}

// httpClient returns the underlying HTTP client used for making requests.
// It creates a default one if none is configured.
func (ft *FallbackTransport) httpClient() *http.Client {
	if ft.Client != nil {
		return ft.Client
	}
	return &http.Client{
		Timeout: ft.Timeout,
	}
}

// TestEndpoints pings the /getMe endpoint on each configured URL and returns a
// map of URL to status ("ok" or "error: ...").
func (ft *FallbackTransport) TestEndpoints() map[string]string {
	results := make(map[string]string)
	for _, base := range ft.allURLs() {
		u, err := url.Parse(base)
		if err != nil {
			results[base] = fmt.Sprintf("error: invalid URL: %v", err)
			continue
		}
		testURL := u.JoinPath("getMe").String()
		resp, err := ft.httpClient().Get(testURL)
		if err != nil {
			results[base] = fmt.Sprintf("error: %v", err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			results[base] = "ok"
		} else {
			results[base] = fmt.Sprintf("error: status %d", resp.StatusCode)
		}
	}
	return results
}

// WrapBot wraps the given Bot so that all API calls go through this
// FallbackTransport. It replaces the bot's HTTP client transport, allowing
// the transport to rewrite the API endpoint on each request.
func (ft *FallbackTransport) WrapBot(bot *Bot) *Bot {
	bot.Client = ft.httpClient()
	bot.Client.Transport = ft
	return bot
}

// RetryWithBackoff retries the given function up to maxAttempts times with
// exponential backoff starting at baseDelay. Returns nil on success or the
// last error if all attempts fail.
func RetryWithBackoff(fn func() error, maxAttempts int, baseDelay time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if attempt == maxAttempts-1 {
				break
			}
			// Exponential backoff: baseDelay * 2^attempt
			delay := baseDelay * (1 << attempt)
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return fmt.Errorf("telegram: retry exhausted after %d attempts: %w", maxAttempts, lastErr)
}

// Ensure FallbackTransport implements http.RoundTripper at compile time.
var _ http.RoundTripper = (*FallbackTransport)(nil)
