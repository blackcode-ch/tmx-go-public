package sysinfo

import (
	"os"
	"testing"
)

// Only a session host's tmx copy anchors a hosted program (RegisterHosted).
func TestHostImage(t *testing.T) {
	for name, want := range map[string]bool{
		"tmx-host-0123abcd.exe":                       true,
		`C:\Users\a\AppData\tmx\bin\tmx-host-1.exe`:   true,
		"/home/a/.local/state/tmx-go/bin/tmx-host-ab": true,
		"TMX-HOST-AB.EXE":                             true,
		"tmx-go.exe":                                  false,
		"explorer.exe":                                false,
		"host":                                        false,
		"":                                            false,
	} {
		if got := hostImage(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

// A program that is not the claimed host's child, or a host that is not a tmx host copy, is
// never registered: an edited host.json gets no control over other processes.
func TestRegisterHostedRefusesStrangers(t *testing.T) {
	me := os.Getpid()
	RegisterHosted(me, os.Getppid(), 1, false) // this test's parent is not a tmx-host-* image
	children.Lock()
	_, ok := children.roots[me]
	children.Unlock()
	if ok {
		t.Fatal("registered a program under a host that is not a session host")
	}
	RegisterHosted(me, me, 1, false) // not its own parent
	children.Lock()
	_, ok = children.roots[me]
	children.Unlock()
	if ok {
		t.Fatal("registered a program as its own host")
	}
}
