// Package ui embeds the built web UI (ui/dist, from `pnpm build`). The daemon serves it at /
// and the desktop app loads it into its window. Before the first build dist holds only .keep.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether a real build (index.html) is embedded.
func Built() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}
