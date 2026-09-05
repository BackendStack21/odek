package main

// Bug-hunt v3 (fix/bughunt-v3) RED tests — schedule daemon exclusion and
// Telegram delivery redaction.
//
// 1. acquireScheduleLock used a read-check-write pidfile: two daemons
//    starting together could both pass the liveness check before either
//    wrote its PID, and both would run the scheduler — every enabled job
//    double-fires. A wave-1 hunter measured 196/200 concurrent pairs both
//    acquiring. The fix routes exclusion through a kernel-arbitrated
//    non-blocking flock (flock.TryLock).
//
// 2. sendTelegramResult delivered scheduled-job results to Telegram
//    UNREDACTED, while the sibling cliDeliverer log-file path redacts the
//    same result — a secret echoed by a scheduled run left the machine in
//    clear only via the Telegram sink.

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestAcquireScheduleLock_ExclusiveUnderConcurrency pins the kernel-
// arbitrated exclusion: in concurrent start pairs, exactly one acquirer
// may win per round.
func TestAcquireScheduleLock_ExclusiveUnderConcurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for round := 0; round < 16; round++ {
		var wg sync.WaitGroup
		results := make([]error, 2)
		releases := make([]func(), 2)
		start := make(chan struct{})

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				rel, err := acquireScheduleLock()
				results[i] = err
				releases[i] = rel
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		for i := 0; i < 2; i++ {
			if results[i] == nil {
				winners++
				if releases[i] != nil {
					releases[i]()
				}
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d/2 concurrent acquirers both acquired the schedule lock (double-fire daemon vector); errors = %v", round, winners, results)
		}
	}
}

// TestAcquireScheduleLock_RefusalNamesHolder pins the diagnostic contract:
// when the lock is held, the refusal error still tells the operator who
// holds it (PID from the pidfile).
func TestAcquireScheduleLock_RefusalNamesHolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rel, err := acquireScheduleLock()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer rel()

	_, err = acquireScheduleLock()
	if err == nil {
		t.Fatal("second acquire while held: want refusal, got success")
	}
	if !strings.Contains(err.Error(), "schedule daemon") {
		t.Fatalf("refusal error should name the schedule daemon holder, got: %v", err)
	}
}

// TestSendTelegramResult_RedactsSecrets pins the redaction parity between
// delivery sinks: a scheduled result containing a secret-shaped token must
// reach Telegram with the raw literal scrubbed, exactly as the cliDeliverer
// log-file path already redacts it.
func TestSendTelegramResult_RedactsSecrets(t *testing.T) {
	rec := &sendRecorder{}
	bot := newRecorderBot(t, rec)

	// Fake but pattern-matching credential (never a real token).
	secret := "ghp_" + strings.Repeat("a", 36)
	result := "Deploy finished. Token used: " + secret + " — all green."

	if err := sendTelegramResult(context.Background(), bot, 555, result); err != nil {
		t.Fatalf("sendTelegramResult: %v", err)
	}

	for _, call := range rec.snapshot() {
		// MarkdownV2 escapes specials like "_", so compare on the
		// escape-invariant 36-char core (and strip escapes for good
		// measure). A redacted payload contains no such run at all.
		stripped := strings.ReplaceAll(call.text, "\\", "")
		if strings.Contains(stripped, strings.Repeat("a", 36)) {
			t.Fatalf("secret token delivered to Telegram unredacted (cliDeliverer redacts the same result); sent text = %q", call.text)
		}
	}
}
