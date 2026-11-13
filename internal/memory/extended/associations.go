package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/session"
)

// Associations stores bidirectional links between related atoms.
type Associations struct {
	mu      sync.RWMutex
	dir     string
	links   map[string]map[string]struct{}
	exists  func(id string) bool
	recency func() map[string]time.Time
	removed map[string]struct{}
}

// maxLinksPerAtom is how many nearest-in-time neighbours each atom selects
// when the association map is pruned; see pruneLocked. Recall only surfaces an
// atom's neighbours as its related-atom list, so near-in-time links carry the
// value and the bound keeps associations.json linear in the atom count.
// Stores written before the bound existed (task atoms linked as a complete
// graph) load unchanged and are pruned the next time the map is persisted
// past the trigger line (needsPrune).
const maxLinksPerAtom = 24

// maxLinkFactor is the multiple of the links a freshly pruned map can hold
// between crowded atoms that the map may reach before a prune runs, so
// pruning (which reads every atom's creation time) is amortised over many
// adds instead of firing on each one.
const maxLinkFactor = 2

// maxTaskLinksPerAtom is how many of the most recent same-project task atoms
// a new goal/decision/convention/intent atom links to.
const maxTaskLinksPerAtom = 8

// maxTemporalLinksPerAtom is how many same-session atoms of nearby turns a new
// atom links to: the nearest turns first, then the newest.
const maxTemporalLinksPerAtom = 8

// NewAssociations returns an in-memory association map.
func NewAssociations() *Associations {
	return &Associations{
		links: make(map[string]map[string]struct{}),
	}
}

// NewAssociationsWithDir returns an Associations that persists to dir.
func NewAssociationsWithDir(dir string) *Associations {
	a := NewAssociations()
	a.dir = dir
	_ = a.Load()
	return a
}

// Link creates an undirected link between two atoms.
func (a *Associations) Link(fromID, toID string) {
	if a == nil || fromID == "" || toID == "" || fromID == toID {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.removed, fromID)
	delete(a.removed, toID)
	a.linkOne(fromID, toID)
	a.linkOne(toID, fromID)
}

func (a *Associations) linkOne(fromID, toID string) {
	if a.links[fromID] == nil {
		a.links[fromID] = make(map[string]struct{})
	}
	a.links[fromID][toID] = struct{}{}
}

// Related returns the atom IDs linked to id, sorted.
func (a *Associations) Related(id string) []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	related, ok := a.links[id]
	if !ok || len(related) == 0 {
		return nil
	}
	out := make([]string, 0, len(related))
	for toID := range related {
		out = append(out, toID)
	}
	sort.Strings(out)
	return out
}

// RemoveAtom removes all links to and from an atom.
func (a *Associations) RemoveAtom(id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.removed == nil {
		a.removed = make(map[string]struct{})
	}
	a.removed[id] = struct{}{}
	delete(a.links, id)
	for fromID, related := range a.links {
		delete(related, id)
		if len(related) == 0 {
			delete(a.links, fromID)
		}
	}
}

