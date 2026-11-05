// tmx: one binary for the daemon, the CLI, the MCP server and the desktop app. See DESIGN.md.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/blackcode-ch/tmx-go/internal/cli"
	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/guardhook"
)

func main() {
	if name := codexguard.ShimName(os.Args[0]); name != "" {
		// Own/Workspace/State come from the pinned manifest guard carries in, never from
		// this shim's own environment: it is the agent's own child process and could
		// otherwise widen its boundary by setting TMXG_GUARD_* before calling pip/uv/etc.
		authorize := func(ctx context.Context, command string, args []string, cwd string, guard codexguard.Guard) error {
			return guardhook.AuthorizeExec(ctx, command, args, cwd, guardhook.FromManifest(guard.Own, guard.Workspace, guard.State))
		}
		// The manifest's location is derived from this shim's own executable path, never
		// from an environment variable the agent could redirect to its own manifest.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tmx: cannot determine the shim's own location")
			os.Exit(126)
		}
		manifestPath := codexguard.ManifestPathFromExecutable(exe)
		os.Exit(codexguard.RunShim(context.Background(), name, os.Args[1:], manifestPath, os.Stdin, os.Stdout, os.Stderr, authorize))
	}
	os.Exit(cli.Main(os.Args[1:]))
}
