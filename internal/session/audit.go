// Package session — audit subsystem.
//
// The audit log records every time the agent ingested externally-
// sourced content (a fetched page, a file outside the working
// directory, an MCP tool response, etc.) along with a heuristic
// `suspicious_divergence` flag for turns where the agent's tool calls
// reference resources that did not appear in the user's preceding
// message. Together they let a user retroactively spot prompt-
// injection attempts that influenced an agent run.
//
// The audit log is local-only; nothing in this package transmits or
// uploads it.

package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/fsatomic"
)

// AuditIngest records that the agent ingested an untrusted-source
// content blob at the given turn.
type AuditIngest struct {
	Turn        int       `json:"turn"`
	Source      string    `json:"source"`              // e.g. "https://x", "/abs/path", "mcp:server:tool"
	ContentHash string    `json:"content_hash"`        // sha256 of the ingested body (first 16 hex)
	Resources   []string  `json:"resources,omitempty"` // bounded resource indicators for divergence correlation
	At          time.Time `json:"at"`
}

// AuditTurn records the per-turn divergence assessment.
type AuditTurn struct {
	Turn                 int      `json:"turn"`
	UserMessage          string   `json:"user_message"`
	ToolCalls            []string `json:"tool_calls"`                    // names of tools called this turn
	NovelResources       []string `json:"novel_resources,omitempty"`     // resources referenced by tools but not by user
	UntrustedResources   []string `json:"untrusted_resources,omitempty"` // resources from untrusted content that were later referenced
	IngestedUntrusted    bool     `json:"ingested_untrusted"`
	SuspiciousDivergence bool     `json:"suspicious_divergence"`
}

// AuditLog is the per-session aggregate.
type AuditLog struct {
	SessionID string        `json:"session_id"`
	Ingests   []AuditIngest `json:"ingests"`
	Turns     []AuditTurn   `json:"turns"`
}

// AuditStore manages per-session audit logs under <dir>/audit/.
type AuditStore struct {
	mu  sync.Mutex
	dir string
}

// NewAuditStore returns a store rooted at dir; the audit subdir is
// created on first write.
func NewAuditStore(dir string) *AuditStore {
	return &AuditStore{dir: filepath.Join(dir, "audit")}
}

func boundedAuditResources(content string) []string {
	resources := ResourcesIn(content)
	if len(resources) > 64 {
		resources = resources[:64]
	}
	for i := range resources {
		if len(resources[i]) > 512 {
			resources[i] = resources[i][:512]
		}
	}
	return resources
}

// auditReadError marks a failed audit-log READ (permissions, I/O) as distinct
// from a corrupt-log unmarshal failure. Reads must abort mutations so a
// transient error cannot cause the next save to overwrite history.
type auditReadError struct{ err error }

func (e auditReadError) Error() string { return e.err.Error() }
func (e auditReadError) Unwrap() error { return e.err }

// RecordIngest appends an ingest entry for a session. The log is stored
// as append-only JSONL (one record per line) so an ingest never rewrites
// existing history; legacy whole-file JSON logs are migrated on first
// write.
func (s *AuditStore) RecordIngest(sessionID string, turn int, source, content string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(content))
	rec := AuditIngest{
		Turn:        turn,
		Source:      source,
		ContentHash: hex.EncodeToString(sum[:8]),
		Resources:   boundedAuditResources(content),
		At:          time.Now().UTC(),
	}
	return s.appendRecord(sessionID, auditRecord{Type: "ingest", Ingest: &rec})
}

// RecordTurn appends a turn assessment for a session.
func (s *AuditStore) RecordTurn(sessionID string, turn AuditTurn) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	return s.appendRecord(sessionID, auditRecord{Type: "turn", Turn: &turn})
}

// auditRecord is one JSONL line: a typed envelope around the per-record
// payloads. Exactly one payload field is set per record.
type auditRecord struct {
	Type   string      `json:"type"`
	Turn   *AuditTurn  `json:"turn,omitempty"`
	Ingest *AuditIngest `json:"ingest,omitempty"`
}

