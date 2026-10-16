package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// matches such entries by hashing each candidate.

type taintSeedKey struct{}

// WithTaintSpanSeed returns a context whose runs seed the engine's span
// store from spans (a session's persisted TaintSpans).
func WithTaintSpanSeed(ctx context.Context, spans []session.TaintSpan) context.Context {
	if len(spans) == 0 {
		return ctx
	}
	return context.WithValue(ctx, taintSeedKey{}, spans)
}

func taintSpanSeedFrom(ctx context.Context) []session.TaintSpan {
	if ctx == nil {
		return nil
	}
	spans, _ := ctx.Value(taintSeedKey{}).([]session.TaintSpan)
	return spans
}

func taintHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// seedLocked adds persisted spans the store does not hold yet.
func (s *taintSpanStore) seedLocked(spans []session.TaintSpan) {
	for _, sp := range spans {
		kind := taintSpanKind(sp.Kind)
		switch kind {
		case taintKindURL, taintKindHost, taintKindPath, taintKindToken:
		default:
			continue
		}
		table, value := s.spans, sp.Value
		if sp.Hash != "" {
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
			evictTaintTableLocked(table)
		}
		s.seq++
		table[k] = &taintSpan{source: sp.Source, seq: s.seq}
	}
}

// evictTaintTableLocked drops the least recently seen eighth of table.
func evictTaintTableLocked(table map[string]*taintSpan) {
	seqs := make([]uint64, 0, len(table))
	for _, sp := range table {
		seqs = append(seqs, sp.seq)
	}
	cutoff := eighthCutoff(seqs)
	for k, sp := range table {
		if sp.seq <= cutoff {
			delete(table, k)
		}
	}
}

// TaintSpanState records the spans of messages (the history being saved,
// so content that arrived after the last gate counts) and exports the
// engine's recorded spans, newest first, as
// session state (see session.MergeTaintSpans for the bounds).
func (e *Engine) TaintSpanState(messages []session.Message) []session.TaintSpan {
	if len(messages) > 0 && (e.dangerousCfg == nil || e.dangerousCfg.TaintEscalationEnabled()) {
		e.spanStore().observe(messages, currentTaintEnv())
	}
	e.taintMu.Lock()
	store := e.taintSpans
	e.taintMu.Unlock()
	if store == nil {
		return nil
	}
	store.mu.Lock()
	type entry struct {
		sp  session.TaintSpan
		seq uint64
	}
	var entries []entry
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
			entries = append(entries, entry{out, sp.seq})
		}
	}
	store.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq > entries[j].seq })
	spans := make([]session.TaintSpan, len(entries))
	for i, en := range entries {
		spans[i] = en.sp
	}
	return session.MergeTaintSpans(spans, nil)
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
