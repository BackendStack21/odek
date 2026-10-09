package main

import (
	"crypto/sha256"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func numberedText(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestReadLinesWithReceipt_FullReadHasDigestAndSize(t *testing.T) {
	text := numberedText(50)
	out, total, receipt, err := readLinesWithReceipt(strings.NewReader(text), 1, 500)
	if err != nil || total != 50 || !receipt.complete {
		t.Fatalf("total=%d complete=%v err=%v", total, receipt.complete, err)
	}
	if receipt.digest != sha256.Sum256([]byte(text)) || receipt.size != int64(len(text)) {
		t.Fatal("full-read receipt must carry the whole-file digest and size")
	}
	if !strings.HasPrefix(out, "1|line 1\n") || !strings.HasSuffix(out, "50|line 50") {
		t.Fatalf("unexpected content: %q", out)
	}
}

func TestRED_ReadLinesWithReceipt_WindowedReadSkipsDigest(t *testing.T) {
	text := numberedText(50)
	for _, tc := range []struct{ offset, limit int }{{10, 5}, {1, 5}} {
		out, total, receipt, err := readLinesWithReceipt(strings.NewReader(text), tc.offset, tc.limit)
		if err != nil || total != 50 {
			t.Fatalf("offset=%d limit=%d: total=%d err=%v", tc.offset, tc.limit, total, err)
		}
		if receipt.complete {
			t.Fatalf("offset=%d limit=%d: windowed read must not license execution", tc.offset, tc.limit)
		}
		if receipt.digest != ([32]byte{}) {
			t.Fatalf("offset=%d limit=%d: a windowed read never uses its digest, so it must not be computed", tc.offset, tc.limit)
		}
		if got := strings.Count(out, "\n") + 1; got != tc.limit {
			t.Fatalf("offset=%d limit=%d: returned %d lines", tc.offset, tc.limit, got)
		}
	}
}

func TestRED_ReadLinesWithReceipt_SmallFileDoesNotAllocateMiBBuffer(t *testing.T) {
	text := numberedText(20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 20; i++ {
		if _, _, _, err := readLinesWithReceipt(strings.NewReader(text), 1, 500); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / 20; per > 256<<10 {
		t.Fatalf("reading a tiny file allocated %d KiB per call; want under 256", per>>10)
	}
}

func TestReadLinesWithReceipt_LongLineCapUnchanged(t *testing.T) {
	long := strings.Repeat("x", 1024*1024+10)
	_, _, _, err := readLinesWithReceipt(strings.NewReader("a\n"+long+"\n"), 1, 500)
	if err == nil {
		t.Fatal("a line over 1 MiB must still fail the scan")
	}
	ok := strings.Repeat("y", 1024*1024-2)
	if _, total, _, err := readLinesWithReceipt(strings.NewReader(ok+"\nz\n"), 1, 500); err != nil || total != 2 {
		t.Fatalf("line just under the cap must scan: total=%d err=%v", total, err)
	}
}
