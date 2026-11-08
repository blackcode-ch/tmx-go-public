package update

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreviousRetention(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			tg := target{path: filepath.Join(t.TempDir(), "tmxg"), goos: goos}
			for i := 0; i < 5; i++ {
				writeExe(t, tg.path, fmt.Sprintf("fleet-v1.0.%d", i))
				if err := keepPrevious(tg); err != nil {
					t.Fatal(err)
				}
			}
			files, err := filepath.Glob(tg.path + ".prev-*")
			if err != nil || len(files) != 2 {
				t.Fatalf("backups: %v %v", files, err)
			}
		})
	}
}

func TestPrunePreviousProtectsRunningAndCurrent(t *testing.T) {
	tg := target{path: filepath.Join(t.TempDir(), "tmxg"), goos: "linux"}
	var files []string
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("%s.prev-%064x", tg.path, i)
		if err := os.WriteFile(p, []byte("binary"), 0700); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i), 0)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	unrelated := tg.path + ".prev-not-a-hash"
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	// Oldest is in use; second oldest is the current rollback copy.
	if err := prunePrevious(tg, files[1], files[0]); err != nil {
		t.Fatal(err)
	}
	for i, p := range files {
		_, err := os.Stat(p)
		want := i == 0 || i == 1 || i == 4
		if want && err != nil || !want && !os.IsNotExist(err) {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated file removed")
	}
}
