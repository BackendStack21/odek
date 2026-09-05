package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/tool"
)

// ── P0-4: the event stream must be able to answer "what actually ran" ───

func TestArgv0(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"rm -rf /tmp/x", "rm"},
		{"FOO=bar BAZ=qux curl http://x", "curl"},
		{`"/bin/rm" -rf /`, "rm"},
		{"  ls  ", "ls"},
		{"", ""},
	}
	for _, c := range cases {
		if got := argv0(c.cmd); got != c.want {
			t.Errorf("argv0(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestArgSummary_Shell(t *testing.T) {
	s := argSummary(context.Background(), "shell", `{"command":"FOO=1 rm -rf /tmp/x"}`)
	if s == nil {
		t.Fatal("nil summary for shell")
	}
	if s["argv0"] != "rm" {
		t.Errorf("argv0 = %v, want rm", s["argv0"])
	}
	if _, ok := s["class"].(string); !ok {
		t.Errorf("class missing or not a string: %v", s["class"])
	}
}

func TestArgSummary_PathTool(t *testing.T) {
	s := argSummary(context.Background(), "write_file", `{"path":"/tmp/out.txt","content":"hi"}`)
	if s == nil {
		t.Fatal("nil summary for write_file")
	}
	if s["path"] != "/tmp/out.txt" {
		t.Errorf("path = %v", s["path"])
	}
	if s["class"] != "local_write" {
		t.Errorf("class = %v, want local_write", s["class"])
	}
}

func TestArgSummary_BatchPatchListsPaths(t *testing.T) {
	args := `{"patches":[{"path":"a.py"},{"path":"b.py"}]}`
	s := argSummary(context.Background(), "batch_patch", args)
	if s == nil {
		t.Fatal("nil summary for batch_patch")
	}
	paths, ok := s["path"].([]string)
	if !ok || len(paths) != 2 {
		t.Fatalf("path list = %v, want 2 entries", s["path"])
	}
}

func TestArgSummary_URLOnlyHost(t *testing.T) {
	s := argSummary(context.Background(), "browser", `{"action":"navigate","url":"https://user:tok@evil.example.com:8443/p?x=1"}`)
	if s == nil {
		t.Fatal("nil summary for browser")
	}
	if s["host"] != "evil.example.com" {
		t.Errorf("host = %v, want evil.example.com (never full URL with credentials)", s["host"])
	}
}

func TestArgSummary_UnknownToolNil(t *testing.T) {
	if s := argSummary(context.Background(), "plan", `{}`); s != nil {
		t.Errorf("argSummary(plan) = %v, want nil", s)
	}
}

// End-to-end: started events carry args_summary always, raw args only on
// explicit opt-in.
func runEventsEngine(t *testing.T, includeArgs bool) []events.Event {
	t.Helper()
	server := newToolLoopServer("shell", `{"command":"echo hello"}`)
	t.Cleanup(server.Close)

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "shell", description: "runs a command", output: "hello"},
	})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetEventsIncludeArgs(includeArgs)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)
	if _, err := engine.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	return col.all()
}

func TestEngine_Events_ArgSummaryAlwaysPresent(t *testing.T) {
	evs := runEventsEngine(t, false)
	var started *events.Event
	for i, ev := range evs {
		if ev.Type == events.TypeToolCallStarted {
			started = &evs[i]
		}
	}
	if started == nil {
		t.Fatal("no tool_call_started event")
	}
	if _, ok := started.Data["args_summary"]; !ok {
		t.Errorf("args_summary missing from started event: %+v", started.Data)
	}
	if _, ok := started.Data["args"]; ok {
		t.Error("raw args leaked into event stream without opt-in")
	}
}

func TestEngine_Events_IncludeArgsOptIn(t *testing.T) {
	evs := runEventsEngine(t, true)
	var started *events.Event
	for i, ev := range evs {
		if ev.Type == events.TypeToolCallStarted {
			started = &evs[i]
		}
	}
	if started == nil {
		t.Fatal("no tool_call_started event")
	}
	raw, ok := started.Data["args"].(string)
	if !ok || !strings.Contains(raw, "echo hello") {
		t.Errorf("opt-in raw args missing: %+v", started.Data)
	}
}

// The args_summary values must round-trip through JSON as a usable object.
func TestArgSummary_JSONRoundTrip(t *testing.T) {
	s := argSummary(context.Background(), "shell", `{"command":"cat /etc/passwd"}`)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["argv0"] != "cat" {
		t.Errorf("argv0 round-trip = %v", back["argv0"])
	}
}

var _ = http.StatusOK
var _ = httptest.NewServer
