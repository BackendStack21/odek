package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// The owner of a sub-agent (a connection bound to the authenticated session)
// can still stop it.
func TestSubagentCancel_OwnSessionTaskAccepted(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := subagentTestSession(t, store, "owner")
	conn := &wsConnInfo{ID: "conn-owner-cancel"}
	conn.setLive(sess.ID, true)
	wsConnRegister(conn)
	defer wsConnUnregister(conn.ID)

	cancelled := make(chan struct{}, 1)
	const taskID = "own-task-0001"
	defer registerSubagentCancel(taskID, func() { cancelled <- struct{}{} })()
	subagentRegistryUpdate(taskID, conn.ID, func(e *subagentEntry) {})

	srv := httptest.NewServer(&golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler: func(c *golangws.Conn) {
			handleWSSubagentCancel(store, c, wsClientMsg{SessionID: sess.ID, AuthToken: sess.AuthToken, TaskID: taskID})
			time.Sleep(200 * time.Millisecond)
		},
	})
	defer srv.Close()
	c, err := golangws.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var msg string
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = golangws.Message.Receive(c, &msg)
	if !strings.Contains(msg, `"accepted":true`) {
		t.Fatalf("owner cancel reply = %s", msg)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("owner's cancel did not reach the task")
	}
}
