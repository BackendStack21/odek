// Command piguard-gateway is a small HTTP front end for the PIGuard daemon.
// It forwards requests to the daemon's Unix socket and returns the JSON
// reply, so odek's HTTP-based guard client can reach the socket-only daemon
// over the compose network.
//
// The daemon speaks newline-delimited JSON ({"text":...}, {"long":...},
// {"texts":[...]}), which is exactly what odek's piguard client sends, so
// JSON bodies are forwarded verbatim. A non-JSON body is treated as raw text
// and wrapped as {"text": <body>} for convenience (curl -d 'some text').
//
// Endpoints:
//
//	GET  /healthz   liveness — checks the daemon socket is reachable
//	POST /detect    {"text": "..."}  (or raw text)
//	POST /long      {"long": "..."}  (or raw text)
//	POST /raw       {"texts": [...]} (batch; or any daemon JSON line)
//
// Note: the daemon dispatches on the JSON keys, not the HTTP path; the paths
// exist only to mirror odek's client configuration.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// maxBody caps a request body, mirroring the daemon's 1 MiB request limit
// (with a little headroom for JSON framing).
const maxBody = 1 << 20

type gateway struct {
	socket string
}

// forward sends one newline-delimited request to the daemon and returns its
// single-line reply.
func (g *gateway) forward(line []byte) ([]byte, error) {
	conn, err := net.DialTimeout("unix", g.socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if _, err := conn.Write(append(bytes.TrimRight(line, "\n"), '\n')); err != nil {
		return nil, err
	}
	resp, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(resp) == 0 {
		return nil, err
	}
	return resp, nil
}

// daemonLine converts an HTTP request body into one daemon protocol line.
// Bodies that already are daemon protocol JSON (carrying a text, long, or
// texts key) pass through verbatim; anything else is wrapped as raw text.
func daemonLine(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		for _, key := range []string{"text", "long", "texts"} {
			if _, ok := probe[key]; ok {
				return trimmed, nil
			}
		}
	}
	return json.Marshal(map[string]string{"text": string(body)})
}

func (g *gateway) handleDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	line, err := daemonLine(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := g.forward(line)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

func (g *gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	conn, err := net.DialTimeout("unix", g.socket, 2*time.Second)
	if err != nil {
		http.Error(w, "daemon unreachable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	conn.Close()
	_, _ = w.Write([]byte("ok\n"))
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	socket := flag.String("socket", "/run/piguard/piguard.sock", "PIGuard daemon Unix socket")
	flag.Parse()

	g := &gateway{socket: *socket}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.handleHealth)
	mux.HandleFunc("/detect", g.handleDetect)
	mux.HandleFunc("/long", g.handleDetect)
	mux.HandleFunc("/raw", g.handleDetect)

	log.Printf("piguard-gateway: listening on %s, daemon socket %s", *addr, *socket)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
