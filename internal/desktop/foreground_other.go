//go:build desktop && !windows

package desktop

// allowForeground: only Windows restricts which process may bring a window to the front.
func allowForeground() {}
