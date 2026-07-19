package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Configurable via env vars
var (
	toolsJSON    = os.Getenv("FAKE_TOOLS")
	errorOnCall  = os.Getenv("FAKE_ERROR_ON_CALL")
	delayStr     = os.Getenv("FAKE_DELAY")
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		if delayStr != "" {
			if d, err := time.ParseDuration(delayStr); err == nil {
				time.Sleep(d)
			}
		}

		switch req.Method {
		case "initialize":
			initResult, _ := json.Marshal(map[string]any{
				"protocolVersion": "2025-03-26",
				"serverInfo":      map[string]string{"name": "fakeserver", "version": "1.0.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
			writeResp(req.ID, initResult, nil)

		case "tools/list":
			var defs []map[string]any
			if toolsJSON != "" {
				json.Unmarshal([]byte(toolsJSON), &defs)
			}
			listResult, _ := json.Marshal(map[string]any{"tools": defs})
			writeResp(req.ID, listResult, nil)

		case "tools/call":
			if errorOnCall != "" {
				writeResp(req.ID, nil, &rpcError{Code: -32000, Message: errorOnCall})
				continue
			}
			result, _ := json.Marshal(map[string]any{
				"content": []map[string]string{
					{"type": "text", "text": "ok"},
				},
			})
			writeResp(req.ID, result, nil)

		case "ping":
			writeResp(req.ID, json.RawMessage("{}"), nil)

		default:
			writeResp(req.ID, nil, &rpcError{Code: -32601, Message: "Method not found: " + req.Method})
		}
	}
}

func writeResp(id json.RawMessage, result json.RawMessage, err *rpcError) {
	resp := response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   err,
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}
