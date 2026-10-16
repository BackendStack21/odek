package loop

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
)

// Untrusted-span escalation.
//
// The untrusted-content wrapper labels external text; it does not stop the
// model from copying an attacker-chosen URL, host, path or token out of that
// text into a tool call. The engine therefore records distinctive spans from
// every message the memory gate's episode rule treats as untrusted
// (session.ToolCallTaintsEpisode for tool results, session.ExternalNonToolIngest
// for wrapped content in other messages), and escalates a later tool call
// whose arguments reuse one: the call is shown to the principal under
// danger.UntrustedArgsClass before any other gate runs. That class is never
// session-trustable and is denied without an interactive channel, so a
// configured allow, a class trust grant, an approved batch or
// non_interactive=allow cannot pass it.
//
// Span rules (taint_spans.go is the reference):
//   - url: absolute URLs (http, https, ws, wss, ftp, sftp, ssh, git) as
//     scheme://host[:port]/path, query and fragment dropped; the full URL is
//     also kept as a link for the link-following rule below.
//   - host: the host of every recorded URL, IPv4 literals, and schemeless
//     host words followed by ":" or "/" whose last label is not a common
//     file extension. Bare hosts on a short list of ubiquitous sites
//     (github.com, pypi.org, …) are not recorded; their URLs still are.
//     Schemeless module paths of Go import specs and go.mod/go.sum lines
//     read by a file tool from .go, go.mod, go.sum or module-cache paths are
//     not recorded either (taintGoSource, taintGoModuleLine); the same line
//     shapes from any other source record their host.
//   - path: absolute, ~/, $HOME/ and ${HOME}/ paths (cleaned) with at least two segments, outside the
//     working directory, the sandbox /workspace mount, the home directory's
//     ancestors, /dev/{null,stdin,stdout,stderr} and the standard bin
//     directories.
//   - token: opaque words of 32–512 characters mixing upper case, lower case
//     and digits that are not hex/UUID-shaped.
//
// A span that the principal's own messages mention (outside any untrusted
// wrapper) never escalates: hosts and their subdomains, URLs on such hosts
// (on the shared sites in taintCommonHosts only the mentioned URL and the
// URLs under it), paths and their descendants, tokens verbatim.
//
// Which calls escalate:
//   - shell / terminal / bg_start: any span, unless the command classifies
//     safe. A command that is plain network egress follows the egress rule.
//   - write_file / patch: a path span in the target path (content is never
//     matched: writing up research with links is ordinary work).
//   - MCP tools (<server>__<tool>): any span in any string argument or key.
//   - http_request and browser navigate: the egress rule when the request is
//     plain network egress; any span in the URL and header values otherwise
//     (uploads, credentialed requests, internal targets).
//
// Egress rule: fetching a URL exactly as it appeared in untrusted content,
// or navigating by path on a host it named, is how browsing works and does
// not escalate. A URL that carries a query string or userinfo the content
// did not contain verbatim, on a recorded host or URL, is the shape of data
// being appended to an attacker endpoint and escalates. Spans outside URLs
// (paths, tokens, bare hosts) still escalate. The rule judges URL text only,
// so it never applies to a command that sends request data, headers or query
// options beside the URL (danger.Analysis.RequestData) or to an http_request
// with a header other than Accept, Accept-Language or User-Agent.
//
// The store is bounded (spans, links and scanned-message digests are capped;
// the least recently seen spans are evicted first) and lives on the engine,
// so it survives context trimming within one engine; a rebuilt engine
// re-seeds from the history it is given. It is a heuristic: a span the model
// transforms (re-encodes, splits, retypes) is not recognised, and data
// encoded into the path of a plain GET is not escalated.

const (
	maxTaintSpans          = 4096
	maxTaintLinks          = 4096
	maxTaintScanned        = 16384
	taintScanMaxBytes      = 1 << 20
	taintPrincipalMaxBytes = 256 << 10
	taintMatchMaxBytes     = 64 << 10
	taintTokenMinLen       = 32
	taintTokenMaxLen       = 512
	taintDisplayMax        = 200
)

type taintSpanKind string

const (
	taintKindURL   taintSpanKind = "url"
	taintKindHost  taintSpanKind = "host"
	taintKindPath  taintSpanKind = "path"
	taintKindToken taintSpanKind = "token"
)

// TaintEscalation is one escalated tool call, handed to the recorder that
// WithTaintEscalationRecorder installs (surfaces write it to the audit log).
type TaintEscalation struct {
	Tool     string
	Kind     string // url, host, path or token
	Span     string // the reused span
	Source   string // where the span was first seen
	Resource string // the escalated command, path or URL
	Approved bool
}

type taintEscalationRecorderKey struct{}

// WithTaintEscalationRecorder returns a context whose runs report every
// untrusted-span escalation to fn.
func WithTaintEscalationRecorder(ctx context.Context, fn func(TaintEscalation)) context.Context {
	return context.WithValue(ctx, taintEscalationRecorderKey{}, fn)
}

// TaintEscalationRecorderFrom returns the recorder installed on ctx, if any.
func TaintEscalationRecorderFrom(ctx context.Context) func(TaintEscalation) {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(taintEscalationRecorderKey{}).(func(TaintEscalation))
	return fn
}

type taintSpan struct {
	source string
	seq    uint64
}

// taintSpan keys are kind + "\x00" + normalised value.
func taintKey(kind taintSpanKind, v string) string { return string(kind) + "\x00" + v }

