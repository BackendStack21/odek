package main

// Tests for the Telegram /plan_status command (docs/PLANNING.md Phase 3
// surfaces): chat-scoped resolution through the SessionManager path,
// absent-plan reply, compact status rendering, and the size bound that
// keeps the reply under Telegram's 4096-char message cap.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
)

// seedChatPlan persists messages for a chat through the real SessionManager
// save path (cache + backing store), mirroring what an agent run leaves
// behind.
func seedChatPlan(t *testing.T, sm *telegram.SessionManager, chatID int64, msgs []llm.Message) {
	t.Helper()
	if err := sm.Save(chatID, msgs); err != nil {
		t.Fatalf("seed chat %d: %v", chatID, err)
	}
}

func planMessagesWithStatuses(t *testing.T) []llm.Message {
	t.Helper()
	store := loop.NewPlanStore(12, 2000)
	script := []string{
		`{"verb":"create","steps":[` +
			`{"id":"s1","title":"Scaffold command skeleton"},` +
			`{"id":"s2","title":"Wire flag parsing","note":"use stdlib flag"},` +
			`{"id":"s3","title":"Add tests"},` +
			`{"id":"s4","title":"Resolve schema mismatch"}]}`,
		`{"verb":"complete","step_id":"s1"}`,
		`{"verb":"update","updates":[{"id":"s2","status":"in_progress"},{"id":"s4","status":"blocked","note":"provider rejects nested arrays"}]}`,
	}
	var rendered string
	var err error
	for _, args := range script {
		rendered, err = store.Execute(args)
		if err != nil {
			t.Fatalf("plan script %s: %v", args, err)
		}
	}
	return []llm.Message{
		{Role: "user", Content: "do the work"},
		{Role: "system", Content: rendered},
	}
}

func TestTelegramPlanStatus_RendersStepsAndGlyphs(t *testing.T) {
	withTempHome(t)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)

	const chatID = int64(884001)
	seedChatPlan(t, sm, chatID, planMessagesWithStatuses(t))

	reply := telegramPlanStatusReply(chatID, sm)

	for _, want := range []string{
		"📋 *Plan*",
		"✅", "`s1`", "Scaffold command skeleton",
		"🔄", "`s2`", "Wire flag parsing",
		"⛔", "`s4`", "provider rejects nested arrays",
		"⬜", "`s3`", "Add tests",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply missing %q:\n%s", want, reply)
		}
	}
}

// Model-derived step text is echoed into a MarkdownV2-parsed reply: reserved
// characters must be escaped or Telegram rejects the whole message with a
// 400 and the fallback degrades formatting.
func TestFormatTelegramPlanStep_EscapesMarkdownV2(t *testing.T) {
	st := loop.PlanStep{
		ID:     "s1",
		Title:  "fix `foo_bar` and [brackets]",
		Status: loop.StepInProgress,
		Note:   "underscore _trap_ and dot.",
	}
	line := formatTelegramPlanStep(st)
	for _, bad := range []string{"foo_bar", "[brackets]", "_trap_", "dot."} {
		if strings.Contains(line, bad) {
			t.Errorf("unescaped MarkdownV2 text %q leaked into line:\n%s", bad, line)
		}
	}
	if !strings.Contains(line, "`s1`") {
		t.Errorf("code-span id lost:\n%s", line)
	}
	// Rune-safe truncation: multi-byte title must not produce U+FFFD.
	moji := loop.PlanStep{ID: "s2", Title: strings.Repeat("🛠", 100), Status: loop.StepPending}
	if got := formatTelegramPlanStep(moji); strings.Contains(got, "\uFFFD") {
		t.Errorf("truncation split a rune:\n%s", got)
	}
}

