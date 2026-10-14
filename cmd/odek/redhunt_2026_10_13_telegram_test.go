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

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
)

// ── helpers (area prefix: tgrh13) ─────────────────────────────────────────

// tgrh13Bot is a fake Telegram API that accepts every call and reports each
// sendMessage body on notify.
func tgrh13Bot(t *testing.T) (*telegram.Bot, chan map[string]any) {
	t.Helper()
	notify := make(chan map[string]any, 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			select {
			case notify <- m:
			default:
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":1}}}`))
	}))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.FileBaseURL = ts.URL + "/file"
	return bot, notify
}

func tgrh13Env(t *testing.T, chatID int64) *telegram.SessionManager {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	deleteChatMutex(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)
	t.Cleanup(func() {
		deleteChatMutex(chatID)
		chatCancels.Delete(chatID)
		chatRunInfos.Delete(chatID)
		telegramTurnUsers.Delete(chatID)
		dropBGRuntimeForChat(chatID)
	})
	t.Cleanup(func() {
		odekDir := filepath.Join(home, ".odek")
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if err := os.RemoveAll(odekDir); err == nil || time.Now().After(deadline) {
				return
			}
		}
	})
	store, err := session.NewStoreWithDir(filepath.Join(home, ".odek", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return telegram.NewSessionManager(store, time.Hour)
}

// tgrh13ToolCallLLM answers the first request that offers the named tool with
// a single call to it, and every later request with a plain final answer.
func tgrh13ToolCallLLM(t *testing.T, toolName, args string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	sent := false
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		first := !sent && strings.Contains(string(raw), `"`+toolName+`"`)
		if first {
			sent = true
		}
		mu.Unlock()
		if !first {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": "",
			"tool_calls": []map[string]any{{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": toolName, "arguments": args},
			}},
		}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(llm.Close)
	return llm
}

func tgrh13Resolved(llmURL string) config.ResolvedConfig {
	return config.ResolvedConfig{
		Model:           "test-model",
		BaseURL:         llmURL,
		APIKey:          "sk-test",
		MaxIter:         4,
		ToolProgress:    "off",
		InteractionMode: "off",
	}
}

// tgrh13AwaitPrompt waits for a sendMessage whose text contains sub.
func tgrh13AwaitPrompt(t *testing.T, notify chan map[string]any, sub string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case m := <-notify:
			text, _ := m["text"].(string)
			if strings.Contains(strings.ReplaceAll(text, `\`, ""), sub) {
				return
			}
		case <-deadline:
			t.Fatalf("prompt containing %q was never sent", sub)
		}
	}
}

// ── findings ──────────────────────────────────────────────────────────────

// /stop (and /new, which runs the same cancel before archiving) cancels the
// turn's context, but a pending clarify waits only on its answer channel, a
// 10-minute timer and typingDone — and typingDone is closed by a defer of the
// very turn that is blocked in the tool call. The cancel therefore never
// reaches the clarify: the turn keeps the chat slot for up to 10 minutes.
// /new then blocks on that slot INLINE on the single update loop, freezing
// every chat (no message, command or approval callback is processed).
func TestRED_TelegramStopDoesNotInterruptPendingClarify(t *testing.T) {
	const chatID = int64(88130001)
	sm := tgrh13Env(t, chatID)
	bot, notify := tgrh13Bot(t)
	llm := tgrh13ToolCallLLM(t, "clarify", `{"question":"Proceed with the migration?"}`)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleChatMessage(chatID, 5, 42, "migrate the db", bot, telegram.NewHandler(bot), sm,
			tgrh13Resolved(llm.URL), "sys", telegram.NewNopLogger())
	}()
	tgrh13AwaitPrompt(t, notify, "Proceed with the migration")

	v, ok := chatCancels.Load(chatID)
	if !ok {
		t.Fatal("no cancel func registered for the running turn")
	}
	v.(context.CancelFunc)() // what /stop and /new do

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Unblock the stuck clarify so the goroutine can exit.
		pendingClarifyReqs.Range(func(k, val any) bool {
			select {
			case val.(*pendingClarifyReq).ch <- "no":
			default:
			}
			return true
		})
		<-done
		t.Fatal("turn still running 5s after /stop: the pending clarify ignores the cancel and holds the chat slot (up to 10 minutes); /new would block the update loop on it")
	}
}

// Same defect for approvals: TelegramApprover.Cancel() is never called on
// the Telegram surface, and PromptCommand waits only on a callback, Cancel or
// the 120 s timeout. A /stop or /new during a pending approval leaves the
// turn blocked for the full timeout, and /new freezes the update loop (and
// with it every chat, including the callback that could answer the prompt).
func TestRED_TelegramStopDoesNotInterruptPendingApproval(t *testing.T) {
	const chatID = int64(88130002)
	sm := tgrh13Env(t, chatID)
	bot, notify := tgrh13Bot(t)
	llm := tgrh13ToolCallLLM(t, "shell", `{"command":"python3 -c 'print(1)'"}`)

	handler := telegram.NewHandler(bot)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleChatMessage(chatID, 5, 42, "run the script", bot, handler, sm,
			tgrh13Resolved(llm.URL), "sys", telegram.NewNopLogger())
	}()
	tgrh13AwaitPrompt(t, notify, "Approval Required")

	v, ok := chatCancels.Load(chatID)
	if !ok {
		t.Fatal("no cancel func registered for the running turn")
	}
	v.(context.CancelFunc)() // what /stop and /new do

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if a := handler.GetApprover(chatID); a != nil {
			a.Cancel()
		}
		<-done
		t.Fatal("turn still running 5s after /stop: the pending approval ignores the cancel and holds the chat slot for the 120s approval timeout; /new would block the update loop on it")
	}
}
