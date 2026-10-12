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

// IsSensitiveReadPath reports whether the absolute, symlink-resolved path
// holds material that must not be pulled into a prompt without an approval
// step: odek's trust anchors under ~/.odek, home credential directories and
// shell rc files, credential-shaped file names anywhere, and system secret
// files. Unlike ClassifyPath it does not rate a path by where it lives, so
// ordinary project files under /opt, /srv or /tmp are not sensitive.
func IsSensitiveReadPath(path string) bool {
	if path == "" {
		return false
	}
	abs := filepath.Clean(path)
	// macOS reports /var, /tmp and /etc under /private once resolved.
	if rest, ok := strings.CutPrefix(abs, "/private/"); ok {
		abs = "/" + rest
	}
	base := filepath.Base(abs)
	if credentialFileBase(base) {
		return true
	}
	if credentialDirectory(filepath.Dir(abs), base) {
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
		if filepath.Dir(abs) == home && shellRCFilesLower[FoldPathCase(base)] {
			return true
		}
		lh := FoldPathCase(home)
		for _, sub := range []string{"/.ssh", "/.gnupg", "/.aws", "/.kube", "/.azure",
			"/.password-store", "/.config/gcloud", "/.config/gh", "/.docker/config.json"} {
			if lower == lh+sub || strings.HasPrefix(lower, lh+sub+"/") {
				return true
			}
		}
	}
	return false
}
