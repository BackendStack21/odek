package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestJSONQueryRejectsTrailingDocumentData(t *testing.T) {
	cases := []struct {
		name, content string
		wantErr       bool
	}{
		{"valid whitespace", "{\"ok\":1} \n\t", false},
		{"concatenated values", `{"ok":1}{"later":2}`, true},
		{"trailing malformed", `{"ok":1} garbage`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			got, callErr := (&jsonQueryTool{}).Call(`{"path":` + strconv.Quote(path) + `}`)
			var result jsonQueryResult
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("result is not json: %v (%s)", err, got)
			}
			if tc.wantErr && result.Error == "" && callErr == nil {
				t.Fatalf("accepted invalid JSON: %s", got)
			}
			if !tc.wantErr && (result.Error != "" || callErr != nil) {
				t.Fatalf("rejected valid JSON: result=%+v err=%v", result, callErr)
			}
		})
	}
}

func TestJSONQueryRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { got, _ := (&jsonQueryTool{}).Call(`{"path":` + strconv.Quote(path) + `}`); done <- got }()
	select {
	case got := <-done:
		var result jsonQueryResult
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatal(err)
		}
		if result.Error == "" {
			t.Fatalf("FIFO accepted: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("json_query blocked opening FIFO")
	}
}
