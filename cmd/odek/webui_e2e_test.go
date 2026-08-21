package main

// webui_e2e_test.go — end-to-end tests for the Web client.
//
// Three layers, each catching a different class of integration break:
//
//   1. Asset contract — every embedded file serves with the right type and
//      security headers, the token injection/cookie flow works, CSP allows
//      what the UI needs, and no asset references an external origin.
//   2. DOM/CSS contract — every element id the JS references exists (in the
//      HTML or in JS-created markup), every class the JS/HTML uses has a
//      CSS rule, the CSS is well-formed, and cross-file constants (the
//      collapse max-height) stay in sync. These catch the "silent UI rot"
//      class of bugs: a renamed id or class breaks one panel and nothing
//      else fails.
//   3. Full client journey — the exact sequence the browser client performs
//      (token URL → WS hello → heartbeat → streamed prompt → REST session
//      management → pin/export → headless run → remote approval →
//      session_switch → cancel → connection kick → events/usage), driven
//      through newServeMux so the production mounting (routes + auth
//      wrappers) is what's actually under test.

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// ── helpers ───────────────────────────────────────────────────────────

// uiFSFiles returns every embedded UI file path ("ui/...").
func uiFSFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(uiFS, "ui", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assetURL maps an embedded path to the URL handleStatic serves it at.
func assetURL(t *testing.T, embedded string) (string, bool) {
	t.Helper()
	switch {
	case embedded == "ui/index.html":
		return "/", true
	case strings.HasPrefix(embedded, "ui/js/"):
		return "/" + strings.TrimPrefix(embedded, "ui/"), true
	case strings.HasPrefix(embedded, "ui/fonts/"):
		return "/" + strings.TrimPrefix(embedded, "ui/"), true
	case embedded == "ui/style.css" || embedded == "ui/app.js":
		return "/" + strings.TrimPrefix(embedded, "ui/"), true
	}
	return "", false
}

// jsSourceFiles returns the non-test JS module sources as (name, content).
func jsSourceFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range uiFSFiles(t) {
		if !strings.HasPrefix(f, "ui/js/") || !strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".test.js") {
			continue
		}
		b, err := uiFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = string(b)
	}
	return out
}

