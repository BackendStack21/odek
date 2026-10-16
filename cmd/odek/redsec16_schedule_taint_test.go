package main

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/session"
)

// A scheduled run delivered into a chat carries its untrusted spans with it,
// so a later turn reusing one is still escalated.
func TestRED_ScheduledTurnCarriesTaintSpansIntoChatSession(t *testing.T) {
	d, sm, _ := newTestDeliverer(t)
	chatID := int64(88120016)
	if err := sm.Save(chatID, []session.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	prev := telegramScheduledHeadlessRun
	defer func() { telegramScheduledHeadlessRun = prev }()
	telegramScheduledHeadlessRun = func(ctx context.Context, _ config.ResolvedConfig, _, _ string, _ []odek.Tool) (string, int64, error) {
		scheduledRunTaintFrom(ctx).set(scheduledRunTaintState{untrusted: true, episode: true,
			spans: []session.TaintSpan{{Kind: "host", Value: "evil.example"}}})
		return "digest", 10, nil
	}
	job := schedule.Job{ID: "jb-spans", Name: "digest", Task: "digest",
		Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: chatID}}
	result, _, err := telegramRunner{}.Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Deliver(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	cs, err := sm.Load(chatID)
	if err != nil || cs == nil {
		t.Fatalf("Load: %v %v", err, cs)
	}
	for _, sp := range cs.TaintSpans() {
		if sp.Kind == "host" && sp.Value == "evil.example" {
			return
		}
	}
	t.Fatalf("chat session lacks the scheduled run's spans: %+v", cs.TaintSpans())
}
