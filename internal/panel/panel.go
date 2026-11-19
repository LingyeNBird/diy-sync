// Package panel embeds the management panel's web build.
//
// `cd web && pnpm build` writes it to internal/panel/dist; build the Go
// binaries after that. Without a build, the server serves a short page that
// says so.
package panel

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Files returns the panel's static files, rooted at dist.
func Files() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}

// Built reports whether the web build is embedded.
func Built() bool {
	_, err := fs.Stat(Files(), "index.html")
	return err == nil
}
