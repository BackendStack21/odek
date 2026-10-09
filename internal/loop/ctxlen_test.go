package loop

import (
	"errors"
	"testing"
)

// Real provider wordings for an over-long prompt must be recognised so the
// engine retries with aggressive trimming instead of failing the session.
func TestRED_ContextLengthErrorProviderWordings(t *testing.T) {
	cases := map[string]string{
		"anthropic":          `anthropic: HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		"gemini":             `gemini: HTTP 400: {"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`,
		"openai-capitalised": `HTTP 400: This model's Maximum Context Length is 128000 tokens. However, your messages resulted in 130000 tokens.`,
	}
	for name, msg := range cases {
		if !isContextLengthError(errors.New(msg)) {
			t.Errorf("%s: context-length wording not detected: %s", name, msg)
		}
	}
}
