package telegram

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRED_Telegram_FallbackTransportReusesConnections(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	ft := &FallbackTransport{PrimaryURL: srv.URL, Timeout: 5 * time.Second}
	client := &http.Client{Transport: ft}
	for i := 0; i < 6; i++ {
		resp, err := client.Get("https://api.telegram.org/bot1/getMe")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("6 sequential requests opened %d connections, want 1", got)
	}
}

func TestFallbackTransportSharesOneDirectClient(t *testing.T) {
	ft := &FallbackTransport{Timeout: time.Second}
	if ft.direct() != ft.direct() {
		t.Fatal("direct client rebuilt per call")
	}
	if ft.direct().Transport == http.RoundTripper(ft) {
		t.Fatal("direct client must not route back through the fallback transport")
	}
}
