package main

import (
	"encoding/json"
	"github.com/BackendStack21/odek/internal/session"
)

type telegramPhotoInput struct{ Path, Caption string }

// The path comes from the application's media downloader, never by parsing
// user text. Captions remain data within the tool's analysis prompt.
func telegramPhotoCalls(photo *telegramPhotoInput) []session.ToolCall {
	args, _ := json.Marshal(visionArgs{Path: photo.Path, Prompt: photoVisionPrompt(photo.Caption)})
	var call session.ToolCall
	call.ID = "odek_photo_analysis"
	call.Type = "function"
	call.Function.Name = "vision"
	call.Function.Arguments = string(args)
	return []session.ToolCall{call}
}
