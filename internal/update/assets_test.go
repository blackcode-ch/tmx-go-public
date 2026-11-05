package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// The helpers other packages use for files of other releases (the voice engine).
func TestReleaseAssetHelpers(t *testing.T) {
	t.Setenv("TMX_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	priv, pub := newKey(t)
	file := bytes.Repeat([]byte("engine "), 1000)
	sums := "0000000000000000000000000000000000000000000000000000000000000000  whisper-cli_linux_amd64\n"
	sig := FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signatureMessage(FleetExtraSignatureContext, "o/r", "whisper-engine-v1", []byte(sums))))
	hidden := false
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hidden {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/tags/whisper-engine-v1":
			json.NewEncoder(w).Encode(release{TagName: "whisper-engine-v1", HTMLURL: "https://example.invalid/w", Assets: []asset{
				{Name: "whisper-cli_linux_amd64", Size: int64(len(file)), URL: "http://" + r.Host + "/assets/cli"}}})
		case "/repos/o/r/releases":
			w.Write([]byte("[]"))
		case "/assets/cli":
			ranges = append(ranges, r.Header.Get("Range"))
			http.ServeContent(w, r, "cli", time.Time{}, bytes.NewReader(file))
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "dev", APIBase: srv.URL, Keys: []string{pub},
		Settings: func() config.UpdateConfig { return config.UpdateConfig{Repo: "o/r"} }, GH: func() string { return "" }})
	ctx := context.Background()
	if u.Repo() != "o/r" {
		t.Fatal(u.Repo())
	}

	r, err := u.ReleaseByTag(ctx, "whisper-engine-v1")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.Find("whisper-cli_linux_amd64")
	if !ok || a.Size != int64(len(file)) || r.URL == "" {
		t.Fatalf("%+v", r)
	}
	resp, err := u.OpenAsset(ctx, a.URL, 3000)
	if err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(rest, file[3000:]) || ranges[0] != "bytes=3000-" {
		t.Fatalf("range: %d %d bytes %q", resp.StatusCode, len(rest), ranges)
	}
	if b, err := u.FetchSmall(ctx, a, 100); err == nil || b != nil {
		t.Fatal("FetchSmall ignored its limit")
	}
	if b, err := u.FetchSmall(ctx, a, 1<<20); err != nil || !bytes.Equal(b, file) {
		t.Fatalf("FetchSmall: %v", err)
	}

	if _, err := u.ReleaseByTag(ctx, "whisper-engine-v2"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("missing tag: %v", err)
	}
	hidden = true
	_, err = u.ReleaseByTag(ctx, "whisper-engine-v1")
	if !errors.Is(err, ErrNoAccess) || !strings.Contains(u.Explain(err), "gh auth login") {
		t.Fatalf("private: %v / %s", err, u.Explain(err))
	}

	list, err := u.VerifySums("whisper-engine-v1", []byte(sums), []byte(sig))
	if err != nil || len(list["whisper-cli_linux_amd64"]) != 64 {
		t.Fatalf("verify: %v %v", list, err)
	}
	other, _ := newKey(t)
	forged := FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(other, signatureMessage(FleetExtraSignatureContext, "o/r", "whisper-engine-v1", []byte(sums))))
	if _, err := u.VerifySums("whisper-engine-v1", []byte(sums), []byte(forged)); !errors.Is(err, errUnsigned) {
		t.Fatalf("forged: %v", err)
	}
	if _, err := u.VerifySums("whisper-engine-v1", []byte(sums+"x"), []byte(sig)); !errors.Is(err, errUnsigned) {
		t.Fatalf("changed sums: %v", err)
	}
}
