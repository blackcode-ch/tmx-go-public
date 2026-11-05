package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Downloads of other files from the release repository, for the packages that fetch extras
// on demand (the voice engine, internal/transcribe). They go through the updater, so they use
// its token and headers, its errors for a private repository, and its release keys: a file is
// trusted only when the release's SHA256SUMS carries a valid signature of a key in keys.go.

// ErrNoAccess: the releases are not visible (a private repository without a token, or a token
// without access to it). Explain turns it into the sentence the owner sees.
var ErrNoAccess = errNoAccess

// ErrNoRelease: the repository's releases are visible, but there is no release with that tag.
var ErrNoRelease = errors.New("no such release")

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"` // the API URL (OpenAsset follows it to the file)
}

// Release is a release of the repository with its files.
type Release struct {
	Tag    string  `json:"tag"`
	URL    string  `json:"url"` // its page on GitHub
	Assets []Asset `json:"assets"`
}

// Find returns the asset called name.
func (r Release) Find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Repo is the release repository (config.json update.repo, default blackcode-ch/tmx-go).
func (u *Updater) Repo() string { return u.settings().Repo }

// ReleaseByTag returns the repository's release tagged tag. ErrNoRelease: there is none (the
// repository's releases are visible); ErrNoAccess: they are not.
func (u *Updater) ReleaseByTag(ctx context.Context, tag string) (Release, error) {
	repo := u.settings().Repo
	resp, err := u.get(ctx, u.o.APIBase+"/repos/"+repo+"/releases/tags/"+url.PathEscape(tag), "application/vnd.github+json")
	if errors.Is(err, errNoAccess) {
		// GitHub answers 404 both for a missing tag and for a repository it hides: ask for the
		// list of releases to tell the two apart.
		probe, perr := u.get(ctx, u.o.APIBase+"/repos/"+repo+"/releases?per_page=1", "application/vnd.github+json")
		if perr != nil {
			return Release{}, perr
		}
		probe.Body.Close()
		return Release{}, fmt.Errorf("%w %s in %s", ErrNoRelease, tag, repo)
	}
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("reading release %s: %w", tag, err)
	}
	if r.TagName != tag {
		return Release{}, fmt.Errorf("%w: mismatched fleet tag", ErrNoRelease)
	}
	if r.Draft {
		return Release{}, fmt.Errorf("%w %s in %s (it is a draft)", ErrNoRelease, tag, repo)
	}
	out := Release{Tag: r.TagName, URL: r.HTMLURL}
	for _, a := range r.Assets {
		out.Assets = append(out.Assets, Asset(a))
	}
	return out, nil
}

// OpenAsset starts downloading a release file at byte offset. The answer is 200 (the whole
// file; also when the server ignored the range) or 206 (from offset); the caller closes it.
func (u *Updater) OpenAsset(ctx context.Context, assetURL string, offset int64) (*http.Response, error) {
	return u.getFrom(ctx, assetURL, "application/octet-stream", offset)
}

// FetchSmall downloads a small release file (at most max bytes) into memory.
func (u *Updater) FetchSmall(ctx context.Context, a Asset, max int64) ([]byte, error) {
	var b limitedBuffer
	b.max = max
	if err := u.fetch(ctx, asset(a), &b, max); err != nil {
		return nil, err
	}
	return b.buf, nil
}

// VerifySums checks SHA256SUMS with the extras context, configured repo, and expected tag, and
// returns its entries (file name → lowercase hex SHA-256).
func (u *Updater) VerifySums(tag string, sums, sig []byte) (map[string]string, error) {
	keys, err := u.trustedKeys()
	if err != nil {
		return nil, err
	}
	if keys.empty() {
		return nil, errors.New("this build has no release key, so it cannot check downloads")
	}
	if err := verifySums(FleetExtraSignatureContext, u.Repo(), tag, sums, sig, keys); err != nil {
		return nil, err
	}
	return parseSums(sums), nil
}

// Explain turns a download error into the sentence the owner sees (the token hint for a
// private repository).
func (u *Updater) Explain(err error) string { return u.explain(err) }

type limitedBuffer struct {
	buf []byte
	max int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(len(b.buf)+len(p)) > b.max+1 {
		return 0, errors.New("the file is larger than allowed")
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}
