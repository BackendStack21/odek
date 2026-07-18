package redact

import (
	"strings"
	"testing"
)

func TestRedactSecrets_Empty(t *testing.T) {
	if r := RedactSecrets(""); r != "" {
		t.Fatalf("empty string should stay empty, got %q", r)
	}
}

func TestRedactSecrets_NoSecrets(t *testing.T) {
	input := "Hello, this is a normal message with no secrets."
	if r := RedactSecrets(input); r != input {
		t.Fatalf("no-secret text should be unchanged: %q", r)
	}
}

func TestRedactSecrets_OpenAIKey(t *testing.T) {
	tests := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz123456",
		"sk-1234567890abcdefghijklmnopqrstuv",
		// Anthropic-style keys contain underscores in the body.
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz_1234567890",
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if result != "[REDACTED]" {
			t.Errorf("OpenAI key not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_ProviderKeys(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"groq", "gsk_abcdefghijklmnopqrstuvwxyz1234567890"},
		{"xai", "xai-abcdefghijklmnopqrstuvwxyz1234567890_0123456789"},
		{"huggingface", "hf_abcdefghijklmnopqrstuvwxyz1234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := RedactSecrets(tc.secret)
			if result != "[REDACTED]" {
				t.Errorf("%s key not redacted: %q → %q", tc.name, tc.secret, result)
			}
		})
	}
}

