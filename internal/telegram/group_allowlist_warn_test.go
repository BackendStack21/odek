package telegram

import (
	"reflect"
	"strings"
	"testing"
)

// An allowed_chats entry for a group (negative id) with no allowed_users
// makes every member of that group a principal; the operator must be told.
func TestRED_GroupChatWithoutUserAllowlistWarns(t *testing.T) {
	cfg := TelegramConfig{AllowedChats: []int64{12345, -1001234567890, -42}}
	if got, want := cfg.GroupChatsWithoutUserAllowlist(), []int64{-1001234567890, -42}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupChatsWithoutUserAllowlist = %v, want %v", got, want)
	}
	msg := GroupAllowlistWarning(cfg)
	for _, want := range []string{"-1001234567890", "-42", "allowed_users", "every member"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q lacks %q", msg, want)
		}
	}
}

func TestGroupAllowlistWarningSilentWhenScoped(t *testing.T) {
	for name, cfg := range map[string]TelegramConfig{
		"users set":    {AllowedChats: []int64{-100}, AllowedUsers: []int64{7}},
		"private only": {AllowedChats: []int64{100, 200}},
		"no chats":     {AllowedUsers: []int64{7}},
		"empty":        {},
	} {
		if ids := cfg.GroupChatsWithoutUserAllowlist(); len(ids) != 0 {
			t.Errorf("%s: unexpected group ids %v", name, ids)
		}
		if msg := GroupAllowlistWarning(cfg); msg != "" {
			t.Errorf("%s: unexpected warning %q", name, msg)
		}
	}
}
