package main

import (
	"errors"
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

// continuePinnedFlag names the first parsed flag that a resumed session
// refuses: anything that would change the provider, model, endpoint, system
// prompt or sandbox posture the session was created with, and --session,
// which continue implies. Checking the parsed flags (not a name list) means
// a new pinning flag added to parseRunFlags cannot slip through unnoticed.
func continuePinnedFlag(f runFlags) string {
	switch {
	case f.Provider != "":
		return "--provider"
	case f.Model != "":
		return "--model"
	case f.BaseURL != "":
		return "--base-url"
	case f.System != "":
		return "--system"
	case f.Sandbox != nil && *f.Sandbox:
		return "--sandbox"
	case f.Sandbox != nil:
		return "--no-sandbox"
	case f.SandboxImage != "":
		return "--sandbox-image"
	case f.SandboxNetwork != "":
		return "--sandbox-network"
	case f.SandboxReadonly != nil:
		return "--sandbox-readonly"
	case f.SandboxMemory != "":
		return "--sandbox-memory"
	case f.SandboxCPUs != "":
		return "--sandbox-cpus"
	case f.SandboxUser != "":
		return "--sandbox-user"
	case f.Session != nil:
		return "--session"
	}
	return ""
}

// parseContinueArgs parses `odek continue` arguments with the run-flag
// parser: --id plus every run flag that shapes a single turn (--max-iter,
// --thinking, --tool/--no-tool, --ctx, --no-color, --stream/--no-stream,
// --events-jsonl, --deliver, budget caps, --external-ref, …) with the same
// syntax as `odek run`, then the task text. Flags that would change what the
// session pins are refused by name (continuePinnedFlag).
//
// Unknown flags are a hard error: they must never be folded into
// the task text, where a typo'd or version-drifted flag silently corrupts
// the prompt. An explicit "--" separator passes everything after it
// through verbatim.
func parseContinueArgs(args []string) (sessionID string, f runFlags, err error) {
	f, err = parseRunFlags(args)
	if err != nil {
		if errors.Is(err, errNoTask) {
			return "", runFlags{}, fmt.Errorf("no task provided for continue")
		}
		return "", runFlags{}, err
	}
	if flag := continuePinnedFlag(f); flag != "" {
		return "", runFlags{}, fmt.Errorf("flag %s is not accepted by odek continue — a resumed session keeps "+
			"its provider, model, endpoint, system prompt and sandbox posture; start a new session "+
			"with odek run --session to change them", flag)
	}
	sessionID, f.SessionID = f.SessionID, ""
	return sessionID, f, nil
}
