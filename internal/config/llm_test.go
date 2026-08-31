package config

import (
	"testing"
	"time"
)

func TestLLMStreamIdleTimeoutFrom(t *testing.T) {
	cases := []struct {
		name string
		llm  *LLMConfig
		env  *int
		want time.Duration
	}{
		{"nil section, no env", nil, nil, 0},
		{"file value", &LLMConfig{StreamIdleTimeoutSeconds: 90}, nil, 90 * time.Second},
		{"env wins over file", &LLMConfig{StreamIdleTimeoutSeconds: 90}, testIntPtr(30), 30 * time.Second},
		{"env only", nil, testIntPtr(45), 45 * time.Second},
		{"env non-positive ignored", &LLMConfig{StreamIdleTimeoutSeconds: 90}, testIntPtr(0), 90 * time.Second},
		{"floor clamps to 5s", &LLMConfig{StreamIdleTimeoutSeconds: 1}, nil, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := llmStreamIdleTimeoutFrom(tc.llm, tc.env); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func testIntPtr(v int) *int { return &v }
