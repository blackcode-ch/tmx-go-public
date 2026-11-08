package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestPublicReleaseInstall(t *testing.T) {
	for _, platform := range []string{"windows", "darwin", "linux"} {
		for _, failure := range []string{"", "bad signature", "missing signature", "missing checksum", "bad checksum", "wrong repository", "wrong tag", "unlisted binary"} {
			t.Run(platform+"/"+failure, func(t *testing.T) {
				key, pub := newKey(t)
				tag := "fleet-v1.2.0"
				tg := target{path: filepath.Join(t.TempDir(), "tmxg"), goos: platform, goarch: "amd64"}
				name := tg.asset(tag)
				payload := []byte("binary " + tag)
				sums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), name))
				if failure == "unlisted binary" {
					sums = []byte(fmt.Sprintf("%x  other\n", sha256.Sum256(payload)))
				}
				repo, signedTag := config.PublicUpdateRepo, tag
				if failure == "wrong repository" {
					repo = config.DefaultUpdateRepo
				}
				if failure == "wrong tag" {
					signedTag = "fleet-v1.3.0"
				}
				sig := []byte(SignReleaseLine(FleetKeyID, key, repo, signedTag, sums))
				if failure == "bad signature" {
					sig = []byte("broken")
				}
				if failure == "bad checksum" {
					payload = []byte("tampered executable")
				}
				files := map[string][]byte{name: payload, "SHA256SUMS": sums, "SHA256SUMS.sig": sig}
				if failure == "missing signature" {
					delete(files, "SHA256SUMS.sig")
				}
				if failure == "missing checksum" {
					delete(files, "SHA256SUMS")
				}
				var rel release
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "" {
						t.Error("public source received credentials")
						http.Error(w, "credentials forbidden", 401)
						return
					}
					if r.URL.Path == "/repos/"+config.PublicUpdateRepo+"/releases" {
						json.NewEncoder(w).Encode([]release{rel})
						return
					}
					if b, ok := files[filepath.Base(r.URL.Path)]; ok {
						w.Write(b)
						return
					}
					http.NotFound(w, r)
				}))
				defer srv.Close()
				rel = release{TagName: tag}
				for n := range files {
					rel.Assets = append(rel.Assets, asset{Name: n, URL: srv.URL + "/assets/" + n})
				}
				writeExe(t, tg.path, "fleet-v1.1.0")
				u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.1.0", Executable: tg.path,
					GOOS: platform, GOARCH: "amd64", APIBase: srv.URL,
					Settings: func() config.UpdateConfig {
						return config.UpdateConfig{Repo: config.PublicUpdateRepo, FleetPublicKey: pub}
					},
					GH:    func() string { t.Error("public source queried gh credentials"); return "unused" },
					Probe: func(context.Context, string) (string, error) { return "tmx " + tag, nil },
				})
				if err := u.SetToken("stale-test-token"); err != nil {
					t.Fatal(err)
				}
				if got := u.Check(context.Background()); got.State != StateAvailable {
					t.Fatalf("check: %+v", got)
				}
				s, err := u.stage(context.Background(), tg, u.rel, tag)
				if failure != "" {
					if err == nil {
						t.Fatal("invalid release staged")
					}
					b, _ := os.ReadFile(tg.path)
					if string(b) != "binary fleet-v1.1.0" {
						t.Fatal("invalid release changed executable")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := swap(tg, s); err != nil {
					t.Fatal(err)
				}
				cleanup(tg)
				b, _ := os.ReadFile(tg.path)
				if string(b) != string(payload) {
					t.Fatal("new executable not installed")
				}
				backups, _ := filepath.Glob(tg.path + ".prev-*")
				if len(backups) != 1 {
					t.Fatalf("backups: %v", backups)
				}
				b, err = os.ReadFile(backups[0])
				if err != nil || string(b) != "binary fleet-v1.1.0" {
					t.Fatalf("backup: %q %v", b, err)
				}
			})
		}
	}
}

func TestUpdateRepositoryDefaultUnchanged(t *testing.T) {
	if got := (*config.UpdateConfig)(nil).Resolved().Repo; got != "blackcode-ch/tmx-go" {
		t.Fatal(got)
	}
}

func TestPreviousCopyIsImmutable(t *testing.T) {
	tg := target{path: filepath.Join(t.TempDir(), "tmxg"), goos: "linux"}
	writeExe(t, tg.path, "fleet-v1.0.0")
	for i := 0; i < 2; i++ {
		if err := keepPrevious(tg); err != nil {
			t.Fatal(err)
		}
	}
	old := []byte("binary fleet-v1.0.0")
	backup := fmt.Sprintf("%s.prev-%x", tg.path, sha256.Sum256(old))
	if err := os.WriteFile(backup, []byte("corrupted backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(filepath.Dir(tg.path), "new")
	writeExe(t, stagedPath, "fleet-v2.0.0")
	if _, err := swap(tg, staged{path: stagedPath}); err == nil {
		t.Fatal("installed despite a corrupt rollback copy")
	}
	if b, _ := os.ReadFile(tg.path); string(b) != string(old) {
		t.Fatal("changed installed binary")
	}
}
