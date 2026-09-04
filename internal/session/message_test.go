package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolCall_UnmarshalV1Nested(t *testing.T) {
	raw := []byte(`{"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`)
	var tc ToolCall
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatal(err)
	}
	if tc.ID != "c1" || tc.Function.Name != "shell" || tc.Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("v1 unmarshal = %+v", tc)
	}
}

func TestToolCall_UnmarshalFlat(t *testing.T) {
	raw := []byte(`{"id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}`)
	var tc ToolCall
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatal(err)
	}
	if tc.Function.Name != "shell" || tc.Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("flat unmarshal = %+v", tc)
	}
	if tc.Type != "function" {
		t.Fatalf("type = %q, want function", tc.Type)
	}
}

func TestMessage_ThinkingSignatureRoundTrip(t *testing.T) {
	m := Message{Role: "assistant", Content: "ok", ReasoningContent: "think", ThinkingSignature: "sig"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ThinkingSignature != "sig" || got.ReasoningContent != "think" {
		t.Fatalf("round-trip = %+v", got)
	}
}

func TestMessage_ToolNamePrefersName(t *testing.T) {
	if (Message{Name: "shell"}).ToolName() != "shell" {
		t.Fatal("v1 name")
	}
	if (Message{}).ToolName() != "" {
		t.Fatal("empty")
	}
}

func TestMessage_MarshalKeepsNestedFunction(t *testing.T) {
	m := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   "c1",
			Type: "function",
		}},
	}
	m.ToolCalls[0].Function.Name = "shell"
	m.ToolCalls[0].Function.Arguments = `{}`
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(b, `"function"`) || !jsonContains(b, `"name":"shell"`) {
		t.Fatalf("v1 nested shape lost: %s", b)
	}
}

func jsonContains(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}

func TestMessage_UnmarshalV1NestedToolCalls(t *testing.T) {
	raw := []byte(`{
		"role":"assistant",
		"content":"",
		"tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{}"}}]
	}`)
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "shell" {
		t.Fatalf("nested tool_calls lost: %+v", m.ToolCalls)
	}
}

func TestUnknownRole(t *testing.T) {
	if UnknownRole("user") || UnknownRole("TOOL") {
		t.Fatal("canonical roles must be known")
	}
	if !UnknownRole("system_override") {
		t.Fatal("unknown role not flagged")
	}
}
