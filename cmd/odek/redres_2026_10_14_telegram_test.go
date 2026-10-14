package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
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
		runChatMediaOffLoop(chatA, nil, func(context.Context) {
			close(started)
			<-release
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			done <- struct{}{}
		})
		// Same chat: queued behind the first, order preserved.
		runChatMediaOffLoop(chatA, nil, func(context.Context) {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			done <- struct{}{}
		})
		// A panicking job is contained and does not stall the chat's queue.
		runChatMediaOffLoop(chatA, nil, func(context.Context) { panic("boom") })
		runChatMediaOffLoop(chatA, nil, func(context.Context) {
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
	runChatMediaOffLoop(chatB, nil, func(context.Context) { close(other) })
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
	runChatMediaOffLoop(chat, nil, func(context.Context) {
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

func TestRED_TelegramListingsSkipOtherChatsMedia(t *testing.T) {
	p := tgresMediaFile(t, "doc_chat111_secret_ab.txt")
	own := filepath.Join(filepath.Dir(p), "doc_chat222_mine_cd.txt")
	if err := os.WriteFile(own, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	root := tgresQuote(filepath.Join(home, ".odek"))
	ctx := withTelegramMediaChat(context.Background(), 222)
	allow := "allow"
	dc := danger.DangerousConfig{DefaultAction: &allow}

	// Control: outside a Telegram turn the same listing does show the file.
	gc := &globTool{dangerousConfig: dc}
	if out, _ := gc.Call(`{"pattern":"**/*","path":` + root + `}`); !strings.Contains(out, "chat111") {
		t.Fatalf("control listing does not show the media file: %q", out)
	}
	g := &globTool{dangerousConfig: dc}
	g.SetContext(ctx)
	out, _ := g.Call(`{"pattern":"**/*","path":` + root + `}`)
	if strings.Contains(out, "chat111") {
		t.Errorf("glob listed another chat's media: %q", out)
	}
	s := &searchFilesTool{dangerousConfig: dc}
	s.SetContext(ctx)
	out, _ = s.Call(`{"pattern":"*","target":"files","path":` + root + `}`)
	if strings.Contains(out, "chat111") {
		t.Errorf("search_files(files) listed another chat's media: %q", out)
	}
	tr := &treeTool{dangerousConfig: dc}
	tr.SetContext(ctx)
	out, _ = tr.Call(`{"path":` + root + `,"include_hidden":true}`)
	if strings.Contains(out, "chat111") {
		t.Errorf("tree listed another chat's media: %q", out)
	}
}

func TestRED_ChecksumCallContextIsPerInvocation(t *testing.T) {
	p := tgresMediaFile(t, "doc_chat111_secret_ab.txt")
	c := &checksumTool{dangerousConfig: danger.DangerousConfig{}}
	// The shared instance carries no context; the invocation's own does.
	out, _ := c.CallContext(withTelegramMediaChat(context.Background(), 222), `{"path":`+tgresQuote(p)+`}`)
	if !strings.Contains(out, "different chat") {
		t.Errorf("CallContext ignored the invocation context: %q", out)
	}
	out, _ = c.CallContext(withTelegramMediaChat(context.Background(), 111), `{"path":`+tgresQuote(p)+`}`)
	if strings.Contains(out, "different chat") || !strings.Contains(out, "hash") {
		t.Errorf("owner call failed: %q", out)
	}
}

func TestRED_TelegramStopAndNewCancelPendingMediaJobs(t *testing.T) {
	const chat int64 = 910030
	release := make(chan struct{})
	started := make(chan context.Context, 1)
	ranSecond := make(chan struct{}, 1)
	runChatMediaOffLoop(chat, nil, func(ctx context.Context) {
		started <- ctx
		<-release
	})
	runChatMediaOffLoop(chat, nil, func(context.Context) { ranSecond <- struct{}{} })
	first := <-started

	if !cancelChatMediaJobs(chat) {
		t.Fatal("pending media jobs were not reported")
	}
	if first.Err() == nil {
		t.Error("running media job's context was not cancelled")
	}
	close(release)
	// A later reset on the same gate chain completes, and the queued job
	// cancelled before it started never ran.
	done := make(chan struct{})
	runChatResetOffLoop(chat, func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gate chain stalled")
	}
	select {
	case <-ranSecond:
		t.Error("cancelled queued media job still ran")
	default:
	}
	if cancelChatMediaJobs(chat) {
		t.Error("registry not cleaned up")
	}
}

// A media turn queued behind a running text turn (waiting for the chat slot)
// must not run once /stop or /new cancelled it.
func TestRED_TelegramCancelledMediaTurnNeverRunsAfterSlotWait(t *testing.T) {
	const chatID = int64(88140001)
	sm := tgrh13Env(t, chatID)
	bot, notify := tgrh13Bot(t)
	var mu sync.Mutex
	mediaSeen := false
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		if strings.Contains(body, "MEDIA-MARKER") {
			mu.Lock()
			mediaSeen = true
			mu.Unlock()
		}
		if strings.Contains(body, "migrate the db") && !strings.Contains(body, "call_1") {
			resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{
				"content": "",
				"tool_calls": []map[string]any{{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "clarify", "arguments": `{"question":"Proceed with the migration?"}`},
				}},
			}}}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(llm.Close)
	handler := telegram.NewHandler(bot)
	resolved := tgrh13Resolved(llm.URL)

	textDone := make(chan struct{})
	go func() {
		defer close(textDone)
		handleChatMessage(chatID, 5, 42, "migrate the db", bot, handler, sm, resolved, "sys", telegram.NewNopLogger())
	}()
	tgrh13AwaitPrompt(t, notify, "Proceed with the migration")

	mediaDone := make(chan struct{})
	runChatMediaOffLoop(chatID, nil, func(ctx context.Context) {
		defer close(mediaDone)
		handleChatMessageGated(ctx, chatID, 6, 42, "MEDIA-MARKER photo", bot, handler, sm, resolved, "sys", telegram.NewNopLogger())
	})
	tgrh13AwaitPrompt(t, notify, "queued") // the media turn is waiting for the slot

	// What /stop does: cancel pending media jobs and the running turn.
	cancelChatTurn(chatID)
	select {
	case <-textDone:
	case <-time.After(10 * time.Second):
		t.Fatal("text turn did not stop")
	}
	select {
	case <-mediaDone:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled media turn did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if mediaSeen {
		t.Fatal("cancelled media turn still ran against the model")
	}
}
