//go:build windows

package sysinfo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDisksWindowsCountersMonotonic(t *testing.T) {
	a, err := Disks()
	if errors.Is(err, ErrUnsupported) {
		// The documented fallback: only when no drive answers IOCTL_DISK_PERFORMANCE.
		if a != nil {
			t.Fatalf("unsupported but returned %v", a)
		}
		if perfAnswers(0) {
			t.Fatal("PhysicalDrive0 answers IOCTL_DISK_PERFORMANCE but Disks reported unsupported")
		}
		return
	}
	if err != nil || len(a) == 0 {
		t.Fatalf("Disks() = %v, %v", a, err)
	}
	// Some I/O between the reads; counters may stay equal but must never go back.
	f := filepath.Join(t.TempDir(), "io")
	if err := os.WriteFile(f, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Disks()
	if err != nil {
		t.Fatal(err)
	}
	prev := map[string]Disk{}
	for _, d := range a {
		if d.Name == "" || d.IOTime < 0 {
			t.Fatalf("implausible disk %+v", d)
		}
		prev[d.Name] = d
	}
	for _, d := range b {
		p, ok := prev[d.Name]
		if !ok {
			continue
		}
		if d.IOTime < p.IOTime || d.Written < p.Written {
			t.Fatalf("%s counters went back: %+v then %+v", d.Name, p, d)
		}
	}
}

func perfAnswers(i int) bool {
	path, _ := windows.UTF16PtrFromString(`\\.\PhysicalDrive` + string(rune('0'+i)))
	h, err := windows.CreateFile(path, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var dp diskPerformance
	var n uint32
	return windows.DeviceIoControl(h, ioctlDiskPerformance, nil, 0, (*byte)(unsafe.Pointer(&dp)), uint32(unsafe.Sizeof(dp)), &n, nil) == nil
}
