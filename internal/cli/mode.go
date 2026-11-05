package cli

import (
	"context"
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// tmx mode NAME [chat|terminal] [--start]: show or switch the mode an agent runs in (DESIGN.md
// "Switching live"). terminal is the harness's own screen (screen and tui say the same).
func cmdMode(e *env, args []string) error {
	fs := newFlags("mode")
	start := fs.Bool("start", false, "start the agent if it is not running")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageErr("usage: tmx mode NAME [chat|terminal] [--start]")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		path := "/api/agents/" + client.Seg(pos[0])
		if len(pos) == 1 {
			var a model.Agent
			if err := c.Get(cx, path, &a); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s: %s%s\n", a.Name, modeText(a), pendingText(a))
			return nil
		}
		to := model.Mode(pos[1])
		switch pos[1] {
		case "screen", "tui":
			to = model.ModeTerminal
		}
		var out fleet.ModeChange
		if err := c.Post(cx, path+"/mode", fleet.ModeRequest{Mode: to}, &out); err != nil {
			return err
		}
		fmt.Fprintln(e.out, out.Note)
		if *start && out.Agent.State != model.StateIdle && out.Agent.State != model.StateWorking &&
			out.Agent.State != model.StateWaiting && out.Agent.State != model.StateStarting {
			var a model.Agent
			if err := c.Post(cx, path+"/start", nil, &a); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s: %s\n", a.Name, a.State)
		}
		return nil
	})
}

func modeText(a model.Agent) string {
	s := string(a.Mode)
	if a.Mode == model.ModeTerminal && a.Harness != model.HarnessShell {
		s += " (its own screen)"
	}
	if a.HomeMode != "" {
		s += fmt.Sprintf(", switched from %s", a.HomeMode)
	}
	return s
}

func pendingText(a model.Agent) string {
	if a.PendingMode == "" {
		return ""
	}
	return fmt.Sprintf("; switches to %s when its turn ends", a.PendingMode)
}
