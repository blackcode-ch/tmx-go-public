//go:build desktop

// Package desktop is the Wails window of `tmx app`. The window is only a client: it loads the
// embedded UI (package ui) and the UI talks to the daemon at Options.URL with Options.Token.
//
// Transport: the UI first tries plain fetch/WebSocket from the wails:// origin to the daemon
// (the daemon allows the Wails origins in CORS). If that is blocked by the webview, the UI uses
// the bridge on App instead: Request proxies one HTTP call, StartEvents relays /api/events as
// "tmx:event" Wails events, and Terminal* relays a terminal WebSocket. See ui/src/lib/transport.
package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/ui"
)

// Options configures the window. URL is the daemon base URL (http://127.0.0.1:8787), Token the
// owner token.
// Available reports whether this build has the desktop window.
const Available = true

type Options struct {
	URL   string
	Token string
}

// Run opens the window and blocks until it is closed. Closing the window never stops agents.
func Run(opts Options) error {
	if !ui.Built() {
		return errors.New("this tmx build has no UI embedded (run pnpm build in ui/ first)")
	}
	opts.URL = strings.TrimRight(opts.URL, "/")
	app := &App{opts: opts, client: &http.Client{Timeout: 60 * time.Second}, terms: map[string]*termConn{}}
	dark := systemDark()
	bg := &options.RGBA{R: 255, G: 255, B: 255, A: 255} // --background (light)
	if dark {
		bg = &options.RGBA{R: 23, G: 23, B: 23, A: 255} // --background (dark), oklch(0.172 0 0)
	}
	return wails.Run(&options.App{
		Title:            "tmx",
		Width:            1380,
		Height:           880,
		MinWidth:         1100,
		MinHeight:        700,
		BackgroundColour: bg,
		AssetServer:      &assetserver.Options{Assets: ui.FS()},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.DefaultAppearance, // follows the system
			About:      &mac.AboutInfo{Title: "tmx", Message: "Runs a tree of coding agents on this machine."},
		},
		Windows: &windows.Options{Theme: windows.SystemDefault},
	})
}

// Config is what the UI needs to reach the daemon.
type Config struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Platform string `json:"platform"` // darwin | windows | linux
	Version  string `json:"version"`  // this window's build; after an update the daemon runs a newer one
}

// App is bound to the frontend as window.go.desktop.App.
type App struct {
	ctx    context.Context
	opts   Options
	client *http.Client

	mu       sync.Mutex
	evCancel context.CancelFunc
	terms    map[string]*termConn
	termSeq  int
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(context.Context) {
	a.nativeShutdown() // app_native.go
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.evCancel != nil {
		a.evCancel()
	}
	for _, t := range a.terms {
		t.cancel()
	}
	stopDeviceAudio() // devaudio.go (its own lock)
}

// Config returns the daemon URL and owner token.
func (a *App) Config() Config {
	return Config{URL: a.opts.URL, Token: a.opts.Token, Platform: goruntime.GOOS, Version: config.Version}
}

// OpenURL opens a link in the default browser (links in the window do not navigate away).
func (a *App) OpenURL(u string) {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		wruntime.BrowserOpenURL(a.ctx, u)
	}
}

// Relaunch opens the window again from the installed tmx and closes this one: after an update
// the daemon runs the new release while this window still shows the UI it started with.
func (a *App) Relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// Windows: the updated .exe took the old one's name; this process may now run as tmx.exe.old-<n>.
	if i := strings.LastIndex(exe, ".old-"); i > 0 {
		exe = exe[:i]
	}
	var cmd *exec.Cmd
	if i := strings.Index(exe, ".app/Contents/MacOS/"); goruntime.GOOS == "darwin" && i >= 0 {
		cmd = hiddenexec.CommandGUI("/usr/bin/open", "-n", exe[:i+len(".app")]) // a new instance of the bundle
	} else {
		cmd = hiddenexec.CommandGUI(exe)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start tmx again: %w", err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		wruntime.Quit(a.ctx)
	}()
	return nil
}

// AllowForeground lets the daemon bring the OS confirmation dialog (Windows Hello) to the
// front; the UI calls it when a confirmation starts. A no-op except on Windows.
func (a *App) AllowForeground() { allowForeground() }

// Request proxies one API call to the daemon (bridge transport). path must start with /api/.
// It returns the response body; a non-2xx status becomes an error carrying the daemon's
// {error} sentence.
func (a *App) Request(method, path, body string) (string, error) {
	return a.RequestWithSession(method, path, body, "", "")
}

