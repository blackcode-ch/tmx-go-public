package companion

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"strings"
	"testing"
)

func TestWindowsDispatchesCompanionLink(t *testing.T) {
	original := shellExecute
	t.Cleanup(func() { shellExecute = original })
	calls := 0
	shellExecute = func(hwnd windows.Handle, verb, file, args, cwd *uint16, show int32) error {
		calls++
		if hwnd != 0 || windows.UTF16PtrToString(verb) != "open" || windows.UTF16PtrToString(file) != DeepLink || args != nil || cwd != nil {
			t.Fatal("unexpected shell arguments")
		}
		return nil
	}
	if err := OpenURL(context.Background(), DeepLink); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"https://example.invalid", "file:///document.pdf", "aios://connect/tmx&calc"} {
		if err := OpenURL(context.Background(), url); err == nil {
			t.Fatalf("opened %q", url)
		}
	}
	if calls != 1 {
		t.Fatalf("shell calls: %d", calls)
	}
	shellExecute = func(windows.Handle, *uint16, *uint16, *uint16, *uint16, int32) error { return errors.New("no handler") }
	if err := OpenURL(context.Background(), DeepLink); err == nil || !strings.Contains(err.Error(), "no handler") {
		t.Fatalf("missing handler: %v", err)
	}
}
