package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/logquery"
)

func logsCmd(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	level := fs.String("level", "", "minimum level: debug, info, warn, error")
	since := fs.String("since", "", "lower time bound (duration ago or RFC3339)")
	until := fs.String("until", "", "upper time bound (RFC3339)")
	surface := fs.String("surface", "", "filter by surface")
	status := fs.String("status", "", "filter by status")
	session := fs.String("session", "", "filter by session id")
	turn := fs.String("turn", "", "filter by turn id")
	run := fs.String("run", "", "filter by run id")
	tree := fs.Bool("tree", false, "include the run and its descendant runs")
	errorCode := fs.String("error-code", "", "filter by error code")
	follow := fs.Bool("follow", false, "follow new records")
	jsonOut := fs.Bool("json", false, "print matching records as JSONL")
	limit := fs.Int("limit", 100, "maximum initial records (0 means unlimited)")
	path := fs.String("file", "", "runtime log path override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *tree && *run == "" {
		return errors.New("--tree requires --run")
	}
	if *limit < 0 {
		return errors.New("--limit must be non-negative")
	}
	if *level != "" {
		switch strings.ToLower(*level) {
		case "debug", "info", "warn", "warning", "error":
		default:
			return fmt.Errorf("invalid --level %q", *level)
		}
	}
	start, err := parseLogTime(*since, true)
	if err != nil {
		return fmt.Errorf("invalid --since: %w", err)
	}
	end, err := parseLogTime(*until, false)
	if err != nil {
		return fmt.Errorf("invalid --until: %w", err)
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return errors.New("--until must be after --since")
	}
	logging := config.LoadLoggingConfig()
	pathValue := logging.File
	if pathValue == "" {
		pathValue = defaultRuntimeLogPath()
	}
	if *level == "" {
		*level = logging.Level
	}
	if *path != "" {
		pathValue = *path
	}
	pathValue = expandHome(pathValue)
	filt := logquery.Filter{Level: *level, Since: start, Until: end, Surface: *surface, Status: *status, SessionID: *session, TurnID: *turn, RunID: *run, ErrorCode: *errorCode, Tree: *tree, TrackIDs: *follow}
	paths := retainedLogPaths(pathValue, logging.MaxFiles)
	result, err := logquery.Read(paths, filt, *limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, r := range result.Records {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "odek: showing retained runtime log records; older records may have been rotated or pruned")
		if *tree {
			renderRunTree(result.Records)
		} else {
			for _, r := range result.Records {
				renderLogRecord(os.Stdout, r)
			}
		}
	}
	if result.Malformed > 0 {
		fmt.Fprintf(os.Stderr, "odek: skipped %d malformed or oversized runtime log record(s)\n", result.Malformed)
	}
	if result.Truncated {
		fmt.Fprintf(os.Stderr, "odek: query record cap reached; older retained records were omitted\n")
	}
	if *follow {
		return followLogs(pathValue, logging.MaxFiles, filt, *jsonOut, result.Positions, result.SeenIDRanges, result.TreeRunIDs)
	}
	return nil
}

func defaultRuntimeLogPath() string { return filepath.Join(expandHome("~/.odek"), "runtime.log") }

func retainedLogPaths(path string, generations int) []string {
	if generations < 1 {
		generations = 1
	}
	paths := make([]string, 0, generations)
	for i := generations - 1; i >= 1; i-- {
		paths = append(paths, fmt.Sprintf("%s.%d", path, i))
	}
	paths = append(paths, path)
	return paths
}

func parseLogTime(s string, allowDuration bool) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if allowDuration {
		if d, e := time.ParseDuration(s); e == nil {
			if d < 0 {
				return time.Time{}, errors.New("duration must be positive")
			}
			return time.Now().Add(-d), nil
		}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("use a duration such as 30m or an RFC3339 timestamp")
	}
	return t, nil
}

