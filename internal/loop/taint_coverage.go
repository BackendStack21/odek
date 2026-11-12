package loop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"hash/maphash"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/BackendStack21/odek/internal/session"
)

// Scan coverage: a rebuilt engine (Telegram per message, serve per
// connection, odek continue, a scheduled run) is seeded with the session's
// persisted spans, which already record every span of the history it is
// handed. Extracting them again costs time in proportion to the session's
// accumulated tool output on every turn. The persisted state therefore
// carries a coverage record (session.TaintCoverage): the number N of leading
// non-system messages whose spans it records, and a digest binding those
// messages. A seeded engine skips extraction only for messages of a prefix
// that reproduces the digest; any difference means a full scan.
//
// What a skip relies on, and how each part is bound:
//   - The digest is HMAC-SHA256 over every field the recording decision
//     reads (role, tool-call id, name, content, and each tool call's id,
//     name and arguments), length-prefixed; arguments are digested in the
//     form the store writes them when that form decodes exactly like the
//     original (taintCoverageArguments), so a reload keeps the coverage. It
//     must be stable across processes, so it cannot use the process-keyed
//     maphash of the scanned set. Content an attacker controls cannot make
//     unscanned messages look covered: that needs two different prefixes
//     with the same HMAC-SHA256, a SHA-256 collision. The key is derived,
//     not secret (anyone who knows the inputs can recompute it); it is there
//     to bind the digest to the span rules and the environment, not to hide
//     it. Content the store changes on save (a redacted secret, invalid
//     UTF-8 replaced) does not reproduce the digest: a full scan.
//   - The key binds taintCoverageRules, the binary's build identity, and the
//     home and working directories (path spans and the workspace trust rule
//     depend on them). A coverage recorded by another release, or under
//     another working directory, never matches. A binary that carries
//     neither a module version nor a VCS revision, or was built from a
//     modified source tree, records and applies no coverage at all.
//   - System messages are not counted and are always scanned: the runtime
//     prompt, memory block and other engine messages are rebuilt every run,
//     and binding them would void every match.
//   - Background-job content (notices, bg_output/bg_status) is never
//     skipped: its decision depends on facts about the whole history (see
//     bgSourceLocked) and is always taken again.
//   - The engine records coverage only for a history it has just observed,
//     under one environment, and the coverage travels in the same TaintState
//     as the spans it vouches for: they are saved in one file write, so a
//     failed save never advances it past them. Every merge keeps all spans
//     of both sides, so a coverage stays true of any merge it survives.
//   - The principal's mentions are collected from every message on every
//     observe, covered or not, so exemptions are rebuilt in full. Links (the
//     egress rule's verbatim URLs) are not persisted: the texts of skipped
//     source messages are kept and their links extracted the first time a
//     match needs one (drainLinkBackfillLocked).

// taintCoverageRules names the recording rules a coverage was taken under.
// Change it whenever extraction, the span rules or the source decision
// change, so coverage recorded under the old rules never matches.
const taintCoverageRules = "odek-taint-coverage-v2"

// maxTaintBackfillBytes bounds the texts kept for link backfill. Past it the
// rest are dropped: losing a link only removes its egress exemption, which
// fails closed.
const maxTaintBackfillBytes = 64 << 20

// taintCoverageBuild returns the build identity coverage is keyed by, or
// false when this binary records and applies no coverage. A variable so the
// package's tests, whose binaries carry no version or revision, can pin one.
var taintCoverageBuild = sync.OnceValues(func() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	return taintCoverageBuildOf(info)
})

// taintCoverageBuildOf derives the identity from build info: the module
// version, and the VCS revision and commit time when stamped. A build that
// tells releases apart by neither (an unstamped "(devel)" build), or one
// from a modified source tree, has no identity: the span rules of two such
// builds may differ, so neither records nor applies coverage.
func taintCoverageBuildOf(info *debug.BuildInfo) (string, bool) {
	var b strings.Builder
	b.WriteString(info.Main.Path + "@" + info.Main.Version)
	identified := info.Main.Version != "" && info.Main.Version != "(devel)"
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.modified":
			if s.Value == "true" {
				return "", false
			}
		case "vcs.revision", "vcs.time":
			b.WriteString(" " + s.Key + "=" + s.Value)
			identified = identified || s.Key == "vcs.revision" && s.Value != ""
		}
	}
	if !identified {
		return "", false
	}
	return b.String(), true
}

