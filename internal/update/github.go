package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GitHub releases are filtered to the fleet namespace before version comparison.

type release struct {
	TagName    string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	HTMLURL    string  `json:"html_url"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"` // the API URL; with Accept: application/octet-stream it redirects to the file
}

// errNoAccess: the releases are not visible (a private repository without a token, or a token
// without access to it).
var errNoAccess = errors.New("cannot see the releases")

func (r release) find(name string) (asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return asset{}, false
}

func (u *Updater) get(ctx context.Context, url, accept string) (*http.Response, error) {
	return u.getFrom(ctx, url, accept, 0)
}

// getFrom is get starting at byte offset (a Range request when offset > 0): the answer is 200
// (the whole file) or 206 (from offset).
func (u *Updater) getFrom(ctx context.Context, url, accept string, offset int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "tmx-updater/"+u.o.Version)
	// Only api.github.com (or the configured API) sees the token: the client drops the
	// Authorization header when a download redirects to another host.
	if tok := u.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := u.o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK || (offset > 0 && resp.StatusCode == http.StatusPartialContent) {
		return resp, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		if strings.Contains(string(b), "rate limit") {
			return nil, fmt.Errorf("GitHub's rate limit; trying again later")
		}
		return nil, fmt.Errorf("%w (HTTP %d)", errNoAccess, resp.StatusCode)
	}
	return nil, fmt.Errorf("GitHub answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// latestRelease lists releases instead of consulting either upstream latest endpoint.
// Bounded pagination fails closed if the repository exceeds the scan limit.
func (u *Updater) latestRelease(ctx context.Context, repo, channel string) (release, error) {
	return u.findRelease(ctx, repo, channel, "")
}

// findRelease is latestRelease, or with want only that tag, under the same channel rules.
func (u *Updater) findRelease(ctx context.Context, repo, channel, want string) (release, error) {
	var best release
	var bestVersion Version
	for page := 1; page <= 10; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", u.o.APIBase, repo, page)
		resp, err := u.get(ctx, endpoint, "application/vnd.github+json")
		if err != nil {
			return release{}, err
		}
		var releases []release
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases)
		resp.Body.Close()
		if err != nil {
			return release{}, fmt.Errorf("reading fleet releases: %w", err)
		}
		for _, r := range releases {
			v, ok := ParseVersion(r.TagName)
			// An rc tag is a prerelease whatever its release says: stable never installs one.
			if !ok || r.Draft || ((r.Prerelease || v.Prerelease()) && channel != "latest") {
				continue
			}
			if want != "" && r.TagName != want {
				continue
			}
			if best.TagName == "" || bestVersion.Less(v) {
				best, bestVersion = r, v
			}
		}
		if len(releases) < 100 {
			if best.TagName == "" && want != "" {
				return release{}, fmt.Errorf("no published fleet release %s on the %s channel", want, channel)
			}
			if best.TagName == "" {
				return release{}, errors.New("no fleet release available")
			}
			return best, nil
		}
	}
	return release{}, errors.New("fleet release scan limit exceeded")
}

// fetch downloads an asset into w, at most max bytes.
func (u *Updater) fetch(ctx context.Context, a asset, w io.Writer, max int64) error {
	if a.Size > max {
		return fmt.Errorf("%s is %d bytes, more than allowed", a.Name, a.Size)
	}
	resp, err := u.get(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	if n > max {
		return fmt.Errorf("%s is larger than allowed", a.Name)
	}
	return nil
}
