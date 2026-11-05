package gate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// This fake is the only registry implementation used by marker tests. No test
// opens HKCU, even when testing the real DPAPI sibling in a temporary directory.
type fakeMarkerRegistry struct {
	value             uint64
	missing           bool
	readErr, writeErr error
}

func TestWindowsMarkerAbsenceRequiresBothCopiesMissing(t *testing.T) {
	r := &fakeMarkerRegistry{missing: true}
	m := windowsMarker{key: "test-only", file: filepath.Join(t.TempDir(), "marker"), registry: r}
	if absent, err := m.Absent(); !absent || err != nil {
		t.Fatal("clean absent marker not recognized")
	}
	if err := m.Set(false); err != nil {
		t.Fatal(err)
	}
	if absent, err := m.Absent(); absent || err != nil {
		t.Fatal("disabled registry/blob mistaken for absent")
	}
	r.missing = true
	if absent, err := m.Absent(); absent || err != nil {
		t.Fatal("remaining blob mistaken for absent")
	}
	if err := os.Remove(m.file); err != nil {
		t.Fatal(err)
	}
	r.missing = false
	if absent, err := m.Absent(); absent || err != nil {
		t.Fatal("remaining registry mistaken for absent")
	}
	r.readErr = errors.New("denied")
	if absent, err := m.Absent(); absent || err == nil {
		t.Fatal("read error mistaken for absent")
	}
}

func (r *fakeMarkerRegistry) ReadEnabled(string) (uint64, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	if r.missing {
		return 0, registry.ErrNotExist
	}
	return r.value, nil
}
func (r *fakeMarkerRegistry) WriteEnabled(_ string, v uint64) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.value, r.missing = v, false
	return nil
}

func TestWindowsMarkerIndependentCopies(t *testing.T) {
	r := &fakeMarkerRegistry{missing: true}
	m := windowsMarker{key: "test-only", file: filepath.Join(t.TempDir(), "marker"), registry: r}
	if err := m.Set(true); err != nil {
		t.Fatal(err)
	}
	r.missing = true
	if on, err := m.Read(); !on || err != nil {
		t.Fatalf("registry loss: %v %v", on, err)
	}
	r.missing, r.value = false, 1
	if err := os.Remove(m.file); err != nil {
		t.Fatal(err)
	}
	if on, err := m.Read(); !on || err != nil {
		t.Fatalf("blob loss: %v %v", on, err)
	}
	if err := m.Set(false); err != nil {
		t.Fatal(err)
	}
	if on, err := m.Read(); on || err != nil {
		t.Fatalf("explicit off: %v %v", on, err)
	}
	r.readErr = errors.New("denied")
	if on, err := m.Read(); !on || err == nil {
		t.Fatal("registry read failure did not close gate")
	}
	r.readErr = nil
	if err := os.WriteFile(m.file, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if on, err := m.Read(); !on || err == nil {
		t.Fatal("corrupt DPAPI blob did not close gate")
	}
	r.writeErr = errors.New("denied")
	if err := m.Set(true); err == nil {
		t.Fatal("registry write failure hidden")
	}
	r.writeErr = nil
	if on, err := m.Read(); !on || err != nil {
		t.Fatalf("partial enable must remain restrictive: %v %v", on, err)
	}
}