// taintCoverageKey returns the HMAC key for env, or false when this binary
// records and applies no coverage.
func taintCoverageKey(env taintEnv) ([]byte, bool) {
	build, ok := taintCoverageBuild()
	if !ok {
		return nil, false
	}
	key := strings.Join([]string{taintCoverageRules, build, env.home, env.cwd}, "\x00")
	return []byte(key), true
}

// taintCoverageHash digests counted messages in order.
type taintCoverageHash struct {
	h   hash.Hash
	n   int
	buf [4096]byte
}

// writeString feeds s through buf: the HMAC has no string writer, and
// converting each message to a byte slice would copy the whole history.
func (c *taintCoverageHash) writeString(s string) {
	for len(s) > 0 {
		k := copy(c.buf[:], s)
		c.h.Write(c.buf[:k])
		s = s[k:]
	}
}

func newTaintCoverageHash(key []byte) *taintCoverageHash {
	return &taintCoverageHash{h: hmac.New(sha256.New, key)}
}

// add digests m (a non-system message).
func (c *taintCoverageHash) add(m *session.Message) {
	field := func(f string) {
		binary.BigEndian.PutUint64(c.buf[:8], uint64(len(f)))
		c.h.Write(c.buf[:8])
		c.writeString(f)
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
		// Arguments are digested in the form the store writes them, tagged
		// so the two forms never share a digest (see taintCoverageArguments).
		args, canonical := taintCoverageArguments(tc.Function.Arguments)
		if canonical {
			field("canonical")
		} else {
			field("raw")
		}
		field(args)
	}
	c.n++
}

// taintCoverageArguments returns the form a tool call's arguments are
// digested in, and whether it is the canonical one.
//
// The store rewrites the arguments of every message it saves
// (session.CanonicalToolArguments: decoded, keys sorted, compact, no HTML
// escaping) while the engine records spans from the arguments as the
// provider sent them, so a digest over the raw text would never reproduce
// after a reload. Digesting the canonical form keeps the coverage across the
// store, and it is sound because the recording decisions read the arguments
// only through struct decodes (session.ToolCallTaintsEpisode,
// taintGoSource, bgReadJobs): for arguments CanonicalToolArguments accepts,
// every struct decode reads the same values from the original and from the
// canonical form, so a history saved in either form is judged exactly as the
// engine that recorded the coverage judged it. Arguments it refuses (invalid
// JSON, text after the value, two keys one struct field would take) are
// digested raw: the store rewrites them on save, so such a history never
// reproduces the coverage and is scanned in full. Canonicalisation is
// idempotent, and the file may hold either form (the store writes messages
// it already saved as they are), so both digest alike.
func taintCoverageArguments(args string) (string, bool) {
	if canon, ok := session.CanonicalToolArguments(args); ok {
		return canon, true
	}
	return args, false
}

func (c *taintCoverageHash) sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

// taintCountedMessage reports whether m counts toward coverage.
func taintCountedMessage(m *session.Message) bool { return m.Role != "system" }

// taintChainSeeds key the in-memory chain over message digests that tells a
// coverage tracker its counted prefix is unchanged.
var taintChainSeeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}

// taintChain digests a sequence of message digests.
type taintChain struct{ h [2]maphash.Hash }

func newTaintChain() *taintChain {
	c := &taintChain{}
	for i := range c.h {
		c.h[i].SetSeed(taintChainSeeds[i])
	}
	return c
}

func (c *taintChain) add(d taintDigest) {
	for i := range c.h {
		c.h[i].Write(d[:])
	}
}

