package config

import "testing"

func TestResolveWebSearch_Defaults(t *testing.T) {
	w := resolveWebSearch(nil)
	if w.MaxResults != 10 {
		t.Errorf("MaxResults = %d, want 10", w.MaxResults)
	}
	if w.Timeout != 15 {
		t.Errorf("Timeout = %d, want 15", w.Timeout)
	}
	if w.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (tool disabled until set)", w.BaseURL)
	}
}

func TestResolveWebSearch_ZeroNumericsFilled(t *testing.T) {
	w := resolveWebSearch(&WebSearchConfig{BaseURL: "http://searxng:8080"})
	if w.MaxResults != 10 {
		t.Errorf("MaxResults = %d, want 10 (zero filled)", w.MaxResults)
	}
	if w.Timeout != 15 {
		t.Errorf("Timeout = %d, want 15 (zero filled)", w.Timeout)
	}
	if w.BaseURL != "http://searxng:8080" {
		t.Errorf("BaseURL = %q, want preserved", w.BaseURL)
	}
}

func TestResolveWebSearch_CustomValues(t *testing.T) {
	w := resolveWebSearch(&WebSearchConfig{
		BaseURL:    "http://127.0.0.1:8888",
		Categories: "general,news",
		Language:   "en",
		MaxResults: 5,
		Timeout:    30,
	})
	if w.BaseURL != "http://127.0.0.1:8888" {
		t.Errorf("BaseURL = %q", w.BaseURL)
	}
	if w.Categories != "general,news" {
		t.Errorf("Categories = %q", w.Categories)
	}
	if w.Language != "en" {
		t.Errorf("Language = %q", w.Language)
	}
	if w.MaxResults != 5 {
		t.Errorf("MaxResults = %d, want 5", w.MaxResults)
	}
	if w.Timeout != 30 {
		t.Errorf("Timeout = %d, want 30", w.Timeout)
	}
}
