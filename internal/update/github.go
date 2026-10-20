// Package update installs and upgrades the external components the gateway
// drives (Xray, Mosdns, geodata). Every download is verified against the
// checksum its publisher provides before anything is unpacked; a component
// is swapped in only after a smoke test, and the previous version is kept
// for rollback.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Repos the components come from.
const (
	RepoXray    = "XTLS/Xray-core"
	RepoMosdns  = "IrineSistiana/mosdns"
	RepoGeodata = "Loyalsoldier/v2ray-rules-dat"
)

// GitHub is a minimal releases client; base URLs are fields so tests can
// point it at an httptest server.
type GitHub struct {
	APIBase      string
	DownloadBase string
	Client       *http.Client
}

// NewGitHub returns a client with production endpoints.
func NewGitHub() *GitHub {
	return &GitHub{APIBase: "https://api.github.com", DownloadBase: "https://github.com", Client: &http.Client{Timeout: 20 * time.Second}}
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

// ValidVersion reports whether a tag is safe to interpolate into a URL.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Release is one GitHub release.
type Release struct {
	Tag        string    `json:"tag"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []Asset   `json:"assets"`
}

// Asset is one release asset with the digest GitHub reports for it.
type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>" when available
	URL    string `json:"url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r ghRelease) toRelease() Release {
	out := Release{Tag: r.TagName, Prerelease: r.Prerelease, Published: r.PublishedAt}
	for _, a := range r.Assets {
		out.Assets = append(out.Assets, Asset{Name: a.Name, Size: a.Size, Digest: a.Digest, URL: a.URL})
	}
	return out
}

func (g *GitHub) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "EdgeRouteGW/2.0")
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		msg := fmt.Sprintf("github api %s: status %d", url, res.StatusCode)
		if res.StatusCode == 403 && res.Header.Get("X-RateLimit-Remaining") == "0" {
			msg += " (rate limited; try again later)"
		}
		return fmt.Errorf("%s", msg)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

// Releases lists up to n releases, newest first.
func (g *GitHub) Releases(ctx context.Context, repo string, n int) ([]Release, error) {
	if n <= 0 || n > 50 {
		n = 20
	}
	var raw []ghRelease
	if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=%d", g.APIBase, repo, n), &raw); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		if ValidVersion(r.TagName) {
			out = append(out, r.toRelease())
		}
	}
	return out, nil
}

// Latest returns the latest non-prerelease.
func (g *GitHub) Latest(ctx context.Context, repo string) (Release, error) {
	var raw ghRelease
	if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases/latest", g.APIBase, repo), &raw); err != nil {
		return Release{}, err
	}
	if !ValidVersion(raw.TagName) {
		return Release{}, fmt.Errorf("unexpected tag %q", raw.TagName)
	}
	return raw.toRelease(), nil
}

// ReleaseByTag fetches one release.
func (g *GitHub) ReleaseByTag(ctx context.Context, repo, tag string) (Release, error) {
	if !ValidVersion(tag) {
		return Release{}, fmt.Errorf("invalid version %q", tag)
	}
	var raw ghRelease
	if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/%s", g.APIBase, repo, tag), &raw); err != nil {
		return Release{}, err
	}
	return raw.toRelease(), nil
}

// Text fetches a small text file (digest files).
func (g *GitHub) Text(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "EdgeRouteGW/2.0")
	res, err := g.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("get %s: status %d", url, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return string(b), err
}

// AssetURL builds the download URL of an asset of a tagged release.
func (g *GitHub) AssetURL(repo, tag, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", g.DownloadBase, repo, tag, asset)
}

// XrayAsset is the Xray zip for this machine.
func XrayAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "Xray-linux-64.zip", nil
	case "arm64":
		return "Xray-linux-arm64-v8a.zip", nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

// MosdnsAsset is the mosdns zip for this machine.
func MosdnsAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return "mosdns-linux-" + runtime.GOARCH + ".zip", nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

// XraySHA256 reads the SHA2-256 line of an Xray .dgst file.
func (g *GitHub) XraySHA256(ctx context.Context, tag, asset string) (string, error) {
	txt, err := g.Text(ctx, g.AssetURL(RepoXray, tag, asset+".dgst"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SHA2-256=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "SHA2-256=")), nil
		}
		if strings.HasPrefix(line, "SHA256(") {
			if _, after, ok := strings.Cut(line, "= "); ok {
				return strings.TrimSpace(after), nil
			}
		}
	}
	return "", fmt.Errorf("no SHA2-256 in %s.dgst", asset)
}

// AssetDigest returns the sha256 GitHub reports for an asset (mosdns ships
// no digest file; the releases API carries one per asset). Fail closed.
func AssetDigest(rel Release, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			if h, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(h) == 64 {
				return h, nil
			}
			return "", fmt.Errorf("asset %s has no sha256 digest", name)
		}
	}
	return "", fmt.Errorf("asset %s not in release %s", name, rel.Tag)
}

// GeodataSHA256 reads rules.zip.sha256sum of a geodata release.
func (g *GitHub) GeodataSHA256(ctx context.Context, tag string) (string, error) {
	txt, err := g.Text(ctx, g.AssetURL(RepoGeodata, tag, "rules.zip.sha256sum"))
	if err != nil {
		return "", err
	}
	f := strings.Fields(txt)
	if len(f) == 0 || len(f[0]) != 64 {
		return "", fmt.Errorf("invalid sha256sum file")
	}
	return f[0], nil
}