// readUIFile reads one embedded UI file.
func readUIFile(t *testing.T, name string) string {
	t.Helper()
	b, err := uiFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// stripCSSComments removes /* ... */ blocks so token scans don't match
// comment text.
func stripCSSComments(css string) string {
	return regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
}

// TestWebUI_DocsCoverAllRoutesAndEvents is the documentation drift guard:
// every API route mounted by newServeMux and every WebSocket event type the
// server emits must appear in docs/WEBUI.md. External clients (bodek, TUIs)
// are written against that document — a route or event missing from it is
// an integration break, not a typo.
func TestWebUI_DocsCoverAllRoutesAndEvents(t *testing.T) {
	docBytes, err := os.ReadFile("../../docs/WEBUI.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(docBytes)

	// Every mux-mounted /api route (production mounting lives in
	// newServeMux in serve.go).
	sources := []string{"serve.go", "serve_api.go", "serve_runs.go"}
	routes := map[string]bool{}
	eventTypes := map[string]string{} // type → source file
	for _, f := range sources {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s := string(b)
		for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\("(/api[^"]*)"`).FindAllStringSubmatch(s, -1) {
			route := strings.TrimRight(m[1], "/")
			if route == "/api" {
				continue
			}
			routes[route] = true
		}
		for _, m := range regexp.MustCompile(`"type":\s*"([a-z_]+)"`).FindAllStringSubmatch(s, -1) {
			eventTypes[m[1]] = f
		}
		// Events stamped via map assignment (server_info, pong).
		for _, m := range regexp.MustCompile(`\["type"\]\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(s, -1) {
			eventTypes[m[1]] = f
		}
	}

	for route := range routes {
		if !strings.Contains(doc, route) {
			t.Errorf("route %s is mounted in the mux but not documented in docs/WEBUI.md", route)
		}
	}

	// Internal bookkeeping types that never cross the wire as events.
	skipEvents := map[string]bool{
		"prompt": true, // client→server message; documented separately
	}
	for ev, f := range eventTypes {
		if skipEvents[ev] {
			continue
		}
		if !strings.Contains(doc, ev) {
			t.Errorf("WS event type %q (emitted in %s) is not documented in docs/WEBUI.md", ev, f)
		}
	}
}

// ── 1. Asset contract ────────────────────────────────────────────────

func TestWebUI_AllAssetsServedWithHeaders(t *testing.T) {
	wsToken, _ := newServeToken()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, f := range uiFSFiles(t) {
		url, routable := assetURL(t, f)
		if !routable {
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+url, nil)
		req.Header.Set("X-Odek-Ws-Token", wsToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", url, resp.StatusCode)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s: empty body", url)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", url)
		}
		if strings.HasSuffix(url, ".js") && !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
			t.Errorf("%s: content-type %q", url, resp.Header.Get("Content-Type"))
		}
		if strings.HasSuffix(url, ".css") && !strings.Contains(resp.Header.Get("Content-Type"), "text/css") {
			t.Errorf("%s: content-type %q", url, resp.Header.Get("Content-Type"))
		}
	}
}

// TestWebUI_AssetETagRevalidation: assets carry a strong ETag with
// must-revalidate so an open tab never serves a stale frontend after a
// binary upgrade, and unchanged assets answer 304.
func TestWebUI_AssetETagRevalidation(t *testing.T) {
	wsToken, _ := newServeToken()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fetch := func(h map[string]string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/style.css", nil)
		req.Header.Set("X-Odek-Ws-Token", wsToken)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	first := fetch(nil)
	if first.Header.Get("Cache-Control") != "no-cache, must-revalidate" {
		t.Errorf("Cache-Control = %q", first.Header.Get("Cache-Control"))
	}
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on static asset")
	}
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first fetch = %d", first.StatusCode)
	}

	// Revalidation with the same ETag → 304, no body transfer.
	second := fetch(map[string]string{"If-None-Match": etag})
	if second.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", second.StatusCode)
	}

	// A different (stale) ETag → full 200 with the current body.
	third := fetch(map[string]string{"If-None-Match": `"stale"`})
	if third.StatusCode != http.StatusOK {
		t.Errorf("stale If-None-Match = %d, want 200", third.StatusCode)
	}
}

func TestWebUI_TokenInjectionAndCookie(t *testing.T) {
	wsToken, _ := newServeToken()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Correct token → injected meta + hardened session cookie + no-store.
	resp, err := http.Get(srv.URL + "/?token=" + wsToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `content="`+wsToken+`"`) {
		t.Error("token not injected into the meta tag")
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == wsTokenCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("token cookie not set")
	}
	if cookie.Value != wsToken || cookie.SameSite != http.SameSiteStrictMode || !cookie.Secure || !cookie.HttpOnly {
		t.Errorf("cookie = %+v", cookie)
	}

	// Missing/wrong token → the page loads but the meta tag stays empty, so
	// the client cannot authenticate anything.
	resp, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), wsToken) {
		t.Error("token leaked to an unauthenticated GET /")
	}
	if !strings.Contains(string(body), `content=""`) {
		t.Error("meta tag placeholder not emptied")
	}
}

func TestWebUI_CSPAllowsWhatTheUINeeds(t *testing.T) {
	wsToken, _ := newServeToken()
	w := httptest.NewRecorder()
	handleStatic(wsToken)(w, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no CSP header")
	}
	scriptSrc := regexp.MustCompile(`script-src ([^;]+)`).FindStringSubmatch(csp)
	if scriptSrc == nil || !strings.Contains(scriptSrc[1], "'self'") {
		t.Errorf("script-src = %v", scriptSrc)
	}
	if scriptSrc != nil && strings.Contains(scriptSrc[1], "unsafe-inline") {
		t.Errorf("script-src allows inline scripts: %q", scriptSrc[1])
	}
	connectSrc := regexp.MustCompile(`connect-src ([^;]+)`).FindStringSubmatch(csp)
	if connectSrc == nil || !strings.Contains(connectSrc[1], "ws:") {
		t.Errorf("connect-src must allow ws: — got %v", connectSrc)
	}
}

func TestWebUI_NoExternalOriginReferences(t *testing.T) {
	// Everything the UI loads must be same-origin (offline policy). Scan
	// resource references, not arbitrary URL strings (the markdown renderer
	// legitimately emits http(s) links for chat content).
	htmlSrc := readUIFile(t, "ui/index.html")
	cssSrc := stripCSSComments(readUIFile(t, "ui/style.css"))

	extHTML := regexp.MustCompile(`(?:src|href)="(https?:)?//[^"]+"`)
	if m := extHTML.FindAllString(htmlSrc, -1); len(m) > 0 {
		t.Errorf("index.html references external origins: %v", m)
	}
	extCSS := regexp.MustCompile(`url\(\s*['"]?(https?:)?//`)
	if m := extCSS.FindAllString(cssSrc, -1); len(m) > 0 {
		t.Errorf("style.css references external origins: %v", m)
	}
	for name, js := range jsSourceFiles(t) {
		extJS := regexp.MustCompile(`(?:src|href)\s*=\s*['"](?:https?:)?//`)
		if m := extJS.FindAllString(js, -1); len(m) > 0 {
			t.Errorf("%s injects external resource URLs: %v", name, m)
		}
	}
}

// ── 2. DOM / CSS contract ────────────────────────────────────────────

func TestWebUI_ReferencedElementIDsExist(t *testing.T) {
	htmlSrc := readUIFile(t, "ui/index.html")

	// IDs present in the static markup.
	staticIDs := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([A-Za-z][\w-]*)"`).FindAllStringSubmatch(htmlSrc, -1) {
		staticIDs[m[1]] = true
	}
	// Static ids must be unique.
	if len(regexp.MustCompile(`id="`).FindAllString(htmlSrc, -1)) != len(staticIDs) {
		t.Error("duplicate id attributes in index.html")
	}

	// IDs the JS creates in template strings (render.js builds blocks with
	// their own ids, e.g. stream-content).
	dynamicIDs := map[string]bool{}
	for _, js := range jsSourceFiles(t) {
		for _, m := range regexp.MustCompile(`id="([A-Za-z][\w-]*)"`).FindAllStringSubmatch(js, -1) {
			dynamicIDs[m[1]] = true
		}
	}

	// IDs the JS references.
	refs := map[string]string{} // id → where
	for name, js := range jsSourceFiles(t) {
		for _, m := range regexp.MustCompile(`getElementById\('([A-Za-z][\w-]*)'\)`).FindAllStringSubmatch(js, -1) {
			refs[m[1]] = name
		}
		for _, m := range regexp.MustCompile(`getElementById\("([A-Za-z][\w-]*)"\)`).FindAllStringSubmatch(js, -1) {
			refs[m[1]] = name
		}
	}
	// dom.js centralizes lookups; treat its ids as refs too (already covered
	// by the same regex).
	for id, where := range refs {
		if !staticIDs[id] && !dynamicIDs[id] {
			t.Errorf("JS references #%s (%s) but no element with that id exists in index.html or JS-created markup", id, where)
		}
	}
}

