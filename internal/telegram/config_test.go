package telegram

import (
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Token != "" {
		t.Errorf("DefaultConfig().Token = %q, want empty string", cfg.Token)
	}
	if cfg.PollInterval != 1 {
		t.Errorf("DefaultConfig().PollInterval = %d, want 1", cfg.PollInterval)
	}
	if cfg.PollTimeout != 30 {
		t.Errorf("DefaultConfig().PollTimeout = %d, want 30", cfg.PollTimeout)
	}
	if cfg.MaxMsgLength != 4096 {
		t.Errorf("DefaultConfig().MaxMsgLength = %d, want 4096", cfg.MaxMsgLength)
	}
	if cfg.DailyTokenBudget != 1000000 {
		t.Errorf("DefaultConfig().DailyTokenBudget = %d, want 1000000", cfg.DailyTokenBudget)
	}
	if cfg.SessionTTL != 24 {
		t.Errorf("DefaultConfig().SessionTTL = %d, want 24", cfg.SessionTTL)
	}
	if cfg.BotUsername != "" {
		t.Errorf("DefaultConfig().BotUsername = %q, want empty string", cfg.BotUsername)
	}
	if cfg.AllowedChats != nil {
		t.Errorf("DefaultConfig().AllowedChats = %v, want nil", cfg.AllowedChats)
	}
	if cfg.AllowedUsers != nil {
		t.Errorf("DefaultConfig().AllowedUsers = %v, want nil", cfg.AllowedUsers)
	}
	if cfg.FallbackURLs != nil {
		t.Errorf("DefaultConfig().FallbackURLs = %v, want nil", cfg.FallbackURLs)
	}
}

// ---------------------------------------------------------------------------
// ConfigFromEnv – env var override behaviour
// ---------------------------------------------------------------------------

func TestConfigFromEnv_noEnvVars(t *testing.T) {
	// When no env vars are set, ConfigFromEnv should return DefaultConfig.
	unsetAllEnvVars(t)
	defer unsetAllEnvVars(t)

	cfg := ConfigFromEnv(DefaultConfig())
	want := DefaultConfig()

	// Compare field by field because structs containing slices cannot use ==.
	if cfg.Token != want.Token {
		t.Errorf("Token = %q, want %q", cfg.Token, want.Token)
	}
	if cfg.PollInterval != want.PollInterval {
		t.Errorf("PollInterval = %d, want %d", cfg.PollInterval, want.PollInterval)
	}
	if cfg.PollTimeout != want.PollTimeout {
		t.Errorf("PollTimeout = %d, want %d", cfg.PollTimeout, want.PollTimeout)
	}
	if cfg.MaxMsgLength != want.MaxMsgLength {
		t.Errorf("MaxMsgLength = %d, want %d", cfg.MaxMsgLength, want.MaxMsgLength)
	}
	if cfg.DailyTokenBudget != want.DailyTokenBudget {
		t.Errorf("DailyTokenBudget = %d, want %d", cfg.DailyTokenBudget, want.DailyTokenBudget)
	}
	if cfg.SessionTTL != want.SessionTTL {
		t.Errorf("SessionTTL = %d, want %d", cfg.SessionTTL, want.SessionTTL)
	}
	if cfg.BotUsername != want.BotUsername {
		t.Errorf("BotUsername = %q, want %q", cfg.BotUsername, want.BotUsername)
	}
	if !equalInt64Slice(cfg.AllowedChats, want.AllowedChats) {
		t.Errorf("AllowedChats = %v, want %v", cfg.AllowedChats, want.AllowedChats)
	}
	if !equalInt64Slice(cfg.AllowedUsers, want.AllowedUsers) {
		t.Errorf("AllowedUsers = %v, want %v", cfg.AllowedUsers, want.AllowedUsers)
	}
	if !equalStringSlice(cfg.FallbackURLs, want.FallbackURLs) {
		t.Errorf("FallbackURLs = %v, want %v", cfg.FallbackURLs, want.FallbackURLs)
	}
}

