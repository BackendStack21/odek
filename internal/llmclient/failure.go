package llmclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/redact"
)

// maxFailureMessageRunes bounds the provider message quoted in a failure
// description.
const maxFailureMessageRunes = 200

// DescribeFailure phrases a failed model call for a person: the provider, the
// HTTP status, the provider's own message and how many attempts were made,
// e.g. "provider openai returned HTTP 500: upstream unavailable (retries
// exhausted after 8 attempts)". It reports false for errors that are not a
// provider failure (cancellation, a timeout with no provider reply, local
// errors), which callers show as before. The description never carries the
// engine's "iteration N:" prefix or the SDK's "[retryable]" tag; the error
// itself, for logs, events and errors.Is/As, is not changed.
//
// The provider message is untrusted: only its first line is used, secrets are
// redacted, control and invisible characters are escaped and it is shortened.
func DescribeFailure(err error) (string, bool) {
	return describeFailure(err, true)
}

// SummarizeFailure phrases a failed model call for text that is stored and
// read back by the model later, such as a session's turn-abort note: the
// provider, the HTTP status and the attempts made, never the provider's own
// message, e.g. "provider openai returned HTTP 500 (retries exhausted after
// 8 attempts)". A failed call DescribeFailure cannot classify keeps only the
// fact ("the call to provider anthropic failed"), since its error text can
// be the provider's words (an error event inside the stream). It reports
// false for errors that are not a failed model call and for cancellation.
func SummarizeFailure(err error) (string, bool) {
	if desc, ok := describeFailure(err, false); ok {
		return desc, true
	}
	var call *AttemptsError
	if err == nil || errors.Is(err, context.Canceled) || !errors.As(err, &call) {
		return "", false
	}
	desc := fmt.Sprintf("the call to %s failed", providerSubject("", call.Provider))
	if clause := attemptsClause(call.Attempts, errors.Is(err, context.DeadlineExceeded)); clause != "" {
		desc += " (" + clause + ")"
	}
	return desc, true
}

// describeFailure is DescribeFailure; quote adds the provider's message.
func describeFailure(err error, quote bool) (string, bool) {
	if err == nil || errors.Is(err, context.Canceled) {
		return "", false
	}
	var call *AttemptsError
	errors.As(err, &call)
	attempts, provider := 0, ""
	if call != nil {
		attempts, provider = call.Attempts, call.Provider
	}
	var desc, note string
	var rate *sdk.RateLimitError
	var api *sdk.APIError
	switch {
	case errors.As(err, &rate):
		// The SDK's own count includes learn-once fallback resends, which
		// the call's counter leaves out; it stands in only without one.
		if call == nil {
			attempts = rate.Attempts
		}
		desc = fmt.Sprintf("%s returned HTTP %d", providerSubject(rate.Provider, provider), rate.Status)
		if msg := failureMessage(rate.Message); quote && msg != "" {
			desc += ": " + msg
		}
		note = "rate limited"
	case errors.As(err, &api):
		desc = fmt.Sprintf("%s returned HTTP %d", providerSubject(api.Provider, provider), api.Status)
		if msg := failureMessage(api.Message); quote && msg != "" {
			desc += ": " + msg
		}
	case call != nil && networkFailure(err) != "":
		// A closed connection may follow a reply that had already started,
		// so it is not reported as the provider being unreachable.
		if reason := networkFailure(err); reason == connectionClosed {
			desc = fmt.Sprintf("connection to %s closed before a complete reply", providerSubject("", provider))
		} else {
			desc = fmt.Sprintf("could not reach %s: %s", providerSubject("", provider), reason)
		}
	default:
		return "", false
	}
	clause := attemptsClause(attempts, errors.Is(err, context.DeadlineExceeded))
	switch {
	case note != "" && clause != "":
		desc += " (" + note + "; " + clause + ")"
	case note != "":
		desc += " (" + note + ")"
	case clause != "":
		desc += " (" + clause + ")"
	}
	return desc, true
}

// providerSubject names the provider: the id the SDK put on the error, else
// the one the call was made to.
func providerSubject(fromErr, fromCall string) string {
	if p := strings.TrimSpace(fromErr); p != "" {
		return "provider " + danger.SanitizeInline(p)
	}
	if p := strings.TrimSpace(fromCall); p != "" {
		return "provider " + danger.SanitizeInline(p)
	}
	return "the model provider"
}

// failureMessage is the provider message's first line, redacted, escaped and
// shortened.
func failureMessage(msg string) string {
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	msg = danger.SanitizeInline(redact.RedactSecrets(msg))
	if r := []rune(msg); len(r) > maxFailureMessageRunes {
		msg = string(r[:maxFailureMessageRunes]) + "…"
	}
	return msg
}

// attemptsClause says how many attempts a failed call made and why retrying
// stopped. A deadline means the request time limit ended the retries before
// the attempt budget did.
func attemptsClause(attempts int, deadline bool) string {
	switch {
	case attempts >= MaxModelAttempts:
		return fmt.Sprintf("retries exhausted after %d attempts", attempts)
	case attempts > 1 && deadline:
		return fmt.Sprintf("stopped retrying after %d attempts: request time limit reached", attempts)
	case attempts > 1:
		return fmt.Sprintf("after %d attempts", attempts)
	case deadline:
		return "request time limit reached"
	}
	return ""
}

const connectionClosed = "connection closed"

// networkFailure names a transport failure that kept a request from getting
// a reply, or returns "" for anything else (a context deadline included: the
// caller reports a plain timeout as it always has).
func networkFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	var dns *net.DNSError
	var certVerify *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var invalidCert x509.CertificateInvalidError
	var hostname x509.HostnameError
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return connectionClosed
	case errors.As(err, &dns) && dns.IsNotFound:
		return "host not found"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.As(err, &certVerify), errors.As(err, &unknownCA),
		errors.As(err, &invalidCert), errors.As(err, &hostname):
		return "TLS certificate not trusted"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "network timeout"
	}
	return ""
}