func TestWebUI_UsedClassesHaveCSSRules(t *testing.T) {
	css := stripCSSComments(readUIFile(t, "ui/style.css"))
	htmlSrc := readUIFile(t, "ui/index.html")

	used := map[string]string{} // class → origin (for error messages)
	add := func(cls, origin string) {
		for _, tok := range strings.Fields(cls) {
			tok = strings.TrimSpace(tok)
			// Skip template-expression fragments and non-class tokens.
			if tok == "" || strings.ContainsAny(tok, "${}()<>.:#'\"") {
				continue
			}
			if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(tok) {
				continue
			}
			if _, dup := used[tok]; !dup {
				used[tok] = origin
			}
		}
	}
	for _, m := range regexp.MustCompile(`class="([^"]*)"`).FindAllStringSubmatch(htmlSrc, -1) {
		add(m[1], "index.html")
	}
	for name, js := range jsSourceFiles(t) {
		for _, m := range regexp.MustCompile(`class="([^"]*)"`).FindAllStringSubmatch(js, -1) {
			// Class attributes built with template expressions carry a
			// static prefix before ${...} — capture it.
			prefix := m[1]
			if i := strings.Index(prefix, "${"); i >= 0 {
				prefix = prefix[:i]
			}
			add(prefix, name)
		}
		for _, m := range regexp.MustCompile(`className\s*=\s*'([^']*)'`).FindAllStringSubmatch(js, -1) {
			add(m[1], name)
		}
		for _, m := range regexp.MustCompile(`classList\.(?:add|toggle|remove)\('([a-z][\w-]*)'`).FindAllStringSubmatch(js, -1) {
			add(m[1], name)
		}
		// Classes appended into className strings via ' + ' concatenation.
		for _, m := range regexp.MustCompile(`className\s*=\s*([a-z][\w-]*(?:\s*\+\s*[a-z][\w-]*)+)`).FindAllStringSubmatch(js, -1) {
			add(strings.ReplaceAll(m[1], "+", " "), name)
		}
		// querySelector targets must also be styled/renderable classes.
		for _, m := range regexp.MustCompile(`querySelector(?:All)?\('([^']*)'`).FindAllStringSubmatch(js, -1) {
			for _, cls := range regexp.MustCompile(`\.([a-z][\w-]*)`).FindAllStringSubmatch(m[1], -1) {
				add(cls[1], name+" (querySelector)")
			}
		}
	}

	// Dynamic status-class maps in panels.js (run-st-*) are resolved from
	// their literal values in the same file, so the regex above already
	// captures them as className fragments only if written as strings — they
	// are (RUN_STATUS_CLASS values). Role classes ('user', 'assistant',
	// 'system') are appended to 'msg ' — captured by the concat rule above
	// only as variables; resolve them explicitly.
	add("user assistant system", "render.js roles")

	// sr-only is a utility that may appear only in CSS.
	delete(used, "sr-only")

	for cls, origin := range used {
		if !strings.Contains(css, "."+cls) {
			t.Errorf("class %q (used in %s) has no CSS rule in style.css", cls, origin)
		}
	}
}

