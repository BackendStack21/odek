package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/skills"
	"github.com/BackendStack21/odek/internal/telegram"
)

const memfix1112cSkillMarker = "SKILL-BODY-MARKER-TG-1112C"

// memfix1112cSkillTurn runs one Telegram chat turn with msg on ctx, with a
// lazy k8s skill installed under the fixture HOME, and reports whether any
// model request carried the skill body.
func memfix1112cSkillTurn(t *testing.T, chatID int64, ctx context.Context, msg string) bool {
	t.Helper()
	sm, home := tgh1112Env(t, chatID)
	skillDir := filepath.Join(home, ".odek", "skills", "k8s-deploy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: k8s-deploy\ndescription: deploy helper\nodek:\n  trigger:\n    topic: kubernetes cluster helm\n    action: deploy rollout\n---\n\n" +
		memfix1112cSkillMarker + " Apply manifests with kubectl and watch the rollout.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen atomic.Bool
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), memfix1112cSkillMarker) {
			seen.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer llm.Close()
	resolved := config.ResolvedConfig{
		Model:           "test-model",
		BaseURL:         llm.URL,
		APIKey:          "sk-test",
		MaxIter:         4,
		ToolProgress:    "off",
		InteractionMode: "off",
		Skills:          skills.SkillsConfig{MaxLazySlots: 3},
	}
	bot := tgh1112Bot(t)
	handleChatMessageGated(ctx, chatID, 5, 42, msg, bot, telegram.NewHandler(bot), sm,
		resolved, "sys", telegram.NewNopLogger())
	return seen.Load()
}

// A voice note is the chat member's own request, but the turn's message
// wraps the transcript as untrusted content, which lazy-skill matching
// ignores. The media callback supplies the transcript as the principal's
// words, and the skill loads.
func TestRED_TelegramVoiceTranscriptLoadsLazySkill(t *testing.T) {
	const chatID = int64(88111301)
	const said = "deploy to the kubernetes cluster with helm"
	ctx := withTelegramSkillText(context.Background(), said, said, false)
	if !memfix1112cSkillTurn(t, chatID, ctx, telegramVoiceMessage(chatID, said)) {
		t.Fatal("the member's voice request did not load its lazy skill")
	}
}

// A forwarded recording or caption, and text the guard flagged, are not the
// member's words: they never load a skill. The caption of a document does.
func TestTelegramSkillTextOnlyForTheMembersOwnWords(t *testing.T) {
	const said = "deploy to the kubernetes cluster with helm"
	bg := context.Background()
	if memfix1112cSkillTurn(t, 88111302, withTelegramSkillText(bg, said, said, true), telegramVoiceMessage(88111302, said)) {
		t.Fatal("a forwarded voice note loaded a skill")
	}
	flagged := "⚠️ SECURITY NOTICE: …\n\n" + said
	if memfix1112cSkillTurn(t, 88111303, withTelegramSkillText(bg, said, flagged, false), telegramVoiceMessage(88111303, flagged)) {
		t.Fatal("a guard-flagged transcript loaded a skill")
	}
	if !memfix1112cSkillTurn(t, 88111304, withTelegramSkillText(bg, said, said, false),
		telegramDocumentMessage("/tmp/odek-media/doc.txt", "notes.txt", said)) {
		t.Fatal("the member's document caption did not load its lazy skill")
	}
	if got := telegramSkillText(withTelegramSkillText(bg, "  ", "  ", false)); got != "" {
		t.Fatalf("blank text attached: %q", got)
	}
}
