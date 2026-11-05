//go:build !desktop

// Package desktop is the Wails window of `tmx app`; this headless build has none.
package desktop

import "errors"

// Available reports whether this build has the desktop window.
const Available = false

// Options configures the window. URL is the daemon base URL, Token the owner token.
type Options struct {
	URL   string
	Token string
}

// Run reports that this build has no desktop app.
func Run(Options) error {
	return errors.New("this tmx build has no desktop app (build it with wails build)")
}
