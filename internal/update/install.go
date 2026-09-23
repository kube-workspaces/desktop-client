// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Installer runs the whole flow — check, download, verify, unpack, swap —
// for the CLI and the shell alike, so the two never disagree about what an
// update means. Zero value is usable except for the fields noted; every
// network call honours ctx.
type Installer struct {
	// Fetcher finds the latest release. Zero value queries production GitHub.
	Fetcher Fetcher
	// Client downloads archives. Nil means a default client.
	Client *http.Client
	// Verifier checks the archive. Nil means [ChecksumVerifier].
	Verifier Verifier
	// VersionVerify self-checks the staged binary. Nil means [VerifyVersion].
	VersionVerify func(binary, wantTag string) error
	// GOOS and GOARCH select the asset; empty means the runtime values.
	GOOS, GOARCH string
	// ExePath is the running binary whose install is updated. Empty means
	// os.Executable().
	ExePath string
}

func (in *Installer) verifier() Verifier {
	if in.Verifier != nil {
		return in.Verifier
	}
	return ChecksumVerifier{}
}

func (in *Installer) platform() (string, string) {
	goos, goarch := in.GOOS, in.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (in *Installer) exePath() (string, error) {
	if in.ExePath != "" {
		return in.ExePath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locate running binary: %w", err)
	}
	return exe, nil
}

// Check fetches the latest release and compares it to current.
func (in *Installer) Check(ctx context.Context, current string) (Result, error) {
	rel, err := in.Fetcher.GetLatest(ctx)
	if err != nil {
		return Result{Current: current}, err
	}
	return Check(current, rel), nil
}

// Install downloads rel's archive for the configured platform, verifies it
// against the release's SHA256SUMS, unpacks it and swaps it into the install
// that ExePath runs from. progress receives cumulative download bytes and
// may be nil. The archive is verified before it is unpacked and unpacked
// before anything moves: a failure at any step leaves the install untouched.
func (in *Installer) Install(ctx context.Context, rel *Release, progress func(downloaded int64)) error {
	p, err := in.Prepare(ctx, rel, progress)
	if err != nil {
		return err
	}
	defer p.Close()
	exe, err := in.exePath()
	if err != nil {
		return err
	}
	return Apply(p.Staged, exe, rel.Tag, in.VersionVerify)
}

// Prepared owns a verified download. Close removes its private staging directory.
type Prepared struct {
	Staged Staged
	Tag    string
	Work   string
}

func (p *Prepared) Close() { _ = os.RemoveAll(p.Work) }

// Prepare downloads and verifies without touching the running installation.
func (in *Installer) Prepare(ctx context.Context, rel *Release, progress func(int64)) (_ *Prepared, retErr error) {
	if rel == nil {
		return nil, fmt.Errorf("update: no release to install")
	}
	goos, goarch := in.platform()
	asset, err := rel.AssetFor(goos, goarch)
	if err != nil {
		return nil, err
	}
	u, err := neturl.Parse(asset.URL)
	if err != nil || filepath.Base(u.Path) != asset.Name {
		return nil, fmt.Errorf("update: asset URL does not match archive name")
	}

	cache, err := CacheDir()
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(cache, "install-*")
	if err != nil {
		return nil, fmt.Errorf("update: create staging dir: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(work)
		}
	}()

	archive, err := Download(ctx, in.Client, asset.URL, work, progress)
	if err != nil {
		return nil, err
	}
	sums, err := fetchSums(ctx, in.Client, rel)
	if err != nil {
		return nil, err
	}
	v := in.verifier()
	if err := v.VerifyChecksum(archive, sums, asset.Name); err != nil {
		return nil, err
	}
	// Signatures are the future second step (§7 of the plan): the one
	// acceptable outcome today is explicitly-not-configured, decided here in
	// the open rather than buried in a helper.
	if err := v.VerifySignature(archive, rel); err != nil && !errors.Is(err, ErrSignatureNotConfigured) {
		return nil, err
	}

	stageDir, err := os.MkdirTemp(work, "stage-*")
	if err != nil {
		return nil, fmt.Errorf("update: create unpack dir: %w", err)
	}
	staged, err := Unpack(archive, stageDir)
	if err != nil {
		return nil, err
	}
	if filepath.Base(staged.Dir) != "kube-workspaces-"+goos+"-"+goarch {
		return nil, fmt.Errorf("update: wrong platform archive layout")
	}
	if !staged.HasChild && (goos != "windows" || goarch != "arm64") {
		return nil, fmt.Errorf("update: release archive is missing the web child")
	}
	verify := in.VersionVerify
	if verify == nil {
		verify = VerifyVersion
	}
	if err := verify(staged.Shell, rel.Tag); err != nil {
		return nil, err
	}
	return &Prepared{Staged: staged, Tag: rel.Tag, Work: work}, nil
}

// fetchSums downloads the release's SHA256SUMS: from its asset entry when
// present, else from the conventional download URL for the tag.
func fetchSums(ctx context.Context, client *http.Client, rel *Release) ([]byte, error) {
	url := ""
	for _, a := range rel.Assets {
		if a.Name == SumsName {
			url = a.URL
			break
		}
	}
	if url == "" {
		return nil, ErrChecksumMissing
	}
	if client == nil {
		want := "https://github.com/kube-workspaces/desktop-client/releases/download/" + rel.Tag + "/" + SumsName
		if url != want {
			return nil, fmt.Errorf("update: unexpected checksum URL")
		}
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 10 {
				return fmt.Errorf("update: invalid checksum redirect")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build checksum request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: download SHA256SUMS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &DownloadError{URL: url, Status: resp.Status}
	}
	sums, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("update: read SHA256SUMS: %w", err)
	}
	return sums, nil
}
