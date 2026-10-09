package danger

import (
	"os"
	"testing"
)

// ruby -r with the library fused to the flag loads and runs the file, like
// the separated spelling which does gate.
func TestRED_UnreadGateRubyFusedRequire(t *testing.T) {
	p := redUnreadScript(t)
	rb := p[:len(p)-3] + ".rb"
	if err := os.WriteFile(rb, []byte("puts 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !redGated("ruby -r " + rb + " -e 1") {
		t.Fatalf("control failed")
	}
	if !redGated("ruby -r" + rb + " -e 1") {
		t.Errorf("fused ruby -rFILE must gate")
	}
}
