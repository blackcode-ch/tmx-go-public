package transfers

import "strings"

// fixedOSPrefix classifies only exact, slash-delimited OS aliases. It deliberately
// uses slash paths so the Darwin layout can also be tested on Windows and Linux.
// No descendant is trusted merely because its owner happens to be root.
func fixedOSPrefix(goos, p string) (prefix, target string) {
	if goos == "darwin" {
		for _, root := range []string{"/var", "/tmp", "/etc"} {
			if p == root || strings.HasPrefix(p, root+"/") {
				return root, "/private" + root
			}
		}
	}
	return "", ""
}
