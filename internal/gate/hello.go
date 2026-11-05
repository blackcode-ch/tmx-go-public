package gate

// Windows Hello's answers and what the gate makes of them. This file has no build tag on
// purpose: the mapping is pure Go and tested on every OS; auth_windows.go only fetches the codes.

import (
	"errors"
	"fmt"
	"strings"
)

// ErrDeclined means the OS prompt answered but did not confirm the owner (Windows Hello: too
// many attempts).
var ErrDeclined = errors.New("the owner was not confirmed")

// helloSettings is where a Windows user sets Windows Hello up.
const helloSettings = "Settings → Accounts → Sign-in options"

// Windows.Security.Credentials.UI.UserConsentVerifierAvailability and
// UserConsentVerificationResult (windows.security.credentials.ui.h, both int32): the codes 0-4
// mean the same in both enums.
const (
	helloAvailable         = 0 // Available / Verified
	helloDeviceNotPresent  = 1
	helloNotConfiguredUser = 2
	helloDisabledByPolicy  = 3
	helloDeviceBusy        = 4
	helloRetriesExhausted  = 5 // result only
	helloCanceled          = 6 // result only
	helloVerified          = helloAvailable
)

// helloAvailability maps CheckAvailabilityAsync's answer to Available's error (nil = the gate
// may ask). Anything but Available keeps the gate from being turned on.
func helloAvailability(code int32) error {
	switch code {
	case helloAvailable:
		return nil
	case helloDeviceNotPresent:
		return errors.New("Windows Hello is not set up on this PC (no PIN, face or fingerprint sign-in): " + helloSettings)
	case helloNotConfiguredUser:
		return errors.New("Windows Hello is not set up for this user: " + helloSettings)
	case helloDisabledByPolicy:
		return errors.New("Windows Hello is turned off by a policy on this PC (ask whoever manages it)")
	case helloDeviceBusy:
		return errors.New("Windows Hello is busy (another prompt is open); try again in a moment")
	}
	return fmt.Errorf("Windows Hello gave an unknown availability (%d)", code)
}

// helloVerdict maps RequestVerificationAsync's UserConsentVerificationResult to Authenticate's
// error. ctxErr is the prompt's context error: a prompt withdrawn because the context ended
// reports that instead of a cancel by the owner (as on macOS).
func helloVerdict(code int32, ctxErr error) error {
	switch code {
	case helloVerified:
		return nil
	case helloCanceled:
		if ctxErr != nil {
			return ctxErr
		}
		return ErrCancelled
	case helloRetriesExhausted:
		return fmt.Errorf("%w: Windows Hello stopped after too many attempts", ErrDeclined)
	case helloDeviceNotPresent, helloNotConfiguredUser, helloDisabledByPolicy, helloDeviceBusy:
		return fmt.Errorf("%w: %v", ErrUnavailable, helloAvailability(code))
	}
	return fmt.Errorf("Windows Hello gave an unknown result (%d)", code)
}

// helloPrompt is the message in the Windows Hello dialog. macOS adds "tmx is trying to" by
// itself; Windows shows the message as given. An HSTRING may hold NULs, a Go UTF-16 conversion
// may not, so they are dropped.
func helloPrompt(reason string) string {
	reason = strings.TrimSpace(strings.ReplaceAll(reason, "\x00", ""))
	if reason == "" {
		reason = "do something only you may do"
	}
	return "tmx is trying to " + strings.TrimSuffix(reason, ".") + "."
}
