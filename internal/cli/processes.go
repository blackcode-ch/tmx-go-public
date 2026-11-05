package cli

import (
	"context"
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

func cmdProcesses(e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("usage: tmx processes list AGENT | system | start AGENT PURPOSE COMMAND [ARG...] | stop ID | promote ID PROJECT OWNER")
	}
	method, path := "GET", ""
	var body any
	switch args[0] {
	case "list":
		if len(args) == 1 {
			path = "/api/processes"
			break
		}
		if len(args) != 2 {
			return usageErr("usage: tmx processes list [AGENT]")
		}
		path = "/api/agents/" + client.Seg(args[1]) + "/processes"
	case "system":
		if len(args) != 1 {
			return usageErr("usage: tmx processes system")
		}
		path = "/api/system/tools"
	case "start":
		if len(args) < 4 {
			return usageErr("usage: tmx processes start AGENT PURPOSE COMMAND [ARG...]")
		}
		method = "POST"
		path = "/api/agents/" + client.Seg(args[1]) + "/processes"
		body = fleet.ProcessRequest{Purpose: args[2], Command: args[3], Args: args[4:]}
	case "stop":
		if len(args) != 2 {
			return usageErr("usage: tmx processes stop ID")
		}
		method = "POST"
		path = "/api/processes/" + client.Seg(args[1]) + "/stop"
	case "promote":
		if len(args) != 4 {
			return usageErr("usage: tmx processes promote ID PROJECT OWNER")
		}
		method = "POST"
		path = "/api/processes/" + client.Seg(args[1]) + "/promote"
		body = map[string]string{"project_id": args[2], "owner_id": args[3]}
	default:
		return usageErr("unknown processes command %q", args[0])
	}
	return withOwner(e, func(c *client.Client, ctx context.Context) error {
		var out any
		if err := ownerDo(e, c, ctx, method, path, body, &out); err != nil {
			return err
		}
		return json.NewEncoder(e.out).Encode(out)
	})
}
