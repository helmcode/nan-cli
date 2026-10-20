package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Release is the newest published version of this CLI.
type Release struct {
	Version     string
	PublishedAt string
	NotesURL    string
}

// releasesURL is GitHub's answer for the repository's latest release. The
// repository is public, so this needs no credential, and it is the same
// place the installers download from: a version said to exist here is one
// they can install.
var releasesURL = "https://api.github.com/repos/helmcode/nan-cli/releases/latest"

// LatestRelease asks which version is the newest. A failure is just "do not
// know": nothing depends on it but a line that says an update exists.
func LatestRelease(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := (&http.Client{Timeout: requestTimeout, CheckRedirect: noRedirects}).Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var wire struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return nil, err
	}
	return &Release{
		Version:     strings.TrimPrefix(wire.TagName, "v"),
		PublishedAt: wire.PublishedAt,
		NotesURL:    wire.HTMLURL,
	}, nil
}
