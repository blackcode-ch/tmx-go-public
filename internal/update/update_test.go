package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// fakeGitHub serves one release per channel: /repos/o/r/releases/latest and /releases/tags/latest,
// with assets under /assets/<name>. Requests without the token get 404 when private is set.
type fakeGitHub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	releases map[string]release // "stable" | "latest"
	files    map[string][]byte
	private  bool
	token    string
	hits     int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{releases: map[string]release{}, files: map[string][]byte{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.hits++
		if g.private && r.Header.Get("Authorization") != "Bearer "+g.token {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch {
		case r.URL.Path == "/repos/o/r/releases":
			var releases []release
			for _, rel := range g.releases {
				releases = append(releases, rel)
			}
			json.NewEncoder(w).Encode(releases)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "accept", http.StatusBadRequest)
				return
			}
			b, ok := g.files[strings.TrimPrefix(r.URL.Path, "/assets/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// publish adds a release with files, a SHA256SUMS over them and its signature by key.
func (g *fakeGitHub) publish(channel, tag string, key ed25519.PrivateKey, files map[string][]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var sums strings.Builder
	r := release{TagName: tag, Prerelease: channel == "latest", HTMLURL: "https://example.invalid/" + tag}
	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
		g.files[name] = b
		r.Assets = append(r.Assets, asset{Name: name, Size: int64(len(b)), URL: g.srv.URL + "/assets/" + name})
	}
	g.files["SHA256SUMS"] = []byte(sums.String())
	r.Assets = append(r.Assets, asset{Name: "SHA256SUMS", URL: g.srv.URL + "/assets/SHA256SUMS"})
	if key != nil {
		g.files["SHA256SUMS.sig"] = []byte(FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(key, signatureMessage(FleetSignatureContext, "o/r", tag, []byte(sums.String())))) + "\n")
		r.Assets = append(r.Assets, asset{Name: "SHA256SUMS.sig", URL: g.srv.URL + "/assets/SHA256SUMS.sig"})
	}
	g.releases[channel] = r
}

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

type harness struct {
	u        *Updater
	exe      string
	restarts chan string
	busy     func() []string
	mu       sync.Mutex
	settings config.UpdateConfig
}

func newHarness(t *testing.T, g *fakeGitHub, pub, version, exe, goos string, desktop bool) *harness {
	h := &harness{exe: exe, restarts: make(chan string, 1)}
	h.busy = func() []string { return nil }
	on := true
	h.settings = config.UpdateConfig{Auto: &on, Repo: "o/r"}
	paths := config.Paths{Home: t.TempDir()}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	h.u = New(Options{
		Paths: paths, Version: version, Executable: exe, Desktop: desktop,
		Settings: func() config.UpdateConfig { h.mu.Lock(); defer h.mu.Unlock(); return h.settings },
		Save:     func(c config.UpdateConfig) error { h.mu.Lock(); defer h.mu.Unlock(); h.settings = c; return nil },
		Busy:     func() []string { return h.busy() },
		Restart:  func(exe string) { h.restarts <- exe },
		APIBase:  g.srv.URL, Keys: []string{pub}, GOOS: goos, GOARCH: "amd64",
		Probe: func(_ context.Context, exe string) (string, error) {
			b, err := os.ReadFile(exe)
			return "tmx " + strings.TrimPrefix(string(b), "binary "), err
		},
		VerifyBundle: func(context.Context, string) error { return nil },
		FirstCheck:   time.Hour, IdlePoll: 60 * time.Millisecond,
		GH: func() string { return "" },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done // Stop writes before TempDir cleanup removes the staged binary.
	})
	go func() {
		defer close(done)
		h.u.Run(ctx)
	}()
	return h
}

func waitState(t *testing.T, u *Updater, want State) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := u.Status(); s.State == want {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state %s, want %s (%+v)", u.Status().State, want, u.Status())
	return Status{}
}

func writeExe(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary "+version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestVersions(t *testing.T) {
	for _, s := range []string{"fleet-v0.1.0-dev", "185f95f-dirty", "fleet-v0.1.1-dirty", "0.1.1", "dev", "fleet-v1.2", "fleet-v1.2.3-rc0", "fleet-v1.2.3-rc", "fleet-v1.2.3-RC1", "fleet-v1.2.3-rc01", "fleet-v1.2.3-beta1", "fleet-v1.2.3-rc1-dirty"} {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("%q parsed as a release", s)
		}
	}
	order := []string{"fleet-v0.1.0", "fleet-v0.1.1", "fleet-v0.1.1-3-g185f95f", "fleet-v0.1.1-12-gabcdef0", "fleet-v0.2.0-rc1", "fleet-v0.2.0-rc1-2-gabcdef0", "fleet-v0.2.0-rc2", "fleet-v0.2.0-rc10", "fleet-v0.2.0", "fleet-v0.2.0-1-gabcdef0", "fleet-v1.0.0-rc1", "fleet-v1.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, ok := ParseVersion(order[i])
		if !ok || !a.Less(b) || b.Less(a) {
			t.Errorf("%s < %s", order[i-1], order[i])
		}
	}
}

// The updater orders rc tags as the signer does (internal/release.Version: rcN < final).
func TestVersionRC(t *testing.T) {
	v, ok := ParseVersion("fleet-v1.2.3-rc4")
	if !ok || v.RC != 4 || !v.Prerelease() || v.String() != "fleet-v1.2.3-rc4" {
		t.Fatalf("%+v %v", v, ok)
	}
	if f, _ := ParseVersion("fleet-v1.2.3"); f.Prerelease() || !v.Less(f) || f.Less(v) {
		t.Fatal("rc not below its release")
	}
	if a, _ := ParseVersion("fleet-v1.2.3-rc4"); a.Less(v) || v.Less(a) {
		t.Fatal("equal versions ordered")
	}
}

func TestTargets(t *testing.T) {
	sep := string(filepath.Separator)
	app := filepath.Join(sep+"Applications", "tmxg.app", "Contents", "MacOS", "tmxg")
	tg, err := detectTarget(app, true, "darwin", "arm64")
	if got := tg.executable(tg.path); got != app {
		t.Fatalf("bundle executable: %s, want %s", got, app)
	}
	if err != nil || !tg.bundle || tg.path != filepath.Join(sep+"Applications", "tmxg.app") || tg.asset("fleet-v1.0.0") != "tmxg-desktop_fleet-v1.0.0_darwin_arm64.app.zip" {
		t.Fatalf("bundle: %+v %v %s", tg, err, tg.asset("fleet-v1.0.0"))
	}
	if _, err := detectTarget("/usr/local/bin/tmx", true, "darwin", "arm64"); err == nil {
		t.Fatal("a desktop build outside a bundle was accepted")
	}
	for _, c := range []struct {
		goos string
		desk bool
		want string
	}{
		{"windows", true, "tmxg-desktop_fleet-v1.0.0_windows_amd64.exe"},
		{"windows", false, "tmxg_fleet-v1.0.0_windows_amd64.exe"},
		{"linux", false, "tmxg_fleet-v1.0.0_linux_amd64"},
		{"darwin", false, "tmxg_fleet-v1.0.0_darwin_amd64"},
	} {
		tg, _ := detectTarget("/x/tmx", c.desk, c.goos, "amd64")
		if got := tg.asset("fleet-v1.0.0"); got != c.want {
			t.Errorf("%s desktop=%v: %s, want %s", c.goos, c.desk, got, c.want)
		}
	}
}

func TestInstallsWhenIdle(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_linux_amd64": []byte("binary fleet-v0.2.0")})
	exe := filepath.Join(t.TempDir(), "bin", "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
	var mu sync.Mutex
	busy := []string{"fx-media"}
	h.busy = func() []string { mu.Lock(); defer mu.Unlock(); return busy }

	s := h.u.checkRelease(context.Background(), true)
	if !s.Available || s.Latest != "fleet-v0.2.0" {
		t.Fatalf("check: %+v", s)
	}
	s = waitState(t, h.u, StateReady)
	if len(s.Waiting) != 1 || s.Waiting[0] != "fx-media" {
		t.Fatalf("waiting: %+v", s)
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.1.1" {
		t.Fatal("installed while an agent was busy")
	}
	mu.Lock()
	busy = nil
	mu.Unlock()
	select {
	case got := <-h.restarts:
		if got != exe {
			t.Fatalf("restart %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no install: %+v", h.u.Status())
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.2.0" {
		t.Fatalf("exe after install: %q", b)
	}
	// The payload above is a fixture, not a native executable; Windows has no execute mode bit.
	if fi, err := os.Stat(exe); err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0) {
		t.Fatalf("installed file: %v %v", fi, err)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".tmxg-update-*")); len(m) != 0 {
		t.Fatalf("leftovers: %v", m)
	}
	// The new binary starts on probation: confirm healthy within the window or be rolled back.
	p, on, err := rollback.OnProbation(filepath.Join(h.u.o.Paths.Home, "rollback"))
	if err != nil || !on || p.From != "fleet-v0.1.1" || p.To != "fleet-v0.2.0" || p.Starts != 0 {
		t.Fatalf("probation %+v %v %v", p, on, err)
	}
	if st := h.u.Status(); st.Probation != "fleet-v0.2.0" {
		t.Fatalf("status probation %q", st.Probation)
	}
}

func TestInstallNowDoesNotWait(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_windows_amd64.exe": []byte("binary fleet-v0.2.0")})
	exe := filepath.Join(t.TempDir(), "tmx.exe")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "windows", false)
	h.busy = func() []string { return []string{"always-busy"} }
	h.mu.Lock()
	off := false
	h.settings.Auto = &off
	h.mu.Unlock()
	if s := h.u.checkRelease(context.Background(), true); s.State != StateAvailable {
		t.Fatalf("auto off: %+v", s)
	}
	h.u.Install(true)
	select {
	case <-h.restarts:
	case <-time.After(5 * time.Second):
		t.Fatalf("no install: %+v", h.u.Status())
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.2.0" {
		t.Fatalf("exe: %q", b)
	}
	if m, _ := filepath.Glob(exe + ".old-*"); len(m) != 1 { // Windows: the running exe moves aside
		t.Fatalf("old exe: %v", m)
	}
	cleanup(h.u.target)
	if m, _ := filepath.Glob(exe + ".old-*"); len(m) != 0 {
		t.Fatalf("cleanup left %v", m)
	}
	if err := rollback.Restore(filepath.Join(h.u.o.Paths.Home, "rollback"), exe, h.u.o.Paths.DB()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.1.1" {
		t.Fatalf("retained N-1 binary: %q", b)
	}
}

