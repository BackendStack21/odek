package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// replConfigOpenPTYSlave opens a pseudo-terminal pair and returns the slave
// end (a real tty that term.MakeRaw accepts). Raw ioctl numbers keep this in
// one file for linux and darwin.
func replConfigOpenPTYSlave(t *testing.T) *os.File {
	t.Helper()
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })
	ioctl := func(req, arg uintptr) error {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), req, arg); e != 0 {
			return e
		}
		return nil
	}
	var name string
	switch runtime.GOOS {
	case "darwin":
		if err := ioctl(0x20007454, 0); err != nil { // TIOCPTYGRANT
			t.Skipf("grantpt: %v", err)
		}
		if err := ioctl(0x20007452, 0); err != nil { // TIOCPTYUNLK
			t.Skipf("unlockpt: %v", err)
		}
		var buf [128]byte
		if err := ioctl(0x40807453, uintptr(unsafe.Pointer(&buf[0]))); err != nil { // TIOCPTYGNAME
			t.Skipf("ptsname: %v", err)
		}
		name = string(buf[:bytes.IndexByte(buf[:], 0)])
	case "linux":
		var unlock int32
		if err := ioctl(0x40045431, uintptr(unsafe.Pointer(&unlock))); err != nil { // TIOCSPTLCK
			t.Skipf("unlockpt: %v", err)
		}
		var n uint32
		if err := ioctl(0x80045430, uintptr(unsafe.Pointer(&n))); err != nil { // TIOCGPTN
			t.Skipf("ptsname: %v", err)
		}
		name = fmt.Sprintf("/dev/pts/%d", n)
	default:
		t.Skipf("pty helper not implemented on %s", runtime.GOOS)
	}
	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open pty slave %s: %v", name, err)
	}
	t.Cleanup(func() { slave.Close() })
	return slave
}

// replConfigReadLine runs one ReadLine against a pipe pre-loaded with input
// (a pty slave satisfies term.MakeRaw while stdin is the pipe).
func replConfigReadLine(t *testing.T, input string) (string, error) {
	t.Helper()
	tty := replConfigOpenPTYSlave(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	oldStdin, oldStderr := os.Stdin, os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdin, os.Stderr = r, devnull
	t.Cleanup(func() { os.Stdin, os.Stderr = oldStdin, oldStderr; devnull.Close() })

	e := newReplEditor("> ", nil)
	e.fd = int(tty.Fd())
	e.history = newReplHistory()

	// One terminal write, as a fast typist or a terminal-generated key
	// sequence produces.
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		w.Close() // nothing else is coming; unblock a stuck read
	}()

	type res struct {
		line string
		err  error
	}
	done := make(chan res, 1)
	go func() {
		l, err := e.ReadLine()
		done <- res{l, err}
	}()
	select {
	case got := <-done:
		return got.line, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine hung")
	}
	return "", nil
}

// An escape sequence delivered in the same read chunk as the surrounding
// keys must be decoded from that chunk, not by a fresh stdin read that
// blocks for the next keystroke and leaks the tail into the line.
func TestRED_ReplEditorArrowKeyInSameReadChunk(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ab\x1b[Dc\r", "acb"},             // Left
		{"ab\x1b[D\x1b[Cc\r", "abc"},       // Left, Right
		{"abc\x1b[H\x1b[3~\r", "bc"},       // Home, Delete
		{"ab\x1bOHx\x1bOFy\r", "xaby"},     // old-style Home/End
		{"a\x1b[1;5Cb\r", "ab"},            // ctrl+Right parameter tail
		{"a\x1b[200~xy\x1b[201~\r", "axy"}, // bracketed paste
	} {
		got, err := replConfigReadLine(t, tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ReadLine(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}
