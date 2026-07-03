//go:build !embedweb

// Package webdist stub: compiled when the embedweb tag is absent. Returns no
// embedded filesystem so the server serves its built-in placeholder.
package webdist

import "io/fs"

// FS reports that no embedded WebUI is available in this build.
func FS() (fs.FS, bool) { return nil, false }
