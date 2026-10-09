package loop

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
)

// maxMintedBoundaries bounds the in-process registry of engine-minted
// boundaries. At the cap the least recently used half is evicted; the only
// cost of eviction is that an older message is re-wrapped on its next run.
const maxMintedBoundaries = 16384

// mintedKey keys the registry digests. It is random per process, so a party
// that can write a session file cannot precompute content whose digest
// collides with a minted boundary.
var mintedKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("loop: minted-boundary key: " + err.Error())
	}
	return k
}()

// mintedDigest identifies content in the registry: HMAC-SHA256 under the
// per-process key. It is a content-identity digest, not a password hash.
func mintedDigest(content string) [32]byte {
	m := hmac.New(sha256.New, mintedKey)
	m.Write([]byte(content))
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}

// mintedBoundaries holds keyed digests (mintedDigest) of untrusted-content boundaries the
// engine produced in this process, each with a last-use sequence number. A
// syntactically valid wrapper is not evidence of provenance — anyone who can
// write a session file or a tool result can produce one — so persisted
// system messages pass through unchanged only when their exact bytes were
// minted here. Package-level so engines rebuilt per turn (serve, Telegram)
// recognise each other's output.
var mintedBoundaries = struct {
	sync.Mutex
	m   map[[32]byte]uint64
	seq uint64
}{m: make(map[[32]byte]uint64)}

// recordMintedBoundary registers content as produced by this engine process.
func recordMintedBoundary(content string) {
	if content == "" {
		return
	}
	h := mintedDigest(content)
	mintedBoundaries.Lock()
	defer mintedBoundaries.Unlock()
	if _, ok := mintedBoundaries.m[h]; !ok && len(mintedBoundaries.m) >= maxMintedBoundaries {
		evictOldestMintedLocked()
	}
	mintedBoundaries.seq++
	mintedBoundaries.m[h] = mintedBoundaries.seq
}

// evictOldestMintedLocked drops the least recently used half of the
// registry: every entry whose last use is older than the median.
func evictOldestMintedLocked() {
	seqs := make([]uint64, 0, len(mintedBoundaries.m))
	for _, s := range mintedBoundaries.m {
		seqs = append(seqs, s)
	}
	slices.Sort(seqs)
	cutoff := seqs[len(seqs)/2]
	for h, s := range mintedBoundaries.m {
		if s < cutoff {
			delete(mintedBoundaries.m, h)
		}
	}
}

// isEngineMinted reports whether content is byte-identical to a boundary
// this process minted, refreshing its recency when it is.
func isEngineMinted(content string) bool {
	if content == "" {
		return false
	}
	h := mintedDigest(content)
	mintedBoundaries.Lock()
	defer mintedBoundaries.Unlock()
	if _, ok := mintedBoundaries.m[h]; !ok {
		return false
	}
	mintedBoundaries.seq++
	mintedBoundaries.m[h] = mintedBoundaries.seq
	return true
}

// unwrapForeignBoundary returns the body of a complete, syntactically valid
// wrapper so the engine can re-wrap it under its own nonce and source; any
// other content is returned unchanged. The foreign open tag — including its
// source attribute — is discarded, and the engine's wrapper neutralises any
// untrusted_content literal left in the body. Unwrapping rather than nesting
// keeps a resumed session from gaining one wrapper layer per restart.
func unwrapForeignBoundary(content string) string {
	if !isFullyWrappedUntrusted(content) {
		return content
	}
	trimmed := strings.TrimSpace(content)
	openEnd := strings.IndexByte(trimmed, '>')
	closeStart := strings.LastIndex(trimmed, "</untrusted_content_")
	if openEnd < 0 || closeStart <= openEnd {
		return content
	}
	body := trimmed[openEnd+1 : closeStart]
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimSuffix(body, "\n")
	return body
}

// protectPersistedContext wraps context that is persisted and read back on
// resume (digest, plan body, other persisted system messages). It records
// the audit ingest but deliberately bypasses the surface wrapper: a surface
// guard would rescan on every restart and prepend another warning banner
// each time, growing the message and breaking the strict plan parser. The
// stable wrapper makes re-wrapping idempotent across restarts.
func (e *Engine) protectPersistedContext(ctx context.Context, source, content string) string {
	if content == "" {
		return ""
	}
	if fn := IngestRecorderFrom(ctx); fn != nil {
		fn(source, content)
	}
	wrapped := stableUntrustedWrap(source, content)
	recordMintedBoundary(wrapped)
	return wrapped
}

// stableUntrustedWrap is defaultUntrustedWrap with a nonce derived from the
// neutralised body instead of a random one, so wrapping the body of its own
// output reproduces the same bytes. The nonce need not be secret: every
// untrusted_content literal in the body is neutralised first, so the body
// can never contain the closing tag whatever the nonce is.
func stableUntrustedWrap(source, content string) string {
	source = strings.NewReplacer(`"`, "″", "<", "‹", ">", "›", "\n", " ", "\r", " ").Replace(source)
	content = strings.ReplaceAll(content, "untrusted_content", "untrusted·content")
	f := fnv.New128a()
	f.Write([]byte(source + "\x00" + content))
	nonce := hex.EncodeToString(f.Sum(nil)[:8])
	return "<untrusted_content_" + nonce + ` source="` + source + "\">" +
		"\n" + content + "\n</untrusted_content_" + nonce + ">"
}
