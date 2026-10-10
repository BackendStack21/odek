package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLinkPreviewConfigs(t *testing.T, global, project string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", "")
	globalDir := filepath.Join(dir, ".odek")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if global != "" {
		if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(global), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if project != "" {
		if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(project), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinkPreviewDefaultsDisabled(t *testing.T) {
	writeLinkPreviewConfigs(t, `{"telegram":{"bot_token":"g"}}`, "")
	if LoadConfig(CLIFlags{}).Telegram.LinkPreview {
		t.Fatal("link previews enabled by default")
	}
}

func TestLinkPreviewGlobalConfigEnables(t *testing.T) {
	writeLinkPreviewConfigs(t, `{"telegram":{"bot_token":"g","link_preview":true}}`, "")
	if !LoadConfig(CLIFlags{}).Telegram.LinkPreview {
		t.Fatal("global telegram.link_preview=true not honored")
	}
}

// A cloned repository's odek.json must not re-open the zero-click preview
// channel: the whole telegram section is operator-only.
func TestLinkPreviewProjectConfigCannotEnable(t *testing.T) {
	writeLinkPreviewConfigs(t, `{"telegram":{"bot_token":"g"}}`, `{"telegram":{"link_preview":true}}`)
	if LoadConfig(CLIFlags{}).Telegram.LinkPreview {
		t.Fatal("project odek.json enabled telegram link previews")
	}
}

func TestLinkPreviewEnvOverridesFile(t *testing.T) {
	writeLinkPreviewConfigs(t, `{"telegram":{"bot_token":"g","link_preview":true}}`, "")
	t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", "false")
	if LoadConfig(CLIFlags{}).Telegram.LinkPreview {
		t.Fatal("ODEK_TELEGRAM_LINK_PREVIEW=false did not override the file")
	}
	writeLinkPreviewConfigs(t, `{"telegram":{"bot_token":"g"}}`, "")
	t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", "true")
	if !LoadConfig(CLIFlags{}).Telegram.LinkPreview {
		t.Fatal("ODEK_TELEGRAM_LINK_PREVIEW=true not honored")
	}
}
