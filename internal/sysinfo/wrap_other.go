//go:build !darwin

package sysinfo

func wrapLow(path string, args []string) (string, []string) { return path, args }
