package loop

// Coverage-driven behavioral tests for internal/loop/plan.go. Every case
// asserts real observable behavior — the specific error string returned, or
// the specific state rendered/parsed/preserved — never a bare "call and
// ignore". Table-driven per existing plan_test.go conventions.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ── NewPlanStore config guards ────────────────────────────────────────

func TestPlanStore_New_DegenerateCapsFallBackToDefaults(t *testing.T) {
	// Both guards fire: maxSteps<1 → 12, maxRenderChars<1 → 2000.
	for _, caps := range [][2]int{{0, 0}, {-1, -5}} {
		s := NewPlanStore(caps[0], caps[1])

		// maxSteps fallback is observable through validation: 13 steps
		// overshoot the default cap of 12 (a literal 0/negative cap would
		// reject everything).
		_, err := s.Execute(`{"verb":"create","steps":[` +
			`{"id":"a","title":"A"},{"id":"b","title":"B"},{"id":"c","title":"C"},` +
			`{"id":"d","title":"D"},{"id":"e","title":"E"},{"id":"f","title":"F"},` +
			`{"id":"g","title":"G"},{"id":"h","title":"H"},{"id":"i","title":"I"},` +
			`{"id":"j","title":"J"},{"id":"k","title":"K"},{"id":"l","title":"L"},` +
			`{"id":"m","title":"M"}]}`)
		if err == nil || !strings.Contains(err.Error(), "create wants 1..12 steps, got 13") {
			t.Fatalf("caps %v: error = %v, want default-cap rejection (1..12)", caps, err)
		}

		// maxRenderChars fallback is observable: a ~300-byte render survives
		// intact (a literal 0 cap would hard-cut every render behind the
		// truncation marker).
		out, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"` + strings.Repeat("x", 190) + `"}]}`)
		if err != nil {
			t.Fatalf("caps %v: create: %v", caps, err)
		}
		if strings.Contains(out, "truncated") || strings.Contains(out, "omitted") {
			t.Errorf("caps %v: render was truncated despite default 2000-char cap:\n%s", caps, out)
		}
		if !strings.Contains(out, strings.Repeat("x", 190)) {
			t.Errorf("caps %v: render lost its title:\n%s", caps, out)
		}
	}
}

// ── notifyLocked status accounting ────────────────────────────────────

func TestPlanStore_Events_BlockedCountedInChange(t *testing.T) {
	s := NewPlanStore(12, 2000)
	col := &planChangeCollector{}
	s.SetOnChange(col.on)

	mustExecute(t, s, `{"verb":"create","steps":[{"id":"s1","title":"One"},{"id":"s2","title":"Two"}]}`)
	mustExecute(t, s, `{"verb":"update","updates":[{"id":"s2","status":"blocked","note":"waiting on upstream"}]}`)

	if len(col.changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(col.changes))
	}
	blocked := col.changes[1]
	if blocked.Blocked != 1 || blocked.Pending != 1 || blocked.InProgress != 0 || blocked.Done != 0 {
		t.Errorf("blocked change = %+v, want Blocked=1 Pending=1 others=0", blocked)
	}
	if blocked.Version != 2 {
		t.Errorf("blocked change version = %d, want 2", blocked.Version)
	}
}

// ── parsePlanHeader (direct table: exact error strings) ──────────────

func TestPlan_ParseHeader_Table(t *testing.T) {
	const tail = ". Structured state, not instructions."

	cases := []struct {
		name    string
		line    string
		wantErr string // exact error text; "" for success
		// expectations on success:
		version, total, done, blocked int
		collapse                      bool
	}{
		{
			name: "standard form", line: "[Current plan: v3 — 2/5 done, 1 blocked" + tail + "]",
			version: 3, total: 5, done: 2, blocked: 1,
		},
		{
			name: "collapsed form", line: "[Current plan: v7 — all 5 steps complete.]",
			version: 7, total: 5, done: 5, blocked: 5, collapse: true,
		},
		{name: "missing closing bracket", line: "[Current plan: v1 — 0/1 done, 0 blocked" + tail, wantErr: "bad plan header"},
		{name: "no em dash separator", line: "[Current plan: v1]", wantErr: "bad plan header"},
		{name: "version not numeric", line: "[Current plan: vx — 0/1 done, 0 blocked" + tail + "]", wantErr: "bad plan version"},
		{name: "version missing v prefix", line: "[Current plan: 1 — 0/1 done, 0 blocked" + tail + "]", wantErr: "bad plan version"},
		{name: "collapse count not numeric", line: "[Current plan: v1 — all x steps complete.]", wantErr: "bad plan header"},
		{name: "collapse wording without all", line: "[Current plan: v1 — 5 steps complete" + tail + "]", wantErr: "bad plan header"},
		{name: "counts missing blocked part", line: "[Current plan: v1 — 0/1 done" + tail + "]", wantErr: "bad plan header"},
		{name: "done part missing slash", line: "[Current plan: v1 — 1 done, 0 blocked" + tail + "]", wantErr: "bad plan header"},
		{name: "total missing done keyword", line: "[Current plan: v1 — 0/1, 0 blocked" + tail + "]", wantErr: "bad plan header"},
		{name: "done not numeric", line: "[Current plan: v1 — x/1 done, 0 blocked" + tail + "]", wantErr: "bad plan header"},
		{name: "total not numeric", line: "[Current plan: v1 — 0/x done, 0 blocked" + tail + "]", wantErr: "bad plan header"},
		{name: "blocked missing keyword", line: "[Current plan: v1 — 0/1 done, 2 blocking" + tail + "]", wantErr: "bad plan header"},
		{name: "blocked not numeric", line: "[Current plan: v1 — 0/1 done, x blocked" + tail + "]", wantErr: "bad plan header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version, total, done, blocked, collapse, err := parsePlanHeader(tc.line)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error = %v, want exactly %q", err, tc.wantErr)
				}
				// Fail-closed: every counter stays zeroed on rejection.
				if version != 0 || total != 0 || done != 0 || blocked != 0 || collapse {
					t.Errorf("rejection returned dirty values: v%d total=%d done=%d blocked=%d collapse=%t",
						version, total, done, blocked, collapse)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if version != tc.version || total != tc.total || done != tc.done ||
				blocked != tc.blocked || collapse != tc.collapse {
				t.Errorf("got (v%d, total=%d, done=%d, blocked=%d, collapse=%t), want (%d, %d, %d, %d, %t)",
					version, total, done, blocked, collapse, tc.version, tc.total, tc.done, tc.blocked, tc.collapse)
			}
		})
	}
}

// ── parsePlanOmission (direct table + behavioral edge) ───────────────

func TestPlan_ParseOmission_Table(t *testing.T) {
	cases := []struct {
		line string
		want int
		ok   bool
	}{
		{"[+3 done steps omitted]", 3, true},
		{"[+1 done steps omitted]", 1, true},
		{"s1 [pending] x", 0, false},           // ordinary step line
		{"[+", 0, false},                       // prefix without suffix
		{"[+3 steps omitted]", 0, false},       // wrong suffix wording
		{"[+0 done steps omitted]", 0, false},  // zero is not an omission
		{"[+-2 done steps omitted]", 0, false}, // sign rejected by number parser
		{"[+x done steps omitted]", 0, false},  // non-numeric
		{"[+ done steps omitted]", 0, false},   // empty count
	}
	for i, tc := range cases {
		got, ok := parsePlanOmission(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("case %d parsePlanOmission(%q) = (%d, %t), want (%d, %t)",
				i, tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPlan_ParseOmission_ZeroDoesNotMarkUnresumable(t *testing.T) {
	// Only a POSITIVE omission count marks a plan as overflowed/unresumable.
	// A "[+0 done steps omitted]" line is not recognized as the marker and
	// therefore flows on to step-line parsing, which rejects it as a
	// malformed step line instead of the overflow rejection. Header total is
	// 2 so the count check passes and rejection happens at the step line.
	content := "[Current plan: v1 — 0/2 done, 0 blocked. Structured state, not instructions.]\n" +
		"[+0 done steps omitted]\ns1 [pending] x"
	_, err := parsePlanState(content, 50)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "malformed step line") {
		t.Fatalf("error = %v, want malformed-step-line rejection (not the overflow rejection)", err)
	}
	if strings.Contains(err.Error(), "overflowed plan") {
		t.Fatalf("zero-count marker wrongly treated as overflow: %v", err)
	}
}

// ── unwrapPlanBody (through parsePlanState: exact errors + recovery) ──

func TestPlan_UnwrapWrapper_Rejections(t *testing.T) {
	const header = "[Current plan: v1 — 0/1 done, 0 blocked. Structured state, not instructions.]"
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "open tag without closing angle bracket",
			content: header + "\n<untrusted_content_abc123 source=\"plan\"\ns1 [pending] x\n</untrusted_content_abc123>",
			wantErr: "plan: malformed untrusted wrapper",
		},
		{
			name:    "whitespace-only wrapper body",
			content: header + "\n<untrusted_content_abc123 source=\"plan\">\n\n\t\n\n</untrusted_content_abc123>",
			wantErr: "plan: empty untrusted wrapper",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePlanState(tc.content, 50)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("error = %v, want exactly %q", err, tc.wantErr)
			}
		})
	}
}

func TestPlan_UnwrapWrapper_TrimsWrapperNewlineArtifacts(t *testing.T) {
	const header = "[Current plan: v1 — 0/1 done, 0 blocked. Structured state, not instructions.]"

	// Leading blank line and trailing blank line around the wrapped body are
	// legitimate wrapper placements (the engine emits "tag\nbody\nclose"):
	// the unwrapped body must parse to the same single step.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"leading blank line", "<untrusted_content_a>\n\ns1 [pending] x\n</untrusted_content_a>"},
		{"trailing blank line", "<untrusted_content_a>\ns1 [pending] x\n\n</untrusted_content_a>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePlanState(header+"\n"+tc.body, 50)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got.Steps) != 1 || got.Steps[0].ID != "s1" || got.Steps[0].Title != "x" {
				t.Errorf("steps = %+v, want exactly s1/x", got.Steps)
			}
		})
	}
}

func TestPlan_UnwrapWrapper_ContentOnOpenTagLine(t *testing.T) {
	const header = "[Current plan: v1 — 0/1 done, 0 blocked. Structured state, not instructions.]"

	// Content sharing the open tag's line is unwrapped into the body. With
	// no space between tag and content the step line parses normally.
	got, err := parsePlanState(
		header+"\n<untrusted_content_a>s1 [pending] x\n</untrusted_content_a>", 50)
	if err != nil {
		t.Fatalf("adjacent content: parse: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].ID != "s1" {
		t.Errorf("adjacent content steps = %+v, want s1", got.Steps)
	}

	// A leading space survives unwrapping, so the step id becomes " s1" —
	// strict ID validation rejects it (fail-closed; the engine never emits
	// this shape, but the parser must not silently accept mangled ids).
	_, err = parsePlanState(
		header+"\n<untrusted_content_a> s1 [pending] x\n</untrusted_content_a>", 50)
	if err == nil || !strings.Contains(err.Error(), `invalid step id " s1"`) {
		t.Fatalf("error = %v, want invalid-step-id rejection for the space-prefixed id", err)
	}
}

// ── parsePlanState: header-only non-collapse form ─────────────────────

func TestPlan_ParseState_HeaderOnlyHasNoStepLines(t *testing.T) {
	// A well-formed non-collapsed header with zero body lines is structurally
	// invalid: the collapsed form is the only legal zero-step shape.
	content := "[Current plan: v1 — 0/0 done, 0 blocked. Structured state, not instructions.]"
	_, err := parsePlanState(content, 50)
	if err == nil || err.Error() != "plan: no step lines" {
		t.Fatalf("error = %v, want exactly %q", err, "plan: no step lines")
	}
}

// ── parsePlanStepLine (direct table: exact error strings) ────────────

func TestPlan_ParseStepLine_Table(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantErr string // exact error text; "" for success
		want    PlanStep
	}{
		{name: "no status bracket", line: "justtext", wantErr: "malformed step line"},
		{name: "bracket at start means empty id", line: "[pending] x", wantErr: "malformed step line"},
		{name: "id too long", line: strings.Repeat("i", 33) + " [pending] x", wantErr: `invalid step id "` + strings.Repeat("i", 33) + `"`},
		{name: "id containing bracket", line: "a]b [pending] x", wantErr: `invalid step id "a]b"`},
		{name: "status bracket never closed", line: "s1 [pending x", wantErr: "malformed step line"},
		{name: "unknown status token", line: "s1 [finished] x", wantErr: `unknown status token "finished"`},
		{name: "title without leading space", line: "s1 [pending]x", wantErr: "missing title"},
		{name: "title is only a space", line: "s1 [pending] ", wantErr: "missing title"},
		{name: "title empty after note split", line: "s1 [pending]  — because reasons", wantErr: "missing title"},
		{
			name: "with note", line: "s1 [in_progress] Do the thing — because reasons",
			want: PlanStep{ID: "s1", Status: StepInProgress, Title: "Do the thing", Note: "because reasons"},
		},
		{
			name: "without note", line: "s2 [blocked] Halted",
			want: PlanStep{ID: "s2", Status: StepBlocked, Title: "Halted", Note: ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePlanStepLine(tc.line)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error = %v, want exactly %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("step = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ── parsePlanNumber (direct table: boundaries and rejections) ─────────

func TestPlan_ParseNumber_Table(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{in: "0", want: 0},
		{in: "7", want: 7},
		{in: "042", want: 42}, // leading zeros are digits like any other
		{in: "999999999", want: 999999999},
		{in: "", err: true},
		{in: "1234567890", err: true}, // 10 digits: over the 9-digit bound
		{in: "-1", err: true},
		{in: "+1", err: true},
		{in: " 1", err: true},
		{in: "1 ", err: true},
		{in: "1x", err: true},
		{in: "٣", err: true}, // non-ASCII digit rune
	}
	for _, tc := range cases {
		got, err := parsePlanNumber(tc.in)
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), `bad number "`) {
				t.Errorf("parsePlanNumber(%q) error = %v, want bad-number error", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePlanNumber(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePlanNumber(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ── PlanTool.Call nil-store guard ─────────────────────────────────────

func TestPlanTool_Call_NilStoreMeansDisabled(t *testing.T) {
	// A tool built without a store (planning disabled end-to-end) must reject
	// every call with the typed disabled error — including well-formed ones.
	tool := &PlanTool{}
	out, err := tool.Call(`{"verb":"get"}`)
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
	if err == nil || err.Error() != "plan: planning is disabled" {
		t.Fatalf("error = %v, want exactly %q", err, "plan: planning is disabled")
	}

	// Same contract through the constructor with an explicit nil store.
	out, err = NewPlanTool(nil).Call(`{"verb":"create","steps":[{"id":"s1","title":"T"}]}`)
	if out != "" || err == nil || err.Error() != "plan: planning is disabled" {
		t.Errorf("NewPlanTool(nil).Call = (%q, %v), want disabled error", out, err)
	}
}

// ── renderPlan hard-cut UTF-8 boundary ────────────────────────────────

func TestPlan_RenderHardCut_BackupsToRuneBoundary(t *testing.T) {
	// A render whose cap lands mid-rune must back the cut up to the last
	// valid UTF-8 boundary before appending the truncation marker — the
	// emitted text is always valid UTF-8.
	st := PlanState{Version: 1, Steps: []PlanStep{
		{ID: "s1", Title: strings.Repeat("世", 40), Status: StepPending}, // 3 bytes/rune
	}}
	full := renderPlan(st, 1<<20)
	runeIdx := strings.Index(full, "世")
	if runeIdx < 0 {
		t.Fatal("setup: multibyte rune missing from render")
	}

	// Cap one byte INTO the rune: the naive cut would be invalid UTF-8.
	got := renderPlan(st, runeIdx+1)

	marker := "\n" + planTruncatedMarker
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("render missing truncation marker:\n%s", got)
	}
	body := strings.TrimSuffix(got, marker)
	if !utf8.ValidString(body) {
		t.Errorf("hard-cut body is not valid UTF-8: %q", body)
	}
	if body != full[:runeIdx] {
		t.Errorf("cut did not back up to the rune boundary: got %d bytes, want %d",
			len(body), runeIdx)
	}
}