// RequestWithSession carries only memory-held UI proof bindings; never URLs or disk.
func (a *App) RequestWithSession(method, path, body, session, grace string) (string, error) {
	if !strings.HasPrefix(path, "/api/") {
		return "", errors.New("only /api/ paths can be requested")
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(a.ctx, method, a.opts.URL+path, rd)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.opts.Token)
	req.Header.Set("X-Tmx-UI-Session", session)
	req.Header.Set("X-Tmx-Grace", grace)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the daemon is not reachable at %s", a.opts.URL)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// StartEvents relays the daemon's /api/events WebSocket as Wails events: every frame as
// "tmx:event" (the frame's JSON text) and the connection state as "tmx:events-state"
// ("open" | "closed"). It reconnects by itself; calling it again restarts the relay.
func (a *App) StartEvents() {
	a.mu.Lock()
	if a.evCancel != nil {
		a.evCancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.evCancel = cancel
	a.mu.Unlock()
	go a.relayEvents(ctx)
}

// StopEvents stops the relay started by StartEvents.
func (a *App) StopEvents() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.evCancel != nil {
		a.evCancel()
		a.evCancel = nil
	}
}

func (a *App) wsURL(path string, q url.Values) string {
	u := strings.Replace(a.opts.URL, "http", "ws", 1) + path
	if q == nil {
		q = url.Values{}
	}
	q.Set("token", a.opts.Token)
	return u + "?" + q.Encode()
}

func (a *App) relayEvents(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		conn, _, err := websocket.Dial(ctx, a.wsURL("/api/events", nil), nil)
		if err == nil {
			conn.SetReadLimit(64 << 20)
			wruntime.EventsEmit(a.ctx, "tmx:events-state", "open")
			backoff = 500 * time.Millisecond
			for {
				_, data, rerr := conn.Read(ctx)
				if rerr != nil {
					break
				}
				wruntime.EventsEmit(a.ctx, "tmx:event", string(data))
			}
			conn.CloseNow()
		}
		if ctx.Err() != nil {
			return
		}
		wruntime.EventsEmit(a.ctx, "tmx:events-state", "closed")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

type termConn struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

// TerminalOpen relays an agent's terminal WebSocket (bridge transport). Output arrives as
// "tmx:term:<handle>" events carrying base64 bytes; "tmx:term-closed:<handle>" when it ends.
func (a *App) TerminalOpen(agentID string, cols, rows int) (string, error) {
	return a.openTerminal("/api/agents/"+url.PathEscape(agentID)+"/terminal", cols, rows)
}

// TerminalOpenPath relays any of the daemon's terminal WebSockets by path: an agent's
// (/api/agents/{id}/terminal) or an AI account's sign-in (/api/accounts/{harness}/login/terminal).
func (a *App) TerminalOpenPath(path string, cols, rows int) (string, error) {
	p, q, err := TerminalPath(path)
	if err != nil {
		return "", err
	}
	return a.openTerminal(p, cols, rows, q)
}

func (a *App) openTerminal(path string, cols, rows int, extra ...url.Values) (string, error) {
	q := url.Values{}
	for _, e := range extra {
		for k, v := range e {
			q[k] = v
		}
	}
	q.Set("cols", fmt.Sprint(cols))
	q.Set("rows", fmt.Sprint(rows))
	ctx, cancel := context.WithCancel(a.ctx)
	conn, _, err := websocket.Dial(ctx, a.wsURL(path, q), nil)
	if err != nil {
		cancel()
		return "", fmt.Errorf("could not open the terminal: %w", err)
	}
	conn.SetReadLimit(16 << 20)
	a.mu.Lock()
	a.termSeq++
	h := fmt.Sprintf("t%d", a.termSeq)
	a.terms[h] = &termConn{conn: conn, cancel: cancel}
	a.mu.Unlock()
	go func() {
		defer func() {
			conn.CloseNow()
			a.mu.Lock()
			delete(a.terms, h)
			a.mu.Unlock()
			wruntime.EventsEmit(a.ctx, "tmx:term-closed:"+h)
		}()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			wruntime.EventsEmit(a.ctx, "tmx:term:"+h, base64.StdEncoding.EncodeToString(data))
		}
	}()
	return h, nil
}

// TerminalSend writes a text frame ({"type":"input",...} or {"type":"resize",...}).
func (a *App) TerminalSend(handle, frame string) error {
	a.mu.Lock()
	t := a.terms[handle]
	a.mu.Unlock()
	if t == nil {
		return errors.New("the terminal is closed")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageText, []byte(frame))
}

// TerminalClose closes a relayed terminal.
func (a *App) TerminalClose(handle string) {
	a.mu.Lock()
	t := a.terms[handle]
	a.mu.Unlock()
	if t != nil {
		t.cancel()
	}
}

// systemDark reports whether the OS is in dark mode, so the window's first frame matches the UI.
func systemDark() bool {
	if goruntime.GOOS != "darwin" {
		return true
	}
	out, err := hiddenexec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "dark")
}
