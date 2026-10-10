package telegram

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// DefaultMaxDownloadSize is the per-file cap for Telegram downloads (5 MiB)
// when the operator does not configure an explicit value.
const DefaultMaxDownloadSize = 5 * 1024 * 1024

// TelegramConfig holds all configuration for the Telegram bot.
type TelegramConfig struct {
	Token             string   `json:"bot_token"`
	AllowedChats      []int64  `json:"allowed_chats"`
	AllowedUsers      []int64  `json:"allowed_users"`
	BotUsername       string   `json:"bot_username"`
	PollInterval      int      `json:"poll_interval"`                  // seconds, default 1
	PollTimeout       int      `json:"poll_timeout"`                   // seconds, default 30
	MaxMsgLength      int      `json:"max_msg_length"`                 // default 4096
	DailyTokenBudget  int64    `json:"daily_token_budget"`             // 0 = unlimited (default)
	SessionTTL        int      `json:"session_ttl_hours"`              // hours, default 24
	AgentTimeout      int      `json:"agent_timeout_seconds"`          // max agent run duration, default 900 (15m), 0 = unlimited
	MaxDownloadSize   int64    `json:"max_download_size,omitempty"`    // 0 = default 5 MiB; <0 = unlimited; >0 = explicit cap
	MediaQuotaPerChat int64    `json:"media_quota_per_chat,omitempty"` // 0 = disabled; >0 = per-chat quota in bytes
	FallbackURLs      []string `json:"fallback_urls"`
	HealthAddr        string   `json:"health_addr"`     // e.g. "127.0.0.1:9090" (empty = disabled)
	LogLevel          string   `json:"log_level"`       // "debug","info","warn","error" (default "info")
	LogFile           string   `json:"log_file"`        // path or empty for stderr
	DefaultChatID     int64    `json:"default_chat_id"` // for --deliver and cron delivery
	// AllowAllUsers must be explicitly set to true to run the bot with NO
	// allowlist (any Telegram user may drive the agent). Without it, an empty
	// AllowedChats + AllowedUsers is a fatal misconfiguration (fail-closed) so
	// an open bot can never be deployed by accident. Env: ODEK_TELEGRAM_ALLOW_ALL.
	AllowAllUsers bool `json:"allow_all_users"`
	// LinkPreview enables Telegram link previews on outbound text. Default
	// false: previews are disabled because Telegram fetches every previewed
	// URL server-side, a zero-click exfiltration channel for any secret an
	// injected answer embeds in a link. Operator-only (the project
	// odek.json telegram section is ignored). Env: ODEK_TELEGRAM_LINK_PREVIEW.
	LinkPreview bool `json:"link_preview,omitempty"`
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
//
// An allowlist variable replaces the base list with its valid entries, so a
// typo can only narrow access; invalid entries are reported as a warning. A
// non-empty value with no valid entry fails closed: the error names the
// variable and the first bad entry, and the returned config keeps the base
// list. Callers must refuse to start on a non-nil error.
func ConfigFromEnv(base TelegramConfig) (TelegramConfig, error) {
	cfg := base

	if v := os.Getenv("ODEK_TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Token = v
	}
	var envErr error
	for _, al := range []struct {
		name string
		dst  *[]int64
	}{
		{"ODEK_TELEGRAM_ALLOWED_CHATS", &cfg.AllowedChats},
		{"ODEK_TELEGRAM_ALLOWED_USERS", &cfg.AllowedUsers},
	} {
		v := os.Getenv(al.name)
		if v == "" {
			continue
		}
		list, bad := parseAllowlistEntries(v)
		if len(list) == 0 {
			if envErr == nil {
				envErr = fmt.Errorf("telegram: %s has no valid entry (%s)", al.name, bad)
			}
			continue
		}
		*al.dst = list
		if bad != "" {
			warnBadAllowlist(al.name, bad)
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_ALLOW_ALL"); v != "" {
		// strconv.ParseBool keeps the truthy grammar consistent with the rest
		// of the config surface; a malformed value fails closed (false).
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		cfg.AllowAllUsers = err == nil && b
	}
	if v := os.Getenv("ODEK_TELEGRAM_LINK_PREVIEW"); v != "" {
		// A malformed value fails closed (previews disabled) and is reported.
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			fmt.Fprintf(warnWriter, "telegram: warning: ODEK_TELEGRAM_LINK_PREVIEW: invalid value %q ignored; link previews stay disabled (use true/false, 1/0, t/f)\n", v)
		}
		cfg.LinkPreview = err == nil && b
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
	if v := os.Getenv("ODEK_TELEGRAM_DEFAULT_CHAT_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.DefaultChatID = id
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_MAX_DOWNLOAD_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.MaxDownloadSize = n
		}
	}
	if v := os.Getenv("ODEK_TELEGRAM_MEDIA_QUOTA_PER_CHAT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.MediaQuotaPerChat = n
		}
	}

	return cfg, envErr
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
	if !cfg.HasAllowlist() && !cfg.AllowAllUsers {
		return fmt.Errorf("telegram: no allowlist configured — set ODEK_TELEGRAM_ALLOWED_CHATS " +
			"and/or ODEK_TELEGRAM_ALLOWED_USERS to restrict access, or set " +
			"ODEK_TELEGRAM_ALLOW_ALL=true to explicitly run an open bot (NOT recommended)")
	}
	return nil
}

