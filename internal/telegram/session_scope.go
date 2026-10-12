package telegram

// SessionIDBelongsToChat reports whether a session-store ID belongs to the
// given Telegram chat (`tg-<chatID>` or `tg-<chatID>-<suffix>`).
func SessionIDBelongsToChat(id string, chatID int64) bool {
	return sessionIDBelongsToChat(id, chatID)
}
