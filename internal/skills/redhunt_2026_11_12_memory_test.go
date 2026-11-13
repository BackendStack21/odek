package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rh1112MemSkillFixtures are operator skills of the usual shape: some with an
// explicit trigger section, some whose keywords are derived from the body.
var rh1112MemSkillFixtures = []struct{ name, trigger, body string }{
	{"docker-build", "topic: docker container image dockerfile registry\n    action: build push tag publish", "Build images with docker buildx. Tag with the git sha, push to the registry, and verify the digest before deploying."},
	{"k8s-deploy", "topic: kubernetes cluster pod helm manifest namespace\n    action: deploy rollout rollback scale", "Apply manifests with kubectl, watch the rollout status, and roll back with helm when health checks fail."},
	{"go-testing", "topic: golang tests coverage race benchmark\n    action: test run debug profile", "Run go test with -race and -count=1, read coverage reports and profile slow benchmarks with pprof."},
	{"postgres-ops", "topic: postgres database migration index query\n    action: migrate backup restore tune", "Write reversible migrations, add indexes concurrently, take pg_dump backups and check slow queries with explain."},
	{"release-notes", "topic: release changelog version semver tag\n    action: write publish draft summarize", "Draft release notes from merged pull requests grouped by area, bump the semantic version and tag the release."},
	{"incident-triage", "", "Incident triage: read the alert, check dashboards and logs, identify the failing service, mitigate first, then write the timeline and the postmortem with owners and follow-up actions."},
	{"terraform-plan", "", "Terraform workflow: format, validate, plan against the workspace, review the resource diff carefully, apply only approved plans and keep state locked in the remote backend."},
	{"frontend-perf", "", "Frontend performance: measure bundle size, lazy load routes, compress images, audit with lighthouse and fix layout shifts and long tasks on the main thread."},
	{"security-review", "", "Security review: check authentication and authorization paths, validate inputs, look for injection, secrets in code, unsafe deserialization and missing rate limits."},
	{"api-design", "", "API design: version endpoints, use consistent resource naming, paginate list responses, document errors and keep request validation strict and explicit."},
}

func rh1112MemSkillManager(t *testing.T) *SkillManager {
	t.Helper()
	user := t.TempDir()
	for _, f := range rh1112MemSkillFixtures {
		dir := filepath.Join(user, f.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "---\nname: %s\ndescription: %s helper\n", f.name, f.name)
		if f.trigger != "" {
			fmt.Fprintf(&b, "odek:\n  trigger:\n    %s\n", f.trigger)
		}
		fmt.Fprintf(&b, "---\n\n%s\n", f.body)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sm := NewSkillManager(user, filepath.Join(t.TempDir(), "no-project-skills"))
	if got := len(sm.AllSkills()); got != len(rh1112MemSkillFixtures) {
		t.Fatalf("fixture: %d skills loaded, want %d", got, len(rh1112MemSkillFixtures))
	}
	return sm
}

// rh1112MemTurn is a user turn as the loop hands it to the skill loader: a
// short request from the principal plus an attached log file (wrapped as
// untrusted content) of about attachBytes bytes, with mostly unique tokens.
func rh1112MemTurn(attachBytes int) string {
	var b strings.Builder
	b.WriteString("deploy the payments service to staging and check why the rollout fails\n\n")
	b.WriteString(`<untrusted_content_0123456789abcdef source="attachment:app.log">` + "\n")
	for i := 0; b.Len() < attachBytes; i++ {
		fmt.Fprintf(&b, "2026-11-12T10:%02d:%02dZ req-%08x svc-%d latency=%dms status=%d path=/api/v1/items/%d user=u%06d\n",
			(i/60)%60, i%60, i*2654435761, i%37, (i*7)%900, 200+(i%5), i*3, i*11)
	}
	b.WriteString("</untrusted_content_0123456789abcdef>\n")
	return b.String()
}

// rh1112MemMatchTime is the fastest of runs MatchLazySkills calls on input.
func rh1112MemMatchTime(sm *SkillManager, input string, runs int) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		start := time.Now()
		sm.MatchLazySkills(input, 5)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// TestRED_SkillMatchCostBoundedByAttachmentSize: picking the lazy skills for
// a turn must not cost time proportional to an attachment carried in the
// turn.
//
// The loop hands the whole enriched user message (attachments, @-refs, --ctx
// files, forwards) to the skill loader before the model starts, and
// ScoredMatcher scores every token against every trigger keyword of every
// skill (prefix/stem and synonym checks with suffix stripping per pair), with
// no bound on the input. Measured: about 10 s per MiB of attachment with 20
// skills; Web UI uploads allow 10 MiB per prompt. Extended-memory recall
// bounds its query (maxRecallQueryRunes) for the same reason; the skill
// matcher does not.
func TestRED_SkillMatchCostBoundedByAttachmentSize(t *testing.T) {
	// Work check, independent of the machine: the text handed to the matcher
	// does not grow with the attachment.
	if a, b := len(skillMatchInput(rh1112MemTurn(4<<10))), len(skillMatchInput(rh1112MemTurn(512<<10))); a != b || b > 4*maxSkillMatchRunes {
		t.Fatalf("matcher input is %d bytes for a 4 KiB attachment and %d for a 512 KiB one", a, b)
	}
	if testing.Short() {
		t.Skip("timing ratio runs outside -short")
	}
	sm := rh1112MemSkillManager(t)
	small := rh1112MemTurn(4 << 10)
	large := rh1112MemTurn(64 << 10) // 16x the attachment
	if got := sm.MatchLazySkills(small, 5); len(got) == 0 {
		t.Fatal("fixture: the request matched no skill")
	}
	tSmall := rh1112MemMatchTime(sm, small, 5)
	tLarge := rh1112MemMatchTime(sm, large, 5)
	if tLarge > 4*tSmall {
		t.Fatalf("skill matching took %v for a turn with a 4 KiB attachment and %v with a 64 KiB one (%.1fx): "+
			"the per-turn skill match scans the whole attachment", tSmall, tLarge, float64(tLarge)/float64(tSmall))
	}
}