func TestWebUI_CSSWellFormedAndVarsDefined(t *testing.T) {
	raw := readUIFile(t, "ui/style.css")
	css := stripCSSComments(raw)

	// Balanced braces.
	depth := 0
	for _, r := range css {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				t.Fatal("unbalanced } in style.css")
			}
		}
	}
	if depth != 0 {
		t.Fatalf("style.css has %d unclosed {", depth)
	}

	// Every var(--x) in use is defined somewhere.
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`--([\w-]+)\s*:`).FindAllStringSubmatch(css, -1) {
		defined[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`var\(--([\w-]+)`).FindAllStringSubmatch(css, -1) {
		if !defined[m[1]] {
			t.Errorf("var(--%s) used but never defined", m[1])
		}
	}

	// Cross-file constant: the JS collapse threshold must equal the CSS
	// max-height of .bubble.collapsible.
	renderJS := jsSourceFiles(t)["render.js"]
	cm := regexp.MustCompile(`COLLAPSE_MAX_HEIGHT_PX = (\d+)`).FindStringSubmatch(renderJS)
	if cm == nil {
		t.Fatal("COLLAPSE_MAX_HEIGHT_PX not found in render.js")
	}
	if !strings.Contains(css, ".bubble.collapsible { max-height: "+cm[1]+"px;") {
		t.Errorf("render.js COLLAPSE_MAX_HEIGHT_PX=%s does not match the .bubble.collapsible max-height in style.css", cm[1])
	}
}

// ── 3. Full client journey ───────────────────────────────────────────

// journeyEnv wires the production mux against a mock LLM.
type journeyEnv struct {
	srv      *httptest.Server
	mux      *http.ServeMux
	token    string
	store    *session.Store
	resolved config.ResolvedConfig
	llm      *mockLLMServer
}

