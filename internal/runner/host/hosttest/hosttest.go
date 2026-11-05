package hosttest

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var testExe struct {
	once sync.Once
	path string
	err  error
}

// Executable is, for a test binary that doubles as `tmx host`, a copy of it named like a
// session host's tmx copy (tmx-host-*): only such an executable is trusted as a host (alive,
// sysinfo.RegisterHosted). Made once per test binary, in its own temporary folder.
func Executable(t testing.TB) string {
	t.Helper()
	testExe.once.Do(func() {
		self, err := os.Executable()
		if err != nil {
			testExe.err = err
			return
		}
		dir, err := os.MkdirTemp("", "tmx-host-test")
		if err != nil {
			testExe.err = err
			return
		}
		dst := filepath.Join(dir, "tmx-host-test"+filepath.Ext(self))
		in, err := os.Open(self)
		if err != nil {
			testExe.err = err
			return
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			testExe.err = err
			return
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			testExe.err = err
			return
		}
		testExe.err = out.Close()
		testExe.path = dst
	})
	if testExe.err != nil {
		t.Fatal(testExe.err)
	}
	return testExe.path
}
