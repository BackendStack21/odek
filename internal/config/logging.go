package config

// LoadLoggingSettings resolves only operator logging policy before command
// parsing and service initialization. Project config cannot enable logging.
// Diagnostics produced while reading the policy are buffered by the caller.
func LoadLoggingConfig() LoggingConfig {
	return LoadConfig(CLIFlags{}).Logging
}

// LoadLoggingSettings is retained for integrations that only need the old
// enabled/size pair. New callers should use LoadLoggingConfig.
func LoadLoggingSettings() (enabled bool, maxMB int64) {
	cfg := LoadLoggingConfig()
	return cfg.Enabled, cfg.MaxFileMB
}
