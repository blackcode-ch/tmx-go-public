package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/discover"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func cmdHarnesses(e *env, args []string) error {
	if _, err := parse(newFlags("harnesses"), args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var list []harness.Status
		if err := c.Get(cx, "/api/harnesses", &list); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "HARNESS\tMODES\tINSTALLED\tPATH")
		for _, h := range list {
			modes := make([]string, len(h.Modes))
			for i, m := range h.Modes {
				modes[i] = string(m)
			}
			inst := "no (" + h.Install + ")"
			if h.Installed {
				inst = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, strings.Join(modes, ", "), inst, orDash(h.Path))
		}
		return tw.Flush()
	})
}

func cmdDiscover(e *env, args []string) error {
	fs := newFlags("discover")
	all := fs.Bool("all", false, "also list processes that cannot be adopted")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var res struct {
			Agents []discover.Agent `json:"agents"`
		}
		if err := c.Get(cx, "/api/discover", &res); err != nil {
			return err
		}
		shown, hidden := 0, 0
		for _, a := range res.Agents {
			if !a.Adoptable && !*all {
				hidden++
				continue
			}
			shown++
			started := "-"
			if a.StartedAt != 0 {
				started = time.UnixMilli(a.StartedAt).Format("Jan 2 15:04")
			}
			fmt.Fprintf(e.out, "%d  %s  %s  started %s\n", a.PID, a.Harness, orDash(a.Cwd), started)
			if !a.Adoptable {
				fmt.Fprintf(e.out, "    not adoptable: %s\n", a.Reason)
				continue
			}
			conv := a.ConversationID
			if conv == "" {
				conv = "?"
			}
			fmt.Fprintf(e.out, "    conversation %s (%s)\n", conv, a.ConversationSource)
			if a.ConversationID == "" {
				for _, cand := range a.Candidates {
					fmt.Fprintf(e.out, "      %s  %s  %s\n", cand.ID, time.UnixMilli(cand.Updated).Format("Jan 2 15:04"), truncate(cand.Preview, 70))
				}
			}
		}
		switch {
		case shown == 0 && hidden == 0:
			fmt.Fprintln(e.out, "no agents are running outside tmx")
		case shown == 0:
			fmt.Fprintf(e.out, "nothing to adopt (%d other agent processes; --all lists them)\n", hidden)
		case hidden > 0:
			fmt.Fprintf(e.out, "(%d more that cannot be adopted; --all lists them)\n", hidden)
		}
		return nil
	})
}

func cmdAdopt(e *env, args []string) error {
	fs := newFlags("adopt")
	name := fs.String("name", "", "agent name (default: <harness>-<folder>)")
	project := fs.String("project", "", "project slug or id")
	level := fs.String("level", "", "worker (default) or lead")
	conv := fs.String("conversation", "", "the conversation to resume, when discover could not tell")
	yes := fs.Bool("yes", false, "confirm: stop the process (SIGTERM) and resume it under tmx")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx adopt PID [--name N] [--project P] [--level worker|lead] [--conversation ID] --yes")
	}
	pid, err := strconv.Atoi(pos[0])
	if err != nil || pid <= 0 {
		return usageErr("%q is not a process id", pos[0])
	}
	req := map[string]any{"pid": pid, "name": *name, "project_id": *project, "level": model.Level(*level),
		"conversation_id": *conv, "confirm": *yes}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var a model.Agent
		if err := ownerDo(e, c, cx, "POST", "/api/adopt", req, &a); err != nil {
			if ae, ok := err.(*client.APIError); ok && !*yes && ae.Status == 400 && strings.Contains(ae.Msg, "repeat with confirm") {
				return usageErr("%s", strings.Replace(ae.Msg, "repeat with confirm", "repeat with --yes", 1))
			}
			return err
		}
		fmt.Fprintf(e.out, "%s: adopted pid %d, %s on %s in %s, conversation %s\n", a.Name, pid, a.State, orDash(a.Model), a.Cwd, orDash(a.SessionID))
		return nil
	})
}
