package danger

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestReadDeliveryRequiresCompleteUnchangedContent(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		ctx := WithLedgerKey(context.Background(), t.Name())
		path := filepath.Join(t.TempDir(), "verify.sh")
		body := []byte("#!/bin/sh\nprintf ok\n")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		call := BeginReadDelivery(ctx)
		RecordReadContentCtx(call, path, int64(len(body)), sha256.Sum256(body))
		if WasReadFreshCtx(ctx, path) {
			t.Fatal("receipt visible before delivery")
		}
		FinishReadDelivery(call, delivered)
		if WasReadFreshCtx(ctx, path) != delivered {
			t.Fatalf("delivery=%v: receipt mismatch", delivered)
		}
		if err := os.WriteFile(path, []byte("unseen changed content"), 0600); err != nil {
			t.Fatal(err)
		}
		if WasReadFreshCtx(ctx, path) {
			t.Fatal("mutation kept receipt")
		}
	}
}

func TestReadDeliveryDoesNotFingerprintUnseenReplacement(t *testing.T) {
	ctx := WithLedgerKey(context.Background(), t.Name())
	path := filepath.Join(t.TempDir(), "verify.sh")
	seen := []byte("seen content")
	if err := os.WriteFile(path, []byte("unseen contents"), 0600); err != nil {
		t.Fatal(err)
	}
	RecordReadContentCtx(ctx, path, int64(len(seen)), sha256.Sum256(seen))
	if WasReadFreshCtx(ctx, path) {
		t.Fatal("receipt bound to replacement rather than displayed bytes")
	}
}
