package loop

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
//   - MCP tools (<server>__<tool>) and delegate_tasks: any span in any string
//     argument or key. In a child, a task fenced as <untrusted_input_<nonce>>
//     is untrusted content (recorded, never principal).
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
// so it survives context trimming within one engine. Evicting a span must
// not forget it: content that names an endpoint and then floods the store
// with filler would otherwise push the endpoint out of the gate. Every span
// the store evicts, and every span a persisted bound drops, goes into a
// fixed-size Bloom filter (session.TaintOverflow) that a lookup missing the
// held spans consults. A filter hit escalates like a held span (the
// principal's mentions still exempt it), with a source saying the content is
// no longer held; a false positive only adds an escalation. Spans and filter
// are exported as sticky session state and seed rebuilt engines
// (taint_persist.go), so a span recorded once keeps escalating for the life
// of the session. It is a heuristic: a span the model
// transforms (re-encodes, splits, retypes) is not recognised, and data
// encoded into the path of a plain GET is not escalated.

const (
	maxTaintSpans          = 4096
	maxTaintLinks          = 4096
	maxTaintScanned        = 16384
	taintScanMaxBytes      = 1 << 20
	taintPrincipalMaxBytes = 256 << 10
	taintMatchMaxBytes     = 64 << 10
	// taintMatchCeiling is the largest candidate matched in full; above it
	// any recorded span escalates the call fail-closed.
	taintMatchCeiling = 4 << 20
	taintTokenMinLen  = 32
	taintTokenMaxLen  = 512
	taintDisplayMax   = 200
)

type taintSpanKind string

const (
	taintKindURL   taintSpanKind = "url"
	taintKindHost  taintSpanKind = "host"
	taintKindPath  taintSpanKind = "path"
	taintKindToken taintSpanKind = "token"
	// taintKindOversize marks a candidate too large to match in full.
	taintKindOversize taintSpanKind = "oversize"
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
	// overflowed marks a span already added to the store's overflow filter
	// (an export cut it from the persisted list; see TaintSpanState).
	overflowed bool
	// persisted caches the span's exported form (value or hash, redacted
	// source) as computed under redaction registry generation persistedGen;
	// see taintSpan.persistedLocked.
	persisted    session.TaintSpan
	persistedGen uint64
	persistedSet bool
	// hash caches the hex SHA-256 of a plain span's value (hashOf), so a
	// span is hashed at most once however often the store looks it up by
	// its persisted hash.
	hash string
}

// hashOf returns the hex SHA-256 of value, the plain span's value,
// computing it once.
func (sp *taintSpan) hashOf(value string) string {
	if sp.hash == "" {
		sp.hash = taintHash(value)
	}
	return sp.hash
}

// taintOverflowSource labels an escalation found only in the overflow
// filter: the span was recorded from untrusted content this session no
// longer holds.
const taintOverflowSource = "earlier untrusted content (no longer held)"

// taintOverflowFullSource replaces taintOverflowSource once the filter is
// nearly full (session.TaintOverflowNearlyFull): the hit may be a false
// match, and the filter is never cleared within a session, so the principal
// is told how to reset it.
const taintOverflowFullSource = "earlier untrusted content (no longer held; this session's untrusted-span record is nearly full, so this may be a false match: a new session resets it)"

// taintSpan keys are kind + "\x00" + normalised value.
func taintKey(kind taintSpanKind, v string) string { return string(kind) + "\x00" + v }

// appendTaintKey appends taintKey(kind, v) to b, so a lookup can index a map
// with string(b) without allocating.
func appendTaintKey(b []byte, kind taintSpanKind, v string) []byte {
	return append(append(append(b, kind...), 0), v...)
}

// taintSeqBase is where sequence numbers start. Recording counts up from it
// and seeding counts down (taintSpanStore.low), so every seeded span is
// older than every span this engine recorded itself.
const taintSeqBase = 1 << 62

