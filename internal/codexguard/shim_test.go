package codexguard

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestShimRefusesMissingOrChangedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(target, []byte("not executable; must never run"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	b, _ := json.Marshal(Manifest{Targets: map[string]Target{"python": {Path: target, SHA256: "wrong"}}})
	if err := os.WriteFile(manifest, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{manifest, filepath.Join(dir, "missing")} {
		var out, errs bytes.Buffer
		if code := RunShim(context.Background(), "python", nil, path, nil, &out, &errs, nil); code != 126 {
			t.Fatalf("code=%d", code)
		}
		if out.Len() != 0 || errs.Len() == 0 {
			t.Fatal("missing refusal or unexpected execution")
		}
	}
}

// TestManifestPathFromExecutableIgnoresHostileEnv is go-reviewer-o's #143 follow-up: the shim
// previously found its manifest via TMXG_CODEX_SHIM_MANIFEST, a variable in the shim's own
// (agent-controlled) environment, so an agent could point it at a manifest it wrote itself
// (e.g. claiming Own=C:\) and undo the pinned guard boundary. ManifestPathFromExecutable takes
// no environment input at all: it derives base/manifest.json from the shim's own exe path
// (base/shims/<name>.exe), which the agent cannot redirect by setting an environment variable.
func TestManifestPathFromExecutableIgnoresHostileEnv(t *testing.T) {
	t.Setenv("TMXG_CODEX_SHIM_MANIFEST", `C:\agent-controlled\manifest.json`)
	base := filepath.Join("C:", "managed", "codex-guard")
	exe := filepath.Join(base, "shims", "python.exe")
	want := filepath.Join(base, "manifest.json")
	if got := ManifestPathFromExecutable(exe); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveTargetIgnoresRelativePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npm.cmd"), []byte("must not run"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveTarget("npm", "."+string(os.PathListSeparator)+dir); err != nil || got != filepath.Join(dir, "npm.cmd") {
		t.Fatal("batch wrapper not resolved")
	}
	path := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveTarget("python", "."+string(os.PathListSeparator)+dir)
	if err != nil || got != path {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestBatchArgumentsRejectInterpretation(t *testing.T) {
	if _, err := BatchLine(`C:\Program Files\npm.cmd`, []string{"run", "test name"}); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"a&b", "%PATH%", "!X!", `a"b`, "a\nb", "x|y", "x>y", "a^b"} {
		if _, err := BatchLine(`C:\tools\npm.cmd`, []string{arg}); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}
