package telegram

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TelegramConfig holds all configuration for the Telegram bot.
type TelegramConfig struct {
	Token            string   `json:"bot_token"`
	AllowedChats     []int64  `json:"allowed_chats"`
	AllowedUsers     []int64  `json:"allowed_users"`
	BotUsername      string   `json:"bot_username"`
	PollInterval     int      `json:"poll_interval"`         // seconds, default 1
	PollTimeout      int      `json:"poll_timeout"`          // seconds, default 30
	MaxMsgLength     int      `json:"max_msg_length"`        // default 4096
	DailyTokenBudget int64    `json:"daily_token_budget"`    // 0 = unlimited (default)
	SessionTTL       int      `json:"session_ttl_hours"`     // hours, default 24
	AgentTimeout     int      `json:"agent_timeout_seconds"` // max agent run duration, default 900 (15m), 0 = unlimited
	FallbackURLs     []string `json:"fallback_urls"`
	HealthAddr       string   `json:"health_addr"`     // e.g. "127.0.0.1:9090" (empty = disabled)
	LogLevel         string   `json:"log_level"`       // "debug","info","warn","error" (default "info")
	LogFile          string   `json:"log_file"`        // path or empty for stderr
	DefaultChatID    int64    `json:"default_chat_id"` // for --deliver and cron delivery
	// AllowAllUsers must be explicitly set to true to run the bot with NO
	// allowlist (any Telegram user may drive the agent). Without it, an empty
	// AllowedChats + AllowedUsers is a fatal misconfiguration (fail-closed) so
	// an open bot can never be deployed by accident. Env: ODEK_TELEGRAM_ALLOW_ALL.
	AllowAllUsers bool `json:"allow_all_users"`
}

// DefaultConfig returns a TelegramConfig with sensible defaults.
func DefaultConfig() TelegramConfig {
	return TelegramConfig{
		PollInterval:     1,
		PollTimeout:      30,
		MaxMsgLength:     4096,
		DailyTokenBudget: 0, // 0 = unlimited
		SessionTTL:       24,
		AgentTimeout:     900, // 15 minutes (0 = unlimited)
	}
}

// ConfigFromEnv reads configuration from environment variables, starting with
// the given base config and overriding any values that are set in the environment.
func ConfigFromEnv(base TelegramConfig) TelegramConfig {
	cfg := base

	if v := os.Getenv("ODEK_TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("ODEK_TELEGRAM_ALLOWED_CHATS"); v != "" {
		cfg.AllowedChats = parseInt64List(v)
	}
	if v := os.Getenv("ODEK_TELEGRAM_ALLOWED_USERS"); v != "" {
		cfg.AllowedUsers = parseInt64List(v)
	}
	if v := os.Getenv("ODEK_TELEGRAM_ALLOW_ALL"); v != "" {
		cfg.AllowAllUsers = parseBool(v)
	}
	if v := os.Getenv("ODEK_TELEGRAM_BOT_USERNAME"); v != "" {
		cfg.BotUsername = v
	}
	if v := os.Getenv("ODEK_TELEGRAM_POLL_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.PollInterval = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_POLL_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.PollTimeout = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_MAX_MSG_LENGTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxMsgLength = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_DAILY_TOKEN_BUDGET"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.DailyTokenBudget = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_SESSION_TTL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.SessionTTL = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_AGENT_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.AgentTimeout = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_FALLBACK_URLS"); v != "" {
		cfg.FallbackURLs = splitAndTrim(v)
	}
	if v := os.Getenv("ODEK_TELEGRAM_HEALTH_ADDR"); v != "" {
		cfg.HealthAddr = v
	}
	if v := os.Getenv("ODEK_TELEGRAM_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("ODEK_TELEGRAM_LOG_FILE"); v != "" {
		cfg.LogFile = v
	}
	if v := os.Getenv("ODEK_TELEGRAM_DEFAULT_CHAT_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.DefaultChatID = id
		}
	}

	return cfg
}

// ValidateConfig checks that the configuration values are within acceptable
// ranges and returns an error describing the first problem found.
func ValidateConfig(cfg TelegramConfig) error {
	if cfg.Token == "" {
		return fmt.Errorf("telegram: bot_token must not be empty")
	}
	if cfg.PollInterval < 1 {
		return fmt.Errorf("telegram: poll_interval must be >= 1, got %d", cfg.PollInterval)
	}
	if cfg.PollTimeout < 1 || cfg.PollTimeout > 60 {
		return fmt.Errorf("telegram: poll_timeout must be between 1 and 60, got %d", cfg.PollTimeout)
	}
	if cfg.MaxMsgLength < 1 || cfg.MaxMsgLength > 4096 {
		return fmt.Errorf("telegram: max_msg_length must be between 1 and 4096, got %d", cfg.MaxMsgLength)
	}
	if cfg.SessionTTL < 1 {
		return fmt.Errorf("telegram: session_ttl_hours must be >= 1, got %d", cfg.SessionTTL)
	}
	if cfg.AgentTimeout < 0 {
		return fmt.Errorf("telegram: agent_timeout_seconds must be >= 0, got %d", cfg.AgentTimeout)
	}
	// Fail-closed authorization: refuse to start an unrestricted bot unless the
	// operator explicitly opts in. An empty allowlist would otherwise let ANY
	// Telegram user drive the agent (and its shell/file tools). Checked last so
	// field-level validation errors surface first.
	if len(cfg.AllowedChats) == 0 && len(cfg.AllowedUsers) == 0 && !cfg.AllowAllUsers {
		return fmt.Errorf("telegram: no allowlist configured — set ODEK_TELEGRAM_ALLOWED_CHATS " +
			"and/or ODEK_TELEGRAM_ALLOWED_USERS to restrict access, or set " +
			"ODEK_TELEGRAM_ALLOW_ALL=true to explicitly run an open bot (NOT recommended)")
	}
	return nil
}

// parseBool parses common truthy string values ("true", "1", "yes", "on",
// case-insensitive). Anything else is false.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// parseInt64List parses a comma-separated string of integers into a slice of int64.
func parseInt64List(s string) []int64 {
	parts := splitAndTrim(s)
	result := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		result = append(result, n)
	}
	return result
}

// splitAndTrim splits a string on commas and trims whitespace from each part.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
