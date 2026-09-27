package redact

import "testing"

// Fused env names with no separator ("MYAPITOKEN") must still be recognised
// as secret-bearing: segment-only matching silently skips them, so the value
// behind MYAPITOKEN is never registered and never redacted.
func TestSensitiveName_FusedForms(t *testing.T) {
	cases := []string{
		"MYAPITOKEN",
		"MyApiToken",
		"OPENAIAPIKEY",
		"GITHUBACCESSTOKEN",
	}
	for _, name := range cases {
		if !sensitiveName(name) {
			t.Errorf("sensitiveName(%q) = false, want true (fused secret name)", name)
		}
	}
	// Non-secret names stay clean.
	for _, name := range []string{"CAPITOL", "RAPID", "TOKENBUCKET_RATE"} {
		if sensitiveName(name) {
			t.Errorf("sensitiveName(%q) = true, want false", name)
		}
	}
}
