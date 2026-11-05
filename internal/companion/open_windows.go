package companion

import (
	"fmt"
	"golang.org/x/sys/windows"
)

var shellExecute = windows.ShellExecute

// Use ShellExecute directly: no command interpreter and no console window.
func openWindowsURL(url string) error {
	if url != DeepLink {
		return fmt.Errorf("only Companion's connect link can be opened")
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	if err := shellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("%s: %w", errNotOpened, err)
	}
	return nil
}
