package runtimelog

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func runPrefilled(t *testing.T, n int, write func([]byte) error) *Logger {
	t.Helper()
	l := &Logger{queue: make(chan []byte, n), done: make(chan struct{}), abort: make(chan struct{}), write: write}
	for i := 0; i < n; i++ {
		l.queue <- []byte(fmt.Sprintf("{\"i\":%d}\n", i))
	}
	close(l.queue)
	go l.run()
	<-l.done
	return l
}

func TestRED_Runtimelog_BurstIsBatchedIntoFewWrites(t *testing.T) {
	var writes int
	var all bytes.Buffer
	runPrefilled(t, 1000, func(b []byte) error { writes++; all.Write(b); return nil })
	if writes > 5 {
		t.Fatalf("1000 queued records took %d writes", writes)
	}
	var want bytes.Buffer
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&want, "{\"i\":%d}\n", i)
	}
	if !bytes.Equal(all.Bytes(), want.Bytes()) {
		t.Fatal("batched output lost ordering or content")
	}
}

func TestBatchFailureCountsEveryRecord(t *testing.T) {
	l := runPrefilled(t, 10, func([]byte) error { return errors.New("disk") })
	if got := l.failures.Load(); got != 10 {
		t.Fatalf("failures=%d, want 10", got)
	}
}

func TestBatchRespectsByteBound(t *testing.T) {
	var max int
	big := bytes.Repeat([]byte("x"), 4000)
	l := &Logger{queue: make(chan []byte, 100), done: make(chan struct{}), abort: make(chan struct{}), write: func(b []byte) error {
		if len(b) > max {
			max = len(b)
		}
		return nil
	}}
	for i := 0; i < 100; i++ {
		l.queue <- append(append([]byte(nil), big...), '\n')
	}
	close(l.queue)
	go l.run()
	<-l.done
	if max > maxBatchBytes+len(big)+1 {
		t.Fatalf("batch of %d bytes exceeds bound", max)
	}
}
