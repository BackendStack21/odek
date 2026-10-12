package danger

import (
	"path/filepath"
	"strings"
)

// systemSecretPaths are system files whose content is credential material.
var systemSecretPaths = map[string]bool{
	"/etc/shadow": true, "/etc/gshadow": true, "/etc/sudoers": true,
	"/etc/master.passwd": true, "/etc/security/opasswd": true,
}

// sensitiveDotDirs are directory names whose contents are credentials
// wherever they appear below the workspace root. .docker is deliberately
// absent: only its config.json holds credentials.
var sensitiveDotDirs = fieldSet(".aws .ssh .kube .gnupg .azure .gcloud .secrets")

// keyDirs are directory names that mark a *.key file as key material.
var keyDirs = fieldSet("ssl tls certs certificates private keys pki secrets .secrets")

// keyExactNames are whole file names (without .key) that mark key material.
var keyExactNames = fieldSet("server private priv privkey secret tls ssl cert client ca signing jwt api host")

// keyStrongTokens are name components (split on - _ .) that mark a *.key
// file as key material wherever they appear.
var keyStrongTokens = fieldSet("server private priv privkey secret tls ssl signing jwt")

// homeReadChildren narrows entries of homeSensitivePrefixes whose directory
// also holds ordinary files (editor config, plugins, build config) to the
// children that carry credentials. ClassifyPath keeps the whole prefix, since
// a write anywhere under it is worth a prompt; a read gate that refused the
// whole directory would block routine @-references from a home workspace.
// An entry absent here is refused whole, so a newly added table entry is
// protected by default. Matching is on path components.
var homeReadChildren = map[string][]string{
	"/.config": {"/gcloud", "/gh", "/hub", "/rclone", "/github-copilot", "/op", "/glab-cli",
		"/doctl", "/hcloud", "/heroku", "/configstore", "/helm", "/pip/pip.conf", "/git/credentials"},
	"/.docker":      {"/config.json"},
	"/.cargo":       {"/credentials", "/credentials.toml"},
	"/.gem":         {"/credentials"},
	"/.terraform.d": {"/credentials.tfrc.json"},
}

func stripPrivate(p string) string {
	if rest, ok := strings.CutPrefix(p, "/private/"); ok {
		return "/" + rest
	}
	return p
}

// IsSensitiveReadPath reports whether the absolute, symlink-resolved path
// holds material that must not be pulled into a prompt without an approval
// step: odek's trust anchors under ~/.odek, home credential directories,
// shell rc and history files, credential-shaped file names, and system secret
// files. Unlike ClassifyPath it does not rate a path by where it lives, so
// ordinary project files under /opt, /srv or /tmp are not sensitive. Directory
// name rules consider only components at or below root (when root is set and
// contains path), so an ancestor named "credentials" does not block a
// workspace.
func IsSensitiveReadPath(root, path string) bool {
	if path == "" {
		return false
	}
	abs := stripPrivate(filepath.Clean(path))
	base := FoldPathCase(filepath.Base(abs))
	dir := FoldPathCase(filepath.Dir(abs))
	if root != "" {
		r := FoldPathCase(stripPrivate(filepath.Clean(root)))
		if dir == r {
			dir = ""
		} else if rest, ok := strings.CutPrefix(dir, r+"/"); ok {
			dir = rest
		}
	}
	if sensitiveBase(base, dir) || sensitiveDir(dir, base) {
		return true
	}
	lower := FoldPathCase(abs)
	if systemSecretPaths[lower] || strings.HasPrefix(lower, "/etc/sudoers.d/") ||
		strings.HasPrefix(lower, "/etc/ssh/ssh_host_") {
		return true
	}
	for _, home := range accountHomes(abs) {
		if isOdekTrustAnchor(home, abs) {
			return true
		}
		if filepath.Dir(abs) == home {
			b := FoldPathCase(filepath.Base(abs))
			if shellRCFilesLower[b] || strings.HasSuffix(b, "_history") && strings.HasPrefix(b, ".") {
				return true
			}
		}
		lh := FoldPathCase(home)
		for _, sub := range homeSensitivePrefixes {
			children, narrowed := homeReadChildren[sub]
			if !narrowed {
				children = []string{""}
			}
			for _, c := range children {
				if p := lh + sub + c; lower == p || strings.HasPrefix(lower, p+"/") {
					return true
				}
			}
		}
		if lower == lh+"/.local/share/keyrings" || strings.HasPrefix(lower, lh+"/.local/share/keyrings/") {
			return true
		}
	}
	return false
}

// sensitiveBase reports credential-shaped file names. base and dir are folded.
func sensitiveBase(base, dir string) bool {
	switch {
	case base == ".envrc", strings.HasPrefix(base, ".env-"):
		return true
	case strings.HasPrefix(base, ".env."):
		for _, s := range []string{".example", ".sample", ".template", ".dist", ".defaults", ".default"} {
			if strings.HasSuffix(base, s) {
				return false
			}
		}
		return true
	case strings.HasSuffix(base, ".env") && len(base) > len(".env"):
		return true
	case strings.HasSuffix(base, ".key") && len(base) > len(".key"):
		return likelyKeyFile(base, dir)
	}
	return credentialFileBase(base)
}

func likelyKeyFile(base, dir string) bool {
	for _, d := range strings.Split(dir, "/") {
		if keyDirs[d] {
			return true
		}
	}
	name := strings.TrimSuffix(base, ".key")
	// Generic words (client, ca, api, cert) name key material only as the
	// whole file name; as a fragment they also appear in documents such as
	// Keynote decks ("presentation-client.key", "capital-plan.key").
	if keyExactNames[name] || strings.HasPrefix(name, "id_") {
		return true
	}
	for _, p := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if keyStrongTokens[p] {
			return true
		}
	}
	return false
}

// sensitiveDir reports whether a (folded, root-relative) directory path holds
// credentials.
func sensitiveDir(dir, base string) bool {
	if dir == "" {
		return false
	}
	parts := strings.Split(dir, "/")
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	for i, p := range parts {
		switch {
		case sensitiveDotDirs[p]:
			return true
		case p == ".docker" && base == "config.json":
			return true
		case i+1 < len(parts) && credentialDirPairs[p+"/"+parts[i+1]]:
			return true
		case credentialPlainDirs[p] && !credentialCodeExts[ext] && base != "...":
			return true
		}
	}
	return false
}
