package events

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BackendStack21/odek/internal/budget"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

func TestErrorCategoriesAndPrivacy(t *testing.T) {
	cases := []struct {
		err   error
		class string
	}{
		{nil, ""}, {&budget.Error{}, "execution_budget"}, {&exec.ExitError{}, "process_exit"}, {exec.ErrNotFound, "executable_not_found"}, {syscall.ENOTDIR, "invalid_file_type"}, {syscall.EISDIR, "invalid_file_type"}, {syscall.ELOOP, "symlink_rejected"}, {context.Canceled, "context_canceled"}, {context.DeadlineExceeded, "deadline_exceeded"},
		{sdk.ErrIdleTimeout, "stream_idle_timeout"}, {os.ErrPermission, "permission_denied"}, {os.ErrNotExist, "not_found"},
		{syscall.ENOSPC, "disk_full"}, {syscall.EROFS, "read_only_filesystem"}, {syscall.EMFILE, "file_descriptors_exhausted"}, {syscall.ENFILE, "file_descriptors_exhausted"},
		{syscall.EADDRINUSE, "address_in_use"}, {syscall.ECONNREFUSED, "connection_refused"}, {syscall.ECONNRESET, "connection_closed"}, {syscall.EPIPE, "connection_closed"},
		{io.EOF, "unexpected_eof"}, {io.ErrUnexpectedEOF, "unexpected_eof"},
		{&sdk.APIError{Status: 401, Message: "PRIVATE"}, "provider_auth"}, {&sdk.APIError{Status: 403}, "provider_auth"},
		{&sdk.RateLimitError{APIError: sdk.APIError{Status: 429, Message: "PRIVATE"}}, "rate_limited"},
		{&sdk.APIError{Status: 503}, "provider_unavailable"}, {&sdk.APIError{Status: 400}, "provider_request"},
		{&sdk.ConfigError{Msg: "PRIVATE"}, "provider_config"}, {&json.SyntaxError{}, "invalid_json"}, {&json.UnmarshalTypeError{}, "invalid_json"},
		{&net.DNSError{Name: "PRIVATE"}, "dns_failure"}, {x509.UnknownAuthorityError{}, "tls_certificate"}, {x509.CertificateInvalidError{}, "tls_certificate"}, {x509.HostnameError{}, "tls_certificate"},
		{&net.DNSError{IsTimeout: true}, "dns_failure"}, {&net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}, "network_timeout"},
		{errors.New("PRIVATE"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.class+fmt.Sprintf("_%T", tc.err), func(t *testing.T) {
			err := tc.err
			if err != nil {
				err = &privacyWrappedError{err: err}
			}
			if got := ErrorClass(err); got != tc.class {
				t.Fatalf("got %s, want %s", got, tc.class)
			}
			b, marshalErr := json.Marshal(ErrorData(err))
			if marshalErr != nil || strings.Contains(string(b), "PRIVATE") {
				t.Fatalf("unsafe details: %s %v", b, marshalErr)
			}
		})
	}
	err := &url.Error{Op: "Post", URL: "https://PRIVATE/path?key=PRIVATE", Err: &sdk.APIError{Status: 429, Message: "PRIVATE"}}
	data := ErrorData(err)
	if data["http_status"] != 429 || data["error_class"] != "rate_limited" {
		t.Fatal(data)
	}
	b, _ := json.Marshal(data)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatal(string(b))
	}
}

type privacyWrappedError struct{ err error }

func (e *privacyWrappedError) Error() string { return "PRIVATE" }
func (e *privacyWrappedError) Unwrap() error { return e.err }

func TestErrorDataRetainsRateLimitAttemptsWithoutMessage(t *testing.T) {
	data := ErrorData(fmt.Errorf("PRIVATE wrapper: %w", &sdk.RateLimitError{APIError: sdk.APIError{Status: 429, Message: "PRIVATE body"}, Attempts: 3}))
	if data["attempts"] != 3 || data["http_status"] != 429 {
		t.Fatal(data)
	}
	body, _ := json.Marshal(data)
	if strings.Contains(string(body), "PRIVATE") {
		t.Fatal(string(body))
	}
}
