package main

import (
	"bufio"
	"net"
	"net/http"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
)

// diagnosticHTTPHandler records failed responses by registered route pattern.
// Request URLs, query strings, headers and response bodies never enter logs.
func diagnosticHTTPHandler(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			pattern = "unmatched_route"
		}
		recorder := &diagnosticResponseWriter{ResponseWriter: w}
		defer func() {
			if value := recover(); value != nil {
				diagnostics.Emit(events.Event{Type: "panic_recovered", Data: map[string]any{"component": "serve", "operation": pattern, "error_class": "panic"}})
				panic(value) // net/http keeps its existing recovery and connection handling
			}
			if recorder.status >= 400 {
				typ := "operation_warning"
				if recorder.status >= 500 {
					typ = "operation_failed"
				}
				diagnostics.Emit(events.Event{Type: typ, Data: map[string]any{"component": "serve", "operation": pattern, "error_class": "http_response", "http_status": recorder.status}})
			}
		}()
		mux.ServeHTTP(recorder, r)
	})
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *diagnosticResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *diagnosticResponseWriter) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *diagnosticResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *diagnosticResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
