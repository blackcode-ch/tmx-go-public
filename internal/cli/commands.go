package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/mcp"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// withOwner runs fn with an owner client and a request context.
func withOwner(e *env, fn func(c *client.Client, cx context.Context) error) error {
	c, err := e.owner()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	if err := fn(c, cx); err != nil {
		if _, ok := err.(*client.APIError); !ok && !healthy(c.BaseURL) {
			return fmt.Errorf("the tmx daemon is not running at %s (start it with: tmx up)", c.BaseURL)
		}
		return err
	}
	return nil
}

func cmdVersion(e *env, args []string) error {
	fs := newFlags("version")
	gateMethod := fs.Bool("gate", false, "print this binary's owner-gate authenticator instead (\"simulated\" = a gatesim test build)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *gateMethod {
		fmt.Fprintln(e.out, gate.OS().Method())
		return nil
	}
	fmt.Fprintln(e.out, "tmx "+config.Version)
	return nil
}

func cmdLs(e *env, args []string) error {
	if _, err := parse(newFlags("ls"), args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var snap model.Snapshot
		if err := c.Get(cx, "/api/snapshot", &snap); err != nil {
			return err
		}
		fmt.Fprintln(e.out, mcp.FormatTree(snap))
		return nil
	})
}

func cmdProject(e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("usage: tmx project new|ls|rm|import ...")
	}
	switch args[0] {
	case "import":
		return cmdProjectImport(e, args[1:])
	case "new":
		fs := newFlags("project new")
		folder := fs.String("folder", "", "project folder (default <workspace>/<slug>)")
		brief := fs.String("brief", "", "what the project is for")
		harness := fs.String("harness", "", "lead harness: claude (default) or codex")
		mdl := fs.String("model", "", "lead model (default: profile default)")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) == 0 {
			return usageErr("usage: tmx project new NAME [--folder DIR] [--brief TEXT] [--harness claude|codex] [--model M]")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var p model.Project
			req := map[string]string{"name": strings.Join(pos, " "), "folder": *folder, "brief": *brief, "harness": *harness, "model": *mdl}
			if err := c.Post(cx, "/api/projects", req, &p); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "project %s (%s) created in %s; lead %s-lead started\n", p.Name, p.Slug, p.Folder, p.Slug)
			return nil
		})
	case "ls":
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var snap model.Snapshot
			if err := c.Get(cx, "/api/snapshot", &snap); err != nil {
				return err
			}
			names := map[string]string{}
			for _, a := range snap.Agents {
				names[a.ID] = a.Name
			}
			tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SLUG\tNAME\tSTATUS\tLEAD\tFOLDER")
			for _, p := range snap.Projects {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Slug, p.Name, p.Status, orDash(names[p.LeadID]), p.Folder)
			}
			return tw.Flush()
		})
	case "rm":
		fs := newFlags("project rm")
		yes := fs.Bool("yes", false, "confirm")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("usage: tmx project rm NAME --yes")
		}
		if !*yes {
			return usageErr("this stops and removes every agent of %s (the folder stays); repeat with --yes", pos[0])
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			if err := ownerDo(e, c, cx, http.MethodDelete, "/api/projects/"+client.Seg(pos[0]), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "project %s removed (folder kept)\n", pos[0])
			return nil
		})
	}
	return usageErr("unknown project command %q (new, ls, rm, import)", args[0])
}

func cmdRun(e *env, args []string) error {
	fs := newFlags("run")
	project := fs.String("project", "", "project slug or id")
	level := fs.String("level", "", "worker (default) or lead; implied by --profile")
	profile := fs.String("profile", "", "agent profile (default: the built-in profile of the level)")
	mdl := fs.String("model", "", "model (default: the profile's)")
	mode := fs.String("mode", "", "chat or terminal")
	harness := fs.String("harness", "", "claude, codex, opencode, pi or shell")
	account := fs.String("account", "", "the harness account it runs as (default: the harness's default for new agents)")
	prompt := fs.String("prompt", "", "first message")
	role := fs.String("role", "", "short role")
	brief := fs.String("brief", "", "standing brief")
	cwd := fs.String("cwd", "", "working folder")
	noWait := fs.Bool("no-wait", false, "over the machine's limits: refuse instead of queueing")
	force := fs.Bool("force", false, "start even over the machine's limits")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx run NAME --project P [--profile P | --level worker] [--model M] [--mode M] [--harness H] [--account A] [--prompt TEXT]")
	}
	req := map[string]string{"name": pos[0], "project_id": *project, "level": *level, "profile": *profile, "model": *mdl,
		"mode": *mode, "harness": *harness, "account": *account, "prompt": *prompt, "role": *role, "brief": *brief, "cwd": *cwd}
	body := map[string]any{"no_wait": *noWait, "force": *force}
	for k, v := range req {
		if v != "" {
			body[k] = v
		}
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var a model.Agent
		if err := c.Post(cx, "/api/agents", body, &a); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s: %s on %s in %s, %s\n", a.Name, a.Level, orDash(a.Model), a.Cwd, strings.TrimPrefix(agentLine(a), a.Name+": "))
		if a.Error != "" {
			return fmt.Errorf("%s: %s", a.Name, a.Error)
		}
		return nil
	})
}

