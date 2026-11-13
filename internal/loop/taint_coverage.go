package loop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
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
//     and binding them would void every match. Nothing on a system message
//     feeds the decision for a counted one: a tool result pairs only with
//     the assistant call that issued it, and every assistant message is
//     counted, so a counted result's pairing is fixed by the counted prefix.
//   - Background-job content (notices, bg_output/bg_status) is never
//     skipped: its decision depends on facts about the whole history (see
//     bgSourceLocked) and is always taken again.
//   - The engine records coverage only for a history it has just observed,
//     under one environment, and the coverage travels in the same TaintState
//     as the spans it vouches for: they are saved in one file write, so a
//     failed save never advances it past them. Every merge keeps all spans
//     of both sides, so a coverage stays true of any merge it survives.
//     The export that carries it is read from the store that observed the
//     history, while no observe has spans still to insert for messages it
//     marked scanned, and the store must still be the engine's record
//     afterwards (TaintSpanState); otherwise the state goes out without
//     coverage.
//   - The principal's mentions are collected from every message on every
//     observe, covered or not, so exemptions are rebuilt in full. Links (the
//     egress rule's verbatim URLs) are not persisted: a covered source is
//     queued links-only at the place a full scan would queue it, and its
//     links are extracted through the same windows (taintRecordWindows) and
//     recorded in the same order, so the link table, its eviction included,
//     is the one a full scan builds.
//
// What a covered engine does not reproduce is the recency of the spans the
// skipped sources hold: a full scan sees them again (newest, and a span
// seeded as its hash becomes a plain span with the new source label), the
// covered engine keeps their seeded order. That only decides which spans
// eviction moves into the overflow filter first and which label a hit
// shows; the filter records every evicted span, so whether a call escalates
// is the same (up to the filter's false positives).

// taintCoverageRules names the recording rules a coverage was taken under.
// Change it whenever extraction, the span rules or the source decision
// change, so coverage recorded under the old rules never matches.
const taintCoverageRules = "odek-taint-coverage-v3"

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
	h hash.Hash
	n int
	// bytes counts the field bytes digested.
	bytes int64
	buf   [4096]byte
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
		c.bytes += int64(len(f))
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

// taintCountedEnd returns the length of the shortest prefix of messages that
// holds n counted messages, and false when messages hold fewer.
func taintCountedEnd(messages []session.Message, n int) (int, bool) {
	counted := 0
	for i := range messages {
		if counted == n {
			return i, true
		}
		if taintCountedMessage(&messages[i]) {
			counted++
		}
	}
	return len(messages), counted == n
}

// taintCoverageTracker is the coverage the store can vouch for: hash holds
// the HMAC over the counted messages of the history it last recorded, digs
// their scan digests in order (one per counted message), which tell the
// next recording whether its history begins with them.
type taintCoverageTracker struct {
	hash *taintCoverageHash
	digs []taintDigest
}

// coverageLocked returns the coverage of messages (with what was derived
// from them, seen, observed just now under env), extending the tracker when
// its counted prefix is unchanged and rebuilding it otherwise. The caller
// holds covMu.
func (s *taintSpanStore) coverageLocked(messages []session.Message, seen []taintSeen, env taintEnv) session.TaintCoverage {
	key, ok := taintCoverageKey(env)
	if !ok {
		return session.TaintCoverage{}
	}
	t := &s.cov
	start := 0
	if t.hash != nil {
		// Extend only when this history begins with the counted messages
		// the tracker already hashed: equal scan digests mean the same
		// fields, which the HMAC reads.
		counted := 0
		for start < len(messages) && counted < len(t.digs) {
			if taintCountedMessage(&messages[start]) {
				if seen[start].d != t.digs[counted] {
					break
				}
				counted++
			}
			start++
		}
		if counted != len(t.digs) {
			t.hash = nil
		}
	}
	if t.hash == nil {
		*t = taintCoverageTracker{hash: newTaintCoverageHash(key)}
		start = 0
	}
	before := t.hash.bytes
	for i := start; i < len(messages); i++ {
		if taintCountedMessage(&messages[i]) {
			t.hash.add(&messages[i])
			t.digs = append(t.digs, seen[i].d)
		}
	}
	s.coverageWork.Add(t.hash.bytes - before)
	if len(t.digs) == 0 {
		return session.TaintCoverage{}
	}
	return session.TaintCoverage{Messages: len(t.digs), Digest: t.hash.sum()}
}

