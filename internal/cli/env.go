package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

// env is what a command runs with: its streams and, loaded on demand, the state dir.
type env struct {
	in        io.Reader
	out, errw io.Writer
	paths     config.Paths
	cfg       config.Config
	loaded    bool
}

func (e *env) load() error {
	if e.loaded {
		return nil
	}
	p, err := config.Resolve()
	if err != nil {
		return err
	}
	c, err := config.Load(p)
	if err != nil {
		return err
	}
	e.paths, e.cfg, e.loaded = p, c, true
	return nil
}

// baseURL is the running daemon's URL (its daemon.json port), else the configured one.
func (e *env) baseURL() string {
	port := e.cfg.Port
	if d, err := config.ReadDaemonInfo(e.paths); err == nil && d.Port > 0 {
		port = d.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// owner returns a client authenticated with the owner token.
func (e *env) owner() (*client.Client, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	tok, err := config.OwnerToken(e.paths)
	if err != nil {
		return nil, err
	}
	return client.New(e.baseURL(), tok), nil
}

// healthy reports whether a tmx daemon answers at url.
func healthy(url string) bool {
	c := client.New(url, "")
	c.HTTP.Timeout = 2 * time.Second
	var h struct {
		OK bool `json:"ok"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.Get(ctx, "/api/health", &h) == nil && h.OK
}

func (e *env) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(e.out, string(b))
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}