// taintSpanStore holds the spans recorded from untrusted content.
type taintSpanStore struct {
	mu    sync.Mutex
	seq   uint64
	spans map[string]*taintSpan
	links map[string]uint64
	// scanned holds digests of messages already scanned; scannedLen and
	// scannedTail let an append-only history skip straight to new messages.
	scanned    map[[32]byte]struct{}
	scannedLen int
	scannedTip [32]byte
	principal  taintPrincipal
}

// taintPrincipal is what the principal's own messages mention.
type taintPrincipal struct {
	raw   string
	spans map[string]bool
}

func newTaintSpanStore() *taintSpanStore {
	return &taintSpanStore{
		spans:   make(map[string]*taintSpan),
		links:   make(map[string]uint64),
		scanned: make(map[[32]byte]struct{}),
	}
}

// taintEnv carries the paths span normalisation depends on.
type taintEnv struct {
	home string
	cwd  string
}

func currentTaintEnv() taintEnv {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	return taintEnv{home: path.Clean(home), cwd: path.Clean(cwd)}
}

// ── extraction ─────────────────────────────────────────────────────────

var (
	taintURLRe        = regexp.MustCompile("(?i)\\b(?:https?|wss?|s?ftp|ssh|git|file)://[^\\s\"'<>`\\\\{}|^]+")
	taintHostWordRe   = regexp.MustCompile(`(?i)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}[:/]`)
	taintBareHostRe   = regexp.MustCompile(`(?i)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}`)
	taintIPv4Re       = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}`)
	taintPathRe       = regexp.MustCompile(`(?:\$\{HOME\}|\$HOME|[~/])[A-Za-z0-9._~+@%-]*(?:/+[A-Za-z0-9._~+@%-]+)+`)
	taintTokenRe      = regexp.MustCompile(`[A-Za-z0-9+_=-]{32,}`)
	taintTrailingTrim = ".,;:!?)]}'\"*"
)

// taintCommonHosts are ubiquitous sites whose bare host is not recorded:
// almost every page mentions them, and a fetch from one is not by itself
// steered. URLs with a path on these hosts are still recorded.
var taintCommonHosts = map[string]bool{
	"github.com": true, "www.github.com": true, "gitlab.com": true, "bitbucket.org": true,
	"google.com": true, "www.google.com": true, "pypi.org": true, "npmjs.com": true,
	"www.npmjs.com": true, "registry.npmjs.org": true, "go.dev": true, "pkg.go.dev": true,
	"golang.org": true, "proxy.golang.org": true, "crates.io": true, "rubygems.org": true,
	"docker.io": true, "hub.docker.com": true, "stackoverflow.com": true,
	"wikipedia.org": true, "en.wikipedia.org": true, "developer.mozilla.org": true,
}

// taintMultiTenantSuffixes are hosting suffixes whose subdomains belong to
// unrelated tenants. The principal mentioning one never covers another
// tenant's subdomain.
var taintMultiTenantSuffixes = map[string]bool{
	"github.io": true, "gitlab.io": true, "githubusercontent.com": true,
	"amazonaws.com": true, "s3.amazonaws.com": true, "cloudfront.net": true,
	"vercel.app": true, "now.sh": true, "netlify.app": true, "netlify.com": true,
	"pages.dev": true, "workers.dev": true, "herokuapp.com": true, "fly.dev": true,
	"onrender.com": true, "railway.app": true, "glitch.me": true, "repl.co": true,
	"ngrok.app": true, "ngrok.io": true, "ngrok-free.app": true, "trycloudflare.com": true,
	"loca.lt": true, "web.app": true, "firebaseapp.com": true, "appspot.com": true,
	"azurewebsites.net": true, "blob.core.windows.net": true, "cloudfunctions.net": true,
	"run.app": true, "surge.sh": true, "pythonanywhere.com": true, "blogspot.com": true,
	"wordpress.com": true, "substack.com": true, "notion.site": true, "webflow.io": true,
	"azureedge.net": true, "r2.dev": true, "deno.dev": true, "replit.app": true,
	// Dynamic DNS, wildcard-IP, tunnel and request-capture services.
	"duckdns.org": true, "ddns.net": true, "hopto.org": true, "no-ip.org": true,
	"nip.io": true, "sslip.io": true, "ngrok-free.dev": true, "ngrok.dev": true,
	"serveo.net": true, "localtunnel.me": true, "ts.net": true, "pipedream.net": true,
	"interact.sh": true, "oast.fun": true, "oast.live": true, "oast.me": true,
	"oast.pro": true, "oast.site": true, "oast.online": true, "requestcatcher.com": true,
	"webhook.site": true,
	// Hosted app and site builders.
	"replit.dev": true, "github.dev": true, "hf.space": true, "streamlit.app": true,
	"supabase.co": true, "lovable.app": true, "neocities.org": true, "wixsite.com": true,
	"weebly.com": true, "azurestaticapps.net": true,
}

// taintFileExtensions are last labels that mark a dotted word as a file
// name rather than a host (main.go:12, README.md/…).
var taintFileExtensions = map[string]bool{
	"go": true, "js": true, "mjs": true, "cjs": true, "ts": true, "tsx": true, "jsx": true,
	"py": true, "pyc": true, "rb": true, "rs": true, "java": true, "kt": true, "kts": true,
	"cc": true, "cpp": true, "hpp": true, "cs": true, "swift": true, "mm": true, "php": true,
	"pl": true, "pm": true, "sh": true, "bash": true, "zsh": true, "fish": true, "ps1": true,
	"md": true, "markdown": true, "rst": true, "txt": true, "json": true, "jsonl": true,
	"yaml": true, "yml": true, "toml": true, "ini": true, "cfg": true, "conf": true,
	"env": true, "lock": true, "sum": true, "mod": true, "xml": true, "html": true, "htm": true,
	"css": true, "scss": true, "sass": true, "less": true, "svg": true, "png": true,
	"jpg": true, "jpeg": true, "gif": true, "webp": true, "ico": true, "pdf": true,
	"zip": true, "gz": true, "tgz": true, "bz2": true, "xz": true, "zst": true, "tar": true,
	"jar": true, "war": true, "so": true, "dylib": true, "dll": true, "exe": true, "bin": true,
	"log": true, "out": true, "err": true, "csv": true, "tsv": true, "sql": true, "db": true,
	"sqlite": true, "proto": true, "gradle": true, "vue": true, "svelte": true, "lua": true,
	"dart": true, "ex": true, "exs": true, "erl": true, "hs": true, "ml": true, "clj": true,
	"scala": true, "tf": true, "tfvars": true, "hcl": true, "nix": true, "pem": true,
	"crt": true, "key": true, "pub": true, "sig": true, "wasm": true, "map": true, "tmp": true,
	"bak": true, "orig": true, "patch": true, "diff": true, "test": true, "spec": true,
}

// taintBinDirs are standard executable directories: shebangs and install
// notes name their entries constantly, and writes into them already classify
// as system writes.
var taintBinDirs = map[string]bool{
	"/bin": true, "/sbin": true, "/usr/bin": true, "/usr/sbin": true,
	"/usr/local/bin": true, "/usr/local/sbin": true, "/opt/homebrew/bin": true,
}

// taintRaw is one extracted span. at/end locate it in the scanned text.
type taintRaw struct {
	kind    taintSpanKind
	value   string
	at, end int
	link    string // url only: the full normalised URL
	query   bool   // url only: carries a query string or userinfo
	urlHost string // url only: its host
}

func trimTaintTrailing(s string) string { return strings.TrimRight(s, taintTrailingTrim) }

// extractTaintURLs returns the URLs in text with their spans.
func extractTaintURLs(text string) []taintRaw {
	var out []taintRaw
	for _, loc := range taintURLRe.FindAllStringIndex(text, -1) {
		raw := trimTaintTrailing(text[loc[0]:loc[1]])
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		scheme := strings.ToLower(u.Scheme)
		// A fully-qualified name ("evil.example.") is the same host.
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		end := loc[0] + len(raw)
		if scheme == "file" {
			if p := u.Path; p != "" {
				out = append(out, taintRaw{kind: taintKindPath, value: p, at: loc[0], end: end})
			}
			continue
		}
		if host == "" {
			continue
		}
		hostPort := host
		if port := u.Port(); port != "" {
			hostPort = host + ":" + port
		}
		p := strings.TrimRight(u.EscapedPath(), "/")
		// The link keeps userinfo verbatim: a URL with userinfo is followed
		// as linked only when that exact userinfo appeared in the content.
		userinfo := ""
		if u.User != nil {
			userinfo = u.User.String() + "@"
		}
		link := scheme + "://" + userinfo + hostPort + p
		if u.RawQuery != "" {
			link += "?" + u.RawQuery
		}
		r := taintRaw{
			kind: taintKindURL, value: scheme + "://" + hostPort + p, at: loc[0], end: end,
			link: link, query: u.RawQuery != "" || u.User != nil, urlHost: host,
		}
		if p == "" {
			// A bare origin is recorded as its host only.
			r.value = ""
		}
		out = append(out, r)
	}
	return out
}

// extractTaintSpans returns every span in text, grouped by kind. Recording
// uses the strict host rule (a schemeless host must be followed by ":" or
// "/"); loose also takes bare dotted host words, which only the principal's
// mentions and the candidate side of a match use: a looser candidate costs
// nothing, since only recorded spans can match.
func extractTaintSpans(text string, env taintEnv, loose bool) []taintRaw {
	return extractTaintSpansFrom(text, env, loose, false)
}

// extractTaintSpansFrom is extractTaintSpans for text from a known source:
// goSource marks a read of Go sources or module files (see taintGoSource),
// the only text whose import specs and go.mod lines are not recorded as hosts.
func extractTaintSpansFrom(text string, env taintEnv, loose, goSource bool) []taintRaw {
	if len(text) > taintScanMaxBytes {
		text = text[:taintScanMaxBytes]
	}
	var out []taintRaw
	urls := extractTaintURLs(text)
	urlSpans := make([][2]int, len(urls))
	for i, u := range urls {
		urlSpans[i] = [2]int{u.at, u.end}
	}
	inURL := func(i int) bool { return inTaintIntervals(urlSpans, i) }
	for _, u := range urls {
		out = append(out, u)
		if u.kind == taintKindURL {
			out = append(out, taintRaw{kind: taintKindHost, value: u.urlHost, at: u.at, end: u.end})
		}
	}
	hostRe, trail, badPrev := taintHostWordRe, 1, "@/._-"
	if loose {
		hostRe, trail, badPrev = taintBareHostRe, 0, "/._-"
	}
	for _, loc := range hostRe.FindAllStringIndex(text, -1) {
		if inURL(loc[0]) || (loc[0] > 0 && strings.ContainsRune(badPrev, rune(text[loc[0]-1]))) || isTaintWordByte(prevByte(text, loc[0])) {
			continue
		}
		end := loc[1] - trail
		if goSource && !loose && taintGoModuleLine(text, loc[0]) {
			// A Go import spec or go.mod/go.sum entry names a dependency,
			// not a target: module-cache reads would otherwise record every
			// import host.
			continue
		}
		if loose && end < len(text) && (isTaintWordByte(text[end]) || text[end] == '-' || text[end] == '.' && end+1 < len(text) && isTaintWordByte(text[end+1])) {
			continue
		}
		host := strings.ToLower(text[loc[0]:end])
		labels := strings.Split(host, ".")
		if taintFileExtensions[labels[len(labels)-1]] {
			continue
		}
		out = append(out, taintRaw{kind: taintKindHost, value: host, at: loc[0], end: end})
	}
	for _, loc := range taintIPv4Re.FindAllStringIndex(text, -1) {
		if b := prevByte(text, loc[0]); b == '.' || isTaintWordByte(b) {
			continue
		}
		if loc[1] < len(text) && (text[loc[1]] == '.' && loc[1]+1 < len(text) && isDigit(text[loc[1]+1]) || isTaintWordByte(text[loc[1]])) {
			continue
		}
		ip := net.ParseIP(text[loc[0]:loc[1]])
		if ip == nil || ip.IsUnspecified() {
			continue
		}
		out = append(out, taintRaw{kind: taintKindHost, value: ip.String(), at: loc[0], end: loc[1]})
	}
	for _, loc := range taintPathRe.FindAllStringIndex(text, -1) {
		if b := prevByte(text, loc[0]); b != 0 && !strings.ContainsRune(" \t\r\n\"'`=([{,<>|;&", rune(b)) {
			continue
		}
		p, ok := normaliseTaintPath(trimTaintTrailing(text[loc[0]:loc[1]]), env)
		if !ok {
			continue
		}
		out = append(out, taintRaw{kind: taintKindPath, value: p, at: loc[0], end: loc[1]})
	}
	for _, loc := range taintTokenRe.FindAllStringIndex(text, -1) {
		tok := strings.TrimRight(text[loc[0]:loc[1]], "=")
		if !opaqueTaintToken(tok) {
			continue
		}
		out = append(out, taintRaw{kind: taintKindToken, value: tok, at: loc[0], end: loc[1]})
	}
	return out
}

