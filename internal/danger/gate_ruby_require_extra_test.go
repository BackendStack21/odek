package danger

import (
	"os"
	"testing"
)

// A required Ruby library gates like any executed file, in both spellings,
// and stops gating once it has been read.
func TestUnreadGateRubyRequireLicence(t *testing.T) {
	p := redUnreadScript(t)
	rb := p[:len(p)-3] + ".rb"
	if err := os.WriteFile(rb, []byte("puts 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"ruby -r" + rb + " -e 1", "ruby -r " + rb + " -e 1", "ruby -w -r" + rb + " " + p} {
		if !redGated(c) {
			t.Errorf("unread library must gate: %s", c)
		}
	}
	RecordRead(rb)
	if redGated("ruby -r" + rb + " -e 1") {
		t.Errorf("a read library must not gate")
	}
}
