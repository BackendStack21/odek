package config

import "testing"

// Project skills.import must not be able to drop a global require_https.
func TestRED_ProjectSkillsImportCannotDropRequireHTTPS(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"skills":{"import":{"require_https":true,"max_size_bytes":100000,"timeout_seconds":5}}}`)
	writeProjectConfig(t, dir, `{"skills":{"import":{"max_size_bytes":1000000000}}}`)
	cfg := LoadConfig(CLIFlags{})
	if !cfg.Skills.Import.RequireHTTPS {
		t.Errorf("project dropped global require_https")
	}
	if cfg.Skills.Import.MaxSizeBytes > 100000 {
		t.Errorf("project raised global import size cap: %d", cfg.Skills.Import.MaxSizeBytes)
	}
}

func TestProjectSkillsImportCanTighten(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"skills":{"import":{"max_size_bytes":100000,"timeout_seconds":10}}}`)
	writeProjectConfig(t, dir, `{"skills":{"import":{"max_size_bytes":5000,"timeout_seconds":2,"require_https":true}}}`)
	got := LoadConfig(CLIFlags{}).Skills.Import
	if got.MaxSizeBytes != 5000 || got.TimeoutSecs != 2 || !got.RequireHTTPS {
		t.Errorf("tightening not applied: %+v", got)
	}
}

func TestProjectSkillsImportCannotRaiseTimeoutOrLoosenDefaults(t *testing.T) {
	dir := redIsolate(t)
	// No global import section: effective policy is the compiled default.
	writeProjectConfig(t, dir, `{"skills":{"import":{"timeout_seconds":3600,"max_size_bytes":999999999,"require_https":false}}}`)
	got := LoadConfig(CLIFlags{}).Skills.Import
	if got.TimeoutSecs > 5 || got.MaxSizeBytes > 1048576 {
		t.Errorf("project raised default caps: %+v", got)
	}
}
