package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	golangws "golang.org/x/net/websocket"

	"github.com/BackendStack21/odek/internal/session"
)

// After a write timeout the watchdog latches the connection dead and tears
// it down. A parked Message.Send goroutine still holds the old write state;
// if releaseConnWriter deletes the entry, a subsequent writeWSJSON creates
// fresh (live) state and issues a second concurrent Send on the same
// *websocket.Conn — which is not concurrency-safe. The dead state must be
// sticky: the same state pointer must come back and every later write must
// fast-fail.
func TestRED_WriteTimeoutDeadConnStaysDead(t *testing.T) {
	old := wsWriteTimeout.Load()
	wsWriteTimeout.Store(int64(150 * time.Millisecond))
	t.Cleanup(func() { wsWriteTimeout.Store(old) })

	pipe := newBlockingWSConn(t)
	conn := pipe.wsConn

	writeWSJSON(conn, map[string]string{"type": "flood", "data": "x"})
	// The write timed out and latched dead; simulate the watchdog teardown.
	w1 := connWriter(conn)
	if !w1.dead {
		t.Fatalf("expected write state to be latched dead after timeout")
	}
	releaseConnWriter(conn)

	w2 := connWriter(conn)
	if w2 != w1 {
		t.Fatalf("connWriter re-created state after release: dead latch was lost (%p != %p)", w2, w1)
	}
	if !w2.dead {
		t.Fatalf("re-acquired write state lost the dead latch")
	}

	// A later write must fast-fail: nothing may reach the connection.
	writeWSJSON(conn, map[string]string{"type": "after"})
	if n := pipe.bytesWritten(); n > 0 {
		t.Fatalf("fast-fail write delivered %d bytes to a dead connection", n)
	}
}

// wsPipe is an io.ReadWriteCloser that completes the x/net/websocket client
// handshake, then blocks all further writes until the test releases it,
// simulating a client that stopped reading (full TCP receive window).
type wsPipe struct {
	rel       chan struct{}
	written   chan []byte
	wsConn    *golangws.Conn
	hsKey     string
	responded bool
}

func newBlockingWSConn(t *testing.T) *wsPipe {
	t.Helper()
	p := &wsPipe{
		rel:     make(chan struct{}),
		written: make(chan []byte, 16),
	}
	t.Cleanup(func() { close(p.rel) })
	cfg, err := golangws.NewConfig(originURL.String(), originURL.String())
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	conn, err := golangws.NewClient(cfg, p)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	p.wsConn = conn
	return p
}

var originURL = mustURL("ws://localhost/")

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}
func (p *wsPipe) Read(b []byte) (int, error) {
	if !p.responded {
		p.responded = true
		sum := sha1.Sum([]byte(p.hsKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"
		return copy(b, resp), nil
	}
	<-p.rel
	return 0, io.EOF
}

func (p *wsPipe) Write(b []byte) (int, error) {
	if p.hsKey == "" {
		// Client handshake request: capture Sec-WebSocket-Key so Read can
		// answer a valid 101.
		req := string(b)
		if i := strings.Index(req, "Sec-WebSocket-Key: "); i >= 0 {
			rest := req[i+len("Sec-WebSocket-Key: "):]
			if j := strings.Index(rest, "\r\n"); j >= 0 {
				p.hsKey = rest[:j]
			}
		}
		return len(b), nil
	}
	// Post-handshake frame: block while the test is live; a released write is
	// recorded as delivered.
	<-p.rel
	select {
	case p.written <- append([]byte(nil), b...):
	default:
	}
	return 0, io.EOF
}

func (p *wsPipe) Close() error { return nil }

func (p *wsPipe) bytesWritten() int {
	select {
	case b := <-p.written:
		return len(b)
	default:
		return 0
	}
}

// validateSessionTokenStrict must compare BEFORE any mint+persist: with an
// empty stored token, an unauthenticated probe (wrong token, DELETE) must get
// a plain 401 and the session file on disk must be byte-for-byte unchanged.
// Minting-and-saving on every probe rewrote the legacy file and rotated the
// token under attacker control.
func TestRED_TokenProbeDoesNotRewriteLegacySession(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{
		ID:        "20260926-tokentest00000000000000000001",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Task:      "legacy session",
		AuthToken: "",
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sess.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if ok := validateSessionTokenStrict(store, sess, "attacker-token"); ok {
		t.Fatalf("strict validation accepted a wrong token on a legacy session")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("failed auth probe mutated the session file (%d -> %d bytes)", len(before), len(after))
	}

	// The lenient variant must not mint+persist before comparing either:
	// a failed probe must not rewrite the file.
	if _, ok := validateSessionToken(store, sess, "attacker-token"); ok {
		t.Fatalf("lenient validation accepted a wrong token on a legacy session")
	}
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after2) {
		t.Fatalf("failed lenient probe mutated the session file (%d -> %d bytes)", len(before), len(after2))
	}
}
