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
	PollInterval     int      `json:"poll_interval"`      // seconds, default 1
	PollTimeout      int      `json:"poll_timeout"`       // seconds, default 30
	MaxMsgLength     int      `json:"max_msg_length"`     // default 4096
	DailyTokenBudget int64    `json:"daily_token_budget"` // default 1000000
	SessionTTL       int      `json:"session_ttl_hours"`  // hours, default 24
	FallbackURLs     []string `json:"fallback_urls"`
}

// DefaultConfig returns a TelegramConfig with sensible defaults.
func DefaultConfig() TelegramConfig {
	return TelegramConfig{
		PollInterval:     1,
		PollTimeout:      30,
		MaxMsgLength:     4096,
		DailyTokenBudget: 1000000,
		SessionTTL:       24,
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
	if v := os.Getenv("ODEK_TELEGRAM_FALLBACK_URLS"); v != "" {
		cfg.FallbackURLs = splitAndTrim(v)
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
	return nil
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
