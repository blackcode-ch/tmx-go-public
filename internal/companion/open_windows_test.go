package companion

import (
	"context"
	"strings"
	"testing"
)

func TestWindowsDoesNotDispatchURLs(t *testing.T) {
	for _, url := range []string{DeepLink, "https://example.invalid", "file:///document.pdf"} {
		err := OpenURL(context.Background(), url)
		if err == nil || !strings.Contains(err.Error(), "open Companion manually") {
			t.Fatalf("%q: %v", url, err)
		}
	}
}
