// Package sidecar embeds the Node sidecar worker sources so the compiled amux
// binary can materialize them into the user's data dir at runtime, where SDK
// framework packages get npm-installed on demand.
package sidecar

import "embed"

// Files holds the sidecar worker sources (worker + adapters + package.json).
//
//go:embed worker.mjs package.json adapters/*.mjs
var Files embed.FS
