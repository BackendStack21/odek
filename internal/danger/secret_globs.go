package danger

import (
	"os"
	"path"
	"strings"
)

// A wildcard operand is expanded by the shell before the program sees it, so
// `cat ~/.odek/secrets.en?` opens the file the literal spelling names. The
// literal tables (sensitivePathFragments, the ~/.odek anchors) cannot see
// that, so a wildcard path is matched here as a pattern, segment by segment,
// against the same tables.

// globSegments splits a folded absolute path into its non-empty components.
func globSegments(p string) []string {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// globSegMatch matches one pattern component against one concrete component
// with shell semantics: a wildcard never matches a leading dot.
func globSegMatch(pat, seg string) bool {
	if pat == seg {
		return true
	}
	if !hasGlobMeta(pat) {
		return false
	}
	if strings.HasPrefix(seg, ".") && strings.ContainsAny(pat[:1], "*?[") {
		return false
	}
	ok, err := path.Match(pat, seg)
	if err != nil {
		return false
	}
	return ok
}

// globLiteralRun counts the literal bytes of a pattern component.
func globLiteralRun(pat string) int {
	n := 0
	for i := 0; i < len(pat); i++ {
		if !strings.ContainsRune("*?[]", rune(pat[i])) {
			n++
		}
	}
	return n
}

// globPrefixMatch reports whether the pattern components reach the candidate
// components or anything below them. The first skip candidate components are
// not compared (a home directory that the pattern itself spells).
func globPrefixMatch(pat, cand []string, skip int) bool {
	if len(pat) < len(cand) {
		return false
	}
	for i, c := range cand {
		if i < skip {
			continue
		}
		if !globSegMatch(pat[i], c) {
			return false
		}
	}
	return true
}

// globWindowMatch reports whether some run of pattern components matches the
// fragment components, anywhere in the path. The last component of the run
// must carry at least two literal bytes so `/etc/*` and `/tmp/??` stay
// ordinary while `/etc/sh*` and `~/.pgpas?` do not.
func globWindowMatch(pat, frag []string) bool {
	for start := 0; start+len(frag) <= len(pat); start++ {
		ok := true
		for k, f := range frag {
			if !globSegMatch(pat[start+k], f) {
				ok = false
				break
			}
		}
		if ok && (!hasGlobMeta(pat[start+len(frag)-1]) || globLiteralRun(pat[start+len(frag)-1]) >= 2) {
			return true
		}
	}
	return false
}

// globHomes returns the home directories (as folded component lists) whose
// ~/.odek tree a pattern may name, with the number of leading components that
// came from the pattern itself and therefore match by construction.
type globHome struct {
	segs []string
	skip int
}

func globHomes(abs string) []globHome {
	var out []globHome
	add := func(h string, skip int) {
		if h != "" {
			out = append(out, globHome{globSegments(FoldPathCase(h)), skip})
		}
	}
	if home, _ := os.UserHomeDir(); home != "" {
		add(home, 0)
		if r, err := resolvePathTarget(home); err == nil {
			add(r, 0)
		}
	}
	for _, h := range accountHomes(abs) {
		add(h, len(globSegments(h)))
	}
	return out
}

// globNamesSensitivePath reports whether a wildcard token can expand to a
// credential file, an odek trust anchor or a process environment file.
func globNamesSensitivePath(tok string) bool {
	expanded := expandShellTokenPath(tok)
	if !hasGlobMeta(expanded) {
		return false
	}
	abs, err := absPath(expanded)
	if err != nil {
		return false
	}
	abs = stripPrivate(abs)
	pat := globSegments(FoldPathCase(abs))
	if len(pat) == 0 {
		return false
	}
	for _, h := range globHomes(abs) {
		cand := append(append([]string{}, h.segs...), ".odek")
		if globPrefixMatch(pat, cand, h.skip) && len(pat) == len(cand) {
			return true
		}
		for _, name := range odekAnchorSampleNames {
			if globPrefixMatch(pat, append(append([]string{}, cand...), name), h.skip) {
				return true
			}
		}
	}
	for _, frag := range sensitivePathFragments {
		if frag == "/environ" {
			if globEnvironMatch(pat) {
				return true
			}
			continue
		}
		if !strings.HasPrefix(frag, "/") {
			// Key file names need a directory context, as the literal rule does.
			if len(pat) > 1 && globSegMatch(pat[len(pat)-1], frag) && globLiteralRun(pat[len(pat)-1]) >= 2 {
				return true
			}
			continue
		}
		if globWindowMatch(pat, globSegments(strings.ToLower(frag))) {
			return true
		}
	}
	return false
}

// globEnvironMatch reports a /proc/<pid>/environ spelling: a "proc" component,
// any process component, then a component that can be "environ".
func globEnvironMatch(pat []string) bool {
	for i := 0; i+2 < len(pat); i++ {
		if pat[i] == "proc" && globSegMatch(pat[i+2], "environ") && globLiteralRun(pat[i+2]) >= 2 {
			return true
		}
	}
	return false
}