func newJourneyEnv(t *testing.T, stream bool, dangerPromptAll bool) *journeyEnv {
	t.Helper()

	// Streaming mock: SSE deltas with usage; plain JSON otherwise (the
	// client must exercise BOTH paths across the journey).
	sseMode := stream
	var llm *mockLLMServer
	llm = mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		if sseMode {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking it through\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"journey \"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":12}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if callCount <= 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Running.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo journey-ok\"}"}}]}}]}`)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"journey answer (approved)"}}],"usage":{"prompt_tokens":200,"completion_tokens":20}}`))
	})

	cleanupEnv := setTestEnv(t, llm.URL)
	t.Cleanup(cleanupEnv)
	store := newTestSessionStore(t)

	resolved := config.LoadConfig(config.CLIFlags{})
	if resolved.System == "" {
		resolved.System = defaultSystem
	}
	resolved.Stream = stream
	if dangerPromptAll {
		prompt := "prompt"
		resolved.Dangerous.DefaultAction = &prompt
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions")),
	)
	wsToken, err := newServeToken()
	if err != nil {
		t.Fatal(err)
	}
	state := &serveState{startedAt: time.Now(), resolved: resolved}
	mux := newServeMux(serveMuxDeps{
		Store:         store,
		Resources:     resourceReg,
		Resolved:      resolved,
		SystemMessage: resolved.System,
		State:         state,
		WsToken:       wsToken,
		MemoryDir:     filepath.Join(t.TempDir(), "memory"),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &journeyEnv{srv: srv, mux: mux, token: wsToken, store: store, resolved: resolved, llm: llm}
}

func (e *journeyEnv) do(t *testing.T, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Odek-Ws-Token", e.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *journeyEnv) dialWS(t *testing.T) *golangws.Conn {
	t.Helper()
	wsUpgradeLimiter.reset()
	cfg, err := golangws.NewConfig("ws://"+e.srv.Listener.Addr().String()+"/ws", "http://127.0.0.1/")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Protocol = append(cfg.Protocol, "odek."+e.token)
	conn, err := golangws.DialConfig(cfg)
	if err != nil {
		t.Fatalf("WS dial: %v", err)
	}
	return conn
}

// TestWebUI_E2E_StreamedClientJourney walks the exact browser flow with
// live streaming on: token page → WS hello → heartbeat → streamed prompt
// (no bulk re-send) → session persisted with usage → REST detail →
// rename+pin (pinned-first listing) → export → session_switch → idle
// cancel → connection registry + kick → events + usage.
func TestWebUI_E2E_StreamedClientJourney(t *testing.T) {
	env := newJourneyEnv(t, true, false)
	resetServeUsageForTest()
	serveEvents.reset()
	t.Cleanup(func() { serveEvents.reset() })

	// 1. Token URL loads the shell with the token injected (the browser's
	// first request).
	resp, err := http.Get(env.srv.URL + "/?token=" + env.token)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), env.token) {
		t.Fatal("token page did not inject the WS token")
	}

	// 2. Unauthenticated REST is rejected; authenticated health answers.
	r, _ := http.Get(env.srv.URL + "/api/health")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated health = %d, want 403", r.StatusCode)
	}
	hr, hb := env.do(t, http.MethodGet, "/api/health", "", nil)
	if hr.StatusCode != http.StatusOK || !strings.Contains(hb, `"status":"ok"`) {
		t.Fatalf("health = %d %s", hr.StatusCode, hb)
	}

	// 3. WS connect → server_info hello.
	conn := env.dialWS(t)
	defer conn.Close()
	hello := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })
	if hello["stream"] != true {
		t.Errorf("server_info stream = %v, want true", hello["stream"])
	}

	// 4. Heartbeat.
	writeJSON(conn, map[string]any{"type": "ping"})
	pong := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "pong" })
	if pong["t"] == nil {
		t.Error("pong missing timestamp")
	}

	// 5. Streamed prompt → deltas, no bulk re-send, session event with token.
	writeJSON(conn, map[string]any{"type": "prompt", "content": "stream me"})
	var sessionID, authToken string
	sawDelta, sawThinkingDelta, sawUsage := false, false, false
	var deltaText strings.Builder
	deadline := time.Now().Add(30 * time.Second)
	conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		var data []byte
		if err := golangws.Message.Receive(conn, &data); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		var evt map[string]any
		if json.Unmarshal(data, &evt) != nil {
			continue
		}
		switch evt["type"] {
		case "session":
			sessionID, _ = evt["session_id"].(string)
			authToken, _ = evt["auth_token"].(string)
		case "token_delta":
			sawDelta = true
			deltaText.WriteString(fmt.Sprint(evt["content"]))
		case "thinking_delta":
			sawThinkingDelta = true
		case "usage":
			sawUsage = true
		case "token":
			t.Errorf("bulk token event during streamed run: %v", evt)
		case "error":
			t.Fatalf("prompt error: %v", evt["message"])
		case "done":
			goto promptDone
		}
	}
	t.Fatal("no done event")
