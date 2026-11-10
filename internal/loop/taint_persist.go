package loop

import (
	"context"
	"slices"
	"sort"

	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
)

// Span persistence: the engine exports its recorded spans as sticky session
// state (session.Session.TaintSpans) and a run seeds its store from that
// state, so trimming the history or rebuilding the engine (serve and
// Telegram per turn, odek continue) never forgets a span. A value that could
// carry a secret — every token, anything the redactor would change, anything
// over the persisted size cap — is exported as its SHA-256 only; the store
// matches such entries by hashing each candidate. The persisted list is
// bounded (session.MaxTaintSpans); the store's overflow filter, which also
// records every span the export cuts from the list, is exported and seeded
// with it (session.Session.TaintOverflow), so no bound forgets a span.

type taintSeedKey struct{}

// WithTaintSpanSeed returns a context whose runs seed the engine's span
// store from st (a session's persisted TaintSpans and TaintOverflow).
func WithTaintSpanSeed(ctx context.Context, st session.TaintState) context.Context {
	if st.Empty() {
		return ctx
	}
	return context.WithValue(ctx, taintSeedKey{}, st)
}

func taintSpanSeedFrom(ctx context.Context) session.TaintState {
	if ctx == nil {
		return session.TaintState{}
	}
	st, _ := ctx.Value(taintSeedKey{}).(session.TaintState)
	return st
}

func taintHash(v string) string { return session.TaintSpanHash(v) }

// seedStateLocked seeds the store from persisted state it has not seeded
// yet: the spans, and the overflow filter ORed into the store's own.
func (s *taintSpanStore) seedStateLocked(st session.TaintState) {
	if n := len(st.Spans); n > 0 && (s.seedLen != n || s.seedHead != &st.Spans[0]) {
		s.seedLocked(st.Spans)
		s.seedLen, s.seedHead = n, &st.Spans[0]
	}
	if len(st.Overflow) > 0 && s.seedOverflow != &st.Overflow[0] {
		// The union is a new filter, so the store never aliases the seed.
		s.overflow = session.UnionTaintOverflow(s.overflow, st.Overflow)
		s.seedOverflow = &st.Overflow[0]
		s.version++
	}
}

// seedLocked adds persisted spans the store does not hold yet. A full table
// evicts into the overflow filter, as recording does.
//
// The list is newest first (as TaintSpanState exports it) and is seeded
// oldest first, so it keeps its recency: the newest persisted span is the
// most recently seen of the seeded ones, every span recorded afterwards is
// newer still, eviction takes the oldest persisted spans first, and an
// export of the seeded store gives the list back in the same order.
//
// A span is held once, in one table: a persisted hash whose plain span is
// already held only makes the plain span newer, and a persisted plain span
// replaces a held copy of its hash (the plain form needs no hashing to
// match and keeps a readable source).
func (s *taintSpanStore) seedLocked(spans []session.TaintSpan) {
	// plainByHash maps the hashed key of every plain span to its plain key;
	// it is built only when a persisted hash meets held plain spans.
	var plainByHash map[string]string
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		kind := taintSpanKind(sp.Kind)
		switch kind {
		case taintKindURL, taintKindHost, taintKindPath, taintKindToken:
		default:
			continue
		}
		table, value, hashed := s.spans, sp.Value, sp.Hash != ""
		if hashed {
			table, value = s.hashed, sp.Hash
		}
		if value == "" {
			continue
		}
		k := taintKey(kind, value)
		if _, ok := table[k]; ok {
			continue
		}
		if hashed && len(s.spans) > 0 {
			if plainByHash == nil {
				plainByHash = make(map[string]string, len(s.spans))
				for pk := range s.spans {
					pkind, pvalue, _ := cutTaintKey(pk)
					plainByHash[taintKey(taintSpanKind(pkind), taintHash(pvalue))] = pk
				}
			}
			if held := s.spans[plainByHash[k]]; held != nil {
				s.seq++
				s.version++
				held.seq = s.seq
				continue
			}
		}
		if !hashed && (len(s.hashed) > 0 || plainByHash != nil) {
			hk := taintKey(kind, taintHash(value))
			delete(s.hashed, hk)
			if plainByHash != nil {
				plainByHash[hk] = k
			}
		}
		if len(table) >= maxTaintSpans {
			s.evictTableLocked(table, hashed)
		}
		s.seq++
		s.version++
		table[k] = &taintSpan{source: sp.Source, seq: s.seq}
	}
}

// taintExport is a memoized TaintSpanState result: valid while the store's
// version and the redaction registry generation are those it was computed
// under.
type taintExport struct {
	state   session.TaintState
	version uint64
	gen     uint64
}

