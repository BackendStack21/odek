package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func TestParseVerifyVerdict(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		verdict string
	}{
		{"clean pass", `{"verdict":"pass","reasons":[],"missing":[]}`, "pass"},
		{"fail with reasons", `{"verdict":"fail","reasons":["claim unsupported"],"missing":["test run"]}`, "fail"},
		{"prose-wrapped", "Here is my assessment:\n{\"verdict\":\"pass\"}\nDone.", "pass"},
		{"uppercase verdict", `{"verdict":"PASS"}`, "pass"},
		{"unknown verdict", `{"verdict":"maybe"}`, "uncertain"},
		{"malformed json", `{"verdict":"pass"`, "uncertain"},
		{"not json", "The answer looks good to me.", "uncertain"},
		{"empty", "", "uncertain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVerifyVerdict(tc.in)
			if got.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.verdict)
			}
		})
	}
}

func TestParseVerifyVerdict_OversizedIsUncertain(t *testing.T) {
	big := make([]byte, verifyVerdictMaxBytes+1024)
	for i := range big {
		big[i] = 'a'
	}
	if v := parseVerifyVerdict(string(big)); v.Verdict != "uncertain" {
		t.Fatalf("oversized payload verdict = %q, want uncertain", v.Verdict)
	}
}

func TestParseVerifyVerdict_ClampsReasonStrings(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	v := parseVerifyVerdict(`{"verdict":"fail","reasons":["` + string(long) + `"]}`)
	if len(v.Reasons[0]) > 210 {
		t.Fatalf("reason not clamped: %d bytes", len(v.Reasons[0]))
	}
}

func TestExtractJSONObject(t *testing.T) {
	obj, ok := extractJSONObject(`prefix {"a":{"b":1},"c":"x}y"} suffix`)
	if !ok {
		t.Fatal("expected object found")
	}
	if obj != `{"a":{"b":1},"c":"x}y"}` {
		t.Fatalf("extracted %q", obj)
	}
	if _, ok := extractJSONObject("no braces"); ok {
		t.Fatal("unexpected object")
	}
	// unbalanced stays not-ok
	if _, ok := extractJSONObject(`{"a":1`); ok {
		t.Fatal("unbalanced object must not extract")
	}
}

func TestVerifyEnabledDefaults(t *testing.T) {
	e := &Engine{}
	if e.verifyEnabled() {
		t.Fatal("zero-value engine must not run verification")
	}
	e.SetVerify(VerifyConfig{Enabled: true})
	if !e.verifyEnabled() {
		t.Fatal("enabled config must run verification")
	}
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeOff})
	if e.verifyEnabled() {
		t.Fatal("mode off must disable the stage even when enabled=true")
	}
	// Unrecognized mode resolves to hint (enabled)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: "bogus"})
	if !e.verifyEnabled() {
		t.Fatal("unrecognized mode must behave as hint (enabled)")
	}
}

func TestVerifyCyclesLeftBound(t *testing.T) {
	e := &Engine{}
	e.SetVerify(VerifyConfig{Enabled: true, MaxCycles: 99})
	for i := 0; i < verifyCyclesMax; i++ {
		if !e.verifyCyclesLeft() {
			t.Fatalf("cycle %d: expected cycles left", i)
		}
		e.verifyCyclesUsed++
	}
	if e.verifyCyclesLeft() {
		t.Fatal("cycles must stop at the absolute cap")
	}
}

func TestVerifyToolTrace(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []session.ToolCall{{Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
	}
	trace := verifyToolTrace(msgs)
	if trace == "" || trace == "(no tool calls were executed)" {
		t.Fatalf("trace = %q", trace)
	}
	if got := verifyToolTrace(nil); got != "(no tool calls were executed)" {
		t.Fatalf("empty trace = %q", got)
	}
}

func TestVerifyOriginalTask(t *testing.T) {
	if got := verifyOriginalTask([]session.Message{{Role: "user", Content: "  do the thing "}}); got != "do the thing" {
		t.Fatalf("task = %q", got)
	}
	long := make([]byte, 3000)
	for i := range long {
		long[i] = 't'
	}
	got := verifyOriginalTask([]session.Message{{Role: "user", Content: string(long)}})
	if len(got) > 2003 {
		t.Fatalf("task not clamped: %d", len(got))
	}
}

func TestVerifyCorrectiveTextWrapsUntrusted(t *testing.T) {
	e := &Engine{}
	e.SetUntrustedWrapper(func(source, content string) string {
		return "<wrapped:" + source + ">"
	})
	e.SetVerify(VerifyConfig{Enabled: true})
	out := e.verifyCorrectiveText(verifyVerdict{Verdict: "fail", Reasons: []string{"r1"}, Missing: []string{"m1"}})
	if out == "" || !contains(out, "<wrapped:verify_verdict>") {
		t.Fatalf("corrective text not wrapped: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
