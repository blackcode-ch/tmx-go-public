package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// ran is what a command printed and how it ended.
type ran struct {
	out      []byte // stdout
	errText  string // the end of stderr
	exitCode int    // -1 = did not run to the end
	err      error  // did not start, or timed out
}

// text is stdout, else stderr, trimmed.
func (r ran) text() string {
	if t := strings.TrimSpace(string(r.out)); t != "" {
		return t
	}
	return strings.TrimSpace(r.errText)
}

// env is the harnesses' environment: the daemon's, without a parent Claude session's
// variables, plus Options.Env.
func (s *Service) env(extra ...string) []string {
	return claude.MergeEnv(claude.BaseEnv(), append(append([]string{}, s.o.Env...), extra...)...)
}

// workDir is the neutral folder the harnesses run in (made again if it was removed).
func (s *Service) workDir() string {
	_ = os.MkdirAll(s.o.Dir, 0o700)
	return s.o.Dir
}

// scope is one account's view of the service: its harness runs with the account's home.
type scope struct {
	*Service
	home []string // the account's HomeEnv (nil = the harness's usual home)
}

// env is the account's harness environment: the service's, then the account's home.
func (s *scope) env(extra ...string) []string {
	return s.Service.env(append(append([]string{}, s.home...), extra...)...)
}

// run runs a harness command in the neutral folder, with no input, for at most timeout. Its
// output is kept in memory only and never logged.
func (s *scope) run(ctx context.Context, timeout time.Duration, path string, args ...string) ran {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := hiddenexec.CommandContext(ctx, path, args...)
	cmd.Dir = s.workDir()
	cmd.Env = s.env()
	stdout, stderr := &capped{max: 1 << 20}, &capped{max: 8 << 10, tail: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	setProcAttr(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	r := ran{exitCode: -1}
	if err := cmd.Start(); err != nil {
		r.err = fmt.Errorf("does not start: %w", err)
		return r
	}
	if runtime.GOOS == "windows" {
		sysinfo.RegisterChild(cmd.Process.Pid, false)
	}
	werr := cmd.Wait()
	reap(cmd)
	r.out, r.errText = stdout.Bytes(), stderr.String()
	if cmd.ProcessState != nil {
		r.exitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.err = fmt.Errorf("gave no answer within %s", timeout.Round(time.Second))
	case ctx.Err() != nil:
		r.err = ctx.Err()
	case werr != nil && r.exitCode < 0:
		r.err = werr
	}
	return r
}

// capped keeps at most max bytes: the first ones, or the last ones (tail). It may be read
// while the command still writes.
type capped struct {
	max  int
	tail bool
	mu   sync.Mutex
	b    bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if c.tail {
		c.b.Write(p)
		if over := c.b.Len() - c.max; over > 0 {
			c.b.Next(over)
		}
		return n, nil
	}
	if room := c.max - c.b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.b.Write(p)
	}
	return n, nil
}

func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.b.Bytes())
}

func (c *capped) String() string { return string(c.Bytes()) }

// firstLine is the first non-empty line of s, at most n runes.
func firstLine(s string, n int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clip(l, n)
		}
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
