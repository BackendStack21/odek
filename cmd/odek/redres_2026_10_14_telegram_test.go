package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

func tgresMediaFile(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".odek", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("private contents of chat A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tgresQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRED_TelegramReadFileRefusesOtherChatsMedia(t *testing.T) {
	p := tgresMediaFile(t, "doc_chat111_note_abcd.txt")
	tl := &readFileTool{dangerousConfig: danger.DangerousConfig{}}
	args, _ := json.Marshal(map[string]any{"path": p})

	tl.SetContext(withTelegramMediaChat(context.Background(), 222))
	out, err := tl.Call(string(args))
	if err == nil || strings.Contains(out, "private contents") {
		t.Fatalf("chat 222 read chat 111's media: out=%q err=%v", out, err)
	}
	if !strings.Contains(out, "different chat") {
		t.Fatalf("want a different-chat refusal, got %q", out)
	}

	// The owning chat still reads it.
	tl.SetContext(withTelegramMediaChat(context.Background(), 111))
	out, err = tl.Call(string(args))
	if err != nil || !strings.Contains(out, "private contents") {
		t.Fatalf("owner read failed: out=%q err=%v", out, err)
	}

	// Outside a Telegram turn nothing changes.
	tl.SetContext(context.Background())
	if out, err = tl.Call(string(args)); err != nil || !strings.Contains(out, "private contents") {
		t.Fatalf("non-telegram read failed: out=%q err=%v", out, err)
	}
}

func TestRED_TelegramMediaScopeCoversSiblingsAndSpellings(t *testing.T) {
	p := tgresMediaFile(t, "voice_chat111_x.ogg")
	home, _ := os.UserHomeDir()
	link := filepath.Join(t.TempDir(), "alias.ogg")
	if err := os.Symlink(p, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	ctx := withTelegramMediaChat(context.Background(), 222)
	for name, path := range map[string]string{
		"absolute": p,
		"tilde":    "~/.odek/media/voice_chat111_x.ogg",
		"dotdot":   filepath.Join(home, ".odek", "media", "..", "media", "voice_chat111_x.ogg"),
		"symlink":  link,
	} {
		if err := checkTelegramMediaRead(ctx, path); err == nil {
			t.Errorf("%s spelling %q: chat 222 allowed", name, path)
		}
	}
	// vision and transcribe consult the same guard before touching the file.
	vt := &visionTool{dangerousConfig: danger.DangerousConfig{}}
	tt := &transcribeTool{dangerousConfig: danger.DangerousConfig{}}
	vt.SetContext(ctx)
	tt.SetContext(ctx)
	if out, _ := tt.Call(`{"path":` + tgresQuote(p) + `}`); !strings.Contains(out, "different chat") {
		t.Errorf("transcribe did not refuse: %q", out)
	}
	if out, _ := vt.CallContext(ctx, `{"path":`+tgresQuote(p)+`}`); !strings.Contains(out, "different chat") {
		t.Errorf("vision did not refuse: %q", out)
	}
	// A file of the chat itself and files outside the media dir are fine.
	if err := checkTelegramMediaRead(withTelegramMediaChat(context.Background(), 111), p); err != nil {
		t.Errorf("owner refused: %v", err)
	}
	if err := checkTelegramMediaRead(ctx, filepath.Join(t.TempDir(), "x.txt")); err != nil {
		t.Errorf("non-media path refused: %v", err)
	}
}

func TestRED_TelegramMediaJobsRunOffTheUpdateLoop(t *testing.T) {
	const chatA, chatB int64 = 910001, 910002
	release := make(chan struct{})
	started := make(chan struct{})
	var order []int
	var mu sync.Mutex
	done := make(chan struct{}, 3)

	returned := make(chan struct{})
	go func() {
		// A slow download/transcription for chat A must not block the caller.
		runChatMediaOffLoop(chatA, nil, func() {
			close(started)
			<-release
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			done <- struct{}{}
		})
		// Same chat: queued behind the first, order preserved.
		runChatMediaOffLoop(chatA, nil, func() {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			done <- struct{}{}
		})
		// A panicking job is contained and does not stall the chat's queue.
		runChatMediaOffLoop(chatA, nil, func() { panic("boom") })
		runChatMediaOffLoop(chatA, nil, func() {
			mu.Lock()
			order = append(order, 3)
			mu.Unlock()
			done <- struct{}{}
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("runChatMediaOffLoop blocked the caller")
	}
	<-started

	// Another chat's job runs while chat A's is still in flight.
	other := make(chan struct{})
	runChatMediaOffLoop(chatB, nil, func() { close(other) })
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("chat B's media job waited on chat A")
	}

	close(release)
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("queued media jobs did not finish")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("per-chat order not preserved: %v", order)
	}
}
