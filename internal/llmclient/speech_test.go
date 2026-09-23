package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func speechTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s, err := NewSDK(Options{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  srv.URL,
		Timeout:  5 * 1000 * 1000 * 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(s, "openai", "tts-1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientSpeak_Success(t *testing.T) {
	var gotAuth string
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio-bytes"))
	})
	res, err := c.Speak(context.Background(), "tts-1", SpeakRequest{Text: "hello", Voice: "alloy", Format: "mp3", Speed: 1.1})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if string(res.Audio) != "audio-bytes" || res.Model != "tts-1" || res.MIMEType == "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestClientSpeak_ProviderError(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := c.Speak(context.Background(), "tts-1", SpeakRequest{Text: "hello", Voice: "alloy"}); err == nil {
		t.Fatal("expected error from non-200 response")
	}
}

func TestClientTranscribe_Success(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"recognized words","model":"whisper-1","language":"en","duration":1.5}`))
	})
	res, err := c.Transcribe(context.Background(), "whisper-1", TranscribeRequest{
		Audio: []byte("wav-bytes"), Filename: "audio.wav", Language: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "recognized words" || res.Language != "en" || res.DurationSec != 1.5 || !strings.Contains(res.Model, "whisper") {
		t.Fatalf("result = %+v", res)
	}
}

func TestClientTranscribe_ProviderError(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := c.Transcribe(context.Background(), "whisper-1", TranscribeRequest{Audio: []byte("wav-bytes")}); err == nil {
		t.Fatal("expected error from non-200 response")
	}
}