promptDone:
	if sessionID == "" || authToken == "" {
		t.Fatalf("session event missing id/token: %q %q", sessionID, authToken)
	}
	if !sawDelta || deltaText.String() != "journey answer" {
		t.Errorf("deltas = %q (seen=%v)", deltaText.String(), sawDelta)
	}
	if !sawThinkingDelta {
		t.Error("no thinking_delta events")
	}
	if !sawUsage {
		t.Error("no live usage event")
	}

	// 6. Session listing carries the session with usage.
	_, lb := env.do(t, http.MethodGet, "/api/sessions?limit=10&offset=0", "", nil)
	if !strings.Contains(lb, sessionID) {
		t.Fatalf("session missing from list: %s", lb)
	}
	if !strings.Contains(lb, `"input_tokens":120`) {
		t.Errorf("list missing usage (input_tokens=120): %.200s", lb)
	}

	// 7. Detail read with the session token.
	dr, db := env.do(t, http.MethodGet, "/api/sessions/"+sessionID, "", map[string]string{"X-Session-Token": authToken})
	if dr.StatusCode != http.StatusOK || !strings.Contains(db, `"messages"`) {
		t.Fatalf("detail = %d %.200s", dr.StatusCode, db)
	}

	// 8. Rename + pin; pinned floats first.
	pr, _ := env.do(t, http.MethodPost, "/api/sessions/"+sessionID, `{"name":"journey pinned","pinned":true}`, map[string]string{"X-Session-Token": authToken})
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("pin = %d", pr.StatusCode)
	}
	_, lb = env.do(t, http.MethodGet, "/api/sessions?limit=10&offset=0", "", nil)
	var sessPage struct {
		Sessions []session.Session `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(lb), &sessPage); err != nil {
		t.Fatal(err)
	}
	if len(sessPage.Sessions) == 0 || sessPage.Sessions[0].ID != sessionID || !sessPage.Sessions[0].Pinned {
		t.Errorf("pinned-first ordering broken: %+v", sessPage.Sessions)
	}

	// 9. Export markdown.
	er, _ := env.do(t, http.MethodGet, "/api/sessions/"+sessionID+"/export?format=md", "", map[string]string{"X-Session-Token": authToken})
	if er.StatusCode != http.StatusOK || !strings.Contains(er.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("export = %d %q", er.StatusCode, er.Header.Get("Content-Disposition"))
	}

	// 10. session_switch round trip (bad token first).
	writeJSON(conn, map[string]any{"type": "session_switch", "session_id": sessionID, "auth_token": "wrong"})
	evt := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "error" })
	if !strings.Contains(fmt.Sprint(evt["message"]), "session token") {
		t.Errorf("expected token error, got %v", evt["message"])
	}
	writeJSON(conn, map[string]any{"type": "session_switch", "session_id": sessionID, "auth_token": authToken})
	evt = readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "session" })
	if evt["session_id"] != sessionID {
		t.Errorf("session_switch echoed %v", evt["session_id"])
	}

	// 11. Idle cancel over WS.
	writeJSON(conn, map[string]any{"type": "cancel", "session_id": sessionID, "auth_token": authToken})
	evt = readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "cancelled" })
	if evt["idle"] != true {
		t.Errorf("idle cancel = %v", evt)
	}

	// 12. Connection registry shows this socket; kick closes it.
	cr, cb := env.do(t, http.MethodGet, "/api/connections", "", nil)
	if cr.StatusCode != http.StatusOK {
		t.Fatalf("connections = %d", cr.StatusCode)
	}
	var conns struct {
		Connections []struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"connections"`
	}
	if err := json.Unmarshal([]byte(cb), &conns); err != nil {
		t.Fatal(err)
	}
	kicked := false
	for _, c := range conns.Connections {
		if c.SessionID == sessionID {
			kr, _ := env.do(t, http.MethodDelete, "/api/connections/"+c.ID, "", nil)
			if kr.StatusCode != http.StatusNoContent {
				t.Errorf("kick = %d", kr.StatusCode)
			}
			kicked = true
			break
		}
	}
	if !kicked {
		t.Fatalf("WS connection not found in registry: %s", cb)
	}
	// The socket must now be closed by the server.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var data []byte
	if err := golangws.Message.Receive(conn, &data); err == nil {
		t.Error("socket still readable after kick")
	}

	// 13. Events + usage saw the run.
	_, eb := env.do(t, http.MethodGet, "/api/events?limit=50&session_id="+sessionID, "", nil)
	if !strings.Contains(eb, "run_started") && !strings.Contains(eb, "run_completed") && !strings.Contains(eb, "iteration_completed") {
		t.Errorf("no run events for session: %.200s", eb)
	}
	_, ub := env.do(t, http.MethodGet, "/api/usage", "", nil)
	if !strings.Contains(ub, `"prompts_completed":1`) || !strings.Contains(ub, `"tokens_in":120`) {
		t.Errorf("usage after journey = %s", ub)
	}
}

