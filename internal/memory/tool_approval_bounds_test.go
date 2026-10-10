package memory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/memory/approval"
)

func TestRED_MemoryToolApproval_ShowsLongContentInFull(t *testing.T) {
	rec := &recordingApprover{}
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)
	tool.SetDangerousConfig(&danger.DangerousConfig{
		Classes:  map[danger.RiskClass]danger.Action{danger.Persistence: danger.Prompt},
		Approver: rec,
	})
	content := strings.Repeat("a", 700) + "HIDDEN-MIDDLE-PAYLOAD" + strings.Repeat("b", 700)
	_, _ = tool.Call(`{"action":"add","target":"user","content":` + approvalJSONQuote(content) + `}`)
	if len(rec.ops) != 1 {
		t.Fatalf("want one prompt, got %d", len(rec.ops))
	}
	if !strings.Contains(rec.ops[0].Resource, content) {
		t.Fatalf("approval does not show the full content: %q", rec.ops[0].Resource)
	}
}

func TestRED_MemoryTool_RefusesContentOverDisplayBound(t *testing.T) {
	for _, args := range []string{
		`{"action":"add","target":"user","content":` + approvalJSONQuote(strings.Repeat("x", approval.MaxTextBytes+1)) + `}`,
		`{"action":"replace","target":"user","old_text":"y","content":` + approvalJSONQuote(strings.Repeat("x", approval.MaxTextBytes+1)) + `}`,
		`{"action":"replace","target":"user","old_text":` + approvalJSONQuote(strings.Repeat("y", approval.MaxTextBytes+1)) + `,"content":"z"}`,
		`{"action":"add_atom","content":` + approvalJSONQuote(strings.Repeat("é", approval.MaxTextBytes/2+1)) + `}`,
	} {
		rec := &recordingApprover{}
		mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
		tool := NewMemoryTool(mm)
		tool.SetDangerousConfig(&danger.DangerousConfig{
			Classes:  map[danger.RiskClass]danger.Action{danger.Persistence: danger.Prompt},
			Approver: rec,
		})
		res, _ := tool.Call(args)
		var out map[string]any
		if err := json.Unmarshal([]byte(res), &out); err != nil {
			t.Fatal(err)
		}
		if out["success"] != false || !strings.Contains(res, "split") {
			t.Errorf("over-bound write not refused with a split hint: %s", res)
		}
		if len(rec.ops) != 0 {
			t.Errorf("over-bound write reached the approver")
		}
	}
}
