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
			return ref, fmt.Errorf("invalid --external-ref %q: want kind=uri or kind=...,uri=...,created_by=...", spec)
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

// parseContinueArgs splits `odek continue` arguments into the optional
// --id / --external-ref flags (front-positioned, repeatable for the
// latter) and the trailing task text.
func parseContinueArgs(args []string) (sessionID string, refSpecs []string, task string, err error) {
	i := 0
	for i < len(args) {
		if args[i] == "--id" && i+1 < len(args) {
			sessionID = args[i+1]
			i += 2
			continue
		}
		if args[i] == "--external-ref" && i+1 < len(args) {
			refSpecs = append(refSpecs, args[i+1])
			i += 2
			continue
		}
		break
	}
	if i >= len(args) {
		return "", nil, "", fmt.Errorf("no task provided for continue")
	}
	return sessionID, refSpecs, strings.Join(args[i:], " "), nil
}