// Persist saves the association map to disk. The file is shared with other
// processes (a CLI forget/prune beside a running daemon), so the write runs
// under a cross-process lock and merges with what is on disk: links another
// process added are kept, links to atoms this process removed are dropped,
// and links this process holds for atoms that no longer exist (when a
// liveness check is installed) are not written back.
func (a *Associations) Persist() error {
	if a == nil || a.dir == "" {
		return nil
	}
	if err := os.MkdirAll(a.dir, 0700); err != nil {
		return fmt.Errorf("associations: mkdir: %w", err)
	}
	unlock, err := flock.Lock(filepath.Join(a.dir, "associations.lock"))
	if err != nil {
		return fmt.Errorf("associations: lock: %w", err)
	}
	defer unlock()

	a.mu.Lock()
	defer a.mu.Unlock()

	disk := a.readDisk()
	merged := make(map[string]map[string]struct{})
	add := func(from, to string) {
		if merged[from] == nil {
			merged[from] = make(map[string]struct{})
		}
		merged[from][to] = struct{}{}
	}
	for from, list := range disk {
		if _, ok := a.removed[from]; ok {
			continue
		}
		for _, to := range list {
			if _, ok := a.removed[to]; ok {
				continue
			}
			add(from, to)
			add(to, from)
		}
	}
	alive := map[string]bool{}
	live := func(id string) bool {
		if a.exists == nil {
			return true
		}
		v, ok := alive[id]
		if !ok {
			v = a.exists(id)
			alive[id] = v
		}
		return v
	}
	for from, related := range a.links {
		for to := range related {
			if _, ok := merged[from][to]; ok {
				continue
			}
			// Not on disk: either new here, or removed by another process.
			if !live(from) || !live(to) {
				continue
			}
			add(from, to)
			add(to, from)
		}
	}
	merged = a.pruneLocked(merged)
	// Adopt the merged view so later persists start from the shared state.
	a.links = merged
	data := make(map[string][]string, len(merged))
	for id, related := range merged {
		if session.ValidateSessionID(id) != nil {
			continue
		}
		list := make([]string, 0, len(related))
		for toID := range related {
			if session.ValidateSessionID(toID) != nil {
				continue
			}
			list = append(list, toID)
		}
		if len(list) > 0 {
			sort.Strings(list)
			data[id] = list
		}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("associations: marshal: %w", err)
	}
	file := filepath.Join(a.dir, "associations.json")
	if err := fsatomic.WriteFile(file, raw, 0600); err != nil {
		return fmt.Errorf("associations: write: %w", err)
	}
	return nil
}

