package antechamber

import (
	"context"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

// AVScanner scans one file with an antivirus and returns a single finding (av-ok, av-threat,
// av-removed, av-failed or av-none).
//
// SystemAV discovers Defender or ClamAV without running anything from the received file.
type AVScanner interface {
	Scan(path string) Finding
}

// NoAV is the default scanner: it scans nothing and says so, like Python with no scanner present.
type NoAV struct{}

type SystemAV struct{}

func (SystemAV) Scan(path string) Finding {
	exe, name, threat := "", "", 1
	if runtime.GOOS == "windows" {
		candidates, _ := filepath.Glob(filepath.Join(os.Getenv("ProgramData"), "Microsoft", "Windows Defender", "Platform", "*", "MpCmdRun.exe"))
		sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
		candidates = append(candidates, filepath.Join(os.Getenv("ProgramFiles"), "Windows Defender", "MpCmdRun.exe"))
		for _, p := range candidates {
			if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
				exe, name, threat = p, "Windows Defender", 2
				break
			}
		}
	}
	if exe == "" {
		for _, p := range []string{"clamdscan", "clamscan"} {
			if found, e := exec.LookPath(p); e == nil {
				exe, name = found, "ClamAV"
				break
			}
		}
	}
	if exe == "" {
		return NoAV{}.Scan(path)
	}
	args := []string{"--no-summary", path}
	if threat == 2 {
		args = []string{"-Scan", "-ScanType", "3", "-File", path, "-DisableRemediation"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := hiddenexec.CommandContext(ctx, exe, args...)
	err := cmd.Run()
	if !lexists(path) {
		return mk("av-removed", name+" removed the file", Blocked, "", "")
	}
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == threat {
		return mk("av-threat", name+" detected a threat", Blocked, "", "")
	}
	if err != nil {
		return mk("av-failed", name+" scan did not finish successfully", NeedsHuman, "", "")
	}
	return mk("av-ok", name+": nothing found", Clean, "", "")
}

// Scan reports that this machine has no antivirus scanner wired in.
func (NoAV) Scan(string) Finding {
	return mk("av-none", "no antivirus scanner on this machine (not scanned)", Clean, "", "")
}
