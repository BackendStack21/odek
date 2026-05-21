package ws

import "testing"

func TestConstants(t *testing.T) {
	if OpText != 1 {
		t.Errorf("OpText = %d, want 1", OpText)
	}
	if OpBinary != 2 {
		t.Errorf("OpBinary = %d, want 2", OpBinary)
	}
}
