package extended

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// echoLLM returns the fuzzed input as the LLM response, so Extract's parse
// path (extractJSON + json.Unmarshal) is exercised end to end.
type echoLLM struct {
	response string
}

func (e echoLLM) SimpleCall(_ context.Context, _, _ string) (string, error) {
	return e.response, nil
}

// FuzzExtractJSON feeds random/mutated LLM responses into extractJSON and
// asserts it never panics and that a "found" result is structurally sane: a
// non-empty bracketed span whose output is either valid JSON or fails
// downstream unmarshalling (i.e. the caller always ends up with valid JSON
// or an error — never a silent mis-parse).
func FuzzExtractJSON(f *testing.F) {
	seeds := []string{
		`[{"text":"User prefers tea","type":"preference","confidence":0.9}]`,
		"```json\n[{\"text\":\"a\",\"type\":\"fact\",\"confidence\":0.5}]\n```",
		"```\n{\"text\":\"no lang fence\"}\n```",
		`Here is the JSON you asked for: [{"text":"x"}] — hope this helps!`,
		`[{"text":"escaped \"quote\" and \\ backslash and \n newline"}]`,
		`[{"a":[{"b":[1,{"c":[2,3]}]}]}]`,
		`[1, 2, 3`,               // truncated
		`{"text": "unterminated`, // truncated object
		`[1,]`,                   // balanced but invalid JSON
		`[{"text":"} inside string {"}]`,
		`"[1,2,3]"`, // JSON string, not an array
		`not json at all`,
		``,
		`   `,
		"```json\n\n```",
		strings.Repeat("x", 1<<20), // 1 MiB of non-JSON
		"[" + strings.Repeat(`{"k":"v"},`, 5000) + `{}]`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		out, ok := extractJSON(input)
		if !ok {
			if out != "" {
				t.Fatalf("extractJSON returned ok=false with non-empty output %q", out)
			}
			return
		}
		if out == "" {
			t.Fatal("extractJSON returned ok=true with empty output")
		}
		if out[0] != '[' && out[0] != '{' {
			t.Fatalf("extractJSON output does not start with a bracket: %q", out[:1])
		}
		last := out[len(out)-1]
		if (out[0] == '[' && last != ']') || (out[0] == '{' && last != '}') {
			t.Fatalf("extractJSON output has mismatched brackets: starts %q, ends %q", out[:1], string(last))
		}
		// The downstream contract is "valid JSON or error": if the span is not
		// valid JSON, unmarshalling it into the atom slice must fail so Extract
		// surfaces an error instead of a silent mis-parse.
		if !json.Valid([]byte(out)) {
			var raw []struct {
				Text       string  `json:"text"`
				Content    string  `json:"content"`
				Type       string  `json:"type"`
				Confidence float32 `json:"confidence"`
			}
			if err := json.Unmarshal([]byte(out), &raw); err == nil {
				t.Fatalf("extractJSON returned invalid JSON that unmarshals cleanly: %q", out)
			}
		}
	})
}

// FuzzExtractParse drives the full Extract parse path with an echo LLM and
// asserts it never panics and always returns either atoms or an error.
func FuzzExtractParse(f *testing.F) {
	seeds := []string{
		`[{"text":"User prefers tea","type":"preference","confidence":0.9}]`,
		"```json\n[{\"text\":\"a\"}]\n```",
		`garbage [{"text":"x","type":"weird","confidence":42}] trailing`,
		`[{"content":"legacy field"}]`,
		`[{"text":""},{"text":"  "}]`,
		`[{"text":"no type or confidence"}]`,
		`[]`,
		`[1, 2, 3`,
		`{}`,
		`[{"text":"a"}]extra[garbage`,
		`null`,
		`"just a string"`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		ex := NewExtractor(echoLLM{response: input}, Config{})
		atoms, err := ex.Extract(context.Background(), "remember this")
		if err != nil {
			return
		}
		for _, a := range atoms {
			if strings.TrimSpace(a.Text) == "" {
				t.Fatalf("Extract returned an atom with empty text: %+v", a)
			}
			if a.Confidence <= 0 || a.Confidence > 1.0 {
				t.Fatalf("Extract returned atom with out-of-range confidence %v", a.Confidence)
			}
		}
	})
}