// taintSpanStore holds the spans recorded from untrusted content.
type taintSpanStore struct {
	mu  sync.Mutex
	seq uint64
	// low is the sequence number of the oldest seeded span; the next span
	// seeded gets low-1.
	low   uint64
	spans map[string]*taintSpan
	// hashed holds persisted spans known only by their SHA-256 (tokens and
	// secret-shaped values; see taint_persist.go), keyed taintKey(kind, hash).
	hashed map[string]*taintSpan
	// plainByHash maps taintKey(kind, hash) to the key in spans of the
	// plain span with that hash. It is built the first time a persisted hash
	// meets held plain spans and kept current from then on (recording and
	// evicting plain spans update it), so a re-seed never re-hashes them.
	plainByHash map[string]string
	// kbuf is scratch space for map lookups by a key built in place.
	kbuf []byte
	// overflow records every span evicted from spans or hashed, and every
	// span an export cut from the persisted list (session.TaintOverflow).
	// It is never cleared. overflowFill caches its Fill while
	// overflowFillKnown is set; every change to the filter clears it.
	overflow          session.TaintOverflow
	overflowFill      float64
	overflowFillKnown bool
	// seedLen, seedHead and seedOverflow identify the persisted state last
	// seeded, so a run does not re-seed it every iteration.
	seedLen      int
	seedHead     *session.TaintSpan
	seedOverflow *byte
	// version changes with every change to what TaintSpanState exports: a
	// span recorded, seen again (its order changes), seeded or evicted, or
	// the overflow filter merged with a seed. export memoizes the last
	// export against it (taint_persist.go).
	version uint64
	export  *taintExport
	// links are evicted without a record: losing one only removes the
	// egress rule's exemption for that URL, which fails closed.
	links map[string]uint64
	// scanned holds digests of messages already scanned. gateTrack and
	// saveTrack let a history that extends the one last observed by the
	// same caller skip straight to its new messages.
	scanned   map[taintDigest]struct{}
	gateTrack taintScanTrack
	saveTrack taintScanTrack
	principal taintPrincipal
	// env is the environment the scan decisions were taken under; a change
	// rescans everything (resetScanLocked).
	env    taintEnv
	envSet bool
	// seedCov is the coverage of the persisted state last seeded
	// (taint_coverage.go). Until a history reproduces it (seedCovApplied),
	// the first observes try to verify it; covered then holds the digests
	// of the messages it vouches for, whose spans are not extracted again
	// (only their links are).
	seedCov        session.TaintCoverage
	seedCovApplied bool
	seedCovTries   int
	covered        map[taintDigest]struct{}
	// covMu guards cov, the coverage this store can vouch for.
	covMu sync.Mutex
	cov   taintCoverageTracker
	// bgTainted records that this session started a background job whose
	// bg_start call crosses the trust boundary (session.ToolCallTaintsEpisode):
	// from then on every background notice and bg_output/bg_status result is
	// a span source. It is sticky and exported with the spans
	// (session.TaintState.Background), because trimming can drop the
	// bg_start call but never the fact.
	bgTainted bool
	// bgJobs maps the id of every background job this store saw started
	// (the bg_start call and its result, in one observed history) to whether
	// that call taints. Background content is judged by the jobs it names
	// even after trimming dropped the calls; content naming a job missing
	// here, or naming none, is a span source (see bgSourceLocked).
	bgJobs map[string]bool
}

// maxTaintBgJobs bounds bgJobs. A full map is emptied: forgetting a job only
// makes its content a span source.
const maxTaintBgJobs = 4096

// taintScanTrack identifies the history one caller observed last: its length
// and a chain digest over every message digest in it. A later history whose
// first n messages reproduce the chain starts scanning at n; anything else
// is checked message by message against the scanned set.
type taintScanTrack struct {
	n     int
	chain [2]uint64
}

func taintDigestChain(d []taintDigest) [2]uint64 {
	c := newTaintChain()
	for _, x := range d {
		c.add(x)
	}
	return c.sum()
}

// maxTaintSeedCoverageTries bounds the attempts to verify a seeded coverage:
// the first observes are a run's first gate (on a possibly trimmed context)
// and its first save (on the full history).
const maxTaintSeedCoverageTries = 2