// TestWebUI_E2E_HeadlessApprovalJourney covers the buffered-fallback path
// plus the full remote-approval bridge through the production mux.
func TestWebUI_E2E_HeadlessApprovalJourney(t *testing.T) {
	env := newJourneyEnv(t, false, true) // stream off, all tools prompt
	resetServeUsageForTest()
	serveRunsActiveReset(t)

	// 1. Start a headless run.
	pr, pb := env.do(t, http.MethodPost, "/api/prompt", `{"content":"run a command","approval_timeout_seconds":120}`, nil)
	if pr.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt = %d %s", pr.StatusCode, pb)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(pb), &started); err != nil || started.RunID == "" {
		t.Fatalf("bad run response: %s", pb)
	}

	// 2. Wait for the pending approval through the REST view.
	var approvalID string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && approvalID == "" {
		run := lookupRun(started.RunID)
		if run == nil {
			t.Fatal("run vanished")
		}
		snap := run.snapshot(false)
		if s, _ := snap["status"].(string); runStatusTerminal(s) {
			t.Fatalf("run finished without approval: %v", snap)
		}
		if pend, ok := snap["pending_approvals"].([]map[string]any); ok && len(pend) > 0 {
			approvalID, _ = pend[0]["id"].(string)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if approvalID == "" {
		t.Fatal("no pending approval appeared")
	}

	// The approval lists over REST too.
	ar, ab := env.do(t, http.MethodGet, "/api/runs/"+started.RunID+"/approvals", "", nil)
	if ar.StatusCode != http.StatusOK || !strings.Contains(ab, approvalID) {
		t.Errorf("approval list = %d %.200s", ar.StatusCode, ab)
	}

	// 3. Approve via the bridge → run completes.
	rr, rb := env.do(t, http.MethodPost, "/api/runs/"+started.RunID+"/approvals/"+approvalID, `{"action":"approve"}`, nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", rr.StatusCode, rb)
	}
	snap := waitRunStatus(t, started.RunID, 30*time.Second)
	if snap["status"] != "completed" {
		t.Fatalf("status = %v (error %v)", snap["status"], snap["error"])
	}
	if snap["result"] != "journey answer (approved)" {
		t.Errorf("result = %v", snap["result"])
	}

	// 4. The run appears in the list; usage counted it.
	_, lb := env.do(t, http.MethodGet, "/api/runs", "", nil)
	if !strings.Contains(lb, started.RunID) {
		t.Errorf("run list missing %s", started.RunID)
	}
	_, ub := env.do(t, http.MethodGet, "/api/usage", "", nil)
	if !strings.Contains(ub, `"prompts_started":1`) {
		t.Errorf("usage = %s", ub)
	}

	// 5. Session delete round trip.
	sid, _ := snap["session_id"].(string)
	sess, err := env.store.Load(sid)
	if err != nil {
		t.Fatal(err)
	}
	dr, _ := env.do(t, http.MethodDelete, "/api/sessions/"+sid, "", map[string]string{"X-Session-Token": sess.AuthToken})
	if dr.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", dr.StatusCode)
	}
	_, lb = env.do(t, http.MethodGet, "/api/sessions?limit=50&offset=0", "", nil)
	if strings.Contains(lb, sid) {
		t.Error("session still listed after delete")
	}
}

func serveRunsActiveReset(t *testing.T) {
	t.Helper()
	resetServeRuns()
	t.Cleanup(resetServeRuns)
}
