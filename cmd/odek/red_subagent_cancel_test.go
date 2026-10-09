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

// A subagent_cancel authenticated with session A's token must only stop
// sub-agents that belong to session A's connection/run. Today the task id is
// resolved through a process-global registry with no ownership check, so a
// holder of any valid session token can stop another run's sub-agent.
func TestRED_SubagentCancel_CrossSessionTaskRefused(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessA, err := store.Create(nil, "m", "A")
	if err != nil {
		t.Fatal(err)
	}
	if sessA.AuthToken == "" {
		sessA.AuthToken = session.GenerateAuthToken()
		if err := store.Save(sessA); err != nil {
			t.Fatal(err)
		}
	}

	// A sub-agent owned by a different run/connection ("other-run").
	cancelled := make(chan struct{}, 1)
	const taskID = "victim-task-0001"
	defer registerSubagentCancel(taskID, func() { cancelled <- struct{}{} })()
	subagentRegistryUpdate(taskID, "other-run", func(e *subagentEntry) {})

	srv := httptest.NewServer(&golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler: func(conn *golangws.Conn) {
			handleWSSubagentCancel(store, conn, wsClientMsg{SessionID: sessA.ID, AuthToken: sessA.AuthToken, TaskID: taskID})
			time.Sleep(200 * time.Millisecond)
		},
	})
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, err := golangws.Dial(url, "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var msg string
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = golangws.Message.Receive(conn, &msg)

	select {
	case <-cancelled:
		t.Fatalf("session A's token cancelled a sub-agent owned by another run; reply=%s", msg)
	case <-time.After(300 * time.Millisecond):
	}
}