func TestConfigFromEnv_token(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "my-secret-token:123")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.Token != "my-secret-token:123" {
		t.Errorf("Token = %q, want %q", cfg.Token, "my-secret-token:123")
	}
	// Ensure other fields stay at defaults.
	if cfg.PollInterval != 1 {
		t.Errorf("PollInterval changed to %d", cfg.PollInterval)
	}
}

func TestConfigFromEnv_emptyTokenIgnored(t *testing.T) {
	// Empty env var should not override the default (which is "").
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.Token != "" {
		t.Errorf("Token = %q, want empty", cfg.Token)
	}
}

func TestConfigFromEnv_allowedChats(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "  -100123 , 42 , 99 ")
	cfg := ConfigFromEnv(DefaultConfig())
	want := []int64{-100123, 42, 99}
	if !equalInt64Slice(cfg.AllowedChats, want) {
		t.Errorf("AllowedChats = %v, want %v", cfg.AllowedChats, want)
	}
}

func TestConfigFromEnv_allowedChatsEmpty(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.AllowedChats != nil {
		t.Errorf("AllowedChats = %v, want nil", cfg.AllowedChats)
	}
}

func TestConfigFromEnv_allowedChatsInvalidSkips(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "abc,  -100123, 12.5, 99,")
	cfg := ConfigFromEnv(DefaultConfig())
	want := []int64{-100123, 99}
	if !equalInt64Slice(cfg.AllowedChats, want) {
		t.Errorf("AllowedChats = %v, want %v", cfg.AllowedChats, want)
	}
}

func TestConfigFromEnv_allowedUsers(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", "  111 , 222 ")
	cfg := ConfigFromEnv(DefaultConfig())
	want := []int64{111, 222}
	if !equalInt64Slice(cfg.AllowedUsers, want) {
		t.Errorf("AllowedUsers = %v, want %v", cfg.AllowedUsers, want)
	}
}

func TestConfigFromEnv_botUsername(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_BOT_USERNAME", "MyAwesomeBot")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.BotUsername != "MyAwesomeBot" {
		t.Errorf("BotUsername = %q, want %q", cfg.BotUsername, "MyAwesomeBot")
	}
}

func TestConfigFromEnv_pollInterval(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_POLL_INTERVAL", "5")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollInterval != 5 {
		t.Errorf("PollInterval = %d, want 5", cfg.PollInterval)
	}
}

func TestConfigFromEnv_pollIntervalInvalid(t *testing.T) {
	// Invalid (non-numeric) values should be silently ignored.
	t.Setenv("ODEK_TELEGRAM_POLL_INTERVAL", "not-a-number")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollInterval != 1 {
		t.Errorf("PollInterval = %d, want default 1", cfg.PollInterval)
	}
}

func TestConfigFromEnv_pollIntervalEmpty(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_POLL_INTERVAL", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollInterval != 1 {
		t.Errorf("PollInterval = %d, want default 1", cfg.PollInterval)
	}
}

func TestConfigFromEnv_pollTimeout(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_POLL_TIMEOUT", "45")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollTimeout != 45 {
		t.Errorf("PollTimeout = %d, want 45", cfg.PollTimeout)
	}
}

func TestConfigFromEnv_pollTimeoutInvalid(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_POLL_TIMEOUT", "abc")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollTimeout != 30 {
		t.Errorf("PollTimeout = %d, want default 30", cfg.PollTimeout)
	}
}

func TestConfigFromEnv_pollTimeoutEmpty(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_POLL_TIMEOUT", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.PollTimeout != 30 {
		t.Errorf("PollTimeout = %d, want default 30", cfg.PollTimeout)
	}
}

func TestConfigFromEnv_maxMsgLength(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_MAX_MSG_LENGTH", "1024")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.MaxMsgLength != 1024 {
		t.Errorf("MaxMsgLength = %d, want 1024", cfg.MaxMsgLength)
	}
}

