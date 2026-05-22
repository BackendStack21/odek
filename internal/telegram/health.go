package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// HealthServer serves a lightweight HTTP health check endpoint.
// It reports bot liveness and uptime for monitoring systems.
type HealthServer struct {
	addr      string
	startTime time.Time
	ready     atomic.Bool
	log       Logger
}

// NewHealthServer creates a HealthServer listening on the given address.
// Use "127.0.0.1:9090" or "0.0.0.0:9090". Empty string disables the server.
func NewHealthServer(addr string) *HealthServer {
	return &HealthServer{
		addr:      addr,
		startTime: time.Now(),
		log:       NewNopLogger(),
	}
}

// SetLogger sets the logger. If nil, a NopLogger is used.
func (hs *HealthServer) SetLogger(l Logger) {
	if l == nil {
		hs.log = NewNopLogger()
		return
	}
	hs.log = l
}

// SetReady marks the health server as ready (polling has started).
// Thread-safe: safe to call from any goroutine.
func (hs *HealthServer) SetReady() {
	hs.ready.Store(true)
}

// ServeHTTP implements http.Handler.
func (hs *HealthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/health" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if !hs.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "starting",
			"message": "bot is initializing, polling not yet started",
		})
		return
	}

	uptime := time.Since(hs.startTime).Truncate(time.Second)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"uptime_seconds": int(uptime.Seconds()),
	})
}

// Start begins listening on the configured address. Blocks until ctx is
// cancelled, then shuts down the HTTP server gracefully.
// Returns any error from starting the listener.
func (hs *HealthServer) Start(ctx context.Context) error {
	if hs.addr == "" {
		return nil // disabled
	}

	ln, err := net.Listen("tcp", hs.addr)
	if err != nil {
		return fmt.Errorf("health server: listen %s: %w", hs.addr, err)
	}

	srv := &http.Server{Addr: hs.addr, Handler: hs}
	hs.log.Info("health server started", "addr", ln.Addr().String())

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "health server: shutdown: %v\n", err)
		}
	}()

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return fmt.Errorf("health server: serve: %w", err)
	}
	return nil
}