func TestRedactSecrets_GitHubToken(t *testing.T) {
	tests := []string{
		"ghp_abcdefghijklmnopqrstuvwxyz1234567890",
		"github_pat_1234567890abcdefghijklmn",
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if result != "[REDACTED]" {
			t.Errorf("GitHub token not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_AWSAccessKey(t *testing.T) {
	input := "AKIAIOSFODNN7EXAMPLE"
	result := RedactSecrets(input)
	if result != "[REDACTED]" {
		t.Errorf("AWS access key not redacted: %q → %q", input, result)
	}
}

func TestRedactSecrets_JWT(t *testing.T) {
	tests := []string{
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3q_8aFvJ6kTlM2A",
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if result != "[REDACTED]" {
			t.Errorf("JWT not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_GenericAPIKey(t *testing.T) {
	tests := []string{
		`api_key=abcdefghijklmnopqrstuvwxyz1234`,
		`API_KEY="abcdefghijklmnopqrstuvwxyz1234"`,
		`api_secret: xyZ9876543210abcdefghijklmn`,
		`auth_token=mySecretTokenThatIsLongEnough123`,
		`access_token=a1b2c3d4e5f6g7h8i9j0k1l2m3`,
		`bearer_token=thisIsABearerTokenThatIsLong20`,
		`password=superSecretPassword_12345abcd`,
		`private_key=myPrivateKeyValue_ThatIs20Plus`,
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if !strings.Contains(result, "[REDACTED]") {
			t.Errorf("generic key not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_AuthorizationHeader(t *testing.T) {
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dummySignatureHere"
	result := RedactSecrets(input)
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("Authorization header not redacted: %q → %q", input, result)
	}
}

func TestRedactSecrets_SlackToken(t *testing.T) {
	// Build test tokens at runtime to avoid GitHub push protection
	// false positives on realistic-looking prefixes.
	mkSlack := func(parts ...string) string {
		return parts[0] + parts[1] + parts[2]
	}
	tests := []string{
		mkSlack("xoxa", "-0000000000-0000000000-", "testTokenForRedact99999999"),
		mkSlack("xoxs", "-9999999999-9999999999-", "anotherTestTokenForRedactOnly"),
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if result != "[REDACTED]" {
			t.Errorf("Slack token not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_StripeKey(t *testing.T) {
	// Build test tokens at runtime to avoid GitHub push protection
	// false positives on the sk_live_/rk_live_ prefixes.
	mkStripe := func(parts ...string) string {
		return parts[0] + parts[1] + parts[2]
	}
	tests := []string{
		mkStripe("sk", "_live_", "BOGUSBOGUSBOGUSBOGUSBOGUSBOGUS99"),
		mkStripe("sk", "_test_", "TOTALLYFAKETOTALLYFAKETOTALLYFAKE"),
		mkStripe("rk", "_live_", "THISAINTREALKEYTHISAINTREALKEY9999"),
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if result != "[REDACTED]" {
			t.Errorf("Stripe key not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_PrivateKey(t *testing.T) {
	input := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3Jx...
-----END RSA PRIVATE KEY-----`
	result := RedactSecrets(input)
	if result != "[REDACTED]" {
		t.Errorf("RSA private key not redacted: got %q", result)
	}

	input2 := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAA...
-----END OPENSSH PRIVATE KEY-----`
	result2 := RedactSecrets(input2)
	if result2 != "[REDACTED]" {
		t.Errorf("OpenSSH private key not redacted: got %q", result2)
	}

	// PKCS#8 — default openssl genpkey output
	input3 := `-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0B...
-----END PRIVATE KEY-----`
	result3 := RedactSecrets(input3)
	if result3 != "[REDACTED]" {
		t.Errorf("PKCS#8 private key not redacted: got %q", result3)
	}

	// PKCS#8 encrypted
	input4 := `-----BEGIN ENCRYPTED PRIVATE KEY-----
MIIFHzBJBgkqhkiG9w0B...
-----END ENCRYPTED PRIVATE KEY-----`
	result4 := RedactSecrets(input4)
	if result4 != "[REDACTED]" {
		t.Errorf("encrypted PKCS#8 private key not redacted: got %q", result4)
	}

	// ED25519
	input5 := `-----BEGIN ED25519 PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIE...
-----END ED25519 PRIVATE KEY-----`
	result5 := RedactSecrets(input5)
	if result5 != "[REDACTED]" {
		t.Errorf("ED25519 private key not redacted: got %q", result5)
	}
}

func TestRedactSecrets_EnvVarCredentials(t *testing.T) {
	tests := []string{
		`export OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz123456`,
		`GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234567890`,
		`DATABASE_PASSWORD=superSecretDBPass_2024!!`,
		`AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
	}
	for _, input := range tests {
		result := RedactSecrets(input)
		if !strings.Contains(result, "[REDACTED]") {
			t.Errorf("env var credential not redacted: %q → %q", input, result)
		}
	}
}

func TestRedactSecrets_MixedContent(t *testing.T) {
	input := `Here is my config:
API_KEY="sk-abc123def456ghi789jkl012mno345pqr"
SERVER_URL="https://api.example.com"
DB_PASSWORD="myDatabasePassword_12345"

And some code:
const token = "not_a_secret"
const api_key="actualSecretKey_ThatIs20Plus"
`
	result := RedactSecrets(input)

	// The API key should be redacted
	if strings.Contains(result, "sk-abc123def456ghi789jkl012mno345pqr") {
		t.Error("OpenAI key still visible in mixed content")
	}
	// Normal text should survive
	if !strings.Contains(result, "SERVER_URL") {
		t.Error("non-secret content was removed")
	}
	if !strings.Contains(result, "const token") {
		t.Error("short token was incorrectly redacted")
	}
	if !strings.Contains(result, "example.com") {
		t.Error("URL was incorrectly removed")
	}
}

func TestRedactSecrets_FalsePositives(t *testing.T) {
	// These are legitimate strings that should NOT be redacted
	safe := []string{
		"const token = 'short'",
		"api_key = 'test'", // too short
		"export NODE_ENV=production",
		"UUID: 550e8400-e29b-41d4-a716-446655440000",
		"const SKU_COUNT = 42",
		"access_token_refresh_interval", // no = after
		"Hello World",                   // no secrets
		"echo 'export FOO=bar'",         // shell command, not env
	}
	for _, input := range safe {
		result := RedactSecrets(input)
		if result != input {
			t.Errorf("false positive: %q → %q", input, result)
		}
	}
}

func TestHasSecrets(t *testing.T) {
	if HasSecrets("") {
		t.Error("empty string should have no secrets")
	}
	if HasSecrets("hello world") {
		t.Error("plain text should have no secrets")
	}
	if !HasSecrets("sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Error("OpenAI key should be detected")
	}
}

func TestIsSafe(t *testing.T) {
	if !IsSafe("hello world") {
		t.Error("plain text should be safe")
	}
	if IsSafe("sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Error("OpenAI key should not be safe")
	}
}

func TestCountSecrets(t *testing.T) {
	if n := CountSecrets(""); n != 0 {
		t.Errorf("empty: got %d, want 0", n)
	}
	if n := CountSecrets("hello"); n != 0 {
		t.Errorf("clean: got %d, want 0", n)
	}
	// Multiple secrets in one string
	input := "key1=sk-abcdefghijklmnopqrstuvwxyz123456\nkey2=AKIAIOSFODNN7EXAMPLE"
	n := CountSecrets(input)
	if n < 1 {
		t.Errorf("multiple secrets: got %d, want >= 1", n)
	}
}

func TestRedactWithCount(t *testing.T) {
	input := "API_KEY=sk-abc123def456ghi789jkl012mno345pqr"
	result, count := RedactWithCount(input)
	// Both the OpenAI key pattern and the generic API key pattern match,
	// so we expect >= 1 (typically 2).
	if count < 1 {
		t.Errorf("expected >= 1 secret, got %d", count)
	}
	if strings.Contains(result, "sk-") {
		t.Error("secret not redacted")
	}
}

func TestRedactChunk(t *testing.T) {
	chunk, had := RedactChunk("hello")
	if had {
		t.Error("clean chunk flagged as having secrets")
	}
	if chunk != "hello" {
		t.Errorf("clean chunk modified: %q", chunk)
	}

	chunk, had = RedactChunk("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !had {
		t.Error("secret chunk not flagged")
	}
	if chunk != "[REDACTED]" {
		t.Errorf("secret not redacted: %q", chunk)
	}
}

func TestSanitizeForLog(t *testing.T) {
	if s := SanitizeForLog("hello"); s != "hello" {
		t.Errorf("clean log: got %q", s)
	}
	s := SanitizeForLog("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !strings.Contains(s, "[REDACTED]") {
		t.Errorf("secret log not sanitized: %q", s)
	}
}

func TestRedactSecrets_GoogleAPIKey(t *testing.T) {
	input := "AIzaSyD-J2q3vQx8kLmN9pR5tU2wA1bC4dE6fG8h"
	result := RedactSecrets(input)
	if result != "[REDACTED]" {
		t.Errorf("Google API key not redacted: %q → %q", input, result)
	}
}

func TestRedactSecrets_TwilioKey(t *testing.T) {
	// Build at runtime to avoid GitHub push protection false positive.
	input := "SK" + "deadbeef12345678deadbeef12345678"
	result := RedactSecrets(input)
	if result != "[REDACTED]" {
		t.Errorf("Twilio key not redacted: %q → %q", input, result)
	}
}

// ── Benchmark ─────────────────────────────────────────────────────────

func BenchmarkRedactSecrets_Clean(b *testing.B) {
	input := "This is a normal message with no secrets whatsoever in it."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RedactSecrets(input)
	}
}

func BenchmarkRedactSecrets_OneSecret(b *testing.B) {
	input := "Here is my key: sk-proj-abcdefghijklmnopqrstuvwxyz123456. Use it wisely."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RedactSecrets(input)
	}
}

func BenchmarkRedactSecrets_ManySecrets(b *testing.B) {
	input := strings.Repeat("key=abcdefghijklmnopqrstuvwxyz1234\n", 10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RedactSecrets(input)
	}
}

func BenchmarkHasSecrets_Clean(b *testing.B) {
	input := "No secrets here at all."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HasSecrets(input)
	}
}

func BenchmarkHasSecrets_OneSecret(b *testing.B) {
	input := "Token: ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HasSecrets(input)
	}
}
