// Package web embeds the built React GUI (web/dist) into the Go binary so
// `go build` produces a single-file distribution. The dev workflow uses Vite
// (`npm run dev`) and proxies /api to the Go server — the embedded bundle is
// only consulted in production builds.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distRoot embed.FS

// DistFS returns the embedded dist/ directory rooted so that "index.html"
// resolves at the FS root (i.e. dist/index.html is served as /index.html).
func DistFS() fs.FS {
	sub, err := fs.Sub(distRoot, "dist")
	if err != nil {
		// Impossible: the embed directive above guarantees dist/ exists.
		panic(err)
	}
	return sub
}
