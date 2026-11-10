package session

import "testing"

func TestTaintOverflowFill(t *testing.T) {
	var empty TaintOverflow
	if empty.Fill() != 0 {
		t.Fatal("an empty filter is not empty")
	}
	if (TaintOverflow{1, 2, 3}).Fill() != 1 {
		t.Fatal("a wrong-size filter does not count as full")
	}
	half := make(TaintOverflow, TaintOverflowBytes)
	for i := range half {
		half[i] = 0xf0
	}
	if got := half.Fill(); got != 0.5 {
		t.Fatalf("half-set filter fill = %v", got)
	}
	var one TaintOverflow
	one.AddValue("host", "a.example")
	if got := one.Fill(); got <= 0 || got > float64(taintOverflowProbes)/taintOverflowBits {
		t.Fatalf("one-span filter fill = %v", got)
	}
	if saturatedTaintOverflow().Fill() != 1 {
		t.Fatal("a saturated filter is not full")
	}
}
