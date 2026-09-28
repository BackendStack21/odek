package config

// LoadLoggingSettings resolves only operator logging policy before command
// parsing and service initialization. Project config cannot enable logging.
// Diagnostics produced while reading the policy are buffered by the caller.
func LoadLoggingSettings() (enabled bool, maxMB int64) {
	loadSecretsEnv()
	cfg := loadFile(GlobalConfigPath())
	if cfg.Logging != nil {
		enabled = cfg.Logging.Enabled
	}
	if v := envBool("LOGGING_ENABLED"); v != nil {
		enabled = *v
	}
	maxMB = resolveMaintenance(cfg.Maintenance).LogMaxMB
	if v := envInt64Ptr("MAINTENANCE_LOG_MAX_MB"); v != nil {
		maxMB = *v
	}
	return enabled, maxMB
}
