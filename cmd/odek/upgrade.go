package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/transport"
)

// upgradeCmd implements `odek upgrade [--check]`: a self-upgrade from
// GitHub Releases. The platform asset is selected by OS/arch autodetection
// (runtime.GOOS/GOARCH → odek-<goos>-<goarch>), the download is verified
// against the release's checksums.txt, and the current binary is replaced
// atomically (temp file + rename in the same directory).
func upgradeCmd(args []string) error {
	checkOnly := false
	for _, a := range args {
		switch a {
		case "--check":
			checkOnly = true
		case "--help", "-h":
			fmt.Println(`Usage: odek upgrade [--check]

Upgrade the odek binary to the latest GitHub release. The asset matching
the current OS/architecture is downloaded, verified against the release's
checksums.txt (SHA-256), and installed atomically over the running binary.

  --check   Report the latest version without installing anything.`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q for upgrade", a)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("upgrade: cannot locate current executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("upgrade: cannot resolve current executable: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := transport.NewPooledClient(60 * time.Second)

	_, err = performUpgrade(ctx, client, githubAPIBase, exe, getVersion(), checkOnly, os.Stdout)
	return err
}

const (
	// githubAPIBase is the GitHub API root used to resolve the latest
	// release. Asset downloads follow the browser_download_url returned by
	// the API. Tests pass a fake server URL instead.
	githubAPIBase = "https://api.github.com"
	githubRepo    = "BackendStack21/odek"
	// githubPageLatest is the HTML latest-release redirect. GitHub's
	// anonymous API quota (and occasional IP blocks) return 403 on the
	// REST latest endpoint; this path is not rate-limited the same way.
	githubPageLatest = "https://github.com/BackendStack21/odek/releases/latest"

	githubUserAgent = "odek-updater"
	githubAPIVer    = "2022-11-28"
)

// ghRelease is the subset of the GitHub release payload upgrade needs.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetURL returns the download URL for the named asset, or "".
func (r *ghRelease) assetURL(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// fetchLatestRelease queries the GitHub API for the repository's latest
// published release. A 401/403/429 against the real API falls back to the
// HTML latest page.
func fetchLatestRelease(ctx context.Context, client *http.Client, apiBase string) (*ghRelease, error) {
	page := ""
	if apiBase == githubAPIBase {
		page = githubPageLatest
	}
	return fetchLatest(ctx, client, apiBase, page)
}

func fetchLatest(ctx context.Context, client *http.Client, apiBase, pageURL string) (*ghRelease, error) {
	rel, err := queryReleaseAPI(ctx, client, apiBase)
	if err == nil {
		return rel, nil
	}
	if pageURL == "" || !statusRetryable(err) {
		return nil, err
	}
	rel, ferr := queryLatestPage(ctx, client, pageURL)
	if ferr != nil {
		return nil, fmt.Errorf("%w; html fallback: %v", err, ferr)
	}
	return rel, nil
}

func queryReleaseAPI(ctx context.Context, client *http.Client, apiBase string) (*ghRelease, error) {
	apiURL := strings.TrimSuffix(apiBase, "/") + "/repos/" + githubRepo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	applyGitHubHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &githubStatusError{code: resp.StatusCode, status: resp.Status}
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("cannot parse GitHub release payload: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("GitHub release payload has no tag_name")
	}
	return &rel, nil
}

func queryLatestPage(ctx context.Context, client *http.Client, pageURL string) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build latest-page request: %w", err)
	}
	req.Header.Set("User-Agent", githubUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query latest page: %w", err)
	}
	defer resp.Body.Close()
	final := resp.Request.URL
	if loc := resp.Header.Get("Location"); loc != "" && (resp.StatusCode == http.StatusFound ||
		resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect ||
		resp.StatusCode == http.StatusPermanentRedirect) {
		if u, perr := resp.Request.URL.Parse(loc); perr == nil {
			final = u
		}
	}
	tag, ok := tagFromURL(final)
	if !ok {
		return nil, fmt.Errorf("latest page did not redirect to a release tag (%s)", resp.Status)
	}
	return conventionalRelease(tag), nil
}

func tagFromURL(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "releases" && parts[i+1] == "tag" && i+2 < len(parts) && parts[i+2] != "" {
			return parts[i+2], true
		}
	}
	return "", false
}

func conventionalRelease(tag string) *ghRelease {
	base := "https://github.com/" + githubRepo + "/releases/download/" + tag + "/"
	rel := &ghRelease{TagName: tag}
	add := func(name string) {
		rel.Assets = append(rel.Assets, struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{Name: name, BrowserDownloadURL: base + name})
	}
	add("checksums.txt")
	for _, goos := range []string{"darwin", "linux"} {
		for _, arch := range []string{"amd64", "arm64"} {
			add("odek-" + goos + "-" + arch)
		}
	}
	return rel
}

func applyGitHubHeaders(req *http.Request) {
	req.Header.Set("User-Agent", githubUserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVer)
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

type githubStatusError struct {
	code   int
	status string
}

func (e *githubStatusError) Error() string {
	if e.code == http.StatusUnauthorized || e.code == http.StatusForbidden || e.code == http.StatusTooManyRequests {
		return fmt.Sprintf("GitHub API returned %s (rate limit or anonymous block — set GITHUB_TOKEN)", e.status)
	}
	return fmt.Sprintf("GitHub API returned %s", e.status)
}

func statusRetryable(err error) bool {
	var se *githubStatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.code == http.StatusUnauthorized || se.code == http.StatusForbidden || se.code == http.StatusTooManyRequests
}

// parseChecksums parses a checksums.txt body ("<sha256>  <name>" per line)
// into a name→hash map.
func parseChecksums(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && len(fields[0]) == 64 {
			out[fields[1]] = fields[0]
		}
	}
	return out
}

// compareVersions compares two "vX.Y.Z" versions, returning -1, 0, or 1.
// A current version that does not parse as semver (e.g. "dev" or a commit
// hash from a local build) is treated as older than any release.
func compareVersions(current, latest string) int {
	c, cok := parseSemver(current)
	l, lok := parseSemver(latest)
	if !lok {
		return 0 // unknown latest — caller errors out before comparing
	}
	if !cok {
		return -1
	}
	for i := 0; i < 3; i++ {
		if c[i] != l[i] {
			if c[i] < l[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// upgradeAssetName maps the running platform to its release asset name.
func upgradeAssetName() (string, error) {
	name := "odek-" + runtime.GOOS + "-" + runtime.GOARCH
	switch name {
	case "odek-darwin-amd64", "odek-darwin-arm64", "odek-linux-amd64", "odek-linux-arm64":
		return name, nil
	}
	return "", fmt.Errorf("no prebuilt release for %s/%s — build from source instead", runtime.GOOS, runtime.GOARCH)
}

// performUpgrade runs the full upgrade flow against apiBase, replacing the
// binary at exePath. It returns the installed (or available, with
// checkOnly) version. The empty string with nil error means "already up to
// date".
func performUpgrade(ctx context.Context, client *http.Client, apiBase, exePath, current string, checkOnly bool, out io.Writer) (string, error) {
	asset, err := upgradeAssetName()
	if err != nil {
		return "", err
	}

	rel, err := fetchLatestRelease(ctx, client, apiBase)
	if err != nil {
		return "", err
	}
	latest := rel.TagName
	if _, ok := parseSemver(latest); !ok {
		return "", fmt.Errorf("latest release tag %q is not a semantic version", latest)
	}

	cmp := compareVersions(current, latest)
	if cmp >= 0 {
		fmt.Fprintf(out, "odek %s is up to date (latest: %s)\n", current, latest)
		return "", nil
	}
	fmt.Fprintf(out, "upgrade available: %s → %s (%s/%s)\n", current, latest, runtime.GOOS, runtime.GOARCH)
	if checkOnly {
		return latest, nil
	}

	assetURL := rel.assetURL(asset)
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no asset %q", latest, asset)
	}
	checksumsURL := rel.assetURL("checksums.txt")
	if checksumsURL == "" {
		return "", fmt.Errorf("release %s has no checksums.txt — refusing to install an unverifiable binary", latest)
	}

	// Download into a temp dir next to the target so the final rename stays
	// on the same filesystem (atomic).
	tmpDir, err := os.MkdirTemp(filepath.Dir(exePath), ".odek-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp dir next to %s: %w", exePath, err)
	}
	defer os.RemoveAll(tmpDir)

	checksumData, err := downloadBytes(ctx, client, checksumsURL)
	if err != nil {
		return "", fmt.Errorf("cannot download checksums.txt: %w", err)
	}
	wantSum := parseChecksums(checksumData)[asset]
	if wantSum == "" {
		return "", fmt.Errorf("checksums.txt has no entry for %q — refusing to install", asset)
	}

	tmpBin := filepath.Join(tmpDir, asset)
	if err := downloadToFile(ctx, client, assetURL, tmpBin); err != nil {
		return "", fmt.Errorf("cannot download %s: %w", asset, err)
	}
	gotSum, err := fileSHA256(tmpBin)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(gotSum, wantSum) {
		return "", fmt.Errorf("checksum mismatch for %s (got %s, want %s) — refusing to install", asset, gotSum, wantSum)
	}

	// Preserve the installed binary's permission bits.
	fi, err := os.Stat(exePath)
	if err != nil {
		return "", fmt.Errorf("cannot stat %s: %w", exePath, err)
	}
	if err := os.Chmod(tmpBin, fi.Mode()); err != nil {
		return "", fmt.Errorf("cannot set permissions: %w", err)
	}
	if err := os.Rename(tmpBin, exePath); err != nil {
		if os.IsPermission(err) {
			return "", fmt.Errorf("cannot replace %s: permission denied — retry with elevated privileges (e.g. sudo odek upgrade)", exePath)
		}
		return "", fmt.Errorf("cannot replace %s: %w", exePath, err)
	}

	fmt.Fprintf(out, "upgraded %s → %s (%s)\n", current, latest, exePath)
	return latest, nil
}

func downloadBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func downloadToFile(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// Cap the download at 256 MiB — release binaries are ~12 MB.
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 256<<20))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
