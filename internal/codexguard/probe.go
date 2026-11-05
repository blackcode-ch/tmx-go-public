package codexguard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// ProbeEffective inspects the pinned binary's merged configuration without a
// model turn. Terminal launches use this before opening their TUI; chat performs
// the same check on its existing app-server connection before thread/start.
func ProbeEffective(ctx context.Context, exe string, args, env []string, cwd, tmx string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := hiddenexec.Command(exe, args...)
	cmd.Env = env
	cmd.Dir = cwd
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { cmd.Process.Kill() })
	defer func() { stop(); cmd.Process.Kill(); cmd.Wait() }()
	encoder := json.NewEncoder(in)
	for _, packet := range []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "tmx-guard-config", "version": "1"}}},
		map[string]any{"method": "initialized"},
		map[string]any{"id": 2, "method": "config/read", "params": map[string]any{"includeLayers": true, "cwd": cwd}},
	} {
		if err := encoder.Encode(packet); err != nil {
			return err
		}
	}
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 4096), 2<<20)
	for scan.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scan.Bytes(), &response) == nil && response.ID == 2 {
			return ValidateEffective(response.Result, tmx)
		}
	}
	return fmt.Errorf("Codex effective configuration could not be verified; launch refused")
}
