package main

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/loop"
)

func TestServeIterationProgress(t *testing.T) {
	for _, tc := range []struct {
		name string
		info loop.IterationInfo
		want []any
	}{
		{"openai_note", loop.IterationInfo{IsPreTool: true, Content: "Checking files."}, []any{map[string]any{"type": "token", "content": "Checking files."}}},
		{"both", loop.IterationInfo{IsPreTool: true, Content: "Checking files.", ReasoningContent: "Plan"}, []any{map[string]any{"type": "thinking", "content": "Plan"}, map[string]any{"type": "token", "content": "Checking files."}}},
		{"reasoning_streamed", loop.IterationInfo{IsPreTool: true, Content: "Checking files.", ReasoningContent: "Plan", StreamedReasoning: true}, []any{map[string]any{"type": "token", "content": "Checking files."}}},
		{"content_streamed", loop.IterationInfo{IsPreTool: true, Content: "Checking files.", ReasoningContent: "Plan", StreamedContent: true}, []any{map[string]any{"type": "thinking", "content": "Plan"}}},
		{"both_streamed", loop.IterationInfo{IsPreTool: true, Content: "Checking files.", ReasoningContent: "Plan", StreamedReasoning: true, StreamedContent: true}, nil},
		{"post_tool", loop.IterationInfo{Content: "old note", ReasoningContent: "old thought"}, nil},
		{"final", loop.IterationInfo{HasFinalAnswer: true, Content: "Answer", ReasoningContent: "Plan"}, nil},
		{"empty", loop.IterationInfo{IsPreTool: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []any
			serveIterationProgress(func(event any) error { got = append(got, event); return nil }, tc.info)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestTelegramNoteChunks_ConserveText(t *testing.T) {
	for _, note := range []string{"", "Short note.", strings.Repeat("a", 4091), strings.Repeat("a", 4092), strings.Repeat("界", 4000), strings.Repeat("🙂", 4000), strings.Repeat("Mixed 界🙂 notes\n", 1000), strings.Repeat("\x80", 9000)} {
		chunks := telegramNoteChunks(note)
		want := ""
		if note != "" {
			want = "💬 " + note
		}
		if got := strings.Join(chunks, ""); got != want {
			t.Fatalf("note bytes lost: got=%d want=%d", len(got), len(want))
		}
		for _, chunk := range chunks {
			if len(chunk) > 4096 || (utf8.ValidString(note) && !utf8.ValidString(chunk)) {
				t.Fatalf("invalid chunk: bytes=%d valid=%v", len(chunk), utf8.ValidString(chunk))
			}
		}
	}
}

func TestServeRun_NotesStayInTimeline(t *testing.T) {
	for _, kind := range []string{"token", "token_delta"} {
		t.Run(kind, func(t *testing.T) {
			r := &serveRun{Status: "running"}
			r.cond = sync.NewCond(&r.mu)
			for _, event := range []map[string]any{
				{"type": kind, "content": "Checking files."},
				{"type": "tool_call", "name": "read_file"},
				{"type": "tool_result", "name": "read_file"},
				{"type": kind, "content": "Final "},
				{"type": kind, "content": "answer."},
			} {
				if err := r.record(event); err != nil {
					t.Fatal(err)
				}
			}
			if r.Result != "Final answer." || r.events[0]["content"] != "Checking files." {
				t.Fatalf("result=%q events=%v", r.Result, r.events)
			}
			r.Status = "completed"
			if err := r.record(map[string]any{"type": "tool_call", "name": "late"}); err != nil {
				t.Fatal(err)
			}
			if r.Result != "Final answer." {
				t.Fatal("late tool event changed terminal result")
			}
		})
	}
}

func TestTelegramIterationProgress(t *testing.T) {
	for _, tc := range []struct {
		info            loop.IterationInfo
		reasoning, note string
	}{
		{loop.IterationInfo{IsPreTool: true, Content: "Checking files. Then testing."}, "", "Checking files. Then testing."},
		{loop.IterationInfo{IsPreTool: true, ReasoningContent: "Need facts. More reasoning.", Content: "Checking files."}, "Need facts.", "Checking files."},
		{loop.IterationInfo{HasFinalAnswer: true, ReasoningContent: "Done thinking.", Content: "Answer"}, "Done thinking.", ""},
		{loop.IterationInfo{Content: "old note", ReasoningContent: "old thought"}, "", ""},
		{loop.IterationInfo{IsPreTool: true, ReasoningContent: "\n\t "}, "", ""},
	} {
		reasoning, note := telegramIterationProgress(tc.info)
		if reasoning != tc.reasoning || note != tc.note {
			t.Errorf("progress=(%q,%q), want (%q,%q)", reasoning, note, tc.reasoning, tc.note)
		}
	}
}

func TestDeltaCounters_FinalCallDelivery(t *testing.T) {
	var c wsDeltaCounters
	c.addReasoning()
	c.addContent()
	if r, n := c.finalSnapshot(); r != 1 || n != 1 {
		t.Fatalf("interrupted snapshot=(%d,%d)", r, n)
	}
	c.markFinal(loop.IterationInfo{})
	if r, n := c.finalSnapshot(); r != 0 || n != 0 {
		t.Fatalf("buffered final hidden by earlier stream: (%d,%d)", r, n)
	}
	c.markFinal(loop.IterationInfo{StreamedReasoning: true, StreamedContent: true})
	if r, n := c.finalSnapshot(); r != 1 || n != 1 {
		t.Fatalf("streamed final=(%d,%d)", r, n)
	}
	c.reset()
	if r, n := c.finalSnapshot(); r != 0 || n != 0 {
		t.Fatalf("reset=(%d,%d)", r, n)
	}
}
