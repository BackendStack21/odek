package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/telegram"
)

func TestRED_TelegramWakeTurnBindsUserOrFailsClosed(t *testing.T) {
	const chat = int64(-100777001)
	telegramTurnUsers.Delete(chat)
	t.Cleanup(func() { telegramTurnUsers.Delete(chat) })

	// No turn yet and several allowed users: no binding.
	cfg := telegram.TelegramConfig{AllowedChats: []int64{chat}, AllowedUsers: []int64{1, 2}}
	if got := telegramWakeUser(chat, cfg); got != 0 {
		t.Fatalf("unbound wake user = %d, want 0", got)
	}
	// Single allowed user: that user.
	if got := telegramWakeUser(chat, telegram.TelegramConfig{AllowedUsers: []int64{5}}); got != 5 {
		t.Fatalf("single allowed user binding = %d, want 5", got)
	}
	// The user who last started a turn wins; a zero user never overwrites.
	rememberTelegramTurnUser(chat, 42)
	rememberTelegramTurnUser(chat, 0)
	if got := telegramWakeUser(chat, cfg); got != 42 {
		t.Fatalf("last turn user binding = %d, want 42", got)
	}

	// An unbound wake approver denies without prompting anyone.
	bot, sent := newRecordingTestBot(t)
	a := newTelegramTurnApprover(bot, chat, 0, true)
	err := a.PromptCommand(danger.CodeExecution, "make build", "")
	if err == nil || !strings.Contains(err.Error(), "no bound user") {
		t.Fatalf("unbound wake approval err = %v, want fail-closed denial", err)
	}
	select {
	case msg := <-sent:
		t.Fatalf("unbound wake approval sent a prompt: %q", msg)
	default:
	}
	// An operator-turn approver keeps the legacy zero-user behavior.
	if newTelegramTurnApprover(bot, chat, 0, false) == nil {
		t.Fatal("nil approver")
	}

	// The persisted wake message carries the bg-wake marker; operator turns do not.
	if m := telegramTurnUserMessage("wake", true); m.Role != "user" || m.Name != "bg-wake" {
		t.Fatalf("wake message = %+v, want user/bg-wake", m)
	}
	if m := telegramTurnUserMessage("hi", false); m.Name != "" {
		t.Fatalf("operator message Name = %q, want empty", m.Name)
	}
}

// handleWakeTurn routes through the shared turn pipeline (and its panic
// recovery) with the resolved wake user.
func TestHandleWakeTurnUsesSharedPipeline(t *testing.T) {
	chatID := int64(88077)
	deleteChatMutex(chatID)
	t.Cleanup(func() { deleteChatMutex(chatID); chatCancels.Delete(chatID); chatRunInfos.Delete(chatID) })
	bot, msgCh := newRecordingTestBot(t)
	handler := telegram.NewHandler(bot)
	handleWakeTurn(chatID, tgWakePreamble, bot, handler, nil, config.ResolvedConfig{Model: "m"},
		"sys", telegram.NewNopLogger())
	select {
	case msg := <-msgCh:
		if !strings.Contains(msg, "Internal error") {
			t.Fatalf("unexpected message %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wake turn did not run the shared pipeline")
	}
	if _, ok := telegramTurnUsers.Load(chatID); ok {
		t.Fatal("a wake turn must not record itself as the chat's turn user")
	}
}

// A wake queued behind another user's turn binds to the user of the turn it
// actually follows: the binding is resolved after the chat slot is acquired.
func TestRED_TelegramWakeBindingResolvedAfterSlot(t *testing.T) {
	chatID := int64(88078)
	deleteChatMutex(chatID)
	telegramTurnUsers.Delete(chatID)
	t.Cleanup(func() {
		deleteChatMutex(chatID)
		chatCancels.Delete(chatID)
		chatRunInfos.Delete(chatID)
		telegramTurnUsers.Delete(chatID)
		onTelegramTurnApprover = nil
	})
	bound := make(chan int64, 1)
	onTelegramTurnApprover = func(a *telegram.TelegramApprover) {
		select {
		case bound <- a.UserID():
		default:
		}
	}

	rememberTelegramTurnUser(chatID, 1001) // user A
	slot := pinChat(chatID)
	slot.mu.Lock() // user B's turn holds the chat
	bot, msgCh := newRecordingTestBot(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleWakeTurn(chatID, tgWakePreamble, bot, telegram.NewHandler(bot), nil,
			config.ResolvedConfig{Model: "m"}, "sys", telegram.NewNopLogger())
	}()
	select { // the wake reports it is queued
	case <-msgCh:
	case <-time.After(2 * time.Second):
		t.Fatal("wake did not queue")
	}
	rememberTelegramTurnUser(chatID, 2002) // user B started that turn
	unpinChat(chatID, slot)
	<-done
	select {
	case got := <-bound:
		if got != 2002 {
			t.Fatalf("wake bound to user %d, want 2002 (the turn it followed)", got)
		}
	default:
		t.Fatal("wake turn built no approver")
	}
}
