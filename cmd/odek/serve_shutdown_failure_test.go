package main

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
)

type failedServeListener struct{ err error }

func (l failedServeListener) Accept() (net.Conn, error) { return nil, l.err }
func (l failedServeListener) Close() error              { return nil }
func (l failedServeListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestServeListenerFailureDrainsBackgroundJobs(t *testing.T) {
	mgr := bgproc.NewManager(bgproc.Config{}, nil)
	previous := serveBG.Swap(mgr)
	defer serveBG.Store(previous)
	defer mgr.Shutdown()
	job, err := mgr.Start("listener-test", "sleep 30", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("permanent accept failure")
	if err = serveOnListener(failedServeListener{failure}, http.NewServeMux()); !errors.Is(err, failure) {
		t.Fatalf("listener error lost: %v", err)
	}
	got, ok := mgr.Get("listener-test", job.ID)
	if !ok || got.Status == bgproc.StatusRunning {
		t.Fatal("job survived listener failure")
	}
	if _, err := mgr.Start("another", "true", "", time.Second); err == nil {
		t.Fatal("manager not shut down")
	}
}
