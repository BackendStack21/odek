package main

import "testing"

// Numeric flags used unchecked fmt.Sscanf: "abc" parsed as 0 silently
// (--temperature OVERWROTE the value with 0; --max-iter was skipped), and
// "1800junk" was accepted as 1800. Sibling budget flags check errors;
// these must too.
func TestParseRunFlags_NumericFlagsRejectGarbage(t *testing.T) {
	if _, err := parseRunFlags([]string{"--temperature", "abc", "do-work"}); err == nil {
		t.Fatal("--temperature abc accepted silently (value would be zeroed)")
	}
	if _, err := parseRunFlags([]string{"--max-iter", "abc", "do-work"}); err == nil {
		t.Fatal("--max-iter abc accepted silently")
	}
	if _, err := parseRunFlags([]string{"--max-iter", "1800junk", "do-work"}); err == nil {
		t.Fatal("--max-iter 1800junk accepted (trailing garbage ignored)")
	}
	if _, err := parseRunFlags([]string{"--thinking-budget", "x", "do-work"}); err == nil {
		t.Fatal("--thinking-budget x accepted silently")
	}

	// Valid values still parse.
	f, err := parseRunFlags([]string{"--max-iter", "42", "--temperature", "0.7", "--thinking-budget", "1024", "do-work"})
	if err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	if f.MaxIter != 42 || f.Temp != 0.7 || f.ThinkingBudget != 1024 {
		t.Fatalf("valid values: %+v", f)
	}
}
