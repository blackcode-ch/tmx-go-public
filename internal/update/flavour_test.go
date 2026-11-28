package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestSignedDesktopArchiveRejectedBeforeProbation(t *testing.T) {
	const tag = "fleet-v1.0.4"
	g := newFakeGitHub(t)
	key, pin := newKey(t)
	g.publish("stable", tag, key, map[string][]byte{
		"tmxg-desktop_" + tag + "_windows_amd64.exe": []byte("PK\x03\x04 archive containing " + tag),
	})
	paths := config.Paths{Home: t.TempDir()}
	exe := filepath.Join(paths.Home, "tmxg.exe")
	migrationConfig(t, paths, config.FlavourDesktop, true)
	u, _ := migrationInstall(t, g, pin, paths, exe, "fleet-v1.0.3", false)
	if st := u.checkRelease(context.Background(), true); st.Latest != tag {
		t.Fatalf("check: %+v", st)
	}
	runScheduledMigration(t, u)
	if st := u.Status(); st.State != StateError || !strings.Contains(st.Error, "not a Windows executable") {
		t.Fatalf("signed archive passed executable check: %+v", st)
	}
}

func TestPublicSignedDesktopPreviewIsRawExecutable(t *testing.T) {
	if os.Getenv("TMX_NETWORK_TESTS") != "1" {
		t.Skip("set TMX_NETWORK_TESTS=1 to verify the signed preview")
	}
	dir := os.Getenv("TMX_PUBLIC_DESKTOP_FIXTURE")
	if dir == "" {
		t.Fatal("set TMX_PUBLIC_DESKTOP_FIXTURE to the downloaded preview-449-desktop assets")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sums, sig := read("SHA256SUMS"), read("SHA256SUMS.sig")
	keys := []config.FleetKey{{ID: FleetKeyID, Type: config.KeyEd25519, Key: FleetReleasePublicKey}}
	const tag = "preview-449-desktop"
	if err := VerifyRelease(keys, nil, config.PublicUpdateRepo, tag, sums, sig); err != nil {
		t.Fatal(err)
	}
	name := "tmxg-desktop_fleet-v1.0.3-449-g1ef46f2b_windows_amd64.exe"
	digest := sha256.Sum256(read(name))
	if got := hex.EncodeToString(digest[:]); got != parseSums(sums)[name] {
		t.Fatalf("desktop executable SHA-256: %s", got)
	}
	if err := verifyWindowsDesktop(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	t.Logf("signature verified, GUI PE, SHA-256 %x", digest)
}

func signedWindowsRelease(t *testing.T, g *fakeGitHub, tag string, key ed25519.PrivateKey) []byte {
	t.Helper()
	var desktop bytes.Buffer
	header := make([]byte, 128)
	copy(header, "MZ")
	binary.LittleEndian.PutUint32(header[0x3c:], 128)
	desktop.Write(header)
	desktop.WriteString("PE\x00\x00")
	opt := pe.OptionalHeader64{Magic: 0x20b, Subsystem: 2, NumberOfRvaAndSizes: 16}
	coff := pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, SizeOfOptionalHeader: uint16(binary.Size(opt))}
	for _, v := range []any{coff, opt} {
		if err := binary.Write(&desktop, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	// The fake version probe reads the last word, while the real PE reader
	// still sees a GUI executable header.
	desktop.WriteString("\n" + tag + "\n")
	files := map[string][]byte{
		"tmxg_" + tag + "_windows_amd64.exe":         []byte("binary " + tag),
		"tmxg-desktop_" + tag + "_windows_amd64.exe": desktop.Bytes(),
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	r := release{TagName: tag}
	add := func(name string, data []byte) {
		g.files[tag+"/"+name] = data
		r.Assets = append(r.Assets, asset{Name: name, URL: g.srv.URL + "/assets/" + tag + "/" + name})
	}
	var sums strings.Builder
	for name, data := range files {
		digest := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(digest[:]), name)
		add(name, data)
	}
	add("SHA256SUMS", []byte(sums.String()))
	add("SHA256SUMS.sig", []byte(SignReleaseLine(FleetKeyID, key, "o/r", tag, []byte(sums.String()))+"\n"))
	g.releases[tag] = r
	return desktop.Bytes()
}

func migrationInstall(t *testing.T, g *fakeGitHub, key string, paths config.Paths, exe, tag string, desktop bool) (*Updater, *int) {
	t.Helper()
	restarts := 0
	u := New(Options{
		Paths: paths, Version: tag, Executable: exe, Desktop: desktop, GOOS: "windows", GOARCH: "amd64",
		Settings: func() config.UpdateConfig {
			c, err := config.Load(paths)
			if err != nil || c.Update == nil {
				t.Fatalf("read install config: %v", err)
			}
			return *c.Update
		},
		APIBase: g.srv.URL, Keys: []string{key},
		Probe: func(_ context.Context, path string) (string, error) {
			b, err := os.ReadFile(path)
			return string(b), err
		},
		IdlePoll: time.Millisecond, Restart: func(string) { restarts++ },
		GH: func() string { return "" },
	})
	return u, &restarts
}

func migrationConfig(t *testing.T, paths config.Paths, flavour string, auto bool) {
	t.Helper()
	c := config.Config{Update: &config.UpdateConfig{Repo: "o/r", Flavour: flavour, Auto: &auto}}
	if err := config.Save(paths, c); err != nil {
		t.Fatal(err)
	}
}

func runScheduledMigration(t *testing.T, u *Updater) {
	t.Helper()
	select {
	case req := <-u.installs:
		u.runInstall(context.Background(), req)
	default:
		t.Fatalf("no install scheduled: %+v", u.Status())
	}
}

func TestAutoOffDesktopMigrationSelectsSameVersionOnly(t *testing.T) {
	const current, newer = "fleet-v1.0.3-456-g1f4b78ae", "fleet-v1.0.3-457-gabcdef0"
	g := newFakeGitHub(t)
	key, pin := newKey(t)
	future := signedWindowsRelease(t, g, newer, key)
	paths := config.Paths{Home: t.TempDir()}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	exe := filepath.Join(paths.Home, "tmxg.exe")
	writeExe(t, exe, current)
	migrationConfig(t, paths, config.FlavourDesktop, false)
	u, restarts := migrationInstall(t, g, pin, paths, exe, current, false)
	ctx := context.Background()
	if st := u.checkRelease(ctx, true); st.State != StateError || !strings.Contains(st.Error, current) || len(u.installs) != 0 {
		t.Fatalf("newer-only release should not schedule: %+v", st)
	}

	wanted := signedWindowsRelease(t, g, current, key)
	hash := sha256.Sum256(wanted)
	if err := rollback.BeginDesktopMigration(u.checkpoint(), current, current, hex.EncodeToString(hash[:]), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rollback.StartProbation(u.checkpoint(), exe, paths.DB(), current, time.Now()); err != nil {
		t.Fatalf("same-version headless start before swap: %v", err)
	}
	if _, ok, err := rollback.OnProbation(u.checkpoint()); err != nil || ok {
		t.Fatalf("same-version headless executable entered desktop probation: %v, %v", ok, err)
	}
	if st := u.checkRelease(ctx, true); st.Latest != current || !st.Available || len(u.installs) != 1 {
		t.Fatalf("same-version migration not scheduled: %+v", st)
	}
	runScheduledMigration(t, u)
	b, err := os.ReadFile(exe)
	if err != nil || *restarts != 1 || !bytes.Equal(b, wanted) {
		t.Fatalf("wrong install: error=%v restarts=%d bytes=%d status=%+v", err, *restarts, len(b), u.Status())
	}
	p, ok, err := rollback.OnProbation(u.checkpoint())
	if err != nil || !ok || p.From != current || p.To != current || p.DesktopSHA256 == "" {
		t.Fatalf("probation: %+v, %t, %v", p, ok, err)
	}
	if err := rollback.StartProbation(u.checkpoint(), exe, paths.DB(), current, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rollback.ConfirmProbation(u.checkpoint(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rollback.CompleteUpdate(u.checkpoint()); err != nil {
		t.Fatal(err)
	}

	desktop, _ := migrationInstall(t, g, pin, paths, exe, current, true)
	if st := desktop.checkRelease(ctx, true); st.RunningFlavour != config.FlavourDesktop || st.Flavour != config.FlavourDesktop ||
		st.Migration != "Switched to the desktop app." || len(desktop.installs) != 0 {
		t.Fatalf("healthy migration repeated or status is missing: %+v", st)
	}
	migrationConfig(t, paths, config.FlavourDesktop, true)
	desktop.checkRelease(ctx, true)
	runScheduledMigration(t, desktop)
	if b, err := os.ReadFile(exe); err != nil || !bytes.Equal(b, future) {
		t.Fatalf("the next update did not keep desktop: bytes=%d, %v, status=%+v", len(b), err, desktop.Status())
	}
}

func TestRolledBackDesktopMigrationWaitsForExplicitAsk(t *testing.T) {
	const current, newer = "fleet-v1.0.3", "fleet-v1.0.4"
	g := newFakeGitHub(t)
	key, pin := newKey(t)
	signedWindowsRelease(t, g, current, key)
	paths := config.Paths{Home: t.TempDir()}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	exe := filepath.Join(paths.Home, "tmxg.exe")
	writeExe(t, exe, current)
	migrationConfig(t, paths, config.FlavourDesktop, false)
	u, _ := migrationInstall(t, g, pin, paths, exe, current, false)
	ctx := context.Background()
	u.checkRelease(ctx, true)
	runScheduledMigration(t, u)
	if err := rollback.MarkFailed(u.checkpoint(), "desktop did not serve"); err != nil {
		t.Fatal(err)
	}
	if err := rollback.StartProbation(u.checkpoint(), exe, paths.DB(), current, time.Now()); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("expected rollback: %v", err)
	}
	if b, err := os.ReadFile(exe); err != nil || string(b) != "binary "+current {
		t.Fatalf("old executable not restored: %q %v", b, err)
	}

	restarted, _ := migrationInstall(t, g, pin, paths, exe, current, false)
	for range 2 {
		st := restarted.checkRelease(ctx, true)
		if st.RunningFlavour != config.FlavourHeadless || st.Flavour != config.FlavourDesktop ||
			!strings.Contains(st.MigrationError, "desktop did not serve") || len(restarted.installs) != 0 {
			t.Fatalf("failed release retried, or status lost: %+v", st)
		}
	}
	if _, err := restarted.InstallTag(ctx, current, true); err != nil || len(restarted.installs) != 1 {
		t.Fatalf("explicit retry refused: %v", err)
	}
	<-restarted.installs
	restarted.clearWant()
	migrationConfig(t, paths, config.FlavourDesktop, true)
	signedWindowsRelease(t, g, newer, key)
	if st := restarted.checkRelease(ctx, true); st.Latest != newer || len(restarted.installs) != 1 {
		t.Fatalf("new version did not unblock migration: %+v", st)
	}
}

func TestFailedDesktopMigrationSurvivesNewerHeadlessProbation(t *testing.T) {
	const current, newer = "fleet-v1.0.3", "fleet-v1.0.4"
	g := newFakeGitHub(t)
	key, pin := newKey(t)
	signedWindowsRelease(t, g, newer, key)
	paths := config.Paths{Home: t.TempDir()}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	exe := filepath.Join(paths.Home, "tmxg.exe")
	writeExe(t, exe, current)
	migrationConfig(t, paths, config.FlavourDesktop, true)
	u, _ := migrationInstall(t, g, pin, paths, exe, current, false)
	ctx := context.Background()
	u.checkRelease(ctx, true)
	runScheduledMigration(t, u)
	if err := rollback.MarkFailed(u.checkpoint(), "desktop did not start"); err != nil {
		t.Fatal(err)
	}
	if err := rollback.StartProbation(u.checkpoint(), exe, paths.DB(), newer, time.Now()); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("expected rollback: %v", err)
	}
	migrationConfig(t, paths, config.FlavourHeadless, true)
	headless, _ := migrationInstall(t, g, pin, paths, exe, current, false)
	headless.checkRelease(ctx, true)
	runScheduledMigration(t, headless)
	if err := rollback.StartProbation(headless.checkpoint(), exe, paths.DB(), newer, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rollback.ConfirmProbation(headless.checkpoint(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rollback.CompleteUpdate(headless.checkpoint()); err != nil {
		t.Fatal(err)
	}
	migrationConfig(t, paths, config.FlavourDesktop, false)
	restarted, _ := migrationInstall(t, g, pin, paths, exe, newer, false)
	st := restarted.checkRelease(ctx, true)
	if len(restarted.installs) != 0 || st.LastUpdate == nil || st.LastUpdate.DesktopMigration ||
		!strings.Contains(st.MigrationError, "desktop did not start") {
		t.Fatalf("newer headless probation erased desktop failure: %+v", st)
	}
}

func TestUnsetWindowsFlavourFollowsTheRunningBuild(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		u := &Updater{target: target{goos: "windows", desk: desktop}}
		given := config.UpdateConfig{}
		selected, err := u.selectedTarget(given)
		if err != nil || selected.desk != desktop || u.wantsDesktop(given) {
			t.Fatalf("unset flavour changed current install: %v, %v", selected, err)
		}
	}
}
