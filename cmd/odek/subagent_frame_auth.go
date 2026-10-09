package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// frameFDEnvVar names the inherited descriptor carrying the per-spawn result
// frame nonce. Like keyFDEnvVar it holds only the descriptor number, never
// the secret itself.
const frameFDEnvVar = "ODEK_SUBAGENT_FRAME_FD"

// frameNonceBytes is the nonce size; it is hex-encoded on the wire.
const frameNonceBytes = 16

// newSubagentFrameChannel mints the nonce that authenticates a child's result
// frame and returns the read end of a pipe already holding it (write end
// closed, so the child reads to EOF). The child inherits the read end as an
// extra descriptor, reads the nonce once at startup and closes it before any
// tool runs. The nonce never enters the child's environment, argv or task
// file, so commands the child runs cannot read it from /proc or the
// filesystem and cannot forge a frame the parent accepts.
func newSubagentFrameChannel() (string, *os.File, error) {
	b := make([]byte, frameNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("frame nonce: %w", err)
	}
	nonce := hex.EncodeToString(b)
	r, w, err := os.Pipe()
	if err != nil {
		return "", nil, fmt.Errorf("frame pipe: %w", err)
	}
	_, werr := w.Write([]byte(nonce + "\n"))
	cerr := w.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = r.Close()
		return "", nil, fmt.Errorf("frame pipe: %w", werr)
	}
	return nonce, r, nil
}

// readFrameNonceFromInheritedFD reads the result-frame nonce the parent
// handed over through $ODEK_SUBAGENT_FRAME_FD and closes the descriptor.
// It returns "" when no descriptor was handed over (standalone runs) or the
// content is not a well-formed nonce. The env var is unset so grandchildren
// never see it.
func readFrameNonceFromInheritedFD() string {
	fdStr := os.Getenv(frameFDEnvVar)
	if fdStr == "" {
		return ""
	}
	os.Unsetenv(frameFDEnvVar)
	// Strict parse: "3x" or " 3" is not a descriptor number.
	fd, err := strconv.Atoi(fdStr)
	if err != nil || fd < 3 {
		return ""
	}
	f := os.NewFile(uintptr(fd), "odek-frame-fd")
	if f == nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 2*frameNonceBytes+2))
	if err != nil {
		return ""
	}
	nonce := strings.TrimSpace(string(data))
	if raw, err := hex.DecodeString(nonce); err != nil || len(raw) != frameNonceBytes {
		return ""
	}
	return nonce
}

// frameAuthentic reports whether a frame's auth field matches the nonce
// minted for this spawn.
func frameAuthentic(got, want string) bool {
	return want != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
