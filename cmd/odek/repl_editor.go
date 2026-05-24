package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/term"
)

// ── Line Editor ─────────────────────────────────────────────────────────

// replEditor provides a line editor with history navigation, cursor
// movement, and tab completion for the odek REPL.
type replEditor struct {
	prompt      string
	completions []string
	history     *replHistory

	// Terminal state
	oldState *term.State
	fd       int

	// Line buffer
	line  []rune
	pos   int // cursor position in runes

	// Paste detection
	bracketed bool
}

// newReplEditor creates a line editor reading from stdin.
func newReplEditor(prompt string, completions []string) *replEditor {
	return &replEditor{
		prompt:      prompt,
		completions: completions,
		history:     newReplHistory(),
		fd:          int(os.Stdin.Fd()),
	}
}

// ReadLine displays the prompt and reads a line of input with full editing.
// Returns the line (without trailing newline) or an error (including io.EOF
// on ctrl+d).
func (e *replEditor) ReadLine() (string, error) {
	var err error
	e.oldState, err = term.MakeRaw(e.fd)
	if err != nil {
		// Fall back to simple scan if terminal can't go raw
		return e.fallbackRead()
	}
	defer term.Restore(e.fd, e.oldState)

	e.line = nil
	e.pos = 0
	e.bracketed = false

	// Enable bracketed paste mode
	fmt.Fprint(os.Stderr, "\x1b[?2004h")

	// Draw prompt + empty line
	e.drawLine()

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		data := buf[:n]

		for _, b := range data {
			done, err := e.handleByte(b)
			if err != nil {
				return "", err
			}
			if done {
				result := strings.TrimSpace(string(e.line))
				if result != "" {
					e.history.Add(result)
				}
				// Disable bracketed paste
				fmt.Fprint(os.Stderr, "\x1b[?2004l")
				fmt.Fprintln(os.Stderr)
				return result, nil
			}
		}
	}
}

// handleByte processes a single byte from stdin.
// Returns (done, error) — done=true means Enter was pressed.
func (e *replEditor) handleByte(b byte) (bool, error) {
	switch {
	case b == 0x1b: // ESC — start of escape sequence
		return e.handleEscape()
	case b == 0x0d: // CR (Enter)
		return e.handleEnter()
	case b == 0x7f || b == 0x08: // DEL / BS
		e.backspace()
	case b == 0x09: // TAB
		e.tabComplete()
	case b == 0x04: // Ctrl+D
		return false, fmt.Errorf("EOF")
	case b == 0x03: // Ctrl+C
		fmt.Fprintln(os.Stderr, "^C")
		return false, fmt.Errorf("interrupt")
	case b == 0x0c: // Ctrl+L — clear screen
		e.clearScreen()
	case b == 0x15: // Ctrl+U — delete line
		e.deleteLine()
	case b == 0x0b: // Ctrl+K — delete to end
		e.deleteToEnd()
	case b == 0x01: // Ctrl+A — home
		e.home()
	case b == 0x05: // Ctrl+E — end
		e.end()
	case b == 0x02: // Ctrl+B — left
		e.cursorLeft()
	case b == 0x06: // Ctrl+F — right
		e.cursorRight()
	case b >= 0x20: // Printable
		e.insert(b)
	}
	return false, nil
}

// handleEscape processes escape sequences (arrow keys, home, end, etc.).
func (e *replEditor) handleEscape() (bool, error) {
	buf := make([]byte, 2)
	n, err := os.Stdin.Read(buf)
	if err != nil || n < 1 {
		return false, err
	}

	switch {
	case buf[0] == '[':
		// CSI sequence
		n, err = os.Stdin.Read(buf[:1])
		if err != nil || n < 1 {
			return false, err
		}
		switch buf[0] {
		case 'A': // Up
			e.historyPrev()
		case 'B': // Down
			e.historyNext()
		case 'C': // Right
			e.cursorRight()
		case 'D': // Left
			e.cursorLeft()
		case 'H': // Home
			e.home()
		case 'F': // End
			e.end()
		case '3': // Delete — \x1b[3~
			e.readTildeOrBracketed("Delete")
			e.delete()
		case '2': // Insert \x1b[2~ or bracketed paste \x1b[200~/201~
			e.readTildeOrBracketed("Insert")
		case '1': // Bracketed paste end \x1b[201~
			e.readTildeOrBracketed("")
		}
	case buf[0] == 'O': // Old-style home/end
		n, err = os.Stdin.Read(buf[:1])
		if err != nil || n < 1 {
			return false, err
		}
		switch buf[0] {
		case 'H': // Home
			e.home()
		case 'F': // End
			e.end()
		}
	}
	return false, nil
}

