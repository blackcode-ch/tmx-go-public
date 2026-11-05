package release

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// With TMX_FAKE_GH_OUT set, the test binary is a fake gh: it copies the file named by --input to
// that path and fails on --input - (stdin, which gh sends with no Content-Length).
func TestMain(m *testing.M) {
	if out := os.Getenv("TMX_FAKE_GH_OUT"); out != "" {
		os.Exit(fakeGh(os.Args[1:], out))
	}
	os.Exit(m.Run())
}

func fakeGh(args []string, out string) int {
	for i, a := range args {
		if a != "--input" || i+1 >= len(args) {
			continue
		}
		if args[i+1] == "-" {
			fmt.Fprintln(os.Stderr, "gh: Bad Content-Length (HTTP 400)")
			return 1
		}
		b, err := os.ReadFile(args[i+1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(out, b, 0o600); err != nil {
			return 1
		}
		return 0
	}
	return 1
}

// The real Gh uploads from a file (gh then sends a Content-Length; uploads.github.com refuses a
// chunked body), sends the exact bytes, and leaves no temp file behind.
func TestGhUploadSendsAFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TMPDIR", tmp)
	out := t.TempDir() + "/got"
	t.Setenv("TMX_FAKE_GH_OUT", out)
	data := []byte("fleet-release-v1:ed25519:c2ln\n")
	if err := (Gh{Path: os.Args[0]}).Upload(context.Background(), "https://uploads.github.com/repos/o/r/releases/1/assets?name=SHA256SUMS.sig", data); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != string(data) {
		t.Fatalf("uploaded %q, want %q", got, data)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if err := (Gh{Path: os.Args[0]}).Upload(context.Background(), "https://example.com/x", data); err == nil {
		t.Fatal("uploaded outside uploads.github.com")
	}
}
