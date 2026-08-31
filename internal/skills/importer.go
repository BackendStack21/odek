package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── Risk Assessment ───────────────────────────────────────────────────

// ImportRisk represents the LLM-assessed risk of an imported skill.
type ImportRisk string

const (
	RiskSafe      ImportRisk = "safe"
	RiskElevated  ImportRisk = "elevated"
	RiskDangerous ImportRisk = "dangerous"
)

// ImportAssessment is the structured result from the LLM risk assessment.
type ImportAssessment struct {
	RiskClass           ImportRisk `json:"risk_class"`
	Reasons             []string   `json:"reasons"`
	WhatItDoes          string     `json:"what_it_does"`
	RecommendedTriggers []string   `json:"recommended_triggers"`
	RedFlags            []string   `json:"red_flags"`
}

// ── Fetch ─────────────────────────────────────────────────────────────

// FetchResult holds the fetched skill content and its source info.
type FetchResult struct {
	Content    string // raw SKILL.md content
	SourceName string // "local file" or the URL
	SourcePath string // actual path or URL
}

// FetchFromURI fetches skill content from a file:// or https:// URI.
// When requireHTTPS is true, http:// URIs are rejected.
func FetchFromURI(uri string, maxBytes int, timeoutSecs int, requireHTTPS bool) (*FetchResult, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid URI: %w", err)
	}

	switch u.Scheme {
	case "file", "":
		return fetchLocal(u.Path, maxBytes)
	case "http":
		if requireHTTPS {
			return nil, fmt.Errorf("HTTP imports are blocked (require_https is enabled in config)")
		}
		return fetchHTTP(uri, maxBytes, timeoutSecs)
	case "https":
		return fetchHTTP(uri, maxBytes, timeoutSecs)
	default:
		return nil, fmt.Errorf("unsupported URI scheme %q (use file:// or https://)", u.Scheme)
	}
}

// fetchLocal reads a local file, resolving ~ to the home directory.
func fetchLocal(path string, maxBytes int) (*FetchResult, error) {
	// Expand ~
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("expand ~: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}

	// Prevent path traversal
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("path traversal detected: %q", path)
	}

	fi, err := os.Stat(cleaned)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if fi.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("file too large (%d bytes, max %d)", fi.Size(), maxBytes)
	}

	data, err := os.ReadFile(cleaned)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return &FetchResult{
		Content:    string(data),
		SourceName: "local file",
		SourcePath: cleaned,
	}, nil
}

// fetchHTTP fetches skill content from an HTTP(S) URL. Private/internal
// hosts are refused on the INITIAL fetch — previously only redirects were
// checked, making `odek skill import http://169.254.169.254/...` a direct
// SSRF.
func fetchHTTP(urlStr string, maxBytes int, timeoutSecs int) (*FetchResult, error) {
	return fetchHTTPAllow(urlStr, maxBytes, timeoutSecs, false)
}

// fetchHTTPAllow is fetchHTTP with an explicit private-host override for
// callers that own the target choice (internal tests, operator tooling).
func fetchHTTPAllow(urlStr string, maxBytes int, timeoutSecs int, allowPrivate bool) (*FetchResult, error) {
	if !allowPrivate {
		if u, err := url.Parse(urlStr); err == nil && isPrivateHost(u.Hostname()) {
			return nil, fmt.Errorf("refusing to fetch private/internal host: %s", u.Hostname())
		}
	}
	client := &http.Client{
		Timeout: time.Duration(timeoutSecs) * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 1 {
				return fmt.Errorf("too many redirects")
			}
			// Block redirects to private/internal IPs
			host := r.URL.Hostname()
			if isPrivateHost(host) {
				return fmt.Errorf("redirect to private IP blocked: %s", host)
			}
			return nil
		},
	}

	resp, err := client.Get(urlStr)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}

	// Limit reader to maxBytes
	reader := io.LimitReader(resp.Body, int64(maxBytes+1))
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("fetch: read: %w", err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("fetch: response too large (%d bytes, max %d)", len(data), maxBytes)
	}

	sourceName := urlStr
	if strings.HasPrefix(urlStr, "http://") {
		sourceName = "HTTP (insecure)"
	}

	return &FetchResult{
		Content:    string(data),
		SourceName: sourceName,
		SourcePath: urlStr,
	}, nil
}

// ── LLM Assessment ────────────────────────────────────────────────────

