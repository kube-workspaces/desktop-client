// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repo coordinates stay in one place so tests can point the fetcher at a
// local httptest server while production uses github.com.
const (
	// GitHubAPI is the production release source.
	GitHubAPI = "https://api.github.com"
	// RepoPath is the desktop-client repo under the API host.
	RepoPath = "/repos/kube-workspaces/desktop-client"
	// ReleasesRoute is the latest-release endpoint below [RepoPath].
	ReleasesRoute = "/releases/latest"
)

// Release is one GitHub release as the updater sees it: a tag plus the
// downloadable asset files.
type Release struct {
	// Tag is the release tag, e.g. v0.1.1.
	Tag string
	// Assets are the release's files by name.
	Assets []Asset
}

// Asset is one downloadable release file.
type Asset struct {
	Size int64
	// Name is the file name, e.g. kube-workspaces-v0.1.1-linux-amd64.tar.gz.
	Name string
	// URL is the direct download URL.
	URL string
}

// releaseJSON mirrors the GitHub API fields the updater reads. Every other
// field on the response is ignored.
type releaseJSON struct {
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	TagName    string `json:"tag_name"`
	Assets     []struct {
		Size               int64  `json:"size"`
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Fetcher asks a GitHub-compatible API for the latest release.
//
// APIBase is the scheme+host (and optional path prefix) to query, so tests
// can serve fixtures from httptest: production passes [GitHubAPI]. Client is
// the HTTP client; nil means a default with a 30 s timeout. Repo is the API
// path of the repo, defaulting to [RepoPath].
type Fetcher struct {
	APIBase string
	Repo    string
	Client  *http.Client
}

// GetLatest returns the latest release, or an error that callers surface as
// "could not check" rather than a failure: offline, rate-limited and
// malformed responses all mean "skip quietly", never "block startup".
func (f Fetcher) GetLatest(ctx context.Context) (*Release, error) {
	base := f.APIBase
	if base == "" {
		base = GitHubAPI
	}
	repo := f.Repo
	if repo == "" {
		repo = RepoPath
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+repo+ReleasesRoute, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("update: no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: release lookup returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("update: read release response: %w", err)
	}
	var raw releaseJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("update: parse release response: %w", err)
	}
	if raw.TagName == "" {
		return nil, fmt.Errorf("update: release response has no tag_name")
	}
	if raw.Draft || raw.Prerelease {
		return nil, fmt.Errorf("update: latest release is not stable")
	}
	if _, err := Parse(raw.TagName); err != nil {
		return nil, err
	}
	rel := &Release{Tag: raw.TagName}
	for _, a := range raw.Assets {
		if a.Name == "" || a.BrowserDownloadURL == "" {
			continue
		}
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return rel, nil
}

// Result is the outcome of comparing the running build against a release.
type Result struct {
	// Current is the stamped version this binary was built with.
	Current string
	// Latest is the release tag that was found.
	Latest string
	// Available reports whether Latest is strictly newer than Current.
	Available bool
	// Release is the release Latest came from (nil when nothing was found).
	Release *Release
}

// Check compares the running build against the latest release. An
// unversioned current build never reports available, but still reports what
// Latest is so `update --check` can show it.
func Check(current string, rel *Release) Result {
	r := Result{Current: current, Release: rel}
	if rel == nil {
		return r
	}
	r.Latest = rel.Tag
	r.Available = Newer(current, rel.Tag)
	return r
}

// AssetFor returns the download URL of this platform's archive in rel, or an
// error when the release carries no such asset.
func (r *Release) AssetFor(goos, goarch string) (Asset, error) {
	if r == nil {
		return Asset{}, fmt.Errorf("update: no release to pick an asset from")
	}
	want, err := AssetName(r.Tag, goos, goarch)
	if err != nil {
		return Asset{}, err
	}
	for _, a := range r.Assets {
		if a.Name == want {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("update: release %s has no asset %q", r.Tag, want)
}

// AssetName maps a release tag and platform to the archive file name — the
// asset contract in code (see package doc). Linux and macOS ship .tar.gz,
// Windows ships .zip. windows/arm64 intentionally resolves like the rest:
// it is shell-only inside, which is an archive-interior fact, not a naming
// one.
func AssetName(tag, goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin", "windows":
	default:
		return "", fmt.Errorf("update: unsupported platform %q", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("update: unsupported architecture %q", goarch)
	}
	if _, err := Parse(tag); err != nil {
		return "", fmt.Errorf("update: cannot name an asset for tag: %w", err)
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("kube-workspaces-%s-%s-%s.%s", tag, goos, goarch, ext), nil
}

// Download streams url to a new file under dir, reporting cumulative bytes
// to progress (which may be nil). A non-200 status is a typed failure, not a
// truncated file: the partial file is removed.
func Download(ctx context.Context, client *http.Client, url, dir string, progress func(downloaded int64)) (string, error) {
	if client == nil {
		if !strings.HasPrefix(url, "https://github.com/kube-workspaces/desktop-client/releases/download/") {
			return "", fmt.Errorf("update: unexpected asset URL")
		}
		client = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 10 {
				return fmt.Errorf("update: invalid download redirect")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("update: build download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &DownloadError{URL: url, Status: resp.Status}
	}

	name := filepath.Base(req.URL.Path)
	if name == "" || name == "/" || strings.Contains(name, "\x00") {
		name = "update-download"
	}
	dst := filepath.Join(dir, name)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("update: create download file: %w", err)
	}
	var downloaded int64
	_, copyErr := io.Copy(out, &progressReader{r: io.LimitReader(resp.Body, (512<<20)+1), f: func(n int) {
		downloaded += int64(n)
		if progress != nil {
			progress(downloaded)
		}
	}})
	syncErr := out.Close()
	if downloaded > 512<<20 {
		copyErr = fmt.Errorf("archive exceeds download limit")
	}
	if copyErr != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("update: download: %w", copyErr)
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("update: save download: %w", syncErr)
	}
	return dst, nil
}

// DownloadError is a failed archive fetch that reached the server.
type DownloadError struct {
	URL    string
	Status string
}

func (e *DownloadError) Error() string {
	return fmt.Sprintf("update: download %s returned %s", e.URL, e.Status)
}

type progressReader struct {
	r io.Reader
	f func(n int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 && p.f != nil {
		p.f(n)
	}
	return n, err
}

// CacheDir returns a directory for staged downloads: the OS user cache with
// the app name below it, created as needed.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("update: locate cache dir: %w", err)
	}
	dir := filepath.Join(base, "kube-workspaces", "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: create cache dir: %w", err)
	}
	return dir, nil
}
