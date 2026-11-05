package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/mcp"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const companionUsage = `usage:
  tmx companion status                     what is installed and connected; probes Companion (read-only)
  tmx companion install                    add tmx's MCP entry and skill to Companion (~/.aios)
  tmx companion uninstall                  remove them and revoke Companion's token
  tmx companion token --rotate             new token for Companion's link (the old one stops at once)
  tmx companion connect                    one click: Companion asks you "Connect tmx?" and, when you
                                           click Connect there, gives tmx a key of its own (installs
                                           tmx into Companion first if needed; waits up to 2 minutes)
  tmx companion connect --token-stdin [--json] [--url http://127.0.0.1:PORT]
                                           a key you have (--token-stdin: one line on stdin; --json:
                                           one JSON object on stdout, what Companion runs)
  tmx companion disconnect                 forget that key
  tmx companion tasks                      tasks and messages handed to Companion and their state
  tmx companion cancel ID                  cancel one
  tmx companion message [--reply-to ID] TEXT
                                           write to Companion in tmx's conversation with it (--reply-to: answer its question)`

// cmdMCPArgs: `tmx mcp` (agents) or `tmx mcp --companion` (Companion's link).
func cmdMCPArgs(e *env, args []string) error {
	fs := newFlags("mcp")
	comp := fs.Bool("companion", false, "Companion's read-only link (started by Companion)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *comp {
		return mcp.RunCompanion(cx)
	}
	return mcp.Run(cx)
}

func (e *env) installOptions() (companion.InstallOptions, error) {
	if err := e.load(); err != nil {
		return companion.InstallOptions{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return companion.InstallOptions{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return companion.InstallOptions{Executable: exe, URL: e.cfg.URL(), StateHome: e.paths.Home}, nil
}

func cmdCompanion(e *env, args []string) error {
	if err := e.load(); err != nil {
		return err
	}
	if !e.cfg.Companion.On() {
		return errors.New("Companion is disabled in the trial profile")
	}
	return companionCommand(e, args)
}

// companionCommand implements the protocol behind the profile gate.
func companionCommand(e *env, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		return companionStatus(e)
	case "install":
		o, err := e.installOptions()
		if err != nil {
			return err
		}
		if _, err := config.CompanionToken(e.paths, true); err != nil {
			return err
		}
		st, err := companion.Install(o)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Installed into Companion: %s\n  and the skill %s\n", st.MCPPath, st.SkillPath)
		fmt.Fprintln(e.out, "Companion picks it up on its next start (or reload its MCP servers). Companion can read the fleet, Needs you and chats, and message the head; nothing else.")
		return nil
	case "uninstall":
		o, err := e.installOptions()
		if err != nil {
			return err
		}
		st, err := companion.Uninstall(o)
		if err != nil {
			return err
		}
		if err := os.Remove(e.paths.CompanionTokenFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if st.Foreign || st.SkillForeign {
			fmt.Fprintln(e.out, "Removed tmx's own entries; an entry named tmx that tmx did not make was left alone.")
		} else {
			fmt.Fprintln(e.out, "Removed tmx from Companion and revoked its token.")
		}
		return nil
	case "token":
		fs := newFlags("companion token")
		rotate := fs.Bool("rotate", false, "make a new token")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		if !*rotate {
			return usageErr("usage: tmx companion token --rotate")
		}
		if err := e.load(); err != nil {
			return err
		}
		if _, err := config.RotateCompanionToken(e.paths); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "New token for Companion's link; the old one no longer works. Companion's tmx entry reads it itself: nothing to reinstall.")
		return nil
	case "connect":
		return companionConnect(e, args)
	case "disconnect":
		if err := e.load(); err != nil {
			return err
		}
		if err := companion.Disconnect(e.paths); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "Forgot Companion's key. Remove tmx in Companion too (People & Agents).")
		return nil
	case "tasks":
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var list []model.CompanionTask
			if err := c.Get(cx, "/api/companion/tasks?limit=50", &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(e.out, "No tasks handed to Companion yet.")
				return nil
			}
			tw := tabwriter.NewWriter(e.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tFROM\tEFFECTS\tAGE\tGOAL")
			for _, t := range list {
				what := t.Spec.Goal
				if t.IsMessage() {
					what = "message: " + t.Body
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.State, t.Origin, t.Effects,
					age(time.UnixMilli(t.CreatedAt)), shortLine(what, 60))
			}
			return tw.Flush()
		})
	case "message":
		fs := newFlags("companion message")
		replyTo := fs.String("reply-to", "", "the message or task Companion asked about: TEXT answers it")
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		text := strings.TrimSpace(strings.Join(pos, " "))
		if text == "" {
			return usageErr("usage: tmx companion message [--reply-to ID] TEXT")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var t model.CompanionTask
			if err := ownerDo(e, c, cx, http.MethodPost, "/api/companion/messages", companion.MessageRequest{Text: text, ReplyTo: *replyTo}, &t); err != nil {
				return err
			}
			if *replyTo != "" {
				fmt.Fprintf(e.out, "Answered Companion (message %s, now %s). Its reply shows in Settings → Companion.\n", t.ID, t.State)
				return nil
			}
			fmt.Fprintf(e.out, "Message %s for Companion: %s. Its reply shows in Settings → Companion (tmx companion tasks).\n", t.ID, t.State)
			return nil
		})
	case "cancel":
		if len(args) != 1 {
			return usageErr("usage: tmx companion cancel ID")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var t model.CompanionTask
			if err := c.Post(cx, "/api/companion/tasks/"+client.Seg(args[0])+"/cancel", nil, &t); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "Task %s: %s\n", t.ID, t.State)
			return nil
		})
	}
	return usageErr("%s", companionUsage)
}

