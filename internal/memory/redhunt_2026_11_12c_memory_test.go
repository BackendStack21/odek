package memory

import "testing"

// rh1112cOrdinaryFacts are facts that merely mention a fetcher near a shell
// word, `source`, `exec`, `.` or a Markdown code span. FactLooksUnsafe kept
// all of them before the download-and-run forms were widened and must keep
// them again.
var rh1112cOrdinaryFacts = []string{
	"user prefers bash over zsh",
	"run curl -s url | jq .",
	// `||` is a list operator, not a pipe.
	"health check: curl -sf http://localhost:8080/health || bash scripts/restart.sh",
	// Markdown code spans after prose words.
	"project source tarballs are downloaded with `curl -L` in CI",
	"commit with git add . then the hook notifies via `curl` webhook",
	"the release job runs deploy.sh `curl` step to post to Slack",
	"prefers bash `curl -s` aliases over wget",
	"The source of truth is the `curl` output from the health endpoint",
	"Frontend data source: uses `fetch` with a retry wrapper",
	"exec into the pod and run `curl localhost:8080/health`",
	"In bash `curl -sS localhost:8080/health` checks health",
	"Open source project; HTTP client is `fetch`",
	// A Markdown table row names tools; a fetcher without an argument
	// fetches nothing.
	"| tool | curl | wget | sh |",
}

func TestRED_FactLooksUnsafeKeepsOrdinaryFacts(t *testing.T) {
	for _, fact := range rh1112cOrdinaryFacts {
		if FactLooksUnsafe(fact) {
			t.Errorf("ordinary fact rejected: %q", fact)
		}
	}
}
