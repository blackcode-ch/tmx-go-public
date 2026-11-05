package companion

import (
	"net/http"
	"strings"
	"time"
)

// NoRedirectClient copies the client so a caller's shared client is not changed.
// A local recipient must never redirect a credential (including a PUT body) to
// another recipient, even another port on this machine.
func NoRedirectClient(client *http.Client) *http.Client {
	c := http.Client{Timeout: 20 * time.Second}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// KeySafeText withholds the whole sentence if it includes the key or any
// fragment of four bytes. Call after JSON decoding, before truncating. This is
// a defence against accidental/error-body echoes, not a covert-channel filter.
func KeySafeText(text, key, fallback string) string {
	if key == "" {
		return text
	}
	n := min(4, len(key))
	for i := 0; i+n <= len(key); i++ {
		if strings.Contains(text, key[i:i+n]) {
			return fallback
		}
	}
	return text
}