func TestSnapshotFailurePreventsInstall(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_windows_amd64.exe": []byte("binary fleet-v0.2.0")})
	exe := filepath.Join(t.TempDir(), "tmx.exe")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "windows", false)
	h.mu.Lock()
	off := false
	h.settings.Auto = &off
	h.mu.Unlock()
	if err := os.Remove(h.u.o.Paths.DB()); err != nil {
		t.Fatal(err)
	}
	if s := h.u.Check(context.Background()); s.State != StateAvailable {
		t.Fatalf("check %+v", s)
	}
	h.u.Install(true)
	s := waitState(t, h.u, StateError)
	if !strings.Contains(s.Error, "cannot prepare rollback") {
		t.Fatalf("error %s", s.Error)
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.1.1" {
		t.Fatalf("binary changed: %q", b)
	}
	select {
	case <-h.restarts:
		t.Fatal("restarted without checkpoint")
	default:
	}
}

func TestRefusesBadReleases(t *testing.T) {
	key, pub := newKey(t)
	other, _ := newKey(t)
	cases := map[string]func(g *fakeGitHub){
		"wrong key": func(g *fakeGitHub) {
			g.publish("stable", "fleet-v0.2.0", other, map[string][]byte{"tmxg_fleet-v0.2.0_linux_amd64": []byte("binary fleet-v0.2.0")})
		},
		"unsigned": func(g *fakeGitHub) {
			g.publish("stable", "fleet-v0.2.0", nil, map[string][]byte{"tmxg_fleet-v0.2.0_linux_amd64": []byte("binary fleet-v0.2.0")})
		},
		"tampered file": func(g *fakeGitHub) {
			g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_linux_amd64": []byte("binary fleet-v0.2.0")})
			g.files["tmxg_fleet-v0.2.0_linux_amd64"] = []byte("binary fleet-v0.2.0 evil")
		},
		"lies about its version": func(g *fakeGitHub) {
			g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_linux_amd64": []byte("binary fleet-v0.1.9")})
		},
		"no build for this machine": func(g *fakeGitHub) {
			g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg_fleet-v0.2.0_linux_arm64": []byte("binary fleet-v0.2.0")})
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			setup(g)
			exe := filepath.Join(t.TempDir(), "tmx")
			writeExe(t, exe, "fleet-v0.1.1")
			h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
			h.u.checkRelease(context.Background(), true)
			s := waitState(t, h.u, StateError)
			if s.Error == "" {
				t.Fatal("no error")
			}
			select {
			case <-h.restarts:
				t.Fatal("restarted")
			case <-time.After(100 * time.Millisecond):
			}
			if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.1.1" {
				t.Fatalf("exe changed: %q", b)
			}
			if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".tmxg-update-*")); len(m) != 0 {
				t.Fatalf("leftovers: %v", m)
			}
			t.Logf("%s: %s", name, s.Error)
		})
	}
}

