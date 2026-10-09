package artifact

import "testing"

func TestRED_OneLineKeepsShapingFormatChars(t *testing.T) {
	keep := []string{
		"می‌خواهم",
		"क्‍ष",
		"👩‍👩‍👧",
		"co­operate",
	}
	for _, s := range keep {
		if got := oneLine(s); got != s {
			t.Errorf("oneLine(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestRED_OneLineFlattensHostileChars(t *testing.T) {
	for _, r := range []rune{0x00, 0x09, 0x0a, 0x0d, 0x1b, 0x7f, 0x85, 0x9f, 0x061C, 0x200E, 0x200F,
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069, 0x2028, 0x2029, 0xFEFF} {
		in := "a" + string(r) + "b"
		if got := oneLine(in); got != "a b" {
			t.Errorf("oneLine(U+%04X) = %q, want %q", r, got, "a b")
		}
	}
}