// Load returns the audit log for a session, or empty if not present.
func (s *AuditStore) Load(sessionID string) (AuditLog, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return AuditLog{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(sessionID)
}

// appendRecord appends one JSONL record to the session's audit log,
// migrating legacy whole-file JSON on the way. Reads abort the append on
// transient I/O errors; a torn trailing line (crash mid-append) is
// salvaged by rewriting only the damaged tail.
//
// Durability note: appends are O_APPEND writes without fsync — a process
// crash cannot tear the OS buffer (line writes are single small appends),
// but an OS crash may lose the most recent records. The JSONL form
// preserves the full prior trail either way; this is the same trade-off
// the events stream documents for its group-commit window.
func (s *AuditStore) appendRecord(sessionID string, rec auditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, sessionID+".json")

	// Probe the existing file. A read failure other than NotExist must
	// surface so a transient error cannot cause history loss.
	legacy := false
	if fi, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			err := auditReadError{err}
			diagnostics.Report("audit", "read", sessionID, err)
			return err
		}
	} else if fi.Mode()&os.ModeSymlink != 0 {
		// A symlink planted at the log path must never be followed: write
		// the record via atomic rename, which replaces the directory entry
		// with a fresh regular file and leaves the symlink's target intact.
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := fsatomic.WriteFile(path, append(line, '\n'), 0600); err != nil {
			diagnostics.Report("audit", "save", sessionID, err)
			return err
		}
		return nil
	} else if fi.IsDir() {
		err := auditReadError{errAuditUnreadableTarget}
		diagnostics.Report("audit", "read", sessionID, err)
		return err
	} else if data, err := os.ReadFile(path); err == nil {
		legacy = isLegacyAuditJSON(data)
		if !legacy {
			// Validate the JSONL tail. A torn LAST line (crash mid-append)
			// is salvageable: appending after it keeps the valid prefix and
			// Load skips the fragment. Corruption anywhere else (or a
			// wholly unparseable file) is forensic evidence — preserve it
			// aside and start a fresh log rather than append behind it.
			if corrupted := corruptJSONLIndex(data); corrupted {
				s.preserveCorruptLocked(sessionID)
			}
		}
	} else {
		// Read of an existing file failed (permissions, I/O): abort rather
		// than risk clobbering history.
		err := auditReadError{err}
		diagnostics.Report("audit", "read", sessionID, err)
		return err
	}

	if legacy {
		// Legacy pretty-printed file: migrate by decoding and re-emitting
		// every record as JSONL (atomic replace), then append the new one.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var old AuditLog
		if err := json.Unmarshal(data, &old); err != nil {
			s.preserveCorruptLocked(sessionID)
		} else {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			writeRecords := func() error {
				for i := range old.Ingests {
					if err := enc.Encode(auditRecord{Type: "ingest", Ingest: &old.Ingests[i]}); err != nil {
						return err
					}
				}
				for i := range old.Turns {
					if err := enc.Encode(auditRecord{Type: "turn", Turn: &old.Turns[i]}); err != nil {
						return err
					}
				}
				return enc.Encode(rec)
			}
			if err := writeRecords(); err != nil {
				return err
			}
			if err := fsatomic.WriteFile(path, buf.Bytes(), 0600); err != nil {
				diagnostics.Report("audit", "save", sessionID, err)
				return err
			}
			return nil
		}
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	// Repair a torn tail (crash mid-append left a fragment with no final
	// newline): start the new record on a fresh line so it stays parseable.
	if tail, err := os.ReadFile(path); err == nil && len(tail) > 0 && tail[len(tail)-1] != '\n' {
		last := bytes.TrimSpace(bytes.Split(tail, []byte("\n"))[len(bytes.Split(tail, []byte("\n")))-1])
		var probe auditRecord
		if json.Unmarshal(last, &probe) != nil {
			line = append([]byte{'\n'}, line...)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		diagnostics.Report("audit", "append", sessionID, err)
		return err
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		diagnostics.Report("audit", "append", sessionID, err)
		return err
	}
	if err := f.Close(); err != nil {
		diagnostics.Report("audit", "append", sessionID, err)
		return err
	}
	return nil
}

// errAuditUnreadableTarget marks a directory planted at the audit-log
// path — writes must refuse instead of truncating.
var errAuditUnreadableTarget = errors.New("audit log path is a symlink or directory")

// corruptJSONLIndex reports whether a JSONL audit log is corrupt beyond a
// salvageable torn tail. A torn LAST line (a crash mid-append) is expected
// and salvageable; invalid lines anywhere else — or no valid line at all —
// mean the log is forensic evidence and must be preserved aside.
func corruptJSONLIndex(data []byte) bool {
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	invalid, valid := 0, 0
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec auditRecord
		if json.Unmarshal(line, &rec) != nil || (rec.Type != "ingest" && rec.Type != "turn") {
			invalid++
		} else {
			valid++
		}
	}
	if invalid == 0 {
		return false
	}
	// Only the trailing fragment may be invalid, and only when a valid
	// prefix exists to salvage. The last line is salvageable only when it
	// is not itself a well-formed typed record (a parseable line with an
	// unknown type is foreign data, not a torn tail).
	if valid > 0 && invalid == 1 {
		last := bytes.TrimSpace(lines[len(lines)-1])
		var rec auditRecord
		if json.Unmarshal(last, &rec) != nil {
			return false // unparseable tail fragment: torn, salvageable
		}
		return rec.Type != "ingest" && rec.Type != "turn"
	}
	return true
}

