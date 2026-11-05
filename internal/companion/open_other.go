//go:build !windows

package companion

import "errors"

func openWindowsURL(string) error { return errors.New("Windows URL handler unavailable") }