// resetScanLocked forgets every scan decision (not the spans): the next
// observe judges every message again.
func (s *taintSpanStore) resetScanLocked() {
	s.scanned = make(map[taintDigest]struct{})
	s.gateTrack, s.saveTrack = taintScanTrack{}, taintScanTrack{}
	s.covered = nil
	s.seedCovApplied, s.seedCovTries = false, 0
	s.covMu.Lock()
	s.cov = taintCoverageTracker{}
	s.covMu.Unlock()
}

// taintPrincipal is what the principal's own messages mention.
type taintPrincipal struct {
	raw   string
	spans map[string]bool
}

func newTaintSpanStore() *taintSpanStore {
	return &taintSpanStore{
		seq:     taintSeqBase,
		low:     taintSeqBase,
		spans:   make(map[string]*taintSpan),
		hashed:  make(map[string]*taintSpan),
		links:   make(map[string]uint64),
		scanned: make(map[taintDigest]struct{}),
		bgJobs:  make(map[string]bool),
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

// taintRecordWindows returns the parts of recorded content that recording
// scans: text over taintScanMaxBytes is scanned in its first
// taintScanMaxBytes (head) and its last taintScanMaxBytes/4 (tail, from a
// word boundary), so padding cannot push a trailing span out of view; only
// the middle of such a message goes unscanned. Shorter text is all head.
func taintRecordWindows(text string) (head, tail string) {
	if len(text) <= taintScanMaxBytes {
		return text, ""
	}
	tail = text[len(text)-taintScanMaxBytes/4:]
	if i := strings.IndexAny(tail, " \t\r\n"); i >= 0 {
		tail = tail[i:] // start at a word boundary
	}
	return text[:taintScanMaxBytes], tail
}

// extractTaintSpansForRecord extracts the spans of recorded content, through
// taintRecordWindows.
func extractTaintSpansForRecord(text string, env taintEnv, goSource bool) []taintRaw {
	head, tail := taintRecordWindows(text)
	out := extractTaintSpansFrom(head, env, false, goSource)
	if tail != "" {
		out = append(out, extractTaintSpansFrom(tail, env, false, goSource)...)
	}
	return out
}

// extractTaintLinksForRecord extracts only the URLs of recorded content,
// through the same windows and in the same order as
// extractTaintSpansForRecord lists them: the links a full extraction would
// record, for a source whose spans a verified coverage already vouches for.
func extractTaintLinksForRecord(text string) []taintRaw {
	head, tail := taintRecordWindows(text)
	out := extractTaintURLs(head)
	if tail != "" {
		out = append(out, extractTaintURLs(tail)...)
	}
	return out
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

// taintDigest identifies a scanned message within this process.
type taintDigest [16]byte

// taintDigestSeeds key the message digest per process, so content cannot be
// crafted to collide with a message already scanned.
var taintDigestSeeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}

// taintMessageDigest digests everything the recording decision for m reads:
// the message, its tool calls, and for a tool result the call it answers
// (call, nil when the history does not hold it). Two messages share a digest
// only when they would be judged alike, so skipping a message whose digest
// was scanned never skips a decision that could come out differently.
func taintMessageDigest(m *session.Message, call *session.ToolCall) taintDigest {
	// Two independently seeded 64-bit hashes over length-prefixed fields:
	// fields cannot run into each other, and a collision needs the seeds.
	var out taintDigest
	for i, seed := range taintDigestSeeds {
		var h maphash.Hash
		h.SetSeed(seed)
		field := func(f string) {
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], uint64(len(f)))
			h.Write(n[:])
			h.WriteString(f)
		}
		field(m.Role)
		field(m.ToolCallID)
		field(m.Name)
		field(m.Content)
		field(strconv.Itoa(len(m.ToolCalls)))
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			field(tc.ID)
			field(tc.Function.Name)
			field(tc.Function.Arguments)
		}
		if call != nil {
			field("call")
			field(call.Function.Name)
			field(call.Function.Arguments)
		} else {
			field("")
		}
		binary.BigEndian.PutUint64(out[i*8:], h.Sum64())
	}
	return out
}

