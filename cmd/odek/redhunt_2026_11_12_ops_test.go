package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	opsReplHelperEnv   = "OPS_REDHUNT_REPL_HELPER"
	opsReplFirstPrompt = "first task RED-OPS-INTERRUPT-ONE"
	opsReplNextPrompt  = "next task RED-OPS-INTERRUPT-TWO"
)

// TestOpsReplHelperProcess is not a test: it is the child process of
// TestRED_ReplUnusableAfterCtrlCInterruptsTurn. It runs the REPL in-process
// with stdin/stderr wired to the parent, so the parent can deliver SIGINT to
// it without signalling the test binary itself.
func TestOpsReplHelperProcess(t *testing.T) {
	if os.Getenv(opsReplHelperEnv) != "1" {
		t.Skip("helper process for TestRED_ReplUnusableAfterCtrlCInterruptsTurn")
	}
	err := replCmd([]string{"--model", "deepseek-chat", "--no-stream"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nOPS-REPL-EXIT-ERROR: %v\n", err)
		os.Exit(3)
	}
	os.Exit(0)
}

// opsReplLLM is a mock OpenAI-format provider. The request carrying the
// first prompt blocks until the client cancels it (a turn the operator
// interrupts with Ctrl-C); every other request answers with plain text.
type opsReplLLM struct {
	once   sync.Once
	first  chan struct{}
	second chan struct{}
}

func (m *opsReplLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case strings.Contains(string(body), opsReplNextPrompt):
		m.once.Do(func() { close(m.second) })
	case strings.Contains(string(body), opsReplFirstPrompt):
		select {
		case <-m.first:
		default:
			close(m.first)
		}
		<-r.Context().Done() // a long model call, interrupted by the operator
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"choices":[{"message":{"content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
}

// replTurnScope: the first interrupt cancels only the current turn; a second
// one while it unwinds forces exit; interrupts that arrived while idle are
// discarded.
func TestOpsReplTurnScope(t *testing.T) {
	sig := make(chan os.Signal, 2)
	exited := make(chan struct{}, 1)
	orig := replForceExit
	replForceExit = func() { exited <- struct{}{} }
	defer func() { replForceExit = orig }()

	sig <- os.Interrupt // idle-time interrupt must not cancel the next turn
	ctx, stop := replTurnScope(sig)
	if ctx.Err() != nil {
		t.Fatal("stale idle interrupt cancelled the new turn")
	}
	sig <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("interrupt did not cancel the turn")
	}
	sig <- os.Interrupt
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("second interrupt did not force exit")
	}
	stop()

	ctx2, stop2 := replTurnScope(sig)
	defer stop2()
	if ctx2.Err() != nil {
		t.Fatal("next turn starts cancelled")
	}
}

// TestRED_ReplUnusableAfterCtrlCInterruptsTurn: the REPL creates one
// signal.NotifyContext for its whole lifetime and derives every turn from
// it. Ctrl-C during a turn cancels that context for good: the turn is
// aborted and its partial history persisted ("odek: agent error: context
// canceled"), the REPL prints the next "─── Turn 2 ───" prompt — and the next
// line the operator submits makes AcquireExecution fail on the cancelled
// context, so replCmd returns "context canceled". The typed prompt never
// reaches the model, the REPL exits, and the end-of-session episode
// extraction is skipped. Interrupting a turn must leave the REPL usable.
func TestRED_ReplUnusableAfterCtrlCInterruptsTurn(t *testing.T) {
	llm := &opsReplLLM{first: make(chan struct{}), second: make(chan struct{})}
	srv := httptest.NewServer(llm)
	defer srv.Close()

	home := t.TempDir()
	writeMockProviderFixture(t, home, srv.URL)

	cmd := exec.Command(os.Args[0], "-test.run=^TestOpsReplHelperProcess$", "-test.count=1")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		opsReplHelperEnv+"=1",
		"HOME="+home,
		"USERPROFILE="+home,
		"ODEK_TEST_HOME="+home,
		"ODEK_NO_SANDBOX=1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	var mu sync.Mutex
	var out strings.Builder
	turn2 := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		seen := false
		for sc.Scan() {
			mu.Lock()
			out.WriteString(sc.Text() + "\n")
			mu.Unlock()
			if !seen && strings.Contains(sc.Text(), "─── Turn 2 ───") {
				seen = true
				close(turn2)
			}
		}
	}()
	exited := make(chan error, 1)
	go func() {
		<-readDone
		exited <- cmd.Wait()
	}()
	transcript := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}

	fmt.Fprintln(stdin, opsReplFirstPrompt)
	select {
	case <-llm.first:
	case err := <-exited:
		t.Fatalf("precondition: REPL exited before the first model call (%v):\n%s", err, transcript())
	case <-time.After(60 * time.Second):
		t.Fatalf("precondition: first model call never arrived:\n%s", transcript())
	}

	// The operator presses Ctrl-C while the model call is in flight.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-turn2:
	case err := <-exited:
		t.Fatalf("precondition: REPL exited on the interrupt itself (%v):\n%s", err, transcript())
	case <-time.After(30 * time.Second):
		t.Fatalf("precondition: REPL never offered the next prompt after the interrupt:\n%s", transcript())
	}

	// The REPL offered a new prompt; the operator types the next task.
	fmt.Fprintln(stdin, opsReplNextPrompt)
	select {
	case <-llm.second:
		fmt.Fprintln(stdin, "/exit")
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
		}
	case err := <-exited:
		t.Fatalf("after Ctrl-C interrupted a turn, the next prompt never reached the model and the REPL exited (%v):\n%s",
			err, transcript())
	case <-time.After(60 * time.Second):
		t.Fatalf("after Ctrl-C interrupted a turn, the next prompt never reached the model:\n%s", transcript())
	}
}