func renderLogRecord(w io.Writer, r logquery.Record) {
	t := r.Timestamp.Local().Format("2006-01-02 15:04:05")
	ids := []string{}
	if r.SessionID != "" {
		ids = append(ids, "session="+safeCell(r.SessionID))
	}
	if r.RunID != "" {
		ids = append(ids, "run="+safeCell(r.RunID))
	}
	if r.Status != "" {
		ids = append(ids, "status="+safeCell(r.Status))
	}
	if r.DurationMS > 0 {
		ids = append(ids, fmt.Sprintf("%dms", r.DurationMS))
	}
	if r.Error != nil && r.Error.Code != "" {
		ids = append(ids, "error="+safeCell(r.Error.Code))
	}
	fmt.Fprintf(w, "%s %-5s %-28s", t, strings.ToUpper(r.Level), safeCell(r.Event))
	if r.Surface != "" {
		fmt.Fprintf(w, " [%s]", safeCell(r.Surface))
	}
	if len(ids) > 0 {
		fmt.Fprintf(w, " %s", strings.Join(ids, " "))
	}
	fmt.Fprintln(w)
}

func safeCell(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			b.WriteRune('�')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderRunTree(records []logquery.Record) {
	type summary struct {
		id, parent, status string
		duration           int64
		last               time.Time
	}
	m := map[string]*summary{}
	for _, r := range records {
		if r.RunID == "" {
			continue
		}
		s := m[r.RunID]
		if s == nil {
			s = &summary{id: r.RunID, parent: r.ParentRunID}
			m[r.RunID] = s
		}
		if r.Status != "" {
			s.status = r.Status
		}
		if r.DurationMS > s.duration {
			s.duration = r.DurationMS
		}
		if r.Timestamp.After(s.last) {
			s.last = r.Timestamp
		}
	}
	children := map[string][]*summary{}
	roots := []*summary{}
	for _, s := range m {
		if p := m[s.parent]; p != nil {
			children[p.id] = append(children[p.id], s)
		} else {
			roots = append(roots, s)
		}
	}
	byID := func(v []*summary) {
		sort.Slice(v, func(i, j int) bool {
			if v[i].last.Equal(v[j].last) {
				return v[i].id < v[j].id
			}
			return v[i].last.Before(v[j].last)
		})
	}
	byID(roots)
	for _, v := range children {
		byID(v)
	}
	type entry struct {
		run   *summary
		depth int
	}
	visited := make(map[string]bool, len(m))
	print := func(root *summary) {
		stack := []entry{{run: root}}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if visited[n.run.id] {
				continue
			}
			visited[n.run.id] = true
			depth := n.depth
			if depth > 32 {
				depth = 32
			}
			fmt.Printf("%s%s parent=%s status=%s duration=%dms last=%s\n", strings.Repeat("  ", depth), safeCell(n.run.id), safeCell(n.run.parent), safeCell(n.run.status), n.run.duration, n.run.last.Local().Format("15:04:05"))
			for i := len(children[n.run.id]) - 1; i >= 0; i-- {
				child := children[n.run.id][i]
				if !visited[child.id] {
					stack = append(stack, entry{child, n.depth + 1})
				}
			}
		}
	}
	for _, r := range roots {
		print(r)
	}
	if len(visited) < len(m) {
		remaining := make([]*summary, 0, len(m)-len(visited))
		for _, s := range m {
			if !visited[s.id] {
				remaining = append(remaining, s)
			}
		}
		byID(remaining)
		for _, s := range remaining {
			print(s)
		}
	}
}

type fileCursor struct {
	dev, ino     uint64
	offset, size int64
}

func (c fileCursor) key() string { return fmt.Sprintf("%d:%d", c.dev, c.ino) }
func followLogs(path string, generations int, filt logquery.Filter, jsonOut bool, positions []logquery.FilePosition, initialIDs []logquery.IDRange, initialTreeRuns []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ids := logquery.NewIDIndex()
	for _, id := range initialIDs {
		if err := ids.AddRange(id); err != nil {
			return err
		}
	}
	treeRuns := make(map[string]bool, len(initialTreeRuns))
	for _, id := range initialTreeRuns {
		treeRuns[id] = true
	}
	return followLogsContext(ctx, path, generations, filt, jsonOut, positions, ids, treeRuns)
}

