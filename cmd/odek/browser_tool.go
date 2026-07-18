package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Regex helpers for HTML parsing (zero-dep) ─────────────────────────

var (
	reTitle            = regexp.MustCompile(`<title[^>]*>([^<]*)</title>`)
	reLink             = regexp.MustCompile(`<a\s[^>]*href\s*=\s*"([^"]*)"[^>]*>([^<]*)</a>`)
	reButton           = regexp.MustCompile(`<button[^>]*>([^<]*)</button>`)
	reInput            = regexp.MustCompile(`<input\s[^>]*type\s*=\s*"(?:submit|button|text|search)"[^>]*>`)
	reInputVal         = regexp.MustCompile(`value\s*=\s*"([^"]*)"`)
	reInputPlaceholder = regexp.MustCompile(`placeholder\s*=\s*"([^"]*)"`)
	reH1               = regexp.MustCompile(`<h[1-6][^>]*>([^<]*)</h[1-6]>`)
	rePTag             = regexp.MustCompile(`<p[^>]*>([^<]*)</p>`)
	reLi               = regexp.MustCompile(`<li[^>]*>([^<]*)</li>`)
)

// clickableRef represents an interactive element extracted from the page.
type clickableRef struct {
	Ref    string `json:"ref"`
	Type   string `json:"type"` // "link", "button", "submit"
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"`     // wrapped URL for JSON output
	rawURL string `json:"-"`                 // unwrapped URL for internal click resolution
}

// browserSnapshot holds the structured view of a loaded page.
type browserSnapshot struct {
	Title    string         `json:"title"`
	URL      string         `json:"url"`
	Content  string         `json:"content"`
	Status   int            `json:"status,omitempty"`
	Elements []clickableRef `json:"elements,omitempty"`
}

// maxBrowserHistory caps the number of snapshots retained in browser state to
// prevent memory DoS from repeated navigate actions.
const maxBrowserHistory = 50

// maxBrowserElements caps the number of interactive elements extracted from a
// page to prevent a hostile page from OOMing the agent with thousands of links
// or buttons.
const maxBrowserElements = 500

// maxBrowserSnapshotBytes caps the extracted text retained per snapshot so the
// history limit cannot be bypassed by a small number of huge pages.
const maxBrowserSnapshotBytes = 1 * 1024 * 1024

// browserState holds the shared state for one browser session.
type browserState struct {
	mu      sync.Mutex
	history []browserSnapshot
	current *browserSnapshot
	nextRef int
}

// ── Browser Tool ──────────────────────────────────────────────────────

type browserTool struct {
	ctxTool
	state           *browserState
	client          *http.Client
	dangerousConfig danger.DangerousConfig
	trustedClasses  map[danger.RiskClass]bool
}

// browserRequestTimeout bounds each browser HTTP request. Tests may lower it to
// verify timeout behavior.
var browserRequestTimeout = 30 * time.Second

func newBrowserTool(dc danger.DangerousConfig) *browserTool {
	t := &browserTool{
		state:           &browserState{nextRef: 1},
		dangerousConfig: dc,
	}
	t.client = &http.Client{
		Timeout:       browserRequestTimeout,
		CheckRedirect: t.checkRedirect,
		Transport:     ssrfGuardedTransport(),
	}
	return t
}

// checkRedirect re-classifies every redirect hop with the same SSRF /
// danger policy applied to the initial URL. Go's http.Client follows up
// to 10 redirects by default, but ONLY when CheckRedirect is nil — once
// we install our own we must enforce the hop limit ourselves. Without
// this, a benign-classified URL could 302 to http://169.254.169.254/
// (cloud metadata) or an internal host and the body would be returned to
// the model unchecked. The skill importer already guards redirects; this
// brings the browser tool in line.
func (t *browserTool) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	target := req.URL.String()
	risk := danger.ClassifyURL(target)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "browser", Resource: target, Risk: risk,
	}, t.trustedClasses); err != nil {
		return fmt.Errorf("redirect to %s blocked: %w", target, err)
	}
	return nil
}