// cloneTaintState returns a copy of st that shares no backing array with it.
func cloneTaintState(st session.TaintState) session.TaintState {
	return session.TaintState{Spans: slices.Clone(st.Spans), Overflow: slices.Clone(st.Overflow)}
}

// persistedLocked returns the span's persisted form — kind, value or hash,
// and redacted source — computing it once per redaction registry generation
// gen. A token, a value over the persisted size cap, and a value the
// redactor would change are persisted as their hash; a secret registered
// later (a new generation) re-decides it. redactedSources memoizes source
// labels within one export, since a message's spans share one.
func (sp *taintSpan) persistedLocked(k string, hashed bool, gen uint64, redactedSources map[string]string) session.TaintSpan {
	if sp.persistedSet && sp.persistedGen == gen {
		return sp.persisted
	}
	kind, value, _ := cutTaintKey(k)
	source, ok := redactedSources[sp.source]
	if !ok {
		source = redact.RedactSecrets(sp.source)
		redactedSources[sp.source] = source
	}
	out := session.TaintSpan{Kind: kind, Source: source}
	switch {
	case hashed:
		out.Hash = value
	case kind == string(taintKindToken) || len(value) > session.MaxTaintSpanValueBytes || redact.RedactSecrets(value) != value:
		out.Hash = taintHash(value)
	default:
		out.Value = value
	}
	sp.persisted, sp.persistedGen, sp.persistedSet = out, gen, true
	return out
}

// TaintSpanState records the spans of messages (the history being saved,
// so content that arrived after the last gate counts) and exports the
// engine's span state: the newest session.MaxTaintSpans recorded spans,
// newest first, and the overflow filter (see session.MergeTaintState for
// the bounds). Spans the list cuts stay held by the store and are added to
// its overflow filter, so the export never forgets one.
//
// Every per-step save calls it, and the set rarely changes between steps:
// an export is memoized against the store's version and the redaction
// registry generation, and each span's persisted form is computed once per
// generation, so an unchanged set costs a copy and a changed one redacts
// only its new spans. The result is the caller's own copy.
func (e *Engine) TaintSpanState(messages []session.Message) session.TaintState {
	if len(messages) > 0 && (e.dangerousCfg == nil || e.dangerousCfg.TaintEscalationEnabled()) {
		e.spanStore().observe(messages, currentTaintEnv())
	}
	e.taintMu.Lock()
	store := e.taintSpans
	e.taintMu.Unlock()
	if store == nil {
		return session.TaintState{}
	}
	// The generation is read before anything is redacted: a secret
	// registered while this export runs must not be stamped as applied.
	gen := redact.Generation()
	type entry struct {
		sp     session.TaintSpan
		seq    uint64
		key    string
		held   *taintSpan
		hashed bool
	}
	// The spans and the filter are read under one hold of the lock: an
	// eviction in between would move a span from the one not yet read into
	// the one already read.
	store.mu.Lock()
	if m := store.export; m != nil && m.version == store.version && m.gen == gen {
		st := cloneTaintState(m.state)
		store.mu.Unlock()
		return st
	}
	version := store.version
	entries := make([]entry, 0, len(store.spans)+len(store.hashed))
	sources := make(map[string]string)
	for _, table := range []struct {
		m      map[string]*taintSpan
		hashed bool
	}{{store.spans, false}, {store.hashed, true}} {
		for k, sp := range table.m {
			entries = append(entries, entry{sp.persistedLocked(k, table.hashed, gen, sources), sp.seq, k, sp, table.hashed})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq > entries[j].seq })
	if len(entries) > session.MaxTaintSpans {
		// Recording a cut span in the filter is part of this export (the
		// filter is copied below), so it leaves the version alone.
		for _, en := range entries[session.MaxTaintSpans:] {
			store.overflowLocked(en.key, en.held, en.hashed)
		}
		entries = entries[:session.MaxTaintSpans]
	}
	overflow := slices.Clone(store.overflow)
	store.mu.Unlock()
	spans := make([]session.TaintSpan, len(entries))
	for i, en := range entries {
		spans[i] = en.sp
	}
	st := session.MergeTaintState(session.TaintState{Spans: spans, Overflow: overflow}, session.TaintState{})
	store.mu.Lock()
	if store.version == version {
		store.export = &taintExport{state: st, version: version, gen: gen}
	}
	store.mu.Unlock()
	return cloneTaintState(st)
}

// cutTaintKey splits a taintKey back into kind and value.
func cutTaintKey(k string) (kind, value string, ok bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}
