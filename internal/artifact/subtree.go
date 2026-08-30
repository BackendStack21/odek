package artifact

import (
	"os"
	"path/filepath"
	"regexp"
)

// sessionSubtreeRe constrains session ids used for artifact path derivation.
// The cascade deletes <root>/<session_id>; a hostile id must never escape
// the artifacts root (same threat model as the session store's own
// ValidateSessionID — enforced here independently so this package does not
// depend on internal/session).
var sessionSubtreeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// RemoveSessionSubtree removes <root>/<session_id> (the delegate_tasks
// artifact output of one session). The id is validated against a strict
// charset first; an invalid id is a silent no-op (nothing matched can exist
// under the root anyway, and cleanup must never turn into a traversal
// primitive). Missing subtrees are not an error.
func RemoveSessionSubtree(root, sessionID string) error {
	if root == "" || !sessionSubtreeRe.MatchString(sessionID) || sessionID == ".." {
		return nil
	}
	return os.RemoveAll(filepath.Join(root, sessionID))
}
