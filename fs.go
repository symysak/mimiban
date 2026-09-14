package main

import (
	"embed"
	"io/fs"
)

// fsSub returns the embedded web FS as-is (index.html at the root).
func fsSub(e embed.FS) (fs.FS, error) { return e, nil }
