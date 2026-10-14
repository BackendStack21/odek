package main

import (
	"context"

	"github.com/BackendStack21/odek/internal/telegram"
)

type telegramMediaChatKey struct{}

// withTelegramMediaChat records, on a Telegram turn's context, the chat whose
// files the turn's file-reading tools may open from the shared media
// directory.
func withTelegramMediaChat(ctx context.Context, chatID int64) context.Context {
	return context.WithValue(ctx, telegramMediaChatKey{}, chatID)
}

// checkTelegramMediaRead refuses a read of another chat's downloaded media.
// Outside a Telegram turn (no chat on the context) it allows everything.
func checkTelegramMediaRead(ctx context.Context, path string) error {
	chatID, ok := ctx.Value(telegramMediaChatKey{}).(int64)
	if !ok || chatID == 0 {
		return nil
	}
	return telegram.CheckChatMediaRead(path, chatID)
}