// untrustedBody is one wrapper in a message.
type untrustedBody struct {
	source string
	body   string
	start  int
	end    int
}

// untrustedBodies returns every real untrusted-content wrapper in s
// (session.UntrustedBodies).
func untrustedBodies(s string) []untrustedBody {
	return loopUntrustedBodies(session.UntrustedBodies(s), "")
}

// stripUntrustedBodies removes every wrapper and fence from s, leaving the
// text the principal wrote (session.StripUntrustedBodies).
func stripUntrustedBodies(s string) string {
	return session.StripUntrustedBodies(s)
}

// untrustedInputSource labels spans recorded from a delegated task that its
// parent fenced as untrusted input.
const untrustedInputSource = "delegated task (untrusted input)"

// untrustedInputBodies returns every <untrusted_input_<nonce>> fence in s
// (session.UntrustedInputBodies): the boundary a parent puts around a
// delegated task derived from untrusted content, labelled
// untrustedInputSource.
func untrustedInputBodies(s string) []untrustedBody {
	return loopUntrustedBodies(session.UntrustedInputBodies(s), untrustedInputSource)
}

// loopUntrustedBodies converts the shared parser's regions, labelling each
// with source when source is set.
func loopUntrustedBodies(in []session.UntrustedBody, source string) []untrustedBody {
	if len(in) == 0 {
		return nil
	}
	out := make([]untrustedBody, len(in))
	for i, b := range in {
		src := b.Source
		if source != "" {
			src = source
		}
		out[i] = untrustedBody{source: src, body: b.Body, start: b.Start, end: b.End}
	}
	return out
}

// bgReadTool reports tools that surface background-job output; they inherit
// the taint of the job they read (see bgSourceLocked).
func bgReadTool(name string) bool { return name == "bg_output" || name == "bg_status" }

var (
	// taintBgStartJobRe finds the job id in a bg_start result.
	taintBgStartJobRe = regexp.MustCompile(`"job_id"\s*:\s*"([^"\\]{1,128})"`)
	// taintBgNoticeJobRe finds the job ids a background notice names (the
	// background manager's id format). Text inside the notice can only add
	// ids, which only makes the notice more likely to count as a source.
	taintBgNoticeJobRe = regexp.MustCompile(`\bbg_[0-9a-f]{8,16}\b`)
)

// bgStartedJob returns the job id a bg_start result reports.
func bgStartedJob(result string) string {
	if m := taintBgStartJobRe.FindStringSubmatch(result); m != nil {
		return m[1]
	}
	return ""
}

// bgReadJobs returns the job a bg_output or bg_status call reads.
func bgReadJobs(tc *session.ToolCall) []string {
	var a struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &a) != nil || a.JobID == "" {
		return nil
	}
	return []string{a.JobID}
}

// bgNoticeJobs returns the job ids a background notice names.
func bgNoticeJobs(body string) []string {
	return taintBgNoticeJobRe.FindAllString(body, 64)
}

// bgSourceLocked reports whether background-job content naming jobs is a
// span source: once any tainting job ran in this session, always; else when
// it names no job, a job this store never saw started, or a job whose
// bg_start call taints. Unknown origin fails closed: the decision never
// depends on whether trimming left the bg_start call in view.
func (s *taintSpanStore) bgSourceLocked(jobs []string) bool {
	if s.bgTainted || len(jobs) == 0 {
		return true
	}
	for _, id := range jobs {
		if tainting, known := s.bgJobs[id]; !known || tainting {
			return true
		}
	}
	return false
}

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

// observe records spans from the messages not yet scanned and refreshes the
// principal's mentions, for the gate.
func (s *taintSpanStore) observe(messages []session.Message, env taintEnv) {
	s.observeTrack(messages, env, &s.gateTrack)
}

