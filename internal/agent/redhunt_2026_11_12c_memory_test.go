package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
)

// rh1112cSkillAgent builds an agent with one lazy k8s skill and a stub LLM.
// It returns the agent and a function reporting whether a request carried
// the skill body.
func rh1112cSkillAgent(t *testing.T) (*Agent, func() bool) {
	t.Helper()
	const marker = "SKILL-BODY-MARKER-1112C"
	var mu sync.Mutex
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = seen || strings.Contains(string(body), marker)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "k8s-deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: k8s-deploy\ndescription: deploy helper\nodek:\n  trigger:\n    topic: kubernetes cluster helm\n    action: deploy rollout\n---\n\n" +
		marker + " Apply manifests with kubectl and watch the rollout.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ag, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		MemoryDir:     t.TempDir(),
		Skills:        &skills.SkillsConfig{MaxLazySlots: 3},
		SkillManager:  skills.NewSkillManager(dir, filepath.Join(t.TempDir(), "none")),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag, func() bool { mu.Lock(); defer mu.Unlock(); return seen }
}

// rh1112cVoiceMessage is a Telegram voice note as the model sees it: the
// transcript wrapped as untrusted content.
func rh1112cVoiceMessage(transcript string) string {
	return `<untrusted_content_0123456789abcdef source="telegram:chat:1:voice">` + "\n" + transcript + "\n</untrusted_content_0123456789abcdef>"
}

// The surface hands the agent the principal's own words for the opening
// message; lazy skills load on them although the message wraps them.
func TestRED_AgentSkillMatchTextLoadsSkillForWrappedRequest(t *testing.T) {
	const said = "deploy to the kubernetes cluster with helm"
	msg := rh1112cVoiceMessage(said)

	ag, loaded := rh1112cSkillAgent(t)
	if _, _, err := ag.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: msg}}); err != nil {
		t.Fatal(err)
	}
	if loaded() {
		t.Fatal("fixture: the wrapped message alone loaded the skill")
	}

	ag, loaded = rh1112cSkillAgent(t)
	ag.SetSkillMatchText(msg, said)
	if _, _, err := ag.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: msg}}); err != nil {
		t.Fatal(err)
	}
	if !loaded() {
		t.Fatal("the principal's transcript did not load its lazy skill")
	}
}

// The text is bound to one message: another message, or a cleared binding,
// is matched on its own unwrapped text only.
func TestAgentSkillMatchTextBoundToItsMessage(t *testing.T) {
	const said = "deploy to the kubernetes cluster with helm"
	ag, loaded := rh1112cSkillAgent(t)
	ag.SetSkillMatchText(rh1112cVoiceMessage("something else"), said)
	if _, _, err := ag.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: rh1112cVoiceMessage(said)}}); err != nil {
		t.Fatal(err)
	}
	if loaded() {
		t.Fatal("principal text bound to another message was used")
	}

	ag, loaded = rh1112cSkillAgent(t)
	msg := rh1112cVoiceMessage(said)
	ag.SetSkillMatchText(msg, said)
	ag.SetSkillMatchText(msg, "")
	if _, _, err := ag.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: msg}}); err != nil {
		t.Fatal(err)
	}
	if loaded() {
		t.Fatal("a cleared binding was used")
	}
	var nilAgent *Agent
	nilAgent.SetSkillMatchText(msg, said) // must not panic
}
