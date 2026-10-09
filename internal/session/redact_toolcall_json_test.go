package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRED_ToolCallArgumentsStayValidJSONAfterRedaction(t *testing.T) {
	cases := []struct {
		name, args string
		secrets    []string
	}{
		{"env write_file", `{"path":".env","content":"DB_PASSWORD=hunter2hunter2hunter2xyz\nPORT=8080\n"}`, []string{"hunter2hunter2hunter2xyz"}},
		{"shell export", `{"command":"export API_KEY=` + redhuntSecret + ` && run"}`, []string{redhuntSecret}},
		{"nested object", `{"a":{"b":{"headers":"Authorization: Bearer ` + redhuntSecret + `"}},"n":3}`, []string{redhuntSecret}},
		{"array", `["export TOKEN=` + redhuntSecret + `", {"k":"PASSWORD=hunter2hunter2hunter2xyz"}]`, []string{redhuntSecret, "hunter2hunter2hunter2xyz"}},
		{"invalid text", `not json PASSWORD=hunter2hunter2hunter2xyz {"x":`, []string{"hunter2hunter2hunter2xyz"}},
		{"secret key name", `{"` + redhuntSecret + `":"v"}`, []string{redhuntSecret}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, err := NewStoreWithDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var tc ToolCall
			tc.ID, tc.Type = "c1", "function"
			tc.Function.Name = "write_file"
			tc.Function.Arguments = c.args
			sess := &Session{ID: "20260101-bbbbbb", Messages: []Message{
				{Role: "system", Content: "s"},
				{Role: "user", Content: "go"},
				{Role: "assistant", ToolCalls: []ToolCall{tc}},
			}}
			if err := store.Save(sess); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.Messages[2].ToolCalls[0].Function.Arguments
			if !json.Valid([]byte(got)) {
				t.Fatalf("persisted arguments are not valid JSON: %q", got)
			}
			for _, s := range c.secrets {
				if strings.Contains(got, s) {
					t.Fatalf("secret %q persisted: %q", s, got)
				}
			}
		})
	}
}
