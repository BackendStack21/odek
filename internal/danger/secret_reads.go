package danger

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Reading a credential into the model's context leaks it as surely as an
// environment dump does, so references to secret-bearing environment
// variables and reads or writes of well-known credential files are
// system_write regardless of the verb that carries them.

// sensitiveEnvSuffixes end the name of an environment variable that carries a
// secret. They are matched as plain suffixes so the bare word (TOKEN) and the
// fused form (AUTHTOKEN) are covered along with the usual _TOKEN spelling.
var sensitiveEnvSuffixes = []string{
	"TOKEN", "SECRET", "SECRET_KEY", "API_KEY", "APIKEY", "PASSWORD", "PASSWD",
	"PASSPHRASE", "PRIVATE_KEY", "PRIVATEKEY", "ACCESS_KEY", "CREDENTIALS",
	"CREDENTIAL", "DATABASE_URL",
}

// sensitiveEnvNames are well-known secret-bearing variables whose names do not
// end in one of the suffixes above.
var sensitiveEnvNames = map[string]bool{
	"AWS_ACCESS_KEY_ID": true, "MYSQL_PWD": true, "REDIS_URL": true,
	"MONGODB_URI": true, "MONGO_URL": true, "SENTRY_DSN": true,
}

// sensitiveEnvName reports whether an environment variable name marks a
// secret. Environment names are upper case by convention; a name with lower
// case letters (a shell loop variable such as $token) is not treated as one.
func sensitiveEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	if sensitiveEnvNames[name] {
		return true
	}
	if strings.HasPrefix(name, "ODEK_") && strings.Contains(name, "KEY") {
		return true
	}
	for _, suffix := range sensitiveEnvSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// envAccessCall matches the non-shell ways a program reads one variable:
// os.environ['X'], os.environ.get('X'), process.env.X, ENV["X"], ENVIRON["X"],
// getenv("X"), os.Getenv("X"), jq's env.X.
var envAccessCall = regexp.MustCompile(`\b(?i:environ|getenv|env)\b\s*(?:\[|\(|\.)\s*(?:get\s*\(\s*)?['"]?([A-Za-z_][A-Za-z0-9_]*)`)

// referencesSensitiveEnv reports whether text expands, or reads through a
// scripting language's environment accessor, a secret-bearing variable. It
// scans every quoting context: single-quoted text is passed to child shells
// and interpreters that expand it later.
func referencesSensitiveEnv(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '$' {
			continue
		}
		j := i + 1
		if j < len(text) && text[j] == '{' {
			j++
			if j < len(text) && (text[j] == '!' || text[j] == '#') {
				j++
			}
		}
		start := j
		for j < len(text) && isShellVarByte(text[j]) {
			j++
		}
		if sensitiveEnvName(text[start:j]) {
			return true
		}
	}
	for _, m := range envAccessCall.FindAllStringSubmatch(text, -1) {
		if sensitiveEnvName(m[1]) {
			return true
		}
	}
	return false
}

// secretNameOperand reports whether a command that prints variables by name
// (printenv NAME, declare -p NAME) names a secret-bearing one.
func secretNameOperand(name string, args []string) bool {
	switch name {
	case "printenv", "declare", "typeset":
	default:
		return false
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && sensitiveEnvName(a) {
			return true
		}
	}
	return false
}

// credentialDataExts are the extensions under which a `secrets.*` file holds
// data rather than source code.
var credentialDataExts = map[string]bool{
	"": true, "yaml": true, "yml": true, "json": true, "toml": true, "ini": true,
	"env": true, "conf": true, "cfg": true, "txt": true, "properties": true,
	"xml": true, "enc": true, "dec": true, "secret": true, "bak": true,
}

// credentialFileBase reports whether a path's final component names a file
// that conventionally holds credentials, wherever it lives.
func credentialFileBase(base string) bool {
	b := strings.ToLower(base)
	switch b {
	case ".env", "credentials", "credentials.json", "credentials.toml", ".netrc", "_netrc", ".npmrc", ".pypirc",
		".git-credentials", "kubeconfig", "terraform.tfstate", ".htpasswd",
		".pgpass", ".vault-token":
		return true
	}
	if strings.HasPrefix(b, ".env.") {
		switch strings.TrimPrefix(b, ".env.") {
		case "example", "sample", "template", "dist", "defaults", "default":
			return false
		}
		return true
	}
	if strings.HasPrefix(b, "service-account") || strings.HasPrefix(b, "service_account") {
		if strings.HasSuffix(b, ".json") {
			return true
		}
	}
	for _, key := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"} {
		if b == key || (strings.HasPrefix(b, key+".") && !strings.HasSuffix(b, ".pub")) {
			return true
		}
	}
	for _, ext := range []string{".pem", ".key", ".kubeconfig", ".tfvars", ".keystore", ".jks", ".p12", ".pfx", ".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(b, ext) && len(b) > len(ext) {
			return true
		}
	}
	if strings.HasPrefix(b, "secrets.") {
		return credentialDataExts[strings.TrimPrefix(filepath.Ext(b), ".")]
	}
	if b == "secrets" {
		return true
	}
	return false
}

// credentialDirs are directory names whose contents are credentials: the
// per-tool dot-directories always, and the plain secrets/credentials folders
// for files that are not source code or documentation.
var (
	credentialDotDirs   = fieldSet(".aws .ssh .kube .docker .gnupg .azure .gcloud .secrets")
	credentialPlainDirs = fieldSet("secrets credentials")
	credentialDirPairs  = map[string]bool{".config/gcloud": true, ".config/gh": true}
	// credentialCodeExts are extensions of source and documentation files,
	// which live in packages and folders that merely carry a secrets-like name
	// (internal/secrets/store.go, docs/credentials/README.md).
	credentialCodeExts = fieldSet("go rs py js mjs cjs ts tsx jsx rb java kt scala c h cc cpp hpp cs swift php lua " +
		"md rst adoc html css scss vue svelte sh bash zsh test snap proto sql lock mod sum")
)