func cmdSend(e *env, args []string) error {
	const usage = "usage: tmx send NAME [-f FILE]... TEXT..."
	if len(args) < 2 {
		return usageErr(usage)
	}
	// Only -f/--file is an option; any other word (even one starting with "-") is text.
	var files, words []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			words = append(words, args[i+1:]...)
			i = len(args)
		case a == "-f" || a == "--file":
			if i+1 >= len(args) {
				return usageErr(usage)
			}
			files = append(files, args[i+1])
			i++
		case strings.HasPrefix(a, "--file="):
			files = append(files, strings.TrimPrefix(a, "--file="))
		default:
			words = append(words, a)
		}
	}
	if len(words) == 0 && len(files) == 0 {
		return usageErr(usage)
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ids := make([]string, 0, len(files))
		for _, path := range files {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var up struct {
				ID string `json:"id"`
			}
			body := map[string]string{"name": filepath.Base(path), "data": base64.StdEncoding.EncodeToString(b)}
			if err := c.Post(cx, "/api/agents/"+client.Seg(args[0])+"/uploads", body, &up); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			ids = append(ids, up.ID)
		}
		var m model.Message
		req := map[string]any{"text": strings.Join(words, " "), "uploads": ids}
		if err := c.Post(cx, "/api/agents/"+client.Seg(args[0])+"/send", req, &m); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "message %s to %s: %s\n", m.ID, args[0], m.State)
		return nil
	})
}

func cmdAction(action string) cmdFunc {
	return func(e *env, args []string) error {
		pos, err := parse(newFlags(action), args)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("usage: tmx %s NAME", action)
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var a model.Agent
			if err := c.Post(cx, "/api/agents/"+client.Seg(pos[0])+"/"+action, nil, &a); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s: %s\n", a.Name, a.State)
			return nil
		})
	}
}

func cmdRm(e *env, args []string) error {
	pos, err := parse(newFlags("rm"), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx rm NAME")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		if err := c.Do(cx, http.MethodDelete, "/api/agents/"+client.Seg(pos[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s removed\n", pos[0])
		return nil
	})
}

func cmdApprovals(e *env, args []string) error {
	if _, err := parse(newFlags("approvals"), args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var snap model.Snapshot
		if err := c.Get(cx, "/api/snapshot", &snap); err != nil {
			return err
		}
		if len(snap.Approvals) == 0 {
			fmt.Fprintln(e.out, "nothing is waiting for you")
			return nil
		}
		names := map[string]string{}
		for _, a := range snap.Agents {
			names[a.ID] = a.Name
		}
		for _, ap := range snap.Approvals {
			who := orDash(names[ap.AgentID])
			if ap.Kind == model.ApprovalBrake {
				fmt.Fprintf(e.out, "%s  %s was paused: %s  [tmx approve %s | tmx approve %s raise]\n", ap.ID, who, ap.Description, ap.ID, ap.ID)
				continue
			}
			if ap.Kind == model.ApprovalQuestion {
				fmt.Fprintf(e.out, "%s  question from %s: %s", ap.ID, who, ap.Question)
				if len(ap.Options) > 0 {
					fmt.Fprintf(e.out, "  [%s]", strings.Join(ap.Options, " | "))
				}
				fmt.Fprintln(e.out)
				continue
			}
			if ap.Kind == model.ApprovalGuard {
				var g model.GuardHold
				_ = json.Unmarshal(ap.Input, &g)
				fmt.Fprintf(e.out, "%s  held by the guard for %s (%s): %s\n", ap.ID, who, g.RuleName, truncate(g.Command, 300))
				continue
			}
			detail := ap.Description
			if detail == "" {
				detail = string(ap.Input)
			}
			fmt.Fprintf(e.out, "%s  %s wants %s: %s\n", ap.ID, who, ap.Tool, truncate(detail, 300))
		}
		return nil
	})
}

func cmdApprove(e *env, args []string) error {
	if len(args) < 1 {
		return usageErr("usage: tmx approve ID [ANSWER...]")
	}
	req := map[string]string{"decision": "allow"}
	if len(args) > 1 {
		req = map[string]string{"decision": "answer", "answer": strings.Join(args[1:], " ")}
	}
	return decide(e, args[0], req)
}

func cmdDeny(e *env, args []string) error {
	fs := newFlags("deny")
	msg := fs.String("message", "", "why (shown to the agent)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx deny ID [--message TEXT]")
	}
	return decide(e, pos[0], map[string]string{"decision": "deny", "message": *msg})
}

func decide(e *env, id string, req map[string]string) error {
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var ap model.Approval
		if err := ownerDo(e, c, cx, http.MethodPost, "/api/approvals/"+client.Seg(id), req, &ap); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s: %s\n", ap.ID, ap.State)
		return nil
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
