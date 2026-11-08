package hiddenexec

import "golang.org/x/sys/windows"

// OpenURL invokes a registered GUI URL handler without a command interpreter or console.
// Callers must allow-list the URL before calling; this helper does not validate it.
// SW_SHOWNORMAL lets the requested application show its window.
func OpenURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
