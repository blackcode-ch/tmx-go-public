package antechamber

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// InspectItem is the structural inspection plus antivirus of one quarantined blob (what the
// inspection child runs).
func InspectItem(blob, name, trust string, folder bool, limits Limits, knownBad map[string]bool, ctxPath string, av AVScanner, ownerNames ...string) ReportJSON {
	rep, err := NewInspector(trust, limits, knownBad, ctxPath, ownerNames...).InspectPath(blob, name, folder)
	if err != nil {
		rep = &Report{Trust: trust, Kind: "unknown"}
		rep.Add(mk("inspect-failed", "inspection failed: "+err.Error(), NeedsHuman, "", ""))
	}
	if lexists(blob) {
		if av != nil {
			rep.Add(av.Scan(blob))
		}
	} else {
		rep.Add(mk("av-removed", "the antivirus removed the file", Blocked, "", ""))
	}
	return rep.JSON()
}

// timeoutFor is the child's time limit: timeout_s plus 1 s per 10 MB.
func timeoutFor(l Limits, size int64) time.Duration {
	return time.Duration((l.TimeoutS + float64(size)/float64(10<<20)) * float64(time.Second))
}

// inspect runs the inspection, in a windowless child process when Isolate is set, so a hostile
// file can at worst time out (needs_human). On timeout only the child this call started is killed.
func (a *Antechamber) inspect(d, name, trust string, folder bool, size int64, ctxPath string) ReportJSON {
	blob := filepath.Join(d, "blob")
	if !a.Isolate {
		return InspectItem(blob, name, trust, folder, a.Limits, a.KnownBad(), ctxPath, a.AV, a.OwnerNames...)
	}
	failed := func(code, text string) ReportJSON {
		r := Report{Trust: trust, Kind: "unknown"}
		r.Add(mk(code, text, NeedsHuman, "", ""))
		return r.JSON()
	}
	exe := a.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return failed("inspect-failed", "inspection failed: "+err.Error())
		}
	}
	lim, _ := json.Marshal(a.Limits)
	out := filepath.Join(d, "report.json")
	args := []string{"antechamber", "inspect", "--name", name, "--trust", trust, "--out", out,
		"--known-bad", a.knownBadFile(), "--limits", string(lim), "--context", ctxPath}
	for _, ownerName := range a.OwnerNames {
		args = append(args, "--owner-name", ownerName)
	}
	if folder {
		args = append(args, "--folder")
	}
	args = append(args, "--", blob)
	timeout := timeoutFor(a.Limits, size)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenexec.CommandContext(ctx, exe, args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	cmd.Cancel = func() error { return sysinfo.KillStarted(cmd.Process) }
	cmd.WaitDelay = 5 * time.Second
	var rep ReportJSON
	if err := cmd.Start(); err != nil {
		rep = failed("inspect-failed", "inspection failed: "+err.Error())
	} else {
		if runtime.GOOS == "windows" {
			sysinfo.RegisterChild(cmd.Process.Pid, false)
		}
		werr := cmd.Wait()
		data, rerr := os.ReadFile(out)
		if rerr == nil {
			rerr = json.Unmarshal(data, &rep)
		}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			rep = failed("timeout", fmt.Sprintf("inspection took longer than %d s", int(timeout.Seconds())))
		case rerr != nil:
			tail := stderr.String()
			if len(tail) > 300 {
				tail = tail[len(tail)-300:]
			}
			if tail = strings.TrimSpace(tail); tail == "" {
				tail = "no report"
				if werr != nil {
					tail = "no report (" + werr.Error() + ")"
				}
			}
			rep = failed("inspect-failed", "inspection failed: "+tail)
		}
	}
	if !lexists(blob) {
		rep.Verdict = Blocked
		rep.Reasons = append([]Reason{{Level: Blocked, Code: "av-removed", Text: "the antivirus removed the file"}}, rep.Reasons...)
	}
	return rep
}

const mainUsage = "usage: tmx antechamber inspect [--name NAME] [--trust own|person] [--folder] [--out FILE] " +
	"[--owner-name NAME] [--known-bad FILE] [--limits JSON] [--context DIR] [--no-av] [--] PATH"

// Main is the hidden `tmx antechamber` command: `inspect` inspects one file (never opens or runs
// it) and writes the report as JSON to --out or stdout. It returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "inspect" {
		fmt.Fprintln(stderr, mainUsage)
		return 2
	}
	var path, name, out, knownBad, limits, ctxPath string
	var ownerNames []string
	trust, folder, noAV := Person, false, false
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		val := func() (string, bool) {
			if i+1 >= len(rest) {
				return "", false
			}
			i++
			return rest[i], true
		}
		var ok = true
		switch a {
		case "--owner-name":
			var name string
			name, ok = val()
			ownerNames = append(ownerNames, name)
		case "--name":
			name, ok = val()
		case "--trust":
			trust, ok = val()
			ok = ok && (trust == Own || trust == Person)
		case "--out":
			out, ok = val()
		case "--known-bad":
			knownBad, ok = val()
		case "--limits":
			limits, ok = val()
		case "--context":
			ctxPath, ok = val()
		case "--folder":
			folder = true
		case "--no-av":
			noAV = true
		case "--":
			if i+1 < len(rest) {
				path, i = rest[i+1], len(rest)
			}
		default:
			if strings.HasPrefix(a, "--") || path != "" {
				ok = false
			} else {
				path = a
			}
		}
		if !ok {
			fmt.Fprintln(stderr, mainUsage)
			return 2
		}
	}
	if path == "" {
		fmt.Fprintln(stderr, mainUsage)
		return 2
	}
	lim := DefaultLimits()
	if limits != "" {
		if err := json.Unmarshal([]byte(limits), &lim); err != nil {
			fmt.Fprintln(stderr, "tmx antechamber: --limits: "+err.Error())
			return 2
		}
	}
	if name == "" {
		name = filepath.Base(path)
	}
	bad := map[string]bool{}
	if knownBad != "" {
		bad = readKnownBad(knownBad)
	}
	var av AVScanner = SystemAV{}
	if noAV {
		av = nil
	}
	rep := InspectItem(path, name, trust, folder, lim, bad, ctxPath, av, ownerNames...)
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		fmt.Fprintln(stderr, "tmx antechamber: "+err.Error())
		return 1
	}
	if out == "" {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		fmt.Fprintln(stderr, "tmx antechamber: "+err.Error())
		return 1
	}
	return 0
}
