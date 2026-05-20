package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── Browser Navigate ──────────────────────────────────────────────────

func TestBrowser_Navigate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Test Page</title></head><body>
			<h1>Hello World</h1>
			<p>This is a test paragraph.</p>
			<a href="/page2">Page 2</a>
			<a href="https://example.com">External</a>
		</body></html>`))
	}))
	defer ts.Close()

	b := &browserTool{}
	result := callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)
	var r struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		URL     string `json:"url"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("navigate error: %s", r.Error)
	}
	if r.Title != "Test Page" {
		t.Errorf("title = %q, want %q", r.Title, "Test Page")
	}
	if !strings.Contains(r.Content, "Hello World") {
		t.Errorf("content missing 'Hello World': %q", r.Content)
	}
	if !strings.Contains(r.Content, "Page 2") {
		t.Errorf("content missing link 'Page 2': %q", r.Content)
	}
	if r.URL != ts.URL {
		t.Errorf("url = %q, want %q", r.URL, ts.URL)
	}
}

func TestBrowser_Navigate_InvalidURL(t *testing.T) {
	b := &browserTool{}
	result := callJSON(t, b, `{"action":"navigate","url":"not-a-valid-url"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid URL")
	}
}

func TestBrowser_Navigate_MissingURL(t *testing.T) {
	b := &browserTool{}
	result := callJSON(t, b, `{"action":"navigate"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for missing URL")
	}
}

func TestBrowser_Navigate_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Not Found"))
	}))
	defer ts.Close()

	b := &browserTool{}
	result := callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)
	var r struct {
		Content string `json:"content"`
		Status  int    `json:"status"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Status != 404 {
		t.Errorf("status = %d, want 404", r.Status)
	}
}

// ── Browser Snapshot ─────────────────────────────────────────────────

func TestBrowser_Snapshot(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>
			<a href="/a">Link A</a>
			<a href="/b">Link B</a>
			<button>Click Me</button>
			<input type="text" name="q" placeholder="Search">
		</body></html>`))
	}))
	defer ts.Close()

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)

	result := callJSON(t, b, `{"action":"snapshot"}`)
	var r struct {
		Content string `json:"content"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("snapshot error: %s", r.Error)
	}
	if !strings.Contains(r.Content, "Link A") {
		t.Errorf("snapshot missing 'Link A': %q", r.Content)
	}
	if !strings.Contains(r.Content, "Link B") {
		t.Errorf("snapshot missing 'Link B': %q", r.Content)
	}
}

func TestBrowser_Snapshot_NoPage(t *testing.T) {
	b := &browserTool{}
	result := callJSON(t, b, `{"action":"snapshot"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when no page loaded")
	}
}

// ── Browser Click ────────────────────────────────────────────────────

func TestBrowser_Click(t *testing.T) {
	var pages map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := pages[r.URL.Path]; ok {
			w.Write([]byte(p))
		} else {
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	pages = map[string]string{
		"/":     `<html><body><a href="/page2" ref="e1">Go to page 2</a></body></html>`,
		"/page2": `<html><body><h1>Page 2 Content</h1></body></html>`,
	}

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)

	// Click on the link
	result := callJSON(t, b, `{"action":"click","ref":"e1"}`)
	var r struct {
		Title string `json:"title"`
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("click error: %s", r.Error)
	}

	// Verify we navigated to page2
	snap := callJSON(t, b, `{"action":"snapshot"}`)
	var s struct {
		Content string `json:"content"`
	}
	mustUnmarshal(t, snap, &s)
	if !strings.Contains(s.Content, "Page 2 Content") {
		t.Errorf("after click, snapshot missing 'Page 2 Content': %q", s.Content)
	}
}

func TestBrowser_Click_InvalidRef(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><a href="/ok" ref="e1">OK</a></body></html>`))
	}))
	defer ts.Close()

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)

	result := callJSON(t, b, `{"action":"click","ref":"nonexistent"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for nonexistent ref")
	}
}

func TestBrowser_Click_MissingRef(t *testing.T) {
	b := &browserTool{}
	result := callJSON(t, b, `{"action":"click"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for missing ref")
	}
}

// ── Browser Back ──────────────────────────────────────────────────────

func TestBrowser_Back(t *testing.T) {
	var pages map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := pages[r.URL.Path]; ok {
			w.Write([]byte(p))
		}
	}))
	defer ts.Close()

	pages = map[string]string{
		"/":     `<html><body><h1>Home</h1></body></html>`,
		"/page2": `<html><body><h1>Page 2</h1></body></html>`,
	}

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`/page2"}`)

	result := callJSON(t, b, `{"action":"back"}`)
	var r struct {
		Title string `json:"title"`
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("back error: %s", r.Error)
	}

	snap := callJSON(t, b, `{"action":"snapshot"}`)
	var s struct {
		Content string `json:"content"`
	}
	mustUnmarshal(t, snap, &s)
	if !strings.Contains(s.Content, "Home") {
		t.Errorf("after back, snapshot missing 'Home': %q", s.Content)
	}
}

func TestBrowser_Back_NoHistory(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><h1>Only Page</h1></body></html>`))
	}))
	defer ts.Close()

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)

	result := callJSON(t, b, `{"action":"back"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when no history")
	}
}

// ── Browser Unknown Action ────────────────────────────────────────────

func TestBrowser_UnknownAction(t *testing.T) {
	b := &browserTool{}
	result := callJSON(t, b, `{"action":"fly"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for unknown action")
	}
}

// ── Browser Schema ───────────────────────────────────────────────────

func TestBrowser_Schema(t *testing.T) {
	b := &browserTool{}
	schema := b.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)

	if _, ok := props["action"]; !ok {
		t.Error("Schema missing 'action' property")
	}
	if _, ok := props["url"]; !ok {
		t.Error("Schema missing 'url' property")
	}
	if _, ok := props["ref"]; !ok {
		t.Error("Schema missing 'ref' property")
	}
}

// ── Browser Link Extraction ───────────────────────────────────────────

func TestBrowser_ExtractsInteractiveElements(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>
			<a href="/a">Link One</a>
			<a href="/b">Link Two</a>
			<button onclick="do()">Button</button>
			<input type="submit" value="Submit">
		</body></html>`))
	}))
	defer ts.Close()

	b := &browserTool{}
	callJSON(t, b, `{"action":"navigate","url":"`+ts.URL+`"}`)

	result := callJSON(t, b, `{"action":"snapshot"}`)
	var r struct {
		Content string `json:"content"`
	}
	mustUnmarshal(t, result, &r)

	// Should contain ref IDs for interactive elements
	if !strings.Contains(r.Content, "e1") {
		t.Errorf("snapshot should contain ref IDs, got: %q", r.Content)
	}
}

// ── Browser Bad Action Parameters ─────────────────────────────────────

func TestBrowser_Navigate_BadJSON(t *testing.T) {
	b := &browserTool{}
	result, err := b.Call(`{"action":"navigate","url":123}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	var r struct {
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(result), &r)
	if r.Error == "" {
		t.Fatal("expected error for bad JSON types")
	}
}
