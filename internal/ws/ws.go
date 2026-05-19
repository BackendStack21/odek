// Package ws implements RFC 6455 WebSocket framing with zero external
// dependencies. Only the server side (upgrade + read/write frames).
//
// This is a minimal, auditable implementation — ~200 LOC. It handles
// text frames, close frames, and ping/pong. Fragmentation is not
// supported (every frame is FIN=true), which is fine for JSON messages.
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

// Magic GUID from RFC 6455 section 4.2.2.
const magicGUID = "258EAFA5-E914-47DA-95CA-5AB5F1A13B20"

// Opcodes.
const (
	OpText   = 0x1
	OpBinary = 0x2
	OpClose  = 0x8
	OpPing   = 0x9
	OpPong   = 0xA
)

// Conn wraps a net.Conn with WebSocket framing.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
	mu   sync.Mutex // guards bw for concurrent writes
}

// Upgrade performs the WebSocket upgrade handshake and returns a Conn.
// The ResponseWriter is hijacked — do not use it after calling Upgrade.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Header.Get("Upgrade") != "websocket" {
		return nil, fmt.Errorf("ws: not a websocket upgrade request")
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("ws: missing Sec-WebSocket-Key")
	}

	// Compute accept key per RFC 6455 section 4.2.2
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(magicGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("ws: server does not support hijacking")
	}
	netConn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: hijack: %w", err)
	}

	// Write the upgrade response
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := bufrw.WriteString(resp); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("ws: write upgrade: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("ws: flush upgrade: %w", err)
	}

	return &Conn{
		conn: netConn,
		br:   bufrw.Reader,
		bw:   bufrw.Writer,
	}, nil
}

// ReadMessage reads a complete message from the WebSocket connection.
// Returns the opcode and payload. For text messages (opcode 0x1), the
// payload is a UTF-8 string. This is a blocking call.
func (c *Conn) ReadMessage() (opcode int, payload []byte, err error) {
	for {
		// Read frame header (2 bytes minimum)
		header := make([]byte, 2)
		if _, err := io.ReadFull(c.br, header); err != nil {
			return 0, nil, fmt.Errorf("ws: read header: %w", err)
		}

		fin := header[0]&0x80 != 0
		opcode = int(header[0] & 0x0F)
		masked := header[1]&0x80 != 0
		length := int64(header[1] & 0x7F)

		// Extended payload length
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(c.br, ext); err != nil {
				return 0, nil, fmt.Errorf("ws: read ext len 16: %w", err)
			}
			length = int64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(c.br, ext); err != nil {
				return 0, nil, fmt.Errorf("ws: read ext len 64: %w", err)
			}
			length = int64(binary.BigEndian.Uint64(ext))
		}

		// Read masking key (client→server frames are always masked)
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, mask[:]); err != nil {
				return 0, nil, fmt.Errorf("ws: read mask: %w", err)
			}
		}

		// Read payload
		payload = make([]byte, length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, fmt.Errorf("ws: read payload: %w", err)
		}

		// Unmask
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}

		switch opcode {
		case OpText, OpBinary:
			if !fin {
				return 0, nil, fmt.Errorf("ws: fragmented frames not supported")
			}
			return opcode, payload, nil
		case OpClose:
			// Echo close frame back, then close
			c.writeFrame(OpClose, nil)
			return OpClose, payload, nil
		case OpPing:
			// Auto-respond with pong
			c.writeFrame(OpPong, payload)
			continue
		case OpPong:
			// Ignore unsolicited pongs
			continue
		default:
			return 0, nil, fmt.Errorf("ws: unknown opcode %d", opcode)
		}
	}
}

// WriteMessage sends a text or binary message. For text messages, pass
// OpText and a valid UTF-8 byte slice.
func (c *Conn) WriteMessage(opcode int, data []byte) error {
	return c.writeFrame(opcode, data)
}

// Close sends a close frame and then closes the underlying connection.
func (c *Conn) Close() error {
	c.writeFrame(OpClose, nil)
	return c.conn.Close()
}

// writeFrame writes a single WebSocket frame. Server frames are never
// masked. All frames are sent with FIN=1 (no fragmentation).
func (c *Conn) writeFrame(opcode int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// FIN=1, RSV=000, opcode
	c.bw.WriteByte(0x80 | byte(opcode))

	// Payload length
	length := len(payload)
	switch {
	case length <= 125:
		c.bw.WriteByte(byte(length))
	case length <= 65535:
		c.bw.WriteByte(126)
		len16 := make([]byte, 2)
		binary.BigEndian.PutUint16(len16, uint16(length))
		c.bw.Write(len16)
	default:
		c.bw.WriteByte(127)
		len64 := make([]byte, 8)
		binary.BigEndian.PutUint64(len64, uint64(length))
		c.bw.Write(len64)
	}

	// Payload (never masked for server frames)
	if len(payload) > 0 {
		c.bw.Write(payload)
	}

	return c.bw.Flush()
}

// generateKey creates a random Sec-WebSocket-Key for testing.
func generateKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Dial connects to a WebSocket server. Used for testing.
func Dial(url string) (*Conn, *http.Response, error) {
	// Parse URL
	if len(url) < 6 || url[:5] != "ws://" {
		return nil, nil, errors.New("ws: only ws:// URLs supported")
	}
	host := url[5:]

	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, nil, fmt.Errorf("ws: dial: %w", err)
	}

	key, err := generateKey()
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		host, key)

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("ws: write request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("ws: read response: %w", err)
	}

	if resp.StatusCode != 101 {
		conn.Close()
		return nil, resp, fmt.Errorf("ws: expected 101, got %s", resp.Status)
	}

	return &Conn{
		conn: conn,
		br:   br,
		bw:   bufio.NewWriter(conn),
	}, resp, nil
}
