package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
)

func tgresMediaFile(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".odek", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("private contents of chat A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tgresQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRED_TelegramReadFileRefusesOtherChatsMedia(t *testing.T) {
	p := tgresMediaFile(t, "doc_chat111_note_abcd.txt")
	tl := &readFileTool{dangerousConfig: danger.DangerousConfig{}}
	args, _ := json.Marshal(map[string]any{"path": p})

	tl.SetContext(withTelegramMediaChat(context.Background(), 222))
	out, err := tl.Call(string(args))
	if err == nil || strings.Contains(out, "private contents") {
		t.Fatalf("chat 222 read chat 111's media: out=%q err=%v", out, err)
	}
	if !strings.Contains(out, "different chat") {
		t.Fatalf("want a different-chat refusal, got %q", out)
	}

	// The owning chat still reads it.
	tl.SetContext(withTelegramMediaChat(context.Background(), 111))
	out, err = tl.Call(string(args))
	if err != nil || !strings.Contains(out, "private contents") {
		t.Fatalf("owner read failed: out=%q err=%v", out, err)
	}

	// Outside a Telegram turn nothing changes.
	tl.SetContext(context.Background())
	if out, err = tl.Call(string(args)); err != nil || !strings.Contains(out, "private contents") {
		t.Fatalf("non-telegram read failed: out=%q err=%v", out, err)
	}
}

func TestRED_TelegramMediaScopeCoversSiblingsAndSpellings(t *testing.T) {
	p := tgresMediaFile(t, "voice_chat111_x.ogg")
	home, _ := os.UserHomeDir()
	link := filepath.Join(t.TempDir(), "alias.ogg")
	if err := os.Symlink(p, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	ctx := withTelegramMediaChat(context.Background(), 222)
	for name, path := range map[string]string{
		"absolute": p,
		"tilde":    "~/.odek/media/voice_chat111_x.ogg",
		"dotdot":   filepath.Join(home, ".odek", "media", "..", "media", "voice_chat111_x.ogg"),
		"symlink":  link,
	} {
		if err := checkTelegramMediaRead(ctx, path); err == nil {
			t.Errorf("%s spelling %q: chat 222 allowed", name, path)
		}
	}
	// vision and transcribe consult the same guard before touching the file.
	vt := &visionTool{dangerousConfig: danger.DangerousConfig{}}
	tt := &transcribeTool{dangerousConfig: danger.DangerousConfig{}}
	vt.SetContext(ctx)
	tt.SetContext(ctx)
	if out, _ := tt.Call(`{"path":` + tgresQuote(p) + `}`); !strings.Contains(out, "different chat") {
		t.Errorf("transcribe did not refuse: %q", out)
	}
	if out, _ := vt.CallContext(ctx, `{"path":`+tgresQuote(p)+`}`); !strings.Contains(out, "different chat") {
		t.Errorf("vision did not refuse: %q", out)
	}
	// A file of the chat itself and files outside the media dir are fine.
	if err := checkTelegramMediaRead(withTelegramMediaChat(context.Background(), 111), p); err != nil {
		t.Errorf("owner refused: %v", err)
	}
	if err := checkTelegramMediaRead(ctx, filepath.Join(t.TempDir(), "x.txt")); err != nil {
		t.Errorf("non-media path refused: %v", err)
	}
}

func TestRED_TelegramMediaJobsRunOffTheUpdateLoop(t *testing.T) {
	const chatA, chatB int64 = 910001, 910002
	release := make(chan struct{})
	started := make(chan struct{})
	var order []int
	var mu sync.Mutex
	done := make(chan struct{}, 3)

	returned := make(chan struct{})
	go func() {
		// A slow download/transcription for chat A must not block the caller.
		runChatMediaOffLoop(chatA, nil, func() {
			close(started)
			<-release
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			done <- struct{}{}
		})
		// Same chat: queued behind the first, order preserved.
		runChatMediaOffLoop(chatA, nil, func() {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			done <- struct{}{}
		})
		// A panicking job is contained and does not stall the chat's queue.
		runChatMediaOffLoop(chatA, nil, func() { panic("boom") })
		runChatMediaOffLoop(chatA, nil, func() {
			mu.Lock()
			order = append(order, 3)
			mu.Unlock()
			done <- struct{}{}
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("runChatMediaOffLoop blocked the caller")
	}
	<-started

	// Another chat's job runs while chat A's is still in flight.
	other := make(chan struct{})
	runChatMediaOffLoop(chatB, nil, func() { close(other) })
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("chat B's media job waited on chat A")
	}

	close(release)
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("queued media jobs did not finish")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("per-chat order not preserved: %v", order)
	}
}

func TestRED_TelegramResumeSummaryReachesNextTurn(t *testing.T) {
	const chat int64 = 910010
	t.Cleanup(func() { telegramResumePending.Delete(chat) })
	srv := simpleLLMServer(t, "You were reviewing the auth refactor.")
	mm := newExtendedBackedManager(t, srv)
	msgs := []session.Message{
		{Role: "system", Content: "identity"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "continue please"},
	}

	// No resume pending: untouched.
	out, n := takeTelegramResumeSummary(context.Background(), chat, mm, msgs, false)
	if n != 0 || len(out) != len(msgs) {
		t.Fatalf("no resume pending but messages changed: n=%d len=%d", n, len(out))
	}

	telegramResumePending.Store(chat, struct{}{})
	// A wake turn leaves the mark for the principal's next turn.
	if _, n := takeTelegramResumeSummary(context.Background(), chat, mm, msgs, true); n != 0 {
		t.Fatal("wake turn consumed the resume summary")
	}
	out, n = takeTelegramResumeSummary(context.Background(), chat, mm, msgs, false)
	if n != 1 || len(out) != len(msgs)+1 {
		t.Fatalf("summary not inserted: n=%d len=%d", n, len(out))
	}
	rb := out[len(out)-2]
	if rb.Name != session.ReturnAfterBreakName || rb.Role != "user" || !strings.Contains(rb.Content, "auth refactor") {
		t.Errorf("summary message wrong: %+v", rb)
	}
	if out[len(out)-1].Content != "continue please" {
		t.Errorf("the turn's own message must stay last: %+v", out[len(out)-1])
	}
	if msgs[len(msgs)-1].Content != "continue please" || len(msgs) != 4 {
		t.Error("input slice mutated")
	}
	// Consumed: the following turn gets no summary.
	if _, n := takeTelegramResumeSummary(context.Background(), chat, mm, msgs, false); n != 0 {
		t.Error("summary injected twice")
	}
}

func TestRED_TelegramTextAndResetWaitForPendingMedia(t *testing.T) {
	const chat int64 = 910020
	var mu sync.Mutex
	var order []string
	rec := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	release := make(chan struct{})
	runChatMediaOffLoop(chat, nil, func() {
		<-release
		rec("media-turn")
	})

	// A text turn (runChatTurn begins with waitChatReset) and a /new queued
	// behind the slow media job must both come after it.
	done := make(chan struct{}, 2)
	go func() {
		waitChatReset(chat)
		rec("text-turn")
		done <- struct{}{}
	}()
	time.Sleep(50 * time.Millisecond)
	runChatResetOffLoop(chat, func() {
		rec("reset")
		done <- struct{}{}
	})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(order) != 0 {
		mu.Unlock()
		t.Fatalf("ran before the media job finished: %v", order)
	}
	mu.Unlock()
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("queued work never ran")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if order[0] != "media-turn" {
		t.Fatalf("media job must come first: %v", order)
	}
}

type tgresCtxCaller interface {
	SetContext(context.Context)
	Call(string) (string, error)
}

func TestRED_TelegramMediaScopeCoversEveryPathTool(t *testing.T) {
	p := tgresMediaFile(t, "doc_chat111_secret_ab.txt")
	dc := danger.DangerousConfig{}
	q := tgresQuote(p)
	cases := map[string]struct {
		tool tgresCtxCaller
		args string
	}{
		"read_file":   {&readFileTool{dangerousConfig: dc}, `{"path":` + q + `}`},
		"head_tail":   {&headTailTool{dangerousConfig: dc}, `{"path":` + q + `}`},
		"base64":      {&base64Tool{dangerousConfig: dc}, `{"path":` + q + `}`},
		"checksum":    {&checksumTool{dangerousConfig: dc}, `{"path":` + q + `}`},
		"diff":        {&diffTool{dangerousConfig: dc}, `{"path_a":` + q + `,"path_b":` + q + `}`},
		"json_query":  {&jsonQueryTool{dangerousConfig: dc}, `{"path":` + q + `,"query":"."}`},
		"file_info":   {&fileInfoTool{dangerousConfig: dc}, `{"path":` + q + `}`},
		"patch":       {&patchTool{dangerousConfig: dc}, `{"path":` + q + `,"old_string":"private","new_string":"x"}`},
		"write_file":  {&writeFileTool{dangerousConfig: dc}, `{"path":` + q + `,"content":"x"}`},
		"search_file": {&searchFilesTool{dangerousConfig: dc}, `{"pattern":"private","path":` + q + `}`},
		"search_dir":  {&searchFilesTool{dangerousConfig: dc}, `{"pattern":"private","path":` + tgresQuote(filepath.Dir(p)) + `}`},
		"tree":        {&treeTool{dangerousConfig: dc}, `{"path":` + tgresQuote(filepath.Dir(p)) + `}`},
		"glob":        {&globTool{dangerousConfig: dc}, `{"pattern":"*","path":` + tgresQuote(filepath.Dir(p)) + `}`},
	}
	for name, c := range cases {
		c.tool.SetContext(withTelegramMediaChat(context.Background(), 222))
		out, _ := c.tool.Call(c.args)
		if !strings.Contains(out, "different chat") || strings.Contains(out, "private contents") {
			t.Errorf("%s: other chat's media not refused: %q", name, out)
		}
	}
	// A broad search root that contains the media directory skips it.
	home, _ := os.UserHomeDir()
	st := &searchFilesTool{dangerousConfig: dc}
	st.SetContext(withTelegramMediaChat(context.Background(), 222))
	out, _ := st.Call(`{"pattern":"private","path":` + tgresQuote(filepath.Join(home, ".odek")) + `}`)
	if strings.Contains(out, "private contents") {
		t.Errorf("broad search read another chat's media: %q", out)
	}
	// The owner and non-Telegram runs are unaffected.
	rt := &readFileTool{dangerousConfig: dc}
	rt.SetContext(withTelegramMediaChat(context.Background(), 111))
	if out, err := rt.Call(`{"path":` + q + `}`); err != nil || !strings.Contains(out, "private contents") {
		t.Errorf("owner refused: %q %v", out, err)
	}
}
