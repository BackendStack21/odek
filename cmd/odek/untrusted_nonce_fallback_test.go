package main

import (
	"errors"
	"testing"
)

// Security review wave A, F3: on entropy-source failure newWrapperNonce
// returned the CONSTANT "00000000" — every wrapper in that degraded mode
// would share one guessable nonce, letting forged tags pair with real
// ones. Fail-open. The fallback must still differ per call.
func TestNewWrapperNonce_FallbackIsNotConstant(t *testing.T) {
	errEntropyBroken := errors.New("entropy unavailable")
	orig := wrapperRandRead
	wrapperRandRead = func(b []byte) (int, error) { return 0, errEntropyBroken }
	t.Cleanup(func() { wrapperRandRead = orig })

	a := newWrapperNonce()
	b := newWrapperNonce()
	if a == b {
		t.Fatalf("degraded-mode nonces are constant (%q) — a forged tag could pair with a real wrapper", a)
	}
	if len(a) != 16 || len(b) != 16 { // 8 bytes hex
		t.Fatalf("degraded nonce length wrong: %q %q", a, b)
	}
}
