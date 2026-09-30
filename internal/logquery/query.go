// Package logquery provides read-only, bounded queries over runtime log JSONL.
package logquery

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/runtimelog"
)

const MaxLineBytes = 1 << 20
const MaxRecords = 200_000
const MaxStoredBytes = 16 << 20
const MaxIDRanges = 1_000_000
const MaxIDProcesses = 4096
const MaxRecordIDBytes = 128
const MaxProcessIDBytes = 96

// Record mirrors the stable odek.log/v1 JSONL fields. Unknown fields are ignored.
type Record = runtimelog.Record
type ErrorInfo = runtimelog.ErrorInfo

type Filter struct {
	Level                                                string
	Since, Until                                         time.Time
	Surface, Status, SessionID, TurnID, RunID, ErrorCode string
	Tree                                                 bool
	TrackIDs                                             bool
}

type Result struct {
	Records      []Record
	Malformed    int
	Truncated    bool
	Positions    []FilePosition
	SeenIDRanges []IDRange
	TreeRunIDs   []string
}

type IDRange struct {
	ProcessID   string
	First, Last uint64
}

// IDIndex keeps an exact, bounded interval set for writer process/sequence IDs.
// Inserts may arrive out of order because warning records use a priority queue.
type IDIndex struct {
	ranges     map[string][]IDRange
	rangeCount int
}

func NewIDIndex() *IDIndex { return &IDIndex{ranges: make(map[string][]IDRange)} }
func ParseRecordID(id string) (string, uint64, bool) {
	if len(id) == 0 || len(id) > MaxRecordIDBytes {
		return "", 0, false
	}
	i := strings.LastIndexByte(id, '-')
	if i <= 0 || i == len(id)-1 || i > MaxProcessIDBytes {
		return "", 0, false
	}
	process := id[:i]
	for _, c := range process {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !valid {
			return "", 0, false
		}
	}
	n, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil || n == 0 {
		return "", 0, false
	}
	return process, n, true
}
func (x *IDIndex) Add(id string) (bool, error) {
	process, seq, ok := ParseRecordID(id)
	if !ok {
		return false, fmt.Errorf("invalid runtime log record_id (must be a generated ID no longer than %d bytes)", MaxRecordIDBytes)
	}
	rs := x.ranges[process]
	i := sort.Search(len(rs), func(i int) bool { return rs[i].Last >= seq })
	if i < len(rs) && rs[i].First <= seq {
		return false, nil
	}
	left := i > 0 && rs[i-1].Last != ^uint64(0) && rs[i-1].Last+1 == seq
	right := i < len(rs) && seq != ^uint64(0) && seq+1 == rs[i].First
	switch {
	case left && right:
		rs[i-1].Last = rs[i].Last
		copy(rs[i:], rs[i+1:])
		rs = rs[:len(rs)-1]
		x.rangeCount--
	case left:
		rs[i-1].Last = seq
	case right:
		rs[i].First = seq
	default:
		rs = append(rs, IDRange{})
		copy(rs[i+1:], rs[i:])
		rs[i] = IDRange{ProcessID: process, First: seq, Last: seq}
		x.rangeCount++
	}
	x.ranges[process] = rs
	if len(x.ranges) > MaxIDProcesses || x.rangeCount > MaxIDRanges {
		return true, fmt.Errorf("too many distinct runtime log record ID ranges to follow safely (limit %d)", MaxIDRanges)
	}
	return true, nil
}
func (x *IDIndex) AddRange(r IDRange) error {
	if r.ProcessID == "" || len(r.ProcessID) > MaxProcessIDBytes || r.First == 0 || r.Last < r.First {
		return fmt.Errorf("invalid runtime log record ID range")
	}
	if len(x.ranges[r.ProcessID]) == 0 && len(x.ranges) >= MaxIDProcesses {
		return fmt.Errorf("too many runtime log processes to follow safely (limit %d)", MaxIDProcesses)
	}
	rs := x.ranges[r.ProcessID]
	i := sort.Search(len(rs), func(i int) bool {
		return rs[i].Last >= r.First || (rs[i].Last != ^uint64(0) && rs[i].Last+1 >= r.First)
	})
	first, last := r.First, r.Last
	removed := 0
	for i < len(rs) && (rs[i].First <= last || (last != ^uint64(0) && rs[i].First == last+1)) {
		if rs[i].Last < first && (rs[i].Last == ^uint64(0) || rs[i].Last+1 < first) {
			i++
			continue
		}
		if rs[i].First < first {
			first = rs[i].First
		}
		if rs[i].Last > last {
			last = rs[i].Last
		}
		removed++
		copy(rs[i:], rs[i+1:])
		rs = rs[:len(rs)-1]
	}
	rs = append(rs, IDRange{})
	copy(rs[i+1:], rs[i:])
	rs[i] = IDRange{ProcessID: r.ProcessID, First: first, Last: last}
	x.rangeCount += 1 - removed
	x.ranges[r.ProcessID] = rs
	if x.rangeCount > MaxIDRanges {
		return fmt.Errorf("too many distinct runtime log record ID ranges to follow safely (limit %d)", MaxIDRanges)
	}
	return nil
}
func (x *IDIndex) Ranges() []IDRange {
	out := make([]IDRange, 0, x.rangeCount)
	for _, rs := range x.ranges {
		out = append(out, rs...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProcessID == out[j].ProcessID {
			return out[i].First < out[j].First
		}
		return out[i].ProcessID < out[j].ProcessID
	})
	return out
}

