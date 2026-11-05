package desktop

import (
	"fmt"
	"net/url"
	"strings"
)

// TerminalPath checks a terminal socket path the UI asks the window to relay: a daemon path
// under /api/ ending in /terminal. Only a sign-in terminal (/api/accounts/…) may carry a query,
// and only its account (?account=). It returns the path and that query.
func TerminalPath(path string) (string, url.Values, error) {
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/api/") || !strings.HasSuffix(u.Path, "/terminal") || strings.Contains(u.Path, "..") {
		return "", nil, fmt.Errorf("not a terminal: %q", path)
	}
	q := url.Values{}
	for k, v := range u.Query() {
		if k != "account" || len(v) != 1 || !strings.HasPrefix(u.Path, "/api/accounts/") {
			return "", nil, fmt.Errorf("not a terminal: %q", path)
		}
		q.Set(k, v[0])
	}
	return u.EscapedPath(), q, nil
}
