package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestEngine_BufferedSummaryAfterStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		stream, _ := request["stream"].(bool)
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Partial progress."},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Plan\",\"content\":\"Working\",\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	engine := New(testChatClient(t, server.URL), tool.NewRegistry([]tool.Tool{&fakeTool{name: "echo", output: "ok"}}), 1, "", nil, 0)
	engine.SetStream(true)
	engine.SetDeltaHandler(func(llmclient.Delta) error { return nil })
	var final *IterationInfo
	engine.SetIterationCallback(func(info IterationInfo) {
		if info.HasFinalAnswer {
			final = &info
		}
	})
	answer, err := engine.Run(context.Background(), "Work")
	if err != nil || !strings.Contains(answer, "Partial progress.") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if final == nil || final.StreamedContent || final.StreamedReasoning {
		t.Fatalf("buffered summary marked as streamed: %+v", final)
	}
}