// ── Cursor / Editing Operations ────────────────────────────────────────

// readTildeOrBracketed reads remaining bytes after a CSI sequence like
// \x1b[2 and determines if it's a tilde sequence (\x1b[2~) or bracketed
// paste (\x1b[200~ / \x1b[201~).
func (e *replEditor) readTildeOrBracketed(_ string) {
	// Read next byte — could be '~' or '0'
	more := make([]byte, 1)
	n, _ := os.Stdin.Read(more)
	if n < 1 {
		return
	}
	if more[0] == '~' {
		// Simple tilde sequence like [2~, [3~
		return
	}
	if more[0] == '0' {
		// \x1b[200~ or \x1b[201~
		end := make([]byte, 2)
		os.Stdin.Read(end)
		if end[0] == '0' && end[1] == '~' {
			e.bracketed = true // start paste
		} else if end[0] == '1' && end[1] == '~' {
			e.bracketed = false // end paste
		}
	}
}

func (e *replEditor) cursorLeft() {
	if e.pos > 0 {
		e.pos--
		fmt.Fprint(os.Stderr, "\b")
	}
}

func (e *replEditor) cursorRight() {
	if e.pos < len(e.line) {
		e.pos++
		fmt.Fprint(os.Stderr, "\x1b[C")
	}
}

func (e *replEditor) home() {
	if e.pos > 0 {
		fmt.Fprintf(os.Stderr, "\r\x1b[%dC", len(e.prompt))
		e.pos = 0
	}
}

func (e *replEditor) end() {
	if e.pos < len(e.line) {
		fmt.Fprintf(os.Stderr, "\r\x1b[%dC", len(e.prompt)+len(e.line))
		e.pos = len(e.line)
	}
}

func (e *replEditor) insert(b byte) {
	// Extend line
	e.line = append(e.line, 0)
	copy(e.line[e.pos+1:], e.line[e.pos:])
	e.line[e.pos] = rune(b)
	e.pos++

	// Redraw from cursor
	e.redrawFromCursor()
}

func (e *replEditor) backspace() {
	if e.pos > 0 {
		e.line = append(e.line[:e.pos-1], e.line[e.pos:]...)
		e.pos--
		e.redrawFromCursor()
	}
}

func (e *replEditor) delete() {
	if e.pos < len(e.line) {
		e.line = append(e.line[:e.pos], e.line[e.pos+1:]...)
		e.redrawFromCursor()
	}
}

func (e *replEditor) deleteLine() {
	e.line = nil
	e.pos = 0
	e.redrawFromCursor()
}

func (e *replEditor) deleteToEnd() {
	if e.pos < len(e.line) {
		e.line = e.line[:e.pos]
		e.redrawFromCursor()
	}
}

// ── History ─────────────────────────────────────────────────────────────

func (e *replEditor) historyPrev() {
	entry := e.history.Prev()
	if entry != nil {
		e.loadHistoryLine(*entry)
	}
}

func (e *replEditor) historyNext() {
	entry := e.history.Next()
	if entry != nil {
		e.loadHistoryLine(*entry)
	}
}

func (e *replEditor) loadHistoryLine(line string) {
	e.line = []rune(line)
	e.pos = len(e.line)
	e.redrawFromCursor()
}

// ── Tab Completion ──────────────────────────────────────────────────────

