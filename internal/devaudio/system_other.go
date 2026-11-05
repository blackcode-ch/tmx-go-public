//go:build !windows || !(amd64 || arm64)

package devaudio

// Supported reports whether this build has a native device-audio source.
const Supported = false

// System is the device's own sound: none on this platform.
func System() (Source, error) { return nil, ErrUnsupported }