func (c *taintChain) sum() [2]uint64 { return [2]uint64{c.h[0].Sum64(), c.h[1].Sum64()} }

// taintCoverageTracker is the coverage the store can vouch for: hash holds
// the HMAC over the first n counted messages of the history it last
// recorded, chain the in-memory digest of those messages.
type taintCoverageTracker struct {
	hash  *taintCoverageHash
	chain [2]uint64
	n     int
}

// coverageLocked returns the coverage of messages (with digests d, observed
// just now under env), extending the tracker when its counted prefix is
// unchanged and rebuilding it otherwise. The caller holds covMu.
func (s *taintSpanStore) coverageLocked(messages []session.Message, d []taintDigest, env taintEnv) session.TaintCoverage {
	key, ok := taintCoverageKey(env)
	if !ok {
		return session.TaintCoverage{}
	}
	t := &s.cov
	chain := newTaintChain()
	start := 0
	if t.hash != nil {
		// Extend only when this history begins with the counted messages
		// the tracker already hashed.
		counted := 0
		for start < len(messages) && counted < t.n {
			if taintCountedMessage(&messages[start]) {
				chain.add(d[start])
				counted++
			}
			start++
		}
		if counted != t.n || chain.sum() != t.chain {
			t.hash = nil
		}
	}
	if t.hash == nil {
		*t = taintCoverageTracker{hash: newTaintCoverageHash(key)}
		chain, start = newTaintChain(), 0
	}
	for i := start; i < len(messages); i++ {
		if taintCountedMessage(&messages[i]) {
			t.hash.add(&messages[i])
			chain.add(d[i])
		}
	}
	t.chain, t.n = chain.sum(), t.hash.n
	if t.n == 0 {
		return session.TaintCoverage{}
	}
	return session.TaintCoverage{Messages: t.n, Digest: t.hash.sum()}
}

// verifySeedCoverage reports whether messages reproduce the seeded coverage
// c under env. On a match it also returns the tracker positioned after the
// covered prefix, so recording the next coverage extends it.
func verifySeedCoverage(messages []session.Message, d []taintDigest, c session.TaintCoverage, env taintEnv) (bool, taintCoverageTracker) {
	key, ok := taintCoverageKey(env)
	if !ok || !c.Valid() {
		return false, taintCoverageTracker{}
	}
	h := newTaintCoverageHash(key)
	chain := newTaintChain()
	for i := range messages {
		if h.n == c.Messages {
			break
		}
		if !taintCountedMessage(&messages[i]) {
			continue
		}
		h.add(&messages[i])
		chain.add(d[i])
	}
	if h.n != c.Messages || !hmac.Equal([]byte(h.sum()), []byte(c.Digest)) {
		return false, taintCoverageTracker{}
	}
	return true, taintCoverageTracker{hash: h, chain: chain.sum(), n: h.n}
}

// addLinkBackfillLocked keeps text, a source the store skipped because a
// verified coverage vouches for its spans, for link extraction on demand.
func (s *taintSpanStore) addLinkBackfillLocked(text string) {
	if s.backfillBytes+len(text) > maxTaintBackfillBytes {
		return
	}
	s.backfill = append(s.backfill, text)
	s.backfillBytes += len(text)
}

// drainLinkBackfillLocked records the links of every kept text. A link a
// full scan would have recorded earlier than anything this store saw gets a
// sequence number below all others, so link eviction still takes the oldest
// first; a link already held keeps its place.
func (s *taintSpanStore) drainLinkBackfillLocked() {
	texts := s.backfill
	s.backfill, s.backfillBytes = nil, 0
	for i := len(texts) - 1; i >= 0; i-- {
		for _, r := range extractTaintURLs(texts[i]) {
			if r.kind != taintKindURL {
				continue
			}
			if _, held := s.links[r.link]; held {
				continue
			}
			if len(s.links) >= maxTaintLinks {
				s.evictLinksLocked()
			}
			s.low--
			s.links[r.link] = s.low
		}
	}
}