type FilePosition struct {
	Path          string
	Device, Inode uint64
	Offset, Size  int64
}

// Read scans rotated then active files. Missing files are normal; all other
// unsafe or unreadable files fail closed. It never creates or locks files.
func Read(paths []string, f Filter, limit int) (Result, error) {
	buffer := recordBuffer{}
	malformed := 0
	truncated := false
	positions := make([]FilePosition, 0, len(paths))
	seenIDs := NewIDIndex()
	legacyIDs := 0
	for _, path := range paths {
		keep := Filter{}
		if !f.Tree {
			keep = f
		}
		bad, cut, position, legacy, err := readFile(path, keep, &buffer, seenIDs, f.TrackIDs)
		if err != nil {
			return Result{}, err
		}
		malformed += bad
		truncated = truncated || buffer.truncated
		legacyIDs += legacy
		if position.Path != "" {
			positions = append(positions, position)
		}
		truncated = truncated || cut
	}
	if f.TrackIDs && legacyIDs > 0 {
		return Result{}, fmt.Errorf("%d retained runtime log record(s) lack record_id; --follow cannot safely avoid replay after rotation or pruning", legacyIDs)
	}
	all := buffer.records[buffer.start:]
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp.Before(all[j].Timestamp) })
	treeRuns := []string{}
	if f.Tree {
		all = treeRecords(all, f.RunID)
		for id := range descendantRunIDs(all, f.RunID) {
			treeRuns = append(treeRuns, id)
		}
		sort.Strings(treeRuns)
	}
	matched := all[:0]
	for _, r := range all {
		if matches(r, f) {
			matched = append(matched, r)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return Result{Records: append([]Record(nil), matched...), Malformed: malformed, Truncated: truncated, Positions: positions, SeenIDRanges: seenIDs.Ranges(), TreeRunIDs: treeRuns}, nil
}

func descendantRunIDs(all []Record, root string) map[string]bool {
	ids := map[string]bool{root: true}
	for _, r := range all {
		if r.RunID != "" && r.RootRunID == root {
			ids[r.RunID] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, r := range all {
			if r.RunID != "" && ids[r.ParentRunID] && !ids[r.RunID] {
				ids[r.RunID] = true
				changed = true
			}
		}
	}
	return ids
}

func readFile(path string, filter Filter, buffer *recordBuffer, ids *IDIndex, trackIDs bool) (int, bool, FilePosition, int, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, FilePosition{}, 0, nil
	}
	if err != nil {
		return 0, false, FilePosition{}, 0, fmt.Errorf("open log: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, false, FilePosition{}, 0, err
	}
	if !st.Mode().IsRegular() {
		return 0, false, FilePosition{}, 0, fmt.Errorf("log must be a regular file: %s", path)
	}
	position := FilePosition{Path: path}
	position.Size = st.Size()
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		position.Device = uint64(sys.Dev)
		position.Inode = uint64(sys.Ino)
	}
	s := bufio.NewReaderSize(f, 64<<10)
	bad := 0
	truncated := false
	completeOffset := int64(0)
	legacyIDs := 0
	for {
		line, tooLong, n, terminated, err := readBoundedLine(s)
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil && err != io.EOF {
			return bad, truncated, FilePosition{}, 0, err
		}
		if terminated {
			completeOffset += n
		}
		if tooLong {
			bad++
			if err == io.EOF {
				break
			}
			continue
		}
		var r Record
		if len(line) == 0 || json.Unmarshal(line, &r) != nil || r.Timestamp.IsZero() {
			bad++
		} else {
			r.Timestamp = r.Timestamp.UTC()
			if trackIDs {
				if r.RecordID == "" {
					legacyIDs++
				} else if _, err := ids.Add(r.RecordID); err != nil {
					return bad, truncated, FilePosition{}, legacyIDs, err
				}
			}
			if r.Level != "" {
				r.Level = strings.ToLower(r.Level)
			}
			if filter == (Filter{}) || matches(r, filter) {
				buffer.add(r, int64(len(line)))
			}
		}
		if err == io.EOF {
			break
		}
	}
	position.Offset = completeOffset
	return bad, truncated, position, legacyIDs, nil
}

type recordBuffer struct {
	records   []Record
	weights   []int64
	start     int
	bytes     int64
	truncated bool
}

func (b *recordBuffer) add(r Record, weight int64) {
	if weight > MaxStoredBytes {
		b.truncated = true
		return
	}
	b.records = append(b.records, r)
	b.weights = append(b.weights, weight)
	b.bytes += weight
	for len(b.records)-b.start > MaxRecords || b.bytes > MaxStoredBytes {
		b.bytes -= b.weights[b.start]
		b.records[b.start] = Record{}
		b.weights[b.start] = 0
		b.start++
		b.truncated = true
	}
	if b.start > 4096 && b.start*2 >= len(b.records) {
		copy(b.records, b.records[b.start:])
		b.records = b.records[:len(b.records)-b.start]
		copy(b.weights, b.weights[b.start:])
		b.weights = b.weights[:len(b.weights)-b.start]
		b.start = 0
	}
}

func readBoundedLine(r *bufio.Reader) ([]byte, bool, int64, bool, error) {
	var line []byte
	tooLong := false
	read := int64(0)
	for {
		part, err := r.ReadSlice('\n')
		read += int64(len(part))
		if !tooLong {
			if len(line)+len(part) > MaxLineBytes {
				tooLong = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			return nil, true, read, len(part) > 0 && part[len(part)-1] == '\n', err
		}
		terminated := len(line) > 0 && line[len(line)-1] == '\n'
		line = bytesTrimLine(line)
		return line, false, read, terminated, err
	}
}

func bytesTrimLine(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

func matches(r Record, f Filter) bool {
	if !f.Since.IsZero() && r.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && r.Timestamp.After(f.Until) {
		return false
	}
	if f.Level != "" && !levelAtLeast(r.Level, f.Level) {
		return false
	}
	if f.Surface != "" && !strings.EqualFold(r.Surface, f.Surface) {
		return false
	}
	if f.Status != "" && !strings.EqualFold(r.Status, f.Status) {
		return false
	}
	if f.SessionID != "" && r.SessionID != f.SessionID {
		return false
	}
	if f.TurnID != "" && r.TurnID != f.TurnID {
		return false
	}
	if f.RunID != "" && !f.Tree && r.RunID != f.RunID {
		return false
	}
	if f.ErrorCode != "" && (r.Error == nil || !strings.EqualFold(r.Error.Code, f.ErrorCode)) {
		return false
	}
	return true
}

// Matches reports whether r satisfies f.
func Matches(r Record, f Filter) bool { return matches(r, f) }

func levelAtLeast(got, want string) bool {
	order := map[string]int{"debug": 0, "info": 1, "warn": 2, "warning": 2, "error": 3}
	g, gok := order[strings.ToLower(got)]
	w, wok := order[strings.ToLower(want)]
	return wok && gok && g >= w
}

// treeRecords selects the requested run and recursively discovers descendants
// from parent_run_id links, including children whose parent record was pruned.
func treeRecords(all []Record, root string) []Record {
	ids := map[string]bool{root: true}
	byRun := make(map[string]Record)
	for _, r := range all {
		if r.RunID != "" {
			byRun[r.RunID] = r
		}
	}
	ancestors := map[string]bool{}
	if selected, ok := byRun[root]; ok {
		parent := selected.ParentRunID
		for parent != "" && !ancestors[parent] {
			ancestors[parent] = true
			p, ok := byRun[parent]
			if !ok {
				break
			}
			parent = p.ParentRunID
		}
	}
	// root_run_id recovers descendants even when retention removed an
	// intermediate parent record.
	for _, r := range all {
		if r.RunID != "" && r.RootRunID == root {
			ids[r.RunID] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, r := range all {
			if r.RunID != "" && ids[r.ParentRunID] && !ids[r.RunID] {
				ids[r.RunID] = true
				changed = true
			}
		}
	}
	out := make([]Record, 0)
	for _, r := range all {
		if ids[r.RunID] || ancestors[r.RunID] || (r.RunID == "" && ids[r.ParentRunID]) {
			out = append(out, r)
		}
	}
	return out
}

// CopyAvailable reads only bytes after offset and returns complete newline
// terminated records plus the new offset. A partial trailing line is retried.
func CopyAvailable(r io.Reader, offset int64) ([]Record, int64, int, error) {
	seek, ok := r.(io.Seeker)
	if !ok {
		return nil, offset, 0, errors.New("reader is not seekable")
	}
	if _, err := seek.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, 0, err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var out []Record
	bad := 0
	consumed := offset
	for {
		var line []byte
		tooLong := false
		lineBytes := int64(0)
		var endErr error
		for {
			part, err := br.ReadSlice('\n')
			lineBytes += int64(len(part))
			endErr = err
			if !tooLong {
				if len(line)+len(part) > MaxLineBytes {
					tooLong = true
					line = nil
				} else {
					line = append(line, part...)
				}
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			break
		}
		if endErr != nil && !errors.Is(endErr, io.EOF) {
			return out, consumed, bad, endErr
		}
		if errors.Is(endErr, io.EOF) {
			break
		}
		consumed += lineBytes
		if tooLong {
			bad++
			continue
		}
		line = bytesTrimLine(line)
		var rec Record
		if json.Unmarshal(line, &rec) != nil || rec.Timestamp.IsZero() {
			bad++
			continue
		}
		rec.Timestamp = rec.Timestamp.UTC()
		out = append(out, rec)
	}
	return out, consumed, bad, nil
}
