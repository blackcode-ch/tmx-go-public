package harness

import (
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"strings"
	"testing"
)

func TestMCPServerNameDistinct(t *testing.T) {
	args := strings.Join(CodexMCPArgs(&runner.MCPServer{Command: "tmxg", Args: []string{"mcp"}}), " ")
	if !strings.Contains(args, "mcp_servers.tmxg.command=") || !strings.Contains(args, "mcp_servers.tmx.enabled=false") {
		t.Fatalf("Python MCP not isolated: %s", args)
	}
}
