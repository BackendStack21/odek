package main

import (
	"strings"
	"testing"
)

// Regression (math_eval %): evalNode's REM branch checked y == 0 on the
// FLOAT before truncating both operands to int64. With a fractional
// divisor that truncates to zero the check passed and the integer modulo
// panicked ("1 % 0.5" → recovered as "internal tool error"); with a
// fractional dividend or divisor that truncates non-zero the operation
// silently returned a wrong answer ("0.5 % 2" → 0 instead of an error).
// Modulo requires integer operands; anything else must be a clean error.
func TestMathEval_RemRequiresIntegers(t *testing.T) {
	// Integer modulo keeps working.
	if v, err := evalMath("7 % 3"); err != nil || v != 1 {
		t.Fatalf("7 %% 3 = %v, %v; want 1, nil", v, err)
	}
	if v, err := evalMath("-7 % 3"); err != nil || v != -1 {
		t.Fatalf("-7 %% 3 = %v, %v; want -1, nil (Go truncating semantics)", v, err)
	}

	// Fractional operands: clean error, never a panic, never a wrong value.
	if _, err := evalMath("1 % 0.5"); err == nil {
		t.Error("1 % 0.5: expected an error (integer divide by zero panic before the fix)")
	} else if !strings.Contains(err.Error(), "integer") {
		t.Errorf("1 %% 0.5: err = %v, want an integer-operands error", err)
	}
	if _, err := evalMath("0.5 % 2"); err == nil {
		t.Error("0.5 % 2: expected an error (silently returned 0 before the fix)")
	}
	if _, err := evalMath("0.5 % 0.25"); err == nil {
		t.Error("0.5 % 0.25: expected an error")
	}
}
