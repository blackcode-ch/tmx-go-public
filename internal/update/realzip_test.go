package update

import (
	"context"
	"os"
	"testing"
)

// TestRealBundle unpacks a real release zip (TMX_TEST_APP_ZIP) and checks its code signature.
func TestRealBundle(t *testing.T) {
	z := os.Getenv("TMX_TEST_APP_ZIP")
	if z == "" {
		t.Skip("set TMX_TEST_APP_ZIP to a tmxg-desktop_*_darwin_*.app.zip")
	}
	app, err := unzipBundle(z, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := codesignVerify(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	out, err := probe(context.Background(), app+"/Contents/MacOS/tmxg")
	t.Logf("unpacked %s; signature ok; version: %s %v", app, out, err)
}