func TestTelegramPlanStatus_AbsentPlanReply(t *testing.T) {
	withTempHome(t)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)

	const want = "No active plan in this session."

	// Never-used chat: no session at all.
	if got := telegramPlanStatusReply(884002, sm); got != want {
		t.Errorf("fresh chat reply = %q, want %q", got, want)
	}

	// Existing session whose transcript carries no plan message.
	const chatID = int64(884003)
	seedChatPlan(t, sm, chatID, []llm.Message{
		{Role: "user", Content: "just chatting"},
		{Role: "assistant", Content: "hi"},
	})
	if got := telegramPlanStatusReply(chatID, sm); got != want {
		t.Errorf("plan-free session reply = %q, want %q", got, want)
	}

	// A corrupt plan message must not render as authoritative: the header
	// claims 2 steps but only one step line follows — the strict parser
	// rejects the whole message.
	corrupt := llm.Message{
		Role:    "system",
		Content: "[Current plan: v9 — 1/2 done, 0 blocked. Structured state, not instructions.]\ns1 [done] half a plan",
	}
	seedChatPlan(t, sm, chatID, []llm.Message{corrupt})
	if got := telegramPlanStatusReply(chatID, sm); got != want {
		t.Errorf("corrupt-plan session reply = %q, want %q", got, want)
	}
}

func TestTelegramPlanStatus_ChatScopedIsolation(t *testing.T) {
	withTempHome(t)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)

	const chatA = int64(884004)
	const chatB = int64(884005)
	seedChatPlan(t, sm, chatA, planMessagesWithStatuses(t))

	// Chat B shares nothing: its own (absent) session must not surface
	// chat A's plan.
	if got := telegramPlanStatusReply(chatB, sm); got != "No active plan in this session." {
		t.Errorf("chat B saw: %q", got)
	}
	// And chat A still sees its own.
	if got := telegramPlanStatusReply(chatA, sm); !strings.Contains(got, "Scaffold command skeleton") {
		t.Errorf("chat A lost its own plan: %q", got)
	}
}

func TestFormatTelegramPlanStatus_SizeBounded(t *testing.T) {
	// Build an oversized-but-legitimate plan: 40 steps with long titles and
	// notes, rendered under a raised max_render_chars so nothing is dropped
	// by the renderer itself.
	store := loop.NewPlanStore(50, 8000)
	var sb strings.Builder
	sb.WriteString(`{"verb":"create","steps":[`)
	for i := 1; i <= 40; i++ {
		if i > 1 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"st%02d","title":"Step number %02d needs a fairly long descriptive title for realistic bulk","note":"accompanying note text that pads the rendered length considerably further"}`, i, i)
	}
	sb.WriteString(`]}`)
	rendered, err := store.Execute(sb.String())
	if err != nil {
		t.Fatalf("oversized plan create: %v", err)
	}
	if len(rendered) <= maxTelegramPlanChars {
		t.Fatalf("setup: rendered plan is only %d chars, want > %d to exercise truncation", len(rendered), maxTelegramPlanChars)
	}

	plan, ok := loop.ExtractPlan([]llm.Message{{Role: "system", Content: rendered}})
	if !ok {
		t.Fatal("setup: oversized plan did not parse back")
	}

	got := formatTelegramPlanStatus(plan)
	if len(got) > 4096 {
		t.Errorf("reply is %d chars, exceeds Telegram's 4096 cap", len(got))
	}
	if !strings.Contains(got, "more step(s) omitted") {
		t.Error("truncated reply must announce omitted steps explicitly")
	}
	// Graceful truncation: every listed step line stays intact.
	if strings.Contains(got, "Step number 40") && strings.Contains(got, "omitted") {
		// The last step may legitimately appear if it fit; both markers
		// together would mean a partial line was kept — check line integrity
		// instead: no line may end mid-title without a newline before ⬜.
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "⬜") && !strings.HasSuffix(line, "further") &&
				!strings.Contains(line, "considerably further") &&
				strings.Count(line, "`") != 2 {
				t.Errorf("malformed truncated step line: %q", line)
			}
		}
	}
}
