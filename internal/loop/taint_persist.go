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
	}
}

// seedLocked adds persisted spans the store does not hold yet. A full table
// evicts into the overflow filter, as recording does.
func (s *taintSpanStore) seedLocked(spans []session.TaintSpan) {
	for _, sp := range spans {
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
		if len(table) >= maxTaintSpans {
			s.evictTableLocked(table, hashed)
		}
		s.seq++
		table[k] = &taintSpan{source: sp.Source, seq: s.seq}
	}
}

// TaintSpanState records the spans of messages (the history being saved,
// so content that arrived after the last gate counts) and exports the
// engine's span state: the newest session.MaxTaintSpans recorded spans,
// newest first, and the overflow filter (see session.MergeTaintState for
// the bounds). Spans the list cuts stay held by the store and are added to
// its overflow filter, so the export never forgets one.
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
	type entry struct {
		sp     session.TaintSpan
		seq    uint64
		key    string
		held   *taintSpan
		hashed bool
	}
	var entries []entry
	// The spans and the filter are read under one hold of the lock: an
	// eviction in between would move a span from the one not yet read into
	// the one already read.
	store.mu.Lock()
	for _, table := range []struct {
		m      map[string]*taintSpan
		hashed bool
	}{{store.spans, false}, {store.hashed, true}} {
		for k, sp := range table.m {
			kind, value, _ := cutTaintKey(k)
			out := session.TaintSpan{Kind: kind, Source: redact.RedactSecrets(sp.source)}
			switch {
			case table.hashed:
				out.Hash = value
			case kind == string(taintKindToken) || len(value) > session.MaxTaintSpanValueBytes || redact.RedactSecrets(value) != value:
				out.Hash = taintHash(value)
			default:
				out.Value = value
			}
			entries = append(entries, entry{out, sp.seq, k, sp, table.hashed})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq > entries[j].seq })
	if len(entries) > session.MaxTaintSpans {
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
	return session.MergeTaintState(session.TaintState{Spans: spans, Overflow: overflow}, session.TaintState{})
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