func (e *replEditor) tabComplete() {
	if len(e.line) == 0 || len(e.completions) == 0 {
		return
	}

	prefix := string(e.line)
	var matches []string
	for _, c := range e.completions {
		if strings.HasPrefix(c, prefix) {
			matches = append(matches, c)
		}
	}

	if len(matches) == 0 {
		return
	}

	if len(matches) == 1 {
		// Complete to the match
		e.line = []rune(matches[0])
		e.pos = len(e.line)
		e.redrawFromCursor()
		return
	}

	// Show all matches
	fmt.Fprintln(os.Stderr)
	for _, m := range matches {
		fmt.Fprintf(os.Stderr, "  %s\n", m)
	}
	e.drawLine()
}

// ── Enter ───────────────────────────────────────────────────────────────

func (e *replEditor) handleEnter() (bool, error) {
	if e.bracketed {
		// In paste mode — insert newline
		e.line = append(e.line, '\n')
		e.pos = len(e.line)
		// Print the newline so the terminal shows it
		fmt.Fprintln(os.Stderr)
		e.drawLine()
		return false, nil
	}
	return true, nil
}

// ── Screen Drawing ──────────────────────────────────────────────────────

func (e *replEditor) drawLine() {
	fmt.Fprint(os.Stderr, "\r", e.prompt, string(e.line))
	// Clear to end of line
	fmt.Fprint(os.Stderr, "\x1b[K")
	// Position cursor
	offset := len(e.prompt) + e.pos
	fmt.Fprintf(os.Stderr, "\r\x1b[%dC", offset)
}

// redrawFromCursor redraws the full line from scratch.
// The old partial-redraw logic (printing from e.pos onward) failed when
// e.pos == len(e.line) — the common "typing at end" case — leaving
// every keystroke invisible. A full drawLine() is correct and fast.
func (e *replEditor) redrawFromCursor() {
	e.drawLine()
}

func (e *replEditor) clearScreen() {
	fmt.Fprint(os.Stderr, "\x1b[2J\x1b[H")
	e.drawLine()
}

// ── Fallback ────────────────────────────────────────────────────────────

func (e *replEditor) fallbackRead() (string, error) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Fprint(os.Stderr, e.prompt)
	if !scanner.Scan() {
		return "", fmt.Errorf("EOF")
	}
	line := strings.TrimSpace(scanner.Text())
	if line != "" {
		e.history.Add(line)
	}
	return line, nil
}

// ── History ─────────────────────────────────────────────────────────────

const (
	maxHistoryLines = 500
	historyFilename = "repl_history"
)

// replHistory manages a ring of command history entries with file persistence.
type replHistory struct {
	mu      sync.Mutex
	entries []string
	pos     int // current position (len(entries) = newest + 1)
	max     int
	loaded  bool
}

func newReplHistory() *replHistory {
	return &replHistory{
		entries: make([]string, 0, 128),
		pos:     0,
		max:     maxHistoryLines,
	}
}

func (h *replHistory) Add(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Dedup consecutive
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == line {
		return
	}

	h.entries = append(h.entries, line)
	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
	h.pos = len(h.entries) // reset position to end
	h.persist()
}

func (h *replHistory) Prev() *string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.entries) == 0 {
		return nil
	}
	if h.pos > 0 {
		h.pos--
	}
	return &h.entries[h.pos]
}

func (h *replHistory) Next() *string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pos < len(h.entries) {
		h.pos++
	}
	if h.pos >= len(h.entries) {
		return nil
	}
	return &h.entries[h.pos]
}

func (h *replHistory) Load(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Dedup consecutive as we load
		if len(h.entries) > 0 && h.entries[len(h.entries)-1] == line {
			continue
		}
		h.entries = append(h.entries, line)
	}
	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
	h.pos = len(h.entries)
	h.loaded = true
}

func (h *replHistory) persist() {
	if !h.loaded {
		return // don't write until at least one Load
	}
	path := filepath.Join(odekDir(), historyFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	for _, entry := range h.entries {
		fmt.Fprintln(f, entry)
	}
}

// odekDir returns the ~/.odek directory path.
func odekDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".odek"
	}
	return filepath.Join(home, ".odek")
}
