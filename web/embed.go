// Package web embeds the built dashboard. It sits next to the assets because
// an embed directive cannot reach outside its own directory.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build. The "all:" prefix keeps files starting with "."
// or "_", which bundlers sometimes emit.
//
//go:embed all:dashboard/dist
var dist embed.FS

// Dashboard returns the built dashboard. ok is false in a fresh checkout,
// where only the placeholder .gitkeep is present.
func Dashboard() (assets fs.FS, ok bool) {
	sub, err := fs.Sub(dist, "dashboard/dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
