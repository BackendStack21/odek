package llmclient

import (
	"fmt"
	"log"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/redact"
)

// maxNoticeRunes bounds the error text quoted in a retry notice.
const maxNoticeRunes = 200

// reportAttempt is the SDK's attempt observer (sdk.WithAttemptObserver). The
// SDK retries 5xx, 408 and 429 replies and network errors on its own, with
// backoff (up to eight attempts), so against a slow or failing provider a turn
// can sit for minutes with nothing on screen. Each failed attempt that is about
// to be retried prints one line (the log, i.e. stderr) with the error, redacted,
// sanitized and cut short, and the delay; the runtime log gets an
// operation_warning with the HTTP status only, since it never carries error
// text. Final failures, learn-once retries (delay 0) and cancellations are not
// reported here: the caller reports the final error, and learn events have their
// own bridge. Model listing is skipped too: a failed discovery shows up as the
// context-window warning.
func reportAttempt(ev sdk.AttemptEvent) {
	if ev.Err == nil || ev.RetryDelay <= 0 || ev.Operation == "models" {
		return
	}
	log.Print(attemptNotice(ev))
	diagnostics.Warning("llm", "request_retry", ev.Err)
}

// attemptNotice is the one-line retry notice for ev.
func attemptNotice(ev sdk.AttemptEvent) string {
	msg := danger.SanitizeInline(redact.RedactSecrets(ev.Err.Error()))
	if r := []rune(msg); len(r) > maxNoticeRunes {
		msg = string(r[:maxNoticeRunes]) + "…"
	}
	return fmt.Sprintf("odek: model request failed (attempt %d): %s — retrying in %s",
		ev.Attempt, msg, ev.RetryDelay.Round(100*time.Millisecond))
}
