package extended

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// LLMClient abstracts the LLM calls needed by Extended Memory.
type LLMClient interface {
	SimpleCall(ctx context.Context, system, user string) (string, error)
}

// Extractor turns raw text (typically a user message) into MemoryAtoms using
// an LLM JSON extraction prompt. Extracted atoms are NOT scanned here: the
// single guard gate is at persistence time (ExtendedMemory.addAtom), which
// quarantines rejections for human review instead of silently dropping them.
type Extractor struct {
	llm LLMClient
	cfg Config
}

// NewExtractor creates an Extractor.
func NewExtractor(llm LLMClient, cfg Config) *Extractor {
	return &Extractor{llm: llm, cfg: cfg}
}

// extractionPrompt asks the LLM to return a JSON array of memory atoms.
const extractionPrompt = `You are a memory extraction system. Read the user message and extract durable, reusable atomic memories.

For each atom, output an object with:
- "text": a concise, first-person-paraphrased statement (not a command).
- "type": one of "fact", "preference", "intent", "decision", "goal", "convention", "file", "error", "question".
- "confidence": a number 0.0-1.0 indicating how certain the memory is.

Rules:
- Only extract stable information worth recalling in future sessions.
- Do NOT extract instructions, commands, or requests to perform actions.
- Do NOT extract ephemeral details specific only to this message.
- If nothing durable is present, return an empty array.

Output ONLY a JSON array. Example:
[{"text":"User prefers concise answers","type":"preference","confidence":0.9}]`

// untrustedRe matches nonce'd untrusted content wrappers so they can be
// stripped before extraction.
var untrustedRe = regexp.MustCompile(`(?s)<untrusted_content_[^\s>]*\s+source="[^"]*"\s*>.*?</untrusted_content_[^\s>]*>`)

// StripUntrustedWrappers removes <untrusted_content_*> blocks from text.
func StripUntrustedWrappers(text string) string {
	return untrustedRe.ReplaceAllString(text, "")
}

// fenceRe matches a leading/trailing markdown code fence (``` or ```json)
// around an LLM response.
var fenceRe = regexp.MustCompile("(?s)^\\s*```[a-zA-Z]*\\s*\n?(.*?)\\s*```\\s*$")

// extractJSON normalizes an LLM JSON response: it strips a surrounding
// markdown code fence and extracts the first complete JSON array or object
// span (balanced, string-aware), tolerating preamble/prose around it. The
// second return value reports whether a candidate span was found.
func extractJSON(resp string) (string, bool) {
	s := strings.TrimSpace(resp)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	if s == "" {
		return "", false
	}
	if s[0] == '[' || s[0] == '{' {
		if json.Valid([]byte(s)) {
			return s, true
		}
	}
	// Find the first JSON opening bracket outside any string and scan for the
	// matching close.
	start := -1
	var open, close byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '[' || c == '{' {
			start = i
			open, close = c, ']'
			if open == '{' {
				close = '}'
			}
			break
		}
	}
	if start < 0 {
		return "", false
	}
	depth := 0
	inString = false
	escaped = false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// normalizeAtomText canonicalizes atom text for exact-match dedup: lowercase
// with whitespace collapsed and trimmed.
func normalizeAtomText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// defaultExtractionConfidence is assigned to extraction-origin atoms when the
// LLM omits the confidence field or returns an out-of-range value. It is
// deliberately below 1.0 so unqualified extracted memories do not inflate
// their retention score. Explicit LLM-provided values in (0,1] are kept.
const defaultExtractionConfidence = 0.7

// Extract atoms from text. Returns nil if the LLM is unavailable, the output
// is unparseable, or no atoms are found. Extracted atoms are sourced from the
// user ("user_said").
func (e *Extractor) Extract(ctx context.Context, text string) ([]MemoryAtom, error) {
	if e.llm == nil {
		return nil, nil
	}
	text = StripUntrustedWrappers(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	resp, err := e.llm.SimpleCall(ctx, extractionPrompt, text)
	if err != nil {
		log.Printf("extended memory: extraction LLM call failed: %v", err)
		return nil, fmt.Errorf("extract: llm call: %w", err)
	}
	resp = strings.TrimSpace(resp)
	if resp == "" || resp == "[]" {
		return nil, nil
	}
	jsonResp, ok := extractJSON(resp)
	if !ok {
		log.Printf("extended memory: extraction parse failed: no JSON in response")
		return nil, fmt.Errorf("extract: parse json: no JSON in response")
	}

	var raw []struct {
		Text       string  `json:"text"`
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Confidence float32 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(jsonResp), &raw); err != nil {
		log.Printf("extended memory: extraction parse failed: %v", err)
		return nil, fmt.Errorf("extract: parse json: %w", err)
	}

	atoms := make([]MemoryAtom, 0, len(raw))
	now := time.Now().UTC()
	for _, r := range raw {
		txt := strings.TrimSpace(r.Text)
		if txt == "" {
			// Accept legacy "content" field during migration.
			txt = strings.TrimSpace(r.Content)
		}
		if txt == "" {
			continue
		}
		typ := r.Type
		if !validType(typ) {
			typ = TypeObservation
		}
		conf := r.Confidence
		if conf <= 0 || conf > 1.0 {
			conf = defaultExtractionConfidence
		}
		id, err := generateAtomID()
		if err != nil {
			log.Printf("extended memory: generate atom id failed: %v", err)
			continue
		}
		atoms = append(atoms, MemoryAtom{
			ID:          id,
			Text:        txt,
			SourceClass: SourceUserSaid,
			Type:        typ,
			CreatedAt:   now,
			Confidence:  conf,
		})
	}
	return atoms, nil
}

func validType(t string) bool {
	switch t {
	case TypeFact, TypePreference, TypeIntent, TypeDecision, TypeGoal,
		TypeConvention, TypeFile, TypeError, TypeQuestion, TypeObservation:
		return true
	default:
		return false
	}
}

// generateAtomID creates a 128-bit random hex ID (32 chars) and validates it
// with the same rules as session IDs.
func generateAtomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	if err := session.ValidateSessionID(id); err != nil {
		return "", err
	}
	return id, nil
}