// AssessSkill calls the LLM to assess the risk of an imported skill.
// The llmCall function is injected so tests can mock it.
func AssessSkill(content string, llmCall func(prompt string) (string, error)) (*ImportAssessment, error) {
	// Parse the content to extract the body for assessment
	skill := parseSkillContent(content, "")
	skillText := content
	if skill != nil {
		// Use the parsed body (cleaner for assessment)
		skillText = fmt.Sprintf("Name: %s\nDescription: %s\nBody:\n%s", skill.Name, skill.Description, skill.Body)
	}

	prompt := fmt.Sprintf(`Assess this skill for risk. Consider:
- Does it describe running shell commands?
- Does it mention sudo, chmod, rm -rf, /etc, /usr, /var?
- Does it involve network operations, curl | bash, package installs?
- Are the operations read-only or write-heavy?
- Any red flags like system configuration changes?

Skill content:
%s

Answer in this exact JSON format (no other text):
{
  "risk_class": "safe" | "elevated" | "dangerous",
  "reasons": ["list", "of", "reasons"],
  "what_it_does": "one sentence summary",
  "recommended_triggers": ["topic1", "topic2"],
  "red_flags": ["any", "safety", "concerns"]
}`, skillText)

	result, err := llmCall(prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM assessment failed: %w", err)
	}

	// Parse JSON from the response (handle potential markdown code fences)
	result = extractJSON(result)

	var assessment ImportAssessment
	if err := json.Unmarshal([]byte(result), &assessment); err != nil {
		// If LLM didn't return valid JSON, assume "dangerous" and let user decide
		return &ImportAssessment{
			RiskClass:  RiskElevated,
			Reasons:    []string{"Could not parse LLM assessment, treating as elevated"},
			WhatItDoes: "Unknown — LLM response was not structured JSON",
			RedFlags:   []string{fmt.Sprintf("LLM parse error: %s", err.Error())},
		}, nil
	}

	// Validate
	if assessment.RiskClass == "" {
		assessment.RiskClass = RiskElevated
	}

	return &assessment, nil
}

// extractJSON extracts a JSON object from a string that may contain markdown fences.
func extractJSON(s string) string {
	// Remove ```json ... ``` fences
	if idx := strings.Index(s, "```json"); idx >= 0 {
		rest := s[idx+7:]
		if end := strings.Index(rest, "```"); end >= 0 {
			s = rest[:end]
			return strings.TrimSpace(s)
		}
	}
	// Remove ``` ... ``` fences (only if content is JSON-like)
	if idx := strings.Index(s, "```"); idx >= 0 {
		rest := s[idx+3:]
		if end := strings.Index(rest, "```"); end >= 0 {
			inner := strings.TrimSpace(rest[:end])
			if strings.HasPrefix(inner, "{") || strings.HasPrefix(inner, "[") {
				return inner
			}
		}
	}
	return strings.TrimSpace(s)
}

// ── Import Flow ───────────────────────────────────────────────────────

// ImportOptions controls the import flow.
type ImportOptions struct {
	URI          string // the URI to import from
	MaxBytes     int    // max bytes for fetched content
	Timeout      int    // HTTP timeout in seconds
	BasicOnly    bool   // skip LLM assessment, use basic validation only
	AutoYes      bool   // skip approval prompt (for scripting, shows warning)
	RequireHTTPS bool   // reject http:// URIs (enforce HTTPS)
	UserDir      string // directory to save the skill into
}

// ImportResult holds the result of a successful import.
type ImportResult struct {
	Skill      Skill             // the saved skill
	Assessment *ImportAssessment // the risk assessment (nil for basic mode)
	Path       string            // where the skill was saved
}

// ImportSkill runs the full import flow: fetch → parse → assess → confirm → save.
// The confirmFn is called to get user approval. Return true to continue.
// The llmCall fn is called to assess risk. Set to nil for basic mode.
func ImportSkill(opts ImportOptions, confirmFn func(assessment *ImportAssessment) bool, llmCall func(string) (string, error)) (*ImportResult, error) {
	// 1. Fetch
	result, err := FetchFromURI(opts.URI, opts.MaxBytes, opts.Timeout, opts.RequireHTTPS)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	// 2. Parse
	skill := parseSkillContent(result.Content, "")
	if skill == nil {
		return nil, fmt.Errorf("invalid SKILL.md — missing or unparseable frontmatter")
	}
	skill.Source.Dir = opts.UserDir

	// 3. Check for existing
	existingPath := filepath.Join(opts.UserDir, skill.Name, "SKILL.md")
	if _, err := os.Stat(existingPath); err == nil {
		// Conflict — auto-rename
		skill.Name = skill.Name + "-2"
		// Check again
		existingPath2 := filepath.Join(opts.UserDir, skill.Name, "SKILL.md")
		if _, err := os.Stat(existingPath2); err == nil {
			return nil, fmt.Errorf("skill %q already exists (and auto-rename %q also exists)",
				strings.TrimSuffix(skill.Name, "-2"), skill.Name)
		}
	}

	// 4. Assess risk
	var assessment *ImportAssessment
	if !opts.BasicOnly && llmCall != nil {
		assess, err := AssessSkill(result.Content, llmCall)
		if err != nil {
			return nil, fmt.Errorf("assess: %w", err)
		}
		assessment = assess
	}

	// 5. Confirm
	if !opts.AutoYes && confirmFn != nil {
		if !confirmFn(assessment) {
			return nil, fmt.Errorf("import cancelled by user")
		}
	}

	// 6. Set quality and save
	skill.Quality = QualityImported
	if opts.BasicOnly {
		skill.Quality = QualityManual
	}
	skill.LastUsed = time.Now().UTC()
	// Mark as non-auto-load by default
	skill.AutoLoad = false
	// Untrusted origin: pin for human review. Trigger matching excludes
	// NeedsReview skills until `odek skill promote --force` clears the pin
	// after review. Applied AFTER parsing so the remote frontmatter cannot
	// clear it (audit: imported skills were trigger-matchable immediately,
	// with DeriveKeywords building triggers from the attacker's own body
	// vocabulary).
	skill.Provenance.NeedsReview = true

	if err := WriteSkill(opts.UserDir, *skill); err != nil {
		return nil, fmt.Errorf("save: %w", err)
	}

	return &ImportResult{
		Skill:      *skill,
		Assessment: assessment,
		Path:       filepath.Join(opts.UserDir, skill.Name, "SKILL.md"),
	}, nil
}