func TestConfigFromEnv_maxMsgLengthInvalid(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_MAX_MSG_LENGTH", "xyz")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.MaxMsgLength != 4096 {
		t.Errorf("MaxMsgLength = %d, want default 4096", cfg.MaxMsgLength)
	}
}

func TestConfigFromEnv_dailyTokenBudget(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_DAILY_TOKEN_BUDGET", "500000")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.DailyTokenBudget != 500000 {
		t.Errorf("DailyTokenBudget = %d, want 500000", cfg.DailyTokenBudget)
	}
}

func TestConfigFromEnv_dailyTokenBudgetInvalid(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_DAILY_TOKEN_BUDGET", "not-a-number")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.DailyTokenBudget != 1000000 {
		t.Errorf("DailyTokenBudget = %d, want default 1000000", cfg.DailyTokenBudget)
	}
}

func TestConfigFromEnv_sessionTTL(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_SESSION_TTL_HOURS", "48")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.SessionTTL != 48 {
		t.Errorf("SessionTTL = %d, want 48", cfg.SessionTTL)
	}
}

func TestConfigFromEnv_sessionTTLInvalid(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_SESSION_TTL_HOURS", "bad")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.SessionTTL != 24 {
		t.Errorf("SessionTTL = %d, want default 24", cfg.SessionTTL)
	}
}

func TestConfigFromEnv_sessionTTLEmpty(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_SESSION_TTL_HOURS", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.SessionTTL != 24 {
		t.Errorf("SessionTTL = %d, want default 24", cfg.SessionTTL)
	}
}

func TestConfigFromEnv_fallbackURLs(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_FALLBACK_URLS", "https://a.com, https://b.com")
	cfg := ConfigFromEnv(DefaultConfig())
	want := []string{"https://a.com", "https://b.com"}
	if !equalStringSlice(cfg.FallbackURLs, want) {
		t.Errorf("FallbackURLs = %v, want %v", cfg.FallbackURLs, want)
	}
}

func TestConfigFromEnv_fallbackURLsEmpty(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_FALLBACK_URLS", "")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.FallbackURLs != nil {
		t.Errorf("FallbackURLs = %v, want nil", cfg.FallbackURLs)
	}
}

func TestConfigFromEnv_fallbackURLsTrimsEmptyEntries(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_FALLBACK_URLS", "https://a.com, , https://b.com,")
	cfg := ConfigFromEnv(DefaultConfig())
	want := []string{"https://a.com", "https://b.com"}
	if !equalStringSlice(cfg.FallbackURLs, want) {
		t.Errorf("FallbackURLs = %v, want %v", cfg.FallbackURLs, want)
	}
}

func TestConfigFromEnv_multipleOverrides(t *testing.T) {
	// Multiple env vars set at once.
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "token:multi")
	t.Setenv("ODEK_TELEGRAM_BOT_USERNAME", "MultiBot")
	t.Setenv("ODEK_TELEGRAM_POLL_INTERVAL", "3")
	t.Setenv("ODEK_TELEGRAM_MAX_MSG_LENGTH", "2048")

	cfg := ConfigFromEnv(DefaultConfig())

	if cfg.Token != "token:multi" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.BotUsername != "MultiBot" {
		t.Errorf("BotUsername = %q", cfg.BotUsername)
	}
	if cfg.PollInterval != 3 {
		t.Errorf("PollInterval = %d", cfg.PollInterval)
	}
	if cfg.MaxMsgLength != 2048 {
		t.Errorf("MaxMsgLength = %d", cfg.MaxMsgLength)
	}
	// Ensure unset fields remain at defaults.
	if cfg.PollTimeout != 30 {
		t.Errorf("PollTimeout = %d, want 30", cfg.PollTimeout)
	}
	if cfg.DailyTokenBudget != 1000000 {
		t.Errorf("DailyTokenBudget = %d, want 1000000", cfg.DailyTokenBudget)
	}
}