var (
	// taintGoImportRe matches a Go import spec line: `import "p"`, or an
	// entry of an import block, optionally named or commented.
	taintGoImportRe = regexp.MustCompile(`^\s*(?:import\s+)?(?:[A-Za-z_.][A-Za-z0-9_]*\s+)?"[^"\s]+"\s*(?://.*)?$`)
	// taintGoModRe matches a go.mod requirement or a go.sum line: a module
	// path followed by a version.
	taintGoModRe = regexp.MustCompile(`^\s*(?:(?:require|replace|exclude|retract)\s+)?[^\s"]+\s+v[0-9]`)
)

// taintGoModuleLineMax bounds the line taintGoModuleLine examines, so the
// check stays constant-time per host word; real import lines are short.
const taintGoModuleLineMax = 512

// taintGoModuleLine reports whether the host word at i begins the module
// path of a Go import spec or a go.mod/go.sum entry on its line.
func taintGoModuleLine(text string, i int) bool {
	start := i
	for start > 0 && text[start-1] != '\n' {
		start--
		if i-start > taintGoModuleLineMax {
			return false
		}
	}
	end := i
	for end < len(text) && text[end] != '\n' {
		end++
		if end-i > taintGoModuleLineMax {
			return false
		}
	}
	line := text[start:end]
	at := i - start
	if at > 0 && line[at-1] == '"' {
		return taintGoImportRe.MatchString(line)
	}
	switch strings.TrimSpace(line[:at]) {
	case "", "require", "replace", "exclude", "retract":
		return taintGoModRe.MatchString(line)
	}
	return false
}

