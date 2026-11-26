// Package ui embeds the built React dashboard (npm run build → dist/app) so
// the `flyke` binary can serve it. The control plane only receives an fs.FS;
// it does not depend on this package.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the dashboard's static files, or nil when the UI was not built
// (go build without `npm run build` first).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist/app")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