const connectUsage = "usage: tmx companion connect [--token-stdin [--json]] [--url http://127.0.0.1:PORT]"

// What --token-stdin reads: one line of at most maxLine bytes, a key of at most maxKey
// (Companion's keys are 43 characters; companion.Connect refuses more than 512).
const (
	maxLine = 1024
	maxKey  = 512
)

// How long `tmx companion connect` (no key) waits for Companion to hand tmx its key, and how
// often it looks (tests shorten them).
var (
	connectWait = 2 * time.Minute
	connectPoll = time.Second
)

// openURL opens Companion's link when no daemon runs (tests replace it).
var openURL companion.Opener = companion.OpenURL

// connectJSON is the one object `connect --json` prints; Companion's "Connect tmx" reads it.
type connectJSON struct {
	OK    bool   `json:"ok"`
	Label string `json:"label,omitempty"`
	Error string `json:"error,omitempty"`
}

// companionConnect is `tmx companion connect`. With a key (--token-stdin as
// Companion runs it) it checks and stores it; without one it asks Companion to connect (the
// owner clicks Connect there) and waits for the key. The key is never printed or logged.
func companionConnect(e *env, args []string) error {
	fs := newFlags("companion connect")
	token := fs.String("token", "", "the agent key Companion made for tmx")
	fromStdin := fs.Bool("token-stdin", false, "read the key from stdin (one line)")
	asJSON := fs.Bool("json", false, `print one JSON object on stdout: {"ok":true,"label":...} or {"ok":false,"error":...}`)
	url := fs.String("url", "", "Companion's address (default: found the way Companion advertises it)")
	pos, err := parse(fs, args)
	if err == nil && len(pos) > 0 {
		err = usageErr("%s", connectUsage)
	}
	jsonOut := *asJSON || wantsJSON(args)
	fail := func(err error) error {
		msg := companion.KeySafeText(errText(err), *token, "Could not connect to Companion; diagnostic withheld because it contained key material")
		if !jsonOut {
			if msg != errText(err) {
				return errors.New(msg)
			}
			return err
		}
		_ = json.NewEncoder(e.out).Encode(connectJSON{Error: msg})
		return exitError{code: 1, msg: msg} // the sentence on stderr too, for people
	}
	if err != nil {
		// Flag parsing errors can quote arbitrary arguments, including credentials.
		return fail(usageErr("invalid connect arguments; %s", connectUsage))
	}
	switch {
	case *token != "":
		return fail(usageErr("--token is refused because process arguments expose keys; use --token-stdin"))
	case *fromStdin:
		key, err := readKey(e.in)
		if err != nil {
			return fail(err)
		}
		*token = key
	case *token == "" && jsonOut:
		return fail(usageErr("--json needs the key on stdin: tmx companion connect --token-stdin --json"))
	case *token == "" && *url != "":
		return fail(usageErr("--url goes with a key (--token-stdin); without one, Companion hands tmx its key itself"))
	case *token == "":
		return connectViaCompanion(e)
	}
	// Companion passes this environment variable from its installed MCP entry.
	// The owner CLI uses its local state/daemon.json, not TMXG_URL. Reject a
	// misleading remote entry rather than silently connecting a different target.
	if raw := os.Getenv(companion.EnvURL); raw != "" {
		if _, err := companion.CheckLoopback(raw); err != nil || strings.Contains(raw, "localhost") {
			return fail(usageErr("TMXG_URL for Companion connect must be http://127.0.0.1:PORT or http://[::1]:PORT; remote handover is not supported"))
		}
	}
	if err := e.load(); err != nil {
		return fail(err)
	}
	res, err := connectKey(e, *url, *token)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		// The protocol needs success, not a remote-controlled display name.
		return json.NewEncoder(e.out).Encode(connectJSON{OK: true, Label: companion.Name})
	}
	res.Label = companion.KeySafeText(res.Label, *token, companion.Name)
	res.Error = companion.KeySafeText(res.Error, *token, "Companion's diagnostic was withheld because it contained key material")
	res.Address.URL = companion.KeySafeText(res.Address.URL, *token, "local address withheld")
	return printConnect(e, res)
}

