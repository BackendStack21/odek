package loop

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

type receiptContractTool struct{ path string }

func (t *receiptContractTool) Name() string                { return "read_file" }
func (t *receiptContractTool) Description() string         { return "delivers read receipt" }
func (t *receiptContractTool) Schema() any                 { return map[string]any{"type": "object"} }
func (t *receiptContractTool) Call(string) (string, error) { panic("must use invocation context") }
func (t *receiptContractTool) CallContext(ctx context.Context, args string) (string, error) {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return "", err
	}
	danger.RecordReadContentCtx(ctx, t.path, int64(len(b)), sha256.Sum256(b))
	return string(b), nil
}

func TestReadReceiptRequiresUnclippedUnredactedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		fresh         bool
	}{
		{"complete", "#!/bin/sh\necho ordinary\n", true},
		{"clipped", strings.Repeat("start\n", 900) + "unique-middle-evidence" + strings.Repeat("end\n", 600), false},
		{"redacted", "export OPENAI_API_KEY=sk-proj-" + strings.Repeat("a", 70), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "read.sh")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "redacted" && redact.RedactSecrets(tc.content) == tc.content {
				t.Fatal("fixture not redacted")
			}
			srv := contractServer(t, fmt.Sprintf(`{"choices":[{"message":{"tool_calls":[{"id":"read","type":"function","function":{"name":"read_file","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, fmt.Sprintf(`{"path":%q}`, path)))
			defer srv.Close()
			e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{&receiptContractTool{path: path}}), 3, "sys", nil, 0)
			ctx := danger.WithLedgerKey(context.Background(), path)
			_, messages, err := e.RunWithMessages(ctx, []session.Message{{Role: "user", Content: "inspect"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := danger.WasReadFreshCtx(ctx, path); got != tc.fresh {
				t.Fatalf("fresh=%v want %v", got, tc.fresh)
			}
			if tc.name == "clipped" {
				found := false
				for _, m := range messages {
					if m.Role == "tool" && strings.Contains(m.Content, "unique-middle-evidence") {
						found = true
					}
				}
				if !found {
					t.Fatal("full durable evidence was lost to model-preview clipping")
				}
			}
		})
	}
}
