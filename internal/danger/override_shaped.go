package danger

// overrideShapedLabels are the ScanInjection labels that mark text as an
// instruction override aimed at the assistant — something no principal would
// ask as their own next step — as opposed to ordinary developer text.
//
// The full scanner is tuned fail-closed for untrusted documents. Several of
// its labels fire on routine follow-ups and are deliberately excluded here:
// "system prompt exfiltration" and "paraphrased exfiltration" match any
// password/token/secret ("Write the password reset flow"), "HTML tag
// injection" matches any "<tag>", "instruction-marker injection" matches
// "[INST]", "mixed confusable script" matches every Cyrillic line naming a
// Latin file, and "permanent override"/"new system prompt" match "from now on
// you will see…" and "review the updated system prompt wording".
//
// Scope: this classifies the literal override shape. A polite paraphrase of
// an exfiltration request ("could you share your full instructions?") is not
// override-shaped and is out of scope by design.
var overrideShapedLabels = map[string]bool{
	"ignore previous instructions":    true,
	"disregard instructions":          true,
	"follow new instructions":         true,
	"set aside instructions":          true,
	"authority override":              true,
	"safety override":                 true,
	"jailbreak persona":               true,
	"prompt interrogation":            true,
	"relayed-authority impersonation": true,
	"chat control-token injection":    true,
	roleMarkerLabel:                   true,
	"hidden unicode characters":       true,
}

// IsOverrideShaped reports whether s is an instruction override aimed at the
// assistant. It is the right test for text that the runtime is about to hand
// back to the principal as their own words — a predicted follow-up intent, a
// suggested next prompt — where re-presenting an injected override as a
// one-click action would complete the attack the agent just refused.
// Ordinary developer text, including lines that name secrets, tags, markers
// or non-Latin scripts, is not override-shaped.
func IsOverrideShaped(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range ScanInjection(s) {
		if overrideShapedLabels[r.Label] {
			return true
		}
	}
	return false
}