// inTaintIntervals reports whether i lies in one of spans, which are sorted
// by start and do not overlap (regexp matches in order of appearance).
func inTaintIntervals(spans [][2]int, i int) bool {
	k := sort.Search(len(spans), func(k int) bool { return spans[k][1] > i })
	return k < len(spans) && spans[k][0] <= i
}

func prevByte(s string, i int) byte {
	if i <= 0 {
		return 0
	}
	return s[i-1]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isTaintWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || isDigit(b) || b == '_'
}

// normaliseTaintPath expands ~/, $HOME/ and ${HOME}/, cleans p (doubled
// slashes, "." and ".." segments) and applies the path exclusions.
func normaliseTaintPath(p string, env taintEnv) (string, bool) {
	for _, prefix := range []string{"~", "${HOME}", "$HOME"} {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		if !strings.HasPrefix(p[len(prefix):], "/") || env.home == "" || env.home == "." {
			return "", false
		}
		p = env.home + p[len(prefix):]
		break
	}
	p = path.Clean(p)
	if strings.Count(p, "/") < 2 {
		return "", false
	}
	switch p {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return "", false
	}
	if taintBinDirs[path.Dir(p)] {
		return "", false
	}
	under := func(root string) bool {
		return root != "" && root != "." && root != "/" && (p == root || strings.HasPrefix(p, root+"/"))
	}
	if under(env.cwd) || under("/workspace") {
		return "", false
	}
	if env.home != "" && (p == env.home || strings.HasPrefix(env.home, p+"/")) {
		return "", false
	}
	return p, true
}

// opaqueTaintToken reports whether tok looks like a key, payload or encoded
// blob rather than an identifier or hash.
func opaqueTaintToken(tok string) bool {
	if len(tok) < taintTokenMinLen || len(tok) > taintTokenMaxLen {
		return false
	}
	var upper, lower, digit, nonHex bool
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case isDigit(c):
			digit = true
		}
		if !isHexOrDash(c) {
			nonHex = true
		}
	}
	return upper && lower && digit && nonHex
}

