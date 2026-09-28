package events

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os/exec"
	"syscall"

	sdk "github.com/BackendStack21/go-llm-sdk"
	"github.com/BackendStack21/odek/internal/budget"
)

// ErrorData describes the cause without copying an error's message, which can
// contain credentials, file contents, request URLs, or provider response bodies.
func ErrorData(err error) map[string]any {
	data := map[string]any{"error_class": ErrorClass(err)}
	if err == nil {
		return data
	}
	data["error_type"] = fmt.Sprintf("%T", err)
	var api *sdk.APIError
	if errors.As(err, &api) {
		data["http_status"] = api.Status
	}
	return data
}

func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, sdk.ErrIdleTimeout):
		return "stream_idle_timeout"
	case errors.Is(err, fs.ErrPermission):
		return "permission_denied"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	case errors.Is(err, syscall.ENOSPC):
		return "disk_full"
	case errors.Is(err, syscall.EROFS):
		return "read_only_filesystem"
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return "file_descriptors_exhausted"
	case errors.Is(err, syscall.EADDRINUSE):
		return "address_in_use"
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EISDIR):
		return "invalid_file_type"
	case errors.Is(err, syscall.ELOOP):
		return "symlink_rejected"
	case errors.Is(err, exec.ErrNotFound):
		return "executable_not_found"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "connection_closed"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	}
	if _, ok := budget.As(err); ok {
		return "execution_budget"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "process_exit"
	}
	var api *sdk.APIError
	if errors.As(err, &api) {
		switch {
		case api.Status == 401 || api.Status == 403:
			return "provider_auth"
		case api.Status == 429:
			return "rate_limited"
		case api.Status >= 500:
			return "provider_unavailable"
		default:
			return "provider_request"
		}
	}
	var cfg *sdk.ConfigError
	if errors.As(err, &cfg) {
		return "provider_config"
	}
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typeError) {
		return "invalid_json"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_failure"
	}
	var cert x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	if errors.As(err, &cert) || errors.As(err, &invalid) || errors.As(err, &host) {
		return "tls_certificate"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network_timeout"
	}
	return "error"
}
