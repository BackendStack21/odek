package redact

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// TestTelegramBotTokenPattern covers the format-pattern gap: a bare Telegram
// bot token has no name= context and no recognised key prefix, so before the
// dedicated pattern it slipped through unredacted.
func TestTelegramBotTokenPattern(t *testing.T) {
	ResetSecrets()
	token := "123456789:AAHfakeTokenValueExample0123456789abcdef"
	out := RedactSecrets("bot token is " + token)
	if strings.Contains(out, token) {
		t.Fatalf("telegram token not redacted by pattern: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got %q", out)
	}
}

// TestKnownValue_BareEcho covers the core gap: a registered secret whose
// format no pattern recognises must still be redacted when printed verbatim.
func TestKnownValue_BareEcho(t *testing.T) {
	ResetSecrets()
	defer ResetSecrets()

	// A token shape no built-in pattern matches.
	secret := "xz9-CUSTOM-internal-credential-2f7b1c4e8d"
	RegisterSecret(secret)

	if got := RedactSecrets("value: " + secret); strings.Contains(got, secret) {
		t.Fatalf("registered secret leaked in bare echo: %q", got)
	}
	if !HasSecrets(secret) {
		t.Fatalf("HasSecrets should detect a registered value")
	}
}

// TestKnownValue_Encodings covers the "echo $KEY | base64 / xxd" gap: common
// encodings of a registered secret must also be redacted.
func TestKnownValue_Encodings(t *testing.T) {
	ResetSecrets()
	defer ResetSecrets()

	secret := "sk-ant-internal-do-not-leak-abcdef0123456789"
	RegisterSecret(secret)
	b := []byte(secret)

	cases := map[string]string{
		"raw":        secret,
		"base64-std": base64.StdEncoding.EncodeToString(b),
		"base64-raw": base64.RawStdEncoding.EncodeToString(b),
		"base64-url": base64.URLEncoding.EncodeToString(b),
		"hex-lower":  hex.EncodeToString(b),
		"hex-upper":  strings.ToUpper(hex.EncodeToString(b)),
		"reversed":   reverseString(secret),
	}
	for name, enc := range cases {
		out := RedactSecrets("leaked=" + enc)
		if strings.Contains(out, enc) {
			t.Errorf("%s encoding leaked: %q", name, out)
		}
	}
}

// TestKnownValue_ProcEnvironDump simulates `cat /proc/self/environ`, whose
// NUL-delimited output the literal matcher handles regardless of delimiters.
func TestKnownValue_ProcEnvironDump(t *testing.T) {
	ResetSecrets()
	defer ResetSecrets()

	secret := "telegram-bot-secret-value-9988776655"
	RegisterSecret(secret)

	dump := "PATH=/usr/bin\x00HOME=/root\x00TELEGRAM_BOT_TOKEN=" + secret + "\x00TERM=xterm"
	out := RedactSecrets(dump)
	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked in /proc environ dump: %q", out)
	}
}

// TestRegisterSecretsFromEnv only registers values of sensitively-named vars.
func TestRegisterSecretsFromEnv(t *testing.T) {
	ResetSecrets()
	defer func() {
		osEnviron = defaultOsEnviron
		ResetSecrets()
	}()

	secret := "anthropic-key-value-abcdefghij1234567890"
	authorName := "Jane Developer"
	osEnviron = func() []string {
		return []string{
			"ANTHROPIC_API_KEY=" + secret,
			"GIT_AUTHOR_NAME=" + authorName, // AUTHOR must NOT be treated as secret
			"PATH=/usr/bin",
		}
	}
	RegisterSecretsFromEnv()

	if got := RedactSecrets("k=" + secret); strings.Contains(got, secret) {
		t.Errorf("env secret not redacted: %q", got)
	}
	if got := RedactSecrets("author is " + authorName); !strings.Contains(got, authorName) {
		t.Errorf("non-secret env var over-redacted: %q", got)
	}
}

// TestRegisterSecret_TooShortIgnored guards against over-redacting short
// values that would collide with ordinary text.
func TestRegisterSecret_TooShortIgnored(t *testing.T) {
	ResetSecrets()
	defer ResetSecrets()

	RegisterSecret("abc") // below minSecretLen
	if HasSecrets("abc appears in normal prose") {
		t.Fatalf("short value should not have been registered")
	}
}

// defaultOsEnviron preserves the production osEnviron for restore in tests.
var defaultOsEnviron = osEnviron
