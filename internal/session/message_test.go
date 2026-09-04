package session

import (
	"encoding/json"
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

func TestUnknownRole(t *testing.T) {
	if UnknownRole("user") || UnknownRole("TOOL") {
		t.Fatal("canonical roles must be known")
	}
	if !UnknownRole("system_override") {
		t.Fatal("unknown role not flagged")
	}
}
