package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	golangws "golang.org/x/net/websocket"
)

// Assistant notes must arrive before the tool they introduce, including
// when the provider answers an SSE request with buffered JSON instead.
func TestServe_OpenAIPreToolNotes(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, mode := range []string{"buffered", "json_fallback", "streamed", "buffered_final"} {
			t.Run(fmt.Sprintf("responses_%v_%s", responses, mode), func(t *testing.T) {
				llmSrv := mockLLM(t, func(w http.ResponseWriter, call int) {
					text := "The result is 2."
					if call == 1 {
						text = "I will calculate the result."
					}
					if mode == "streamed" || (mode == "buffered_final" && call == 1) {
						w.Header().Set("Content-Type", "text/event-stream")
						emit := func(event any) { raw, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", raw) }
						if responses {
							emit(map[string]any{"type": "response.output_text.delta", "delta": text})
							if call == 1 {
								emit(map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"type": "function_call", "call_id": "calc", "name": "math_eval"}})
								emit(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "delta": `{"expression":"1+1"}`})
							}
							emit(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
						} else {
							emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
							finish := "stop"
							if call == 1 {
								finish = "tool_calls"
								emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "calc", "type": "function", "function": map[string]any{"name": "math_eval", "arguments": `{"expression":"1+1"}`}}}}}}})
							}
							emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
							fmt.Fprint(w, "data: [DONE]\n\n")
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if responses {
						phase := "final_answer"
						if call == 1 {
							phase = "commentary"
						}
						items := []any{map[string]any{"type": "message", "role": "assistant", "phase": phase, "content": []any{map[string]any{"type": "output_text", "text": text}}}}
						if call == 1 {
							items = append(items, map[string]any{"type": "function_call", "call_id": "calc", "name": "math_eval", "arguments": `{"expression":"1+1"}`})
						}
						json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": items}) //nolint:errcheck
						return
					}
					if call == 1 {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"I will calculate the result.","tool_calls":[{"id":"calc","type":"function","function":{"name":"math_eval","arguments":"{\"expression\":\"1+1\"}"}}]},"finish_reason":"tool_calls"}]}`)
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"The result is 2."},"finish_reason":"stop"}]}`)
					}
				})
				defer llmSrv.Close()
				defer setTestEnv(t, llmSrv.URL)()
				ln, mux := buildServeMuxV2(t, newTestSessionStore(t), func(rc *config.ResolvedConfig) {
					rc.Stream = mode != "buffered"
					rc.Provider, rc.Model, rc.APIKey, rc.BaseURL = "openai", "gpt-4o", "test-key", llmSrv.URL
					if responses {
						rc.Model = "gpt-5.6-luna"
					}
				})
				defer startServeTest(t, ln, mux)()
				waitForHTTP(t, ln.Addr().String())
				wsUpgradeLimiter.reset()
				conn := dialTestWS(t, ln.Addr().String())
				defer conn.Close()
				readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })
				writeJSON(conn, map[string]any{"type": "prompt", "content": "Calculate 1+1 using math_eval."})
				conn.SetReadDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
				notes, answers := 0, 0
				for {
					var raw []byte
					if err := golangws.Message.Receive(conn, &raw); err != nil {
						t.Fatal(err)
					}
					var event map[string]any
					if err := json.Unmarshal(raw, &event); err != nil {
						t.Fatal(err)
					}
					switch event["type"] {
					case "token", "token_delta":
						if event["content"] == "I will calculate the result." {
							notes++
						}
						if event["content"] == "The result is 2." {
							answers++
						}
					case "tool_call":
						if notes != 1 {
							t.Errorf("tool arrived before its note: notes=%d", notes)
						}
					case "error":
						t.Fatalf("unexpected error: %v", event)
					case "done":
						if notes != 1 || answers != 1 {
							t.Errorf("notes=%d answers=%d; want one each", notes, answers)
						}
						return
					}
				}
			})
		}
	}
}
