package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// GitHub is the GitHub REST API as the signer uses it. Gh runs the gh CLI (`gh api`, the
// signer's own login); tests inject a fake. Paths are relative to the API root, except Upload's
// absolute uploads.github.com URL.
type GitHub interface {
	// Get returns the JSON body of GET path.
	Get(ctx context.Context, path string) ([]byte, error)
	// Download returns a release asset's bytes (GET path with Accept: application/octet-stream).
	Download(ctx context.Context, path string) ([]byte, error)
	// Upload posts data as a new release asset to url.
	Upload(ctx context.Context, url string, data []byte) error
	// Patch sends PATCH path with a JSON body.
	Patch(ctx context.Context, path string, body []byte) error
}

// Gh is GitHub through `gh api` (a hidden child process; gh's own authentication).
type Gh struct{ Path string }

func (g Gh) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	path := g.Path
	if path == "" {
		path = "gh"
	}
	cmd := hiddenexec.CommandContext(ctx, path, append([]string{"api"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("gh api %s: %v: %s", args[len(args)-1], err, msg)
	}
	return out.Bytes(), nil
}

func (g Gh) Get(ctx context.Context, path string) ([]byte, error) { return g.run(ctx, nil, path) }

func (g Gh) Download(ctx context.Context, path string) ([]byte, error) {
	return g.run(ctx, nil, "-H", "Accept: application/octet-stream", path)
}

func (g Gh) Upload(ctx context.Context, url string, data []byte) error {
	if !strings.HasPrefix(url, "https://uploads.github.com/") {
		return errors.New("refusing an upload URL outside uploads.github.com")
	}
	// From a file, not stdin: gh sends stdin chunked with no Content-Length, which
	// uploads.github.com refuses (HTTP 400 "Bad Content-Length"). The data is public (a .sig).
	f, err := os.CreateTemp("", "tmx-release-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	_, err = g.run(ctx, nil, "--method", "POST", "-H", "Content-Type: application/octet-stream", "--input", f.Name(), url)
	return err
}

func (g Gh) Patch(ctx context.Context, path string, body []byte) error {
	_, err := g.run(ctx, body, "--method", "PATCH", "--input", "-", path)
	return err
}