// HasAllowlist reports whether at least one allowlist (chats or users) is
// configured. With no allowlist the bot is open to every Telegram user, which
// requires the explicit AllowAllUsers opt-in (see ValidateConfig).
func (c TelegramConfig) HasAllowlist() bool {
	return len(c.AllowedChats) > 0 || len(c.AllowedUsers) > 0
}

// GroupChatsWithoutUserAllowlist returns the allowed_chats entries that are
// groups or supergroups (Telegram gives those negative ids) when no
// allowed_users list is configured. Authorization then rests on the chat
// alone, so every member of such a group may drive the agent and answer its
// approval prompts.
func (c TelegramConfig) GroupChatsWithoutUserAllowlist() []int64 {
	if len(c.AllowedUsers) > 0 {
		return nil
	}
	var ids []int64
	for _, id := range c.AllowedChats {
		if id < 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// GroupAllowlistWarning renders the startup warning for
// GroupChatsWithoutUserAllowlist, or "" when there is nothing to warn about.
func GroupAllowlistWarning(c TelegramConfig) string {
	ids := c.GroupChatsWithoutUserAllowlist()
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return fmt.Sprintf("allowed_chats includes group chat(s) %s but allowed_users is empty: "+
		"every member of those groups can drive the agent and answer its approval prompts; "+
		"set allowed_users (ODEK_TELEGRAM_ALLOWED_USERS) to restrict who is a principal",
		strings.Join(parts, ", "))
}

// parseInt64List parses a comma-separated string of integers into a slice of
// int64. ok is false when any non-empty entry is not an integer; callers must
// then discard the result rather than use a partial list.
func parseInt64List(s string) (result []int64, ok bool) {
	parts := splitAndTrim(s)
	result = make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, false
		}
		result = append(result, n)
	}
	return result, true
}

// warnWriter receives configuration warnings; tests replace it.
var warnWriter io.Writer = os.Stderr

// parseAllowlistEntries returns the valid integer entries of a comma-separated
// list and a description of the first invalid entry ("" when none; "no
// entries" when the value held nothing at all).
func parseAllowlistEntries(s string) (valid []int64, bad string) {
	parts := splitAndTrim(s)
	if len(parts) == 0 {
		return nil, "no entries"
	}
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			if bad == "" {
				bad = fmt.Sprintf("invalid entry %q", p)
			}
			continue
		}
		valid = append(valid, n)
	}
	return valid, bad
}

// warnBadAllowlist reports an allowlist variable with an ignored entry, so an
// operator who mistyped an id sees why it is not in force.
func warnBadAllowlist(name, bad string) {
	fmt.Fprintf(warnWriter, "telegram: warning: %s: %s ignored; using the valid entries only\n", name, bad)
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
