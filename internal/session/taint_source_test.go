package session

import "testing"

func wrapperFor(source, body string) string {
	return "<untrusted_content_ab12 source=\"" + source + "\">\n" + body + "\n</untrusted_content_ab12>"
}

// Engine-derived context (plan, digest, memory block, …) is wrapped too, but
// it is derived from history that is already taint-tracked; only external
// content may taint the session.
func TestRED_EngineDerivedWrappersDoNotTaint(t *testing.T) {
	for _, src := range []string{"plan", "plan_remaining", "compaction", "memory", "persisted_system",
		"progress_summary", "completed_effects", "skill", "extended_memory", "return_after_break"} {
		if ContentCarriesUntrusted(wrapperFor(src, "derived text")) {
			t.Errorf("engine-derived source %q tainted", src)
		}
	}
	// A neutralised external tag nested in an engine-derived body is data.
	nested := wrapperFor("compaction", "summary of <untrusted·content_cd source=\"tool:shell\"> output")
	if ContentCarriesUntrusted(nested) {
		t.Error("neutralised nested wrapper tainted")
	}
	for _, src := range []string{"tool:shell", "/abs/path/plan", "$ cat plan", "mcp:srv:tool", "browser:https://x",
		"project:AGENTS.md", "bg", "session_search:get", "attachment:plan", "external:plan", "telegram:chat:1:voice", "delegate_tasks", ""} {
		if !ContentCarriesUntrusted(wrapperFor(src, "external text")) {
			t.Errorf("external source %q did not taint", src)
		}
	}
	// An external wrapper after an engine-derived one still taints.
	if !ContentCarriesUntrusted(wrapperFor("plan", "p") + "\n" + wrapperFor("tool:read_file", "x")) {
		t.Error("external wrapper after an engine-derived one missed")
	}
}

// Episode recall carries a summary of another session, judged trusted only by
// the memory gate's per-tool rule; recalling it must taint the run so it
// cannot steer a trusted delegation.
func TestRED_EpisodeRecallTaints(t *testing.T) {
	if EngineDerivedSource("episode") || !ContentCarriesUntrusted(wrapperFor("episode", "earlier session")) {
		t.Fatal("episode recall does not taint")
	}
}
