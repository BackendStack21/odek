package config

import "testing"

// TestRED_MediaAutoProcessingOffByDefault: Telegram voice auto-transcription
// needs whisper.cpp plus a model and photo auto-description needs a local
// MiniCPM-V stack (or a vision-capable provider model), so neither may be
// on unless the operator opts in. An absent section and a section without
// the field resolve the same way.
func TestRED_MediaAutoProcessingOffByDefault(t *testing.T) {
	if resolveTranscription(nil).AutoTranscribe {
		t.Error("auto_transcribe defaults to true with no transcription section")
	}
	if resolveTranscription(&TranscriptionConfig{Model: "tiny"}).AutoTranscribe {
		t.Error("auto_transcribe defaults to true with a section that omits it")
	}
	if resolveVision(nil).AutoDescribeEnabled() {
		t.Error("auto_describe defaults to true with no vision section")
	}
	if resolveVision(&VisionConfig{Backend: VisionBackendLocal}).AutoDescribeEnabled() {
		t.Error("auto_describe defaults to true with a section that omits it")
	}
	if !resolveTranscription(&TranscriptionConfig{AutoTranscribe: true}).AutoTranscribe {
		t.Error("explicit auto_transcribe=true was dropped")
	}
	on := true
	if !resolveVision(&VisionConfig{AutoDescribe: &on}).AutoDescribeEnabled() {
		t.Error("explicit auto_describe=true was dropped")
	}
}