// credentialDirectory reports whether a file sits under a directory that
// holds credentials. dir is the path before the file name; every component is
// examined, not only the last.
func credentialDirectory(dir, base string) bool {
	parts := strings.Split(strings.ToLower(dir), "/")
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), ".")
	for i, p := range parts {
		switch {
		case credentialDotDirs[p]:
			return true
		case i+1 < len(parts) && credentialDirPairs[p+"/"+parts[i+1]]:
			return true
		case credentialPlainDirs[p] && !credentialCodeExts[ext] && base != "...":
			return true
		}
	}
	return false
}

// credentialGlobSamples are representative credential file names a wildcard
// operand is tested against.
var credentialGlobSamples = []string{
	".env", ".env.local", "x.pem", "x.key", "x.p12", "x.pfx", "x.jks", "x.keystore",
	"x.tfvars", "x.kubeconfig", "x.tfstate", "credentials.json", "service-account.json",
	"id_rsa", "id_ed25519", ".netrc", ".npmrc", ".pypirc", ".git-credentials",
	"secrets.yaml", "kubeconfig",
}

// credentialPathToken reports whether tok (an argument, an `if=`/`of=` or
// `--opt=` value, or a redirect source or target) names a credential file.
func credentialPathToken(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.HasPrefix(tok, "-") {
		_, v, ok := strings.Cut(tok, "=")
		if !ok || !strings.HasPrefix(tok, "--") {
			return false
		}
		tok = v
	} else if k, v, ok := strings.Cut(tok, "="); ok && (k == "if" || k == "of") {
		tok = v
	}
	base := tok
	if i := strings.LastIndexByte(tok, '/'); i >= 0 {
		base = tok[i+1:]
	}
	if base == "" {
		return false
	}
	if credentialFileBase(base) {
		return true
	}
	if i := strings.LastIndexByte(tok, '/'); i > 0 && credentialDirectory(tok[:i], base) {
		return true
	}
	if strings.ContainsAny(base, "*?[") {
		// A wildcard operand needs a literal run to be about credentials at
		// all: `cat *` and `cat *.*` are ordinary, `cat *.pem` is not.
		literal, longest := 0, 0
		for i := 0; i < len(base); i++ {
			if strings.IndexByte("*?[]", base[i]) >= 0 {
				literal = 0
				continue
			}
			literal++
			if literal > longest {
				longest = literal
			}
		}
		if longest < 3 {
			return false
		}
		for _, sample := range credentialGlobSamples {
			if ok, _ := filepath.Match(strings.ToLower(base), sample); ok {
				return true
			}
		}
	}
	return false
}

// metadataOnlyVerbs report names, sizes or types without printing file
// contents, so a credential file operand does not make them a secret read.
var metadataOnlyVerbs = map[string]bool{
	"ls": true, "stat": true, "test": true, "[": true, "[[": true, "wc": true,
	"du": true, "file": true,
}

// patternOperandVerbs take a search pattern as their first operand; the
// pattern is not a file even when it spells a credential file name.
var patternOperandVerbs = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "zgrep": true, "rg": true,
	"ag": true, "ack": true, "git-grep": true,
}

// nameMatchOptions take a file-name pattern, not a file to open.
var nameMatchOptions = map[string]bool{
	"-name": true, "-iname": true, "-path": true, "-ipath": true,
	"-wholename": true, "-iwholename": true, "-regex": true, "-iregex": true,
	"-g": true, "--glob": true, "--iglob": true, "--include": true,
	"--exclude": true, "--exclude-dir": true, "--ignore-glob": true,
}

// stageTouchesCredentialFile reports whether a stage reads or writes a
// credential file. stage is the whole stage (redirects included) and inner the
// command after wrappers. For a display verb only redirect targets count.
func stageTouchesCredentialFile(stage, inner []string, display bool) bool {
	name := ""
	if len(inner) > 0 {
		name = commandName(inner[0])
	}
	if metadataOnlyVerbs[name] {
		// A redirect still opens its file.
		for i := 1; i < len(stage); i++ {
			if (isRedirectToken(stage[i-1]) || stage[i-1] == "<") && credentialPathToken(stage[i]) {
				return true
			}
		}
		return false
	}
	skipPattern := patternOperandVerbs[name]
	if skipPattern {
		for _, tok := range inner[1:] {
			if tok == "-e" || tok == "--regexp" || strings.HasPrefix(tok, "--regexp=") || tok == "-f" || tok == "--file" {
				skipPattern = false // the patterns are named by the option
				break
			}
		}
	}
	innerStart := len(stage) - len(inner)
	for i, tok := range stage {
		if i > 0 && isRedirectToken(stage[i-1]) {
			if credentialPathToken(tok) {
				return true
			}
			continue
		}
		if display {
			continue
		}
		if i > 0 && nameMatchOptions[stage[i-1]] {
			continue
		}
		if i >= innerStart+1 && name != "" {
			if skipPattern && !strings.HasPrefix(tok, "-") {
				skipPattern = false
				continue
			}
			if i > 0 && (stage[i-1] == "-e" || stage[i-1] == "--regexp") && patternOperandVerbs[name] {
				continue
			}
		}
		if i < innerStart && !isAssignment(tok) && !strings.HasPrefix(tok, "-") {
			continue
		}
		if credentialPathToken(tok) {
			return true
		}
	}
	return false
}