func zipBundle(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestInstallsBundle(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	g.publish("stable", "fleet-v0.2.0", key, map[string][]byte{"tmxg-desktop_fleet-v0.2.0_darwin_amd64.app.zip": zipBundle(t, map[string]string{
		"tmxg.app/Contents/MacOS/tmxg": "binary fleet-v0.2.0", "tmxg.app/Contents/Info.plist": "<plist/>",
	})})
	apps := filepath.Join(t.TempDir(), "Applications")
	exe := filepath.Join(apps, "tmxg.app", "Contents", "MacOS", "tmxg")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "darwin", true)
	h.u.checkRelease(context.Background(), true)
	select {
	case got := <-h.restarts:
		if got != exe {
			t.Fatalf("restart %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no install: %+v", h.u.Status())
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.2.0" {
		t.Fatalf("bundle exe: %q", b)
	}
	if _, err := os.Stat(filepath.Join(apps, "tmxg.app", "Contents", "Info.plist")); err != nil {
		t.Fatal(err)
	}
	cleanup(h.u.target)
	entries, _ := os.ReadDir(apps)
	if len(entries) != 2 || entries[0].Name() != "tmxg.app" || !strings.HasPrefix(entries[1].Name(), "tmxg.app.prev-") {
		t.Fatalf("Applications holds %v", entries)
	}
	if b, err := os.ReadFile(filepath.Join(apps, entries[1].Name())); err != nil || string(b) != "binary fleet-v0.1.1" {
		t.Fatalf("retained bundle executable: %q %v", b, err)
	}
}

func TestBadZipIsRefused(t *testing.T) {
	dir := t.TempDir()
	for name, files := range map[string]map[string]string{
		"escapes":  {"tmxg.app/Contents/MacOS/tmxg": "x", "../evil": "x"},
		"not app":  {"tmx/Contents/MacOS/tmxg": "x"},
		"two apps": {"a.app/x": "x", "b.app/x": "x"},
	} {
		z := filepath.Join(dir, name+".zip")
		os.WriteFile(z, zipBundle(t, files), 0o600)
		if _, err := unzipBundle(z, filepath.Join(dir, name)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ditto keeps extended attributes (com.apple.provenance on a Mac that built the app) as ._name
// files beside each file; written into the bundle they would break its sealed signature.
func TestUnzipSkipsAppleDouble(t *testing.T) {
	dir := t.TempDir()
	z := filepath.Join(dir, "app.zip")
	os.WriteFile(z, zipBundle(t, map[string]string{
		"tmxg.app/Contents/MacOS/tmxg": "binary", "tmxg.app/Contents/MacOS/._tmx": "attrs",
		"tmxg.app/Contents/._Info.plist": "attrs", "tmxg.app/._Contents": "attrs",
		"__MACOSX/tmxg.app/._x": "attrs", "tmxg.app/Contents/Info.plist": "plist",
	}), 0o600)
	app, err := unzipBundle(z, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(app, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(app, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(got)
	if strings.Join(got, ",") != "Contents/Info.plist,Contents/MacOS/tmxg" {
		t.Fatalf("unpacked %v", got)
	}
}

func TestLatestChannelAndPrivateRepo(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	g.private, g.token = true, "secret-token"
	g.publish("latest", "fleet-v0.1.1-3-g185f95f", key, map[string][]byte{"tmxg_fleet-v0.1.1-3-g185f95f_linux_amd64": []byte("binary fleet-v0.1.1-3-g185f95f")})
	exe := filepath.Join(t.TempDir(), "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
	h.mu.Lock()
	h.settings.Channel, h.settings.Auto = "latest", new(bool)
	h.mu.Unlock()
	s := h.u.checkRelease(context.Background(), true)
	if s.State != StateError || !s.NeedsToken || !strings.Contains(s.Error, "gh auth login") {
		t.Fatalf("no token: %+v", s)
	}
	if err := h.u.SetToken("secret-token"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(h.u.o.Paths.GitHubTokenFile()); err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("token file: %v %v", fi, err)
	}
	s = h.u.checkRelease(context.Background(), true)
	if s.State != StateAvailable || s.Latest != "fleet-v0.1.1-3-g185f95f" || !s.HasToken || s.NeedsToken {
		t.Fatalf("latest channel: %+v", s)
	}
	if err := h.u.SetToken(""); err != nil || h.u.Status().HasToken {
		t.Fatal("token not removed")
	}
}

func TestDevBuildsAndKeylessBuildsDoNotUpdate(t *testing.T) {
	g := newFakeGitHub(t)
	_, pub := newKey(t)
	exe := filepath.Join(t.TempDir(), "tmx")
	writeExe(t, exe, "x")
	if s := newHarness(t, g, pub, "185f95f-dirty", exe, "linux", false).u.Check(context.Background()); s.State != StateOff || !strings.Contains(s.Reason, "development") {
		t.Fatalf("dev: %+v", s)
	}
	u := New(Options{Version: "fleet-v0.1.1", Executable: exe, GOOS: "linux", GOARCH: "amd64", Keys: []string{}})
	if s := u.Check(context.Background()); s.State != StateOff || !strings.Contains(s.Reason, "release key") {
		t.Fatalf("keyless: %+v", s)
	}
	if g.hits != 0 {
		t.Fatal("an off updater asked GitHub")
	}
}
