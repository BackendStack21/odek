package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
)

func newTestDeliverer(t *testing.T) (telegramDeliverer, *telegram.SessionManager, <-chan string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)
	bot, recv := newRecordingTestBot(t)
	d := telegramDeliverer{
		bot:      bot,
		fallback: cliDeliverer{resolved: config.ResolvedConfig{}},
		sessions: sm,
		log:      schedule.NopLogger{},
	}
	return d, sm, recv
}

// TestScheduleDeliver_RecordsIntoExistingSession verifies Option B: a delivered
// scheduled result is appended to the target chat's existing conversation as a
// labeled user turn + the assistant result, so a follow-up message sees it.
func TestScheduleDeliver_RecordsIntoExistingSession(t *testing.T) {
	d, sm, recv := newTestDeliverer(t)
	chatID := int64(5551)
	if err := sm.Save(chatID, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	job := schedule.Job{
		ID: "jb-1", Name: "daily digest", Task: "summarize my day",
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: chatID},
	}
	if err := d.Deliver(context.Background(), job, "the digest"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// The message was actually sent.
	select {
	case got := <-recv:
		if !strings.Contains(got, "digest") {
			t.Errorf("sent text = %q, want it to contain 'digest'", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message was sent to Telegram")
	}

	// The conversation now ends with the scheduled exchange.
	cs, err := sm.Load(chatID)
	if err != nil || cs == nil {
		t.Fatalf("Load: %v, cs=%v", err, cs)
	}
	if len(cs.Messages) != 5 {
		t.Fatalf("expected 5 messages (3 seed + 2 scheduled), got %d", len(cs.Messages))
	}
	userTurn := cs.Messages[3]
	if userTurn.Role != "user" || !strings.Contains(userTurn.Content, "scheduled task") || !strings.Contains(userTurn.Content, "summarize my day") {
		t.Errorf("scheduled user turn wrong: %+v", userTurn)
	}
	asstTurn := cs.Messages[4]
	if asstTurn.Role != "assistant" || asstTurn.Content != "the digest" {
		t.Errorf("assistant result turn wrong: %+v", asstTurn)
	}
}

// TestScheduleDeliver_PreservesAlternationAfterUserEndingSession guards the
// edge where the session already ends on a bare user message (a turn cancelled
// before the agent replied, or a context-injection command). Appending another
// user turn would produce two consecutive user messages, which Anthropic
// rejects on the next call. The write-back must fold into a single assistant
// turn instead — and never produce two same-role messages in a row.
func TestScheduleDeliver_PreservesAlternationAfterUserEndingSession(t *testing.T) {
	d, sm, recv := newTestDeliverer(t)
	chatID := int64(5560)
	if err := sm.Save(chatID, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "an interrupted turn"}, // session ends on user
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	job := schedule.Job{
		ID: "jb-9", Name: "digest", Task: "summarize",
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: chatID},
	}
	if err := d.Deliver(context.Background(), job, "the result"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	<-recv

	cs, err := sm.Load(chatID)
	if err != nil || cs == nil {
		t.Fatalf("Load: %v cs=%v", err, cs)
	}
	// No two consecutive same-role messages anywhere.
	for i := 1; i < len(cs.Messages); i++ {
		if cs.Messages[i].Role == cs.Messages[i-1].Role {
			t.Fatalf("consecutive %q messages at %d: %+v", cs.Messages[i].Role, i, cs.Messages)
		}
	}
	// The result was recorded, and the session ends on an assistant turn.
	last := cs.Messages[len(cs.Messages)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "the result") {
		t.Errorf("last message should be the assistant result, got %+v", last)
	}
}

// TestScheduleDeliver_NoSessionNotCreated verifies a notification-only chat
// (never used interactively) is NOT given a session just because a schedule
// posted to it — avoiding an ever-growing transcript of scheduled posts.
func TestScheduleDeliver_NoSessionNotCreated(t *testing.T) {
	d, sm, recv := newTestDeliverer(t)
	chatID := int64(5552)

	job := schedule.Job{
		ID: "jb-2", Name: "ping", Task: "ping",
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: chatID},
	}
	if err := d.Deliver(context.Background(), job, "pong"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	select {
	case <-recv:
	case <-time.After(2 * time.Second):
		t.Fatal("no message was sent to Telegram")
	}

	cs, err := sm.Load(chatID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cs != nil {
		t.Errorf("a notification-only chat should not get a session, got %d messages", len(cs.Messages))
	}
}

// TestScheduleDeliver_EmptyResultNotRecorded verifies an empty result is not
// appended (nothing meaningful to record).
func TestScheduleDeliver_EmptyResultNotRecorded(t *testing.T) {
	d, sm, _ := newTestDeliverer(t)
	chatID := int64(5553)
	if err := sm.Save(chatID, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	job := schedule.Job{
		ID: "jb-3", Name: "noop", Task: "t",
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: chatID},
	}
	if err := d.Deliver(context.Background(), job, ""); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	cs, err := sm.Load(chatID)
	if err != nil || cs == nil {
		t.Fatalf("Load: %v cs=%v", err, cs)
	}
	if len(cs.Messages) != 1 {
		t.Errorf("empty result must not append, got %d messages", len(cs.Messages))
	}
}

// TestScheduleDeliver_NilSessionManagerNoPanic verifies the write-back is a
// safe no-op when no SessionManager is wired (e.g. the CLI daemon path).
func TestScheduleDeliver_NilSessionManagerNoPanic(t *testing.T) {
	bot, recv := newRecordingTestBot(t)
	d := telegramDeliverer{bot: bot, fallback: cliDeliverer{resolved: config.ResolvedConfig{}}}
	job := schedule.Job{
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: 5554},
	}
	if err := d.Deliver(context.Background(), job, "result"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case <-recv:
	case <-time.After(2 * time.Second):
		t.Fatal("no message was sent")
	}
}
