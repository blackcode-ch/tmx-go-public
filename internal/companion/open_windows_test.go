package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWindowsDispatchesCompanionLink(t *testing.T) {
	original := shellOpenURL
	t.Cleanup(func() { shellOpenURL = original })
	calls := 0
	shellOpenURL = func(url string) error {
		calls++
		if url != DeepLink {
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
	shellOpenURL = func(string) error { return errors.New("no handler") }
	if err := OpenURL(context.Background(), DeepLink); err == nil || !strings.Contains(err.Error(), "no handler") {
		t.Fatalf("missing handler: %v", err)
	}
}