func followLogsContext(ctx context.Context, path string, generations int, filt logquery.Filter, jsonOut bool, positions []logquery.FilePosition, recordIDs *logquery.IDIndex, treeRuns map[string]bool) error {
	seen := map[string]fileCursor{}
	paths := retainedLogPaths(path, generations)
	// Continue from the exact complete-line offsets consumed by the initial
	// query. This closes the read-to-follow gap without replaying retained lines.
	for _, p := range positions {
		c := fileCursor{dev: p.Device, ino: p.Inode, offset: p.Offset, size: p.Size}
		seen[c.key()] = c
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		activeKeys := make(map[string]bool, len(paths))
		for _, p := range paths {
			recs, bad, key, err := readFollowPath(p, seen)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "odek: follow: %v\n", err)
				continue
			}
			activeKeys[key] = true
			if bad > 0 && !jsonOut {
				fmt.Fprintf(os.Stderr, "odek: follow skipped %d malformed or oversized runtime log record(s)\n", bad)
			}
			selected, err := selectFollowRecords(recs, recordIDs, filt, treeRuns)
			if err != nil {
				return err
			}
			for _, r := range selected {
				if jsonOut {
					b, e := json.Marshal(r)
					if e == nil {
						fmt.Fprintln(os.Stdout, string(b))
					}
				} else {
					renderLogRecord(os.Stdout, r)
				}
			}
		}
		retainActiveCursors(seen, activeKeys)
	}
}

func selectFollowRecords(records []logquery.Record, ids *logquery.IDIndex, f logquery.Filter, treeRuns map[string]bool) ([]logquery.Record, error) {
	var out []logquery.Record
	for _, r := range records {
		added, err := ids.Add(r.RecordID)
		if err != nil {
			return nil, err
		}
		if added && followMatches(r, f, treeRuns) {
			out = append(out, r)
		}
	}
	return out, nil
}

func followMatches(r logquery.Record, f logquery.Filter, treeRuns map[string]bool) bool {
	if f.Tree {
		included := r.RunID == f.RunID || treeRuns[r.RunID] || treeRuns[r.ParentRunID] || r.RootRunID == f.RunID
		if !included {
			return false
		}
		if r.RunID != "" {
			treeRuns[r.RunID] = true
		}
	}
	return logquery.Matches(r, f)
}

func retainActiveCursors(seen map[string]fileCursor, active map[string]bool) {
	for key := range seen {
		if !active[key] {
			delete(seen, key)
		}
	}
}

func readFollowPath(path string, seen map[string]fileCursor) ([]logquery.Record, int, string, error) {
	f, err := openLogRead(path)
	if err != nil {
		return nil, 0, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, "", err
	}
	id := identity(st)
	if !st.Mode().IsRegular() {
		return nil, 0, "", fmt.Errorf("log must be a regular file: %s", path)
	}
	cur, ok := seen[id.key()]
	if !ok {
		cur = id
	} else if st.Size() < cur.size {
		cur.offset = 0
	}
	recs, next, bad, err := logquery.CopyAvailable(f, cur.offset)
	if err != nil {
		return nil, bad, "", err
	}
	cur.offset = next
	cur.size = st.Size()
	seen[id.key()] = cur
	return recs, bad, id.key(), nil
}

func identity(st os.FileInfo) fileCursor {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return fileCursor{dev: uint64(s.Dev), ino: uint64(s.Ino)}
	}
	return fileCursor{}
}
func statCursor(p string) (fileCursor, bool) {
	f, e := openLogRead(p)
	if e != nil {
		return fileCursor{}, false
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return fileCursor{}, false
	}
	c := identity(st)
	c.offset, _ = f.Seek(0, io.SeekEnd)
	c.size = st.Size()
	return c, true
}
func openLogRead(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
