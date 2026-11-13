package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The curl, wget and HTTPie (http, https, xh) credential rule must redact
// exactly what the release it shipped in (v2.37.3, commit acd5cc5c) redacts,
// unless the difference hides a password that release left readable.
// testdata/curl_base_golden.json holds that release's output for a corpus of
// the package's test literals that run one of these commands and of
// generated commands, each with the password planted in it.
//
// The golden file is written once from the release and checked in:
//
//  1. Here: REDACT_CURL_CORPUS_OUT=/tmp/curl-corpus.json go test -run TestWriteCurlBaseGolden ./internal/redact/
//  2. git worktree add /tmp/redact-base acd5cc5c, then copy this file into
//     /tmp/redact-base/internal/redact/ (it uses nothing the release lacks).
//  3. There: REDACT_CURL_CORPUS_IN=/tmp/curl-corpus.json REDACT_CURL_GOLDEN_OUT=<this
//     package>/testdata/curl_base_golden.json go test -run TestWriteCurlBaseGolden ./internal/redact/
//  4. git worktree remove /tmp/redact-base.

// curlGoldenCase is one corpus input, the password planted in it (empty for
// a test literal) and the release's output.
type curlGoldenCase struct {
	In      string `json:"in"`
	Planted string `json:"planted,omitempty"`
	Base    string `json:"base,omitempty"`
}

// curlGoldenImproved names, for test literals whose output differs from the
// release's, the password the release left readable and this one hides.
var curlGoldenImproved = map[string]string{}

func TestCurlRedactionMatchesReleaseBase(t *testing.T) {
	raw, err := os.ReadFile("testdata/curl_base_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []curlGoldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 200 {
		t.Fatalf("golden corpus holds %d cases", len(cases))
	}
	improved := 0
	for _, c := range cases {
		got := RedactSecrets(c.In)
		if got == c.Base {
			continue
		}
		planted := c.Planted
		if planted == "" {
			planted = curlGoldenImproved[c.In]
		}
		if planted == "" || !strings.Contains(c.Base, planted) || strings.Contains(got, planted) {
			t.Errorf("differs from the release: %q\n  release %q\n  now     %q", c.In, c.Base, got)
			continue
		}
		improved++
	}
	t.Logf("%d cases, %d differ by hiding a password the release left readable", len(cases), improved)
}

// curlGoldenTemplates are generated commands; PW stands for the planted
// password.
var curlGoldenTemplates = []string{
	"curl -u admin:PW https://x",
	"curl -uadmin:PW https://x",
	"curl -sSu admin:PW https://x",
	"curl --user admin:PW https://x",
	"curl --user=admin:PW https://x",
	"curl -U proxy:PW -x http://p https://x",
	"curl --proxy-user proxy:PW -x http://p https://x",
	`curl -u "admin:PW" https://x`,
	"curl -u 'admin:PW' https://x",
	`curl -H "X-Note: (draft)" -u admin:PW https://x`,
	`curl -d "f(x)" -u admin:PW https://x`,
	"curl -d `date` -u admin:PW https://x",
	"curl https://en.wikipedia.org/wiki/Foo_(bar) -u admin:PW",
	"$(curl -s -u admin:PW https://x)",
	"echo `curl -s -u admin:PW https://x` done",
	"curl -u admin:PW https://x | jq .",
	"curl -u admin:PW https://x; echo ok",
	`curl -b "session=PW" https://x`,
	`curl --cookie "sid=PW; theme=dark" https://x`,
	"wget --password=PW https://x",
	"wget --http-password PW https://x",
	`wget --header="X: (a)" --password=PW https://x`,
	"http -a admin:PW POST x",
	`http POST x "q=f(x)" -a admin:PW`,
	"https --auth admin:PW x",
	"xh --auth=admin:PW x",
}

// curlGoldenPasswords are planted into the templates.
var curlGoldenPasswords = []string{"S3cretPass9", "Tr0ub4dor&3x", "hunter2hunter2"}

// curlGoldenContexts wrap a command the way tool output carries it.
var curlGoldenContexts = []func(string) string{
	func(s string) string { return s },
	func(s string) string { return `{"command":"` + curlGoldenJSON(s) + `"}` },
	func(s string) string { return `{"cmd":"echo hi\n` + curlGoldenJSON(s) + `"}` },
	func(s string) string { return `cmd := exec.Command("sh", "-c", "` + curlGoldenJSON(s) + `")` },
	func(s string) string { return "sh -c '" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
	func(s string) string { return "Run `" + s + "` to check." },
}

// curlGoldenJSON encodes s as the body of a JSON string.
func curlGoldenJSON(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s)
}

// curlGoldenCommandIn reports whether s runs curl, wget, http, https or xh:
// the name stands as a word followed by a blank, a backslash or the end.
func curlGoldenCommandIn(s string) bool {
	l := strings.ToLower(s)
	for _, name := range []string{"curl", "wget", "http", "https", "xh"} {
		for i := strings.Index(l, name); i >= 0; {
			e := i + len(name)
			before := i == 0 || strings.IndexByte("abcdefghijklmnopqrstuvwxyz0123456789_-.", l[i-1]) < 0 || (i >= 2 && l[i-2] == '\\')
			if before && (e == len(l) || strings.IndexByte(" \t\n\r\\", l[e]) >= 0) {
				return true
			}
			k := strings.Index(l[i+1:], name)
			if k < 0 {
				break
			}
			i += 1 + k
		}
	}
	return false
}

// curlGoldenCorpus is the package's test literals that run one of the
// commands, then every template with every password in every context.
func curlGoldenCorpus(t *testing.T) []curlGoldenCase {
	var out []curlGoldenCase
	for _, s := range gateTestLiterals(t) {
		if len(s) <= 4096 && curlGoldenCommandIn(s) {
			out = append(out, curlGoldenCase{In: s})
		}
	}
	for k, tpl := range curlGoldenTemplates {
		for p, pw := range curlGoldenPasswords {
			for c, wrap := range curlGoldenContexts {
				if (k+p+c)%3 != 0 { // a third of the combinations keeps the file small
					continue
				}
				out = append(out, curlGoldenCase{In: wrap(strings.ReplaceAll(tpl, "PW", pw)), Planted: pw})
			}
		}
	}
	return out
}

// TestWriteCurlBaseGolden writes the corpus, or the golden file from a
// corpus; see the comment at the top of this file.
func TestWriteCurlBaseGolden(t *testing.T) {
	if out := os.Getenv("REDACT_CURL_CORPUS_OUT"); out != "" {
		curlGoldenWrite(t, out, curlGoldenCorpus(t))
		return
	}
	in, out := os.Getenv("REDACT_CURL_CORPUS_IN"), os.Getenv("REDACT_CURL_GOLDEN_OUT")
	if in == "" || out == "" {
		t.Skip("writes the golden file only when asked")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var cases []curlGoldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for i := range cases {
		cases[i].Base = RedactSecrets(cases[i].In)
	}
	curlGoldenWrite(t, out, cases)
}

func curlGoldenWrite(t *testing.T, path string, cases []curlGoldenCase) {
	b, err := json.MarshalIndent(cases, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
