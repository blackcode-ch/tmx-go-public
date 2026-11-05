package term

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// The test binary doubles as a fake harness TUI when TMXG_FAKE_TUI=1: an input box with the
// harness's footer (TMXG_FAKE_FLAVOR: claude by default, opencode or pi), bracketed paste, a
// permission menu for the message "perm" (not pi: it has none), a three-option dialog for
// "pick3" that ignores keys for its first 900 ms and moves its cursor only after a delay (a
// dialog still starting up), a working indicator for anything else, and (TMXG_FAKE_TRUST=1)
// Claude's folder-trust gate first. With --session-id (or --resume) it writes the transcript
// Claude would: each message as a user record, its reply as an assistant record.
func TestMain(m *testing.M) {
	if os.Getenv("TMXG_FAKE_TUI") == "1" {
		fakeTUI()
		os.Exit(0)
	}
	// The test binary is also `tmx host` (host_test.go) and the program a host runs.
	if len(os.Args) > 1 && os.Args[1] == "host" {
		os.Exit(testHostMain(os.Args[2:]))
	}
	if os.Getenv("TMXG_FAKE_LINE") == "1" {
		fakeLine()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeTUI() {
	stty := hiddenexec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	_ = stty.Run()
	out := func(s string) { os.Stdout.WriteString(s) }
	draw := func(body string) { out("\x1b[2J\x1b[H" + strings.ReplaceAll(body, "\n", "\r\n")) }
	var mu sync.Mutex // the screen and the menu: keys and delayed cursor moves
	sid := ""
	for i, a := range os.Args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(os.Args) {
			sid = os.Args[i+1]
		}
	}
	record := func(rec map[string]any) {
		if sid == "" {
			return
		}
		dir := harness.ClaudeProjectDirIn(claude.ConfigRoot(os.Environ()), ".") // like claude: its CLAUDE_CONFIG_DIR
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, sid+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		b, _ := json.Marshal(rec)
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	flavor := os.Getenv("TMXG_FAKE_FLAVOR")
	idle := func(last string) {
		switch flavor {
		case "opencode":
			draw(last + "\n\n  ┃\n  ┃  Build  claude-sonnet\n\n  tab agents   ctrl+p commands")
		case "pi":
			draw(last + "\n\n" + strings.Repeat("─", 40) + "\n\n" + strings.Repeat("─", 40) + "\n~/project  ctrl+c twice to exit")
		default:
			draw(last + "\n\n" + strings.Repeat("─", 40) + "\n❯\n" + strings.Repeat("─", 40) + "\n  ? for shortcuts")
		}
	}
	working := map[string]string{"opencode": "  ⬝⬝■ esc interrupt", "pi": "⠋ Working... (esc to interrupt)"}[flavor]
	if working == "" {
		working = "✻ Working… (esc to interrupt)"
	}
	type menu struct {
		header   string
		labels   []string
		sel      int
		numbered bool
		deafTill time.Time     // keys before this are lost
		lag      time.Duration // the cursor moves this long after the key
		done     func(label string)
	}
	var cur *menu
	drawMenu := func() {
		var b strings.Builder
		b.WriteString(cur.header + "\n")
		for i, l := range cur.labels {
			mark := "  "
			if i+1 == cur.sel {
				mark = "❯ "
				if flavor == "opencode" {
					mark = "> "
				}
			}
			if cur.numbered {
				fmt.Fprintf(&b, " %s%d. %s\n", mark, i+1, l)
			} else {
				fmt.Fprintf(&b, "  %s%s\n", mark, l)
			}
		}
		b.WriteString("\n Esc to cancel")
		draw(b.String())
	}
	move := func(m *menu, by int) {
		if time.Now().Before(m.deafTill) {
			return
		}
		go func() {
			time.Sleep(m.lag)
			mu.Lock()
			defer mu.Unlock()
			if cur == m {
				m.sel = min(max(m.sel+by, 1), len(m.labels))
				drawMenu()
			}
		}()
	}
	out("\x1b[?2004h")
	if os.Getenv("TMXG_FAKE_TRUST") == "1" {
		cur = &menu{header: " Quick safety check: Is this a project you created or one you trust?\n",
			labels: []string{"Yes, I trust this folder", "No, exit"}, sel: 2, numbered: true,
			done: func(l string) {
				if l != "Yes, I trust this folder" {
					os.Exit(4)
				}
				idle("● trusted")
			}}
		drawMenu()
	} else {
		idle("● ready")
	}
	handle := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		record(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
		chose := func(l string) { idle("● chose: " + l) }
		switch {
		case text == "perm" && flavor == "opencode":
			cur = &menu{header: "  △ Permission required\n    $ rm -rf build\n", labels: []string{"Allow once", "Allow always", "Reject"}, sel: 1, done: chose}
			drawMenu()
			return
		case text == "perm" && flavor != "pi":
			cur = &menu{header: " Bash command\n   rm -rf build\n Do you want to proceed?",
				labels: []string{"Yes", "No, and tell Claude what to do differently (esc)"}, sel: 1, numbered: true, done: chose}
			drawMenu()
			return
		case text == "pick3":
			cur = &menu{header: " Which plan?\n", labels: []string{"Option A", "Option B", "Option C"}, sel: 1, numbered: true,
				deafTill: time.Now().Add(900 * time.Millisecond), lag: 150 * time.Millisecond, done: chose}
			drawMenu()
			return
		}
		draw("● got: " + text + "\n\n" + working)
		mu.Unlock()
		time.Sleep(700 * time.Millisecond)
		mu.Lock()
		record(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []map[string]any{{"type": "text", "text": "got: " + text}}}})
		idle("● got: " + text)
	}
	var in, paste bytes.Buffer
	pasting := false
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		in.Write(buf[:n])
		for in.Len() > 0 {
			s := in.String()
			switch {
			case strings.HasPrefix(s, "\x1b[200~"):
				pasting = true
				in.Next(6)
			case strings.HasPrefix(s, "\x1b[201~"):
				pasting = false
				in.Next(6)
			case pasting:
				r := []rune(s)[0]
				paste.WriteRune(r)
				in.Next(len(string(r)))
			case strings.HasPrefix(s, "\x1b[B") || strings.HasPrefix(s, "\x1b[A"):
				in.Next(3)
				mu.Lock()
				if m := cur; m != nil {
					by := 1
					if s[2] == 'A' {
						by = -1
					}
					move(m, by)
				}
				mu.Unlock()
			case strings.HasPrefix(s, "\x1b"):
				in.Next(1) // esc
				mu.Lock()
				if cur != nil {
					m := cur
					cur = nil
					m.done("(cancelled)")
				}
				mu.Unlock()
			case s[0] == '\r':
				in.Next(1)
				mu.Lock()
				if m := cur; m != nil {
					if time.Now().Before(m.deafTill) {
						mu.Unlock()
						continue
					}
					cur = nil
					m.done(m.labels[m.sel-1])
					mu.Unlock()
					continue
				}
				mu.Unlock()
				text := paste.String()
				paste.Reset()
				handle(text)
			default:
				in.Next(1)
			}
		}
	}
}