// taintSeedCheck is the outcome of checking a seeded coverage: applies when
// it may be marked applied, verified when a history reproduced it (tracker
// then holds the HMAC and digests of the covered messages).
type taintSeedCheck struct {
	applies, verified bool
	tracker           taintCoverageTracker
}

// checkSeedCoverage checks the seeded coverage c for an observe of messages
// (seen derived from them). A coverage equal to the one the store records
// now is its own: those messages were observed here, and it applies without
// a check. Otherwise the history checked is transcript when set (the run's
// full history; messages may be a trimmed projection of it, which cannot
// reproduce the coverage) and messages otherwise. A history with fewer
// counted messages than c covers cannot reproduce it and costs nothing; any
// other spends the store's one check of c.
func (s *taintSpanStore) checkSeedCoverage(c session.TaintCoverage, messages []session.Message, seen []taintSeen, transcript []session.Message, env taintEnv) taintSeedCheck {
	if s.ownCoverage(c) {
		return taintSeedCheck{applies: true}
	}
	target := messages
	if transcript != nil {
		target, seen = transcript, nil
	}
	end, ok := taintCountedEnd(target, c.Messages)
	if !ok {
		return taintSeedCheck{}
	}
	s.mu.Lock()
	check := s.seedCov == c && !s.seedCovApplied && s.seedCovTries < maxTaintSeedCoverageTries
	if check {
		s.seedCovTries++
	}
	s.mu.Unlock()
	if !check {
		return taintSeedCheck{}
	}
	target = target[:end]
	if seen == nil {
		var digested int64
		seen, _, _, digested, _ = taintSeenFor(target, taintScanTrack{})
		s.digestWork.Add(digested)
	}
	ok, tracker, hashed := verifySeedCoverage(target, seen, c, env)
	s.verifyWork.Add(hashed)
	return taintSeedCheck{applies: ok, verified: ok, tracker: tracker}
}

// ownCoverage reports whether c is the coverage the store records now.
func (s *taintSpanStore) ownCoverage(c session.TaintCoverage) bool {
	s.covMu.Lock()
	defer s.covMu.Unlock()
	t := &s.cov
	return t.hash != nil && len(t.digs) == c.Messages && hmac.Equal([]byte(t.hash.sum()), []byte(c.Digest))
}

// verifySeedCoverage reports whether messages (with what was derived from
// them, seen) reproduce the seeded coverage c under env. On a match it also
// returns the tracker positioned after the covered prefix, so recording the
// next coverage extends it. hashed is the number of message bytes the check
// digested.
func verifySeedCoverage(messages []session.Message, seen []taintSeen, c session.TaintCoverage, env taintEnv) (bool, taintCoverageTracker, int64) {
	key, ok := taintCoverageKey(env)
	if !ok || !c.Valid() {
		return false, taintCoverageTracker{}, 0
	}
	h := newTaintCoverageHash(key)
	digs := make([]taintDigest, 0, c.Messages)
	for i := range messages {
		if h.n == c.Messages {
			break
		}
		if !taintCountedMessage(&messages[i]) {
			continue
		}
		h.add(&messages[i])
		digs = append(digs, seen[i].d)
	}
	if h.n != c.Messages || !hmac.Equal([]byte(h.sum()), []byte(c.Digest)) {
		return false, taintCoverageTracker{}, h.bytes
	}
	return true, taintCoverageTracker{hash: h, digs: digs}, h.bytes
}