// observeTrack is observe for a caller with its own history track. It
// returns each message's scan digest.
func (s *taintSpanStore) observeTrack(messages []session.Message, env taintEnv, track *taintScanTrack) []taintDigest {
	// Each tool result is judged by the call it answers: the nearest
	// preceding call with its id (providers may reuse ids across turns).
	paired := make([]*session.ToolCall, len(messages))
	latest := make(map[string]*session.ToolCall)
	bgStartTaints := make(map[*session.ToolCall]bool)
	bgTainted := false
	type bgJob struct {
		id       string
		tainting bool
	}
	var bgJobs []bgJob
	var principal strings.Builder
	for i := range messages {
		m := &messages[i]
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			latest[tc.ID] = tc
			if tc.Function.Name == "bg_start" {
				taints := session.ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments)
				bgStartTaints[tc] = taints
				bgTainted = bgTainted || taints
			}
		}
		if m.Role != "tool" {
			continue
		}
		tc := latest[m.ToolCallID]
		paired[i] = tc
		if tc != nil && tc.Function.Name == "bg_start" {
			if id := bgStartedJob(m.Content); id != "" {
				bgJobs = append(bgJobs, bgJob{id, bgStartTaints[tc]})
			}
		}
	}
	// The principal's messages are collected newest first, so the latest
	// instructions always fit the budget however long an early paste was.
	for i := len(messages) - 1; i >= 0 && principal.Len() < taintPrincipalMaxBytes; i-- {
		m := &messages[i]
		if m.Role != "user" || session.IsSyntheticUserName(m.Name) {
			continue
		}
		text := stripUntrustedBodies(m.Content)
		if room := taintPrincipalMaxBytes - principal.Len(); len(text) > room {
			text = text[:room]
		}
		principal.WriteString(text)
		principal.WriteByte('\n')
	}

	// Every message's digest binds the call it answers, so equal digests
	// mean equal decisions.
	d := make([]taintDigest, len(messages))
	for i := range messages {
		d[i] = taintMessageDigest(&messages[i], paired[i])
	}

	// Extraction runs outside the mutex: only the scan bookkeeping and the
	// span insertions hold it.
	principalText := principal.String()
	s.mu.Lock()
	if s.envSet && s.env != env {
		// Path spans and the workspace trust rule depend on the
		// environment: decisions taken under another one do not carry.
		s.resetScanLocked()
	}
	s.env, s.envSet = env, true
	needPrincipal := principalText != s.principal.raw || s.principal.spans == nil
	seedCov := s.seedCov
	tryCoverage := seedCov.Valid() && !s.seedCovApplied && s.seedCovTries < maxTaintSeedCoverageTries
	if tryCoverage {
		s.seedCovTries++
	}
	s.mu.Unlock()
	var principalSpans map[string]bool
	if needPrincipal {
		principalSpans = taintPrincipalSpans(principalText, env)
	}
	var covTracker taintCoverageTracker
	covOK := false
	if tryCoverage {
		covOK, covTracker = verifySeedCoverage(messages, d, seedCov, env)
	}

	// A job is one source to record. A source a verified coverage vouches
	// for is queued links-only at the place a full scan would extract it,
	// so the links (which are not persisted) are recorded in the same order
	// and through the same windows as a full scan records them.
	type job struct {
		text, source string
		goSource     bool
		linksOnly    bool
	}
	var jobs []job
	s.mu.Lock()
	if covOK && s.seedCov == seedCov && s.env == env && !s.seedCovApplied {
		// The seeded state records the spans of the first seedCov.Messages
		// counted messages: their extraction is skipped below.
		s.seedCovApplied = true
		if s.covered == nil {
			s.covered = make(map[taintDigest]struct{}, seedCov.Messages)
		}
		for i, counted := 0, 0; i < len(messages) && counted < seedCov.Messages; i++ {
			if taintCountedMessage(&messages[i]) {
				s.covered[d[i]] = struct{}{}
				counted++
			}
		}
		s.covMu.Lock()
		if s.cov.hash == nil {
			s.cov = covTracker
		}
		s.covMu.Unlock()
	}
	// The background facts are sticky: a bg_start call trimming later drops
	// still decides the content of its job.
	if bgTainted && !s.bgTainted {
		s.bgTainted = true
		s.version++
	}
	for _, j := range bgJobs {
		if len(s.bgJobs) >= maxTaintBgJobs {
			s.bgJobs = make(map[string]bool)
		}
		s.bgJobs[j.id] = s.bgJobs[j.id] || j.tainting
	}
	start := 0
	if n := track.n; n > 0 && n <= len(messages) && taintDigestChain(d[:n]) == track.chain {
		start = n
	}
	for i := start; i < len(messages); i++ {
		m := &messages[i]
		if _, ok := s.scanned[d[i]]; ok {
			continue
		}
		if len(s.scanned) >= maxTaintScanned {
			s.scanned = make(map[taintDigest]struct{})
		}
		s.scanned[d[i]] = struct{}{}
		// A source a verified coverage vouches for has only its links
		// extracted. Background content is always judged and extracted
		// afresh.
		_, covered := s.covered[d[i]]
		switch m.Role {
		case "assistant":
			continue
		case "tool":
			tc := paired[i]
			bgContent := tc != nil && bgReadTool(tc.Function.Name)
			if tc != nil {
				if bgContent {
					if !s.bgSourceLocked(bgReadJobs(tc)) {
						continue
					}
				} else if !session.ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) {
					continue
				}
			}
			if covered && !bgContent {
				jobs = append(jobs, job{text: m.Content, linksOnly: true})
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
			jobs = append(jobs, job{text: m.Content, source: source, goSource: tc != nil && taintGoSource(tc)})
		default:
			for _, w := range untrustedBodies(m.Content) {
				switch {
				case session.ExternalNonToolIngest(w.source):
					jobs = append(jobs, job{text: w.body, source: w.source, linksOnly: covered})
				case w.source == "bg" && s.bgSourceLocked(bgNoticeJobs(w.body)):
					jobs = append(jobs, job{text: w.body, source: w.source})
				}
			}
			if m.Role == "user" {
				for _, w := range untrustedInputBodies(m.Content) {
					jobs = append(jobs, job{text: w.body, source: w.source, linksOnly: covered})
				}
			}
		}
	}
	*track = taintScanTrack{n: len(messages), chain: taintDigestChain(d)}
	s.mu.Unlock()

	extracted := make([][]taintRaw, len(jobs))
	for i, j := range jobs {
		if j.linksOnly {
			extracted[i] = extractTaintLinksForRecord(j.text)
		} else {
			extracted[i] = extractTaintSpansForRecord(j.text, env, j.goSource)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if needPrincipal {
		s.principal = taintPrincipal{raw: principalText, spans: principalSpans}
	}
	for i, j := range jobs {
		if j.linksOnly {
			s.insertLinksLocked(extracted[i])
		} else {
			s.insertLocked(extracted[i], j.source)
		}
	}
	return d
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

// insertLinksLocked records the links of extracted spans exactly as
// insertLocked records them, and nothing else.
func (s *taintSpanStore) insertLinksLocked(spans []taintRaw) {
	for _, r := range spans {
		if r.kind == taintKindURL {
			s.recordLinkLocked(r.link)
		}
	}
}

// insertLocked records extracted spans under source.
func (s *taintSpanStore) insertLocked(spans []taintRaw, source string) {
	for _, r := range spans {
		if r.kind == taintKindURL {
			s.recordLinkLocked(r.link)
			if r.value == "" {
				continue
			}
		}
		if r.kind == taintKindHost && taintCommonHosts[r.value] {
			continue
		}
		k := taintKey(r.kind, r.value)
		s.seq++
		s.version++
		if sp, ok := s.spans[k]; ok {
			sp.seq = s.seq
			continue
		}
		sp := &taintSpan{source: source, seq: s.seq}
		// A span is held once. A copy seeded as its hash gives way to this
		// sighting, which is newer and carries a real source label.
		if len(s.hashed) > 0 || s.plainByHash != nil {
			hk := taintKey(r.kind, sp.hashOf(r.value))
			delete(s.hashed, hk)
			if s.plainByHash != nil {
				s.plainByHash[hk] = k
			}
		}
		if len(s.spans) >= maxTaintSpans {
			s.evictTableLocked(s.spans, false)
		}
		s.spans[k] = sp
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

// evictTableLocked moves the least recently seen eighth of table (s.spans,
// or s.hashed when hashed is set) into the overflow filter.
func (s *taintSpanStore) evictTableLocked(table map[string]*taintSpan, hashed bool) {
	seqs := make([]uint64, 0, len(table))
	for _, sp := range table {
		seqs = append(seqs, sp.seq)
	}
	cutoff := eighthCutoff(seqs)
	for k, sp := range table {
		if sp.seq <= cutoff {
			s.overflowLocked(k, sp, hashed)
			if !hashed && s.plainByHash != nil {
				kind, value, _ := cutTaintKey(k)
				delete(s.plainByHash, taintKey(taintSpanKind(kind), sp.hashOf(value)))
			}
			delete(table, k)
		}
	}
	s.version++
}

// overflowLocked adds the span keyed k (in the hashed table when hashed is
// set) to the overflow filter, once.
func (s *taintSpanStore) overflowLocked(k string, sp *taintSpan, hashed bool) {
	if sp.overflowed {
		return
	}
	kind, value, _ := cutTaintKey(k)
	if hashed {
		s.overflow.AddHash(kind, value)
	} else {
		s.overflow.AddValue(kind, value)
	}
	sp.overflowed = true
	s.overflowFillKnown = false
}

// overflowSourceLocked returns the source label of a hit found only in the
// overflow filter: taintOverflowFullSource once the filter is nearly full,
// taintOverflowSource before. The fill is counted once per filter change.
func (s *taintSpanStore) overflowSourceLocked() string {
	if !s.overflowFillKnown {
		s.overflowFill, s.overflowFillKnown = s.overflow.Fill(), true
	}
	if s.overflowFill >= session.TaintOverflowNearlyFull {
		return taintOverflowFullSource
	}
	return taintOverflowSource
}

// recordLinkLocked records link as the most recently seen link, evicting
// first when the table is full (also when link is already held).
func (s *taintSpanStore) recordLinkLocked(link string) {
	if len(s.links) >= maxTaintLinks {
		s.evictLinksLocked()
	}
	s.seq++
	s.links[link] = s.seq
}

// evictLinksLocked drops the least recently seen eighth of the links. A
// dropped link is not recorded anywhere: it only exempted a URL under the
// egress rule, so losing it fails closed.
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

// lookupLocked finds a recorded span covering candidate r: a held span, or
// one the overflow filter records.
func (s *taintSpanStore) lookupLocked(r taintRaw) (taintHit, bool) {
	try := func(kind taintSpanKind, v string) (taintHit, bool) {
		var source string
		sp, ok := s.spans[taintKey(kind, v)]
		if ok {
			source = sp.source
		} else if len(s.hashed) > 0 || len(s.overflow) > 0 {
			h := taintHash(v)
			if sp, ok = s.hashed[taintKey(kind, h)]; ok {
				source = sp.source
			} else if s.overflow.HasHash(string(kind), h) {
				ok, source = true, s.overflowSourceLocked()
			}
		}
		if !ok || s.principalLocked(kind, v) {
			return taintHit{}, false
		}
		return taintHit{kind: kind, span: v, source: source}, true
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

// emptyLocked reports whether the store has recorded no span at all: none
// held and none in the overflow filter.
func (s *taintSpanStore) emptyLocked() bool {
	return len(s.spans) == 0 && len(s.hashed) == 0 && len(s.overflow) == 0
}

// match returns the first recorded span text reuses. With egress set, the
// egress rule applies to the URLs in text.
func (s *taintSpanStore) match(text string, kinds taintKinds, egress bool, env taintEnv) (taintHit, bool) {
	if len(text) > taintMatchCeiling {
		// Too large to match in full: with any span recorded (held or in
		// the overflow filter), fail closed.
		s.mu.Lock()
		empty := s.emptyLocked()
		s.mu.Unlock()
		if empty {
			return taintHit{}, false
		}
		return taintHit{kind: taintKindOversize, span: fmt.Sprintf("%d-byte argument", len(text)), source: "an argument too large to check"}, true
	}
	cands := extractTaintSpans(text, env, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emptyLocked() {
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

// taintURLPositionWord reports whether a shell word with an unresolved
// expansion sits where a URL is built: it holds a scheme, or the text before
// the expansion already has a host or path shape (".", "/" or ":").
func taintURLPositionWord(w string) bool {
	if strings.Contains(w, "://") {
		return true
	}
	cut := len(w)
	for _, marker := range []string{"$", "`", "odek.dynamic-subst"} {
		if i := strings.Index(w, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.ContainsAny(w[:cut], "./:")
}

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
		// One analysis serves the class and the request-data check. The
		// unread-script gate only raises an already non-safe class, so the
		// plain analysis class decides safe and plain-egress here.
		analysis := danger.Analyze(p.Command)
		cls := analysis.Class()
		if cls == danger.Safe {
			return taintHit{}, "", false
		}
		// The egress rule judges URL text only, so it applies only when no
		// stage sends request data, headers or query options beside it.
		egress := cls == danger.NetworkEgress && !analysis.RequestData
		// The exemption is decided on the words the shell really passes:
		// quoting, a backslash or "$Q" can splice a query onto a linked URL
		// that the raw text shows bare. A URL-position word holding an
		// unresolved expansion voids the exemption outright, and both the
		// raw and the normalised text are matched.
		var normalised string
		if egress {
			// An over-long command classifies unknown, never plain egress,
			// so ShellWords always succeeds here.
			words, dynamic, _ := danger.ShellWords(p.Command)
			for i, w := range words {
				if dynamic[i] && taintURLPositionWord(w) {
					egress = false
				}
			}
			normalised = strings.Join(words, " ")
		}
		hit, ok := store.match(p.Command, taintMatchAll, egress, env)
		if !ok && normalised != "" && normalised != p.Command {
			hit, ok = store.match(normalised, taintMatchAll, egress, env)
		}
		return hit, p.Command, ok
	case "write_file", "patch":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &p) != nil || p.Path == "" {
			return taintHit{}, "", false
		}
		target := p.Path
		if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "~") && !strings.HasPrefix(target, "$") {
			// Relative targets resolve against the working directory, so
			// "../" cannot step outside the workspace unnoticed.
			if env.cwd == "" || env.cwd == "." {
				return taintHit{}, "", false
			}
			target = filepath.Join(env.cwd, target)
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
	if strings.Contains(name, "__") || name == "delegate_tasks" {
		// An MCP tool is third-party code with any effect, and a delegated
		// task hands its strings to a child that acts on them: every string
		// argument takes the full match.
		hit, ok := store.match(taintArgumentStrings(args), taintMatchAll, false, env)
		return hit, name + " " + args, ok
	}
	return taintHit{}, "", false
}

// taintArgumentStrings returns every string (and object key) in a JSON
// argument object, one per line. Collection stops just past taintMatchCeiling,
// so an oversized argument set reaches match's fail-closed rule. Arguments
// that do not parse are returned as they are.
func taintArgumentStrings(args string) string {
	var v any
	if json.Unmarshal([]byte(args), &v) != nil {
		return args
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		if b.Len() > taintMatchCeiling {
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

// ResetTaintSpans empties the engine's untrusted-span record: both span
// tables, the overflow filter, the links, the scan bookkeeping, the
// principal's mentions, the export memo and the seed identity. The record
// belongs to one session; an engine that serves several in turn (a Web UI
// connection) calls this when its session changes, so the next run starts
// from the new session's persisted state only (WithTaintSpanSeed) and the
// previous session's record never reaches the new one's file. Call it
// between runs, after the previous session's state was saved.
func (e *Engine) ResetTaintSpans() {
	e.taintMu.Lock()
	e.taintSpans = nil
	e.taintMu.Unlock()
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
	e.seedTaintSpans(ctx)
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