// wantsJSON: --json is among args (also when they do not parse, so that error is JSON too).
func wantsJSON(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "--json", "-json", "--json=true", "-json=true":
			return true
		}
	}
	return false
}

// errText is an error as one sentence for people (without the "tmx: " prefix).
func errText(err error) string {
	var ex exitError
	var ae *client.APIError
	switch {
	case errors.As(err, &ex):
		return ex.msg
	case errors.As(err, &ae):
		return ae.Msg
	}
	return err.Error()
}

// readKey reads the key from stdin: the first line (up to its newline or the end of input, so a
// caller that keeps stdin open does not hang it), trimmed.
func readKey(r io.Reader) (string, error) {
	if r == nil {
		r = strings.NewReader("")
	}
	line, err := bufio.NewReader(io.LimitReader(r, maxLine+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("could not read the key from stdin")
	}
	if len(line) > maxLine {
		return "", usageErr("that is not a key: more than %d bytes on one line", maxLine)
	}
	key := strings.TrimSpace(line)
	switch {
	case key == "":
		return "", usageErr("no key on stdin: give Companion's key for tmx as one line")
	case len(key) > maxKey:
		return "", usageErr("that is not a key: longer than %d characters", maxKey)
	}
	return key, nil
}

// connectKey checks the key with Companion and stores it: through the daemon when it runs
// (tasks waiting for a key go out at once), else here.
func connectKey(e *env, url, token string) (companion.ConnectResult, error) {
	if c, err := e.owner(); err == nil {
		c.HTTP = companion.NoRedirectClient(c.HTTP)
		cx, cancel := ctx()
		var st companion.Status
		err := c.Do(cx, http.MethodPut, "/api/companion/connect", map[string]string{"url": url, "token": token}, &st)
		cancel()
		var ae *client.APIError
		switch {
		case err == nil:
			return companion.ConnectResult{Address: st.Address, Reachable: st.KeyOK != nil && *st.KeyOK, Label: st.Label, Error: st.Error}, nil
		case errors.As(err, &ae):
			return companion.ConnectResult{}, err // the daemon (or Companion) refused it
		}
		// no daemon: below
	}
	cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return companion.Connect(cx, e.paths, url, token, nil, "tmx/cli")
}

// connectViaCompanion installs tmx into Companion if needed, opens Companion's "Connect tmx?"
// (aios://connect/tmx) on this machine, and waits for the key Companion hands tmx when the owner
// clicks Connect there (Companion runs `tmx companion connect --token-stdin --json`).
func connectViaCompanion(e *env) error {
	c, err := e.owner()
	if err != nil {
		return err
	}
	before := keySetAt(e, c)
	var res companion.OpenConnectResult
	cx, cancel := ctx()
	err = c.Do(cx, http.MethodPost, "/api/companion/open-connect", nil, &res)
	cancel()
	var ae *client.APIError
	switch {
	case err == nil:
	case errors.As(err, &ae):
		return err // Companion never ran here, or an entry named tmx that is not tmx's
	default: // no daemon: here
		c = nil
		o, err := e.installOptions()
		if err != nil {
			return err
		}
		cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err = companion.OpenConnect(cx, o, e.paths, openURL)
		cancel()
		if err != nil {
			return err
		}
	}
	if res.Installed {
		fmt.Fprintln(e.out, "Installed tmx into Companion.")
	}
	if !res.Opened {
		return errors.New(res.Error)
	}
	fmt.Fprintln(e.out, "Companion is asking you to connect tmx: click Connect there")
	deadline := time.Now().Add(connectWait)
	for time.Now().Before(deadline) {
		time.Sleep(connectPoll)
		if at := keySetAt(e, c); at != 0 && at != before {
			return reportConnected(e, c)
		}
	}
	return fmt.Errorf("Companion has not given tmx a key in %s. Click Connect next to tmx in Companion (People & Agents) and run "+
		"tmx companion connect again, or give a key yourself: tmx companion connect --token-stdin", connectWait.Round(time.Second))
}

// keySetAt is when the agent key tmx holds was stored (ms; 0 = none), from the daemon's status
// (c nil: no daemon) or the state folder. A new key changes it.
func keySetAt(e *env, c *client.Client) int64 {
	if c != nil {
		cx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var st companion.Status
		err := c.Get(cx, "/api/companion?probe=0", &st)
		cancel()
		if err == nil {
			return st.KeySetAt
		}
	}
	if fi, err := os.Stat(e.paths.CompanionAgentTokenFile()); err == nil {
		return fi.ModTime().UnixMilli()
	}
	return 0
}

// reportConnected prints what Companion says about the key it just handed tmx.
func reportConnected(e *env, c *client.Client) error {
	if c != nil {
		cx, cancel := ctx()
		var st companion.Status
		err := c.Get(cx, "/api/companion?probe=1", &st)
		cancel()
		if err == nil {
			if st.KeyOK != nil && !*st.KeyOK {
				return fmt.Errorf("Companion gave tmx a key but does not accept it now (%s); run tmx companion connect again", st.Error)
			}
			return printConnect(e, companion.ConnectResult{Address: st.Address, Reachable: st.KeyOK != nil && *st.KeyOK, Label: st.Label, Error: st.Error})
		}
	}
	fmt.Fprintln(e.out, "Connected: Companion gave tmx its key (tmx companion status checks it).")
	return nil
}

func printConnect(e *env, res companion.ConnectResult) error {
	if res.Reachable {
		fmt.Fprintf(e.out, "Connected: Companion at %s accepts the key (it knows tmx as %q).\n", res.Address.URL, res.Label)
		return nil
	}
	fmt.Fprintf(e.out, "Saved the key, but could not check it: %s\n", res.Error)
	fmt.Fprintf(e.out, "tmx looks for Companion at %s (%s) and checks the key when it sends a task.\n", res.Address.URL, res.Address.Source)
	return nil
}

func companionStatus(e *env) error {
	o, err := e.installOptions()
	if err != nil {
		return err
	}
	c, err := e.owner()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	var st companion.Status
	if err := c.Get(cx, "/api/companion?probe=1", &st); err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) {
			return err
		}
		// No daemon: what can be read locally.
		st.Install, _ = companion.ReadInstall(o)
		if t, _ := config.ReadSecret(e.paths.CompanionTokenFile()); t != "" {
			st.LinkToken = true
		}
		if t, _ := config.ReadSecret(e.paths.CompanionAgentTokenFile()); t != "" {
			st.Connected = true
		}
		fmt.Fprintln(e.out, "(the tmx daemon is not running: Companion was not checked)")
	}
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	in := st.Install
	fmt.Fprintln(e.out, "Companion → tmx (Companion reads tmx):")
	switch {
	case !in.CompanionDir:
		fmt.Fprintf(e.out, "  Companion is not set up for this user (%s missing)\n", in.AIOSHome)
	case in.Foreign:
		fmt.Fprintf(e.out, "  an entry named tmx that tmx did not make is at %s; left alone\n", in.MCPPath)
	case !in.Installed:
		fmt.Fprintln(e.out, "  not installed (tmx companion install)")
	default:
		fmt.Fprintf(e.out, "  installed: %s (current: %s, enabled in Companion: %s, skill: %s)\n", in.MCPPath, yes(in.Current), yes(in.Enabled), yes(in.Skill))
	}
	fmt.Fprintf(e.out, "  link token: %s\n", yes(st.LinkToken))
	fmt.Fprintln(e.out, "tmx → Companion (tmx hands Companion tasks):")
	fmt.Fprintf(e.out, "  agent key set: %s   address: %s (%s)\n", yes(st.Connected), st.Address.URL, st.Address.Source)
	if st.Checked {
		if st.Reachable {
			fmt.Fprintf(e.out, "  Companion answers (version %s)", st.Version)
			if st.KeyOK != nil {
				if *st.KeyOK {
					fmt.Fprintf(e.out, "; it knows tmx as %q", st.Label)
				} else {
					fmt.Fprint(e.out, "; it refused the key")
				}
			}
			fmt.Fprintln(e.out)
		} else {
			fmt.Fprintln(e.out, "  Companion is not reachable")
		}
		if st.Error != "" {
			fmt.Fprintf(e.out, "  %s\n", st.Error)
		}
	}
	if st.Active+st.Awaiting > 0 {
		fmt.Fprintf(e.out, "tasks: %d queued or running, %d waiting for your OK\n", st.Active, st.Awaiting)
	}
	return nil
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func shortLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
