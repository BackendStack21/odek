package config

import "testing"

// Project may only LOWER numeric caps; 0/negative must not remove them.
func TestRED_ProjectBackgroundZeroCannotUncapTimeout(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"background":{"max_timeout_seconds":600}}`)
	writeProjectConfig(t, dir, `{"background":{"max_timeout_seconds":0}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.MaxTimeoutSeconds != 600 {
		t.Errorf("project removed global timeout cap: %d", cfg.Background.MaxTimeoutSeconds)
	}
}

func TestRED_ProjectBackgroundZeroCannotRaiseMaxJobs(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"background":{"max_jobs":2,"max_output_bytes":4096}}`)
	writeProjectConfig(t, dir, `{"background":{"max_jobs":0,"max_output_bytes":-1}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.MaxJobs > 2 {
		t.Errorf("max_jobs raised above global 2: %d", cfg.Background.MaxJobs)
	}
	if cfg.Background.MaxOutputBytes > 4096 {
		t.Errorf("max_output_bytes raised above global 4096: %d", cfg.Background.MaxOutputBytes)
	}
}

func TestRED_ProjectBackgroundZeroWakesDisablesWakes(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"background":{"max_wakes_per_hour":30}}`)
	writeProjectConfig(t, dir, `{"background":{"max_wakes_per_hour":0}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.MaxWakesPerHour != 0 {
		t.Errorf("project could not disable wakes: %d", cfg.Background.MaxWakesPerHour)
	}

	dir = redIsolate(t)
	writeGlobalConfig(t, dir, `{"background":{"max_wakes_per_hour":30}}`)
	writeProjectConfig(t, dir, `{"background":{"max_wakes_per_hour":-1}}`)
	cfg = LoadConfig(CLIFlags{})
	if cfg.Background.MaxWakesPerHour != 30 {
		t.Errorf("negative wake cap not re-inherited: %d", cfg.Background.MaxWakesPerHour)
	}
}

func TestProjectBackgroundLowerValuesStillApply(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"background":{"max_jobs":4,"max_timeout_seconds":600,"max_wakes_per_hour":10}}`)
	writeProjectConfig(t, dir, `{"background":{"max_jobs":2,"max_timeout_seconds":60,"max_wakes_per_hour":-5}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.MaxJobs != 2 || cfg.Background.MaxTimeoutSeconds != 60 {
		t.Errorf("lowered caps not applied: jobs=%d timeout=%d", cfg.Background.MaxJobs, cfg.Background.MaxTimeoutSeconds)
	}
	if cfg.Background.MaxWakesPerHour != 10 {
		t.Errorf("negative wake cap not re-inherited: %d", cfg.Background.MaxWakesPerHour)
	}
}
