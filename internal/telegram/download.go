package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fileIDSuffix derives a short, collision-free filename suffix from a Telegram
// file_id. Telegram file_ids share a long, near-constant prefix that encodes
// the file type, datacenter, and version (e.g. "AgACAgIAAxkBAAI…" for photos);
// the bytes that actually distinguish one file from another come *after* that
// prefix. Truncating the raw file_id therefore collides across different files,
// so we hash the full id and keep the first 16 hex chars — unique per file.
func fileIDSuffix(fileID string) string {
	sum := sha256.Sum256([]byte(fileID))
	return hex.EncodeToString(sum[:])[:16]
}

// chatMediaPattern returns a glob pattern that matches files saved for chatID.
func chatMediaPattern(dir string, chatID int64) string {
	return filepath.Join(dir, fmt.Sprintf("*_chat%d_*", chatID))
}

// chatMediaUsage returns the total size of media files already stored for chatID.
func chatMediaUsage(dir string, chatID int64) (int64, error) {
	matches, err := filepath.Glob(chatMediaPattern(dir, chatID))
	if err != nil {
		return 0, err
	}
	var total int64
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		total += fi.Size()
	}
	return total, nil
}

// checkMediaQuota returns an error if writing additionalSize bytes for chatID
// would exceed quota. A quota of 0 disables the check.
func checkMediaQuota(dir string, chatID, additionalSize, quota int64) error {
	if quota <= 0 {
		return nil
	}
	usage, err := chatMediaUsage(dir, chatID)
	if err != nil {
		return fmt.Errorf("telegram: media quota: %w", err)
	}
	if usage+additionalSize > quota {
		return fmt.Errorf("telegram: media quota exceeded for chat %d (%d + %d > %d bytes)", chatID, usage, additionalSize, quota)
	}
	return nil
}

// ── Media Directory ────────────────────────────────────────────────────────

// MediaDir returns the directory where downloaded media files are stored.
// Defaults to ~/.odek/media/. Creates the directory if it doesn't exist.
func MediaDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("telegram media: home dir: %w", err)
	}
	dir := filepath.Join(home, ".odek", "media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("telegram media: mkdir: %w", err)
	}
	return dir, nil
}

// ── Voice Download ─────────────────────────────────────────────────────────

// DownloadVoice downloads a voice message from Telegram and saves it to the
// media directory. Returns the local file path. The file is saved as
// "voice_chat<chatID>_<fileID>.ogg" using a content-hash-safe truncation of the fileID.
func DownloadVoice(bot *Bot, chatID int64, fileID string) (string, error) {
	dir, err := MediaDir()
	if err != nil {
		return "", err
	}

	// Get file metadata from Telegram.
	f, err := bot.GetFile(fileID)
	if err != nil {
		return "", fmt.Errorf("telegram voice: get file: %w", err)
	}
	if f.FilePath == "" {
		return "", fmt.Errorf("telegram voice: no file path returned")
	}

	// Download raw bytes.
	data, err := bot.DownloadFile(f.FilePath)
	if err != nil {
		return "", fmt.Errorf("telegram voice: download: %w", err)
	}

	// Enforce per-chat media quota before writing.
	if err := checkMediaQuota(dir, chatID, int64(len(data)), bot.MediaQuotaPerChat); err != nil {
		return "", err
	}

	// Determine extension from original path.
	ext := filepath.Ext(f.FilePath)
	if ext == "" {
		ext = ".ogg"
	}

	// Hash the full fileID for a unique, collision-free filename suffix.
	localPath := filepath.Join(dir, fmt.Sprintf("voice_chat%d_%s%s", chatID, fileIDSuffix(fileID), ext))

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram voice: save: %w", err)
	}

	return localPath, nil
}

// ── Photo Download ─────────────────────────────────────────────────────────

// DownloadPhoto downloads the largest available size of a photo and saves it
// to the media directory. Uses the last (largest) PhotoSize in the slice.
// Returns the local file path. Saved as "photo_chat<chatID>_<fileID>.jpg".
func DownloadPhoto(bot *Bot, chatID int64, fileIDs []string) (string, error) {
	if len(fileIDs) == 0 {
		return "", fmt.Errorf("telegram photo: no file IDs")
	}

	// Telegram sends multiple sizes; the last one is the largest.
	fileID := fileIDs[len(fileIDs)-1]

	dir, err := MediaDir()
	if err != nil {
		return "", err
	}

	// Get file metadata.
	f, err := bot.GetFile(fileID)
	if err != nil {
		return "", fmt.Errorf("telegram photo: get file: %w", err)
	}
	if f.FilePath == "" {
		return "", fmt.Errorf("telegram photo: no file path returned")
	}

	// Download raw bytes.
	data, err := bot.DownloadFile(f.FilePath)
	if err != nil {
		return "", fmt.Errorf("telegram photo: download: %w", err)
	}

	// Enforce per-chat media quota before writing.
	if err := checkMediaQuota(dir, chatID, int64(len(data)), bot.MediaQuotaPerChat); err != nil {
		return "", err
	}

	// Determine extension.
	ext := filepath.Ext(f.FilePath)
	if ext == "" {
		ext = ".jpg"
	}

	localPath := filepath.Join(dir, fmt.Sprintf("photo_chat%d_%s%s", chatID, fileIDSuffix(fileID), ext))

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram photo: save: %w", err)
	}

	return localPath, nil
}

