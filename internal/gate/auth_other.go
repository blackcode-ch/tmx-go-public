//go:build !gatesim && !windows && (!darwin || !cgo)

package gate

import (
	"context"
	"errors"
	"runtime"
)

// OS returns this machine's authenticator. Only macOS (Touch ID / password, with cgo) and
// Windows (Windows Hello) have one in this build (passkeys are planned; DESIGN.md "Safety"),
// so the gate cannot be turned on elsewhere.
func OS() Authenticator { return unavailable{} }

type unavailable struct{}

func (unavailable) Method() string { return "none" }
func (unavailable) Label() string  { return "not available" }
func (unavailable) Available() error {
	return errors.New("owner confirmation is built only for macOS and Windows so far (this is " + runtime.GOOS + ")")
}
func (unavailable) Authenticate(context.Context, string) error { return ErrUnavailable }