// isLegacyAuditJSON reports whether data is a legacy whole-file audit log
// (a JSON object) rather than the append-only JSONL form (one typed record
// per line). A JSONL record also starts with '{', so the discriminator is
// the typed envelope: JSONL lines carry "type":"ingest"|"turn" while the
// legacy object carries the top-level "session_id" key.
func isLegacyAuditJSON(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return false
	}
	firstLine := trimmed
	if i := bytes.IndexByte(trimmed, '\n'); i >= 0 {
		firstLine = trimmed[:i]
	}
	if bytes.Contains(firstLine, []byte(`"type":"ingest"`)) || bytes.Contains(firstLine, []byte(`"type":"turn"`)) {
		return false
	}
	return bytes.Contains(trimmed, []byte(`"session_id"`))
}

func (s *AuditStore) loadLocked(sessionID string) (AuditLog, error) {
	path := filepath.Join(s.dir, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AuditLog{SessionID: sessionID}, nil
		}
		// Any other read failure (permissions, I/O) must surface: treating
		// it as "no history yet" would let a transient error silently
		// rewrite the audit trail on the next save.
		return AuditLog{SessionID: sessionID}, auditReadError{err}
	}

	if isLegacyAuditJSON(data) {
		var log AuditLog
		if err := json.Unmarshal(data, &log); err != nil {
			return AuditLog{SessionID: sessionID}, err
		}
		return log, nil
	}

	// JSONL form: decode line by line. A torn final line (crash mid-append)
	// is skipped rather than failing the whole log; the last complete
	// record boundary defines the history.
	log := AuditLog{SessionID: sessionID}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec auditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // torn or corrupt tail — keep the salvaged prefix
		}
		switch rec.Type {
		case "ingest":
			if rec.Ingest != nil {
				log.Ingests = append(log.Ingests, *rec.Ingest)
			}
		case "turn":
			if rec.Turn != nil {
				log.Turns = append(log.Turns, *rec.Turn)
			}
		}
	}
	return log, nil
}

// preserveCorruptLocked moves an unparseable audit log aside instead of
// letting the next save silently destroy it. The audit log is the only
// post-hoc evidence of prompt injection; a single truncated write used to
// erase the whole prior trail (2026-08 audit). Best-effort — if the rename
// fails the fresh log still overwrites, matching the old behaviour.
func (s *AuditStore) preserveCorruptLocked(sessionID string) {
	path := filepath.Join(s.dir, sessionID+".json")
	side := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000")
	_ = os.Rename(path, side)
}

// ── Divergence heuristic ─────────────────────────────────────────────

// reResource matches strings that look like a resource the agent might
// act on: URLs, absolute paths, dotted file extensions, command names.
// The heuristic compares resources referenced by tool calls against
// the user's preceding message; anything novel is suspicious when the
// session has ingested untrusted content this turn.
//
// Optional surrounding quotes are captured so JSON-encoded tool arguments
// (e.g. {"path":"README.md"}) are inspected correctly.
var reResource = regexp.MustCompile(`(?i)["']?(https?://[^\s'"<>]+|/[A-Za-z0-9_./-]{2,}|[A-Za-z0-9_-]+\.[A-Za-z]{2,5})["']?`)

// ResourcesIn returns the set of resource-like tokens found in text.
func ResourcesIn(text string) []string {
	matches := reResource.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		res := strings.TrimRight(m[1], ".,);")
		if seen[res] || res == "" {
			continue
		}
		seen[res] = true
		out = append(out, res)
	}
	return out
}

// NovelResources returns resources from toolText that do not appear in
// userText. Order preserved; case-insensitive comparison.
func NovelResources(userText string, toolText string) []string {
	userSet := make(map[string]bool)
	for _, r := range ResourcesIn(userText) {
		userSet[strings.ToLower(r)] = true
	}
	var novel []string
	for _, r := range ResourcesIn(toolText) {
		if !userSet[strings.ToLower(r)] {
			novel = append(novel, r)
		}
	}
	return novel
}
