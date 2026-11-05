//go:build windows

package discover

import (
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"strconv"
	"strings"
	"time"
)

// listProcs asks WMI (through PowerShell, hidden) for the process table with command
// lines. Working folders are not readable this way; such agents are listed without one.
func listProcs() ([]Proc, error) {
	ps := hiddenexec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,CommandLine,@{n='Started';e={$_.CreationDate.ToUniversalTime().ToString('o')}} | ConvertTo-Json -Compress")
	out, err := ps.Output()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ProcessId, ParentProcessId int
		CommandLine                string
		Started                    string
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	procs := make([]Proc, 0, len(rows))
	for _, r := range rows {
		p := Proc{PID: r.ProcessId, PPID: r.ParentProcessId, Args: splitCommandLine(r.CommandLine)}
		if t, err := time.Parse(time.RFC3339Nano, r.Started); err == nil {
			p.Started = t
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func splitCommandLine(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ' ' && !quoted:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func procCwds([]int) map[int]string { return map[int]string{} }

func alive(pid int) bool {
	tl := hiddenexec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := tl.Output()
	return err == nil && strings.Contains(string(out), " "+strconv.Itoa(pid)+" ")
}

// terminate asks the process to close (taskkill without /F, that PID only).
func terminate(pid int) error {
	tk := hiddenexec.Command("taskkill", "/PID", strconv.Itoa(pid))
	return tk.Run()
}
