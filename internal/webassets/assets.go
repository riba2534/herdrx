package webassets

import (
	"embed"
	"io/fs"
)

// The tracked .gitkeep permits CLI-only builds from a clean checkout.
// Build the website assets with make web-build before compiling the server.
//go:embed dist/*
var content embed.FS

func FS() fs.FS {
	assets, err := fs.Sub(content, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
