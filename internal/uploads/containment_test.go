package uploads

import (
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveRefusesAgentDirectoryEscape(t *testing.T) {
	for _, within := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside", true: "other-agent"}[within], func(t *testing.T) {
			s := New(config.Paths{Home: t.TempDir()})
			if err := os.MkdirAll(s.root, 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if within {
				outside = s.AgentDir("agent2")
				if err := os.MkdirAll(outside, 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := s.AgentDir("agent1")
			if runtime.GOOS == "windows" {
				if out, err := hiddenexec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
					t.Fatalf("junction: %v %s", err, out)
				}
			} else if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
			if _, err := s.Save("agent1", "file.txt", "text/plain", []byte("test")); err == nil {
				t.Fatal("write escaped agent folder")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside changed: %v %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
				t.Fatalf("unexpected output: %v", err)
			}
		})
	}
}
