package companion

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// DeepLink opens Companion at tmx's card in People & Agents with a "Connect tmx?" dialog.
// Companion connects only when the owner clicks Connect there: it mints a key with the owner's
// session and hands it to tmx itself (`<entry.command> companion connect --token-stdin --json`,
// with the entry's env), so the owner never copies a key.
const DeepLink = "aios://connect/tmx"

// Opener hands a link to the operating system, which opens the app registered for its scheme.
type Opener func(ctx context.Context, url string) error

// OpenURL uses the operating system's registered URL handler.
// A handler that keeps running (xdg-open may wait for the app) counts as opened.
func OpenURL(ctx context.Context, url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = hiddenexec.Command("/usr/bin/open", url)
	case "windows":
		return openWindowsURL(url)
	default:
		cmd = hiddenexec.Command("xdg-open", url)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not open %s: %v", url, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			why := strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
			if why == "" {
				why = err.Error()
			}
			return fmt.Errorf("%s (%s)", errNotOpened, short(why, 200))
		}
		return nil
	case <-t.C:
		return nil // still running: the handler took over
	case <-ctx.Done():
		return nil // handed over; the command goes on by itself
	}
}

const errNotOpened = "Companion did not open: no app on this computer opens Companion's links (aios://). Install or update Companion, then try again"

// OpenConnectResult is POST /api/companion/open-connect.
type OpenConnectResult struct {
	Installed bool   `json:"installed"`       // tmx's entry was (re)written into Companion now (it was missing or out of date)
	Opened    bool   `json:"opened"`          // the link went to the operating system
	Error     string `json:"error,omitempty"` // why it did not open
}

// OpenConnect makes sure tmx is installed into Companion (Companion finds tmx, and the binary it
// runs to hand over the key, in that entry), then opens DeepLink. An install that fails (no
// Companion folder, a foreign entry) is returned as the error; a link that does not open is
// reported in the result.
func OpenConnect(ctx context.Context, o InstallOptions, p config.Paths, open Opener) (OpenConnectResult, error) {
	var res OpenConnectResult
	st, err := ReadInstall(o)
	if err != nil {
		return res, err
	}
	tok, _ := config.ReadSecret(p.CompanionTokenFile())
	if !st.Installed || !st.Current || !st.Skill || tok == "" {
		if _, err := config.CompanionToken(p, true); err != nil {
			return res, err
		}
		if _, err := Install(o); err != nil {
			return res, err
		}
		res.Installed = true
	}
	if open == nil {
		open = OpenURL
	}
	if err := open(ctx, DeepLink); err != nil {
		res.Error = err.Error()
		return res, nil
	}
	res.Opened = true
	return res, nil
}

// OpenConnect is the daemon's: install if needed and open Companion's "Connect tmx?" dialog.
func (s *Service) OpenConnect(ctx context.Context) (OpenConnectResult, error) {
	return OpenConnect(ctx, s.o.Install, s.o.Paths, s.o.Open)
}
