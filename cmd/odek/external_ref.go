package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BackendStack21/odek/internal/session"
)

// parseExternalRefFlag parses one --external-ref value into a validated
// session.ExternalRef. Two forms are accepted:
//
//	--external-ref kind=opaque_application_state,uri=app://workflow/123,created_by=example-app
//	--external-ref ci-run=https://ci.example.test/runs/4821   (shorthand kind=uri)
//
// In the shorthand form created_by defaults to "cli". The key=value form
// also accepts an optional read_only=true|false. Parse and validation
// errors are fatal at the call sites — the operator explicitly asked for
// the ref, so silently dropping it would violate least surprise.
func parseExternalRefFlag(spec string) (session.ExternalRef, error) {
	ref := session.ExternalRef{CreatedBy: "cli"}
	if !strings.Contains(spec, ",") {
		// Shorthand: kind=uri (the URI may itself contain '=').
		kind, uri, ok := strings.Cut(spec, "=")
		if !ok {
			return ref, fmt.Errorf("invalid --external-ref %q: want the kind=uri shorthand or comma-separated key=value pairs", spec)
		}
		ref.Kind, ref.URI = kind, uri
	} else {
		for _, pair := range strings.Split(spec, ",") {
			key, val, ok := strings.Cut(pair, "=")
			if !ok {
				return ref, fmt.Errorf("invalid --external-ref %q: malformed pair %q (want key=value)", spec, pair)
			}
			switch key {
			case "kind":
				ref.Kind = val
			case "uri":
				ref.URI = val
			case "created_by":
				ref.CreatedBy = val
			case "read_only":
				b, err := strconv.ParseBool(val)
				if err != nil {
					return ref, fmt.Errorf("invalid --external-ref %q: read_only must be true or false, got %q", spec, val)
				}
				ref.ReadOnly = b
			default:
				return ref, fmt.Errorf("invalid --external-ref %q: unknown key %q (want kind, uri, created_by, read_only)", spec, key)
			}
		}
	}
	if err := ref.Validate(); err != nil {
		return ref, fmt.Errorf("invalid --external-ref %q: %w", spec, err)
	}
	return ref, nil
}

// parseExternalRefFlags parses every repeatable --external-ref value.
func parseExternalRefFlags(specs []string) ([]session.ExternalRef, error) {
	var refs []session.ExternalRef
	for _, spec := range specs {
		ref, err := parseExternalRefFlag(spec)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// continueRejectedFlags are run flags odek continue refuses. A resumed
// session keeps the provider, model, endpoint, system prompt and sandbox
// posture it was created with, so flags that would change them are an
// error rather than a silent no-op; --session is implied by continue.
var continueRejectedFlags = map[string]bool{
	"--provider": true, "--model": true, "--base-url": true, "--system": true,
	"--sandbox": true, "--no-sandbox": true,
	"--sandbox-image": true, "--sandbox-network": true, "--sandbox-readonly": true,
	"--sandbox-memory": true, "--sandbox-cpus": true, "--sandbox-user": true,
	"--session": true,
}

// parseContinueArgs splits `odek continue` arguments into the optional
// --id, the per-turn run flags and the trailing task text. Every run flag
// that shapes a single turn (--max-iter, --thinking, --tool/--no-tool,
// --ctx, --no-color, --stream/--no-stream, --events-jsonl, budget caps,
// --external-ref, …) is accepted with the same syntax as `odek run`; the
// flags in continueRejectedFlags are refused with an explanation.
//
// Unknown flags are a hard error: they must never be folded into
// the task text, where a typo'd or version-drifted flag silently corrupts
// the prompt. An explicit "--" separator passes everything after it
// through verbatim.
func parseContinueArgs(args []string) (sessionID string, f runFlags, err error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case args[i] == "--id":
			if i+1 >= len(args) {
				return "", runFlags{}, fmt.Errorf("--id requires a value")
			}
			sessionID = args[i+1]
			i++
		case continueRejectedFlags[args[i]]:
			return "", runFlags{}, fmt.Errorf("flag %s is not accepted by odek continue — a resumed session keeps "+
				"its provider, model, endpoint, system prompt and sandbox posture; start a new session "+
				"with odek run --session to change them", args[i])
		default:
			rest = append(rest, args[i])
		}
	}
	f, err = parseRunFlags(rest)
	if err != nil {
		if err.Error() == "no task provided" {
			return "", runFlags{}, fmt.Errorf("no task provided for continue")
		}
		return "", runFlags{}, err
	}
	return sessionID, f, nil
}
