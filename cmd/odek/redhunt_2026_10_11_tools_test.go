package main

import (
	"encoding/json"
	"testing"
)

// config_view receives the raw tool-call arguments JSON from the loop
// ({"section":"security"}), exactly like every other tool. It must honour
// the section parameter its own schema advertises.
func TestRED_ConfigViewJSONArgs(t *testing.T) {
	tool := &configViewTool{view: buildConfigView(introspectionFixture())}

	out, err := tool.Call(`{"section":"security"}`)
	if err != nil {
		t.Fatalf("config_view with schema-shaped args failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["sandbox"]; !ok {
		t.Errorf("security section missing sandbox: %s", out)
	}
	if _, ok := m["model"]; ok {
		t.Errorf("security section must not carry model: %s", out)
	}

	// A no-argument call is sent as "{}" by most providers; it is the full view.
	full, err := tool.Call(`{}`)
	if err != nil {
		t.Fatalf("config_view with empty-object args failed: %v", err)
	}
	all, _ := tool.Call("all")
	if full != all {
		t.Errorf("{} must render the full view")
	}
	// {"section":"all"} and whitespace-padded JSON behave the same.
	if got, err := tool.Call(" {\"section\":\"all\"} "); err != nil || got != all {
		t.Errorf(`{"section":"all"} = %v, %v`, got, err)
	}
	// Unknown section in JSON form still errors.
	if _, err := tool.Call(`{"section":"nope"}`); err == nil {
		t.Errorf("unknown section must error")
	}
	// Malformed JSON errors rather than being treated as a section name.
	if _, err := tool.Call(`{"section":`); err == nil {
		t.Errorf("malformed args must error")
	}
}
