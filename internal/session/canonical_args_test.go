package session

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// canonicalArgsCorpus are argument texts the canonical form must accept,
// in the shapes providers send: spacing, key order, pretty printing, Go's
// HTML escapes, escaped slashes, unicode escapes, numbers as written.
var canonicalArgsCorpus = []string{
	`{"command":"ls -la"}`,
	`{"command": "curl -s https://feed.example/x -o x"}`,
	`{"path":"notes.md","content":"see https://feed.example/x"}`,
	"{\n  \"command\": \"curl -s https://feed.example/x -o x\"\n}",
	`{"command":"go build ./... && ./app > out.log"}`,
	`{"command":"curl -s https:\/\/feed.example\/x"}`,
	`{"q":"café   line","n":1.000e10,"m":-0,"big":123456789012345678901234567890}`,
	`{"files":[{"path":"b.go"},{"path":"a.go"}],"path_a":"x","path_b":"y"}`,
	`{"nested":{"z":[1,2,{"y":null,"x":true}],"a":false}}`,
	`{"html":"<b>&amp;</b>"}`,
	`{}`,
	`[]`,
	`"just a string"`,
	`42`,
	`null`,
	`  {"padded":"value"}  `,
	"{\"Key\":1,\"Kelvin\":2}",
	`{"path":"\ud800"}`,
	// Field matching folds case only: separators and their absence name
	// different fields, so these keys are distinct.
	`{"path_a":"x","pathA":"y","path-a":"z","job_id":"j","jobId":"k","job-id":"l"}`,
}

// canonicalArgsRefused are argument texts whose canonical form could decode
// differently, or that the store does not rewrite canonically.
var canonicalArgsRefused = map[string]string{
	"empty":                  ``,
	"not json":               `ls -la`,
	"exact duplicate":        `{"path":"a","path":"b"}`,
	"case-folded duplicate":  `{"path":"README.md","PATH":"/etc/x"}`,
	"kelvin-folded":          "{\"key\":1,\"Key\":2}",
	"nested duplicate":       `{"files":[{"path":"a","Path":"b"}]}`,
	"duplicate container":    `{"files":[{"path":"a"}],"files":[{}]}`,
	"trailing close":         `{"a":1}]`,
	"trailing value":         `{"a":1} {"b":2}`,
	"byte order mark":        "\xef\xbb\xbf{\"a\":1}",
	"truncated":              `{"command":"ls`,
	"single-quoted":          `{'a':1}`,
	"trailing comma":         `{"a":1,}`,
	"duplicate in array obj": `[{"x":1,"X":2}]`,
	"separator kept, case":   `{"path_a":"a","PATH_A":"b"}`,
	// Already canonical text (keys sorted, compact) still refused for keys
	// differing only in case.
	"canonical, case-folded": `{"PATH":"/etc/x","path":"README.md"}`,
	"canonical, nested":      `{"files":[{"Path":"a","path":"b"}]}`,
}

// The canonical form is what the store writes for arguments without a
// secret, it is a fixed point, and anything it refuses is refused for a
// reason that can change a decode.
func TestCanonicalToolArguments_MatchesStoreEncoding(t *testing.T) {
	for _, args := range canonicalArgsCorpus {
		canon, ok := CanonicalToolArguments(args)
		if !ok {
			t.Errorf("%q refused", args)
			continue
		}
		if stored := redactToolArguments(args); stored != canon {
			t.Errorf("%q: canonical %q, the store writes %q", args, canon, stored)
		}
		again, ok := CanonicalToolArguments(canon)
		if !ok || again != canon {
			t.Errorf("%q: canonical form %q is not a fixed point (%q, %v)", args, canon, again, ok)
		}
	}
	for name, args := range canonicalArgsRefused {
		if canon, ok := CanonicalToolArguments(args); ok {
			t.Errorf("%s: %q accepted as %q", name, args, canon)
		}
	}
}

