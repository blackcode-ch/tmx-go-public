//go:build !darwin

package transfers

func canonicalOSPrefix(p string) (string, error) { return p, nil }
