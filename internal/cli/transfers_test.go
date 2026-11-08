package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransfersList(t *testing.T) {
	d := cliDaemon(t)
	root := filepath.Join(d.Home, "transfers")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "1122334455667788.json"), []byte(`{"id":"1122334455667788","state":"placed","dir":"in","name":"hello.txt"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := tmx(t, "transfers", "list")
	if code != 0 || !strings.Contains(out, "1122334455667788") || !strings.Contains(out, "placed") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
}
