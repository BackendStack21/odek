package main

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// Overflowing arithmetic yields +Inf, which json.Marshal cannot encode: the
// tool must report a clean structured {error:...} result, not the raw
// "marshal error" fallback. (Structured error results also carry a permanent
// tool error by design so plan checks never count them as success.)
func TestRED_MathEvalOverflowReturnsGoError(t *testing.T) {
	for _, expr := range []string{"1e308*10", "(1e308*10)-(1e308*10)", "1e308+1e308"} {
		out, _ := (&mathEvalTool{}).Call(`{"expression":"` + expr + `"}`)
		if strings.Contains(out, "marshal error") {
			t.Fatalf("%s: math_eval leaked a marshal error: %s", expr, out)
		}
		var r struct {
			Expression string `json:"expression"`
			Error      string `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil || r.Error == "" || r.Expression != expr {
			t.Fatalf("%s: expected structured {expression,error} result, got %s", expr, out)
		}
	}
}

// Modulo on integer-valued floats outside int64 range is converted with
// int64(x) (implementation-defined) and silently returns a wrong answer.
func TestRED_MathEvalModuloHugeOperandWrong(t *testing.T) {
	out, _ := (&mathEvalTool{}).Call(`{"expression":"1e30 % 7"}`)
	var r struct {
		Result float64 `json:"result"`
		Error  string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unparseable result %s: %v", out, err)
	}
	if r.Error != "" {
		return // rejecting is acceptable
	}
	bf, _ := new(big.Float).SetString("1e30")
	bi, _ := bf.Int(nil)
	want := new(big.Int).Mod(bi, big.NewInt(7)).Int64()
	if int64(r.Result) != want {
		t.Fatalf("1e30 %% 7 = %v, want %d (or an error)", r.Result, want)
	}
}

func TestMathEvalModuloInt64Boundary(t *testing.T) {
	out, _ := (&mathEvalTool{}).Call(`{"expression":"9007199254740992 % 10"}`)
	if !strings.Contains(out, `"result":2`) {
		t.Fatalf("in-range modulo broken: %s", out)
	}
	out, _ = (&mathEvalTool{}).Call(`{"expression":"7 % 9223372036854775808"}`)
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("2^63 divisor must be rejected: %s", out)
	}
}
