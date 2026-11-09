package cli

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

type daemonStartWriter struct{ started chan struct{} }

func (w daemonStartWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "tmx daemon started") {
		select {
		case w.started <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func TestDaemonStartsWithBrokenInspectionOwnerState(t *testing.T) {
	for _, kind := range []string{"corrupt", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			t.Setenv(config.EnvPort, "")
			paths, err := config.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(paths)
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Port = ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			cfg.Profile = config.ProfileProduction
			cfg.Workspace = t.TempDir()
			// The test executable cannot serve as a tmx session host. Force the
			// supported in-process fallback instead of copying/launching it.
			if err := os.WriteFile(filepath.Join(paths.Home, "bin"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			// No pair token: the peer client cannot contact this synthetic peer.
			cfg.Peers = []config.Peer{{Name: "fixture", URL: "https://fixture.invalid"}}
			cfg.ClaudeCommand = filepath.Join(paths.Home, "nonexistent-harness.exe")
			cfg.SignInCommands = map[string]string{"codex": cfg.ClaudeCommand, "antigravity": cfg.ClaudeCommand}
			if err := os.MkdirAll(paths.SecretsDir(), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt" {
				err = os.WriteFile(paths.P2PStateFile(), []byte(`{broken`), 0600)
			} else {
				err = os.Mkdir(paths.P2PStateFile(), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			token, err := config.OwnerToken(paths)
			if err != nil {
				t.Fatal(err)
			}
			oldLog := slog.Default()
			defer slog.SetDefault(oldLog)
			started := make(chan struct{}, 1)
			done := make(chan error, 1)
			e := &env{paths: paths, cfg: cfg, loaded: true, out: io.Discard, errw: daemonStartWriter{started}}
			go func() { done <- cmdDaemon(e, nil) }()
			c := client.New(cfg.URL(), token)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := c.Do(ctx, "POST", "/api/shutdown", nil, nil); err != nil {
					t.Errorf("shutdown: %v", err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("daemon exit: %v", err)
					}
				case <-time.After(40 * time.Second):
					t.Error("daemon did not stop")
				}
			}()
			select {
			case <-started:
			case err := <-done:
				done <- err
				t.Fatalf("daemon failed before startup: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("daemon did not start")
			}
			if !healthy(cfg.URL()) {
				t.Fatal("daemon is not healthy")
			}
			b, err := os.ReadFile(paths.DaemonLog())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "level=WARN msg=\"inspection owner names: using configured names and generic defaults\"") {
				t.Fatal("owner-name fallback warning missing")
			}
		})
	}
}