func (t *browserTool) Name() string { return "browser" }

func (t *browserTool) Description() string {
	return `Navigate and interact with web pages. Supports four actions:

  navigate — Fetch a URL and extract page content + interactive elements
  snapshot — Return the current page's text view with ref IDs for elements
  click    — Follow a link or interact with an element by ref ID
  back     — Return to the previous page in navigation history

Note: Uses regex-based HTML parsing with NO JavaScript execution. Best for server-rendered HTML pages. SPAs and JS-heavy sites may return limited content.

Use browser_navigate(url) first, then browser_snapshot() to see interactive
elements with their ref IDs (e.g. @e1, @e2), then browser_click(ref) to
follow links or interact with buttons.`
}

// browserArgs holds all possible parameters for the browser tool.
type browserArgs struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
}

// browserResult is the generic response returned by any browser action.
type browserResult struct {
	Title    string         `json:"title,omitempty"`
	URL      string         `json:"url,omitempty"`
	Content  string         `json:"content,omitempty"`
	Status   int            `json:"status,omitempty"`
	Elements []clickableRef `json:"elements,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (t *browserTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"navigate", "snapshot", "click", "back"},
				"description": "Action to perform: 'navigate' (fetch a URL), 'snapshot' (view current page), 'click' (interact with element by ref), 'back' (go to previous page).",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "URL to navigate to (required for 'navigate' action).",
			},
			"ref": map[string]any{
				"type":        "string",
				"description": "Element reference ID (e.g. 'e1', 'e3') from a snapshot. Required for 'click' action.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *browserTool) Call(argsJSON string) (string, error) {
	var args browserArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Action == "" {
		return jsonError("action is required (navigate, snapshot, click, back)")
	}

	// Ensure state and client exist
	if t.state == nil {
		t.state = &browserState{nextRef: 1}
	}
	if t.client == nil {
		t.client = &http.Client{
			Timeout:       browserRequestTimeout,
			CheckRedirect: t.checkRedirect,
			Transport:     ssrfGuardedTransport(),
		}
	}

	switch args.Action {
	case "navigate":
		return t.doNavigate(args.URL)
	case "snapshot":
		return t.doSnaPshot()
	case "click":
		return t.doClick(args.Ref)
	case "back":
		return t.doBack()
	default:
		return jsonError(fmt.Sprintf("unknown action %q: must be navigate, snapshot, click, or back", args.Action))
	}
}

func (t *browserTool) doNavigate(rawURL string) (string, error) {
	if rawURL == "" {
		return jsonError("url is required for navigate action")
	}

	// Validate URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return jsonError(fmt.Sprintf("invalid URL %q: must start with http:// or https://", rawURL))
	}

	req, err := http.NewRequestWithContext(t.toolCtx(), "GET", rawURL, nil)
	if err != nil {
		return jsonError(fmt.Sprintf("cannot create request: %v", err))
	}
	req.Header.Set("User-Agent", "odek-browser/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	// Security: classify and check browser operation
	risk := danger.ClassifyURL(rawURL)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "browser", Resource: rawURL, Risk: risk,
	}, t.trustedClasses); err != nil {
		return jsonError(err.Error())
	}

	resp, err := t.client.Do(req)
	if err != nil {
		// Wrap network/TLS errors as untrusted: x509 errors can contain
		// attacker-controlled SAN text, and dial errors can expose internal IPs.
		msg := fmt.Sprintf("cannot fetch %q: %v", rawURL, err)
		return jsonError(wrapUntrusted(t.toolCtx(), rawURL, msg))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB cap
	if err != nil {
		return jsonError(fmt.Sprintf("cannot read response: %v", err))
	}

	html := string(body)
	// Use the post-redirect URL for attribution and relative-link resolution.
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	snap := parseHTML(t.toolCtx(), html, finalURL, resp.StatusCode)

	// Store in state. Keep a persistent copy of the snapshot for current; the
	// local variable's address would otherwise escape to the heap implicitly.
	t.state.mu.Lock()
	t.state.history = append(t.state.history, snap)
	if len(t.state.history) > maxBrowserHistory {
		t.state.history = t.state.history[len(t.state.history)-maxBrowserHistory:]
	}
	snapCopy := snap
	t.state.current = &snapCopy
	t.state.nextRef = len(snap.Elements) + 1
	t.state.mu.Unlock()

	return jsonResult(browserResult{
		Title:    snap.Title,
		URL:      snap.URL,
		Content:  wrapUntrusted(t.toolCtx(), snap.URL, snap.Content),
		Status:   snap.Status,
		Elements: snap.Elements,
	})
}

func (t *browserTool) doSnaPshot() (string, error) {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()

	if t.state.current == nil {
		return jsonError("no page loaded — call browser_navigate(url) first")
	}

	return jsonResult(browserResult{
		Title:    t.state.current.Title,
		URL:      t.state.current.URL,
		Content:  wrapUntrusted(t.toolCtx(), t.state.current.URL, t.state.current.Content),
		Elements: t.state.current.Elements,
	})
}

func (t *browserTool) doClick(ref string) (string, error) {
	if ref == "" {
		return jsonError("ref is required for click action")
	}

	t.state.mu.Lock()
	current := t.state.current
	t.state.mu.Unlock()

	if current == nil {
		return jsonError("no page loaded — call browser_navigate(url) first")
	}

	// Find the element by ref
	var target *clickableRef
	for i := range current.Elements {
		if current.Elements[i].Ref == ref {
			target = &current.Elements[i]
			break
		}
	}

	if target == nil {
		return jsonError(fmt.Sprintf("element %q not found on current page. Use browser_snapshot() to see available refs.", ref))
	}

	if target.Type == "link" {
		// Resolve relative URLs using the unwrapped URL; fall back to the
		// (wrapped) URL field if no raw URL is available.
		baseURL := current.URL
		u := target.rawURL
		if u == "" {
			u = target.URL
		}
		targetURL := resolveURL(u, baseURL)
		return t.doNavigate(targetURL)
	}

	if target.Type == "button" || target.Type == "submit" {
		// For buttons, just acknowledge the click (no form submission logic yet)
		return jsonResult(browserResult{
			Content: fmt.Sprintf("Clicked %s: %s", target.Type, target.Text),
		})
	}

	return jsonError(fmt.Sprintf("cannot click on element type %q", target.Type))
}

func (t *browserTool) doBack() (string, error) {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()

	if len(t.state.history) < 2 {
		return jsonError("no previous page in history")
	}

	// Pop current, go to previous
	t.state.history = t.state.history[:len(t.state.history)-1]
	t.state.current = &t.state.history[len(t.state.history)-1]

	return jsonResult(browserResult{
		Title:   t.state.current.Title,
		URL:     t.state.current.URL,
		Content: wrapUntrusted(t.toolCtx(), t.state.current.URL, t.state.current.Content),
	})
}

// ── HTML Parsing ──────────────────────────────────────────────────────

func parseHTML(ctx context.Context, html, pageURL string, status int) browserSnapshot {
	var snap browserSnapshot
	snap.URL = pageURL
	snap.Status = status

	// Extract title
	if m := reTitle.FindStringSubmatch(html); len(m) > 1 {
		snap.Title = strings.TrimSpace(m[1])
	}

	var contentParts []string
	var elements []clickableRef
	refCounter := 1
	seen := make(map[string]bool) // track unique URLs to avoid duplicate refs

	// Extract headings
	for _, m := range reH1.FindAllStringSubmatch(html, -1) {
		text := strings.TrimSpace(m[1])
		if text != "" {
			contentParts = append(contentParts, "# "+text)
		}
	}

	// Extract paragraphs
	for _, m := range rePTag.FindAllStringSubmatch(html, -1) {
		text := strings.TrimSpace(m[1])
		if text != "" {
			contentParts = append(contentParts, text)
		}
	}

	// Extract list items
	for _, m := range reLi.FindAllStringSubmatch(html, -1) {
		text := strings.TrimSpace(m[1])
		if text != "" {
			contentParts = append(contentParts, "• "+text)
		}
	}

	// Extract links
	for _, m := range reLink.FindAllStringSubmatch(html, -1) {
		if len(elements) >= maxBrowserElements {
			break
		}
		href := strings.TrimSpace(m[1])
		text := strings.TrimSpace(m[2])
		if href == "" || text == "" || href == "#" || strings.HasPrefix(href, "javascript:") {
			continue
		}
		// Skip duplicates
		uniqKey := href + "|" + text
		if seen[uniqKey] {
			continue
		}
		seen[uniqKey] = true

		ref := fmt.Sprintf("e%d", refCounter)
		refCounter++
		elements = append(elements, clickableRef{
			Ref:    ref,
			Type:   "link",
			Text:   text,
			URL:    href,
			rawURL: href,
		})
		contentParts = append(contentParts, fmt.Sprintf("[%s] %s → %s", ref, text, href))
	}

	// Extract buttons and inputs
	for _, m := range reButton.FindAllStringSubmatch(html, -1) {
		if len(elements) >= maxBrowserElements {
			break
		}
		text := strings.TrimSpace(m[1])
		if text == "" {
			text = "button"
		}
		ref := fmt.Sprintf("e%d", refCounter)
		refCounter++
		elements = append(elements, clickableRef{
			Ref:  ref,
			Type: "button",
			Text: text,
		})
		contentParts = append(contentParts, fmt.Sprintf("[%s] [button: %s]", ref, text))
	}

	for _, m := range reInput.FindAllStringSubmatch(html, -1) {
		if len(elements) >= maxBrowserElements {
			break
		}
		tag := m[0]
		text := ""
		if vm := reInputVal.FindStringSubmatch(tag); len(vm) > 1 {
			text = vm[1]
		} else if pm := reInputPlaceholder.FindStringSubmatch(tag); len(pm) > 1 {
			text = "[" + pm[1] + "]"
		}
		if text == "" {
			text = "input"
		}
		ref := fmt.Sprintf("e%d", refCounter)
		refCounter++
		elements = append(elements, clickableRef{
			Ref:  ref,
			Type: "submit",
			Text: text,
		})
		contentParts = append(contentParts, fmt.Sprintf("[%s] [input: %s]", ref, text))
	}

	snap.Content = strings.Join(contentParts, "\n")
	if len(snap.Content) > maxBrowserSnapshotBytes {
		snap.Content = snap.Content[:maxBrowserSnapshotBytes] +
			"\n[content truncated: exceeds per-snapshot byte cap]"
	}
	snap.Elements = elements

	// Title, element text, and link URLs come from the page — wrap them as
	// untrusted content so a hostile `href` cannot inject instructions.
	snap.Title = wrapUntrusted(ctx, pageURL, snap.Title)
	for i := range snap.Elements {
		snap.Elements[i].Text = wrapUntrusted(ctx, pageURL, snap.Elements[i].Text)
		if snap.Elements[i].Type == "link" && snap.Elements[i].URL != "" {
			snap.Elements[i].URL = wrapUntrusted(ctx, pageURL, snap.Elements[i].URL)
		}
	}
	// Keep the raw page URL itself unwrapped for internal navigation; it is
	// wrapped at the result-output boundary in doNavigate/doSnapshot.

	return snap
}

// ── URL Resolution ────────────────────────────────────────────────────

func resolveURL(href, base string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return href
	}
	refURL, err := url.Parse(href)
	if err != nil {
		return href
	}
	return baseURL.ResolveReference(refURL).String()
}