// ---------------------------------------------------------------------------
// ValidateConfig
// ---------------------------------------------------------------------------

func TestValidateConfig_valid(t *testing.T) {
	cfg := TelegramConfig{
		Token:        "valid:token",
		PollInterval: 1,
		PollTimeout:  30,
		MaxMsgLength: 4096,
		SessionTTL:   24,
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig() = %v, want nil", err)
	}
}

func TestValidateConfig_validMinimal(t *testing.T) {
	cfg := TelegramConfig{
		Token:        "abc",
		PollInterval: 1,
		PollTimeout:  1,
		MaxMsgLength: 1,
		SessionTTL:   1,
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig() = %v, want nil", err)
	}
}

func TestValidateConfig_validMaximums(t *testing.T) {
	cfg := TelegramConfig{
		Token:        "abc",
		PollInterval: 999,
		PollTimeout:  60,
		MaxMsgLength: 4096,
		SessionTTL:   8760,
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig() = %v, want nil", err)
	}
}

func TestValidateConfig_emptyToken(t *testing.T) {
	cfg := DefaultConfig()
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("ValidateConfig() = nil, want error about empty token")
	}
	if err.Error() != "telegram: bot_token must not be empty" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestValidateConfig_pollIntervalZero(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = 0
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for PollInterval = 0")
	}
	if err.Error() != "telegram: poll_interval must be >= 1, got 0" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_pollIntervalNegative(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = -5
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for PollInterval = -5")
	}
}

func TestValidateConfig_pollTimeoutZero(t *testing.T) {
	cfg := validConfig()
	cfg.PollTimeout = 0
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for PollTimeout = 0")
	}
	if err.Error() != "telegram: poll_timeout must be between 1 and 60, got 0" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_pollTimeoutNegative(t *testing.T) {
	cfg := validConfig()
	cfg.PollTimeout = -1
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for PollTimeout = -1")
	}
}

func TestValidateConfig_pollTimeoutTooLarge(t *testing.T) {
	cfg := validConfig()
	cfg.PollTimeout = 61
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for PollTimeout = 61")
	}
	if err.Error() != "telegram: poll_timeout must be between 1 and 60, got 61" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_pollTimeoutAtBoundary(t *testing.T) {
	// Both boundaries should be valid.
	cfg := validConfig()

	cfg.PollTimeout = 1
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("PollTimeout=1 should be valid, got %v", err)
	}

	cfg.PollTimeout = 60
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("PollTimeout=60 should be valid, got %v", err)
	}
}

func TestValidateConfig_maxMsgLengthZero(t *testing.T) {
	cfg := validConfig()
	cfg.MaxMsgLength = 0
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for MaxMsgLength = 0")
	}
	if err.Error() != "telegram: max_msg_length must be between 1 and 4096, got 0" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_maxMsgLengthTooLarge(t *testing.T) {
	cfg := validConfig()
	cfg.MaxMsgLength = 4097
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for MaxMsgLength = 4097")
	}
	if err.Error() != "telegram: max_msg_length must be between 1 and 4096, got 4097" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_maxMsgLengthNegative(t *testing.T) {
	cfg := validConfig()
	cfg.MaxMsgLength = -10
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for MaxMsgLength = -10")
	}
}

func TestValidateConfig_maxMsgLengthAtBoundary(t *testing.T) {
	cfg := validConfig()

	cfg.MaxMsgLength = 1
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("MaxMsgLength=1 should be valid, got %v", err)
	}

	cfg.MaxMsgLength = 4096
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("MaxMsgLength=4096 should be valid, got %v", err)
	}
}

