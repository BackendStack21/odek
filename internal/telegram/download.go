package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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
// "voice_<fileID>.ogg" using a content-hash-safe truncation of the fileID.
func DownloadVoice(bot *Bot, fileID string) (string, error) {
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

	// Determine extension from original path.
	ext := filepath.Ext(f.FilePath)
	if ext == "" {
		ext = ".ogg"
	}

	// Hash the full fileID for a unique, collision-free filename suffix.
	localPath := filepath.Join(dir, fmt.Sprintf("voice_%s%s", fileIDSuffix(fileID), ext))

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram voice: save: %w", err)
	}

	return localPath, nil
}

// ── Photo Download ─────────────────────────────────────────────────────────

// DownloadPhoto downloads the largest available size of a photo and saves it
// to the media directory. Uses the last (largest) PhotoSize in the slice.
// Returns the local file path. Saved as "photo_<fileID>.jpg".
func DownloadPhoto(bot *Bot, fileIDs []string) (string, error) {
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

	// Determine extension.
	ext := filepath.Ext(f.FilePath)
	if ext == "" {
		ext = ".jpg"
	}

	localPath := filepath.Join(dir, fmt.Sprintf("photo_%s%s", fileIDSuffix(fileID), ext))

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram photo: save: %w", err)
	}

	return localPath, nil
}

// ── Document Download ─────────────────────────────────────────────────────

// DownloadDocument downloads a document/file from Telegram and saves it
// to the media directory. Returns the local file path. The file preserves
// the original filename from the Telegram Document metadata.
func DownloadDocument(bot *Bot, fileID, fileName string) (string, error) {
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

	// Use original filename or generate one from file ID.
	safeName := fileName
	if safeName == "" {
		ext := filepath.Ext(f.FilePath)
		if ext == "" {
			ext = ".bin"
		}
		safeName = "doc_" + fileID[:min(16, len(fileID))] + ext
	}
	localPath := filepath.Join(dir, safeName)

	if err := os.WriteFile(localPath, data, 0600); err != nil {
		return "", fmt.Errorf("telegram document: save: %w", err)
	}

	return localPath, nil
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
