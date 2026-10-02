package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/narrate"
	"github.com/BackendStack21/odek/internal/render"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestEngine_OpenAIResponsesNotes(t *testing.T) {
	for _, engaging := range []bool{false, true} {
		for _, mode := range []string{"buffered", "streamed", "fallback_after_first"} {
			t.Run(fmt.Sprintf("engaging_%v_%s", engaging, mode), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/responses" {
						t.Errorf("path=%q", r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if calls > 1 && !strings.Contains(fmt.Sprint(body["input"]), "Note 1.") {
						t.Error("assistant note lost from replay")
					}
					text := fmt.Sprintf("Note %d.", calls)
					items := []any{map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
					if calls <= 2 {
						items = append(items, map[string]any{"type": "function_call", "call_id": fmt.Sprint(calls), "name": "echo", "arguments": "{}"})
					} else {
						text = "Final answer."
						items = []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
					}
					response := map[string]any{"status": "completed", "output": items}
					stream, _ := body["stream"].(bool)
					if stream && (mode == "streamed" || calls == 1) {
						w.Header().Set("Content-Type", "text/event-stream")
						emit := func(event any) { raw, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", raw) }
						emit(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": fmt.Sprintf("Reason %d.", calls)})
						emit(map[string]any{"type": "response.output_text.delta", "delta": text, "output_index": 0})
						if calls <= 2 {
							emit(map[string]any{"type": "response.output_item.added", "output_index": 1, "item": items[1]})
							emit(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "delta": "{}"})
						}
						emit(map[string]any{"type": "response.completed", "response": response})
					} else {
						w.Header().Set("Content-Type", "application/json")
						response["output"] = append([]any{map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": fmt.Sprintf("Reason %d.", calls)}}}}, items...)
						if err := json.NewEncoder(w).Encode(response); err != nil {
							t.Error(err)
						}
					}
				}))
				defer server.Close()
				client, err := llmclient.Dial("openai", "gpt-5.6-luna", "test-key", server.URL)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				renderer := render.New(&output, false)
				engine := New(client, tool.NewRegistry([]tool.Tool{&fakeTool{name: "echo", output: "ok"}}), 5, "", renderer, 0)
				if engaging {
					engine.SetInteractionMode("engaging")
					engine.SetNarrator(narrate.New(true))
				}
				engine.SetStream(mode != "buffered")
				engine.SetDeltaHandler(func(d llmclient.Delta) error {
					if d.Kind == llmclient.DeltaReasoning {
						renderer.StreamReasoning(d.Text)
					} else if d.Kind == llmclient.DeltaContent {
						renderer.StreamContent(d.Text)
					} else {
						t.Error("tool argument delta leaked")
					}
					return nil
				})
				var pre []IterationInfo
				engine.SetIterationCallback(func(info IterationInfo) {
					if info.IsPreTool {
						pre = append(pre, info)
					} else if info.Content != "" {
						t.Errorf("note repeated on post-tool/final callback: %+v", info)
					}
				})
				answer, err := engine.Run(context.Background(), "Use echo twice.")
				if err != nil || answer != "Final answer." || calls != 3 {
					t.Fatalf("answer=%q calls=%d err=%v", answer, calls, err)
				}
				if len(pre) != 2 {
					t.Fatalf("pre callbacks=%v", pre)
				}
				for i, info := range pre {
					wantStream := mode == "streamed" || (mode == "fallback_after_first" && i == 0)
					if info.Content != fmt.Sprintf("Note %d.", i+1) || info.StreamedContent != wantStream || info.StreamedReasoning != wantStream {
						t.Errorf("pre callback=%+v", info)
					}
				}
				for _, text := range []string{"Note 1.", "Note 2.", "Final answer.", "Reason 1.", "Reason 2.", "Reason 3."} {
					if strings.Count(output.String(), text) != 1 {
						t.Errorf("%q not rendered exactly once:\n%s", text, output.String())
					}
				}
			})
		}
	}
}
