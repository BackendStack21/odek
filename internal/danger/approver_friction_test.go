package danger

import (
	"testing"
	"time"
)

// TestApprover_FrictionTriggersAfterThreshold verifies that recording
// FrictionThreshold approvals of the same class inside FrictionWindow
// causes shouldFriction to return true.
func TestApprover_FrictionTriggersAfterThreshold(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 3
	a.FrictionWindow = 60 * time.Second

	for i := 0; i < 3; i++ {
		if a.shouldFriction(SystemWrite) {
			t.Errorf("shouldFriction true before threshold (i=%d)", i)
		}
		a.recordApproval(SystemWrite)
	}
	if !a.shouldFriction(SystemWrite) {
		t.Error("shouldFriction false after threshold reached")
	}
}

// TestApprover_FrictionPerClass verifies that approvals of one class
// do not trip friction for another class.
func TestApprover_FrictionPerClass(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 2
	a.FrictionWindow = 60 * time.Second

	a.recordApproval(SystemWrite)
	a.recordApproval(SystemWrite)
	if !a.shouldFriction(SystemWrite) {
		t.Error("SystemWrite should be in friction")
	}
	if a.shouldFriction(NetworkEgress) {
		t.Error("NetworkEgress should NOT be in friction (different class)")
	}
}

// TestApprover_FrictionResetsAfterWindow verifies that old approvals
// are pruned when they fall outside the window.
func TestApprover_FrictionResetsAfterWindow(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 2
	a.FrictionWindow = 10 * time.Millisecond

	a.recordApproval(SystemWrite)
	a.recordApproval(SystemWrite)
	if !a.shouldFriction(SystemWrite) {
		t.Fatal("friction should have triggered")
	}
	time.Sleep(20 * time.Millisecond)
	if a.shouldFriction(SystemWrite) {
		t.Error("friction should have reset after window expired")
	}
}

// TestApprover_FrictionDisabledWhenThresholdZero verifies the opt-out.
func TestApprover_FrictionDisabledWhenThresholdZero(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 0

	for i := 0; i < 100; i++ {
		a.recordApproval(SystemWrite)
	}
	if a.shouldFriction(SystemWrite) {
		t.Error("friction must stay off when FrictionThreshold == 0")
	}
}
