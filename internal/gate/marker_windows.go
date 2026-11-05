package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Both independent copies must say off to disable the marker. The registry
// survives resetting TMXG_HOME; the sibling DPAPI blob survives registry loss.
type markerRegistry interface {
	ReadEnabled(string) (uint64, error)
	WriteEnabled(string, uint64) error
}

type currentUserRegistry struct{}

func (currentUserRegistry) ReadEnabled(path string) (uint64, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Enabled")
	if errors.Is(err, registry.ErrNotExist) {
		return 0, errors.New("enrolment marker value missing")
	}
	return v, err
}

func (currentUserRegistry) WriteEnabled(path string, value uint64) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetQWordValue("Enabled", value)
}

type windowsMarker struct {
	key, file string
	registry  markerRegistry
}

func platformMarker(p config.Paths) Marker {
	home, err := filepath.Abs(p.Home)
	if err != nil {
		home = p.Home
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(home))))
	return windowsMarker{key: `Software\tmx-go\Gate\` + hex.EncodeToString(sum[:]), file: home + ".gate-enrolled", registry: currentUserRegistry{}}
}
func (m windowsMarker) Location() string { return `HKCU\` + m.key + " (Enabled), and " + m.file }
func (m windowsMarker) Absent() (bool, error) {
	_, err := m.registry.ReadEnabled(m.key)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	_, err = os.Lstat(m.file)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}
func (m windowsMarker) Read() (bool, error) {
	on := false
	v, err := m.registry.ReadEnabled(m.key)
	if err == nil {
		on = v != 0
	} else if !errors.Is(err, registry.ErrNotExist) {
		return true, err
	}
	b, err := os.ReadFile(m.file)
	if errors.Is(err, os.ErrNotExist) {
		return on, nil
	}
	if err != nil {
		return true, err
	}
	if len(b) == 0 {
		return true, errors.New("empty enrolment marker")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return true, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	value := string(unsafe.Slice(out.Data, out.Size))
	if value != "on" && value != "off" {
		return true, errors.New("invalid enrolment marker")
	}
	return on || value == "on", nil
}
func (m windowsMarker) Set(on bool) error {
	v := []byte("off")
	var flag uint64
	if on {
		v = []byte("on")
		flag = 1
	}
	in := windows.DataBlob{Size: uint32(len(v)), Data: &v[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	// Truncation/corruption is restrictive on the next read.
	if err := os.WriteFile(m.file, unsafe.Slice(out.Data, out.Size), 0600); err != nil {
		return err
	}
	return m.registry.WriteEnabled(m.key, flag)
}