// readDisk returns the persisted links, or nil when the file is absent or
// unreadable (the merge then writes this process's view alone).
func (a *Associations) readDisk() map[string][]string {
	raw, err := os.ReadFile(filepath.Join(a.dir, "associations.json"))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var m map[string][]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// Load reads the association map from disk.
func (a *Associations) Load() error {
	if a == nil || a.dir == "" {
		return nil
	}
	file := filepath.Join(a.dir, "associations.json")
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("associations: read: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("associations: parse: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, list := range raw {
		if session.ValidateSessionID(id) != nil {
			continue
		}
		for _, toID := range list {
			if session.ValidateSessionID(toID) != nil {
				continue
			}
			a.linkOne(id, toID)
			a.linkOne(toID, id)
		}
	}
	return nil
}

// SetExists installs the liveness check used to drop links to atoms that no
// longer exist in the store.
func (a *Associations) SetExists(fn func(id string) bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.exists = fn
	a.mu.Unlock()
}

// Restore clears the removal marks of ids that were put back in the store, so
// their persisted links survive later merges.
func (a *Associations) Restore(ids ...string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		delete(a.removed, id)
	}
}

// SetRecency installs the lookup used to rank an atom's links when the
// per-atom bound applies: newer neighbours win. Without it, ties fall back to
// the id order.
func (a *Associations) SetRecency(fn func() map[string]time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.recency = fn
	a.mu.Unlock()
}

// needsPrune reports whether links (a symmetric map) is past the prune
// trigger. Call an atom with more than maxLinksPerAtom links crowded: an atom
// that is not crowded selects every one of its links, so only a link between
// two crowded atoms can be dropped by a prune. A prune keeps such a link only
// when one of its ends selected it, so a freshly pruned map holds at most
// maxLinksPerAtom of them per crowded atom; the trigger fires once they
// exceed maxLinkFactor times that. Counting links per crowded atom, not per
// atom of the whole store, a dense cluster fires however large a sparse rest
// of the store is, while a hub linked to many sparse atoms never does. Below
// the trigger the map holds at most maxLinkFactor*maxLinksPerAtom times as
// many links as it has linked atoms (a total; one atom may hold more). It
// reads link counts only, never the recency table.
func needsPrune(links map[string]map[string]struct{}) bool {
	crowded, prunable := 0, 0
	for _, related := range links {
		if len(related) <= maxLinksPerAtom {
			continue
		}
		crowded++
		for to := range related {
			if len(links[to]) > maxLinksPerAtom {
				prunable++
			}
		}
	}
	// prunable counts each link between crowded atoms from both ends.
	return prunable > 2*maxLinkFactor*maxLinksPerAtom*crowded
}

// PruneIfDense persists the map when it is past the prune trigger, so the
// size of associations.json reflects the pruned map before it is charged
// against the store's size cap. Below the trigger it does nothing.
func (a *Associations) PruneIfDense() error {
	if a == nil || a.dir == "" {
		return nil
	}
	a.mu.RLock()
	dense := needsPrune(a.links)
	a.mu.RUnlock()
	if !dense {
		return nil
	}
	return a.Persist()
}

// pruneLocked bounds the association map. It does nothing below the trigger
// (needsPrune), so a healthy store never reads the atom recency table (a full
// chunk read); past it each atom selects its maxLinksPerAtom nearest-in-time
// neighbours and a link is kept when EITHER end selected it. Every atom that
// had a link keeps at least one (its own selection), so pruning never strands
// an atom, and the result is O(N): at most maxLinksPerAtom*N links after a
// prune and at most maxLinkFactor*maxLinksPerAtom*N before the next one. An
// atom kept by many other atoms may hold more than maxLinksPerAtom links. A
// freshly pruned map is below the trigger, so the next persist does not prune
// again. The order is total (time distance, then distance in id order, then
// id) so the outcome is deterministic, and pruning an already pruned map
// changes nothing: a kept link stays within its selecting end's nearest set
// when the other candidates only shrink. Ties (equal or unknown creation
// times) go to the neighbours nearest in id order rather than to the lowest
// ids, so a cluster without usable times does not turn its lowest ids into
// hubs that every other atom selects.
// Caller must hold a.mu.
func (a *Associations) pruneLocked(links map[string]map[string]struct{}) map[string]map[string]struct{} {
	if !needsPrune(links) {
		return links
	}
	var created map[string]time.Time
	if a.recency != nil {
		created = a.recency()
	}
	distance := func(x, y string) (int64, bool) {
		tx, okx := created[x]
		ty, oky := created[y]
		if !okx || !oky || tx.IsZero() || ty.IsZero() {
			return 0, false
		}
		d := int64(tx.Sub(ty))
		if d < 0 {
			d = -d
		}
		if d < 0 { // saturated Duration
			d = 1<<63 - 1
		}
		return d, true
	}
	order := make([]string, 0, len(links))
	for id := range links {
		order = append(order, id)
	}
	sort.Strings(order)
	rank := make(map[string]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	rankDistance := func(x, y string) int {
		d := rank[x] - rank[y]
		if d < 0 {
			d = -d
		}
		return d
	}
	pruned := make(map[string]map[string]struct{}, len(links))
	keep := func(x, y string) {
		for _, p := range [2][2]string{{x, y}, {y, x}} {
			if pruned[p[0]] == nil {
				pruned[p[0]] = make(map[string]struct{})
			}
			pruned[p[0]][p[1]] = struct{}{}
		}
	}
	for id, related := range links {
		ids := make([]string, 0, len(related))
		for to := range related {
			ids = append(ids, to)
		}
		sort.Slice(ids, func(i, j int) bool {
			di, oki := distance(id, ids[i])
			dj, okj := distance(id, ids[j])
			if oki != okj {
				return oki // a known distance beats an unknown one
			}
			if di != dj {
				return di < dj
			}
			if ri, rj := rankDistance(id, ids[i]), rankDistance(id, ids[j]); ri != rj {
				return ri < rj
			}
			return ids[i] < ids[j]
		})
		if len(ids) > maxLinksPerAtom {
			ids = ids[:maxLinksPerAtom]
		}
		for _, to := range ids {
			keep(id, to)
		}
	}
	return pruned
}

// FileSize returns the size of the persisted associations.json in bytes.
func (a *Associations) FileSize() int64 {
	if a == nil || a.dir == "" {
		return 0
	}
	info, err := os.Stat(filepath.Join(a.dir, "associations.json"))
	if err != nil {
		return 0
	}
	return info.Size()
}
