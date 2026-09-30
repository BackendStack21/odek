package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestRESTRunReportsSessionWhileProviderIsRunning(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%t", existing), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
				once.Do(func() { close(entered) })
				<-release
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			})
			defer llm.Close()
			env := newRestRunEnv(t, llm.URL, nil)
			defer close(release)
			body := `{"content":"hello"}`
			if existing {
				sess, err := env.store.Create(nil, "test-model", "fixture")
				if err != nil {
					t.Fatal(err)
				}
				body = fmt.Sprintf(`{"content":"hello","session_id":%q,"auth_token":%q}`, sess.ID, sess.AuthToken)
			}
			_, resp := startTestRun(t, env, body)
			run := lookupRun(resp["run_id"].(string))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider was not called")
			}
			snap := run.snapshot(true)
			if snap["session_id"] == "" {
				t.Fatal("active REST run omitted its session ID after the session event")
			}
			if runStatusTerminal(snap["status"].(string)) {
				t.Fatal("fixture already completed")
			}
		})
	}
}

func TestWebSocketModelDiscoveryKeepsSessionBusy(t *testing.T) {
	var models atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			if models.Add(1) == 1 {
				close(entered)
				<-release
			}
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer llm.Close()
	defer setTestEnv(t, llm.URL)()
	t.Setenv("ODEK_MODEL", "deepseek-v4-flash")
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	defer close(release)
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	payload, _ := json.Marshal(map[string]any{"type": "session_switch", "session_id": sess.ID, "auth_token": sess.AuthToken})
	if err := websocket.Message.Send(conn, string(payload)); err != nil {
		t.Fatal(err)
	}
	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "session" {
			break
		}
	}
	if err := websocket.Message.Send(conn, `{"type":"prompt","content":"hello","model":"custom-small"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery not called")
	}
	if got := (wsWakeRouter{}).State(sess.ID); got != wakeBusy {
		t.Fatalf("operator turn blocked in discovery but wake state=%v, want busy", got)
	}
}

func TestWebSocketKeepsEffectiveSessionBoundWhileRunning(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%t", existing), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
				once.Do(func() { close(entered) })
				<-release
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			})
			defer llm.Close()
			defer setTestEnv(t, llm.URL)()
			t.Setenv("ODEK_MODEL", "deepseek-v4-flash")
			store := newTestSessionStore(t)
			ln, mux := buildServeMux(t, store)
			defer startServeTest(t, ln, mux)()
			defer close(release)
			wsUpgradeLimiter.reset()
			conn := dialTestWS(t, ln.Addr().String())
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			readSession := func() string {
				for {
					var raw []byte
					if err := websocket.Message.Receive(conn, &raw); err != nil {
						t.Fatal(err)
					}
					var frame map[string]any
					if err := json.Unmarshal(raw, &frame); err != nil {
						t.Fatal(err)
					}
					if frame["type"] == "session" {
						return frame["session_id"].(string)
					}
				}
			}
			if existing {
				sess, err := store.Create(nil, "test-model", "fixture")
				if err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(map[string]any{"type": "session_switch", "session_id": sess.ID, "auth_token": sess.AuthToken})
				if err := websocket.Message.Send(conn, string(payload)); err != nil {
					t.Fatal(err)
				}
				_ = readSession()
			}
			wantModel := loadJSONMockResolved().Model
			payload := map[string]any{"type": "prompt", "content": "hello"}
			if existing {
				wantModel = "gpt-4o"
				payload["model"] = wantModel
			}
			data, _ := json.Marshal(payload)
			if err := websocket.Message.Send(conn, string(data)); err != nil {
				t.Fatal(err)
			}
			sid := readSession()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider was not called")
			}
			conns := wsConnsForSession(sid)
			if len(conns) != 1 || !conns[0].isBusy() {
				t.Fatalf("active socket lost session binding: %d matching connections", len(conns))
			}
			if got := conns[0].wireCopy().Model; got != wantModel {
				t.Errorf("live model=%q, want %q", got, wantModel)
			}
			if got := (wsWakeRouter{}).State(sid); got != wakeBusy {
				t.Fatalf("wake state=%v, want busy", got)
			}
		})
	}
}
