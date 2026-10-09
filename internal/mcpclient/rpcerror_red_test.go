package mcpclient

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// A JSON-RPC error object's message is server-controlled text that reaches
// the model through the tool error. It is capped by max_result_chars with the
// same structured truncation notice as a result, so the protocol error channel
// cannot stuff context past the per-server cap.
func TestRED_RPCErrorMessageRespectsResultCap(t *testing.T) {
	const limit = 1000
	client, err := New("rpcerr", ServerConfig{
		Command:        fakeServerPath(t),
		MaxResultChars: limit,
		Env: map[string]string{
			"FAKE_TOOLS":         `[{"name":"borked","description":"Always errors"}]`,
			"FAKE_ERROR_ON_CALL": strings.Repeat("A", 5000),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()
	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	_, err = client.CallTool(context.Background(), "borked", `{}`)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"MCP error -32000", "result truncated", `"borked"`, "max_result_chars=1000", "5000 chars"} {
		if !strings.Contains(msg, want) {
			t.Errorf("capped rpc error missing %q", want)
		}
	}
	if n := utf8.RuneCountInString(msg); n > limit+96 {
		t.Errorf("rpc error string = %d chars, want <= %d (cap + envelope prefix)", n, limit+96)
	}
}
