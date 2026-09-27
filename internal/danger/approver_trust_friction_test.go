package danger

import (
	"os"
	"testing"
	"time"
)

// Trust grants ("t"/"trust") count as approvals: they must feed the friction
// log, otherwise an attacker can rapid-fire trust grants that never engage
// approval-fatigue friction.
func TestPromptCommand_TrustGrantRecordsApproval(t *testing.T) {
	ResetTTYFrictionStateForTest()
	f, err := os.CreateTemp("", "ttyin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("t\nt\nt\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	a := &TTYApprover{TTYPath: f.Name(), FrictionThreshold: 3, FrictionWindow: time.Minute}
	cls := NetworkEgress
	for i := 0; i < 3; i++ {
		if err := a.PromptCommand(cls, "curl http://example.com", ""); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}
	if got := a.recentApprovalCount(cls); got != 3 {
		t.Fatalf("recentApprovalCount after 3 trust grants = %d, want 3 (trust grants must be recorded)", got)
	}
	if !a.shouldFriction(cls) {
		t.Fatal("friction should engage after 3 quick trust grants")
	}
}
