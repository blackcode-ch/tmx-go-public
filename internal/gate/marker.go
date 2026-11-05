package gate

import (
	"errors"
	"os"
	"path/filepath"
)

// Marker survives loss of the gate state directory. Read errors fail closed.
// Set is called before gate.json is changed, so a partial write is restrictive.
type Marker interface {
	Read() (bool, error)
	// Absent distinguishes never enrolled from an explicitly disabled marker.
	Absent() (bool, error)
	Set(bool) error
	Location() string
}

// FileMarker is also used by tests, entirely inside their temporary directories.
type FileMarker string

func (m FileMarker) Location() string { return string(m) }
func (m FileMarker) Absent() (bool, error) {
	_, err := os.Lstat(string(m))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}
func (m FileMarker) Read() (bool, error) {
	b, err := os.ReadFile(string(m))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	switch string(b) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return true, errors.New("invalid enrolment marker")
}
func (m FileMarker) Set(on bool) error {
	if err := os.MkdirAll(filepath.Dir(string(m)), 0700); err != nil {
		return err
	}
	v := "off"
	if on {
		v = "on"
	}
	return os.WriteFile(string(m), []byte(v), 0600)
}