func isHexOrDash(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-'
}

// ── recording ──────────────────────────────────────────────────────────

func taintMessageDigest(m *session.Message) [32]byte {
	// SHA-256 over length-prefixed fields: no two distinct messages share a
	// digest, so a new message is never skipped as already scanned.
	h := sha256.New()
	for _, f := range []string{m.Role, m.ToolCallID, m.Name, m.Content} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// untrustedBody is one wrapper in a message.
type untrustedBody struct {
	source string
	body   string
	start  int
	end    int
}

// untrustedBodies returns every real untrusted-content wrapper in s.
func untrustedBodies(s string) []untrustedBody {
	var out []untrustedBody
	const attr = ` source="`
	off := 0
	for {
		i := strings.Index(s[off:], session.UntrustedWrapperPrefix)
		if i < 0 {
			return out
		}
		start := off + i
		rest := s[start+len(session.UntrustedWrapperPrefix):]
		n := 0
		for n < len(rest) && (isDigit(rest[n]) || rest[n] >= 'a' && rest[n] <= 'f') {
			n++
		}
		if n == 0 || !strings.HasPrefix(rest[n:], attr) {
			off = start + len(session.UntrustedWrapperPrefix)
			continue
		}
		nonce := rest[:n]
		label := rest[n+len(attr):]
		q := strings.Index(label, `">`)
		if q < 0 {
			out = append(out, untrustedBody{source: "", body: label, start: start, end: len(s)})
			return out
		}
		bodyStart := start + len(session.UntrustedWrapperPrefix) + n + len(attr) + q + 2
		closeTag := "</untrusted_content_" + nonce + ">"
		c := strings.Index(s[bodyStart:], closeTag)
		end := len(s)
		body := s[bodyStart:]
		if c >= 0 {
			body = s[bodyStart : bodyStart+c]
			end = bodyStart + c + len(closeTag)
		}
		out = append(out, untrustedBody{source: label[:q], body: body, start: start, end: end})
		off = end
		if off >= len(s) {
			return out
		}
	}
}

// stripUntrustedBodies removes every wrapper from s, leaving the text the
// principal wrote.
func stripUntrustedBodies(s string) string {
	bodies := untrustedBodies(s)
	if len(bodies) == 0 {
		return s
	}
	var b strings.Builder
	prev := 0
	for _, w := range bodies {
		b.WriteString(s[prev:w.start])
		b.WriteByte('\n')
		prev = w.end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// bgReadTool reports tools that surface background-job output; they inherit the taint of the
// bg_start calls in the history.
func bgReadTool(name string) bool { return name == "bg_output" || name == "bg_status" }

// taintGoSource reports whether a tool call reads only Go sources or module
// files: every path argument names a .go, go.mod or go.sum file, or lies in
// the Go module cache (a pkg/mod directory). Only such reads skip import
// specs and go.mod lines when recording hosts.
func taintGoSource(tc *session.ToolCall) bool {
	var a struct {
		Path  string `json:"path"`
		PathA string `json:"path_a"`
		PathB string `json:"path_b"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &a) != nil {
		return false
	}
	paths := []string{a.Path, a.PathA, a.PathB}
	for _, f := range a.Files {
		paths = append(paths, f.Path)
	}
	seen := false
	for _, p := range paths {
		if p == "" {
			continue
		}
		seen = true
		clean := path.Clean(p)
		base := path.Base(clean)
		if !strings.HasSuffix(base, ".go") && base != "go.mod" && base != "go.sum" && !strings.Contains(clean+"/", "/pkg/mod/") {
			return false
		}
	}
	return seen && session.EpisodePathTools[tc.Function.Name]
}

// taintingToolCall reports whether a recorded call's result is a span
// source: the episode rule says it crossed the trust boundary, or it reads
// output of a background job that did.
func taintingToolCall(tc *session.ToolCall, bgTaints bool) bool {
	return session.ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) ||
		bgTaints && bgReadTool(tc.Function.Name)
}

// observe records spans from the messages not yet scanned and refreshes the
// principal's mentions.
func (s *taintSpanStore) observe(messages []session.Message, env taintEnv) {
	calls := make(map[string]*session.ToolCall)
	bgTaints := false
	var principal strings.Builder
	for i := range messages {
		m := &messages[i]
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			calls[tc.ID] = tc
			if tc.Function.Name == "bg_start" && session.ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) {
				bgTaints = true
			}
		}
		if m.Role == "user" && !session.IsSyntheticUserName(m.Name) && principal.Len() < taintPrincipalMaxBytes {
			principal.WriteString(stripUntrustedBodies(m.Content))
			principal.WriteByte('\n')
		}
	}

	// Extraction runs outside the mutex: only the scan bookkeeping and the
	// span insertions hold it.
	principalText := principal.String()
	s.mu.Lock()
	needPrincipal := principalText != s.principal.raw || s.principal.spans == nil
	s.mu.Unlock()
	var principalSpans map[string]bool
	if needPrincipal {
		principalSpans = taintPrincipalSpans(principalText, env)
	}

	type job struct {
		text, source string
		goSource     bool
	}
	var jobs []job
	s.mu.Lock()
	start := 0
	if s.scannedLen > 0 && s.scannedLen <= len(messages) && taintMessageDigest(&messages[s.scannedLen-1]) == s.scannedTip {
		start = s.scannedLen
	}
	for i := start; i < len(messages); i++ {
		m := &messages[i]
		d := taintMessageDigest(m)
		if _, ok := s.scanned[d]; ok {
			continue
		}
		if len(s.scanned) >= maxTaintScanned {
			s.scanned = make(map[[32]byte]struct{})
		}
		s.scanned[d] = struct{}{}
		switch m.Role {
		case "assistant":
			continue
		case "tool":
			tc := calls[m.ToolCallID]
			if tc != nil && !taintingToolCall(tc, bgTaints) {
				continue
			}
			source := "tool output"
			if tc != nil {
				source = "tool:" + tc.Function.Name
			}
			for _, w := range untrustedBodies(m.Content) {
				if w.source != "" && !session.EngineDerivedSource(w.source) {
					source = w.source
					break
				}
			}
			jobs = append(jobs, job{m.Content, source, tc != nil && taintGoSource(tc)})
		default:
			for _, w := range untrustedBodies(m.Content) {
				if session.ExternalNonToolIngest(w.source) || (w.source == "bg" && bgTaints) {
					jobs = append(jobs, job{w.body, w.source, false})
				}
			}
		}
	}
	if n := len(messages); n > 0 {
		s.scannedLen = n
		s.scannedTip = taintMessageDigest(&messages[n-1])
	}
	s.mu.Unlock()

	extracted := make([][]taintRaw, len(jobs))
	for i, j := range jobs {
		extracted[i] = extractTaintSpansFrom(j.text, env, false, j.goSource)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if needPrincipal {
		s.principal = taintPrincipal{raw: principalText, spans: principalSpans}
	}
	for i, j := range jobs {
		s.insertLocked(extracted[i], j.source)
	}
}

// taintPrincipalSpans returns the span keys the principal's text mentions.
func taintPrincipalSpans(text string, env taintEnv) map[string]bool {
	spans := make(map[string]bool)
	for _, r := range extractTaintSpans(text, env, true) {
		if r.value != "" {
			spans[taintKey(r.kind, r.value)] = true
		}
		if r.kind == taintKindURL {
			spans[taintKey(taintKindHost, r.urlHost)] = true
		}
	}
	return spans
}

// insertLocked records extracted spans under source.
func (s *taintSpanStore) insertLocked(spans []taintRaw, source string) {
	for _, r := range spans {
		if r.kind == taintKindURL {
			if len(s.links) >= maxTaintLinks {
				s.evictLinksLocked()
			}
			s.seq++
			s.links[r.link] = s.seq
			if r.value == "" {
				continue
			}
		}
		if r.kind == taintKindHost && taintCommonHosts[r.value] {
			continue
		}
		k := taintKey(r.kind, r.value)
		s.seq++
		if sp, ok := s.spans[k]; ok {
			sp.seq = s.seq
			continue
		}
		if len(s.spans) >= maxTaintSpans {
			s.evictSpansLocked()
		}
		s.spans[k] = &taintSpan{source: source, seq: s.seq}
	}
}

// eighthCutoff returns the last-seen sequence number at or below which the
// least recently seen eighth of seqs lies. Sequence numbers are unique.
func eighthCutoff(seqs []uint64) uint64 {
	slices.Sort(seqs)
	n := len(seqs) / 8
	if n == 0 {
		n = 1
	}
	return seqs[n-1]
}

// evictSpansLocked drops the least recently seen eighth of the spans.
func (s *taintSpanStore) evictSpansLocked() {
	seqs := make([]uint64, 0, len(s.spans))
	for _, sp := range s.spans {
		seqs = append(seqs, sp.seq)
	}
	cutoff := eighthCutoff(seqs)
	for k, sp := range s.spans {
		if sp.seq <= cutoff {
			delete(s.spans, k)
		}
	}
}

// evictLinksLocked drops the least recently seen eighth of the links.
func (s *taintSpanStore) evictLinksLocked() {
	seqs := make([]uint64, 0, len(s.links))
	for _, seq := range s.links {
		seqs = append(seqs, seq)
	}
	cutoff := eighthCutoff(seqs)
	for k, seq := range s.links {
		if seq <= cutoff {
			delete(s.links, k)
		}
	}
}

// ── matching ───────────────────────────────────────────────────────────

// taintHit is a reused span.
type taintHit struct {
	kind   taintSpanKind
	span   string
	source string
}

// principalLocked reports whether the principal's own messages mention r.
func (s *taintSpanStore) principalLocked(kind taintSpanKind, v string) bool {
	p := s.principal.spans
	switch kind {
	case taintKindHost:
		if p[taintKey(taintKindHost, v)] {
			return true
		}
		// A mentioned parent domain covers its subdomains, unless it is a
		// multi-tenant hosting suffix: there every subdomain is a different
		// owner, and only the exact mentioned host is covered.
		for h := v; strings.Count(h, ".") >= 1; {
			h = h[strings.IndexByte(h, '.')+1:]
			if p[taintKey(taintKindHost, h)] && !taintMultiTenantSuffixes[h] {
				return true
			}
		}
		return false
	case taintKindURL:
		// A mentioned host covers every URL on it, except on the shared
		// hosting sites in taintCommonHosts, where one user's repository says
		// nothing about another's: there only the mentioned URL and the URLs
		// under it are covered.
		if u, err := url.Parse(v); err == nil {
			host := strings.ToLower(u.Hostname())
			if !taintCommonHosts[host] && s.principalLocked(taintKindHost, host) {
				return true
			}
		}
		for q := v; strings.Count(q, "/") > 2; q = q[:strings.LastIndexByte(q, '/')] {
			if p[taintKey(taintKindURL, q)] {
				return true
			}
		}
		return false
	case taintKindPath:
		for q := v; q != "/" && q != "."; q = path.Dir(q) {
			if p[taintKey(taintKindPath, q)] {
				return true
			}
		}
		return false
	default:
		return p[taintKey(kind, v)] || strings.Contains(s.principal.raw, v)
	}
}

// lookupLocked finds a recorded span covering candidate r.
func (s *taintSpanStore) lookupLocked(r taintRaw) (taintHit, bool) {
	try := func(kind taintSpanKind, v string) (taintHit, bool) {
		sp, ok := s.spans[taintKey(kind, v)]
		if !ok || s.principalLocked(kind, v) {
			return taintHit{}, false
		}
		return taintHit{kind: kind, span: v, source: sp.source}, true
	}
	switch r.kind {
	case taintKindHost:
		for h := r.value; ; {
			if hit, ok := try(taintKindHost, h); ok {
				return hit, true
			}
			dot := strings.IndexByte(h, '.')
			if dot < 0 || strings.Count(h, ".") < 2 || net.ParseIP(r.value) != nil {
				return taintHit{}, false
			}
			h = h[dot+1:]
		}
	case taintKindURL:
		if r.value == "" {
			return taintHit{}, false
		}
		origin := r.value
		if i := strings.Index(origin, "://"); i >= 0 {
			if j := strings.IndexByte(origin[i+3:], '/'); j >= 0 {
				origin = origin[:i+3+j]
			}
		}
		for v := r.value; len(v) > len(origin); v = v[:strings.LastIndexByte(v, '/')] {
			if hit, ok := try(taintKindURL, v); ok {
				return hit, true
			}
		}
		return taintHit{}, false
	case taintKindPath:
		for q := r.value; strings.Count(q, "/") >= 2; q = path.Dir(q) {
			if hit, ok := try(taintKindPath, q); ok {
				return hit, true
			}
		}
		return taintHit{}, false
	default:
		return try(r.kind, r.value)
	}
}

// taintKinds selects which span kinds a match considers.
type taintKinds uint8

const (
	taintMatchURL taintKinds = 1 << iota
	taintMatchHost
	taintMatchPath
	taintMatchToken
	taintMatchAll = taintMatchURL | taintMatchHost | taintMatchPath | taintMatchToken
)

func (k taintKinds) has(kind taintSpanKind) bool {
	switch kind {
	case taintKindURL:
		return k&taintMatchURL != 0
	case taintKindHost:
		return k&taintMatchHost != 0
	case taintKindPath:
		return k&taintMatchPath != 0
	default:
		return k&taintMatchToken != 0
	}
}

// match returns the first recorded span text reuses. With egress set, the
// egress rule applies to the URLs in text.
func (s *taintSpanStore) match(text string, kinds taintKinds, egress bool, env taintEnv) (taintHit, bool) {
	if len(text) > taintMatchMaxBytes {
		text = text[:taintMatchMaxBytes]
	}
	cands := extractTaintSpans(text, env, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.spans) == 0 {
		return taintHit{}, false
	}
	// URLs the content linked verbatim, and path-only URLs, are browsing
	// under the egress rule: spans inside them do not count.
	var exempt [][2]int
	if egress {
		for _, c := range cands {
			if c.kind != taintKindURL {
				continue
			}
			if _, linked := s.links[c.link]; linked || !c.query {
				exempt = append(exempt, [2]int{c.at, c.end})
			}
		}
	}
	isExempt := func(c taintRaw) bool { return inTaintIntervals(exempt, c.at) }
	for _, c := range cands {
		if !kinds.has(c.kind) || isExempt(c) {
			continue
		}
		if hit, ok := s.lookupLocked(c); ok {
			return hit, true
		}
	}
	return taintHit{}, false
}

// ── engine gate ────────────────────────────────────────────────────────

// taintPassiveHeaders are the request headers that keep an http_request a
// plain fetch for the egress rule; any other header can carry data, and a
// passive one does too when its value is data-shaped (taintDataShaped).
var taintPassiveHeaders = map[string]bool{"accept": true, "accept-language": true, "user-agent": true}

// taintDataShaped reports whether a header value carries an opaque token
// (the token-span shape) or a secret the redactor recognises.
func taintDataShaped(v string) bool {
	if len(v) > taintMatchMaxBytes {
		return true
	}
	for _, loc := range taintTokenRe.FindAllStringIndex(v, -1) {
		if opaqueTaintToken(strings.TrimRight(v[loc[0]:loc[1]], "=")) {
			return true
		}
	}
	return redact.RedactSecrets(v) != v
}

// taintEscalationFor returns the span a tool call reuses, if the call must
// be escalated, and the resource to show the principal.
func (e *Engine) taintEscalationFor(ctx context.Context, store *taintSpanStore, name, args string, env taintEnv) (taintHit, string, bool) {
	switch name {
	case "shell", "terminal", "bg_start":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(args), &p) != nil || strings.TrimSpace(p.Command) == "" {
			return taintHit{}, "", false
		}
		cls, _ := danger.ClassifyScriptGateCtx(ctx, p.Command)
		if cls == danger.Safe {
			return taintHit{}, "", false
		}
		// The egress rule judges URL text only, so it applies only when no
		// stage sends request data, headers or query options beside it.
		egress := cls == danger.NetworkEgress && !danger.Analyze(p.Command).RequestData
		hit, ok := store.match(p.Command, taintMatchAll, egress, env)
		return hit, p.Command, ok
	case "write_file", "patch":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &p) != nil || p.Path == "" {
			return taintHit{}, "", false
		}
		target := p.Path
		if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "~") {
			return taintHit{}, "", false
		}
		hit, ok := store.match(target, taintMatchPath, false, env)
		return hit, p.Path, ok
	case "http_request":
		var p struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal([]byte(args), &p) != nil {
			hit, ok := store.match(args, taintMatchAll, false, env)
			return hit, args, ok
		}
		cls := worstRisk(danger.HTTPRequestClass(p.Method, p.Headers), danger.ClassifyURL(p.URL))
		egress := cls == danger.NetworkEgress
		text := p.URL
		for name, v := range p.Headers {
			text += "\n" + v
			if !taintPassiveHeaders[strings.ToLower(strings.TrimSpace(name))] || taintDataShaped(v) {
				egress = false
			}
		}
		hit, ok := store.match(text, taintMatchAll, egress, env)
		return hit, p.URL, ok
	case "browser":
		var p struct {
			Action string `json:"action"`
			URL    string `json:"url"`
		}
		if json.Unmarshal([]byte(args), &p) != nil || p.Action != "navigate" || p.URL == "" {
			return taintHit{}, "", false
		}
		cls := worstRisk(danger.NetworkEgress, danger.ClassifyURL(p.URL))
		hit, ok := store.match(p.URL, taintMatchAll, cls == danger.NetworkEgress, env)
		return hit, p.URL, ok
	}
	if strings.Contains(name, "__") {
		// An MCP tool is third-party code with any effect: every string
		// argument takes the full match.
		hit, ok := store.match(taintArgumentStrings(args), taintMatchAll, false, env)
		return hit, name + " " + args, ok
	}
	return taintHit{}, "", false
}

// taintArgumentStrings returns every string (and object key) in a JSON
// argument object, one per line and bounded by taintMatchMaxBytes. Arguments
// that do not parse are returned as they are.
func taintArgumentStrings(args string) string {
	var v any
	if json.Unmarshal([]byte(args), &v) != nil {
		return args
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		if b.Len() >= taintMatchMaxBytes {
			return
		}
		switch t := v.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				walk(k)
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}

// spanStore returns the engine's untrusted-span store, creating it.
func (e *Engine) spanStore() *taintSpanStore {
	e.taintMu.Lock()
	defer e.taintMu.Unlock()
	if e.taintSpans == nil {
		e.taintSpans = newTaintSpanStore()
	}
	return e.taintSpans
}

func boundTaintDisplay(s string) string {
	if len(s) <= taintDisplayMax {
		return s
	}
	cut := taintDisplayMax
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// taintGate escalates every call in the batch whose arguments reuse an
// untrusted span. It returns, per call, the denial reason ("" when the call
// may proceed to the ordinary gates). Only the first admitted calls — those
// the tool-call budget lets run — are examined.
func (e *Engine) taintGate(ctx context.Context, messages []session.Message, calls []session.ToolCall, admitted int, callIDs []string, iterNum int) []string {
	if e.dangerousCfg != nil && !e.dangerousCfg.TaintEscalationEnabled() {
		return nil
	}
	env := currentTaintEnv()
	store := e.spanStore()
	store.observe(messages, env)
	var denied []string
	for i, tc := range calls {
		if i >= admitted {
			// The tool-call budget skips this call: it never runs.
			break
		}
		hit, resource, ok := e.taintEscalationFor(ctx, store, tc.Function.Name, tc.Function.Arguments, env)
		if !ok {
			continue
		}
		span := boundTaintDisplay(hit.span)
		source := boundTaintDisplay(hit.source)
		description := fmt.Sprintf("argument derived from untrusted content: %s %q (first seen in %s)",
			hit.kind, danger.SanitizeInline(span), danger.SanitizeInline(source))
		approver := e.approver
		if approver == nil {
			tty := danger.NewTTYApprover(e.dangerousCfg)
			tty.Ctx = ctx
			approver = tty
		}
		err := approver.PromptCommand(danger.UntrustedArgsClass, resource, description)
		approved := err == nil
		decision := "approved"
		if !approved {
			decision = "denied"
			if denied == nil {
				denied = make([]string, len(calls))
			}
			denied[i] = fmt.Sprintf("error: denied — an argument was derived from untrusted content (%s %q, first seen in %s); the principal did not approve it",
				hit.kind, danger.SanitizeInline(span), danger.SanitizeInline(source))
		}
		e.emitSignal(SignalEvent{Type: "taint_escalation", Tool: tc.Function.Name,
			Detail: fmt.Sprintf("%s: %s %q (first seen in %s)", decision, hit.kind, danger.SanitizeInline(span), danger.SanitizeInline(source))})
		callID := ""
		if i < len(callIDs) {
			callID = callIDs[i]
		}
		e.emitEvent(events.Event{Type: events.TypeTaintEscalated, Iteration: iterNum, Tool: tc.Function.Name, Data: map[string]any{
			"call_id":     callID,
			"span_kind":   string(hit.kind),
			"span_sha256": events.ArgsDigest(hit.span),
			"span_bytes":  len(hit.span),
			"approved":    approved,
		}})
		if fn := TaintEscalationRecorderFrom(ctx); fn != nil {
			fn(TaintEscalation{Tool: tc.Function.Name, Kind: string(hit.kind), Span: hit.span, Source: hit.source, Resource: resource, Approved: approved})
		}
	}
	return denied
}