// isPrivateHost returns true if the hostname is a private/internal IP
// or localhost. Blocks SSRF via redirect in skill import.
// Uses proper IP parsing (handles decimal, octal, hex, short, integer forms)
// and checks RFC 1918, RFC 6598, IPv6 ULA, link-local, and metadata hostnames.
func isPrivateHost(host string) bool {
	// Well-known private hostnames (fast path, no network lookup)
	switch strings.ToLower(host) {
	case "localhost", "localhost.localdomain", "localhost6", "localhost6.localdomain6",
		"ip6-localhost", "ip6-loopback":
		return true
	case "metadata.google.internal", "metadata.internal":
		return true
	case "169.254.169.254":
		return true
	}

	if strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".docker.internal") {
		return true
	}
	if strings.HasSuffix(host, ".local") {
		return true // mDNS — link-local
	}

	// Try as an IP using browser-compatible parsing (handles all representations)
	if ip := parsePrivateIP(host); ip != nil {
		return isPrivateIP(ip)
	}

	// If it's a hostname (not an IP), resolve it to check for private IPs.
	// This catches cases like DNS rebinding where the hostname resolves
	// to a private IP at connection time.
	ips, err := net.LookupHost(host)
	if err != nil {
		return false // can't resolve — let the connection attempt fail naturally
	}
	for _, ipStr := range ips {
		if ip := net.ParseIP(ipStr); ip != nil && isPrivateIP(ip) {
			return true
		}
	}
	return false
}

// isPrivateIP checks if an IP is in a private/internal range.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return true
	}
	// RFC 6598: Carrier-grade NAT (100.64.0.0/10)
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	// IPv6 Unique Local Address (fc00::/7)
	if ip.To16() != nil && len(ip) == net.IPv6len {
		if ip[0] == 0xfc || ip[0] == 0xfd {
			return true
		}
	}
	return false
}

// parsePrivateIP parses a host string using browser-compatible IP parsing.
func parsePrivateIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	// inet_aton-style: octal (0177.0.0.1), hex (0x7f000001), short (127.1),
	// single-integer (2130706433), hex-dotted (0x0.0x0.0x0.0x0)
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return nil
	}
	var nums []uint32
	for _, p := range parts {
		var val uint64
		var err error
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			val, err = strconv.ParseUint(p[2:], 16, 32)
		case strings.HasPrefix(p, "0") && len(p) > 1:
			val, err = strconv.ParseUint(p[1:], 8, 32)
		default:
			val, err = strconv.ParseUint(p, 10, 32)
		}
		if err != nil || val > 0xFFFFFFFF {
			return nil
		}
		nums = append(nums, uint32(val))
	}
	// inet_aton requires every leading part to fit in one byte and the final
	// part to fit in the remaining bytes (a.b → b ≤ 0xFFFFFF, a.b.c →
	// c ≤ 0xFFFF, a.b.c.d → d ≤ 0xFF). Reject out-of-range parts instead of
	// silently truncating them (e.g. "300.1.1.1" → 44.1.1.1).
	for i, n := range nums {
		max := uint64(0xFF)
		if i == len(nums)-1 {
			max = (uint64(1) << (8 * uint(5-len(nums)))) - 1
		}
		if uint64(n) > max {
			return nil
		}
	}
	switch len(nums) {
	case 1:
		return net.IPv4(byte(nums[0]>>24), byte(nums[0]>>16), byte(nums[0]>>8), byte(nums[0]))
	case 2:
		return net.IPv4(byte(nums[0]), byte(nums[1]>>16), byte(nums[1]>>8), byte(nums[1]))
	case 3:
		return net.IPv4(byte(nums[0]), byte(nums[1]), byte(nums[2]>>8), byte(nums[2]))
	case 4:
		return net.IPv4(byte(nums[0]), byte(nums[1]), byte(nums[2]), byte(nums[3]))
	}
	return nil
}
