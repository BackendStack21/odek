package session

import (
	"encoding/binary"
	"hash/maphash"
)

// maxTaintMemos bounds the per-session taint-scan memo.
const maxTaintMemos = 1024

// taintMemo records which message prefix a committed save already scanned
// for both taint flags (UntrustedIngested, EpisodeUntrusted). It is anchored
// to the exact revision that save wrote and to a keyed digest of every
// taint-relevant field of every message in the prefix, so a snapshot that
// rewrites any earlier message — not only the last one — is rescanned whole.
// Two prefixes are kept: the messages as scanned (what an in-loop persist
// snapshot hands back next step) and as written (redacted, size-trimmed;
// what a caller reusing the saved struct hands back).
type taintMemo struct {
	generation string
	revision   uint64
	scannedN   int
	scanned    uint64
	writtenN   int
	written    uint64
}

var taintSeed = maphash.MakeSeed()

// taintPrefixHash digests the fields the taint scans read (role, name,
// content, tool-call id/name/arguments) of msgs[:n], length-prefixed so
// field boundaries cannot be shifted.
func taintPrefixHash(msgs []Message, n int) uint64 {
	var h maphash.Hash
	h.SetSeed(taintSeed)
	var lenBuf [8]byte
	str := func(s string) {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(s)))
		h.Write(lenBuf[:])
		h.WriteString(s)
	}
	for i := 0; i < n; i++ {
		m := &msgs[i]
		str(m.Role)
		str(m.Name)
		str(m.Content)
		str(m.ToolCallID)
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(m.ToolCalls)))
		h.Write(lenBuf[:])
		for _, tc := range m.ToolCalls {
			str(tc.ID)
			str(tc.Function.Name)
			str(tc.Function.Arguments)
		}
	}
	return h.Sum64()
}

// taintScanFrom returns the index from which msgs must be scanned for taint.
// It is nonzero only when current is the revision this store committed with
// a memo and msgs begins with a prefix that save scanned or wrote unchanged;
// that save's flags are already ORed in from current. Anything else (another
// writer, a symlink alias, a rewritten earlier message) scans everything.
func (s *Store) taintScanFrom(id string, current *Session, msgs []Message) int {
	if current == nil {
		return 0
	}
	memo, ok := s.taintMemos[id]
	if !ok || memo.generation != current.Generation || memo.revision != current.Revision {
		return 0
	}
	best := 0
	if memo.scannedN > 0 && memo.scannedN <= len(msgs) && taintPrefixHash(msgs, memo.scannedN) == memo.scanned {
		best = memo.scannedN
	}
	if memo.writtenN > best && memo.writtenN <= len(msgs) && taintPrefixHash(msgs, memo.writtenN) == memo.written {
		best = memo.writtenN
	}
	return best
}

// rememberTaintScan records the prefixes a committed save scanned and wrote.
func (s *Store) rememberTaintScan(sess *Session, scanned []Message) {
	if s.taintMemos == nil || len(s.taintMemos) >= maxTaintMemos {
		s.taintMemos = make(map[string]taintMemo)
	}
	s.taintMemos[sess.ID] = taintMemo{
		generation: sess.Generation,
		revision:   sess.Revision,
		scannedN:   len(scanned),
		scanned:    taintPrefixHash(scanned, len(scanned)),
		writtenN:   len(sess.Messages),
		written:    taintPrefixHash(sess.Messages, len(sess.Messages)),
	}
}
