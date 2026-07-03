//go:build embedweb

// Package webdist embeds the built React WebUI (web/dist). It is only compiled
// when the `embedweb` build tag is set, which release builds use after running
// the Vite build. Without the tag, the server falls back to a placeholder page
// so `go build ./...` works without a frontend build.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded dist filesystem rooted at dist/.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	return sub, true
}
