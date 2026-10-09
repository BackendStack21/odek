package main

import "testing"

// Credential detection on NAME=value args keys on whole underscore-separated
// name components, so ordinary variables are left readable, and the URL
// userinfo match stops at the query string.
func TestRED_ListToolsRedactionDoesNotOverMatch(t *testing.T) {
	keep := []string{"PATH=/usr/bin", "AUTHOR=bob", "PATTERN=abc", "http://host/x?email=a@b.com/x", "KEYBOARD=us"}
	got := redactCredentialArgs(keep)
	for i, a := range keep {
		if got[i] != a {
			t.Errorf("%q was masked to %q", a, got[i])
		}
	}
	mask := []string{"GITHUB_PAT=ghp_x", "DB_PASSWORD=x", "API_KEY=x", "AUTH_HEADER=x", "SECRET=x", "ACCESS_TOKEN=x", "http://user:pw@host/x"}
	got = redactCredentialArgs(mask)
	for i, a := range mask {
		if got[i] == a {
			t.Errorf("%q was not masked", a)
		}
	}
}
