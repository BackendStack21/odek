package danger

import "strings"

// ── gpg ────────────────────────────────────────────────────────────────

// gpgProgramOptions name a program gpg runs, a configuration file that can
// name one, or a helper search path.
var gpgProgramOptions = []string{
	"photo-viewer", "exec-path", "keyserver-program", "agent-program",
	"dirmngr-program", "options", "gpg-agent-info", "show-photos",
}

// gpgKeyringVerbs change the keyring, the trust database or the key material
// on disk (or fetch keys into them): irreversible credential changes.
var gpgKeyringVerbs = []string{
	"delete-key", "delete-keys", "delete-secret-key", "delete-secret-keys",
	"delete-secret-and-public-key", "delete-secret-and-public-keys",
	"import", "fast-import", "import-ownertrust", "recv-keys", "receive-keys",
	"refresh-keys", "send-keys", "edit-key", "card-edit", "change-passphrase",
	"passwd", "sign-key", "lsign-key", "gen-key", "generate-key",
	"full-generate-key", "full-gen-key", "quick-generate-key", "quick-gen-key",
	"quick-add-uid", "quick-adduid", "quick-revoke-uid", "quick-revoke-sig",
	"quick-sign-key", "quick-lsign-key", "quick-set-expire",
	"quick-set-primary-uid", "quick-add-key", "quick-addkey",
	"generate-designated-revoker", "desig-revoke", "locate-keys",
	"auto-key-locate", "search-keys", "update-trustdb", "check-trustdb",
	"rebuild-keydb-caches",
}

// gpgLongOptions yields the long option names of a gpg command line, up to
// the `--` terminator. A value-less value is cut at `=`.
func gpgLongOptions(tokens []string) (opts []string, values []string) {
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "--" {
			break
		}
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, val, hasVal := strings.Cut(tok[2:], "=")
		if !hasVal && i+1 < len(tokens) {
			val = tokens[i+1]
		}
		opts = append(opts, name)
		values = append(values, val)
	}
	return opts, values
}

// gpgOptionIs reports whether opt names target, or is an abbreviation of it
// that getopt_long would accept (an ambiguous prefix is a tool error, so
// flagging it costs nothing).
func gpgOptionIs(opt, target string) bool {
	if opt == target {
		return true
	}
	// A complete option of its own is not an abbreviation of a longer one.
	if gpgPlainOptions[opt] {
		return false
	}
	return len(opt) >= 3 && strings.HasPrefix(target, opt)
}

// gpgPlainOptions are complete gpg options that are also prefixes of an
// option that runs a program or changes keys.
var gpgPlainOptions = map[string]bool{
	"keyserver": true, "import-options": true, "export": true,
}

// gpgRunsProgram reports whether a gpg invocation runs an arbitrary program:
// a photo viewer, a keyserver or agent helper, an alternative PATH for the
// helpers, or a configuration file that can name any of them. The
// show-photos list/verify option runs the configured photo viewer.
func gpgRunsProgram(name string, tokens []string) bool {
	if name != "gpg" && name != "gpg2" && name != "gpgv" {
		return false
	}
	opts, values := gpgLongOptions(tokens)
	for i, opt := range opts {
		for _, target := range gpgProgramOptions {
			if gpgOptionIs(opt, target) {
				return true
			}
		}
		if gpgOptionIs(opt, "list-options") || gpgOptionIs(opt, "verify-options") ||
			gpgOptionIs(opt, "export-options") || gpgOptionIs(opt, "import-options") {
			if strings.Contains(values[i], "photo") {
				return true
			}
		}
	}
	return false
}

// gpgMutatesKeyring reports whether a gpg invocation deletes, imports,
// generates, signs or otherwise rewrites key material, or fetches keys into
// the keyring.
func gpgMutatesKeyring(name string, tokens []string) bool {
	if name != "gpg" && name != "gpg2" {
		return false
	}
	opts, _ := gpgLongOptions(tokens)
	for _, opt := range opts {
		if strings.HasPrefix(opt, "delete") {
			return true
		}
		for _, verb := range gpgKeyringVerbs {
			if gpgOptionIs(opt, verb) {
				return true
			}
		}
	}
	return false
}
