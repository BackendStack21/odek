package telegram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownloadDocument_Success verifies document download with filename.
func TestDownloadDocument_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"doc1","file_path":"docs/report.pdf"}}`)
			return
		}
		w.Write([]byte("fake-pdf-data"))
	}))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadDocument(bot, 42, "doc1", "report.pdf")
	if err != nil {
		t.Fatalf("DownloadDocument: %v", err)
	}
	if !strings.Contains(path, "report.pdf") {
		t.Errorf("expected filename in path, got %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "fake-pdf-data" {
		t.Errorf("content = %q, want %q", string(data), "fake-pdf-data")
	}
	os.Remove(path)
}

// TestDownloadDocument_NoFileName verifies fallback when filename is empty.
func TestDownloadDocument_NoFileName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"doc2","file_path":"docs/data"}}`)
			return
		}
		w.Write([]byte("binary-data"))
	}))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadDocument(bot, 42, "doc2", "")
	if err != nil {
		t.Fatalf("DownloadDocument: %v", err)
	}
	// Should have generated filename with fileID prefix
	if !strings.Contains(filepath.Base(path), "doc_") {
		t.Errorf("expected generated filename, got %q", path)
	}
	os.Remove(path)
}

// TestHandleUpdate_Document verifies document message routing.
func TestHandleUpdate_Document(t *testing.T) {
	var (
		capturedFileID   string
		capturedFileName string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnDocumentMessage = func(chatID int64, messageID int, fileID string, fileName string, _ int64) (string, error) {
		capturedFileID = fileID
		capturedFileName = fileName
		return "document received", nil
	}

	upd := Update{
		ID: 1,
		Message: &Message{
			ID:   55,
			Chat: &Chat{ID: 777},
			From: &User{ID: 888},
			Document: &Document{
				FileID:   "doc_file_123",
				FileName: "report.pdf",
				MimeType: "application/pdf",
				FileSize: 1024,
			},
		},
	}

	h.HandleUpdate(upd)

	if capturedFileID != "doc_file_123" {
		t.Errorf("fileID = %q, want %q", capturedFileID, "doc_file_123")
	}
	if capturedFileName != "report.pdf" {
		t.Errorf("fileName = %q, want %q", capturedFileName, "report.pdf")
	}
}