// For arguments the canonical form accepts, every struct decode reads the
// same from the original and from the canonical text: the property the
// coverage digest relies on (internal/loop taintCoverageArguments).
func TestCanonicalToolArguments_DecodesAlike(t *testing.T) {
	type decision struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		PathA   string `json:"path_a"`
		PathB   string `json:"path_b"`
		Action  string `json:"action"`
		JobID   string `json:"job_id"`
		Files   []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	check := func(args string) {
		t.Helper()
		canon, ok := CanonicalToolArguments(args)
		if !ok {
			return
		}
		var a, b decision
		errA := json.Unmarshal([]byte(args), &a)
		errB := json.Unmarshal([]byte(canon), &b)
		if (errA == nil) != (errB == nil) || fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%q and its canonical form %q decode differently: %+v (%v) / %+v (%v)", args, canon, a, errA, b, errB)
		}
		for _, name := range []string{"read_file", "shell", "bg_start", "memory", "http_request", "srv__tool"} {
			if ToolCallTaintsEpisode(name, args) != ToolCallTaintsEpisode(name, canon) {
				t.Errorf("%s %q: the episode rule judges the canonical form %q differently", name, args, canon)
			}
		}
	}
	for _, args := range canonicalArgsCorpus {
		check(args)
	}
	for _, args := range canonicalArgsRefused {
		check(args)
	}
	r := rand.New(rand.NewPCG(20261112, 2))
	n := 3000
	if testing.Short() {
		n = 600
	}
	for i := 0; i < n; i++ {
		check(randomToolArgs(r))
	}
}

// Random argument texts, written the way a provider might: any key order,
// spacing, escapes, case variants of the keys the decisions read (which can
// make a case-folded duplicate) and repeated keys.
func TestCanonicalToolArguments_RandomMatchesStore(t *testing.T) {
	r := rand.New(rand.NewPCG(20261112, 1))
	n := 3000
	if testing.Short() {
		n = 600
	}
	accepted := 0
	for i := 0; i < n; i++ {
		args := randomToolArgs(r)
		canon, ok := CanonicalToolArguments(args)
		if !ok {
			continue
		}
		accepted++
		if stored := redactToolArguments(args); stored != canon {
			t.Fatalf("%q: canonical %q, the store writes %q", args, canon, stored)
		}
		if again, ok := CanonicalToolArguments(canon); !ok || again != canon {
			t.Fatalf("%q: canonical form %q is not a fixed point", args, canon)
		}
	}
	if accepted < n/2 {
		t.Fatalf("only %d of %d random argument texts were accepted", accepted, n)
	}
}

// randomToolArgs returns a random JSON object text. Values never hold a
// secret the redactor would change, and keys never name a credential.
func randomToolArgs(r *rand.Rand) string {
	keys := []string{"command", "path", "path_a", "path_b", "files", "action", "job_id", "url", "content", "timeout_seconds"}
	var b strings.Builder
	var value func(depth int)
	space := func() {
		switch r.IntN(4) {
		case 0:
			b.WriteString(" ")
		case 1:
			b.WriteString("\n  ")
		}
	}
	str := func() {
		parts := []string{"ls -la", "a && b > c", "https://x.example/p?q=1", "/srv/data/f.log", "café", `quote \" here`, `back\\slash`, "\\u0026", "\\/", "<tag>", "\\u2028"}
		b.WriteByte('"')
		for k := 0; k < 1+r.IntN(3); k++ {
			b.WriteString(parts[r.IntN(len(parts))])
		}
		b.WriteByte('"')
	}
	object := func(depth int) {
		b.WriteByte('{')
		n := r.IntN(4)
		for k := 0; k < n; k++ {
			if k > 0 {
				b.WriteByte(',')
			}
			space()
			key := keys[r.IntN(len(keys))]
			switch r.IntN(10) {
			case 0:
				key = strings.ToUpper(key)
			case 1:
				key = strings.ToUpper(key[:1]) + key[1:]
			case 2:
				key = strings.ReplaceAll(key, "_", "")
			case 3:
				key = strings.ReplaceAll(key, "_", "-")
			}
			b.WriteString(`"` + key + `"`)
			space()
			b.WriteByte(':')
			space()
			value(depth + 1)
		}
		space()
		b.WriteByte('}')
	}
	value = func(depth int) {
		switch c := r.IntN(8); {
		case c == 0 && depth < 3:
			object(depth)
		case c == 1 && depth < 3:
			b.WriteByte('[')
			for k := 0; k < r.IntN(3); k++ {
				if k > 0 {
					b.WriteByte(',')
				}
				value(depth + 1)
			}
			b.WriteByte(']')
		case c == 2:
			b.WriteString([]string{"1", "1.0", "-0", "1e3", "12345678901234567890"}[r.IntN(5)])
		case c == 3:
			b.WriteString([]string{"true", "false", "null"}[r.IntN(3)])
		default:
			str()
		}
	}
	object(0)
	return b.String()
}