// ── Document Download ─────────────────────────────────────────────────────

// DownloadDocument downloads a document/file from Telegram and saves it
// to the media directory. Returns the local file path. The filename is prefixed
// with the chat ID so per-chat quotas can be enforced.
func DownloadDocument(bot *Bot, chatID int64, fileID, fileName string) (string, error) {
	dir, err := MediaDir()
	if err != nil {
		return "", err
	}

	// Get file metadata from Telegram.
	f, err := bot.GetFile(fileID)
	if err != nil {
		return "", fmt.Errorf("telegram document: get file: %w", err)
	}
	if f.FilePath == "" {
		return "", fmt.Errorf("telegram document: no file path returned")
	}

	// Download raw bytes.
	data, err := bot.DownloadFile(f.FilePath)
	if err != nil {
		return "", fmt.Errorf("telegram document: download: %w", err)
	}

	// Enforce per-chat media quota before writing.
	if err := checkMediaQuota(dir, chatID, int64(len(data)), bot.MediaQuotaPerChat); err != nil {
		return "", err
	}

	// Use original filename or generate one from file ID, prefixed with a
	// "doc_chat<chatID>_" tag. The "<type>_chat<chatID>_" prefix mirrors the
	// voice/photo naming so the file is matched by chatMediaPattern and counted
	// toward the per-chat media quota (a bare "chat<chatID>_" prefix would not
	// match the leading-underscore glob and would let documents bypass the cap).
	safeName := sanitizeDocName(fileName, fileID, f.FilePath)
	localPath := filepath.Join(dir, fmt.Sprintf("doc_chat%d_%s", chatID, safeName))

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram document: save: %w", err)
	}

	return localPath, nil
}

// maxDocNameLen limits downloaded document basenames. Leaving headroom for
// the "doc_chat<N>_" prefix keeps the final filename under typical filesystem
// limits (e.g. 255 bytes on ext4) even for long Telegram document names.
const maxDocNameLen = 200

// sanitizeDocName derives a safe, single-component filename for a downloaded
// Telegram document. The supplied fileName comes from attacker-controlled
// Document metadata, so directory components must be stripped to prevent path
// traversal outside the media directory (e.g. "../../.ssh/authorized_keys").
// Hidden names, names containing characters outside a safe ASCII set, and
// overly long names are rejected and replaced with a deterministic name
// generated from the file ID.
func sanitizeDocName(fileName, fileID, filePath string) string {
	base := filepath.Base(filepath.Clean(fileName))
	// filepath.Base never returns a path containing a separator, but it can
	// still yield "." (empty/relative input) or ".." which must be rejected.
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return fallbackDocName(fileID, filePath)
	}
	// Reject hidden files (e.g. ".bashrc") to prevent configuration-file
	// injection into ~/.odek/media.
	if strings.HasPrefix(base, ".") {
		return fallbackDocName(fileID, filePath)
	}
	// Restrict to a safe character set. Replace everything else with an
	// underscore so the filename remains useful but cannot carry shell
	// metacharacters, control characters, or Unicode homoglyphs.
	var safe []rune
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			safe = append(safe, r)
		case r >= 'A' && r <= 'Z':
			safe = append(safe, r)
		case r >= '0' && r <= '9':
			safe = append(safe, r)
		case r == '.', r == '_', r == '-':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
	}
	base = string(safe)
	if base == "" || base == "." {
		return fallbackDocName(fileID, filePath)
	}
	// Enforce maximum length while preserving the extension.
	if len(base) > maxDocNameLen {
		ext := filepath.Ext(base)
		if ext == "" {
			ext = ".bin"
		}
		maxStem := maxDocNameLen - len(ext)
		if maxStem < 1 {
			base = "name" + ext
		} else {
			stem := base[:len(base)-len(ext)]
			if len(stem) > maxStem {
				stem = stem[:maxStem]
			}
			base = stem + ext
		}
	}
	return base
}

// fallbackDocName returns a deterministic filename derived from the Telegram
// file ID and the original file extension.
func fallbackDocName(fileID, filePath string) string {
	ext := filepath.Ext(filePath)
	if ext == "" {
		ext = ".bin"
	}
	return "doc_" + fileID[:min(16, len(fileID))] + ext
}

// ── Media Cleanup ──────────────────────────────────────────────────────────

// CleanupMedia removes media files older than maxAge from the downloaded
// media directory (~/.odek/media/). Returns the number of files removed.
// Non-existent directories and subdirectories are silently skipped.
func CleanupMedia(maxAge time.Duration) (int, error) {
	dir, err := MediaDir()
	if err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // nothing to clean
		}
		return 0, fmt.Errorf("telegram cleanup: read dir: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	var removed int

	for _, e := range entries {
		if e.IsDir() {
			continue // skip subdirectories
		}

		info, err := e.Info()
		if err != nil {
			continue // skip unreadable entries
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, e.Name())
			if err := os.Remove(path); err != nil {
				// Log but don't fail — one bad file shouldn't block cleanup.
				continue
			}
			removed++
		}
	}

	return removed, nil
}