func TestValidateConfig_sessionTTLZero(t *testing.T) {
	cfg := validConfig()
	cfg.SessionTTL = 0
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for SessionTTL = 0")
	}
	if err.Error() != "telegram: session_ttl_hours must be >= 1, got 0" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateConfig_sessionTTLNegative(t *testing.T) {
	cfg := validConfig()
	cfg.SessionTTL = -1
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for SessionTTL = -1")
	}
}

func TestValidateConfig_returnsFirstError(t *testing.T) {
	// Multiple invalid fields: only the first error (Token empty) should be returned.
	cfg := TelegramConfig{
		Token:        "",
		PollInterval: 0,
		PollTimeout:  99,
		MaxMsgLength: 0,
		SessionTTL:   0,
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for multiple invalid fields")
	}
	if err.Error() != "telegram: bot_token must not be empty" {
		t.Errorf("expected first error about token, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// parseInt64List helper
// ---------------------------------------------------------------------------

func TestParseInt64List(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int64
	}{
		{"empty string", "", []int64{}},
		{"single", "42", []int64{42}},
		{"multiple", "1,2,3", []int64{1, 2, 3}},
		{"with whitespace", "  -100,  5 , 99 ", []int64{-100, 5, 99}},
		{"trailing comma", "10,20,", []int64{10, 20}},
		{"invalid entries skipped", "abc, 42, 12.5, 99, xyz-", []int64{42, 99}},
		{"all invalid", "abc, def", []int64{}},
		{"mixed valid and empty", " , 42, , 99 ", []int64{42, 99}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseInt64List(tt.input)
			if !equalInt64Slice(got, tt.want) {
				t.Errorf("parseInt64List(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sub-table style: ValidateConfig
// ---------------------------------------------------------------------------

func TestValidateConfig_table(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TelegramConfig
		wantErr bool
		errMsg  string // empty means don't check message
	}{
		{
			name:    "valid default-based",
			cfg:     validConfig(),
			wantErr: false,
		},
		{
			name: "empty token",
			cfg: TelegramConfig{
				Token:        "",
				PollInterval: 1,
				PollTimeout:  30,
				MaxMsgLength: 4096,
				SessionTTL:   24,
			},
			wantErr: true,
			errMsg:  "bot_token must not be empty",
		},
		{
			name: "poll interval 0",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.PollInterval = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "poll_interval must be >= 1",
		},
		{
			name: "poll timeout 0",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.PollTimeout = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "poll_timeout must be between 1 and 60",
		},
		{
			name: "poll timeout 61",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.PollTimeout = 61
				return c
			}(),
			wantErr: true,
			errMsg:  "poll_timeout must be between 1 and 60",
		},
		{
			name: "max msg length 0",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.MaxMsgLength = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "max_msg_length must be between 1 and 4096",
		},
		{
			name: "max msg length 4097",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.MaxMsgLength = 4097
				return c
			}(),
			wantErr: true,
			errMsg:  "max_msg_length must be between 1 and 4096",
		},
		{
			name: "session ttl 0",
			cfg: func() TelegramConfig {
				c := validConfig()
				c.SessionTTL = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "session_ttl_hours must be >= 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if err.Error() != tt.errMsg && !contains(err.Error(), tt.errMsg) {
					t.Errorf("error message = %q, want containing %q", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// unsetAllEnvVars removes every ODEK_TELEGRAM_* environment variable so that
// tests start from a clean slate.
func unsetAllEnvVars(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		if len(e) >= 14 && e[:14] == "ODEK_TELEGRAM_" {
			kv := splitAt(e, '=')
			os.Unsetenv(kv)
		}
	}
}

// splitAt splits s at the first occurrence of sep and returns the key portion.
func splitAt(s string, sep byte) string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i]
		}
	}
	return s
}

// validConfig returns a TelegramConfig that passes ValidateConfig.
func validConfig() TelegramConfig {
	return TelegramConfig{
		Token:        "test:token",
		PollInterval: 1,
		PollTimeout:  30,
		MaxMsgLength: 4096,
		SessionTTL:   24,
	}
}

func equalInt64Slice(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
