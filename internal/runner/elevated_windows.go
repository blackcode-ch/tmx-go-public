package runner

import "golang.org/x/sys/windows"

func processElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
